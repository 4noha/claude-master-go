package screen

import (
	"strings"
	"testing"
)

// 下詰め（EnableBottomFill）: follow 中かつ内容 < vrows のとき先頭へ
// 空行を積み内容を下端へ。**カーソル復元は +pad されること**（CLAUDE.md
// の IME カーソル不変条件を破らない）。既定 renderer（host/既存テスト）
// は無効＝従来上詰めのまま（回帰なし）。合成でなく実 VT を使用。
func TestRenderANSIBottomFillShortContent(t *testing.T) {
	// 実 VT: 20x5、CUP(2,1) 後 "abc" → cx=3,cy=1（絶対行 cur=1）。
	mk := func() *VT {
		v := NewModel(20, 5)
		v.Feed([]byte("\x1b[2;1Habc"))
		if x, y := v.Cursor(); x != 3 || y != 1 {
			t.Fatalf("前提崩れ cx=%d cy=%d", x, y)
		}
		return v
	}

	// 既定（bottomFill 無効）= 従来上詰め。total=5<vrows=10 → pad なし、
	// カーソル crow = cur-lastOy+1 = 2 → "2;4"。
	d := NewScrollRenderer()
	fd := d.RenderANSI(mk(), 10, 20)
	if got := trailingCUP(fd); got != "2;4" {
		t.Fatalf("既定は上詰めのまま（回帰）: cursor got=%q want=2;4", got)
	}
	if strings.Contains(string(fd), "\x1b[H\r\n") {
		t.Fatal("既定 renderer に下詰め空行が混入（host/不変条件破壊）")
	}

	// 下詰め有効 = pad=10-5=5。内容は下端、カーソルは +pad で 7;4。
	bf := NewScrollRenderer()
	bf.EnableBottomFill()
	fb := bf.RenderANSI(mk(), 10, 20)
	if got := trailingCUP(fb); got != "7;4" {
		t.Fatalf("下詰めでカーソルが +pad されない（IME 不具合再発）: "+
			"got=%q want=7;4", got)
	}
	if !strings.HasPrefix(string(fb),
		"\x1b[?2026h\x1b[2J\x1b[9999;1H\x1b[H\r\n\r\n\r\n\r\n\r\n") {
		t.Fatalf("先頭 5 空行（下詰め）が積まれていない: %q",
			string(fb)[:40])
	}
	if !strings.Contains(string(fb), "abc") {
		t.Fatal("内容 abc が消えた（無損失でない）")
	}
	if !strings.HasSuffix(string(fb), "\x1b[?2026l") {
		t.Fatal("同期出力の囲いが壊れた")
	}

	// 内容が viewport を充足（vrows=5=total）→ 下詰め有効でも pad=0、
	// 既定と完全一致（充足時は無改変）。
	bf2 := NewScrollRenderer()
	bf2.EnableBottomFill()
	full := bf2.RenderANSI(mk(), 5, 20)
	d2 := NewScrollRenderer()
	if string(full) != string(d2.RenderANSI(mk(), 5, 20)) {
		t.Fatal("充足時に下詰めが余計な差分を出した（pad!=0）")
	}
	if got := trailingCUP(full); got != "2;4" {
		t.Fatalf("充足時カーソルが想定外: got=%q want=2;4", got)
	}
}
