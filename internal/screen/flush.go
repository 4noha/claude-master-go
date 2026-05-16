package screen

// HistoryFlusher は VT の「確定してスクロールアウトした行」を取り出す
// （SESSION_LOG / 将来の HOST_FLOW_SCROLLBACK 用）。Python
// pty_scroll.HistoryFlusher の移植。
//
// 原理: claude が改行で 1 行スクロールアウトさせると VT.lineFeed が
// その行を history.top（maxlen 付き）へ確定 push し v.histTotal を ++
// する。これは端末エミュレータ自身の確定判定＝グラウンドトゥルースで
// あり、footer キーワード等のヒューリスティック分類は一切しない
// （「分類は一切しない」不変条件を破らない）。
//
// Python は deque 内 行オブジェクトの identity を追跡して「前回以降に
// 積まれた行」を delta 抽出し、_last が maxlen で押し出されたら重複
// 防止のため resync only（[]）する。Go では行スライスに identity が
// 無いので、HistTotal()（trim されても増え続ける大域連番）を identity
// として使う ＝ 大域 index による同値実装。lastTotal より前が trim で
// 落ちていれば保持済みの未 emit 分だけ返す（Python が resync で全捨て
// するより欠落が少ないが、dedup/分類は一切しない点は同じ。capture は
// 毎フレーム呼ぶので通常 trim 欠落は起きない）。
type HistoryFlusher struct {
	armed     bool
	lastTotal int      // 前回 takeNew 時点の VT.HistTotal()
	pending   []string // capture 済み・未 drain の確定行（古い→新しい）
}

// pendingCap は never-idle 暴走時のメモリ防御（通常 idle で drain）。
const pendingCap = 100000

// Reset は identity 追跡だけ再 arm（リサイズ等）。capture 済み pending は
// 既に確定した正しい行なので保持する（Python reset と同一）。
func (f *HistoryFlusher) Reset() {
	f.armed = false
	f.lastTotal = 0
}

// Capture は全フレームで呼ぶ。確定行を内部 pending に蓄積するだけ
// （端末へは書かない）。VT.hist は maxlen 付きなので毎フレーム取り込ま
// ないと長いバーストで古い確定行が押し出され失われる。
func (f *HistoryFlusher) Capture(v *VT) {
	if n := f.takeNew(v); len(n) > 0 {
		f.pending = append(f.pending, n...)
		if len(f.pending) > pendingCap {
			f.pending = f.pending[len(f.pending)-pendingCap:]
		}
	}
}

// HasPending は未 drain の確定行があるか。
func (f *HistoryFlusher) HasPending() bool { return len(f.pending) > 0 }

// PendingLen は未 drain 行数。
func (f *HistoryFlusher) PendingLen() int { return len(f.pending) }

// Drain は蓄積した確定行を取り出してクリア（scrollback/ファイルへ書く
// のは呼び出し側）。
func (f *HistoryFlusher) Drain() []string {
	out := f.pending
	f.pending = nil
	return out
}

// takeNew は前回以降に history へ確定した行を古い→新しい順で返す。
// 初回（arm 時）は既存履歴を流さず空（接続時点から開始）。lastTotal が
// trim 境界より前なら保持済みの未 emit 分にクランプ（Python の
// 「_last 落ち → resync」と同じく重複を出さない）。
func (f *HistoryFlusher) takeNew(v *VT) []string {
	total := v.HistTotal()
	if !f.armed {
		f.armed = true
		f.lastTotal = total // 接続時点まで skip（過去ログ大量 dump 回避）
		return nil
	}
	lines := v.HistoryLines() // 保持中 history（古い→新しい）
	firstIdx := total - len(lines)
	lo := f.lastTotal
	if lo < firstIdx { // trim で落ちた区間は追えない → 保持分先頭から
		lo = firstIdx
	}
	f.lastTotal = total
	if lo >= total {
		return nil
	}
	return lines[lo-firstIdx : total-firstIdx]
}
