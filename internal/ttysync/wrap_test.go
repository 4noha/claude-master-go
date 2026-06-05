package ttysync

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// FakeClock は test 用の時間制御 seam。NewTimer は登録された FakeTimer を
// 返し、Trigger(n) で n 番目の active timer を発火させる。生時刻の進行
// 概念は持たず、明示 Trigger でのみ timer C が flush する＝race-free。
type FakeClock struct {
	mu     sync.Mutex
	timers []*FakeTimer
}

// NewTimer は FakeTimer を返し timers に追加。Stop 後の timer は active=false。
func (c *FakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &FakeTimer{c: make(chan time.Time, 1), active: true}
	c.timers = append(c.timers, t)
	return t
}

// Triggers は active な timer 全てを発火 (1 個ずつ・複数同時発火させたい時)。
func (c *FakeClock) FireAllActive() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if t.active {
			t.active = false
			select {
			case t.c <- time.Now():
				n++
			default:
			}
		}
	}
	return n
}

// ActiveCount は現在 active な timer 数を返す (test 同期用：pump goroutine
// が timer を arm するまで FireAllActive を待たせる)。
func (c *FakeClock) ActiveCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if t.active {
			n++
		}
	}
	return n
}

// FakeTimer は Timer interface 実装。
type FakeTimer struct {
	c      chan time.Time
	active bool
}

func (t *FakeTimer) C() <-chan time.Time { return t.c }
func (t *FakeTimer) Stop()                { t.active = false }

// blockingReader は test 用 src。Push で bytes を流し、Close で EOF を返す。
type blockingReader struct {
	ch     chan []byte
	doneCh chan struct{}
}

func newBlockingReader() *blockingReader {
	return &blockingReader{ch: make(chan []byte, 4), doneCh: make(chan struct{})}
}

func (r *blockingReader) Push(b []byte) { r.ch <- b }
func (r *blockingReader) Close() error  { close(r.doneCh); return nil }

func (r *blockingReader) Read(p []byte) (int, error) {
	select {
	case b, ok := <-r.ch:
		if !ok {
			return 0, io.EOF
		}
		n := copy(p, b)
		return n, nil
	case <-r.doneCh:
		return 0, io.EOF
	}
}

// recordingWriter は test 用 dst。Write 毎の bytes を Calls に追記。
type recordingWriter struct {
	mu    sync.Mutex
	calls [][]byte
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, append([]byte(nil), p...))
	return len(p), nil
}

func (w *recordingWriter) Calls() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([][]byte, len(w.calls))
	copy(out, w.calls)
	return out
}

func (w *recordingWriter) TotalBytes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.calls {
		n += len(c)
	}
	return n
}

// TestPumpBatchesBurstIntoSingleWrite: 同一 idle 期間内に到着した burst
// が 1 Write に集約されることを確認 (flicker 軽減の核心動作)。
func TestPumpBatchesBurstIntoSingleWrite(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() { done <- PumpWithIdle(dst, src, 10*time.Millisecond, clock) }()

	// burst 3 chunk を高速 push (timer は最後の push で reset され続ける)
	src.Push([]byte("AAA"))
	src.Push([]byte("BBB"))
	src.Push([]byte("CCC"))
	// pump goroutine が読みきるまで小さく yield
	waitFor(t, func() bool { return readerEmpty(src) }, time.Second)

	// idle 期間中 (timer 未発火) は dst へ 0 write
	if got := len(dst.Calls()); got != 0 {
		t.Fatalf("idle 中なのに write された: calls=%d", got)
	}

	// timer 発火 → flush
	if clock.FireAllActive() == 0 {
		t.Fatal("active timer 無し")
	}
	waitFor(t, func() bool { return dst.TotalBytes() == 9 }, time.Second)

	calls := dst.Calls()
	if len(calls) != 1 {
		t.Fatalf("burst が 1 write に集約されてない: calls=%d", len(calls))
	}
	if !bytes.Equal(calls[0], []byte("AAABBBCCC")) {
		t.Fatalf("write 内容不一致: got=%q", calls[0])
	}

	// 後片付け
	src.Close()
	if err := <-done; err != nil {
		t.Fatalf("pump return error: %v", err)
	}
}

// TestPumpMultipleBurstsSeparated: 2 つの burst が timer 発火で別 write
// に分離されることを確認。
func TestPumpMultipleBurstsSeparated(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() { done <- PumpWithIdle(dst, src, 10*time.Millisecond, clock) }()

	src.Push([]byte("first"))
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	waitFor(t, func() bool { return dst.TotalBytes() == 5 }, time.Second)

	src.Push([]byte("second"))
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	waitFor(t, func() bool { return dst.TotalBytes() == 11 }, time.Second)

	calls := dst.Calls()
	if len(calls) != 2 {
		t.Fatalf("2 burst が分離されてない: calls=%d", len(calls))
	}
	if !bytes.Equal(calls[0], []byte("first")) || !bytes.Equal(calls[1], []byte("second")) {
		t.Fatalf("write 内容: %q %q", calls[0], calls[1])
	}

	src.Close()
	<-done
}

// TestPumpEOFFlushesRemainder: src が EOF を返した時点で残 buffer が
// 確実に flush される (timer 待たない＝drop しない)。
func TestPumpEOFFlushesRemainder(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() { done <- PumpWithIdle(dst, src, 10*time.Millisecond, clock) }()

	src.Push([]byte("tail"))
	waitFor(t, func() bool { return readerEmpty(src) }, time.Second)
	// EOF 投入 (timer 発火させない)
	src.Close()

	if err := <-done; err != nil {
		t.Fatalf("pump return error: %v", err)
	}
	if dst.TotalBytes() != 4 {
		t.Fatalf("EOF 時に残 buffer flush されてない: bytes=%d", dst.TotalBytes())
	}
}

// TestPumpReadErrorPropagated: src が io.EOF 以外を返したら error を伝搬
// するが、それまでの buffer は flush する。
func TestPumpReadErrorPropagated(t *testing.T) {
	src := &erroringReader{err: errors.New("boom")}
	dst := &recordingWriter{}
	err := PumpWithIdle(dst, src, 10*time.Millisecond, &FakeClock{})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("error 伝搬されてない: %v", err)
	}
}

type erroringReader struct{ err error }

func (r *erroringReader) Read(p []byte) (int, error) { return 0, r.err }

// TestRealClockBasic: RealClock + 実 time.Timer で簡易確認 (production
// path の sanity check)。
func TestRealClockBasic(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}

	done := make(chan error, 1)
	go func() { done <- PumpWithIdle(dst, src, 5*time.Millisecond, RealClock{}) }()

	src.Push([]byte("hello"))
	waitFor(t, func() bool { return dst.TotalBytes() == 5 }, time.Second)

	calls := dst.Calls()
	if len(calls) != 1 || !bytes.Equal(calls[0], []byte("hello")) {
		t.Fatalf("RealClock pump fail: calls=%v", calls)
	}
	src.Close()
	<-done
}

// helpers
func readerEmpty(r *blockingReader) bool {
	return len(r.ch) == 0
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waitFor timeout (%v)", timeout)
}
