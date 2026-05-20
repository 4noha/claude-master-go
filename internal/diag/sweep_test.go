//go:build !windows

package diag

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Sweep は live PID（自プロセス）は残し、ありえない PID は消す。
// PID 形式以外（remote_sessions.json 等）は触らない。
func TestSweepRemovesDeadKeepsAlive(t *testing.T) {
	sessDir := t.TempDir()
	diagDir := t.TempDir()
	mine := os.Getpid()
	mineStr := strconv.Itoa(mine)
	// 自プロセス由来（alive）: 残るべき
	mustWrite(t, filepath.Join(sessDir, mineStr+".sock"), "")
	mustWrite(t, filepath.Join(sessDir, mineStr+".status.json"), `{}`)
	mustWrite(t, filepath.Join(diagDir, mineStr+".snap"), `{}`)
	// 大きすぎて存在しないはずの PID（dead）: 削除されるべき
	deadPID := "9999999"
	mustWrite(t, filepath.Join(sessDir, deadPID+".sock"), "")
	mustWrite(t, filepath.Join(sessDir, deadPID+".status.json"), `{}`)
	mustWrite(t, filepath.Join(diagDir, deadPID+".snap"), `{}`)
	// 非 PID 形式: 触られないべき（残骸でなく正当な cache）
	mustWrite(t, filepath.Join(sessDir, "remote_sessions.json"), `{}`)
	mustWrite(t, filepath.Join(diagDir, "notes.txt"), `manual`)

	s, d := Sweep(sessDir, diagDir)
	if s != 2 || d != 1 {
		t.Fatalf("削除数想定外: sessions=%d (want 2) snap=%d (want 1)", s, d)
	}
	// alive のものは残ってる
	for _, f := range []string{
		filepath.Join(sessDir, mineStr+".sock"),
		filepath.Join(sessDir, mineStr+".status.json"),
		filepath.Join(diagDir, mineStr+".snap"),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("alive PID の file が誤削除: %s", f)
		}
	}
	// dead のものは消えてる
	for _, f := range []string{
		filepath.Join(sessDir, deadPID+".sock"),
		filepath.Join(sessDir, deadPID+".status.json"),
		filepath.Join(diagDir, deadPID+".snap"),
	} {
		if _, err := os.Stat(f); err == nil {
			t.Fatalf("dead PID の file が残った: %s", f)
		}
	}
	// 非 PID 形式は不変
	for _, f := range []string{
		filepath.Join(sessDir, "remote_sessions.json"),
		filepath.Join(diagDir, "notes.txt"),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("非 PID file が誤削除: %s", f)
		}
	}
}

// 存在しない dir は安全に no-op（returns 0/0、エラー無し）。
func TestSweepMissingDirs(t *testing.T) {
	s, d := Sweep("/nonexistent/sess", "/nonexistent/diag")
	if s != 0 || d != 0 {
		t.Fatalf("不在 dir で削除数: %d/%d", s, d)
	}
}

// 空 dir も no-op。
func TestSweepEmptyDirs(t *testing.T) {
	s, d := Sweep(t.TempDir(), t.TempDir())
	if s != 0 || d != 0 {
		t.Fatalf("空 dir で削除数: %d/%d", s, d)
	}
}

// extractPID: <pid>.suffix 形式のみ ok、それ以外 false。
func TestExtractPID(t *testing.T) {
	for _, c := range []struct {
		name string
		ok   bool
		pid  int
	}{
		{"12345.sock", true, 12345},
		{"12345.status.json", true, 12345},
		{"12345.snap", true, 12345},
		{"remote_sessions.json", false, 0},
		{"notes.txt", false, 0},
		{"abc.sock", false, 0},
		{"-5.sock", false, 0},
		{"0.sock", false, 0},
	} {
		var suffixes []string
		switch {
		case len(c.name) > 5 && c.name[len(c.name)-5:] == ".snap":
			suffixes = []string{".snap"}
		default:
			suffixes = []string{".sock", ".status.json"}
		}
		pid, ok := extractPID(c.name, suffixes)
		if ok != c.ok || (ok && pid != c.pid) {
			t.Fatalf("extractPID(%q) = (%d, %v) want (%d, %v)", c.name, pid, ok, c.pid, c.ok)
		}
	}
}

// isAlive: 自プロセスは true、巨大未使用 PID は false。
func TestIsAliveSelfAndDead(t *testing.T) {
	if !isAlive(os.Getpid()) {
		t.Fatal("自プロセス を dead と判定")
	}
	if isAlive(9999999) {
		t.Fatal("巨大 PID を alive と判定")
	}
	if isAlive(0) || isAlive(-1) {
		t.Fatal("無効 PID で alive 判定")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
