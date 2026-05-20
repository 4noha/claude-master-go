//go:build windows

package diag

import "syscall"

// STILL_ACTIVE は GetExitCodeProcess が返す「まだ走っている」コード。
const stillActive = 259

// isAlive: Windows は POSIX signal 非対応のため OpenProcess+
// GetExitCodeProcess で生死判定。OpenProcess が失敗すれば不在/権限不足
// →保守的に false（消去対象）。コード 259 = STILL_ACTIVE のみ alive。
func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
