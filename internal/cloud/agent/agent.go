// Package agent は source PC 常駐のクラウド同期統括。制御線
// （Firestore WatchWake＝常時・無料）で wake を受け、その sid の
// ローカル unix socket を WSS relay へブリッジ（データ線）、quiescence
// （無通信 idle）でデータ線を閉じて wake 待ちに戻る。
//
// 画面解釈はしない（relay/agent はバイト透過）。データ線は wake 時のみ
// 開き静止で閉じる＝DESIGN_M6 の「差分時のみ・静止で切断」。
package agent

import (
	"context"
	"sync"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/relay"
	"github.com/4noha/claude-master-go/internal/cloud/state"
)

type Agent struct {
	St       *state.Client
	RelayURL string
	// ResolveSock は sid（claude session_id）→ ローカル <pid>.sock。
	// 本番は STATUS_FILE / sessions ディレクトリ参照で注入。
	ResolveSock func(sid string) (string, bool)
	IdleClose   time.Duration       // データ線の静止切断猶予
	OnDataClosed func(sid string)   // 任意フック（テスト/メトリクス）

	mu     sync.Mutex
	active map[string]bool // 同 sid の二重ブリッジ防止
}

// Run は制御線（WatchWake）を回す。wake ごとに handleWake を goroutine
// 起動（watcher は止めない）。ctx 終了でクリーンに戻る。
func (a *Agent) Run(ctx context.Context) error {
	if a.active == nil {
		a.active = map[string]bool{}
	}
	return a.St.WatchWake(ctx, func(sid string) {
		go a.handleWake(ctx, sid)
	})
}

func (a *Agent) handleWake(ctx context.Context, sid string) {
	a.mu.Lock()
	if a.active[sid] {
		a.mu.Unlock()
		return // 既にデータ線が開いている sid は無視
	}
	a.active[sid] = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.active, sid)
		a.mu.Unlock()
		if a.OnDataClosed != nil {
			a.OnDataClosed(sid)
		}
	}()

	sock, ok := a.ResolveSock(sid)
	if !ok {
		return // そのセッションがこの PC に無い
	}
	// データ線: relay ⇄ ローカル unix socket。idle で自動クローズ。
	_ = relay.BridgeSourceIdle(ctx, a.RelayURL, sid, sock, a.IdleClose)
}
