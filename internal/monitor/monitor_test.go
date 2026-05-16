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
	// SyncOnce は claude-master 管理（<pid>.sock あり）だけタブ化する。
	// テスト機に素の claude があっても socket 無しは対象外なので期待値も
	// managedOnly 後で比較（新仕様: 管理外 claude のタブは作らない）。
	managed := managedOnly(cfg, sessions)
	cur := SyncOnce(cfg, mgr, map[string]scanner.ClaudeSession{}, sessions)
	if len(cur) != len(uniqKeys(managed)) {
		t.Fatalf("current 件数不一致: %d != %d", len(cur), len(uniqKeys(managed)))
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
	o := out.String()
	if !strings.Contains(o, "Claude Code Sessions") ||
		!strings.Contains(o, "更新:") || !strings.Contains(o, "セッション数:") {
		t.Fatalf("Dashboard が STATUS_FILE を整形描画していない: %q", o)
	}
}

// RenderDashboard を「M5e-1/_write_status が実際に書く JSON 実スキーマ」
// で検証（box 幅・ヘッダ・行・limit サブ行・footer の忠実）。合成
// green でなく実 scan→WriteStatus→load した実データでも描画確認。
func TestRenderDashboardRealSchema(t *testing.T) {
	// 実スキーマ（json 復号で数値は float64）
	data := map[string]any{
		"updated_at": "2026-05-16 21:00:00",
		"sessions": []any{
			map[string]any{
				"pid": float64(4242), "short_dir": "claude-master-go",
				"start_time": "05-16 20:00", "cpu_percent": float64(3.2),
				"mem_mb": float64(128.4), "usage_percent": float64(91),
				"reset_time": "8:30 pm", "reset_tz": "UTC+9",
				"session_id": "deadbeef-1111-2222-3333-444455556666",
				"window_name": "claude-master-go",
			},
		},
	}
	out := RenderDashboard(data, 100, "最新")
	lines := strings.Split(out, "\n")
	for _, ln := range lines { // 全行が幅 W=100（rune 数）で枠が揃う
		if rcount(ln) != 100 {
			t.Fatalf("枠幅が W に揃っていない(%d): %q", rcount(ln), ln)
		}
	}
	must := []string{
		" Claude Code Sessions ", "PID", "Dir", "CPU%", "Mem MB",
		"Use%", "Resets", "4242", "claude-master-go", "91%",
		"Limit: 91%", "Resets: 8:30 pm", "(UTC+9)", "[deadbeef]",
		"更新: 2026-05-16 21:00:00", "セッション数: 1", "ツール: 最新",
	}
	for _, m := range must {
		if !strings.Contains(out, m) {
			t.Fatalf("dashboard に %q が無い:\n%s", m, out)
		}
	}
	// セッション無し
	empty := RenderDashboard(map[string]any{"updated_at": "x"}, 80, "")
	if !strings.Contains(empty, "(CLI セッションなし)") ||
		!strings.Contains(empty, "セッション数: 0") {
		t.Fatalf("空セッション描画が不正:\n%s", empty)
	}

	// 実 scan→WriteStatus→load→render が破綻しない（実データ）
	cfg := testCfg(t)
	mgr, done := testMgr(t, cfg)
	defer done()
	ss, _ := scanner.Scan(false)
	if err := WriteStatus(cfg, mgr, ss); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cfg.StatusFile)
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("実 status JSON 復号失敗: %v", err)
	}
	r := RenderDashboard(d, 90, "")
	if !strings.Contains(r, "Claude Code Sessions") ||
		!strings.Contains(r, "セッション数:") {
		t.Fatalf("実 scan 由来データで描画破綻:\n%s", r)
	}
	for _, ln := range strings.Split(r, "\n") {
		if rcount(ln) != 90 {
			t.Fatalf("実データで枠幅不揃い(%d): %q", rcount(ln), ln)
		}
	}
}

// managedOnly は <pid>.sock を持つ（＝claude-master proxy 経由で起動
// した）セッションだけ残し、素の claude を除外する（要望「claude-master
// で起動していない claude プロセスのタブは表示しない」）。実ファイル
// （SessionsDir に実 socket ファイル）で検証＝合成 stat に頼らない。
func TestManagedOnlyFiltersSocketless(t *testing.T) {
	cfg := testCfg(t)
	if err := os.MkdirAll(cfg.SessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	managedPid, barePid := 424242, 525252
	sock := filepath.Join(cfg.SessionsDir, strconv.Itoa(managedPid)+".sock")
	if err := os.WriteFile(sock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	in := []scanner.ClaudeSession{
		{Pid: managedPid, Cwd: "/x/managed", SessionID: "aaaa"},
		{Pid: barePid, Cwd: "/x/bare"}, // socket 無し＝管理外
	}
	got := managedOnly(cfg, in)
	if len(got) != 1 || got[0].Pid != managedPid {
		t.Fatalf("socket 持ちのみ残るべき: got=%+v", got)
	}
}

// RenderDashboard は data["remote"] があれば「リモート（他 PC）」節を
// PC 名・↗dir 付きで足し、無ければ出さない。枠幅は他行と不変。
func TestRenderDashboardRemoteSection(t *testing.T) {
	base := map[string]any{"updated_at": "2026-05-17 12:00:00",
		"sessions": []any{}}
	if r := RenderDashboard(base, 90, ""); strings.Contains(r, "リモート") {
		t.Fatalf("remote 未指定なのにリモート節が出た:\n%s", r)
	}
	withR := map[string]any{"updated_at": "2026-05-17 12:00:00",
		"sessions": []any{},
		"remote": []any{
			map[string]any{"pc": "Mac-Studio", "short_dir": "claude-master",
				"session_id": "deadbeef-1111"},
		}}
	r := RenderDashboard(withR, 90, "")
	if !strings.Contains(r, "リモート（他 PC）") ||
		!strings.Contains(r, "Mac-Studio") ||
		!strings.Contains(r, "↗claude-master") ||
		!strings.Contains(r, "[deadbeef]") {
		t.Fatalf("リモート節が期待通り描画されない:\n%s", r)
	}
	if !strings.Contains(r, "リモート: 1") {
		t.Fatalf("footer にリモート数が無い:\n%s", r)
	}
	for _, ln := range strings.Split(r, "\n") {
		if rcount(ln) != 90 {
			t.Fatalf("リモート節で枠幅不揃い(%d): %q", rcount(ln), ln)
		}
	}
}
