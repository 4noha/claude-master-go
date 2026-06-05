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

// TotalCount は累計 timer 作成数 (test 同期用：「N 回目の timer arm が
// 終わるまで待つ」のに使う＝ActiveCount だけだと前 timer が stop された
// 直後の新規も「1」のままで区別できない)。
func (c *FakeClock) TotalCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
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

// TestHoldAfterDestructiveCollapses: destructive op を含む chunk と
// その後の cell content (timer 1 回発火を跨ぐタイミング) が、hold mode
// により 1 batch に集約されることを確認 (blackout 抑止の核心動作)。
func TestHoldAfterDestructiveCollapses(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond, HoldAfterDestructive: 50 * time.Millisecond},
			clock)
	}()

	// 1) \x1b[2J を push → parser が detect、hold mode arm、timer は
	//    hold 期間 (50ms) で起動。
	src.Push([]byte("\x1b[2J"))
	// pump 処理待ち＋timer arm 確認 (TotalCount で 1 個目作成を待つ)
	waitFor(t, func() bool { return readerEmpty(src) && clock.TotalCount() >= 1 }, time.Second)

	if clock.ActiveCount() != 1 {
		t.Fatalf("hold timer not active: count=%d", clock.ActiveCount())
	}

	// 2) 通常 idle 期間内に cell content を追加。push 後 main goroutine
	//    が処理して新 timer を arm するのを待つ (= TotalCount が 2 に
	//    なるまで)
	src.Push([]byte("cells"))
	waitFor(t, func() bool { return readerEmpty(src) && clock.TotalCount() >= 2 }, time.Second)

	// 3) この時点でまだ flush されていないこと
	if dst.TotalBytes() != 0 {
		t.Fatalf("hold 中なのに flush された: bytes=%d", dst.TotalBytes())
	}

	// 4) hold 期間経過 (timer 発火) → flush
	clock.FireAllActive()
	waitFor(t, func() bool { return dst.TotalBytes() > 0 }, time.Second)

	calls := dst.Calls()
	if len(calls) != 1 {
		t.Fatalf("destructive + cell が 1 batch に集約されてない: calls=%d", len(calls))
	}
	if !bytes.Equal(calls[0], []byte("\x1b[2Jcells")) {
		t.Fatalf("集約内容不一致: got=%q", calls[0])
	}

	src.Close()
	<-done
}

// TestHoldDisabledFallsBackToIdle: HoldAfterDestructive=0 なら従来の純
// idle batch のみ。destructive op が来ても idle (短い方) で flush。
func TestHoldDisabledFallsBackToIdle(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond, HoldAfterDestructive: 0},
			clock)
	}()

	src.Push([]byte("\x1b[2J"))
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	// idle (10ms) のみ＝1 timer
	clock.FireAllActive()
	waitFor(t, func() bool { return dst.TotalBytes() == 4 }, time.Second)

	if got := len(dst.Calls()); got != 1 || !bytes.Equal(dst.Calls()[0], []byte("\x1b[2J")) {
		t.Fatalf("hold 無効時の挙動不一致: calls=%v", dst.Calls())
	}

	src.Close()
	<-done
}

// TestHoldResetsAfterFlush: hold mode は 1 度の flush で解除、次の
// destructive op で再 arm される (= 連続 frame で都度 hold が効く)。
func TestHoldResetsAfterFlush(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond, HoldAfterDestructive: 50 * time.Millisecond},
			clock)
	}()

	// 1st cycle: 2J + content → hold flush
	src.Push([]byte("\x1b[2Jaaa"))
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	waitFor(t, func() bool { return dst.TotalBytes() == 7 }, time.Second)

	// 2nd cycle: 2J + content → 改めて hold で flush (前 cycle で hold
	// は解除されているので、再度 arm されて 1 batch 集約)
	src.Push([]byte("\x1b[2Jbbb"))
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	waitFor(t, func() bool { return dst.TotalBytes() == 14 }, time.Second)

	calls := dst.Calls()
	if len(calls) != 2 {
		t.Fatalf("2 cycle 期待: calls=%d", len(calls))
	}
	if !bytes.Equal(calls[0], []byte("\x1b[2Jaaa")) || !bytes.Equal(calls[1], []byte("\x1b[2Jbbb")) {
		t.Fatalf("cycle 内容: %q %q", calls[0], calls[1])
	}

	src.Close()
	<-done
}

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
