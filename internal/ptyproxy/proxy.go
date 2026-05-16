// Package ptyproxy は claude を PTY 配下で fork+exec し、出力を忠実
// VT モデルへ流し、host stdin/stdout と unix socket(複数 client)を
// 多重化する（Python pty_proxy.py の移植・M3）。
//
// Slice 1（本ファイル現状）: fork+exec + master 読取 + VT 投入 +
// winsize。host/socket 多重化と per-client 描画は後続スライス。
package ptyproxy

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/4noha/claude-master-go/internal/screen"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// Proxy は claude プロセスと忠実 VT モデルを保持する。
type Proxy struct {
	cmd    *exec.Cmd
	master *os.File
	tty    *os.File
	VT     *screen.VT
	cols   int
	rows   int
}

// Start は argv のコマンドを (cols,rows) の PTY 配下で起動する。
// slave tty を raw 化（OPOST/ONLCR/echo を無効）し、子の出力を
// **verbatim** に master へ流す（claude 自身が raw にするのと同義。
// 録画リプレイ検証でもバイト忠実）。
func Start(argv []string, cols, rows int) (*Proxy, error) {
	if len(argv) == 0 {
		return nil, errors.New("ptyproxy: 空 argv")
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, err
	}
	// slave を raw に（子出力の post-processing を止めて忠実中継）
	if _, e := term.MakeRaw(int(tty.Fd())); e != nil {
		// 失敗しても致命ではない（環境により）。続行
		_ = e
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{
		Rows: uint16(rows), Cols: uint16(cols),
	}); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		return nil, err
	}
	return &Proxy{
		cmd: cmd, master: ptmx, tty: tty,
		VT:   screen.NewModel(cols, rows),
		cols: cols, rows: rows,
	}, nil
}

// Setsize は実行中 PTY と VT モデルのサイズを変更する（M3 後続で
// host/client リサイズ経路から呼ぶ）。
func (p *Proxy) Setsize(cols, rows int) error {
	p.cols, p.rows = cols, rows
	return pty.Setsize(p.master, &pty.Winsize{
		Rows: uint16(rows), Cols: uint16(cols),
	})
}

// PumpToVT は master を任意チャンクで読み、忠実 VT へ流す。子が終了し
// master が閉じる（EOF / EIO）まで。Slice 1 検証用の最小ループ。
// 後続スライスで host/clients への per-client 描画を足す。
func (p *Proxy) PumpToVT() error {
	buf := make([]byte, 4096)
	for {
		n, err := p.master.Read(buf)
		if n > 0 {
			p.VT.Feed(buf[:n])
		}
		if err != nil {
			if err == io.EOF || isPtyClosed(err) {
				return nil
			}
			return err
		}
	}
}

// Wait は子プロセス終了を待つ。
func (p *Proxy) Wait() error { return p.cmd.Wait() }

// Pid は子 claude プロセス PID（未起動/テスト構築時は 0）。
func (p *Proxy) Pid() int {
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

// Close はリソース解放。
func (p *Proxy) Close() {
	if p.master != nil {
		_ = p.master.Close()
	}
	if p.tty != nil {
		_ = p.tty.Close()
	}
}

// isPtyClosed: 子終了後に master を読むと OS により EIO になる。EOF 同等。
func isPtyClosed(err error) bool {
	var perr *os.PathError
	if errors.As(err, &perr) {
		return errors.Is(perr.Err, syscall.EIO)
	}
	return errors.Is(err, syscall.EIO)
}
