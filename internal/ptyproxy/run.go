package ptyproxy

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/4noha/claude-master-go/internal/config"
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
	defer func() { srv.Stop(); _ = os.Remove(sockPath) }()

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
