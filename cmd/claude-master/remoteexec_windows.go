//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// 遠隔命令の実行系（windows）。unix の launchd/SIGTERM/setsid を Windows
// 等価へ。常駐は install.ps1 が登録する **S4U スケジュールタスク**
// `claude-master-monitor` / `claude-master-cloud`（実機構）。kill/detach
// は branch の monitor.procTerminate / detachSysProcAttr と同規約。

const (
	detachedProcess       = 0x00000008 // DETACHED_PROCESS
	createNewProcessGroup = 0x00000200 // CREATE_NEW_PROCESS_GROUP
)

// restartDaemons は S4U タスク 2 本を End→Run で再起動（unix の
// launchctl kickstart -k 相当。monitor 先・cloud 後＝自己再起動順）。
func restartDaemons() error {
	for _, tn := range []string{"claude-master-monitor", "claude-master-cloud"} {
		_ = exec.Command("schtasks", "/End", "/TN", tn).Run()
		if err := exec.Command("schtasks", "/Run", "/TN", tn).Run(); err != nil {
			return fmt.Errorf("schtasks /Run %s: %w", tn, err)
		}
	}
	return nil
}

// killProxy は TerminateProcess（Windows は graceful SIGTERM 無し＝
// branch monitor.procTerminate と同手法）。プロセス不在は成功扱い
// （unix の ESRCH→nil とパリティ）。
func killProxy(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("不正 pid")
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return nil // ハンドル開けない＝既に不在扱い
	}
	defer syscall.CloseHandle(h)
	return syscall.TerminateProcess(h, 1)
}

// spawnResumeProxy は `claude-master proxy --resume <sid>` を cwd で
// detached 起動（DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP＝unix setsid
// 相当・branch monitor.detachSysProcAttr と同フラグ）。proxy 内部の
// ConPTY/IPC 経由で web/cloud から会話復帰。
func spawnResumeProxy(sid, cwd string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(self, "proxy", "--resume", sid)
	if cwd != "" {
		c.Dir = cwd
	}
	devnull, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0) // Windows: "NUL"
	if devnull != nil {
		c.Stdin, c.Stdout, c.Stderr = devnull, devnull, devnull
	}
	c.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
	if err := c.Start(); err != nil {
		return err
	}
	_ = c.Process.Release()
	return nil
}
