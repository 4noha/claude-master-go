//go:build windows

package diag

import "syscall"

// sendSIGTERM (Windows): POSIX signal 非対応のため OpenProcess+
// TerminateProcess で代替。proxy 側は SIGINT/SIGTERM の signal.Notify
// を NotifyFatal で持つが Windows ではプロセス強制終了に近く defer は
// 動かない可能性あり＝**v0.2.1+ で run.go の defer 内 cleanup が proxy
// 終了直前まで効くことに依存**（M8c の挙動同等）。
func sendSIGTERM(pid int) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	return syscall.TerminateProcess(h, 1)
}
