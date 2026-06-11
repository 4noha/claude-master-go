package tmuxcc

import (
	"io"
	"sync"
)

// Forwarder は %output で届いた bytes を **一切再描画せず verbatim で**
// out へ転送する中間層 (Web の sync.js と同じ役割を tmux -CC の上に
// 建てたもの)。
//
// 設計根拠 (L4-A' 5-iter 失敗 → 巻き戻し → 再設計の結論):
//   - proxy frame (BSU+?25l+2J+rows+cursor 復元+?25h+ESU) は元から
//     「完全に整合した 1 画面」の atomic 単位。cls (2J) は frame 内に
//     密閉されており、frame 丸ごと atomic に commit されれば cls が
//     単独で可視化される瞬間は存在しない (= Web が完璧な理由と同一)
//   - tmux 通常経路はこの境界を tmux が消費・破壊する (m1 実測: 64%
//     裸 emit) ため、外側でいくら時間基準の batch をしても「frame の
//     途中で commit 境界が落ちる」中間状態 flicker が原理的に残る
//   - tmux -CC の %output は pane に書かれた bytes を byte-exact で
//     運ぶ (実 claude proxy で PoC 済: BSU+?25l+2J header 完全保持)
//     ＝frame 境界が生き残っている唯一の場所
//   - したがって正しい中間層は「decode して素通し」だけ。VT を持たず
//     再描画しない＝L4-A' 初版〜++++ の失敗 (自前 2J flash・全行
//     書込・行 diff の scroll 暴走・throttle) を全て構造的に回避
//
// MVP: 単一 active pane の転送のみ。非 active pane の bytes は破棄
// (frame は毎回完全なので、将来 pane 切替を実装する時は切替後の
// catch-up を capture-pane で取れば良い)。
type Forwarder struct {
	mu     sync.Mutex
	out    io.Writer
	active string
}

// NewForwarder は転送先 out (通常 os.Stdout) で Forwarder を作る。
func NewForwarder(out io.Writer) *Forwarder {
	return &Forwarder{out: out}
}

// HandleOutput は %output の decoded bytes を処理する。active pane の
// ものだけ verbatim 転送。active 未確定なら最初に出力した pane を採用。
func (f *Forwarder) HandleOutput(paneID string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active == "" {
		f.active = paneID
	}
	if paneID != f.active || f.out == nil {
		return
	}
	_, _ = f.out.Write(data)
}

// SetActive は転送対象 pane を明示設定する (attach 直後に
// display-message で確定した active pane を入れる)。
func (f *Forwarder) SetActive(paneID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active = paneID
}

// Active は現在の転送対象 pane id ("" = 未確定)。
func (f *Forwarder) Active() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active
}
