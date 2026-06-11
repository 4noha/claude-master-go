// Package ttysync は外側端末向けの「idle-based byte batching wrapper」を
// 提供する。tmux を子プロセスとして PTY 経由で起動し、tmux→外側端末
// 区間で「短い idle (~4ms) が来るまで bytes を蓄積→1 write で flush」
// することで、tmux の sync wrap 漏れ (約 50%) による flicker を端末側
// で軽減する。
//
// 設計根拠:
//   - 実バイト解析で tmux 3.6 が outer に出す bytes の ~50% が BSU/ESU
//     外で裸 stream として emit される事実を確認 (2026-06-05・CLAUDE.md
//     「tmux 経由ちらつき残課題」節)。proxy 側 ?25l/h + terminal-features
//     sync では完全解消せず、tmux/端末 の協力が必要。
//   - DECSET 2026 を honor しない端末 (xterm.js / Mac Terminal.app) でも
//     有効な universal な対策として、protocol 非依存の「byte timing 集約」
//     を採用。多くの modern terminal は render-tick (60Hz 程度) 基盤で
//     byte burst を tick 内に処理するため、1 write に集約すれば atomic
//     に近い描画が得られる仮定。
//   - 「typing echo を遅延させない」ため、idle 検出は短く (既定 4ms)。
//     人間知覚閾値以下で keystroke latency は気付かれない。
//   - 「frame coalescing が逆に flicker 製造」(過去 workflow refute) を
//     避けるため、固定 tick (16ms 等) ではなく idle 検出方式を採用。
//     tmux 自然な burst の終わりで自動的に flush される。
package ttysync

import (
	"fmt"
	"io"
	"os"
	"time"
)

// getDebug は env CM_TTYSYNC_DEBUG=1 の時に flush 毎の size を stderr へ
// 1 行で出す関数を返す (それ以外 nil)。本機能の効果を実測確認するため。
func getDebug() func(int) {
	if os.Getenv("CM_TTYSYNC_DEBUG") == "" {
		return nil
	}
	start := time.Now()
	return func(n int) {
		fmt.Fprintf(os.Stderr, "[ttysync] +%.1fms flush %d\n",
			float64(time.Since(start).Microseconds())/1000.0, n)
	}
}

// PumpConfig は PumpWithIdleConfig の調整値。
type PumpConfig struct {
	// Idle: 通常の idle 検出時間 (~4ms 想定)。bytes 無受信がこの期間
	// 続いたら buffer を flush。
	Idle time.Duration

	// HoldAfterDestructive: ANSI parser で `\x1b[2J` 等の destructive
	// op を検出した直後だけ「extended idle」に切替える時間 (~32ms
	// 想定)。これにより「画面クリア → 直後の redraw」が同一 batch に
	// 集約され、端末で blackout が visible にならなくなる。0 なら
	// hold mode 無効 (= 純 idle batch)。
	HoldAfterDestructive time.Duration

	// SyncWrap: true なら各 flush を BSU/ESU (DECSET 2026) で囲み、
	// 内側に紛れた既存 marker は除去 (syncwrap.go)。2026 honor 端末
	// (iTerm2 等・実測 m1 で bare tmux は 64% 裸 emit) で flush 全体が
	// atomic 描画になる。非対応端末では 16 bytes/flush の無害な
	// オーバーヘッドのみ。
	SyncWrap bool

	// MaxHold: buffer 最古 byte の最大滞留時間。idle が来ない連続
	// stream (例: cat 大ファイル) でも MaxHold 経過で強制 flush＝
	// 「無限に buffer して画面が止まる」latent bug の backstop。
	// 0 で無効。
	MaxHold time.Duration

	// MaxBuffer: buffer サイズ上限。超えたら即時 flush (メモリ保護)。
	// 0 で無効。
	MaxBuffer int
}

// PumpWithIdle は backward-compat 薄い shim (hold mode 無効版)。
func PumpWithIdle(dst io.Writer, src io.Reader, idle time.Duration,
	c Clock) error {
	return PumpWithIdleConfig(dst, src,
		PumpConfig{Idle: idle, HoldAfterDestructive: 0}, c)
}

// PumpWithIdleConfig は src→dst の idle-batch pump に加え、ANSI parser
// で destructive op (画面クリア系) を検出した直後だけ idle 時間を hold
// に延長することで「2J→redraw 区間」を同一 batch に集約する (blackout
// 抑止)。HoldAfterDestructive=0 なら shim 同等動作。
//
// 不変条件:
//   - 受信した bytes は順序保持で確実に dst へ届く (drop しない)。
//   - flush 時の Write は **1 回呼び**、複数 chunk を集約。
//   - src.Read 中の dst.Write はしない (interleave で順序逆転回避)。
//   - hold 中は「次の bytes 到着で再 hold」ではなく「hold 終了 (=flush
//     完了) で hold 解除」。新たな destructive op で再 arm。
//   - parser state は chunk 境界跨ぎで保持される。
func PumpWithIdleConfig(dst io.Writer, src io.Reader, cfg PumpConfig,
	c Clock) error {

	type readEv struct {
		data []byte
		err  error
	}
	reads := make(chan readEv, 4)
	go func() {
		defer close(reads)
		b := make([]byte, 8192)
		for {
			n, err := src.Read(b)
			if n > 0 {
				d := append([]byte(nil), b[:n]...)
				reads <- readEv{data: d}
			}
			if err != nil {
				reads <- readEv{err: err}
				return
			}
		}
	}()

	var buf []byte
	var timer Timer
	var timerC <-chan time.Time
	var maxHoldTimer Timer
	var maxHoldC <-chan time.Time
	dbg := getDebug()
	parser := &ansiParser{}
	tracker := newTailTracker() // SyncWrap 時のみ feed
	holdActive := false

	stopIdle := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			timerC = nil
		}
	}
	stopMaxHold := func() {
		if maxHoldTimer != nil {
			maxHoldTimer.Stop()
			maxHoldTimer = nil
			maxHoldC = nil
		}
	}

	// maxTailHold: 進行中 sequence の hold 上限。unterminated OSC 等の
	// 病的 stream で tail が無限成長するのを防ぐ。超えたら degrade
	// (wrap せず raw emit＝挿入による sequence 破壊は起きない)。
	const maxTailHold = 32 << 10

	// flush は buffer を dst へ書く。
	//
	// SyncWrap 時の不変条件 (adversarial review で確定した「flush 境界
	// が escape sequence / UTF-8 rune の途中だと ESU/BSU 挿入で可視
	// ゴミが出る」bug の根治):
	//   - tracker が「進行中 sequence の開始 offset」を厳密に追跡
	//   - emit するのは ground 状態で終わる head 部分のみ。tail
	//     (進行中 sequence) は buf に hold し次 bytes と自然結合
	//   - hold した tail に対して timer は arm しない (不完全 sequence
	//     は端末でも描画不能＝急いで流す価値が無い)。次の bytes 到着
	//     or EOF で解消＝無限ループも起きない
	//   - eof=true (stream 終端) のみ tail を raw で後置 emit (後続
	//     BSU が無いので挿入破壊は起きない・dangling 不完全 sequence
	//     は端末で無害に破棄される)
	flush := func(eof bool) error {
		if len(buf) == 0 {
			return nil
		}
		stopIdle()
		stopMaxHold()
		holdActive = false // flush で hold 解除＝次の destructive で再 arm
		if !cfg.SyncWrap {
			if dbg != nil {
				dbg(len(buf))
			}
			_, err := dst.Write(buf)
			buf = buf[:0]
			return err
		}
		split := tracker.SplitAt(len(buf))
		if len(buf)-split > maxTailHold {
			// 病的 long sequence: wrap せず raw emit (挿入無し＝破壊無し)
			if dbg != nil {
				dbg(len(buf))
			}
			_, err := dst.Write(buf)
			buf = buf[:0]
			tracker.Reset()
			return err
		}
		head := buf[:split]
		payload, carry := stripSyncMarkers(head)
		// head は tracker により ground 終端なので carry は常に nil の
		// はずだが、万一に備え byte 保存を優先して payload へ戻す。
		if len(carry) > 0 {
			payload = append(payload, carry...)
		}
		var out []byte
		if len(payload) > 0 {
			out = wrapSync(payload)
		}
		if eof {
			// 終端: hold 中 tail を raw で後置 (後続 BSU 無し＝安全)
			out = append(out, buf[split:]...)
			buf = buf[:0]
			tracker.Reset()
			if len(out) == 0 {
				return nil
			}
			if dbg != nil {
				dbg(len(out))
			}
			_, err := dst.Write(out)
			return err
		}
		// tail を buf 先頭へ繰越し (copy は overlap-safe)
		rest := buf[split:]
		buf = append(buf[:0], rest...)
		tracker.Rebase(split)
		if len(out) == 0 {
			return nil // marker のみ or 全部 tail＝emit する実体無し
		}
		if dbg != nil {
			dbg(len(out))
		}
		_, err := dst.Write(out)
		return err
	}

	for {
		select {
		case r, ok := <-reads:
			if !ok {
				return flush(true)
			}
			if r.err != nil {
				_ = flush(true)
				if r.err == io.EOF {
					return nil
				}
				return r.err
			}
			// ANSI parser に流して destructive op を検出
			if cfg.HoldAfterDestructive > 0 && !holdActive {
				for i := 0; i < len(r.data); i++ {
					if parser.Feed(r.data[i]) {
						holdActive = true
						// 1 度 hold に入れば残バイトの parser feed
						// は不要 (どうせ hold で flush される)
						break
					}
				}
			} else if cfg.HoldAfterDestructive > 0 {
				// 既に hold 中でも parser state 維持 (chunk 跨ぎの
				// CSI 不完全 sequence が次の判定に影響しないよう)
				for i := 0; i < len(r.data); i++ {
					parser.Feed(r.data[i])
				}
			}
			if cfg.SyncWrap {
				base := len(buf)
				for i := 0; i < len(r.data); i++ {
					tracker.Feed(r.data[i], base+i)
				}
			}
			buf = append(buf, r.data...)
			// MaxBuffer 超過は即時 flush (メモリ保護・タイマー非依存)
			if cfg.MaxBuffer > 0 && len(buf) >= cfg.MaxBuffer {
				if err := flush(false); err != nil {
					return err
				}
				continue
			}
			// MaxHold: 未 arm なら arm (flush で停止→次の append で再
			// arm)。bytes が来続けても既存 timer は再 arm しない＝最古
			// byte の滞留時間で必ず発火＝連続 stream でも描画が止まら
			// ない (idle だけだと永遠に reset され buffer が無限成長
			// する latent bug の backstop)。
			if cfg.MaxHold > 0 && maxHoldTimer == nil {
				maxHoldTimer = c.NewTimer(cfg.MaxHold)
				maxHoldC = maxHoldTimer.C()
			}
			// idle timer を (再) 起動。hold 中なら hold ms。
			d := cfg.Idle
			if holdActive && cfg.HoldAfterDestructive > 0 {
				d = cfg.HoldAfterDestructive
			}
			stopIdle()
			timer = c.NewTimer(d)
			timerC = timer.C()
		case <-timerC:
			timer = nil
			timerC = nil
			if err := flush(false); err != nil {
				return err
			}
		case <-maxHoldC:
			maxHoldTimer = nil
			maxHoldC = nil
			if err := flush(false); err != nil {
				return err
			}
		}
	}
}

// Clock は時間制御 seam。production は RealClock、test は FakeClock。
type Clock interface {
	NewTimer(d time.Duration) Timer
}

// Timer は time.Timer の最小 interface。Reset は使わず Stop+新規 NewTimer
// で代替 (Reset の channel 排出規律が複雑なため)。
type Timer interface {
	C() <-chan time.Time
	Stop()
}

// RealClock は時刻に time stdlib を使う production 実装。
type RealClock struct{}

// NewTimer は time.NewTimer ラッパ。
func (RealClock) NewTimer(d time.Duration) Timer {
	return &realTimer{t: time.NewTimer(d)}
}

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time { return r.t.C }

// Stop は time.Timer.Stop + 未排出 channel 排出 (Go の慣用)。
func (r *realTimer) Stop() {
	if !r.t.Stop() {
		select {
		case <-r.t.C:
		default:
		}
	}
}
