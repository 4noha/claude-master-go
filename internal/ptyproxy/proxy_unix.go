//go:build !windows

package ptyproxy

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// unixBackend は creack/pty fork+exec 実体。挙動は M8b 前の proxy.go と
// バイト同一（darwin/linux parity 厳守）。
type unixBackend struct {
	cmd    *exec.Cmd
	master *os.File
	tty    *os.File
}

// startBackend(unix): slave tty を raw 化（OPOST/ONLCR/echo 無効）し子
// 出力を verbatim 中継。子は execv 前に slave へ pty.Setsize（レース
// フリー）。
func startBackend(argv []string, cols, rows int) (ptyBackend, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, err
	}
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
	cmd.SysProcAttr = childSysProcAttr()
	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		return nil, err
	}
	return &unixBackend{cmd: cmd, master: ptmx, tty: tty}, nil
}

func (b *unixBackend) Master() io.ReadWriteCloser { return b.master }

func (b *unixBackend) Setsize(cols, rows int) error {
	return pty.Setsize(b.master, &pty.Winsize{
		Rows: uint16(rows), Cols: uint16(cols),
	})
}

func (b *unixBackend) Wait() error { return b.cmd.Wait() }

func (b *unixBackend) Pid() int {
	if b.cmd != nil && b.cmd.Process != nil {
		return b.cmd.Process.Pid
	}
	return 0
}

func (b *unixBackend) Close() {
	if b.master != nil {
		_ = b.master.Close()
	}
	if b.tty != nil {
		_ = b.tty.Close()
	}
}

// closedErr: 子終了後に master を読むと OS により EIO になる。EOF 同等。
func (b *unixBackend) closedErr(err error) bool {
	var perr *os.PathError
	if errors.As(err, &perr) {
		return errors.Is(perr.Err, syscall.EIO)
	}
	return errors.Is(err, syscall.EIO)
}
