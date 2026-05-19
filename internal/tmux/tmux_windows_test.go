//go:build windows

package tmux

import (
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/4noha/claude-master-go/internal/scanner"
)

// M8e gate（鉄則#2: 合成でなく実 psmux）。monitor が実際に呼ぶ
// Manager メソッドのみを実 psmux(tmux.exe on PATH)で検証:
// EnsureSession / AddWindow(shell・socket) / ListWindows(#{window_name})
// / WindowFor / RenameWindow / RemoveWindow。psmux 非忠実の
// @cm_remote / pane_current_command は monitor 非依存(=cloud agent=M8f)
// なので対象外。
//
// 前提: psmux が PATH 上（ユーザーが導入済）。未導入なら Skip。
func TestMonitorTmuxPathOnRealPsmux(t *testing.T) {
	if err := CheckTmux(); err != nil {
		t.Skipf("tmux(psmux) 不在: %v", err)
	}
	sess := "cmtest-m8e-" + strconv.Itoa(os.Getpid())
	m, err := NewManager(sess)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", sess).Run()
	})

	m.EnsureSession() // has-session(rc!=0)→new-session -d（psmux 忠実）

	// shell window（socketPath="" → interactiveShell(windows)=cmd）
	s1 := scanner.ClaudeSession{Pid: 991001} // Cwd="" → ShortDir "unknown"
	w1 := m.AddWindow(s1, "")
	if w1 == "" || m.WindowFor(s1.Key()) != w1 {
		t.Fatalf("AddWindow(shell): w1=%q WindowFor=%q", w1, m.WindowFor(s1.Key()))
	}
	if !containsStr(m.ListWindows(), w1) {
		t.Fatalf("ListWindows に %q が無い: %v", w1, m.ListWindows())
	}

	// socket window（socketCmd=windows quote の <self> socket-client）
	s2 := scanner.ClaudeSession{Pid: 991002}
	w2 := m.AddWindow(s2, `C:\Users\nokki\.claude-master\sessions\x.sock`)
	if w2 == "" || !containsStr(m.ListWindows(), w2) {
		t.Fatalf("AddWindow(socket): w2=%q list=%v", w2, m.ListWindows())
	}

	// RenameWindow → #{window_name} per-window 忠実(psmux spike 済)
	m.RenameWindow(s1.Key(), "renamed1")
	if m.WindowFor(s1.Key()) != "renamed1" ||
		!containsStr(m.ListWindows(), "renamed1") {
		t.Fatalf("RenameWindow 失敗: WindowFor=%q list=%v",
			m.WindowFor(s1.Key()), m.ListWindows())
	}

	// RemoveWindow → kill-window（psmux 忠実）
	m.RemoveWindow(s1.Key())
	if containsStr(m.ListWindows(), "renamed1") || m.WindowFor(s1.Key()) != "" {
		t.Fatalf("RemoveWindow 後も残存: list=%v WindowFor=%q",
			m.ListWindows(), m.WindowFor(s1.Key()))
	}
	m.RemoveWindow(s2.Key())

	t.Logf("実 psmux で monitor 利用 tmux 経路 OK: "+
		"EnsureSession/AddWindow(shell,socket)/ListWindows/WindowFor/"+
		"RenameWindow/RemoveWindow（session=%s）", sess)
}

func containsStr(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
