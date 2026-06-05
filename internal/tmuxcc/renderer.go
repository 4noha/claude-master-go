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
// initialEmitted も false にリセット＝resize 後の最初の emit は 2J
// 付き full redraw で outer を clean にする。
func (r *Renderer) SetSize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cols, r.rows = cols, rows
	for id := range r.panes {
		r.panes[id] = newPaneState(cols, rows)
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
// pane の content は元の pane と無関係なので initialEmitted を false に
// 戻して clean redraw する。
func (r *Renderer) SetActive(paneID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.panes[paneID]; !ok {
		r.panes[paneID] = newPaneState(r.cols, r.rows)
	}
	r.active = paneID
	// 新 active pane の初回 emit は full redraw で outer を clean に
	r.panes[paneID].initialEmitted = false
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
// 初回は RenderANSI (2J 込み full) で outer を clean state にし、以降
// は RenderANSIIncremental (2J 無し・既存 cell 上書き) で毎 frame の
// blackout flash を物理的に発生させない。要 r.mu。
//
// blackout 抑止の意味: 外側端末が DECSET 2026 を honor しない経路
// (xterm.js / Mac Terminal.app 等) では \x1b[2J を即時実行→画面真っ黒
// →再描画＝frame ごとに blackout が visible。L4-A' tmux-render 初版で
// この毎 frame 2J を打ってしまい「普通に全画面ちらつく」状況を再現した
// (実機検証で確定)。incremental 切替でこれが構造的に消える。
func (r *Renderer) emitActiveLocked() {
	ps, ok := r.panes[r.active]
	if !ok || r.out == nil {
		return
	}
	var frame []byte
	if !ps.initialEmitted {
		frame = ps.sr.RenderANSI(ps.vt, r.rows, r.cols)
		ps.initialEmitted = true
	} else {
		frame = ps.sr.RenderANSIIncremental(ps.vt, r.rows, r.cols)
	}
	_, _ = r.out.Write(frame)
}

func newPaneState(cols, rows int) *paneState {
	return &paneState{
		vt: screen.NewModel(cols, rows),
		sr: screen.NewScrollRenderer(),
	}
}
