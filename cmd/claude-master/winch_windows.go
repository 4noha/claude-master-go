//go:build windows

package main

import "os"

// notifyWinch (windows): SIGWINCH は無い。nil チャネルを返すと
// ptyproxy.RunProxy は Sigwinch 経路を無効化する（コンソールリサイズ
// →PTY 追従は M8c）。
func notifyWinch() (chan os.Signal, func()) {
	return nil, func() {}
}
