// Package screen は claude 出力の忠実 VT モデル（M2）。
//
// 評価段階: VT パース/可視 buffer は vt10x を使い、pyte の可視 buffer と
// 実録画(resume-burst)で一致するかを検証する。history.top（スクロール
// アウト確定行）と先頭アンカーは別層で自前実装する（vt10x は scrollback
// を持たないため）。一致しなければ別ライブラリ/自前 VT へフォールバック。
package screen

import (
	"strings"

	"github.com/hinshun/vt10x"
)

// VT は vt10x の薄いラッパ。可視 buffer をテキスト行で取り出す。
type VT10x struct {
	t          vt10x.Terminal
	cols, rows int
}

func NewVT10x(cols, rows int) *VT10x {
	return &VT10x{
		t:    vt10x.New(vt10x.WithSize(cols, rows)),
		cols: cols, rows: rows,
	}
}

// Feed は claude 出力バイトを流し込む。
func (v *VT10x) Feed(p []byte) { _, _ = v.t.Write(p) }

// VisibleLines は現在の可視 buffer を行テキストで返す（pyte 側 display_
// oracle と同じく rstrip。Char==0 は空白扱い）。
func (v *VT10x) VisibleLines() []string {
	out := make([]string, v.rows)
	for y := 0; y < v.rows; y++ {
		var b strings.Builder
		for x := 0; x < v.cols; x++ {
			g := v.t.Cell(x, y)
			if g.Char == 0 {
				b.WriteByte(' ')
			} else {
				b.WriteRune(g.Char)
			}
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}
