//go:build !windows

package diag

import "syscall"

// sendSIGTERM: POSIX kill(SIGTERM) で proxy に graceful exit 要請。
// proxy 内部の signal handler (NotifyFatal) が WriteDump→proxyCancel→
// defer cleanup を走らせて exit。会話 jsonl は残るので再 attach 可能。
func sendSIGTERM(pid int) error {
	if pid <= 0 {
		return syscall.ESRCH
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
