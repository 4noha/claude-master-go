package screen

import (
	"regexp"
	"strings"
	"testing"
)

// 末尾の同期終了直前に出るカーソル復元 CUP を取り出す（無ければ ""）。
var cupRe = regexp.MustCompile(`\x1b\[(\d+);(\d+)H\x1b\[\?2026l$`)

func trailingCUP(frame []byte) string {
	m := cupRe.FindSubmatch(frame)
	if m == nil {
		return ""
	}
	return string(m[1]) + ";" + string(m[2])
}

// RenderANSI はフレーム末尾で物理カーソルを VT モデルのカーソル位置へ
// 復元しなければならない（しないと右下に残り IME preedit がそこへ出て
// 日本語入力不能）。全角は表示桁で数える（rune 数ではない）こと。
func TestRenderANSIRestoresCursorIncludingWideChars(t *testing.T) {
	s := NewScrollRenderer()

	// 半角: CUP(2,1) 後 "abc" → cx=3,cy=1 → 期待 \x1b[2;4H
	v1 := NewModel(20, 5)
	v1.Feed([]byte("\x1b[2;1Habc"))
	if x, y := v1.Cursor(); x != 3 || y != 1 {
		t.Fatalf("前提崩れ(半角) cx=%d cy=%d", x, y)
	}
	if got := trailingCUP(s.RenderANSI(v1, 5, 20)); got != "2;4" {
		t.Fatalf("半角カーソル復元が誤り: got=%q want=2;4", got)
	}

	// 全角: CUP(3,1) 後 "あいう"(各 2 桁) → cx=6,cy=2 → 期待 \x1b[3;7H
	// （rune 数 3 で数えると誤って 3;4 になる＝表示桁で数える検証）。
	s2 := NewScrollRenderer()
	v2 := NewModel(20, 5)
	v2.Feed([]byte("\x1b[3;1Hあいう"))
	if x, y := v2.Cursor(); x != 6 || y != 2 {
		t.Fatalf("前提崩れ(全角) cx=%d cy=%d（runewidth 反映?）", x, y)
	}
	got := trailingCUP(s2.RenderANSI(v2, 5, 20))
	if got != "3;7" {
		t.Fatalf("全角カーソル復元が誤り: got=%q want=3;7（rune 数なら誤 3;4）", got)
	}

	// フレーム構造（同期囲い・クリア規律）が壊れていないこと。
	frame := string(s2.RenderANSI(v2, 5, 20))
	if !strings.HasPrefix(frame, "\x1b[?2026h\x1b[2J\x1b[9999;1H\x1b[H") ||
		!strings.HasSuffix(frame, "\x1b[?2026l") {
		t.Fatalf("フレーム構造が崩れた: %q", frame)
	}
}

// nav 遡り中（カーソル行が viewport 外）は従来どおりカーソル復元を
// 出さない（読書中で IME 非使用。出すと遡り表示上で誤位置になる）。
func TestRenderANSINoCursorWhenScrolledOffViewport(t *testing.T) {
	s := NewScrollRenderer()
	v := NewModel(20, 4)
	// 4 行画面に 30 行流して history を作る（カーソルは最下部付近）。
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("line\r\n")
	}
	v.Feed([]byte(sb.String()))
	s.RenderANSI(v, 4, 20) // lastMaxOy/lastOy を確定
	s.Scroll(-100)         // 最上部まで遡る（follow 解除）
	frame := s.RenderANSI(v, 4, 20)
	if got := trailingCUP(frame); got != "" {
		t.Fatalf("viewport 外なのにカーソル復元を出した: %q", got)
	}
	if !strings.HasSuffix(string(frame), "\x1b[?2026l") {
		t.Fatalf("同期終了が無い: %q", string(frame))
	}
}
