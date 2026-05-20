//go:build windows

package diag

import (
	"os"
	"os/signal"
	"syscall"
)

// Windows には SIGHUP 概念が無い（Console-Ctrl は os.Interrupt として
// 来る・SIGTERM は taskkill /F 以外で来る経路がある）。catch 可能な
// 致命相当として Interrupt / SIGTERM のみ登録する。Unix 版は別ファイル。
func FatalSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

func NotifyFatal(ch chan os.Signal) { signal.Notify(ch, FatalSignals()...) }

// Windows には SIGUSR1 が無い（Go signal も非対応）。NotifyNonFatal は
// no-op で API 互換のみ維持（Windows では SIGUSR1 経由の生検は使えない・
// 将来 RPC 経路で代替）。
func NonFatalSignals() []os.Signal { return nil }
func NotifyNonFatal(_ chan os.Signal) { /* no-op on windows */ }
