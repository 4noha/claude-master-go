package tmuxcc

import (
	"bytes"
	"sync"
	"testing"
)

type fwdBuf struct {
	mu    sync.Mutex
	calls [][]byte
}

func (w *fwdBuf) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, append([]byte(nil), p...))
	return len(p), nil
}

func (w *fwdBuf) Calls() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([][]byte, len(w.calls))
	copy(out, w.calls)
	return out
}

// TestForwarderVerbatim: active pane の bytes は加工ゼロで素通し
// (再描画しない＝frame 境界・cls 密閉を完全保持する中間層の核心)。
func TestForwarderVerbatim(t *testing.T) {
	dst := &fwdBuf{}
	f := NewForwarder(dst)

	frame := []byte("\x1b[?2026h\x1b[?25l\x1b[2J\x1b[9999;1H\x1b[Hhello 日本語\x1b[3;7H\x1b[?25h\x1b[?2026l")
	f.HandleOutput("%5", frame)

	calls := dst.Calls()
	if len(calls) != 1 || !bytes.Equal(calls[0], frame) {
		t.Fatalf("verbatim 転送でない: %q", calls)
	}
}

// TestForwarderAdoptsFirstPane: active 未設定なら最初に出力した pane を
// 採用し、他 pane は破棄。
func TestForwarderAdoptsFirstPane(t *testing.T) {
	dst := &fwdBuf{}
	f := NewForwarder(dst)

	f.HandleOutput("%1", []byte("first"))
	f.HandleOutput("%2", []byte("other")) // 非 active → 破棄
	f.HandleOutput("%1", []byte("again"))

	calls := dst.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls=%d", len(calls))
	}
	if !bytes.Equal(calls[0], []byte("first")) || !bytes.Equal(calls[1], []byte("again")) {
		t.Fatalf("active filter 不正: %q", calls)
	}
	if f.Active() != "%1" {
		t.Fatalf("active=%q", f.Active())
	}
}

// TestForwarderSetActive: 明示設定が adopt より優先。
func TestForwarderSetActive(t *testing.T) {
	dst := &fwdBuf{}
	f := NewForwarder(dst)
	f.SetActive("%9")
	f.HandleOutput("%1", []byte("ignored"))
	f.HandleOutput("%9", []byte("shown"))
	calls := dst.Calls()
	if len(calls) != 1 || !bytes.Equal(calls[0], []byte("shown")) {
		t.Fatalf("SetActive filter 不正: %q", calls)
	}
}
