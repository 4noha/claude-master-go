package ttysync

import "bytes"

// sync-wrap: flush 単位を丸ごと BSU/ESU (DECSET 2026) で囲み、内側に
// 紛れた既存 BSU/ESU marker は除去する。
//
// 目的: m1 実測 (2026-06-05) で tmux outer は claude UI 時に bytes の
// 64% を BSU/ESU 外の裸 stream で emit する。DECSET 2026 を honor する
// 端末 (iTerm2 等) ですら裸部分は incremental 描画＝flicker。flush
// (= idle 検出で区切った burst) を 100% wrap すれば、2026 honor 端末は
// 全 bytes を atomic に commit する。
//
// 内側 marker を strip する理由: tmux 自身が wrap した区間 (36%) の
// BSU/ESU が我々の wrap の中に入れ子になると、DEC private mode の
// ネスト挙動は未規定＝端末によっては最初の ESU で commit されて以降の
// wrap が無効化される。完全 marker は除去し、我々の 1 組だけにする。
var (
	bsuSeq     = []byte("\x1b[?2026h")
	esuSeq     = []byte("\x1b[?2026l")
	syncPrefix = []byte("\x1b[?2026") // BSU/ESU 共通 prefix (7 bytes)
)

// stripSyncMarkers は data から完全な BSU/ESU marker を除去した payload
// と、末尾に残った「marker になりかけの不完全 prefix」(次 chunk と再結合
// すべき carry) を返す。carry は最大 7 bytes ("\x1b[?2026")。
//
// marker でない ESC sequence (SGR/CUP 等) は無加工で payload に残る。
// "\x1b[?2026" に続くのが h/l 以外 (例: 仮想的な "\x1b[?2026$y") の
// 場合も無加工で残す (exact-match のみ除去＝ヒューリスティック無し)。
func stripSyncMarkers(data []byte) (payload, carry []byte) {
	payload = make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		if data[i] != 0x1b {
			// fast path: 次の ESC までまとめてコピー
			j := bytes.IndexByte(data[i:], 0x1b)
			if j < 0 {
				payload = append(payload, data[i:]...)
				return payload, nil
			}
			payload = append(payload, data[i:i+j]...)
			i += j
			continue
		}
		rest := data[i:]
		if len(rest) >= 8 && bytes.HasPrefix(rest, syncPrefix) &&
			(rest[7] == 'h' || rest[7] == 'l') {
			i += 8 // 完全 marker → 除去
			continue
		}
		if len(rest) < 8 && bytes.HasPrefix(syncPrefix, rest) {
			// 末尾が marker の真 prefix → carry (次 chunk と再結合)
			carry = append([]byte(nil), rest...)
			return payload, carry
		}
		// marker でない ESC → そのまま 1 byte コピーして続行
		payload = append(payload, data[i])
		i++
	}
	return payload, nil
}

// wrapSync は payload を BSU + payload + ESU で囲んだ新 slice を返す。
func wrapSync(payload []byte) []byte {
	out := make([]byte, 0, len(payload)+len(bsuSeq)+len(esuSeq))
	out = append(out, bsuSeq...)
	out = append(out, payload...)
	out = append(out, esuSeq...)
	return out
}

// ---- tailTracker: flush 境界の sequence 分断防止 ----
//
// SyncWrap は flush 末尾に ESU・次 flush 先頭に BSU を**挿入**するため、
// flush 境界が escape sequence や UTF-8 多バイト rune の途中だと、端末の
// VT parser は挿入された ESC で進行中 sequence を破棄し、続きのバイト
// (例 "13m") を**プレーンテキストとして印字**してしまう (adversarial
// review で確定した実バグ。SyncWrap 無しなら間に何も挿入されず端末側で
// 自然再結合される＝無害だった)。
//
// tailTracker は buf へ append される全 byte を offset 付きで追跡し、
// 「現在進行中の sequence/rune が buf のどこから始まったか」(seqStart)
// を厳密に保持する。flush 時は buf[:seqStart] だけを emit し、
// buf[seqStart:] を hold ＝ sequence の途中に決して marker を挿入しない。
// 判定は決定論的 state machine (exact parse)＝ヒューリスティック無し。
type tailTracker struct {
	state   ansiState
	utf8rem int // ground 中の UTF-8 継続バイト残数 (0=なし)
	// seqStart: 進行中 sequence/rune の buf 内開始 offset。-1=ground。
	seqStart int
}

func newTailTracker() *tailTracker { return &tailTracker{seqStart: -1} }

// Feed は buf[off] に append された byte b を追跡する。
func (t *tailTracker) Feed(b byte, off int) {
	// UTF-8 多バイト rune の継続中
	if t.utf8rem > 0 {
		if b&0xc0 == 0x80 {
			t.utf8rem--
			if t.utf8rem == 0 {
				t.seqStart = -1
			}
			return
		}
		// 不正継続: rune 追跡を破棄してこの byte を ground で再処理
		t.utf8rem = 0
		t.seqStart = -1
	}
	switch t.state {
	case asGround:
		switch {
		case b == 0x1b:
			t.state = asEsc
			t.seqStart = off
		case b >= 0xc0: // UTF-8 lead byte
			n := 1
			switch {
			case b>>5 == 0x6:
				n = 1
			case b>>4 == 0xe:
				n = 2
			case b>>3 == 0x1e:
				n = 3
			default:
				return // 不正 lead は素通し (1 byte 扱い)
			}
			t.utf8rem = n
			t.seqStart = off
		}
	case asEsc:
		switch b {
		case '[':
			t.state = asCSI
		case ']':
			t.state = asOSC
		case 'P':
			t.state = asDCS
		case 0x1b:
			// ESC ESC: 新 sequence 開始として追跡し直す
			t.seqStart = off
		default:
			// 2 byte ESC sequence (charset 等) 完了
			t.state = asGround
			t.seqStart = -1
		}
	case asCSI:
		if (b >= '0' && b <= '9') || b == ';' || b == '?' || b == ':' ||
			(b >= 0x20 && b <= 0x2f) {
			return // param/intermediate 継続
		}
		// final byte (or 不正 byte) で完了
		t.state = asGround
		t.seqStart = -1
	case asOSC:
		switch b {
		case 0x07:
			t.state = asGround
			t.seqStart = -1
		case 0x1b:
			t.state = asOSCEsc
		}
	case asOSCEsc:
		// ESC \ (ST) で終了。他は OSC 本文継続 (ESC 後の任意 byte)
		if b == '\\' {
			t.state = asGround
			t.seqStart = -1
		} else {
			t.state = asOSC
		}
	case asDCS:
		switch b {
		case 0x07:
			t.state = asGround
			t.seqStart = -1
		case 0x1b:
			t.state = asDCSEsc
		}
	case asDCSEsc:
		if b == '\\' {
			t.state = asGround
			t.seqStart = -1
		} else {
			t.state = asDCS
		}
	}
}

// SplitAt は buf に対する (emit可能な head 長, hold すべき tail 開始) を
// 返す。ground なら head=len(buf)。進行中 sequence があれば seqStart。
func (t *tailTracker) SplitAt(bufLen int) int {
	if t.seqStart < 0 || t.seqStart > bufLen {
		return bufLen
	}
	return t.seqStart
}

// Rebase は flush で buf が buf[keepFrom:] に切り詰められた後の offset
// 補正。hold した tail が buf 先頭に来る。
func (t *tailTracker) Rebase(keepFrom int) {
	if t.seqStart >= 0 {
		t.seqStart -= keepFrom
		if t.seqStart < 0 {
			t.seqStart = 0
		}
	}
}

// Reset は追跡状態を破棄して ground に戻す (異常長 sequence の degrade
// 時・buf 全 emit 時)。
func (t *tailTracker) Reset() {
	t.state = asGround
	t.utf8rem = 0
	t.seqStart = -1
}
