//go:build windows

package monitor

import "syscall"

// Windows のデタッチ起動: 端末から切り離し（DETACHED_PROCESS）かつ
// 新プロセスグループ（コンソール Ctrl イベントを親から受けない）＝
// macOS launchd / unix setsid デーモンに相当。
const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
}
