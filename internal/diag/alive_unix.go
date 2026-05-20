//go:build !windows

package diag

import (
	"os"
	"syscall"
)

// isAlive: kill -0 同等の POSIX 経路（os.FindProcess は unix で必ず成功
// するが Signal(0) は ESRCH で死亡判定可能）。permission denied (EPERM)
// は alive 扱い＝**他ユーザの同 PID には触らない安全動作**。
func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM = プロセス存在するが権限不足→alive 扱い（保守的に消さない）
	return err == syscall.EPERM
}
