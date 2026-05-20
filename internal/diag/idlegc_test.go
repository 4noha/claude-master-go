//go:build !windows

package diag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// IdleGCSweep: threshold より古い host_out_last の live PID を SIGTERM。
// 自プロセス（テスト bin 自身）の snap を「古い last」で書き、kill 経
// 由で自分を殺すと test runner 全滅するので、テストでは sendSIGTERM
// は呼ばれない条件（自 PID は alive・last 新しい・PID 不一致など）の
// 組合せで Sweep ロジックの分岐を網羅する。kill 実発火経路だけは
// 「巨大未使用 PID で sendSIGTERM を呼ぶ→自然に ESRCH/失敗」で確認。
func writeSnap(t *testing.T, dir string, pid int, hostOutLast, cwd string) {
	t.Helper()
	s := map[string]any{
		"pid":           pid,
		"host_out_last": hostOutLast,
		"cwd":           cwd,
	}
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".snap"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIdleGCSweepZeroThresholdNoop(t *testing.T) {
	dir := t.TempDir()
	writeSnap(t, dir, os.Getpid(), "2000-01-01T00:00:00Z", "/x")
	if r := IdleGCSweep(dir, 0); len(r) != 0 {
		t.Fatalf("threshold=0 で kill: %+v", r)
	}
	if r := IdleGCSweep(dir, -1*time.Second); len(r) != 0 {
		t.Fatalf("threshold<0 で kill: %+v", r)
	}
}

func TestIdleGCSweepMissingDir(t *testing.T) {
	if r := IdleGCSweep("/nonexistent/never-exists", time.Hour); len(r) != 0 {
		t.Fatalf("不在 dir で kill: %+v", r)
	}
}

func TestIdleGCSweepSkipsNeverHostOutLast(t *testing.T) {
	dir := t.TempDir()
	mine := os.Getpid()
	writeSnap(t, dir, mine, "never", "/x") // never = 起動直後 = skip
	if r := IdleGCSweep(dir, time.Microsecond); len(r) != 0 {
		t.Fatalf("never で kill 誤発火: %+v", r)
	}
	writeSnap(t, dir, mine, "", "/x") // 空も skip
	if r := IdleGCSweep(dir, time.Microsecond); len(r) != 0 {
		t.Fatalf("空 host_out_last で kill 誤発火: %+v", r)
	}
}

func TestIdleGCSweepSkipsRecentActivity(t *testing.T) {
	dir := t.TempDir()
	mine := os.Getpid()
	// 5 秒前 active → threshold 1 時間 → skip
	writeSnap(t, dir, mine, time.Now().Add(-5*time.Second).Format(time.RFC3339Nano), "/x")
	if r := IdleGCSweep(dir, time.Hour); len(r) != 0 {
		t.Fatalf("active proxy で kill: %+v", r)
	}
}

func TestIdleGCSweepSkipsDeadPID(t *testing.T) {
	dir := t.TempDir()
	// 不在 PID で「古い last」snap = isAlive=false で skip
	writeSnap(t, dir, 9999999, time.Now().Add(-2*time.Hour).Format(time.RFC3339Nano), "/x")
	if r := IdleGCSweep(dir, time.Hour); len(r) != 0 {
		t.Fatalf("dead PID で kill 誤発火: %+v", r)
	}
}

func TestIdleGCSweepSkipsBadJSON(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "12345.snap"), []byte("not json"), 0o644)
	if r := IdleGCSweep(dir, time.Hour); len(r) != 0 {
		t.Fatalf("壊れ snap で kill: %+v", r)
	}
}

func TestIdleGCSweepSkipsNonPidFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("manual"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "abc.snap"), []byte(`{"host_out_last":"2000-01-01T00:00:00Z"}`), 0o644)
	if r := IdleGCSweep(dir, time.Hour); len(r) != 0 {
		t.Fatalf("非 PID file で kill: %+v", r)
	}
}

// sendSIGTERM: 巨大未使用 PID で ESRCH が返る（自プロセスは殺さない）。
func TestSendSIGTERMNonExistentPID(t *testing.T) {
	if err := sendSIGTERM(9999999); err == nil {
		t.Fatal("不在 PID で err=nil（成功扱い） — kill が wild card に当たる危険")
	}
	if err := sendSIGTERM(0); err == nil {
		t.Fatal("pid=0 で err=nil（全プロセス kill 危険）")
	}
	if err := sendSIGTERM(-1); err == nil {
		t.Fatal("pid=-1 で err=nil（全プロセス kill 危険）")
	}
}
