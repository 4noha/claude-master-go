package agent

// restart-proxy オーケストレーション＆jsonl→UUID 解決の決定論検証。
// 実 kill/spawn は seam、UUID 解決は t.TempDir の実 FS（合成でなく実
// ファイル走査）。claude --resume 自体は ptyproxy resume-burst fixture
// で別途担保済。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsResumable(t *testing.T) {
	ok := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"0a1b2c3d-4e5f-1111-2222-333344445555",
	}
	ng := []string{"pid-1234", "", "abc", "zzzzzzzz-0000-",
		"550E8400-E29B-..." /* 大文字は claude 形でない */}
	for _, s := range ok {
		if !IsResumable(s) {
			t.Errorf("UUID 鍵が復帰可能と判定されない: %q", s)
		}
	}
	for _, s := range ng {
		if IsResumable(s) {
			t.Errorf("非 UUID が復帰可能と誤判定: %q", s)
		}
	}
}

func TestProxyRestarterOrchestration(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	rUUID := "0a1b2c3d-4e5f-1111-2222-333344445555" // 解決された会話 UUID

	t.Run("UUID sid はそのまま kill→spawn(sid)", func(t *testing.T) {
		var kp int
		var su, sc string
		pr := &ProxyRestarter{
			Lookup:      func(string) (int, string, bool) { return 4242, "/p", true },
			ResolveUUID: func(string) (string, bool) { t.Fatal("UUID sid で解決呼ばれた"); return "", false },
			Kill:        func(pid int) error { kp = pid; return nil },
			Spawn:       func(u, c string) error { su, sc = u, c; return nil },
		}
		if err := pr.Restart(context.Background(), uuid); err != nil {
			t.Fatalf("正常系で失敗: %v", err)
		}
		if kp != 4242 || su != uuid || sc != "/p" {
			t.Fatalf("kill/spawn 引数不一致: kp=%d su=%q sc=%q", kp, su, sc)
		}
	})

	t.Run("pid- は cwd から UUID 解決して kill→spawn(解決UUID)", func(t *testing.T) {
		var su, sc, gotCwd string
		pr := &ProxyRestarter{
			Lookup: func(string) (int, string, bool) { return 7, "/work/foo", true },
			ResolveUUID: func(cwd string) (string, bool) {
				gotCwd = cwd
				return rUUID, true
			},
			Kill:  func(int) error { return nil },
			Spawn: func(u, c string) error { su, sc = u, c; return nil },
		}
		if err := pr.Restart(context.Background(), "pid-99"); err != nil {
			t.Fatalf("pid- 解決成功で失敗: %v", err)
		}
		if gotCwd != "/work/foo" {
			t.Fatalf("ResolveUUID へ渡る cwd 不一致: %q", gotCwd)
		}
		if su != rUUID || sc != "/work/foo" {
			t.Fatalf("spawn は解決 UUID/cwd であるべき: su=%q sc=%q", su, sc)
		}
	})

	t.Run("pid- 解決失敗は kill せず保全", func(t *testing.T) {
		killed, spawned := 0, 0
		pr := &ProxyRestarter{
			Lookup:      func(string) (int, string, bool) { return 7, "/x", true },
			ResolveUUID: func(string) (string, bool) { return "", false },
			Kill:        func(int) error { killed++; return nil },
			Spawn:       func(string, string) error { spawned++; return nil },
		}
		if err := pr.Restart(context.Background(), "pid-99"); err == nil {
			t.Fatal("解決失敗が拒否されない（kill して会話喪失リスク）")
		}
		if killed != 0 || spawned != 0 {
			t.Fatalf("解決失敗で副作用: kill=%d spawn=%d", killed, spawned)
		}
	})

	t.Run("pid- で ResolveUUID 未配線は拒否(kill せず)", func(t *testing.T) {
		killed := 0
		pr := &ProxyRestarter{
			Lookup: func(string) (int, string, bool) { return 7, "/x", true },
			Kill:   func(int) error { killed++; return nil },
			Spawn:  func(string, string) error { return nil },
		}
		if err := pr.Restart(context.Background(), "pid-99"); err == nil {
			t.Fatal("ResolveUUID 未配線が拒否されない")
		}
		if killed != 0 {
			t.Fatalf("未配線で kill 発火: %d", killed)
		}
	})

	t.Run("未発見(Lookup false)は kill せず拒否", func(t *testing.T) {
		killed := 0
		pr := &ProxyRestarter{
			Lookup: func(string) (int, string, bool) { return 0, "", false },
			Kill:   func(int) error { killed++; return nil },
			Spawn:  func(string, string) error { return nil },
		}
		if err := pr.Restart(context.Background(), uuid); err == nil {
			t.Fatal("未発見が拒否されない")
		}
		if killed != 0 {
			t.Fatalf("未発見で kill 発火: %d", killed)
		}
	})

	t.Run("未配線(Lookup/Kill/Spawn nil)は拒否", func(t *testing.T) {
		if err := (&ProxyRestarter{}).Restart(context.Background(), uuid); err == nil {
			t.Fatal("未配線が拒否されない")
		}
	})

	t.Run("kill 失敗なら spawn しない", func(t *testing.T) {
		spawned := 0
		pr := &ProxyRestarter{
			Lookup: func(string) (int, string, bool) { return 7, "/x", true },
			Kill:   func(int) error { return errors.New("perm") },
			Spawn:  func(string, string) error { spawned++; return nil },
		}
		if err := pr.Restart(context.Background(), uuid); err == nil {
			t.Fatal("kill 失敗が伝播しない")
		}
		if spawned != 0 {
			t.Fatalf("kill 失敗後に spawn 発火: %d", spawned)
		}
	})
}

func TestResolveClaudeUUID(t *testing.T) {
	root := t.TempDir()
	mk := func(projDir, name, cwd string, modUnix int64) {
		d := filepath.Join(root, projDir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		fp := filepath.Join(d, name)
		// 1行目=cwd 無し、2行目=権威 cwd（jsonlCwd が先頭30件走査を検証）
		body := `{"type":"summary","sessionId":"x"}` + "\n" +
			`{"type":"user","isMeta":true,"cwd":` + jsonStr(cwd) +
			`,"sessionId":"x"}` + "\n"
		if err := os.WriteFile(fp, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		ts := time.Unix(modUnix, 0)
		if err := os.Chtimes(fp, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	uOld := "11111111-2222-3333-4444-555555555555"
	uNew := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	uBar := "99999999-8888-7777-6666-555555555555"
	mk("projA", uOld+".jsonl", "/work/foo", 1000)         // 古い・cwd=foo
	mk("projA", uNew+".jsonl", "/work/foo", 2000)         // 新しい・cwd=foo ← 勝つ
	mk("projB", uBar+".jsonl", "/work/bar", 3000)         // cwd=bar
	mk("projA", "notuuid.jsonl", "/work/foo", 9999)       // uuid 形でない→無視
	mk("projA", "12345678.jsonl", "/work/foo", 9999)      // uuid 形でない→無視

	if got, ok := ResolveClaudeUUID(root, "/work/foo"); !ok || got != uNew {
		t.Fatalf("cwd=/work/foo は最新 mtime の %s を返すべき: got=%q ok=%v", uNew, got, ok)
	}
	// 末尾スラッシュ正規化
	if got, ok := ResolveClaudeUUID(root, "/work/foo/"); !ok || got != uNew {
		t.Fatalf("末尾スラッシュ正規化されない: got=%q ok=%v", got, ok)
	}
	if got, ok := ResolveClaudeUUID(root, "/work/bar"); !ok || got != uBar {
		t.Fatalf("cwd=/work/bar 解決失敗: got=%q ok=%v", got, ok)
	}
	if got, ok := ResolveClaudeUUID(root, "/work/none"); ok || got != "" {
		t.Fatalf("該当 cwd 無しは ok=false のはず: got=%q ok=%v", got, ok)
	}
	if _, ok := ResolveClaudeUUID(filepath.Join(root, "nope"), "/work/foo"); ok {
		t.Fatal("projects root 不在で ok=true")
	}
}

// jsonStr は最小の JSON 文字列リテラル化（テスト固定値用）。
func jsonStr(s string) string { return `"` + s + `"` }
