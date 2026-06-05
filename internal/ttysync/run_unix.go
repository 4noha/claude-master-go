//go:build !windows

package ttysync

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// Opts は WrapStdio の調整パラメータ。
type Opts struct {
	// IdleMs は idle 検出時間 (default 4)。短すぎると batch 効果が出ず、
	// 長すぎると typing echo latency が体感される。4ms は人間知覚閾値
	// 以下で frame burst の境界検出には十分。
	IdleMs int

	// HoldAfterDestructiveMs は ANSI parser で destructive op
	// (\x1b[2J 等の画面クリア系) を検出した直後だけ idle を extended
	// に切替える時間 (default 32)。これにより「画面クリア → redraw」
	// 区間が同一 batch に集約され blackout が visible にならない。
	// 0 で hold mode 無効。
	HoldAfterDestructiveMs int
}

// WrapStdio は argv を子プロセスとして PTY 経由で起動し、
//   - host stdin → 子 PTY (passthrough, batch なし＝typing 即時)
//   - 子 PTY → host stdout (idle batch で flicker 軽減)
//
// する。SIGWINCH は子 PTY に伝搬。子終了で正常 return。raw mode は host
// 側にかける (子は自分で raw 化する想定＝tmux はその通り)。
//
// 既存 ptyproxy の startBackend と同じ creack/pty pattern。
func WrapStdio(argv []string, opts Opts) error {
	if len(argv) == 0 {
		return errors.New("ttysync: empty argv")
	}
	idle := time.Duration(opts.IdleMs) * time.Millisecond
	if idle <= 0 {
		idle = 4 * time.Millisecond
	}
	hold := time.Duration(opts.HoldAfterDestructiveMs) * time.Millisecond
	if opts.HoldAfterDestructiveMs == 0 {
		// 既定 32ms = 60Hz 約 2 tick・blackout→redraw 区間カバーに十分
		hold = 32 * time.Millisecond
	} else if opts.HoldAfterDestructiveMs < 0 {
		hold = 0 // 明示無効化
	}

	// 1) PTY 確保
	ptmx, tty, err := pty.Open()
	if err != nil {
		return err
	}
	defer ptmx.Close()

	// 2) 初期サイズを host から
	if ws, err := pty.GetsizeFull(os.Stdin); err == nil {
		_ = pty.Setsize(ptmx, ws)
	}

	// 3) 子プロセス spawn (子の stdio = slave tty)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}
	if err := cmd.Start(); err != nil {
		_ = tty.Close()
		return err
	}
	// 子が tty を保持＝親は close して良い (close しないと EOF 検出不可)
	_ = tty.Close()

	// 4) host stdin を raw 化 (子側 = tmux も raw を期待)
	if old, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		defer term.Restore(int(os.Stdin.Fd()), old)
	}

	// 5) SIGWINCH → resize 子 PTY (既存 resize_unix.go と同 pattern)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			if ws, err := pty.GetsizeFull(os.Stdin); err == nil {
				_ = pty.Setsize(ptmx, ws)
			}
		}
	}()

	// 6) stdin → ptmx (即時 passthrough・typing latency 増やさない)
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		_, _ = io.Copy(ptmx, os.Stdin)
	}()

	// 7) ptmx → stdout (idle batch・flicker 軽減の核心)
	pumpDone := make(chan error, 1)
	go func() {
		pumpDone <- PumpWithIdleConfig(os.Stdout, ptmx,
			PumpConfig{Idle: idle, HoldAfterDestructive: hold},
			RealClock{})
	}()

	// 8) 子終了待ち
	waitErr := cmd.Wait()
	// ptmx を閉じて pump goroutine の Read を unblock
	_ = ptmx.Close()
	<-pumpDone

	// 終了 status を error として伝搬 (tmux 等は exit 0 が多いが)
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			// exit 0 でなければ exit code を OS に返したい呼び出し元向け
			return ee
		}
		return waitErr
	}
	return nil
}
