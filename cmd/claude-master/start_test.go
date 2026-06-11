//go:build !windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/scanner"
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

// resolveResumeArgs: args 空 + cwd の jsonl から UUID 解決 → --resume 付与。
// VSCode crash 後の新タブ `claude` で会話継続できる C 案完全自動化の核心。
func TestResolveResumeArgsWithExistingJsonl(t *testing.T) {
	projRoot := t.TempDir()
	cwd := "/Users/4noha/works/some-proj"
	// agent.ResolveClaudeUUID は projectsRoot 配下を走査し jsonl 先頭の
	// cwd field と target を突合せる。サニタイズ規則は逆算不要なので
	// project dir 名は任意の文字列で OK（先頭 30 レコードまでに cwd が
	// 出る jsonl があれば一致）。
	projDir := filepath.Join(projRoot, "-Users-4noha-works-some-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	uuid := "83ed0817-bebf-46d4-b28d-5870a8d3a722"
	jsonl := filepath.Join(projDir, uuid+".jsonl")
	body := `{"cwd":"` + cwd + `","type":"user","isMeta":true}` + "\n"
	if err := os.WriteFile(jsonl, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	args, resumed := resolveResumeArgs([]string{}, cwd, projRoot)
	if resumed != uuid {
		t.Fatalf("resumed=%q want %q", resumed, uuid)
	}
	if len(args) != 2 || args[0] != "--resume" || args[1] != uuid {
		t.Fatalf("args=%v (want [--resume %s])", args, uuid)
	}
}

// resolveResumeArgs: jsonl 不在 → args そのまま (=完全新規セッション)。
func TestResolveResumeArgsNoJsonl(t *testing.T) {
	projRoot := t.TempDir()
	args, resumed := resolveResumeArgs([]string{}, "/no/such/cwd", projRoot)
	if resumed != "" {
		t.Fatalf("resumed should be empty: %q", resumed)
	}
	if len(args) != 0 {
		t.Fatalf("args 改変された: %v (want 空)", args)
	}
}

// resolveResumeArgs: args 非空（user 明示指定）→ touch せず尊重。
// 自動 resume と user 明示指定が衝突しない不変条件。
func TestResolveResumeArgsExistingArgsUntouched(t *testing.T) {
	projRoot := t.TempDir()
	// jsonl 配置しても args 非空なら無視されるべき（明示指定優先）
	input := []string{"--resume", "explicit-uuid"}
	args, resumed := resolveResumeArgs(input, "/anything", projRoot)
	if resumed != "" {
		t.Fatalf("resumed should be empty for non-empty args: %q", resumed)
	}
	if len(args) != 2 || args[0] != "--resume" || args[1] != "explicit-uuid" {
		t.Fatalf("args 改変された: %v", args)
	}
}

// resolveResumeArgs: 同 cwd 複数 jsonl は mtime 最新を選ぶ
// （agent.ResolveClaudeUUID の不変条件・「最古 attach から復帰」を避ける）。
func TestResolveResumeArgsPicksLatestJsonl(t *testing.T) {
	projRoot := t.TempDir()
	cwd := "/Users/4noha/works/multi"
	projDir := filepath.Join(projRoot, "-Users-4noha-works-multi")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := "00000000-1111-1111-1111-000000000000"
	newer := "ffffffff-eeee-eeee-eeee-ffffffffffff"
	body := `{"cwd":"` + cwd + `","type":"user"}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, older+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// 1 秒以上 sleep して mtime に差を付ける（Stat の精度は秒以下も
	// 取れるが、テスト安定化のため明示差）
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(projDir, newer+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, resumed := resolveResumeArgs([]string{}, cwd, projRoot)
	if resumed != newer {
		t.Fatalf("最新 jsonl が選ばれず: %q (want %q)", resumed, newer)
	}
}

// findLiveSessionsInSubtree: cwd 完全一致は除外、子孫のみ列挙、
// updated_at 降順でソート。VSCode crash 後の project root から
// 子 cwd の session を resume する picker の入力源。
func TestFindLiveSessionsInSubtreeBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	root := "/Users/4noha/works/proj"
	sessions := []map[string]any{
		// 完全一致 → 除外されるべき
		{"cwd": root, "key": "k-exact", "updated_at": "2026-05-23 04:00:00"},
		// 子孫 (audio-router) → 採用
		{"cwd": root + "/audio-router", "key": "k-audio", "updated_at": "2026-05-23 05:00:00", "pid": float64(100)},
		// 孫 (deep nest) → 採用
		{"cwd": root + "/sub/deep", "key": "k-deep", "updated_at": "2026-05-23 03:00:00", "pid": float64(200)},
		// 兄弟（root の sibling）→ 除外
		{"cwd": "/Users/4noha/works/other", "key": "k-sibling", "updated_at": "2026-05-23 06:00:00"},
		// prefix だけ似てる別 path（"/proj-similar"）→ 除外
		{"cwd": "/Users/4noha/works/proj-similar", "key": "k-prefix-trap", "updated_at": "2026-05-23 07:00:00"},
		// 親 → 除外
		{"cwd": "/Users/4noha/works", "key": "k-parent", "updated_at": "2026-05-23 02:00:00"},
		// key 空 → 除外（不正データ）
		{"cwd": root + "/empty-key", "key": "", "updated_at": "2026-05-23 01:00:00"},
	}
	b, _ := json.Marshal(map[string]any{"sessions": sessions})
	_ = os.WriteFile(path, b, 0o644)

	got := findLiveSessionsInSubtree(path, root)
	if len(got) != 2 {
		t.Fatalf("件数想定外: %d (want 2)\nresult=%+v", len(got), got)
	}
	// updated_at 降順: audio-router(05:00) > deep(03:00)
	if got[0].Key != "k-audio" {
		t.Fatalf("先頭 key 想定外: %q (want k-audio)", got[0].Key)
	}
	if got[1].Key != "k-deep" {
		t.Fatalf("2 番目 key 想定外: %q (want k-deep)", got[1].Key)
	}
	if got[0].Pid != 100 {
		t.Fatalf("pid 取得失敗: %d", got[0].Pid)
	}
}

// findLiveSessionsInSubtree: 不在/壊れ STATUS_FILE は nil（空）。
func TestFindLiveSessionsInSubtreeMissingOrBroken(t *testing.T) {
	if got := findLiveSessionsInSubtree("/no/such/file", "/x"); got != nil {
		t.Fatalf("不在で nil 期待: %+v", got)
	}
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.json")
	_ = os.WriteFile(broken, []byte("{not json"), 0o644)
	if got := findLiveSessionsInSubtree(broken, "/x"); got != nil {
		t.Fatalf("壊れで nil 期待: %+v", got)
	}
}

// findLiveSessionsInSubtree: trailing slash 正規化（cwd="/foo/" でも
// "/foo/bar" を子孫と認識）。
func TestFindLiveSessionsInSubtreeNormalizesTrailingSlash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	sessions := []map[string]any{
		{"cwd": "/foo/bar", "key": "k1", "updated_at": "2026-01-01"},
	}
	b, _ := json.Marshal(map[string]any{"sessions": sessions})
	_ = os.WriteFile(path, b, 0o644)
	for _, root := range []string{"/foo", "/foo/", "/foo//"} {
		got := findLiveSessionsInSubtree(path, root)
		if len(got) != 1 || got[0].Key != "k1" {
			t.Fatalf("root=%q で見つからず: %+v", root, got)
		}
	}
}

// pickSubtreeCandidate: 入力 → index の純関数規律
//
//	"" (Enter)     → 0 (default = updated_at 最新)
//	"0"/"n"/"N"   → -1 (新規 spawn)
//	"1".."count"  → index 数値-1
//	範囲外/parse 失敗 → -1 (安全側)
func TestPickSubtreeCandidate(t *testing.T) {
	cases := []struct {
		in    string
		count int
		want  int
	}{
		{"", 3, 0},
		{"\n", 3, 0}, // trim 後 ""
		{" ", 3, 0},
		{"0", 3, -1},
		{"n", 3, -1},
		{"N", 3, -1},
		{"1", 3, 0},
		{"2", 3, 1},
		{"3", 3, 2},
		{"4", 3, -1}, // 範囲外 → 新規
		{"-1", 3, -1},
		{"abc", 3, -1}, // parse fail
		{"  2  ", 3, 1}, // trim
	}
	for _, c := range cases {
		got := pickSubtreeCandidate(c.in, c.count)
		if got != c.want {
			t.Fatalf("pickSubtreeCandidate(%q, %d) = %d want %d", c.in, c.count, got, c.want)
		}
	}
}

// findLiveManagedByUUID: STATUS_FILE 非依存の dup 防止 backstop。
// 「UUID 一致 かつ <pid>.sock あり」のみ live 扱い。scan エラーは不検出
// （呼び元が従来 spawn 経路へ安全 fallback）。scanner 自体は実 ps/lsof
// の既存テストが担保＝ここは選別ロジックを seam 注入で検証。
func TestFindLiveManagedByUUID(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{SessionsDir: tmp}
	orig := startScanSessions
	defer func() { startScanSessions = orig }()

	startScanSessions = func(bool) ([]scanner.ClaudeSession, error) {
		return []scanner.ClaudeSession{
			{Pid: 111, SessionID: "uuid-no-sock"},
			{Pid: 222, SessionID: "uuid-with-sock"},
		}, nil
	}

	// sock 無し → 不検出（素 claude / 死んだ proxy を live 扱いしない）
	if _, ok := findLiveManagedByUUID(cfg, "uuid-no-sock"); ok {
		t.Fatal("sock 無しの session を live 扱いした")
	}
	// sock あり → 検出
	if err := os.WriteFile(filepath.Join(tmp, "222.sock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	pid, ok := findLiveManagedByUUID(cfg, "uuid-with-sock")
	if !ok || pid != 222 {
		t.Fatalf("live session 検出失敗: pid=%d ok=%v", pid, ok)
	}
	// 不一致 UUID / 空 UUID → 不検出
	if _, ok := findLiveManagedByUUID(cfg, "uuid-unknown"); ok {
		t.Fatal("無関係 UUID を検出した")
	}
	if _, ok := findLiveManagedByUUID(cfg, ""); ok {
		t.Fatal("空 UUID を検出した")
	}
	// scan エラー → 不検出（spawn 経路へ fallback）
	startScanSessions = func(bool) ([]scanner.ClaudeSession, error) {
		return nil, errors.New("ps timeout (injected)")
	}
	if _, ok := findLiveManagedByUUID(cfg, "uuid-with-sock"); ok {
		t.Fatal("scan エラー時に live 扱いした")
	}
}
