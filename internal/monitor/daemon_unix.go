//go:build !windows

package monitor

import "syscall"

// detachSysProcAttr は親が死んでも生存する独立セッションを作る
// （Python subprocess.Popen(start_new_session=True) 相当＝端末シグナル
// 非受信のデーモン）。
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
