package agent

import (
	"context"
	"hash/fnv"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/tmux"
)

// remotePalette は PC ごとに割り当てる識別色（読みやすい 8 色）。
var remotePalette = []string{
	"colour39", "colour213", "colour154", "colour208",
	"colour45", "colour220", "colour171", "colour120",
}

// colorFor は pc 名から決定的に色を選ぶ（同 PC は常に同色）。
func colorFor(pc string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(pc))
	return remotePalette[int(h.Sum32())%len(remotePalette)]
}

// shortName はラベルを短く（先頭 ↗＝リモート印＋dir を rune 12 まで）。
func shortName(dir string) string {
	r := []rune(dir)
	if len(r) > 12 {
		r = r[:12]
	}
	return "↗" + string(r)
}

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
			mgr.EnsureCmdWindow(key, shortName(dir), wc(pc, sid, dir))
			// PC ごとの識別色（リモート窓を一目で区別）
			mgr.SetWindowStyle(key, "fg="+colorFor(pc))
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
