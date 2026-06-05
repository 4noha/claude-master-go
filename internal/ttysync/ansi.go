package ttysync

// 最小 ANSI state machine。byte stream を 1 byte ずつ Feed して
// 「destructive op (画面 blackout 源) が完了した」フラグを返す。
//
// 目的: tmux outer 出力に紛れる `\x1b[2J` 等の screen-clear を検出して、
// PumpWithIdle の hold-mode 切替えに使う (blackout 抑止＝直後の redraw
// と同一 batch に集約する)。
//
// 不変条件:
//   - cell content (printable bytes) には影響しない＝feed しても state
//     のみ更新で副作用無し。
//   - 「destructive 完了」は CSI final byte 受信時点で 1 度だけ true を
//     返す＝呼び元の hold-mode arm 制御で重複発火しない。
//   - ESC sequence の途中で chunk が割れても (PTY read 境界) state を
//     繰越して継続認識。
//
// サポート範囲（最小）:
//   - CSI (\x1b[) の final byte 認識：パラメータ蓄積→destructive 判定
//   - OSC/DCS は終端まで読み飛ばし (BEL or ESC\)
//   - 単発 ESC (CHARSET 等) は次 byte で消費
//
// 非対応 (false negative になっても害無し):
//   - SS2/SS3 (\x1bN/\x1bO)
//   - APC/PM/SOS
//   - 8-bit C1 (0x9b 等)
//   - intermediate bytes (CSI ESC 0x20-0x2F)

type ansiState int

const (
	asGround ansiState = iota
	asEsc
	asCSI
	asOSC
	asDCS
	asOSCEsc // OSC 内で ESC 受信 (ST 待ち)
	asDCSEsc
)

// ansiParser は byte stream を消費して destructive op 検出を返す。
// state 持ち越し対応 (PTY read 境界で chunk 分割しても継続認識)。
type ansiParser struct {
	state  ansiState
	params []byte // CSI のパラメータ・final byte 受信で消費
}

// Feed は 1 byte を消費し、CSI final byte の完了時に「destructive か」
// を返す。それ以外の byte (printable / state 遷移中) は false。
func (p *ansiParser) Feed(b byte) bool {
	switch p.state {
	case asGround:
		if b == 0x1b {
			p.state = asEsc
		}
		return false
	case asEsc:
		switch b {
		case '[':
			p.state = asCSI
			p.params = p.params[:0]
		case ']':
			p.state = asOSC
		case 'P':
			p.state = asDCS
		default:
			// CHARSET (B/0 等) や single-shift・無関係 1 byte
			p.state = asGround
		}
		return false
	case asCSI:
		// パラメータ (digit/;/?) を蓄積、final (0x40-0x7E) で完了
		switch {
		case (b >= '0' && b <= '9') || b == ';' || b == '?':
			p.params = append(p.params, b)
			return false
		case b >= 0x40 && b <= 0x7E:
			// final byte 完了
			destructive := isDestructiveCSI(b, p.params)
			p.state = asGround
			return destructive
		default:
			// intermediate byte (0x20-0x2F) は parser を ground へ戻して
			// 諦める (非対応・false negative 許容)
			p.state = asGround
			return false
		}
	case asOSC:
		// OSC は BEL (0x07) または ESC \ で終端
		switch b {
		case 0x07:
			p.state = asGround
		case 0x1b:
			p.state = asOSCEsc
		}
		return false
	case asOSCEsc:
		// ESC 後の '\' (= 0x5c) で OSC 終端、他なら ground (recover)
		p.state = asGround
		return false
	case asDCS:
		switch b {
		case 0x07:
			p.state = asGround
		case 0x1b:
			p.state = asDCSEsc
		}
		return false
	case asDCSEsc:
		p.state = asGround
		return false
	}
	return false
}

// isDestructiveCSI は CSI final byte とパラメータから「画面破壊系か」
// を判定。blackout 源として保守的に広めに取る:
//   - 'J' (ED: erase in display) — 全消去 / 部分消去
//   - 'K' (EL: erase in line) — 行末まで / 全行消去
//
// 'K' は通常 1 行のみで blackout には繋がりにくいが、tmux outer 出力で
// 大量に並ぶことがある (各行末 trim) ので保守的に含める。実害無し
// (false positive は hold mode を多少長引かせるだけ＝flicker 抑止強化)。
func isDestructiveCSI(final byte, params []byte) bool {
	switch final {
	case 'J':
		// \x1b[J, \x1b[0J: cursor→end of screen
		// \x1b[1J: start→cursor
		// \x1b[2J: 全消去 ← 主犯
		// \x1b[3J: scrollback 消去
		// 全部 destructive 扱い (parameter 解析省略)
		return true
	case 'K':
		// 全消去系含む。控えめに hold をかける用途には十分。
		return true
	}
	return false
}
