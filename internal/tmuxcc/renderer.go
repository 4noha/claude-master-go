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
func (r *Renderer) SetSize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cols, r.rows = cols, rows
	// 既存 VT を捨てて新サイズで作り直し。次の %output で再 Feed される
	// (catch-up frame が proxy から来る前提)。
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

// SetActive は表示する pane を切替え、即 full-redraw。
func (r *Renderer) SetActive(paneID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.panes[paneID]; !ok {
		r.panes[paneID] = newPaneState(r.cols, r.rows)
	}
	r.active = paneID
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

// emitActiveLocked は active pane VT を screen.RenderANSI で 1 回の
// Write に集約して out に書く。要 r.mu。
func (r *Renderer) emitActiveLocked() {
	ps, ok := r.panes[r.active]
	if !ok || r.out == nil {
		return
	}
	frame := ps.sr.RenderANSI(ps.vt, r.rows, r.cols)
	// 1 syscall に集約された atomic write。outer 端末は BSU/ESU を
	// 解する限り完全 atomic 描画 (screen.RenderANSI が ?2026h/l + ?25l/h
	// を frame 末尾の cursor 復元と共に出している＝既存実装の incl)。
	_, _ = r.out.Write(frame)
}

func newPaneState(cols, rows int) *paneState {
	return &paneState{
		vt: screen.NewModel(cols, rows),
		sr: screen.NewScrollRenderer(),
	}
}
