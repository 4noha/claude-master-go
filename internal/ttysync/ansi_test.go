package ttysync

import "testing"

// feedAll は parser に bytes を順次 Feed し、destructive 発火回数を返す。
func feedAll(p *ansiParser, s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if p.Feed(s[i]) {
			n++
		}
	}
	return n
}

// TestPlainTextNotDestructive: printable bytes だけなら fire 0。
func TestPlainTextNotDestructive(t *testing.T) {
	p := &ansiParser{}
	if got := feedAll(p, "hello world\nfoo bar\r\n"); got != 0 {
		t.Fatalf("plain text で fire: %d", got)
	}
}

// TestDetect2J: \x1b[2J 1 件で fire 1。
func TestDetect2J(t *testing.T) {
	p := &ansiParser{}
	if got := feedAll(p, "\x1b[2J"); got != 1 {
		t.Fatalf("fire 数: %d (want 1)", got)
	}
}

// TestDetectBareJ: \x1b[J (cursor→end of screen) も destructive 扱い。
func TestDetectBareJ(t *testing.T) {
	p := &ansiParser{}
	if got := feedAll(p, "\x1b[J"); got != 1 {
		t.Fatalf("fire 数: %d (want 1)", got)
	}
}

// TestDetectK: \x1b[K (line clear) も destructive 扱い (保守的に)。
func TestDetectK(t *testing.T) {
	p := &ansiParser{}
	if got := feedAll(p, "\x1b[K\x1b[2K"); got != 2 {
		t.Fatalf("fire 数: %d (want 2)", got)
	}
}

// TestCSIChunkBoundary: ESC sequence が chunk 境界で割れても state を
// 持ち越して認識できる (PTY read 境界耐性)。
func TestCSIChunkBoundary(t *testing.T) {
	p := &ansiParser{}
	// "\x1b[2J" を 1 byte ずつ Feed
	n := 0
	for _, b := range []byte{0x1b, '[', '2', 'J'} {
		if p.Feed(b) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("chunk 境界跨ぎで fire 数: %d (want 1)", n)
	}
}

// TestSGRNotDestructive: \x1b[31m (色) は fire 0。
func TestSGRNotDestructive(t *testing.T) {
	p := &ansiParser{}
	if got := feedAll(p, "\x1b[31mRED\x1b[0m\x1b[38;2;255;0;0mTC\x1b[39m"); got != 0 {
		t.Fatalf("SGR で fire: %d", got)
	}
}

// TestCUPNotDestructive: \x1b[1;2H (cursor position) は fire 0。
func TestCUPNotDestructive(t *testing.T) {
	p := &ansiParser{}
	if got := feedAll(p, "\x1b[H\x1b[1;1H\x1b[24;80H"); got != 0 {
		t.Fatalf("CUP で fire: %d", got)
	}
}

// TestOSCSkipped: OSC (e.g. window title set) は BEL or ESC\ で抜けて
// 中身は無視＝後続の CSI 認識に影響しない。
func TestOSCSkipped(t *testing.T) {
	p := &ansiParser{}
	// OSC 0; title BEL + \x1b[2J
	stream := "\x1b]0;Title\x07\x1b[2J"
	if got := feedAll(p, stream); got != 1 {
		t.Fatalf("OSC 後の 2J 認識失敗: %d (want 1)", got)
	}
	// OSC を ESC\ で抜ける版
	p2 := &ansiParser{}
	stream2 := "\x1b]0;Title\x1b\\\x1b[2J"
	if got := feedAll(p2, stream2); got != 1 {
		t.Fatalf("OSC(ESC\\) 後の 2J 認識失敗: %d (want 1)", got)
	}
}

// TestDCSSkipped: DCS (e.g. tmux passthrough) は中身無視で後続認識継続。
func TestDCSSkipped(t *testing.T) {
	p := &ansiParser{}
	stream := "\x1bPtmux;hello\x1b\\\x1b[2J"
	if got := feedAll(p, stream); got != 1 {
		t.Fatalf("DCS 後の 2J 認識失敗: %d (want 1)", got)
	}
}

// TestEscRecoverFromUnknown: 不明な ESC 続き (例: ESC = だけ) は state
// を ground に戻して次の ESC を認識する。
func TestEscRecoverFromUnknown(t *testing.T) {
	p := &ansiParser{}
	if got := feedAll(p, "\x1b=\x1b[2J"); got != 1 {
		t.Fatalf("ESC = 後の 2J 認識失敗: %d (want 1)", got)
	}
}

// TestRealTmuxOuterCapture: 実 tmux outer 出力の断片を投入して fire
// 数が想定範囲か確認 (回帰検知用)。10 BSU/ESU + 2J × 2 + K 多数
// の capture を input にして fire 数を期待値範囲で検証。
func TestRealTmuxOuterCapture(t *testing.T) {
	// 実 capture 断片の縮小版（CLAUDE.md の解析と一致するパターン）
	stream := "\x1b[?2026h\x1b[?25l\x1b[H\x1b[2J\x1b[1;1Hcontent\x1b[K\r\n" +
		"\x1b[2;1Hmore\x1b[K\x1b[?25h\x1b[?2026l"
	p := &ansiParser{}
	got := feedAll(p, stream)
	// 2J 1 + K 2 = 3 fire (保守的判定)
	if got != 3 {
		t.Fatalf("real-like capture fire 数: %d (want 3)", got)
	}
}
