//go:build !windows

// 実 tmux サーバで Manager CRUD/@cm_remote/pane を検証する unix 専用
// テスト（@cm_remote/pane_current_command は psmux 非忠実＝Windows 対象外。
// それらは cloud agent=M8f）。Windows の monitor 利用サブセットは
// tmux_windows_test.go（実 psmux）。Mac/linux 従来通り＝parity。
package tmux

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/4noha/claude-master-go/internal/scanner"
)

// 実 tmux サーバ＋専用テストセッション（ユーザーの本番 claude-master
// セッションを汚さない一意名）で CRUD を検証する。合成は使わない。

func testSession(t *testing.T) (*Manager, func()) {
	t.Helper()
	if err := CheckTmux(); err != nil {
		t.Skipf("tmux 未インストール: %v", err)
	}
	name := "cm-gotest-" + strconv.Itoa(os.Getpid())
	m, err := NewManager(name)
	if err != nil {
		t.Skipf("NewManager: %v", err)
	}
	m.EnsureSession()
	// automatic-rename を切る（pane の実行プロセスで window 名が
	// 上書きされると -n name と不一致になる＝実 tmux の正規設定）。
	exec.Command("tmux", "set-option", "-t", name, "automatic-rename", "off").Run()
	cleanup := func() { exec.Command("tmux", "kill-session", "-t", name).Run() }
	return m, cleanup
}

func TestEnsureSessionIsIdempotent(t *testing.T) {
	m, done := testSession(t)
	defer done()
	if !ok("has-session", "-t", m.Session) {
		t.Fatal("EnsureSession 後にセッションが無い")
	}
	m.EnsureSession() // 二度目: 二重作成しない（has-session 先行）
	if !ok("has-session", "-t", m.Session) {
		t.Fatal("二度目の EnsureSession で壊れた")
	}
}

func TestAddRenameRemoveWindowRealTmux(t *testing.T) {
	m, done := testSession(t)
	defer done()

	dir := "/tmp/cm-tmuxtest-dir"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	s1 := scanner.ClaudeSession{Pid: 1, Cwd: dir,
		SessionID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	s2 := scanner.ClaudeSession{Pid: 2, Cwd: dir, // 同 ShortDir・別キー
		SessionID: "11111111-2222-3333-4444-555555555555"}

	n1 := m.AddWindow(s1, "") // socket 無し＝対話シェル window
	if n1 != s1.ShortDir() {
		t.Fatalf("初回 window 名が ShortDir でない: %q != %q", n1, s1.ShortDir())
	}
	if !contains(m.ListWindows(), n1) {
		t.Fatalf("作成した window が一覧に無い: %q in %v", n1, m.ListWindows())
	}
	n2 := m.AddWindow(s2, "") // 衝突 → -2 サフィックス
	if n2 == n1 || !strings.HasPrefix(n2, s2.ShortDir()) {
		t.Fatalf("衝突回避名が不正: n1=%q n2=%q", n1, n2)
	}
	if !contains(m.ListWindows(), n2) {
		t.Fatalf("2 個目 window が一覧に無い: %q in %v", n2, m.ListWindows())
	}
	// 同キー再 AddWindow は既存 window を再利用（monitor 再起動復元）
	if again := m.AddWindow(s1, ""); again != n1 {
		t.Fatalf("同キー再追加で別 window: %q != %q", again, n1)
	}

	m.RenameWindow(s1.Key(), "renamed-w")
	if m.WindowFor(s1.Key()) != "renamed-w" {
		t.Fatalf("RenameWindow 後 WindowFor 不一致: %q", m.WindowFor(s1.Key()))
	}
	if !contains(m.ListWindows(), "renamed-w") || contains(m.ListWindows(), n1) {
		t.Fatalf("実 tmux の window 名がリネームされていない: %v", m.ListWindows())
	}

	m.RemoveWindow(s1.Key())
	if m.WindowFor(s1.Key()) != "" {
		t.Fatal("RemoveWindow 後も WindowFor が残る")
	}
	if contains(m.ListWindows(), "renamed-w") {
		t.Fatalf("実 tmux から window が消えていない: %v", m.ListWindows())
	}
	// s2 の window は無傷
	if !contains(m.ListWindows(), n2) {
		t.Fatalf("無関係 window が巻き添えで消えた: %v", m.ListWindows())
	}
}

func TestSetupDashboardRealTmux(t *testing.T) {
	m, done := testSession(t)
	defer done()
	m.SetupDashboard("true") // 無害コマンド。dashboard window が在ること
	if !contains(m.ListWindows(), "dashboard") {
		t.Fatalf("dashboard window が無い: %v", m.ListWindows())
	}
}

func TestShquotePOSIX(t *testing.T) {
	cases := map[string]string{
		"":                "''",
		"plain":           "plain",
		"/opt/bin/claude": "/opt/bin/claude",
		"a b":             "'a b'",
		"it's":            `'it'"'"'s'`,
	}
	for in, want := range cases {
		if got := shquote(in); got != want {
			t.Fatalf("shquote(%q)=%q want %q", in, got, want)
		}
	}
}
