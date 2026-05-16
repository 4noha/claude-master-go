// Package screen の忠実 VT モデル（M2 核）。
//
// pyte.HistoryScreen を「claude が実際に使う sequence 部分集合」に絞って
// 移植（resume-burst 実測: CUF/CUD/CUU/CUB, CUP/CHA, EL/ED, DECSTBM,
// DECSC/RC, SGR=文字非影響で無視, CR/LF, OSC/DA/private-mode=セル非影響）。
// スクロールは LF のみ（ESC D/M 不使用）。scroll 時に index() 意味論で
// 最上行を history へ＝Python 版の心臓部と同一。検証は実録画の pyte
// 既知正(expected_visible/history.txt)に対する byte 一致。
package screen

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

const histMax = 5000 // pyte HistoryScreen(history=5000) と同じ

type cell struct {
	r    rune
	cont bool // 全角の継続セル（pyte の data=="" 相当。文字抽出でスキップ）
}

// VT は claude 出力の忠実画面モデル。history.top + 可視 buffer。
type VT struct {
	cols, rows int
	buf        [][]cell
	cx, cy     int
	top, bot   int // スクロール margin（0-index, inclusive, 既定 full）
	hist       [][]cell
	wrap       bool // 次の描画で行折返し保留（DECAWM, 既定 on）
	scx, scy   int  // DECSC/RC 退避
	// パーサ状態
	st    pstate
	parm  []byte
	osc   bool
	csiPv bool   // CSI ? プライベート
	pend  []byte // チャンク境界で割れた末尾(不完全 UTF-8)の繰越し
}

type pstate int

const (
	stGround pstate = iota
	stEsc
	stCSI
	stOSC
	stOSCEsc // OSC 中に ESC（ST = ESC \）
)

func NewModel(cols, rows int) *VT {
	v := &VT{cols: cols, rows: rows}
	v.buf = blank(cols, rows)
	v.top, v.bot = 0, rows-1
	return v
}

func blank(cols, rows int) [][]cell {
	b := make([][]cell, rows)
	for y := range b {
		b[y] = make([]cell, cols)
	}
	return b
}
func blankLine(cols int) []cell { return make([]cell, cols) }

// Feed は claude 出力バイト列を投入（バイト単位状態機械。pyte と同様
// 不正/未対応はサイレントスキップ）。
func (v *VT) Feed(p []byte) {
	if len(v.pend) > 0 { // 前回末尾の不完全 UTF-8 を前置
		p = append(append([]byte{}, v.pend...), p...)
		v.pend = v.pend[:0]
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch v.st {
		case stGround:
			switch {
			case c == 0x1b:
				v.st = stEsc
			case c == '\r':
				v.cx = 0
				v.wrap = false
			case c == '\n':
				v.lineFeed()
				v.wrap = false
			case c == '\b':
				if v.cx > 0 {
					v.cx--
				}
				v.wrap = false
			case c == '\t':
				v.cx = ((v.cx / 8) + 1) * 8
				if v.cx >= v.cols {
					v.cx = v.cols - 1
				}
			case c == 0x07: // BEL
			case c < 0x20:
				// 他制御は無視
			default:
				if v.putByte(p, &i) {
					return // 末尾不完全 UTF-8 を v.pend に繰越し済
				}
			}
		case stEsc:
			switch c {
			case '[':
				v.st = stCSI
				v.parm = v.parm[:0]
				v.csiPv = false
			case ']':
				v.st = stOSC
			case '7': // DECSC
				v.scx, v.scy = v.cx, v.cy
				v.st = stGround
			case '8': // DECRC
				v.cx, v.cy = v.scx, v.scy
				v.clampCursor()
				v.st = stGround
			case 'D': // IND（未使用だが一応）
				v.lineFeed()
				v.st = stGround
			case 'M': // RI 逆 index
				v.reverseIndex()
				v.st = stGround
			case 'E': // NEL
				v.cx = 0
				v.lineFeed()
				v.st = stGround
			case '=', '>': // DECKPAM/NM 無視
				v.st = stGround
			default:
				v.st = stGround
			}
		case stCSI:
			if c == '?' || c == '>' || c == '=' {
				if c == '?' {
					v.csiPv = true
				}
				continue
			}
			if (c >= '0' && c <= '9') || c == ';' {
				v.parm = append(v.parm, c)
				continue
			}
			v.csi(c)
			v.st = stGround
		case stOSC:
			if c == 0x07 { // BEL 終端
				v.st = stGround
			} else if c == 0x1b {
				v.st = stOSCEsc
			}
			// 本文は捨てる（タイトル等＝セル非影響）
		case stOSCEsc:
			// ESC \ で ST。いずれにせよ OSC 終了
			v.st = stGround
		}
	}
}

// putByte は ground での UTF-8 1文字を取り出して描画。末尾が不完全
// （チャンク境界で割れた多バイト）なら v.pend に繰越して true を返す。
func (v *VT) putByte(p []byte, i *int) (incomplete bool) {
	b := p[*i]
	var r rune
	n := 1
	switch {
	case b < 0x80:
		r = rune(b)
	case b>>5 == 0x6:
		n = 2
	case b>>4 == 0xe:
		n = 3
	case b>>3 == 0x1e:
		n = 4
	default:
		return false // 不正先頭バイト（1 byte スキップ）
	}
	if n == 1 {
		v.draw(r)
		return false
	}
	if *i+n > len(p) {
		// チャンク境界で割れた多バイト。末尾を保存し次 Feed で前置。
		v.pend = append(v.pend[:0], p[*i:]...)
		return true
	}
	r = decodeRune(p[*i : *i+n])
	*i += n - 1
	if r != 0 {
		v.draw(r)
	}
	return false
}

func decodeRune(b []byte) rune {
	rs := []rune(string(b))
	if len(rs) == 0 {
		return 0
	}
	return rs[0]
}

// draw は 1 文字を現在カーソル位置へ。DECAWM(既定 on) で行末折返し。
func (v *VT) draw(r rune) {
	w := runewidth.RuneWidth(r)
	if w == 0 {
		return // 結合文字等は簡略化（pyte も簡略）。実録画では影響軽微
	}
	if v.wrap {
		v.cx = 0
		v.lineFeed()
		v.wrap = false
	}
	if v.cx+w > v.cols {
		// 行末で入りきらない → 折返し
		v.cx = 0
		v.lineFeed()
	}
	v.buf[v.cy][v.cx] = cell{r: r}
	if w == 2 && v.cx+1 < v.cols {
		v.buf[v.cy][v.cx+1] = cell{cont: true}
	}
	v.cx += w
	if v.cx >= v.cols {
		v.cx = v.cols - 1
		v.wrap = true // 次文字で折返し（pyte の wrap 保留）
	}
}

// lineFeed は LF/index。pyte.HistoryScreen.index 同一意味:
// cursor が bottom margin なら buf[top] を history へ push して領域を
// 1 行上へスクロール、さもなくば cursor.y++。
func (v *VT) lineFeed() {
	if v.cy == v.bot {
		// margin 先頭行を history.top へ（コピー）
		line := make([]cell, v.cols)
		copy(line, v.buf[v.top])
		v.hist = append(v.hist, line)
		if len(v.hist) > histMax {
			v.hist = v.hist[len(v.hist)-histMax:]
		}
		for y := v.top; y < v.bot; y++ {
			v.buf[y] = v.buf[y+1]
		}
		v.buf[v.bot] = blankLine(v.cols)
	} else if v.cy < v.rows-1 {
		v.cy++
	}
}

func (v *VT) reverseIndex() {
	if v.cy == v.top {
		for y := v.bot; y > v.top; y-- {
			v.buf[y] = v.buf[y-1]
		}
		v.buf[v.top] = blankLine(v.cols)
	} else if v.cy > 0 {
		v.cy--
	}
}

func (v *VT) params() []int {
	if len(v.parm) == 0 {
		return nil
	}
	parts := strings.Split(string(v.parm), ";")
	out := make([]int, len(parts))
	for i, s := range parts {
		n := 0
		for _, ch := range s {
			n = n*10 + int(ch-'0')
		}
		out[i] = n
	}
	return out
}

func arg(ps []int, i, def int) int {
	if i < len(ps) && ps[i] != 0 {
		return ps[i]
	}
	if i < len(ps) && ps[i] == 0 && def == 0 {
		return 0
	}
	return def
}

func (v *VT) csi(final byte) {
	ps := v.params()
	switch final {
	case 'A': // CUU
		v.cy -= arg(ps, 0, 1)
	case 'B': // CUD
		v.cy += arg(ps, 0, 1)
	case 'C': // CUF
		v.cx += arg(ps, 0, 1)
	case 'D': // CUB
		v.cx -= arg(ps, 0, 1)
	case 'G': // CHA 列絶対
		v.cx = arg(ps, 0, 1) - 1
	case 'd': // VPA 行絶対
		v.cy = arg(ps, 0, 1) - 1
	case 'H', 'f': // CUP
		v.cy = arg(ps, 0, 1) - 1
		v.cx = arg(ps, 1, 1) - 1
	case 'J': // ED
		v.eraseDisplay(argOr0(ps))
	case 'K': // EL
		v.eraseLine(argOr0(ps))
	case 'r': // DECSTBM
		if v.csiPv {
			break
		}
		if len(ps) >= 2 && ps[0] > 0 && ps[1] > 0 {
			v.top, v.bot = ps[0]-1, ps[1]-1
		} else {
			v.top, v.bot = 0, v.rows-1
		}
		v.cx, v.cy = 0, v.top
	case 'm', 'h', 'l', 'q', 'c', 'n', 't', 'p', ' ':
		// SGR/モード/DA/DSR/cursor-style 等＝セル内容に非影響、無視
	}
	v.wrap = false
	v.clampCursor()
}

func argOr0(ps []int) int {
	if len(ps) == 0 {
		return 0
	}
	return ps[0]
}

func (v *VT) clampCursor() {
	if v.cx < 0 {
		v.cx = 0
	}
	if v.cx >= v.cols {
		v.cx = v.cols - 1
	}
	if v.cy < 0 {
		v.cy = 0
	}
	if v.cy >= v.rows {
		v.cy = v.rows - 1
	}
}

func (v *VT) eraseLine(mode int) {
	row := v.buf[v.cy]
	switch mode {
	case 0: // カーソル→行末
		for x := v.cx; x < v.cols; x++ {
			row[x] = cell{}
		}
	case 1: // 行頭→カーソル
		for x := 0; x <= v.cx && x < v.cols; x++ {
			row[x] = cell{}
		}
	case 2: // 行全体
		for x := 0; x < v.cols; x++ {
			row[x] = cell{}
		}
	}
}

func (v *VT) eraseDisplay(mode int) {
	switch mode {
	case 0: // カーソル→画面末
		v.eraseLine(0)
		for y := v.cy + 1; y < v.rows; y++ {
			v.buf[y] = blankLine(v.cols)
		}
	case 1: // 画面頭→カーソル
		for y := 0; y < v.cy; y++ {
			v.buf[y] = blankLine(v.cols)
		}
		v.eraseLine(1)
	case 2: // 画面全体（history は触らない＝pyte 同様）
		v.buf = blank(v.cols, v.rows)
	case 3: // scrollback クリア（pyte erase_in_display(3)）
		v.buf = blank(v.cols, v.rows)
		v.hist = nil
	}
}

func lineText(row []cell) string {
	var b strings.Builder
	for _, c := range row {
		if c.cont {
			continue
		}
		if c.r == 0 {
			b.WriteByte(' ')
		} else {
			b.WriteRune(c.r)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// VisibleLines は可視 buffer をテキスト行で（pyte display_oracle と同じ
// 抽出: 継続セル skip・空セル空白・rstrip）。
func (v *VT) VisibleLines() []string {
	out := make([]string, v.rows)
	for y := 0; y < v.rows; y++ {
		out[y] = lineText(v.buf[y])
	}
	return out
}

// HistoryLines は history.top（確定スクロールアウト行）を古い→新しい順。
func (v *VT) HistoryLines() []string {
	out := make([]string, len(v.hist))
	for i := range v.hist {
		out[i] = lineText(v.hist[i])
	}
	return out
}

// Cursor は現在カーソル位置。
func (v *VT) Cursor() (x, y int) { return v.cx, v.cy }

// HistLen は history.top 行数。
func (v *VT) HistLen() int { return len(v.hist) }
