//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// 遠隔命令の実行系（unix=POSIX）。M8 前に main.go へ inline されていた
// 実装と **挙動同一**（darwin/linux parity 厳守）。branch の
// remotecmd_unix.go と同じ OS-split 規約。

// restartDaemons は launchd の monitor/cloud 2 デーモンを kickstart -k
// で再起動（cloud 自身も含むため自分は SIGKILL→launchd 再生で新バイナリ
// に載る。それゆえ呼び元は Ack 先行）。
func restartDaemons() error {
	dom := fmt.Sprintf("gui/%d", os.Getuid())
	// monitor を先に（cloud を後＝自己 kill 前に他方を確実に発火）
	_ = exec.Command("launchctl", "kickstart", "-k",
		dom+"/com.4noha.claude-master").Run()
	return exec.Command("launchctl", "kickstart", "-k",
		dom+"/com.4noha.claude-master-cloud").Run()
}

// killProxy は proxy を SIGTERM→3s 猶予→生存なら SIGKILL（子 claude も
// 同時に落ちる）。既に不在なら成功扱い。
func killProxy(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("不正 pid")
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			return nil // 既に不在
		}
		return err
	}
	for i := 0; i < 30; i++ {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	return nil
}

// spawnDetachedProxy は `claude-master proxy [args...]` を cwd で
// detached（setsid・stdio=/dev/null）起動。親と独立に存続するので、
// 親プロセスが死んでも proxy が残る＝**self-update 反映の前提**
// （proxy を毎回新規 spawn で立ち上げる）。runStart からの新規セッ
// ション開始と、ProxyRestarter からの --resume 復帰の両方で使う。
func spawnDetachedProxy(args []string, cwd string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	pArgs := append([]string{"proxy"}, args...)
	c := exec.Command(self, pArgs...)
	if cwd != "" {
		c.Dir = cwd
	}
	devnull, _ := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if devnull != nil {
		c.Stdin, c.Stdout, c.Stderr = devnull, devnull, devnull
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 親と独立に存続
	if err := c.Start(); err != nil {
		return err
	}
	_ = c.Process.Release()
	return nil
}

// spawnResumeProxy は `claude-master proxy --resume <sid>` を cwd で
// detached 起動（ProxyRestarter 経由の遠隔復帰用・後方互換シム）。
// 元端末へは戻らないが unix socket を出すので web/cloud 経由で会話を
// 復帰できる。claude --resume の再ストリーム/SESSION_LOG/dedup 不変は
// ptyproxy 側で既に担保。
func spawnResumeProxy(sid, cwd string) error {
	return spawnDetachedProxy([]string{"--resume", sid}, cwd)
}
