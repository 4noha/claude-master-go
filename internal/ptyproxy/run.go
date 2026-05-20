package ptyproxy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/diag"
)

// ProxyOpts は RunProxy の依存注入。subcommand は os.Stdin(raw)/os.Stdout/
// 実 winsize/SIGWINCH を渡し、テストは pipe/buffer/関数を渡す（M5c と
// 同じく実キーパスを実 socket で検証可能にするため）。
type ProxyOpts struct {
	Argv     []string                // ラップする claude argv（先頭=実バイナリ）
	Cfg      *config.Config          // nil なら config.Load()
	HostIn   io.Reader               // host stdin（nil 可）
	HostOut  io.Writer               // host stdout（nil 可）
	SockPath string                  // "" なら SessionsDir/<pid>.sock
	WinSize  func() (cols, rows int) // host 端末サイズ（nil=80x24）
	Sigwinch <-chan os.Signal        // リサイズ通知（nil 可）
	Version  string                  // claude-master バイナリ版（status.json の cm_version。"" で無効）
	// Counters は接続 client 数を server.go から atomic に書き込ませる
	// ため diag.Counters を渡す（nil 可・テスト等）。これにより snap の
	// connected_clients/last_disconnect が真値で更新され、IdleGCSweep の
	// 真の閉じ忘れ判定が成立。runProxy 呼出側で生成→注入。
	Counters *diag.Counters
	// Ctx は致命シグナル等で外側から **proxy を綺麗に畳む**ための取消。
	// nil なら従来挙動（無視）。非 nil で Done が来たら PTY master を
	// 閉じる→子 claude に EOF/HUP→claude 終了→`p.Wait()` 復帰→
	// 既存 defer（sock/status クリーンアップ）が走る。VSCode 親死亡
	// （SIGHUP）の cleanup 漏れ＝stale sock/status の根治路。
	Ctx context.Context
}

// RunProxy は claude を PTY ラップして起動し、host stdout + unix socket
// 多重化で中継、host stdin を HandleHostInput へ、SIGWINCH を PTY/host
// サイズへ反映し、子終了まで待って終了コードを返す。Python pty_proxy
// run()/_loop の中核（既定 SIZE_POLICY=client のミニ tmux 経路）。
// 使用量ヒューリスティック status / limit auto-pause は M5e（未移植）。
func RunProxy(o ProxyOpts) (int, error) {
	cfg := o.Cfg
	if cfg == nil {
		cfg = config.Load()
	}
	cols, rows := 80, 24
	if o.WinSize != nil {
		if c, r := o.WinSize(); c > 0 && r > 0 {
			cols, rows = c, r
		}
	}
	p, err := Start(o.Argv, cols, rows) // 子は execv 前に slave へ pty.Setsize（レースフリー）
	if err != nil {
		return 1, err
	}
	defer p.Close()

	srv := NewServer(p, cfg, o.HostOut, cols, rows)
	srv.cmVersion = o.Version // status.json の cm_version（per-proxy 版・旧 inode 検出用）
	srv.SetCounters(o.Counters) // nil-safe; subscribe/unsubscribe で atomic 更新

	sockPath := o.SockPath
	if sockPath == "" {
		if err := os.MkdirAll(cfg.SessionsDir, 0o755); err != nil {
			return 1, fmt.Errorf("sessions dir: %w", err)
		}
		sockPath = filepath.Join(cfg.SessionsDir,
			fmt.Sprintf("%d.sock", p.Pid()))
	}
	_ = os.Remove(sockPath) // 前回の残骸（stale socket）を除去
	if err := srv.Serve(sockPath); err != nil {
		return 1, fmt.Errorf("serve %s: %w", sockPath, err)
	}
	statusPath := filepath.Join(cfg.SessionsDir,
		fmt.Sprintf("%d.status.json", p.Pid()))
	srv.EnableStatus(statusPath, p.Pid()) // 使用量 status（M5e）
	defer func() {
		srv.Stop()
		_ = os.Remove(sockPath)
		_ = os.Remove(statusPath)
	}()

	// host stdin → HandleHostInput（nav/pagekey/wheel/通常転送）
	if o.HostIn != nil {
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := o.HostIn.Read(buf)
				if n > 0 {
					srv.HandleHostInput(append([]byte{}, buf[:n]...))
				}
				if err != nil {
					return
				}
			}
		}()
	}

	// SIGWINCH → PTY サイズ追従（client policy）＋ host 再描画
	if o.Sigwinch != nil {
		go func() {
			for range o.Sigwinch {
				c, r := cols, rows
				if o.WinSize != nil {
					if nc, nr := o.WinSize(); nc > 0 && nr > 0 {
						c, r = nc, nr
					}
				}
				_ = p.Setsize(c, r) // 子へ SIGWINCH → claude 再描画
				srv.SetHostSize(c, r)
			}
		}()
	}

	// 外側 ctx cancel で master を閉じ、claude を綺麗に終了させる経路。
	// 既存 defer（sock/status クリーンアップ）を確実に走らせるための
	// soft-kill。重複 Close は p.Close 側で no-op（defer 上の二重 Close OK）。
	if o.Ctx != nil {
		go func() {
			<-o.Ctx.Done()
			p.Close() // master/tty 閉鎖（HUP 経路）
			// 子が setsid 等で SIGHUP を確実に受けない実装もあるため
			// 短い grace 後に強制終了（cross-platform: unix SIGKILL /
			// windows TerminateProcess）。Pid 既終了は no-op。
			if pid := p.Pid(); pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					_ = proc.Kill()
				}
			}
		}()
	}

	// 子 claude 終了まで待つ。終了で master EOF→masterPump が Stop。
	waitErr := p.Wait()
	<-srv.Done() // masterPump 完了（最終 broadcast / session log finalize）を待つ
	code := 0
	if waitErr != nil {
		if ee, ok := waitErr.(interface{ ExitCode() int }); ok {
			code = ee.ExitCode()
		} else {
			code = 1
		}
	}
	return code, nil
}
