//go:build windows

package monitor

import "syscall"

// STILL_ACTIVE（GetExitCodeProcess が稼働中プロセスに返す値）。
const stillActive = 259

// procAlive は OpenProcess + GetExitCodeProcess==STILL_ACTIVE で生存
// 確認（unix の kill(pid,0) 相当）。ハンドルが開けない/終了済は false。
func procAlive(pid int) bool {
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

// procTerminate は TerminateProcess（unix の SIGTERM 相当。graceful な
// CTRL_BREAK 送出は M8e で検討）。プロセス無しは非 nil を返し、CmdStop
// 既存挙動（非 nil→PID ファイル掃除）と互換。
func procTerminate(pid int) error {
	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	return syscall.TerminateProcess(h, 1)
}
