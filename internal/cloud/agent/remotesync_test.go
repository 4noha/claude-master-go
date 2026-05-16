package agent

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/tmux"
)

// 合成不使用: 実 Firestore エミュレータ（TestMain で起動済）＋実 tmux
// 隔離セッションで、他 PC のセッションが this PC の tmux に窓として
// 同期され、自 PC は除外、消えたら窓も消えることを機械検証。

func TestSyncRemoteOnceRealFirestoreRealTmux(t *testing.T) {
	if err := tmux.CheckTmux(); err != nil {
		t.Skipf("tmux 不在: %v", err)
	}
	tsession := "cm-rtsync-" + strconv.Itoa(os.Getpid())
	mgr, err := tmux.NewManager(tsession)
	if err != nil {
		t.Skipf("NewManager: %v", err)
	}
	mgr.EnsureSession()
	exec.Command("tmux", "set-option", "-t", tsession,
		"automatic-rename", "off").Run()
	defer exec.Command("tmux", "kill-session", "-t", tsession).Run()

	ctx := context.Background()
	const selfPC = "this-pc"
	// 他 PC（remoteA）に 2 セッション、自 PC にも 1 セッション登録
	ra, _ := state.New(ctx, projectID, "remoteA")
	defer ra.Close()
	if err := ra.RegisterPC(ctx); err != nil {
		t.Fatal(err)
	}
	ra.PushStatus(ctx, []map[string]any{
		{"key": "rs1", "session_id": "rs1", "short_dir": "projA",
			"is_active": true, "pid": float64(1), "cwd": "/a",
			"start_time": "x", "cpu_percent": float64(0), "mem_mb": float64(0)},
		{"key": "rs2", "session_id": "rs2", "short_dir": "projB",
			"is_active": false, "pid": float64(2), "cwd": "/b",
			"start_time": "y", "cpu_percent": float64(0), "mem_mb": float64(0)},
	})
	me, _ := state.New(ctx, projectID, selfPC)
	defer me.Close()
	me.RegisterPC(ctx)
	me.PushStatus(ctx, []map[string]any{
		{"key": "self1", "session_id": "self1", "short_dir": "mine",
			"is_active": true, "pid": float64(9), "cwd": "/m",
			"start_time": "z", "cpu_percent": float64(0), "mem_mb": float64(0)}})

	wc := func(pc, sid, dir string) string { return "sleep 120" } // 無害

	known := SyncRemoteOnce(ctx, me, mgr, selfPC, wc, map[string]bool{})

	wins := strings.Join(mgr.ListWindows(), ",")
	// remoteA の 2 セッションが窓化されている
	if mgr.WindowFor("R:remoteA/rs1") == "" ||
		mgr.WindowFor("R:remoteA/rs2") == "" {
		t.Fatalf("リモートセッションが窓化されない: %s", wins)
	}
	if !strings.Contains(wins, "remoteA-projA") ||
		!strings.Contains(wins, "remoteA-projB") {
		t.Fatalf("窓名が想定外: %s", wins)
	}
	// 自 PC のセッションは窓化しない（ローカル監視の担当）
	if mgr.WindowFor("R:this-pc/self1") != "" ||
		strings.Contains(wins, "this-pc-mine") {
		t.Fatalf("自 PC セッションを誤同期: %s", wins)
	}

	// remoteA の rs2 を削除 → 次パスで窓も閉じる
	ra.PushStatus(ctx, []map[string]any{}) // no-op（差分なし）
	// セッション doc を直接消すため DeletePC で remoteA ごと除去
	ra.DeletePC(ctx)
	known = SyncRemoteOnce(ctx, me, mgr, selfPC, wc, known)
	if known["R:remoteA/rs1"] || known["R:remoteA/rs2"] {
		t.Fatalf("削除後も known に残る: %v", known)
	}
	w := strings.Join(mgr.ListWindows(), ",")
	if strings.Contains(w, "remoteA-projA") || strings.Contains(w, "remoteA-projB") {
		t.Fatalf("リモート消失後も窓が残る: %s", w)
	}
}
