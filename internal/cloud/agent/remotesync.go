package agent

import (
	"context"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/tmux"
)

// RemoteSession は他 PC のセッション 1 つ（tmux 窓化の単位）。
type RemoteSession struct{ PC, SID, Dir string }

// WindowCmd は (pc,sid,dir) からその窓で動かすコマンドを作る
// （通常は cloud attach の再接続ループ）。
type WindowCmd func(pc, sid, dir string) string

func sessSID(s map[string]any) string {
	if v, _ := s["key"].(string); v != "" {
		return v
	}
	if v, _ := s["session_id"].(string); v != "" {
		return v
	}
	return ""
}

// SyncRemoteOnce は 1 周分: 自分以外の PC の全セッションに対応する
// tmux 窓を確保し、消えたものを閉じる。known（key→在席）を更新して返す。
// key は "R:<pc>/<sid>"（ローカル監視の key と名前空間衝突しない）。
func SyncRemoteOnce(ctx context.Context, st *state.Client, mgr *tmux.Manager,
	selfPC string, wc WindowCmd, known map[string]bool) map[string]bool {

	cur := map[string]bool{}
	pcs, _ := st.ListPCs(ctx)
	for _, pc := range pcs {
		if pc == selfPC || pc == "" {
			continue // 自 PC は監視デーモンがローカルで窓化済
		}
		ss, _ := st.ListSessions(ctx, pc)
		for _, s := range ss {
			sid := sessSID(s)
			if sid == "" {
				continue
			}
			dir, _ := s["short_dir"].(string)
			if dir == "" {
				dir = sid
			}
			key := "R:" + pc + "/" + sid
			cur[key] = true
			mgr.EnsureCmdWindow(key, pc+"-"+dir, wc(pc, sid, dir))
		}
	}
	for k := range known {
		if !cur[k] {
			mgr.RemoveWindow(k) // リモートで消えたセッションの窓を閉じる
		}
	}
	return cur
}

// RunRemoteTmuxSync は他 PC の claude セッションを this PC の tmux へ
// interval ごとに同期し続ける（cloud agent から起動）。ctx 終了で戻る。
func RunRemoteTmuxSync(ctx context.Context, st *state.Client, mgr *tmux.Manager,
	selfPC string, wc WindowCmd, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	known := map[string]bool{}
	for {
		known = SyncRemoteOnce(ctx, st, mgr, selfPC, wc, known)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
