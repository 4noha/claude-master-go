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

// IdleGCSweep: connected_clients == 0 かつ last_disconnect が threshold
// より古い live PID を SIGTERM。自プロセスを誤殺しないよう alive+古い
// 条件を組み合わせる際は connected_clients > 0 でガード（自 PID で
// kill されると test runner 全滅）。
func writeSnap(t *testing.T, dir string, pid int, lastDisc string, connected int32, cwd string) {
	t.Helper()
	s := map[string]any{
		"pid":               pid,
		"last_disconnect":   lastDisc,
		"connected_clients": connected,
		"cwd":               cwd,
	}
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".snap"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIdleGCSweepZeroThresholdNoop(t *testing.T) {
	dir := t.TempDir()
	writeSnap(t, dir, os.Getpid(), "2000-01-01T00:00:00Z", 0, "/x")
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

func TestIdleGCSweepSkipsConnected(t *testing.T) {
	dir := t.TempDir()
	mine := os.Getpid()
	// 古い last_disconnect だが connected_clients > 0 ＝ 観測者居る → skip
	writeSnap(t, dir, mine, "2000-01-01T00:00:00Z", 2, "/x")
	if r := IdleGCSweep(dir, time.Microsecond); len(r) != 0 {
		t.Fatalf("connected>0 で kill 誤発火: %+v", r)
	}
}

func TestIdleGCSweepSkipsNeverLastDisconnect(t *testing.T) {
	dir := t.TempDir()
	mine := os.Getpid()
	writeSnap(t, dir, mine, "never", 0, "/x") // 起動直後で未だ disconnect 経験無し
	if r := IdleGCSweep(dir, time.Microsecond); len(r) != 0 {
		t.Fatalf("never で kill 誤発火: %+v", r)
	}
	writeSnap(t, dir, mine, "", 0, "/x")
	if r := IdleGCSweep(dir, time.Microsecond); len(r) != 0 {
		t.Fatalf("空 last_disconnect で kill 誤発火: %+v", r)
	}
}

func TestIdleGCSweepSkipsRecentDisconnect(t *testing.T) {
	dir := t.TempDir()
	mine := os.Getpid()
	// 5 秒前 disconnect → threshold 1 時間 → skip
	writeSnap(t, dir, mine, time.Now().Add(-5*time.Second).Format(time.RFC3339Nano), 0, "/x")
	if r := IdleGCSweep(dir, time.Hour); len(r) != 0 {
		t.Fatalf("recent disconnect で kill: %+v", r)
	}
}

func TestIdleGCSweepSkipsDeadPID(t *testing.T) {
	dir := t.TempDir()
	// 不在 PID で「古い disconnect」snap = isAlive=false で skip
	writeSnap(t, dir, 9999999, time.Now().Add(-2*time.Hour).Format(time.RFC3339Nano), 0, "/x")
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
