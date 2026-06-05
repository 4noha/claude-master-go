package tmuxcc

import (
	"io"
	"sync"
	"time"

	"github.com/4noha/claude-master-go/internal/screen"
)

// Renderer は per-pane VT 状態を保持し、stdout に atomic frame を吐く。
//
// 設計:
//   - %output 受信→該当 pane の screen.VT に Feed (proxy frame 構造を
//     既存 VT がそのまま解する＝PoC で実証)
//   - 描画は active pane の VT を行 diff で RenderANSIDiff＝1 atomic
//     write to stdout (BSU/ESU で囲まれた完全 frame)
//   - tmux outer render を経由しないので、tmux-outer 区間で発生した
//     flicker (約 50% 裸 chunk) が物理的に発生しない
//   - 多 pane の split layout は P5 で対応。MVP は active pane focus のみ
//
// 描画タイミング (throttling):
//   - 直前 emit から MinInterval 経過済なら **即時 emit** (latency 最小)
//   - 未経過なら timer schedule＝次 tick (= 直前 emit + MinInterval) で
//     emit。tick 内の追加 %output は同じ timer で coalesce される
//   - これにより claude UI のような高頻度 %output (74 fps 観測)を 30 fps
//     cap で coalesce＝tmux 通常 outer の 24 fps cycle と同等以下に
//     収まる。低頻度 workload (test-flicker 6.8 fps) では throttle 閾値
//     以下なので影響無し
type Renderer struct {
	mu   sync.Mutex
	out  io.Writer
	rows int
	cols int

	// pane VT map: paneID (e.g. "%0") → VT 状態
	panes map[string]*paneState

	// active pane (focus 中)。初期は最初に来た %output の pane。
	// %active-window-changed 等の制御で更新 (将来) 。
	active string

	// throttling: 高頻度 %output を coalesce して frame rate を cap する
	// (default 33ms = 30 fps)。MinInterval=0 で disable (即時 emit)。
	minInterval time.Duration
	lastEmit    time.Time
	timer       *time.Timer // scheduled emit (nil=未 schedule)
}

type paneState struct {
	vt *screen.VT
	sr *screen.ScrollRenderer
	// initialEmitted: 初回 emit (= 2J 入り RenderANSI) 済か。これが
	// false の間は full redraw で outer を clean state にする。以降は
	// RenderANSIDiff (変更行のみ) で blackout flash と全画面書込を回避。
	// SetSize / SetActive (pane 切替) で false に戻し再 full emit する。
	initialEmitted bool
}

// NewRenderer は指定サイズ・出力先で Renderer を作る。
// throttling 無し (即時 emit) なので、coalescing が必要なら
// NewRendererWithThrottle を使う。
func NewRenderer(out io.Writer, cols, rows int) *Renderer {
	return &Renderer{
		out:   out,
		cols:  cols,
		rows:  rows,
		panes: map[string]*paneState{},
	}
}

// NewRendererWithThrottle は emit を minInterval で coalesce する版。
// minInterval=33ms (30 fps cap) が claude UI 高頻度 %output 74 fps を
// 落として bare tmux 24 fps cycle 同等以下にする目安。0 で disable。
func NewRendererWithThrottle(out io.Writer, cols, rows int, minInterval time.Duration) *Renderer {
	r := NewRenderer(out, cols, rows)
	r.minInterval = minInterval
	return r
}

// SetSize は描画サイズを変更。既存 pane VT は再生成 (resize 時)。
// 各 pane の diff baseline も破棄＝resize 後の最初の emit は full
// redraw で outer を clean にする。pending timer は停止 (新サイズで
// emit するため)。
func (r *Renderer) SetSize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cols, r.rows = cols, rows
	for id := range r.panes {
		r.panes[id] = newPaneState(cols, rows)
		// 新規 pane なので diff baseline は元々 nil＝full emit
	}
	r.stopTimerLocked()
}

// HandleOutput は %output メッセージを処理: 該当 pane VT に Feed して
// active pane なら emit を schedule (throttled) または即時実行。
func (r *Renderer) HandleOutput(paneID string, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ps, ok := r.panes[paneID]
	if !ok {
		ps = newPaneState(r.cols, r.rows)
		r.panes[paneID] = ps
		// 最初に来た pane を active 初期値に
		if r.active == "" {
			r.active = paneID
		}
	}
	ps.vt.Feed(data)
	// active pane のみ描画 (非 active への bytes は VT に蓄積するだけ＝
	// focus 切替時に最新状態が描ける)
	if paneID == r.active {
		r.scheduleEmitLocked()
	}
}

// SetActive は表示する pane を切替え、即 full-redraw (2J 付き)。新
// pane の content は元の pane と無関係なので diff baseline を破棄して
// clean redraw する。pending timer は停止。
func (r *Renderer) SetActive(paneID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.panes[paneID]; !ok {
		r.panes[paneID] = newPaneState(r.cols, r.rows)
	}
	r.active = paneID
	// 新 active pane の初回 emit は full redraw で outer を clean に
	r.panes[paneID].initialEmitted = false
	r.panes[paneID].sr.ResetDiffBaseline()
	r.stopTimerLocked()
	// pane 切替は user-driven の即時応答なので throttling を経由せず
	// emit (lastEmit も新規 emit 時刻に更新)
	r.emitActiveLocked()
	r.lastEmit = time.Now()
}

// Active は現在 focus 中の paneID。
func (r *Renderer) Active() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// PaneIDs は管理中の全 pane ID。
func (r *Renderer) PaneIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.panes))
	for id := range r.panes {
		out = append(out, id)
	}
	return out
}

// RemovePane は pane が閉じられた時に VT 状態を破棄。active が消えた
// 場合は active=""（呼び元が次の active を SetActive で指定）。
func (r *Renderer) RemovePane(paneID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.panes, paneID)
	if r.active == paneID {
		r.active = ""
	}
}

// Close は pending timer を停止 (リソース解放)。Renderer 終了時に呼ぶ。
// 既に停止済 / timer 未起動の場合は no-op。
func (r *Renderer) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopTimerLocked()
}

// scheduleEmitLocked は throttled emit のスケジューラ。
// - minInterval=0: 即時 emit (throttle 無効)
// - 直前 emit から minInterval 経過済: 即時 emit (latency 最小)
// - 未経過: timer schedule (未 schedule のみ。多重 schedule 防止)
//
// 要 r.mu。
func (r *Renderer) scheduleEmitLocked() {
	if r.minInterval == 0 {
		r.emitActiveLocked()
		r.lastEmit = time.Now()
		return
	}
	now := time.Now()
	elapsed := now.Sub(r.lastEmit)
	if elapsed >= r.minInterval {
		// 直前 emit から十分時間経過＝即時 emit
		r.emitActiveLocked()
		r.lastEmit = now
		return
	}
	// throttle 中＝timer schedule (既に schedule 済なら no-op)
	if r.timer != nil {
		return
	}
	delay := r.minInterval - elapsed
	r.timer = time.AfterFunc(delay, r.firedEmit)
}

// firedEmit は throttle timer 発火 callback。lock 取って emit。
func (r *Renderer) firedEmit() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timer = nil
	r.emitActiveLocked()
	r.lastEmit = time.Now()
}

// stopTimerLocked は pending timer を停止。要 r.mu。
func (r *Renderer) stopTimerLocked() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}

// emitActiveLocked は active pane VT を 1 回の Write で out に書く。
// **行単位 diff** で「前回 emit から変更した行のみ」再描画＝毎 frame
// の全画面書込 (= 全 cell 書込) を構造的に消す。
//
// 設計の歴史:
//   - L4-A' 初版: 毎 frame screen.RenderANSI で **\x1b[2J + 全行
//     書込** → 「全画面ちらつき」(2J flash) を user 報告
//   - L4-A'+ : RenderANSIIncremental (2J 抜き) で blackout 解消 → が
//     **毎 frame 全行書込は残った**→「めちゃくちゃ更新されてチカチカ」
//     を user 報告
//   - L4-A'++ : RenderANSIDiff で**変更行のみ** emit
//   - L4-A'+++ : auto-wrap 無効化で scroll 抑止
//   - L4-A'++++ (本実装): emit throttling で claude UI 高頻度 (74 fps)
//     を 30 fps cap に＝tmux 通常 outer の 24 fps cycle 同等以下に
//
// 要 r.mu。
func (r *Renderer) emitActiveLocked() {
	ps, ok := r.panes[r.active]
	if !ok || r.out == nil {
		return
	}
	// RenderANSIDiff は内部で「初回は full / 以降 diff」を判定する
	// (prevCells==nil チェック)。SetSize/SetActive で
	// ResetDiffBaseline 呼んで初回扱いに戻す＝initialEmitted フラグ
	// より状態管理を screen 側に寄せて bug 源を 1 箇所化。
	if !ps.initialEmitted {
		ps.sr.ResetDiffBaseline() // 念のため (新規 pane でも no-op)
		ps.initialEmitted = true
	}
	frame := ps.sr.RenderANSIDiff(ps.vt, r.rows, r.cols)
	_, _ = r.out.Write(frame)
}

func newPaneState(cols, rows int) *paneState {
	return &paneState{
		vt: screen.NewModel(cols, rows),
		sr: screen.NewScrollRenderer(),
	}
}
