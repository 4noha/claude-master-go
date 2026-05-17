//go:build windows

package ptyproxy

import "syscall"

// childSysProcAttr (windows): ConPTY 配下での子起動は M8b で実装する。
// M8a はビルドを通すための最小実装（PTY 自体は creack/pty の
// unsupported スタブにより Start が ErrUnsupported を返す＝実 PTY は
// M8b ConPTY バックエンドで提供）。
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}
