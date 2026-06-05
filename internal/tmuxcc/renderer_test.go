package tmuxcc

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// recBuf は io.Writer with per-call byte log。各 Write を独立 slice
// として保持＝atomic emit 検証 (writes 数・各 chunk の中身) に使う。
type recBuf struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	calls    [][]byte
	maxWrite int // 最大 1 write の bytes 数
}

func (r *recBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]byte(nil), p...))
	if len(p) > r.maxWrite {
		r.maxWrite = len(p)
	}
	return r.buf.Write(p)
}

func (r *recBuf) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.buf.Bytes()...)
}

func (r *recBuf) Calls() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *recBuf) Writes() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.calls) }

// TestRenderer_SecondEmitOmitsFullClear: 2 回目以降の emit は 2J を含
// まない (blackout 抑止)。初回のみ full RenderANSI (2J 込み) を使い
// outer を clean state にし、以降 RenderANSIIncremental に切替える。
func TestRenderer_SecondEmitOmitsFullClear(t *testing.T) {
	dst := &recBuf{}
	r := NewRenderer(dst, 20, 5)

	// 1 回目: 2J 入り full
	r.HandleOutput("%0", []byte("first\r\n"))
	calls1 := dst.Calls()
	if len(calls1) != 1 {
		t.Fatalf("1st emit count: %d", len(calls1))
	}
	if !bytes.Contains(calls1[0], []byte("\x1b[2J")) {
		t.Fatalf("1st emit should contain 2J for clean state: %q", calls1[0])
	}

	// 2 回目: 2J 無し incremental
	r.HandleOutput("%0", []byte("second\r\n"))
	calls2 := dst.Calls()
	if len(calls2) != 2 {
		t.Fatalf("2nd emit count: %d", len(calls2))
	}
	if bytes.Contains(calls2[1], []byte("\x1b[2J")) {
		t.Fatalf("2nd emit must NOT contain 2J (blackout source): %q", calls2[1])
	}
	// BSU/ESU + ?25l/h は incremental でも残る
	if !bytes.Contains(calls2[1], []byte("\x1b[?2026h")) ||
		!bytes.Contains(calls2[1], []byte("\x1b[?2026l")) {
		t.Fatalf("2nd emit missing BSU/ESU: %q", calls2[1])
	}
	if !bytes.Contains(calls2[1], []byte("\x1b[?25l")) {
		t.Fatalf("2nd emit missing cursor hide: %q", calls2[1])
	}
}

// TestRenderer_SetSizeReArmsFullEmit: SetSize 後の最初の emit は再び
// 2J 入り (clean redraw)。
func TestRenderer_SetSizeReArmsFullEmit(t *testing.T) {
	dst := &recBuf{}
	r := NewRenderer(dst, 20, 5)

	r.HandleOutput("%0", []byte("a"))
	r.HandleOutput("%0", []byte("b"))
	// 2 回目は 2J 無し
	if bytes.Contains(dst.Calls()[1], []byte("\x1b[2J")) {
		t.Fatal("2nd emit had 2J unexpectedly")
	}

	r.SetSize(40, 10)
	r.HandleOutput("%0", []byte("c"))
	// SetSize 後の初回は 2J 入り full
	last := dst.Calls()[len(dst.Calls())-1]
	if !bytes.Contains(last, []byte("\x1b[2J")) {
		t.Fatalf("post-resize 1st emit should be full: %q", last)
	}
}

// TestRenderer_SetActiveReArmsFullEmit: SetActive (pane 切替) でも
// 新 active pane の初回 emit は full (2J 入り)。
func TestRenderer_SetActiveReArmsFullEmit(t *testing.T) {
	dst := &recBuf{}
	r := NewRenderer(dst, 20, 5)

	r.HandleOutput("%0", []byte("a"))
	r.HandleOutput("%0", []byte("b"))
	r.HandleOutput("%1", []byte("c")) // 非 active＝emit せず
	r.SetActive("%1")                 // 切替で full emit
	last := dst.Calls()[len(dst.Calls())-1]
	if !bytes.Contains(last, []byte("\x1b[2J")) {
		t.Fatalf("SetActive 1st emit should be full: %q", last)
	}
}

// TestRenderer_OutputProducesAtomicFrame: proxy frame らしき bytes を
// Feed すると 1 回の Write で BSU/ESU 付き完全 frame が出る。
func TestRenderer_OutputProducesAtomicFrame(t *testing.T) {
	dst := &recBuf{}
	r := NewRenderer(dst, 20, 5)

	// proxy frame っぽい input (cell + BSU/ESU は出力側で screen.VT が
	// 構築する＝input 側は plain text でも OK)
	r.HandleOutput("%0", []byte("Hello\r\nWorld\r\n"))

	if dst.Writes() != 1 {
		t.Fatalf("expected 1 write, got %d", dst.Writes())
	}
	out := dst.Bytes()
	if !bytes.Contains(out, []byte("\x1b[?2026h")) {
		t.Fatal("output missing BSU")
	}
	if !bytes.Contains(out, []byte("\x1b[?2026l")) {
		t.Fatal("output missing ESU")
	}
	if !bytes.Contains(out, []byte("\x1b[?25l")) {
		t.Fatal("output missing cursor hide")
	}
	// 内容に Hello / World が含まれている (cell として描かれている)
	if !bytes.Contains(out, []byte("Hello")) || !bytes.Contains(out, []byte("World")) {
		t.Fatalf("output missing content: %q", out)
	}
}

// TestRenderer_NonActivePaneDoesNotEmit: 非 active pane への %output は
// VT に蓄積されるが Write されない (Active 切替時に最新を描く)。
func TestRenderer_NonActivePaneDoesNotEmit(t *testing.T) {
	dst := &recBuf{}
	r := NewRenderer(dst, 20, 5)

	// %0 が active 初期値になる (first output)
	r.HandleOutput("%0", []byte("first\r\n"))
	w1 := dst.Writes()
	if w1 != 1 {
		t.Fatalf("first output should emit: writes=%d", w1)
	}
	// %1 (非 active) への output は emit しない
	r.HandleOutput("%1", []byte("hidden\r\n"))
	if dst.Writes() != w1 {
		t.Fatalf("non-active output should not emit: writes=%d (expected %d)", dst.Writes(), w1)
	}
	// active 切替で %1 の最新状態が描かれる
	r.SetActive("%1")
	if dst.Writes() != w1+1 {
		t.Fatalf("SetActive should emit: writes=%d", dst.Writes())
	}
	// 直近の出力に "hidden" が描かれているはず (full Bytes に含む)
	out := dst.Bytes()
	if !bytes.Contains(out, []byte("hidden")) {
		t.Fatalf("expected hidden in last output: %q", out)
	}
}

// TestRenderer_RemovePane: pane を削除すると VT も解放、active なら ""
// に。
func TestRenderer_RemovePane(t *testing.T) {
	r := NewRenderer(&recBuf{}, 20, 5)
	r.HandleOutput("%0", []byte("a"))
	r.HandleOutput("%1", []byte("b"))
	if got := len(r.PaneIDs()); got != 2 {
		t.Fatalf("PaneIDs: %d", got)
	}
	r.RemovePane("%0")
	if r.Active() != "" {
		t.Fatalf("Active should be empty after removing active pane: %q", r.Active())
	}
	if got := len(r.PaneIDs()); got != 1 {
		t.Fatalf("PaneIDs after remove: %d", got)
	}
}

// TestRenderer_AtomicWritePerOutput: 高頻度 HandleOutput が 1 output =
// 1 Write になる (frame coalescing は別 layer・wrapper の責務外)。
func TestRenderer_AtomicWritePerOutput(t *testing.T) {
	dst := &recBuf{}
	r := NewRenderer(dst, 80, 24)
	for i := 0; i < 10; i++ {
		r.HandleOutput("%0", []byte("burst"))
	}
	if got := dst.Writes(); got != 10 {
		t.Fatalf("expected 10 writes (1 per output), got %d", got)
	}
	// 1 write は完全 frame (BSU/ESU でラップ・1 syscall に集約)
	if dst.maxWrite < 100 {
		t.Fatalf("frame too small for atomic emit: max=%d", dst.maxWrite)
	}
}

// TestRenderer_PoCFixtureRoundTrip: PoC で取った実 %output data を
// Renderer に流して、出力に元の内容が再現されること。
func TestRenderer_PoCFixtureRoundTrip(t *testing.T) {
	dst := &recBuf{}
	r := NewRenderer(dst, 80, 24)
	// PoC fixture: BSU + SGR + cell + ESU を含む完全 frame
	data := []byte("\x1b[?2026h\x1b[31mhello world\x1b[0m\x1b[?2026l\r\n")
	r.HandleOutput("%0", data)

	out := dst.Bytes()
	// 出力には "hello world" が cell として描画されている
	if !bytes.Contains(out, []byte("hello world")) {
		t.Fatalf("PoC content not rendered: %q", out)
	}
	// 1 atomic Write 内で BSU/ESU で囲まれている
	bsuIdx := strings.Index(string(out), "\x1b[?2026h")
	esuIdx := strings.Index(string(out), "\x1b[?2026l")
	if bsuIdx < 0 || esuIdx < 0 || esuIdx < bsuIdx {
		t.Fatalf("BSU/ESU order broken: bsu=%d esu=%d", bsuIdx, esuIdx)
	}
}
