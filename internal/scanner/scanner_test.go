package scanner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 合成行は使わない: 実 `ps aux` 出力・実 claude CLI 引数契約
// （--resume <uuid> / --output-format stream-json は CLAUDE.md 記載の
// 実プロトコル）・実環境 Scan で検証する。

// splitWSN/parsePSLine を実 macOS `ps aux` 出力で検証。自プロセス行を
// 実際に取り出し pid 復元できること＝列フォーマット忠実を機械担保。
func TestParsePSLineAgainstRealPS(t *testing.T) {
	out, err := exec.Command("ps", "aux").Output()
	if err != nil {
		t.Skipf("ps aux 不可: %v", err)
	}
	lines := strings.Split(string(out), "\n")
	myPID := strconv.Itoa(os.Getpid())
	foundSelf := false
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// 全実行行を通す: panic しない・claude 以外は ok=false
		s, ok := parsePSLine(line, false)
		if ok {
			if s.Pid <= 0 {
				t.Fatalf("ok なのに pid 不正: %q -> %+v", line, s)
			}
			// 採用行は COMMAND 先頭が claude 系のはず
			parts := splitWSN(line, 10)
			base := strings.Fields(parts[10])[0]
			if !isClaudeCmdBase(base) {
				t.Fatalf("claude でない行を採用: base=%q", base)
			}
		}
		// 自プロセス行で splitWSN の列復元（実フォーマット）を確認
		parts := splitWSN(line, 10)
		if len(parts) >= 11 && parts[1] == myPID {
			foundSelf = true
			if _, e := strconv.Atoi(parts[1]); e != nil {
				t.Fatalf("実 ps 行 pid 復元失敗: %q", parts[1])
			}
			if _, e := strconv.ParseFloat(parts[2], 64); e != nil {
				t.Fatalf("実 ps 行 %%CPU 復元失敗: %q", parts[2])
			}
			if strings.TrimSpace(parts[10]) == "" {
				t.Fatalf("実 ps 行 COMMAND 空（splitWSN 破損）: %q", line)
			}
		}
	}
	if !foundSelf {
		t.Fatal("自プロセス行が実 ps から拾えなかった（テスト前提崩れ）")
	}
}

// claude CLI の実引数契約（CLAUDE.md: 識別キー=--resume 値、
// VS Code=--output-format stream-json）で session_id 抽出/VSCode 判定。
func TestExtractSessionIDAndVSCodeRealContract(t *testing.T) {
	uuid := "3f2a1b4c-1d2e-4a5b-8c9d-0e1f2a3b4c5d"
	if got := extractSessionID([]string{"claude", "--resume", uuid}); got != uuid {
		t.Fatalf("--resume <uuid> 抽出失敗: %q", got)
	}
	if got := extractSessionID([]string{"claude", "--resume", "notauuid"}); got != "" {
		t.Fatalf("UUID 形でない値を採用: %q", got)
	}
	if got := extractSessionID([]string{"claude"}); got != "" {
		t.Fatalf("--resume 無しで非空: %q", got)
	}
	if got := extractSessionID([]string{"claude", "--resume"}); got != "" {
		t.Fatalf("--resume 値欠落で非空: %q", got)
	}
	vs := []string{"claude", "--output-format", "stream-json"}
	if !isVSCodeSession(vs) {
		t.Fatal("VS Code 実契約を VSCode 判定できない")
	}
	if isVSCodeSession([]string{"claude", "--resume", uuid}) {
		t.Fatal("通常セッションを VSCode 誤判定")
	}
	if isClaudeCmdBase("python") || !isClaudeCmdBase("claude") ||
		!isClaudeCmdBase("/opt/homebrew/bin/claude") {
		t.Fatal("cmd_base 判定が実契約と不一致")
	}
}

// 実環境 Scan: ps が無いと nil+err、あれば各セッションが健全。
// （CI に claude が無くても空+nil で落ちない＝実環境堅牢性）。
func TestScanLiveEnvironment(t *testing.T) {
	sessions, err := Scan(false)
	if err != nil {
		t.Skipf("実環境で ps 実行不可: %v", err)
	}
	for _, s := range sessions {
		if s.Pid <= 0 {
			t.Fatalf("pid 不正: %+v", s)
		}
		if s.Key() == "" {
			t.Fatalf("Key 空: %+v", s)
		}
		if s.ShortDir() == "" {
			t.Fatalf("ShortDir 空: %+v", s)
		}
	}
	t.Logf("実環境 claude セッション数=%d", len(sessions))
}

// Key/ShortDir を実 cwd（os.Getwd）で検証（合成パス不使用）。
func TestKeyAndShortDirRealCwd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	withID := ClaudeSession{Pid: 99, Cwd: wd,
		SessionID: "11112222-3333-4444-5555-666677778888"}
	if withID.Key() != withID.SessionID {
		t.Fatalf("session_id があるのに Key が pid: %q", withID.Key())
	}
	noID := ClaudeSession{Pid: 99, Cwd: wd}
	if noID.Key() != "pid-99" {
		t.Fatalf("session_id 無しの Key 不正: %q", noID.Key())
	}
	wantDir := wd[strings.LastIndex(wd, "/")+1:]
	if noID.ShortDir() != wantDir {
		t.Fatalf("ShortDir が実 cwd 末尾と不一致: got=%q want=%q",
			noID.ShortDir(), wantDir)
	}
	if (ClaudeSession{Cwd: "/"}).ShortDir() != "unknown" {
		t.Fatal("ルート cwd の ShortDir が unknown でない")
	}
}

// lsof は C/POSIX ロケールで非ASCII バイトを文字列 "\xNN" に化かす。
// unescapeLsof はそれを元バイトへ戻す（U+2010 ハイフン "‐"=E2 80 90
// を含む実パス claude‐master が壊れた実バグの核）。純関数を決定的に検証。
func TestUnescapeLsofRestoresUTF8(t *testing.T) {
	cases := []struct{ in, want string }{
		{`/Users/4noha/works/claude\xe2\x80\x90master`,
			"/Users/4noha/works/claude‐master"},
		{"/plain/ascii/path", "/plain/ascii/path"}, // 非対象は素通し
		{`a\xe2\x80\x90b\x20c`, "a‐b c"},        // 連続/空白エスケープ
		{`no-backslash-x-here`, "no-backslash-x-here"},
	}
	for _, c := range cases {
		if got := unescapeLsof(c.in); got != c.want {
			t.Fatalf("unescapeLsof(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// 実 lsof で「cwd に U+2010 を含むプロセス」を解決し、C ロケール下でも
// 正しい UTF-8 パスが返ることを検証（ロケール強制＋unescape の統合）。
// 合成 stub は使わず実プロセス・実 lsof・実ディレクトリで担保。
func TestGetCwdLsofRealUnicodeHyphenDir(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof 不在")
	}
	base := t.TempDir()
	uniDir := filepath.Join(base, "claude‐master") // U+2010 を含む
	if err := os.Mkdir(uniDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command("sleep", "30")
	cmd.Dir = uniDir
	// テスト自身を C ロケールにしても getCwdLsof 内のロケール強制＋
	// unescape で正しく解決できることを示す（バグ条件を再現した上で緑）。
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	// macOS は /var→/private/var 等の symlink があり lsof は実体パスを
	// 返すため symlink 解決して比較（本検証の主眼は U+2010 の保持）。
	want, _ := filepath.EvalSymlinks(uniDir)

	var got string
	for i := 0; i < 50; i++ { // lsof が見えるまで小待ち
		if got = getCwdLsof(cmd.Process.Pid); got != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if g, _ := filepath.EvalSymlinks(got); got != uniDir && g != want {
		t.Fatalf("U+2010 含む cwd が壊れた: got=%q want=%q (bytes=%v)",
			got, want, []byte(got))
	}
	if (ClaudeSession{Cwd: got}).ShortDir() != "claude‐master" {
		t.Fatalf("ShortDir が U+2010 を保持していない: %q",
			(ClaudeSession{Cwd: got}).ShortDir())
	}
}
