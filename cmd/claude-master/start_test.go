//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// findLiveKeyByCwd の挙動: cwd 一致の最新 updated_at セッション key を返す。
// 不在は空文字。複数同 cwd は updated_at 比較で最新採用。
func TestFindLiveKeyByCwd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	write := func(sess []map[string]any) {
		b, _ := json.Marshal(map[string]any{"sessions": sess})
		_ = os.WriteFile(path, b, 0o644)
	}

	// cwd 不一致のみ → 空
	write([]map[string]any{
		{"cwd": "/Users/x/other", "key": "k1", "updated_at": "2026-01-01T00:00:00Z"},
	})
	if got := findLiveKeyByCwd(path, "/Users/x/target"); got != "" {
		t.Fatalf("不一致 cwd で key 返却: %q", got)
	}

	// 1 件一致 → その key
	write([]map[string]any{
		{"cwd": "/Users/x/target", "key": "k-target", "updated_at": "2026-01-01T00:00:00Z"},
	})
	if got := findLiveKeyByCwd(path, "/Users/x/target"); got != "k-target" {
		t.Fatalf("一致 key 取得失敗: %q", got)
	}

	// 複数一致 → updated_at 最新（lexical でも RFC3339 は時系列）
	write([]map[string]any{
		{"cwd": "/Users/x/target", "key": "old", "updated_at": "2026-01-01T00:00:00Z"},
		{"cwd": "/Users/x/target", "key": "new", "updated_at": "2026-05-01T00:00:00Z"},
	})
	if got := findLiveKeyByCwd(path, "/Users/x/target"); got != "new" {
		t.Fatalf("最新採用失敗: %q", got)
	}

	// STATUS_FILE 不在 → 空（panic しない）
	if got := findLiveKeyByCwd("/nonexistent/never.json", "/x"); got != "" {
		t.Fatalf("不在 STATUS_FILE で key: %q", got)
	}

	// 壊れ JSON → 空
	_ = os.WriteFile(path, []byte("not json"), 0o644)
	if got := findLiveKeyByCwd(path, "/x"); got != "" {
		t.Fatalf("壊れ STATUS_FILE で key: %q", got)
	}

	// key が空文字 → 採用しない（壊れたエントリ除外）
	write([]map[string]any{
		{"cwd": "/Users/x/target", "key": "", "updated_at": "2026-01-01T00:00:00Z"},
	})
	if got := findLiveKeyByCwd(path, "/Users/x/target"); got != "" {
		t.Fatalf("空 key を返却: %q", got)
	}
}

// waitKeyForCwd: 出現を poll で検知（最大 timeout）。出現しなければ
// 空文字を返す。proxy 起動→STATUS_FILE 反映の隙間を吸収する用途。
func TestWaitKeyForCwdAppearsThenSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	// 初期は空
	_ = os.WriteFile(path, []byte(`{"sessions":[]}`), 0o644)

	var seen atomic.Bool
	go func() {
		time.Sleep(700 * time.Millisecond)
		_ = os.WriteFile(path, []byte(`{"sessions":[{"cwd":"/x","key":"newkey","updated_at":"2026-01-01T00:00:00Z"}]}`), 0o644)
		seen.Store(true)
	}()

	t0 := time.Now()
	got := waitKeyForCwd(path, "/x", 3*time.Second)
	dt := time.Since(t0)
	if got != "newkey" {
		t.Fatalf("出現後の key 取得失敗: %q（seen=%v）", got, seen.Load())
	}
	if dt < 500*time.Millisecond || dt > 2*time.Second {
		t.Fatalf("検知タイミング異常: %v（出現は ~700ms 後・poll 500ms）", dt)
	}
}

func TestWaitKeyForCwdTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	_ = os.WriteFile(path, []byte(`{"sessions":[]}`), 0o644)
	t0 := time.Now()
	got := waitKeyForCwd(path, "/x", 600*time.Millisecond)
	dt := time.Since(t0)
	if got != "" {
		t.Fatalf("timeout で key 返却: %q", got)
	}
	if dt < 500*time.Millisecond || dt > 1500*time.Millisecond {
		t.Fatalf("timeout 動作異常: %v", dt)
	}
}

// findLiveSessionByCwd は (key, pid) を返す。moved-dir 検出に必須。
func TestFindLiveSessionReturnsPid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	write := func(sess []map[string]any) {
		b, _ := json.Marshal(map[string]any{"sessions": sess})
		_ = os.WriteFile(path, b, 0o644)
	}
	write([]map[string]any{
		{"cwd": "/X", "key": "k", "pid": float64(12345), "updated_at": "2026-01-01T00:00:00Z"},
	})
	key, pid := findLiveSessionByCwd(path, "/X")
	if key != "k" || pid != 12345 {
		t.Fatalf("(key,pid) = (%q,%d) want (k,12345)", key, pid)
	}
	// 不一致 cwd
	if k, p := findLiveSessionByCwd(path, "/Y"); k != "" || p != 0 {
		t.Fatalf("不一致 で (%q,%d)", k, p)
	}
}

// readSnapStartCwd: snap.cwd を返す。snap 不在/壊れは空。
// HOME を t.TempDir() に差し替えて isolated test。
func TestReadSnapStartCwd(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", td)
	if err := os.MkdirAll(filepath.Join(td, ".claude-master", "diag"), 0o755); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(td, ".claude-master", "diag", "999.snap")
	_ = os.WriteFile(snap, []byte(`{"pid":999,"cwd":"/old/path","uptime_sec":100}`), 0o644)
	if got := readSnapStartCwd(999); got != "/old/path" {
		t.Fatalf("cwd 取得失敗: %q", got)
	}
	// snap 不在
	if got := readSnapStartCwd(888); got != "" {
		t.Fatalf("不在 で空でない: %q", got)
	}
	// 壊れ
	_ = os.WriteFile(snap, []byte("not json"), 0o644)
	if got := readSnapStartCwd(999); got != "" {
		t.Fatalf("壊れ snap で空でない: %q", got)
	}
}

