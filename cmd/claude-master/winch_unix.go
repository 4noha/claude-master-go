//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyWinch は SIGWINCH 通知チャネルと解除関数を返す（runProxy が
// PTY/host サイズ追従に使う。Python と同一トリガ）。
func notifyWinch() (chan os.Signal, func()) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGWINCH)
	return c, func() { signal.Stop(c) }
}
