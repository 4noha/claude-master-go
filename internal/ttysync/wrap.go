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
	dbg := getDebug()
	parser := &ansiParser{}
	holdActive := false

	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if dbg != nil {
			dbg(len(buf))
		}
		_, err := dst.Write(buf)
		buf = buf[:0]
		if timer != nil {
			timer.Stop()
			timer = nil
			timerC = nil
		}
		holdActive = false // flush で hold mode 解除＝次の destructive
		// 検出で再度 arm
		return err
	}

	for {
		select {
		case r, ok := <-reads:
			if !ok {
				return flush()
			}
			if r.err != nil {
				_ = flush()
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
			buf = append(buf, r.data...)
			// timer を (再) 起動。hold 中なら hold ms、それ以外 idle ms。
			if timer != nil {
				timer.Stop()
			}
			d := cfg.Idle
			if holdActive && cfg.HoldAfterDestructive > 0 {
				d = cfg.HoldAfterDestructive
			}
			timer = c.NewTimer(d)
			timerC = timer.C()
		case <-timerC:
			timer = nil
			timerC = nil
			if err := flush(); err != nil {
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
