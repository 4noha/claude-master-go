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
