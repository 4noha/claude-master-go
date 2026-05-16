package agent

import (
	"context"
	"hash/fnv"
	"strings"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/tmux"
)

// remotePalette は PC ごとに割り当てる識別色（読みやすい 8 色）。
var remotePalette = []string{
	"colour39", "colour213", "colour154", "colour208",
	"colour45", "colour220", "colour171", "colour120",
}

func colorFor(pc string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(pc))
	return remotePalette[int(h.Sum32())%len(remotePalette)]
}

func shortName(dir string) string {
	r := []rune(dir)
	if len(r) > 12 {
		r = r[:12]
	}
	return "↗" + string(r)
}

// WindowCmd は (pc,sid,dir) からその窓で動かすコマンドを作る。
// 生成コマンドには必ず attachMarker(pc,sid) を含めること（stateless
// 在席判定のキー）。
type WindowCmd func(pc, sid, dir string) string

// attachMarker は窓を (pc,sid) で一意特定する pane_start_command 内の
// 安定部分文字列。auto-rename・agent 再起動に影響されない真の在席キー。
func attachMarker(pc, sid string) string {
	return "cloud attach " + sid + " --pc " + pc
}

func sessSID(s map[string]any) string {
	if v, _ := s["key"].(string); v != "" {
		return v
	}
	if v, _ := s["session_id"].(string); v != "" {
		return v
	}
	return ""
}

// ReconcileRemote は **stateless** に「他 PC の全セッション」と
// 「tmux 上の既存リモート窓（pane_start_command で判定）」を突き合わせ、
// 不足を作成・余剰/重複/消失を kill する。in-process 状態に依存しない
// ため agent 再起動や tmux auto-rename でも重複生成しない（runaway 修正）。
func ReconcileRemote(ctx context.Context, st *state.Client, mgr *tmux.Manager,
	selfPC string, wc WindowCmd) {

	// desired: marker -> (pc,sid,dir)
	type meta struct{ pc, sid, dir string }
	desired := map[string]meta{}
	pcs, _ := st.ListPCs(ctx)
	for _, pc := range pcs {
		if pc == selfPC || pc == "" {
			continue // 自 PC はローカル監視デーモンの担当
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
			desired[attachMarker(pc, sid)] = meta{pc, sid, dir}
		}
	}

	// current: tmux 上のリモート窓（window_id -> start command）
	cur := mgr.MarkedWindows("claude-master cloud attach ")

	// desired ごとに、対応する現存窓を集計（0=作成、1=維持、2+=重複→余剰kill）
	for marker, d := range desired {
		var ids []string
		for id, cmd := range cur {
			if strings.Contains(cmd, marker) {
				ids = append(ids, id)
				delete(cur, id) // desired 済みは cur から外す
			}
		}
		switch {
		case len(ids) == 0:
			id := mgr.NewWindow(shortName(d.dir), wc(d.pc, d.sid, d.dir))
			mgr.StyleWindowID(id, "fg="+colorFor(d.pc))
		default:
			mgr.StyleWindowID(ids[0], "fg="+colorFor(d.pc)) // 1 本維持
			for _, extra := range ids[1:] {
				mgr.KillWindowID(extra) // 重複（過去 runaway）を自己修復
			}
		}
	}
	// desired に無い残りはリモートで消えたセッション → kill
	// （cur は window_id→cmd。キー＝window_id で kill すること）
	for id := range cur {
		mgr.KillWindowID(id)
	}
}

// RunRemoteTmuxSync は他 PC の claude セッションを this PC の tmux へ
// 同期し続ける。**push 駆動**: 起動時に 1 回 reconcile し、その後は
// Firestore の sessions 変更通知（WatchSessions）が来るたびに reconcile
// （5s ポーリング廃止）。バースト変更は最小デバウンスで coalesce。
// ctx 終了で戻る。WatchSessions がエラー終了したら再購読する。
func RunRemoteTmuxSync(ctx context.Context, st *state.Client, mgr *tmux.Manager,
	selfPC string, wc WindowCmd) {

	trigger := make(chan struct{}, 1)
	kick := func() {
		select {
		case trigger <- struct{}{}:
		default: // 既に保留中（coalesce）
		}
	}
	kick() // 初期同期

	// reconcile 実行ループ（最小デバウンス 1s でバーストを集約）
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-trigger:
				ReconcileRemote(ctx, st, mgr, selfPC, wc)
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}
	}()

	// Firestore push 購読（落ちたら短間隔で再購読。ポーリングではない）
	for {
		if err := st.WatchSessions(ctx, kick); err == nil || ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}
