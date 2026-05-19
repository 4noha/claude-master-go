//go:build !windows

package ptyproxy

import "syscall"

// childSysProcAttr は claude 子プロセスを新セッション＋制御端末付きで
// 起動する SysProcAttr（Python pty_proxy と同一: 端末シグナル独立・
// スレーブ tty を制御端末化）。
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true}
}
