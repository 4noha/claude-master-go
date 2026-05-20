package diag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// CountingWriter は lossless passthrough（裏 io.Writer の出力と完全
// 一致）でかつ atomic にカウンタと最終時刻が進む。VSCode 端末への書込
// ホットパスに挿入するため正確性は必須。
func TestCountingWriterLosslessAndAccurate(t *testing.T) {
	var buf bytes.Buffer
	var cnt atomic.Int64
	var ts atomic.Int64
	w := WrapWriter(&buf, &cnt, &ts)
	parts := [][]byte{[]byte("hello "), []byte("world"), []byte{0xff, 0xfe, 0x00}}
	want := append(append([]byte{}, parts[0]...), append(parts[1], parts[2]...)...)
	t0 := time.Now().UnixNano()
	for _, p := range parts {
		n, err := w.Write(p)
		if err != nil || n != len(p) {
			t.Fatalf("write %q: n=%d err=%v", p, n, err)
		}
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("バイト経路が改変された: got %q want %q", buf.Bytes(), want)
	}
	if got, want := cnt.Load(), int64(len(want)); got != want {
		t.Fatalf("カウンタ不一致: got=%d want=%d", got, want)
	}
	if got := ts.Load(); got < t0 {
		t.Fatalf("最終時刻が更新されていない: got=%d t0=%d", got, t0)
	}
}

// 失敗書込でカウンタが進まない（partial 書込は実バイト分だけ進む）。
type partialWriter struct{ wrote int }

func (p *partialWriter) Write(b []byte) (int, error) {
	if len(b) > 0 {
		p.wrote += 1
		return 1, errors.New("simulated partial")
	}
	return 0, nil
}

func TestCountingWriterPartialError(t *testing.T) {
	var cnt atomic.Int64
	var ts atomic.Int64
	w := WrapWriter(&partialWriter{}, &cnt, &ts)
	n, err := w.Write([]byte("abc"))
	if n != 1 || err == nil {
		t.Fatalf("partial write 期待: n=%d err=%v", n, err)
	}
	if cnt.Load() != 1 {
		t.Fatalf("partial 後のカウンタは実書込み分: got=%d want=1", cnt.Load())
	}
}

// WriteSnap は MkdirAll＋tmp→rename の原子書出。ファイルが現れ JSON
// パース可能で、設定したカウンタ/extras が反映される。
func TestWriteSnapAtomicAndFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "999.snap")
	c := NewCounters()
	c.HostOutBytes.Store(12345)
	c.HostOutLastNs.Store(time.Now().UnixNano())
	c.ImagePaste.Store(7)
	if err := WriteSnap(path, 4242, c, map[string]any{"k": "v"}); err != nil {
		t.Fatalf("WriteSnap: %v", err)
	}
	// tmp が残らない（rename 後）。
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatalf(".tmp が残っている＝原子書出になっていない")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snap: %v", err)
	}
	var s Snap
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("JSON 不正: %v\n%s", err, b)
	}
	if s.Pid != 4242 || s.HostOut != 12345 || s.ImagePaste != 7 {
		t.Fatalf("fields 不一致: %+v", s)
	}
	if s.HostOutLast == "never" {
		t.Fatalf("時刻が反映されない")
	}
	if v, _ := s.Extras["k"].(string); v != "v" {
		t.Fatalf("extras 不反映: %+v", s.Extras)
	}
	if s.Goroutines <= 0 {
		t.Fatalf("goroutine 数が記録されない: %d", s.Goroutines)
	}
}

// WriteDump は goroutine 全 stack ＋ counters を含むファイルを生成。
// 「all goroutines」マーカと self の関数名を含むことで実 stack 取得を確認。
func TestWriteDumpContainsStacksAndCounters(t *testing.T) {
	dir := t.TempDir()
	c := NewCounters()
	c.MasterBytes.Store(98765)
	path, err := WriteDump(dir, 1234, "signal-hup", c, map[string]any{"why": "test"})
	if err != nil {
		t.Fatalf("WriteDump: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	s := string(b)
	for _, must := range []string{
		"=== all goroutines ===",
		"TestWriteDumpContainsStacksAndCounters", // self 関数名（実 stack）
		"\"master_bytes\": 98765",
		"signal-hup",
		"\"why\": \"test\"",
	} {
		if !strings.Contains(s, must) {
			t.Fatalf("dump に %q が無い:\n%s", must, s)
		}
	}
	// filename サニタイズ（reason の OS 不正文字を弾く）。
	if !strings.Contains(filepath.Base(path), "signal-hup") {
		t.Fatalf("filename に reason が無い: %s", path)
	}
}

func TestSafeReasonSanitizes(t *testing.T) {
	for in, want := range map[string]string{
		"signal-hup": "signal-hup",
		"foo/bar bz": "foo_bar_bz",
		"":           "unknown",
		"x\nY..Z":    "x_Y__Z",
		"日本語":        "___", // rune 反復＝non-ASCII でも 1 rune→1 _（3 rune）
	} {
		if got := safeReason(in); got != want {
			t.Fatalf("safeReason(%q): got=%q want=%q", in, got, want)
		}
	}
}

// StartPeriodicSnap は ctx cancel まで interval 毎に snap を更新。
// 100ms interval で 250ms 待つと初回 + 1〜2 回追加更新が出る。ctx
// cancel 後は新規更新が止まる（mtime 不変）。
func TestStartPeriodicSnapUpdatesAndStops(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "111.snap")
	c := NewCounters()
	ctx, cancel := context.WithCancel(context.Background())
	StartPeriodicSnap(ctx, 50*time.Millisecond, path, 111, c, nil)
	// 初回出現を最大 1s 待つ
	var st os.FileInfo
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s, err := os.Stat(path); err == nil {
			st = s
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st == nil {
		t.Fatalf("snap が出現しない（PeriodicSnap が動いていない）")
	}
	first := st.ModTime()
	// 更新（カウンタ変化）後 mtime が進む
	c.HostOutBytes.Store(42)
	time.Sleep(200 * time.Millisecond)
	st2, _ := os.Stat(path)
	if !st2.ModTime().After(first) {
		t.Fatalf("snap が再書出されない（mtime 不変: %v→%v）", first, st2.ModTime())
	}
	cancel()
	time.Sleep(200 * time.Millisecond)
	st3, _ := os.Stat(path)
	last := st3.ModTime()
	time.Sleep(300 * time.Millisecond)
	st4, _ := os.Stat(path)
	if st4.ModTime().After(last) {
		t.Fatalf("ctx cancel 後も書込が続く（goroutine リーク）: %v→%v", last, st4.ModTime())
	}
	// 内容が valid JSON で counter 42 を含む（最終状態）
	b, _ := os.ReadFile(path)
	var s Snap
	if err := json.Unmarshal(b, &s); err != nil || s.HostOut != 42 {
		t.Fatalf("最終 snap が想定外: err=%v snap=%+v", err, s)
	}
}

// FatalSignals は OS 別に **非空**（unix=3 / windows=2）でなければ
// signal 補足が機能しない＝診断機構の根幹が無効化される。
func TestFatalSignalsNonEmpty(t *testing.T) {
	sigs := FatalSignals()
	if len(sigs) == 0 {
		t.Fatalf("FatalSignals が空＝signal 捕捉が無効")
	}
}

// Cwd フィールド: SetStartCwd で 1 度書いた値が以後の snap に乗る。
// 起動時 cwd を残すことで、SIGHUP 連鎖で多 proxy 同時死亡した時に
// dump だけで「どの VSCode タブの claude だったか」を決定論的に特定
// 可能化する（2026-05-20 の事故で 60874 のタブ特定が不能だった盲点解消）。
func TestSnapCwdPropagatesFromSetStartCwd(t *testing.T) {
	// 既定（未設定 or 空）では omitempty で JSON に出ない
	SetStartCwd("")
	dir := t.TempDir()
	p1 := dir + "/empty.snap"
	if err := WriteSnap(p1, 1, NewCounters(), nil); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(p1)
	if strings.Contains(string(b1), "\"cwd\":") {
		t.Fatalf("空 cwd で field 出力（omitempty 失敗）: %s", b1)
	}
	// 設定後は snap に必ず出る
	SetStartCwd("/Users/4noha/works/test-cwd")
	defer SetStartCwd("") // テスト後始末
	p2 := dir + "/with-cwd.snap"
	if err := WriteSnap(p2, 1, NewCounters(), nil); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(p2)
	var s Snap
	if err := json.Unmarshal(b2, &s); err != nil {
		t.Fatal(err)
	}
	if s.Cwd != "/Users/4noha/works/test-cwd" {
		t.Fatalf("cwd 不反映: got=%q", s.Cwd)
	}
	// Dump にも乗る（troubleshooting 用に snapshot block 内に出る）
	dpath, err := WriteDump(dir, 1, "test", NewCounters(), nil)
	if err != nil {
		t.Fatal(err)
	}
	dump, _ := os.ReadFile(dpath)
	if !strings.Contains(string(dump), "/Users/4noha/works/test-cwd") {
		t.Fatalf("dump に cwd 含まれず:\n%s", dump)
	}
}

// OnClientConnect/Disconnect: ConnectedClients を atomic 増減、0 到達
// 瞬間に LastDisconnectNs を書く。nil レシーバ safe。
func TestCountersClientConnectDisconnect(t *testing.T) {
	c := NewCounters()
	if got := c.ConnectedClients.Load(); got != 0 {
		t.Fatalf("初期 ConnectedClients=%d want 0", got)
	}
	c.OnClientConnect()
	if got := c.ConnectedClients.Load(); got != 1 {
		t.Fatalf("Connect 後 ConnectedClients=%d want 1", got)
	}
	if got := c.LastDisconnectNs.Load(); got != 0 {
		t.Fatalf("Connect 後 LastDisconnectNs=%d want 0（接続中）", got)
	}
	c.OnClientConnect()
	if got := c.ConnectedClients.Load(); got != 2 {
		t.Fatalf("Connect 2回目 ConnectedClients=%d want 2", got)
	}
	c.OnClientDisconnect()
	if got := c.ConnectedClients.Load(); got != 1 {
		t.Fatalf("Disconnect 1回目 ConnectedClients=%d want 1", got)
	}
	if got := c.LastDisconnectNs.Load(); got != 0 {
		t.Fatalf("まだ接続中なら LastDisconnectNs=0、got=%d", got)
	}
	c.OnClientDisconnect()
	if got := c.ConnectedClients.Load(); got != 0 {
		t.Fatalf("Disconnect 2回目 ConnectedClients=%d want 0", got)
	}
	if c.LastDisconnectNs.Load() == 0 {
		t.Fatalf("0 到達瞬間に LastDisconnectNs 書込されない")
	}
	// nil safe
	var nilC *Counters
	nilC.OnClientConnect()
	nilC.OnClientDisconnect() // panic しない
}

// nil io.Writer 互換: WrapWriter(nil cnt, nil ts) は素通し。
func TestWrapWriterNilCountersPassthrough(t *testing.T) {
	var buf bytes.Buffer
	w := WrapWriter(&buf, nil, nil)
	if w == nil {
		t.Fatalf("nil 返却＝API 破壊")
	}
	if _, err := io.WriteString(w, "ok"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if buf.String() != "ok" {
		t.Fatalf("素通し違反: %q", buf.String())
	}
}
