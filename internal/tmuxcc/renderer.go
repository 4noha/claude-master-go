package tmuxcc

import (
	"io"
	"sync"

	"github.com/4noha/claude-master-go/internal/screen"
)

// Renderer は per-pane VT 状態を保持し、stdout に atomic frame を吐く。
//
// 設計:
//   - %output 受信→該当 pane の screen.VT に Feed (proxy frame 構造を
//     既存 VT がそのまま解する＝PoC で実証)
//   - 描画は「現在の active pane の VT を full-screen で RenderANSI」＝
//     1 atomic write to stdout (BSU/ESU で囲まれた完全 frame)
//   - tmux outer render を経由しないので、tmux-outer 区間で発生した
//     flicker (約 50% 裸 chunk) が物理的に発生しない
//   - 多 pane の split layout は P5 で対応。MVP は active pane focus のみ
//
// 描画タイミング:
//   - %output 受信ごとに即時 RenderANSI (= proxy frame ごとに 1 atomic
//     emit)
//   - 高頻度 emit 時の余計な描画コストは screen.RenderANSI 既存 (proxy
//     server.go と同等) ＝既に検証済み path で性能負荷も同等
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
}

type paneState struct {
	vt *screen.VT
	sr *screen.ScrollRenderer
	// initialEmitted: 初回 emit (= 2J 入り RenderANSI) 済か。これが
	// false の間は full redraw で outer を clean state にする。以降は
	// RenderANSIIncremental (2J 無し・既存 cell 上書き) で blackout
	// flash を回避。SetSize / SetActive (pane 切替) で false に戻し
	// 再 full emit する。
	initialEmitted bool
}

// NewRenderer は指定サイズ・出力先で Renderer を作る。
// cols/rows は外側端末のサイズ。tmux 側 pane size は通常これと等しい
// (resize-pane 等で変わらない限り)。
func NewRenderer(out io.Writer, cols, rows int) *Renderer {
	return &Renderer{
		out:   out,
		cols:  cols,
		rows:  rows,
		panes: map[string]*paneState{},
	}
}

// SetSize は描画サイズを変更。既存 pane VT は再生成 (resize 時)。
// 各 pane の diff baseline も破棄＝resize 後の最初の emit は full
// redraw で outer を clean にする。
func (r *Renderer) SetSize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cols, r.rows = cols, rows
	for id := range r.panes {
		r.panes[id] = newPaneState(cols, rows)
		// 新規 pane なので diff baseline は元々 nil＝full emit
	}
}

// HandleOutput は %output メッセージを処理: 該当 pane VT に Feed して
// active pane なら即 atomic re-render。
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
		r.emitActiveLocked()
	}
}

// SetActive は表示する pane を切替え、即 full-redraw (2J 付き)。新
// pane の content は元の pane と無関係なので diff baseline を破棄して
// clean redraw する。
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
	r.emitActiveLocked()
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
//   - L4-A'++ (本実装): RenderANSIDiff で**変更行のみ** emit。初回
//     のみ full RenderANSI (2J 込み) で outer を clean state にし、
//     以降 diff。tmux 通常 outer の差分描画と同等粒度＋我々の方が
//     BSU/ESU + ?25l/h で 100% 囲んでいる分有利。
//
// SetSize / SetActive 時は ps.sr.ResetDiffBaseline() で diff baseline
// を破棄＝次回 full emit (2J 入り) で clean redraw。
//
// outer 端末が DECSET 2026 を honor しない経路 (xterm.js / Mac
// Terminal.app 等) でも、変更行が少なければ 1 atomic write の負荷も
// 視覚露出も最小化される。
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
