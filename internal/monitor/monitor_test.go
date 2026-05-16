package monitor

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/scanner"
	"github.com/4noha/claude-master-go/internal/tmux"
)

// 合成は使わない: 実 `ps`/`lsof` の scanner.Scan、実 tmux サーバ
// （隔離テストセッション）、実 pid セマンティクスで検証する。

func testCfg(t *testing.T) *config.Config {
	t.Helper()
	tmp := t.TempDir()
	return &config.Config{
		TmuxSession:  "cm-montest-" + strconv.Itoa(os.Getpid()),
		PollInterval: 1,
		StatusFile:   filepath.Join(tmp, "status.json"),
		PidFile:      filepath.Join(tmp, "pid"),
		LogFile:      filepath.Join(tmp, "log"),
		SessionsDir:  filepath.Join(tmp, "sessions"),
	}
}

func testMgr(t *testing.T, cfg *config.Config) (*tmux.Manager, func()) {
	t.Helper()
	if err := tmux.CheckTmux(); err != nil {
		t.Skipf("tmux 未インストール: %v", err)
	}
	m, err := tmux.NewManager(cfg.TmuxSession)
	if err != nil {
		t.Skipf("NewManager: %v", err)
	}
	m.EnsureSession()
	return m, func() {
		exec.Command("tmux", "kill-session", "-t", cfg.TmuxSession).Run()
	}
}

// 実 scan + 実 tmux で差分同期: 新規キー→window 作成、消失→削除。
func TestSyncOnceRealScanRealTmux(t *testing.T) {
	cfg := testCfg(t)
	mgr, done := testMgr(t, cfg)
	defer done()

	sessions, err := scanner.Scan(false)
	if err != nil {
		t.Skipf("scan 不可: %v", err)
	}
	cur := SyncOnce(cfg, mgr, map[string]scanner.ClaudeSession{}, sessions)
	if len(cur) != len(uniqKeys(sessions)) {
		t.Fatalf("current 件数不一致: %d != %d", len(cur), len(uniqKeys(sessions)))
	}
	wins := map[string]bool{}
	for _, w := range mgr.ListWindows() {
		wins[w] = true
	}
	for key := range cur {
		w := mgr.WindowFor(key)
		if w == "" || !wins[w] {
			t.Fatalf("実 tmux に同期 window が無い: key=%s win=%q list=%v",
				key, w, mgr.ListWindows())
		}
	}
	// 全消失 → 全 window 削除
	SyncOnce(cfg, mgr, cur, nil)
	for key := range cur {
		if mgr.WindowFor(key) != "" {
			t.Fatalf("消失キーの window が残る: %s", key)
		}
	}
	if len(sessions) > 0 {
		t.Logf("実 %d セッションを実 tmux へ同期→全削除を検証", len(sessions))
	}
}

func uniqKeys(ss []scanner.ClaudeSession) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		m[s.Key()] = true
	}
	return m
}

// 実 scan を STATUS_FILE へ。再読込で構造（updated_at + sessions[]）
// と各エントリの key/pid が忠実なこと。
func TestWriteStatusRealScan(t *testing.T) {
	cfg := testCfg(t)
	mgr, done := testMgr(t, cfg)
	defer done()
	sessions, err := scanner.Scan(false)
	if err != nil {
		t.Skipf("scan 不可: %v", err)
	}
	if err := WriteStatus(cfg, mgr, sessions); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	b, err := os.ReadFile(cfg.StatusFile)
	if err != nil {
		t.Fatalf("status 未生成: %v", err)
	}
	var p struct {
		UpdatedAt string           `json:"updated_at"`
		Sessions  []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("status が不正 JSON: %v", err)
	}
	if p.UpdatedAt == "" {
		t.Fatal("updated_at 空")
	}
	if len(p.Sessions) != len(sessions) {
		t.Fatalf("sessions 件数不一致: %d != %d", len(p.Sessions), len(sessions))
	}
	for i, s := range p.Sessions {
		if _, ok := s["key"]; !ok {
			t.Fatalf("sessions[%d] に key が無い", i)
		}
		if _, ok := s["pid"]; !ok {
			t.Fatalf("sessions[%d] に pid が無い", i)
		}
	}
}

// 実 tmux に作った window を STATUS_FILE 経由で復元（再起動時の
// 重複ウィンドウ防止）。
func TestRestoreWindowsRealTmux(t *testing.T) {
	cfg := testCfg(t)
	mgr, done := testMgr(t, cfg)
	defer done()

	s := scanner.ClaudeSession{Pid: 1, Cwd: "/tmp/cm-restore-dir",
		SessionID: "abcdef01-2345-6789-abcd-ef0123456789"}
	os.MkdirAll(s.Cwd, 0o755)
	defer os.RemoveAll(s.Cwd)
	win := mgr.AddWindow(s, "") // 実 tmux に window 作成
	if err := WriteStatus(cfg, mgr, []scanner.ClaudeSession{s}); err != nil {
		t.Fatal(err)
	}
	// 新しい Manager（再起動相当）で復元
	m2, err := tmux.NewManager(cfg.TmuxSession)
	if err != nil {
		t.Fatal(err)
	}
	if m2.WindowFor(s.Key()) != "" {
		t.Fatal("復元前なのに WindowFor が非空")
	}
	restoreWindows(cfg, m2)
	if m2.WindowFor(s.Key()) != win {
		t.Fatalf("STATUS_FILE から window 名が復元されない: %q != %q",
			m2.WindowFor(s.Key()), win)
	}
}

// 実 pid セマンティクス（自プロセス=alive、終了済子=dead）。
func TestPidAliveRealProcess(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatal("自プロセスを alive と判定できない")
	}
	c := exec.Command("/bin/sh", "-c", "exit 0")
	_ = c.Run() // 完了済み = 死亡
	if pidAlive(c.Process.Pid) {
		t.Fatal("終了済プロセスを alive と誤判定")
	}
}

// CmdStart の多重起動ガード（実 alive pid ＝自分を書く → spawn しない）。
func TestCmdStartGuardsDoubleStart(t *testing.T) {
	cfg := testCfg(t)
	if err := os.WriteFile(cfg.PidFile,
		[]byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := CmdStart(cfg, &out); err != nil {
		t.Fatalf("CmdStart: %v", err)
	}
	if !strings.Contains(out.String(), "すでに起動中") {
		t.Fatalf("多重起動ガードが効いていない: %q", out.String())
	}
}

func TestCmdStopNoPidFile(t *testing.T) {
	cfg := testCfg(t)
	var out bytes.Buffer
	if err := CmdStop(cfg, &out); err != nil {
		t.Fatalf("CmdStop: %v", err)
	}
	if !strings.Contains(out.String(), "起動していません") {
		t.Fatalf("PID ファイル無しの出力が不正: %q", out.String())
	}
}

func TestCmdStatusRealScan(t *testing.T) {
	cfg := testCfg(t)
	var out bytes.Buffer
	if err := CmdStatus(cfg, &out); err != nil {
		t.Fatalf("CmdStatus: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "PID") && !strings.Contains(s, "見つかりません") {
		t.Fatalf("status 出力が想定外: %q", s)
	}
}

// Dashboard は実 scan 由来の STATUS_FILE を 1 周描画して done で抜ける。
func TestDashboardRendersRealStatusFile(t *testing.T) {
	cfg := testCfg(t)
	mgr, done := testMgr(t, cfg)
	defer done()
	sessions, _ := scanner.Scan(false)
	if err := WriteStatus(cfg, mgr, sessions); err != nil {
		t.Fatal(err)
	}
	d := make(chan struct{})
	close(d) // 1 周描画したら即終了
	var out bytes.Buffer
	doneCh := make(chan struct{})
	go func() { Dashboard(cfg, d, &out); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Dashboard が done で終了しない")
	}
	if !strings.Contains(out.String(), "claude-master") {
		t.Fatalf("Dashboard が STATUS_FILE を描画していない: %q", out.String())
	}
}
