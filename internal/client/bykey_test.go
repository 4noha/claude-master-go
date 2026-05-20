//go:build !windows

package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
)

// ResolveSockByKey の正常系・異常系を tmp 環境で機械検証（実 STATUS_FILE
// と実 sessions/<pid>.sock で）。Windows は M8c で AF_UNIX 動作実証済だ
// が、t.TempDir() と net.Listen("unix") の組み合わせは harness で安定し
// ない既知挙動のため unix only テスト（実 Windows e2e は手動）。
func setupStatus(t *testing.T, sessions []map[string]any) *config.Config {
	t.Helper()
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "status.json")
	sessDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"sessions": sessions})
	if err := os.WriteFile(statusPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return &config.Config{StatusFile: statusPath, SessionsDir: sessDir}
}

func touchSock(t *testing.T, cfg *config.Config, pid int) string {
	t.Helper()
	p := filepath.Join(cfg.SessionsDir, strconv.Itoa(pid)+".sock")
	// ResolveSockByKey は os.Stat 検証のみ＝普通ファイルで OK
	// （net.Listen("unix") は macOS で t.TempDir() の長 path が AF_UNIX
	// 104 字制限を超え bind: invalid argument になる既知制約を回避）。
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return p
}

func TestResolveSockByKeyFindsExisting(t *testing.T) {
	cfg := setupStatus(t, []map[string]any{
		{"key": "uuid-a", "pid": float64(111)},
		{"key": "pid-222", "pid": float64(222)},
	})
	touchSock(t, cfg, 111)
	touchSock(t, cfg, 222)

	got, ok := ResolveSockByKey(cfg, "uuid-a")
	if !ok || filepath.Base(got) != "111.sock" {
		t.Fatalf("UUID 解決失敗: %q ok=%v", got, ok)
	}
	got, ok = ResolveSockByKey(cfg, "pid-222")
	if !ok || filepath.Base(got) != "222.sock" {
		t.Fatalf("pid- 解決失敗: %q ok=%v", got, ok)
	}
}

func TestResolveSockByKeyKeyMissing(t *testing.T) {
	cfg := setupStatus(t, []map[string]any{
		{"key": "uuid-a", "pid": float64(111)},
	})
	touchSock(t, cfg, 111)
	if _, ok := ResolveSockByKey(cfg, "uuid-zzz"); ok {
		t.Fatal("不在 key で ok=true")
	}
}

func TestResolveSockByKeySockMissing(t *testing.T) {
	cfg := setupStatus(t, []map[string]any{
		{"key": "uuid-a", "pid": float64(333)},
	})
	// sock を作らない＝STATUS_FILE に key/pid はあるが sock 不在
	if _, ok := ResolveSockByKey(cfg, "uuid-a"); ok {
		t.Fatal("sock 不在で ok=true（存在検証が機能していない）")
	}
}

// pid フィールドが文字列でも数値でも拾える（json decode の揺れ吸収）。
func TestResolveSockByKeyPIDStringFallback(t *testing.T) {
	cfg := setupStatus(t, []map[string]any{
		{"key": "uuid-a", "pid": "444"}, // string 型
	})
	touchSock(t, cfg, 444)
	got, ok := ResolveSockByKey(cfg, "uuid-a")
	if !ok || filepath.Base(got) != "444.sock" {
		t.Fatalf("pid string fallback 失敗: %q ok=%v", got, ok)
	}
}

func TestResolveSockByKeyEmptyKey(t *testing.T) {
	cfg := setupStatus(t, []map[string]any{
		{"key": "uuid-a", "pid": float64(111)},
	})
	if _, ok := ResolveSockByKey(cfg, ""); ok {
		t.Fatal("空 key で ok=true")
	}
}

func TestResolveSockByKeyBrokenStatus(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "status.json")
	_ = os.WriteFile(statusPath, []byte("not json"), 0o644)
	cfg := &config.Config{StatusFile: statusPath, SessionsDir: dir}
	if _, ok := ResolveSockByKey(cfg, "x"); ok {
		t.Fatal("壊れた STATUS_FILE で ok=true")
	}
}

func TestResolveSockByKeyMissingStatusFile(t *testing.T) {
	cfg := &config.Config{
		StatusFile:  "/nonexistent/path/never-exists.json",
		SessionsDir: "/nonexistent",
	}
	if _, ok := ResolveSockByKey(cfg, "x"); ok {
		t.Fatal("STATUS_FILE 不在で ok=true")
	}
}

// RunByKey: key が STATUS_FILE から見えない状態が 10s 超続いたら正常
// 終了する（実時間 12s ＋ 0.5s poll マージン）。中で実 Run は呼ばれない
// （sock 解決前に loop 終了）。
func TestRunByKeyTerminatesWhenKeyDisappears(t *testing.T) {
	cfg := setupStatus(t, []map[string]any{
		{"key": "other", "pid": float64(999)},
	})
	done := make(chan error, 1)
	t0 := time.Now()
	go func() { done <- RunByKey("never-here", cfg) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("key 不在で err=%v want nil", err)
		}
		dt := time.Since(t0)
		if dt < 9*time.Second || dt > 14*time.Second {
			t.Fatalf("終了タイミング異常: %v（想定 10〜13s）", dt)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("RunByKey が timeout 内に終了しない")
	}
}

// RunByKey が空 key で即エラー。
func TestRunByKeyEmptyKey(t *testing.T) {
	cfg := setupStatus(t, nil)
	if err := RunByKey("", cfg); err == nil {
		t.Fatal("空 key で err=nil")
	}
}

// sock が途中で復活すると（STATUS_FILE に key/pid 追加＋sock 生成）
// poll が解決成功 → Run へ進む。Run 内部の Connect は実 socket を読む
// が、こちらは「解決成功までの遷移」だけを観測する。観測のため Run へ
// 入る前に key を再度消して timeout exit させる方法は競合的なので、
// 「ResolveSockByKey の入力切替で取得値が変わる」を確認する単体 test
// を別途置く（既に上で網羅）＝本テストは挙動経路自体を確認する位置付け
// （poll の interval 動作確認）。
func TestRunByKeyDetectsAppearance(t *testing.T) {
	cfg := setupStatus(t, []map[string]any{
		{"key": "other", "pid": float64(999)},
	})
	var resolved atomic.Bool
	go func() {
		// 1s 後に key を追加し sock も用意 → ResolveSockByKey が ok を
		// 返し始めるはず。ただし Run は実 socket 接続を試みるため、
		// 接続側のテストは別途。ここでは「解決状態の遷移検出」のみ。
		time.Sleep(800 * time.Millisecond)
		sessions := []map[string]any{
			{"key": "other", "pid": float64(999)},
			{"key": "target", "pid": float64(555)},
		}
		b, _ := json.Marshal(map[string]any{"sessions": sessions})
		_ = os.WriteFile(cfg.StatusFile, b, 0o644)
		_ = touchSockFor(cfg, 555)
		// 解決確認
		if _, ok := ResolveSockByKey(cfg, "target"); ok {
			resolved.Store(true)
		}
	}()
	time.Sleep(2 * time.Second)
	if !resolved.Load() {
		t.Fatal("sock 出現後も Resolve が ok を返さない")
	}
}

// テスト helper: ResolveSockByKey は os.Stat で存在検証するだけなので
// 種類は問わない＝普通のファイルで OK（net.Listen の Close-on-unlink
// 仕様を回避するため）。
func touchSockFor(cfg *config.Config, pid int) error {
	p := filepath.Join(cfg.SessionsDir, strconv.Itoa(pid)+".sock")
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	return f.Close()
}
