//go:build !windows

package monitor

import "syscall"

// procAlive は signal 0 送出で生存確認（Python os.kill(pid, 0) と同一）。
func procAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// procTerminate は SIGTERM 送出（Python os.kill(pid, SIGTERM) と同一。
// 非 nil は「プロセス無し」を含む＝CmdStop は非 nil で PID ファイル
// 掃除へ）。
func procTerminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
