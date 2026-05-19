//go:build windows

package ptyproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/UserExistsError/conpty"
)

// winBackend は Windows ConPTY 実体（UserExistsError/conpty。生
// x/sys/windows 直叩きは子が pseudoconsole へ attach せず脆いと PoC で
// 実証＝検証済ライブラリ採用。DESIGN_M8 参照）。
//
// 重要（unix との意味論差）: unix PTY は子終了で master が EOF するが、
// ConPTY は pseudoconsole が出力 write 側を保持するため Close()
// （ClosePseudoConsole）まで Read が返らない。コードベースは「子終了→
// master EOF→masterPump 終了→srv.Done」を前提にする（run.go）。よって
// Start 時に「cpty.Wait（子終了）→ Close()」を駆動する内部 goroutine を
// 立て、unix と同じ「子終了で master が閉じる」意味論を Windows でも
// 成立させる。
type winBackend struct {
	cpty      *conpty.ConPty
	exited    chan struct{} // 子終了で close
	code      int
	werr      error
	closeOnce sync.Once
	tearing   atomic.Bool // close 開始後は Read エラー＝正常終了扱い
}

// startBackend(windows): argv を Windows コマンドライン（os/exec と同じ
// syscall.EscapeArg 規約）へ整形し ConPTY 配下に起動。
func startBackend(argv []string, cols, rows int) (ptyBackend, error) {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = syscall.EscapeArg(a)
	}
	cpty, err := conpty.Start(strings.Join(parts, " "),
		conpty.ConPtyDimensions(cols, rows))
	if err != nil {
		return nil, err
	}
	b := &winBackend{cpty: cpty, exited: make(chan struct{})}
	// 子終了監視→pseudoconsole 閉鎖（master Read を EOF させ unix 同等の
	// 「子終了で master が閉じる」を成立）。
	go func() {
		c, e := cpty.Wait(context.Background())
		b.code, b.werr = int(c), e
		close(b.exited)
		b.shutdown()
	}()
	return b, nil
}

func (b *winBackend) shutdown() {
	b.closeOnce.Do(func() {
		b.tearing.Store(true)
		_ = b.cpty.Close()
	})
}

func (b *winBackend) Master() io.ReadWriteCloser { return b.cpty }

// Setsize: ConPTY は (width,height)。cols=width / rows=height。
func (b *winBackend) Setsize(cols, rows int) error {
	return b.cpty.Resize(cols, rows)
}

// Wait: 子終了まで block（unix cmd.Wait 同等。run.go は Wait 後に
// srv.Done を待つ）。非ゼロ終了は ExitCode() を運ぶ err にする
// （run.go の `interface{ ExitCode() int }` 判定に一致）。
func (b *winBackend) Wait() error {
	<-b.exited
	if b.werr != nil {
		return b.werr
	}
	if b.code == 0 {
		return nil
	}
	return exitCodeErr{b.code}
}

func (b *winBackend) Pid() int { return b.cpty.Pid() }

func (b *winBackend) Close() { b.shutdown() }

// closedErr: teardown 開始後（子終了 or 明示 Close）の Read エラーは
// 全て「master が閉じた」正常終了扱い（io.EOF は PumpToVT 側で別途）。
func (b *winBackend) closedErr(err error) bool {
	if b.tearing.Load() {
		return true
	}
	return errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}

// exitCodeErr は非ゼロ終了コードを run.go へ伝える（*exec.ExitError の
// ExitCode() に相当する windows 側の担い手）。
type exitCodeErr struct{ code int }

func (e exitCodeErr) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e exitCodeErr) ExitCode() int { return e.code }
