package tmuxcc

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// recBuf は io.Writer with byte log + write call count。
type recBuf struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	writes   int
	maxWrite int // 最大 1 write の bytes 数
}

func (r *recBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
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

func (r *recBuf) Writes() int { r.mu.Lock(); defer r.mu.Unlock(); return r.writes }

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
