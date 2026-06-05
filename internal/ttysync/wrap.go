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
	"io"
	"time"
)

// PumpWithIdle は src から読み続けて dst へ flush するが、bytes 到着で
// idle タイマーを (再) 起動し、idle 期間無入力で buffer を 1 write に
// 集約して flush する。src が EOF / error を返したら残 buffer を flush
// して返る。clock seam で test では fake timer 駆動可。
//
// 不変条件:
//   - 受信した bytes は順序保持で確実に dst へ届く (drop しない)。
//   - flush 時の Write は **1 回呼び**、複数 chunk を集約。
//   - src.Read 中の dst.Write はしない (interleave で順序逆転回避)。
func PumpWithIdle(dst io.Writer, src io.Reader, idle time.Duration,
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

	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		_, err := dst.Write(buf)
		buf = buf[:0]
		if timer != nil {
			timer.Stop()
			timer = nil
			timerC = nil
		}
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
			buf = append(buf, r.data...)
			// idle タイマーを (再) 起動。bytes 到着の度に reset するので
			// 連続 burst の末尾で 1 度だけ発火する＝tmux 自然 burst 境界。
			if timer != nil {
				timer.Stop()
			}
			timer = c.NewTimer(idle)
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
