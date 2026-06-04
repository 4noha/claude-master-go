package screen

import (
	"fmt"
	"strings"
)

// ScrollRenderer は viewport のスクロール位置を保持し、論理 canvas
// (history.top + 可視 buffer) から viewport 行を切り出す。Python 版
// pty_scroll.ScrollRenderer の **先頭アンカー方式**を忠実移植。
//
// 不変条件（Python で苦労して確立）:
//   - スクロール位置は canvas 先頭(最古=0)からの絶対 anchor。最下部
//     基準だと遡り中に claude 出力で canvas が伸び表示がドリフトする。
//     先頭基準なら末尾追記で view が動かない（tmux copy-mode と同じ）。
//   - 最下部到達で follow（live 追従）へ復帰。
type ScrollRenderer struct {
	follow    bool
	anchor    int // not-follow 時の viewport 先頭絶対 oy
	lastMaxOy int // 直近 render の max_oy（Scroll() が screen 非依存なため）
	lastOy    int // 直近 render の viewport 先頭絶対行（カーソル相対変換用）
}

func NewScrollRenderer() *ScrollRenderer { return &ScrollRenderer{follow: true} }

func (s *ScrollRenderer) FollowActive() bool { return s.follow }

func (s *ScrollRenderer) FollowBottom() { s.follow = true }

// Scroll: dy<0=上(古い方へ遡る), dy>0=下(新しい方へ)。Python 版 scroll
// と同一規則。clamp は ViewLines 時。
func (s *ScrollRenderer) Scroll(dy int) {
	if dy == 0 {
		return
	}
	if s.follow {
		if dy < 0 { // live から上へ遡り始める
			s.anchor = s.lastMaxOy + dy
			if s.anchor < 0 {
				s.anchor = 0
			}
			s.follow = false
		}
		// dy>0 で live 中は何もしない（最下部のまま）
		return
	}
	s.anchor += dy
	if s.anchor >= s.lastMaxOy {
		s.follow = true // 最下部到達 → live 追従
	} else if s.anchor < 0 {
		s.anchor = 0
	}
}

// Scrollback は互換: 「最下部から何行上か」。0=follow。
func (s *ScrollRenderer) Scrollback() int {
	if s.follow {
		return 0
	}
	d := s.lastMaxOy - s.anchor
	if d < 0 {
		return 0
	}
	return d
}

// ViewLines は論理 canvas = hist + vis から viewport(vrows 行)を返す。
// follow なら最下部、さもなくば先頭アンカー位置（末尾追記で不動）。
// 行数が足りない場合は空文字でパディングせず短い slice を返す
// （Python の render も canvas 末満で break）。
func (s *ScrollRenderer) ViewLines(hist, vis []string, vrows int) []string {
	total := len(hist) + len(vis)
	maxOy := total - vrows
	if maxOy < 0 {
		maxOy = 0
	}
	s.lastMaxOy = maxOy

	var oy int
	if s.follow {
		oy = maxOy
	} else {
		oy = s.anchor
		if oy >= maxOy {
			oy = maxOy
			s.follow = true // 最下部に達した → live
		} else if oy < 0 {
			oy = 0
		}
		s.anchor = oy
	}

	line := func(i int) string {
		if i < len(hist) {
			return hist[i]
		}
		j := i - len(hist)
		if j < len(vis) {
			return vis[j]
		}
		return ""
	}
	out := make([]string, 0, vrows)
	for r := 0; r < vrows; r++ {
		L := oy + r
		if L >= total {
			break
		}
		out = append(out, line(L))
	}
	return out
}

// Canvas は VT モデルの論理 canvas（hist + vis）行を返す補助。
func Canvas(v *VT) (hist, vis []string) {
	return v.HistoryLines(), v.VisibleLines()
}

// ---- ANSI 描画（Python pty_scroll._append_row / render_viewport 移植）----

func colorSeq(c color, fg bool) string {
	if c == colDefault {
		return ""
	}
	p := "48"
	if fg {
		p = "38"
	}
	if c&0x1000000 != 0 { // truecolor
		r := byte(c >> 16)
		g := byte(c >> 8)
		b := byte(c)
		return fmt.Sprintf("\x1b[%s;2;%d;%d;%dm", p, r, g, b)
	}
	return fmt.Sprintf("\x1b[%s;5;%dm", p, int(c)-1) // palette
}

// appendRow は 1 行を SGR 付き ANSI で書き出す（継続セル skip・空セルは
// 既定 style の空白・style 変化時のみ reset+再適用。Python と同一手順）。
func appendRow(b *strings.Builder, row []cell, vcols int) {
	cur := style{}
	first := true
	for x := 0; x < vcols; x++ {
		var ch cell
		if x < len(row) {
			ch = row[x]
		}
		if ch.cont {
			continue // 全角継続セルは描かない
		}
		st := ch.st
		data := " "
		if ch.r != 0 {
			data = string(ch.r)
		} else {
			st = style{} // 空セルは既定 style
		}
		if first || st != cur {
			b.WriteString("\x1b[0m")
			if st.bold {
				b.WriteString("\x1b[1m")
			}
			if st.italic {
				b.WriteString("\x1b[3m")
			}
			if st.under {
				b.WriteString("\x1b[4m")
			}
			if st.reverse {
				b.WriteString("\x1b[7m")
			}
			if st.fg != colDefault {
				b.WriteString(colorSeq(st.fg, true))
			}
			if st.bg != colDefault {
				b.WriteString(colorSeq(st.bg, false))
			}
			cur = st
			first = false
		}
		b.WriteString(data)
	}
	b.WriteString("\x1b[0m")
}

// ViewCells は ViewLines と同一 oy 計算で cell 行 viewport を返す。
func (s *ScrollRenderer) ViewCells(hist, vis [][]cell, vrows int) [][]cell {
	total := len(hist) + len(vis)
	maxOy := total - vrows
	if maxOy < 0 {
		maxOy = 0
	}
	s.lastMaxOy = maxOy
	var oy int
	if s.follow {
		oy = maxOy
	} else {
		oy = s.anchor
		if oy >= maxOy {
			oy = maxOy
			s.follow = true
		} else if oy < 0 {
			oy = 0
		}
		s.anchor = oy
	}
	s.lastOy = oy // RenderANSI がカーソル行を viewport 相対へ変換するのに使う
	line := func(i int) []cell {
		if i < len(hist) {
			return hist[i]
		}
		j := i - len(hist)
		if j < len(vis) {
			return vis[j]
		}
		return nil
	}
	out := make([][]cell, 0, vrows)
	for r := 0; r < vrows; r++ {
		L := oy + r
		if L >= total {
			break
		}
		out = append(out, line(L))
	}
	return out
}

// RenderANSI は viewport を同期出力で囲んだ ANSI フレームに（Python
// render_viewport のバイト構造移植: 先頭 \x1b[?2026h、全消去後カーソルを
// 画面最下部へ(_CLEAR_SEQ 規律)、行毎 appendRow、末尾 \x1b[?2026l）。
//
// フレーム描画中はカーソルを `\x1b[?25l` で隠し、復元できる場合のみ末尾で
// `\x1b[?25h` で戻す。理由: DECSET 2026（同期出力）が外側端末まで伝搬
// しない経路では `\x1b[2J\x1b[9999;1H\x1b[H` + 各行描画の間カーソルが
// 各位置で可視のまま描かれ「カーソルが散ってちらつく」事象になる
// （tmux 経由 + sync 非対応外側端末で再現。VSCode terminal 等）。VT モデル
// は DECTCEM (`?25h/l`) をセル非影響として無視する（vt.go csi）ので
// claude の意図と衝突しない（そもそも proxy frame には載っていない）。
// nav 遡りでカーソル復元しないケースは hide のまま末尾 ESU を出す
// （nav 読書中はカーソル不要）。
func (s *ScrollRenderer) RenderANSI(v *VT, vrows, vcols int) []byte {
	rows := s.ViewCells(v.hist, v.buf, vrows)
	var b strings.Builder
	b.WriteString("\x1b[?2026h")        // synchronized output begin
	b.WriteString("\x1b[?25l")          // hide cursor: sync 非対応外側端末でのちらつき防止
	b.WriteString("\x1b[2J\x1b[9999;1H") // _CLEAR_SEQ: 全消去→最下部
	b.WriteString("\x1b[H")
	for i, row := range rows {
		appendRow(&b, row, vcols)
		if i+1 < len(rows) {
			b.WriteString("\r\n")
		}
	}
	// 物理カーソルを VT モデルのカーソル位置へ復元する。これが無いと
	// 描画終了時にカーソルが最終行末尾（≒右下）に残り、IME の preedit
	// がそこに出て日本語入力が事実上不能になる（半角直接入力は preedit
	// が無いため露見しにくいが同じ不具合）。cx は draw() が runewidth
	// で進める表示桁なので全角でも正しい。viewport 外（nav 遡り中）は
	// 出さない＝従来挙動を維持（読書中で IME 非使用）。復元できる時のみ
	// `?25h` で cursor 表示を戻す。nav scrolled-off では hide のまま
	// ESU を出す（cursor 不要・次フレーム live 復帰時に自動で show）。
	cur := len(v.hist) + v.cy  // hist+vis 連結での絶対行
	crow := cur - s.lastOy + 1 // viewport 内 1-based 行
	ccol := v.cx + 1                 // 表示桁 1-based
	if crow >= 1 && crow <= vrows {
		if ccol < 1 {
			ccol = 1
		}
		if ccol > vcols {
			ccol = vcols
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH", crow, ccol)
		b.WriteString("\x1b[?25h") // show cursor: 復元位置が確定したので戻す
	}
	b.WriteString("\x1b[?2026l") // synchronized output end
	return []byte(b.String())
}
