package ttysync

import (
	"bytes"
	"os"
	"testing"
	"time"
)

// ---- stripSyncMarkers 単体 ----

func TestStripSyncMarkers_RemovesCompleteMarkers(t *testing.T) {
	in := []byte("abc\x1b[?2026hdef\x1b[?2026lghi")
	payload, carry := stripSyncMarkers(in)
	if !bytes.Equal(payload, []byte("abcdefghi")) {
		t.Fatalf("payload: %q", payload)
	}
	if carry != nil {
		t.Fatalf("carry should be nil: %q", carry)
	}
}

func TestStripSyncMarkers_PreservesOtherEscapes(t *testing.T) {
	in := []byte("\x1b[31mRED\x1b[0m\x1b[2J\x1b[1;2H")
	payload, carry := stripSyncMarkers(in)
	if !bytes.Equal(payload, in) {
		t.Fatalf("non-marker escapes must be preserved: %q", payload)
	}
	if carry != nil {
		t.Fatalf("carry: %q", carry)
	}
}

func TestStripSyncMarkers_CarriesTailPrefix(t *testing.T) {
	// 末尾が marker の途中で切れている → carry
	in := []byte("abc\x1b[?20")
	payload, carry := stripSyncMarkers(in)
	if !bytes.Equal(payload, []byte("abc")) {
		t.Fatalf("payload: %q", payload)
	}
	if !bytes.Equal(carry, []byte("\x1b[?20")) {
		t.Fatalf("carry: %q", carry)
	}
	// carry + 続き で再結合すると marker が完成して除去される
	recombined := append(carry, []byte("26hdef")...)
	payload2, carry2 := stripSyncMarkers(recombined)
	if !bytes.Equal(payload2, []byte("def")) {
		t.Fatalf("recombined payload: %q", payload2)
	}
	if carry2 != nil {
		t.Fatalf("recombined carry: %q", carry2)
	}
}

func TestStripSyncMarkers_NonMarkerEscTailNotCarried(t *testing.T) {
	// "\x1b[3" は marker prefix ではない ("\x1b[?" でない) → carry せず
	// そのまま payload に残る
	in := []byte("abc\x1b[3")
	payload, carry := stripSyncMarkers(in)
	if !bytes.Equal(payload, in) {
		t.Fatalf("payload: %q", payload)
	}
	if carry != nil {
		t.Fatalf("carry: %q", carry)
	}
}

func TestStripSyncMarkers_SimilarButDifferentSeqPreserved(t *testing.T) {
	// "\x1b[?2026" + h/l 以外 → marker ではない → 無加工で残す
	in := []byte("\x1b[?2026$y")
	payload, carry := stripSyncMarkers(in)
	if !bytes.Equal(payload, in) {
		t.Fatalf("payload: %q", payload)
	}
	if carry != nil {
		t.Fatalf("carry: %q", carry)
	}
}

// ---- PumpWithIdleConfig SyncWrap 統合 (FakeClock 決定論) ----

// TestSyncWrapFlushWrapsAndStrips: 内側 marker 入り burst が
// 「strip + 全体 BSU/ESU wrap」の 1 write になる。
func TestSyncWrapFlushWrapsAndStrips(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond, SyncWrap: true}, clock)
	}()

	src.Push([]byte("abc\x1b[?2026hdef\x1b[?2026lghi"))
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	waitFor(t, func() bool { return dst.TotalBytes() > 0 }, time.Second)

	calls := dst.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 write, got %d", len(calls))
	}
	want := []byte("\x1b[?2026habcdefghi\x1b[?2026l")
	if !bytes.Equal(calls[0], want) {
		t.Fatalf("wrapped flush mismatch\n got=%q\nwant=%q", calls[0], want)
	}

	src.Close()
	<-done
}

// TestSyncWrapMarkerOnlyBufferSkipsWrite: marker だけの burst は emit
// する実体が無い＝write しない (空 BSU/ESU の spam 防止)。
func TestSyncWrapMarkerOnlyBufferSkipsWrite(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond, SyncWrap: true}, clock)
	}()

	src.Push([]byte("\x1b[?2026h\x1b[?2026l"))
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	// しばらく待っても write されない
	time.Sleep(50 * time.Millisecond)
	if got := len(dst.Calls()); got != 0 {
		t.Fatalf("marker-only buffer should not be written: calls=%d %q",
			got, dst.Calls())
	}

	src.Close()
	<-done
}

// TestSyncWrapIdleHoldsPartialTailUntilEOF: idle 発火時、末尾の不完全
// sequence (marker 途中含む) は emit せず hold する＝ESU/BSU が断片間に
// 挿入されて「26h」等が印字される破壊 (adversarial review 確定 bug) の
// 根治。hold した tail は EOF で raw 後置 emit (後続 BSU 無し＝安全)。
func TestSyncWrapIdleHoldsPartialTailUntilEOF(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond, SyncWrap: true}, clock)
	}()

	src.Push([]byte("abc\x1b[?20")) // 末尾 = marker (CSI) 途中
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive() // idle 発火
	waitFor(t, func() bool { return dst.TotalBytes() > 0 }, time.Second)

	// head "abc" だけが wrap されて出る (tail は hold)
	calls := dst.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls=%d", len(calls))
	}
	if !bytes.Equal(calls[0], []byte("\x1b[?2026habc\x1b[?2026l")) {
		t.Fatalf("idle flush should emit head only\n got=%q", calls[0])
	}

	// EOF で tail が raw 後置 emit される (bytes 喪失なし)
	src.Close()
	<-done
	calls = dst.Calls()
	if len(calls) != 2 {
		t.Fatalf("EOF 後 calls=%d", len(calls))
	}
	if !bytes.Equal(calls[1], []byte("\x1b[?20")) {
		t.Fatalf("EOF should emit held tail raw\n got=%q", calls[1])
	}
}

// TestSyncWrapNeverSplitsCSIAcrossFlush: CSI 途中で flush 境界が来ても
// sequence が分断されない (前半 flush は head のみ・後半 flush で完全な
// CSI が 1 つの wrap 内に入る)。「…\x1b[38;5;2」+「13m…」が
// 「\x1b[?2026l」を挟んで切れて "13m" が印字される実 bug の回帰検知。
func TestSyncWrapNeverSplitsCSIAcrossFlush(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond, SyncWrap: true}, clock)
	}()

	src.Push([]byte("abc\x1b[38;5;2")) // SGR 途中で切れる
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	waitFor(t, func() bool { return dst.TotalBytes() > 0 }, time.Second)

	calls := dst.Calls()
	if !bytes.Equal(calls[0], []byte("\x1b[?2026habc\x1b[?2026l")) {
		t.Fatalf("1st flush must hold partial CSI\n got=%q", calls[0])
	}

	src.Push([]byte("13mdef")) // CSI 完成 + 続き
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	waitFor(t, func() bool { return len(dst.Calls()) >= 2 }, time.Second)

	calls = dst.Calls()
	want := []byte("\x1b[?2026h\x1b[38;5;213mdef\x1b[?2026l")
	if !bytes.Equal(calls[1], want) {
		t.Fatalf("2nd flush must contain intact CSI\n got=%q\nwant=%q",
			calls[1], want)
	}

	src.Close()
	<-done
}

// TestSyncWrapNeverSplitsUTF8AcrossFlush: UTF-8 多バイト rune の途中で
// flush 境界が来ても rune が分断されない (分断されると U+FFFD 化)。
func TestSyncWrapNeverSplitsUTF8AcrossFlush(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond, SyncWrap: true}, clock)
	}()

	// "あ" = e3 81 82 の先頭 2 bytes で切る
	src.Push([]byte{'o', 'k', 0xe3, 0x81})
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	waitFor(t, func() bool { return dst.TotalBytes() > 0 }, time.Second)

	calls := dst.Calls()
	if !bytes.Equal(calls[0], []byte("\x1b[?2026hok\x1b[?2026l")) {
		t.Fatalf("1st flush must hold partial rune\n got=%q", calls[0])
	}

	src.Push([]byte{0x82, '!'}) // rune 完成 + 続き
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	waitFor(t, func() bool { return len(dst.Calls()) >= 2 }, time.Second)

	calls = dst.Calls()
	want := append(append([]byte("\x1b[?2026h"), []byte("あ!")...), []byte("\x1b[?2026l")...)
	if !bytes.Equal(calls[1], want) {
		t.Fatalf("2nd flush must contain intact rune\n got=%q\nwant=%q",
			calls[1], want)
	}

	src.Close()
	<-done
}

// TestSyncWrapRealTmuxCaptureInvariant: 実 tmux outer capture (ANSI 色 +
// 日本語 + 2J + 45 BSU/ESU pairs + 78% naked の混在 stream) を chunk
// 分割して流し、「全 write から BSU/ESU を除去した連結 == 入力から
// BSU/ESU を除去したもの」(無損失・順序保存・marker 以外は 1 byte も
// 変えない) を機械検証。合成だけで緑にしない (鉄則 #2)。
func TestSyncWrapRealTmuxCaptureInvariant(t *testing.T) {
	raw, err := os.ReadFile("testdata/tmux-outer-real.raw")
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond, SyncWrap: true}, clock)
	}()

	// 決定論的な不規則 chunk 分割 (sequence 境界を意図的に跨がせる)
	sizes := []int{7, 64, 3, 256, 1, 1024, 13, 511}
	pos := 0
	si := 0
	for pos < len(raw) {
		n := sizes[si%len(sizes)]
		si++
		if pos+n > len(raw) {
			n = len(raw) - pos
		}
		src.Push(raw[pos : pos+n])
		pos += n
		// 3 push ごとに idle 発火 = flush 境界を sequence 途中に量産
		if si%3 == 0 {
			waitFor(t, func() bool { return readerEmpty(src) }, time.Second)
			clock.FireAllActive()
		}
	}
	waitFor(t, func() bool { return readerEmpty(src) }, time.Second)
	clock.FireAllActive()
	src.Close()
	if err := <-done; err != nil {
		t.Fatalf("pump error: %v", err)
	}

	// 不変条件: marker 除去後のバイト列が完全一致
	var got []byte
	for _, c := range dst.Calls() {
		got = append(got, c...)
	}
	gotStripped := removeAllMarkers(got)
	wantStripped := removeAllMarkers(raw)
	if !bytes.Equal(gotStripped, wantStripped) {
		// 最初の不一致位置を特定して報告
		n := len(gotStripped)
		if len(wantStripped) < n {
			n = len(wantStripped)
		}
		at := -1
		for i := 0; i < n; i++ {
			if gotStripped[i] != wantStripped[i] {
				at = i
				break
			}
		}
		t.Fatalf("real capture round-trip 不一致: len got=%d want=%d 最初の差異@%d",
			len(gotStripped), len(wantStripped), at)
	}

	// 各 write は「BSU で始まり ESU で終わる」か「EOF の raw tail」のみ
	for i, c := range dst.Calls() {
		if bytes.HasPrefix(c, []byte("\x1b[?2026h")) &&
			!bytes.HasSuffix(c, []byte("\x1b[?2026l")) {
			t.Fatalf("write[%d] BSU 開始なのに ESU 終端でない", i)
		}
	}
}

func removeAllMarkers(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\x1b[?2026h"), nil)
	return bytes.ReplaceAll(b, []byte("\x1b[?2026l"), nil)
}

// TestSyncWrapOffPreservesExactBytes: SyncWrap=false (既定) は従来通り
// 入力 bytes 完全一致で flush (backward compat)。
func TestSyncWrapOffPreservesExactBytes(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond}, clock)
	}()

	in := []byte("abc\x1b[?2026hdef") // marker 入りでも無加工
	src.Push(in)
	waitFor(t, func() bool { return readerEmpty(src) && clock.ActiveCount() >= 1 }, time.Second)
	clock.FireAllActive()
	waitFor(t, func() bool { return dst.TotalBytes() > 0 }, time.Second)

	if calls := dst.Calls(); len(calls) != 1 || !bytes.Equal(calls[0], in) {
		t.Fatalf("SyncWrap off must preserve bytes: %q", calls)
	}

	src.Close()
	<-done
}

// ---- MaxHold backstop (FakeClock 決定論) ----

// TestMaxHoldForcesFlushDuringContinuousStream: idle が一度も発火しない
// 連続 stream でも MaxHold timer 発火で flush される (無限 buffer の
// backstop)。FireDuration で MaxHold timer だけ選択発火する。
func TestMaxHoldForcesFlushDuringContinuousStream(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	idle := 10 * time.Millisecond
	maxHold := 50 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: idle, MaxHold: maxHold}, clock)
	}()

	// 連続 push (毎回 idle timer が reset される状況を模擬)
	src.Push([]byte("aaa"))
	waitFor(t, func() bool { return readerEmpty(src) && clock.CountDuration(maxHold) >= 1 }, time.Second)
	src.Push([]byte("bbb"))
	waitFor(t, func() bool { return readerEmpty(src) && clock.CountDuration(idle) >= 2 }, time.Second)

	// idle は発火させず MaxHold だけ発火 → flush 強制
	if clock.FireDuration(maxHold) == 0 {
		t.Fatal("maxHold timer not armed")
	}
	waitFor(t, func() bool { return dst.TotalBytes() == 6 }, time.Second)

	calls := dst.Calls()
	if len(calls) != 1 || !bytes.Equal(calls[0], []byte("aaabbb")) {
		t.Fatalf("maxHold flush: %q", calls)
	}

	src.Close()
	<-done
}

// TestMaxBufferForcesImmediateFlush: MaxBuffer 超過は timer 非依存で
// 即時 flush。
func TestMaxBufferForcesImmediateFlush(t *testing.T) {
	src := newBlockingReader()
	dst := &recordingWriter{}
	clock := &FakeClock{}

	done := make(chan error, 1)
	go func() {
		done <- PumpWithIdleConfig(dst, src,
			PumpConfig{Idle: 10 * time.Millisecond, MaxBuffer: 10}, clock)
	}()

	src.Push([]byte("0123456789AB")) // 12 bytes >= MaxBuffer 10
	waitFor(t, func() bool { return dst.TotalBytes() == 12 }, time.Second)

	if calls := dst.Calls(); len(calls) != 1 || !bytes.Equal(calls[0], []byte("0123456789AB")) {
		t.Fatalf("maxBuffer flush: %q", calls)
	}

	src.Close()
	<-done
}
