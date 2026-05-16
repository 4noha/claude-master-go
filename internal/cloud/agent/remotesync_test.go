package agent

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/tmux"
)

// 合成不使用: 実 Firestore エミュレータ＋実 tmux 隔離セッション。
// runaway バグの核心（再 reconcile / agent 再起動相当で窓が重複生成
// されないこと）を機械検証する。

// テスト用 window コマンド: 実 cloud attach の代わりに無害な sleep。
// ただし pane_start_command に "claude-master cloud attach <sid> --pc
// <pc>"（= MarkedWindows の needle＋attachMarker）を必ず含める。
func testWC(pc, sid, dir string) string {
	return "sleep 600 # /x/claude-master " + attachMarker(pc, sid)
}

func remoteWinCount(mgr *tmux.Manager) int {
	mw, _ := mgr.MarkedWindows("claude-master cloud attach ")
	return len(mw)
}

func TestReconcileRemoteStatelessNoDuplicate(t *testing.T) {
	if err := tmux.CheckTmux(); err != nil {
		t.Skipf("tmux 不在: %v", err)
	}
	tsession := "cm-rtsync-" + strconv.Itoa(os.Getpid())
	mgr, err := tmux.NewManager(tsession)
	if err != nil {
		t.Skipf("NewManager: %v", err)
	}
	mgr.EnsureSession()
	defer exec.Command("tmux", "kill-session", "-t", tsession).Run()

	ctx := context.Background()
	const selfPC = "this-pc"
	ra, _ := state.New(ctx, projectID, "remoteA")
	defer ra.Close()
	ra.RegisterPC(ctx)
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

	ReconcileRemote(ctx, me, mgr, selfPC, testWC)
	if n := remoteWinCount(mgr); n != 2 {
		t.Fatalf("初回 reconcile でリモート窓が 2 でない: %d", n)
	}
	// 短縮名＋自 PC 除外
	wins := strings.Join(mgr.ListWindows(), ",")
	if !strings.Contains(wins, "↗projA") || !strings.Contains(wins, "↗projB") {
		t.Fatalf("短縮窓名が想定外: %s", wins)
	}
	if strings.Contains(wins, "↗mine") {
		t.Fatalf("自 PC セッションを誤同期: %s", wins)
	}

	// ★ runaway 修正の核心: 何度 reconcile しても増えない（冪等）
	for i := 0; i < 5; i++ {
		ReconcileRemote(ctx, me, mgr, selfPC, testWC)
	}
	if n := remoteWinCount(mgr); n != 2 {
		t.Fatalf("再 reconcile で窓が増殖（runaway 未修正）: %d", n)
	}

	// ★ agent 再起動相当: 別 Manager（in-process 状態ゼロ）でも増えない
	mgr2, _ := tmux.NewManager(tsession)
	ReconcileRemote(ctx, me, mgr2, selfPC, testWC)
	if n := remoteWinCount(mgr2); n != 2 {
		t.Fatalf("再起動相当で窓が重複生成（stateless 検出失敗）: %d", n)
	}

	// 既存 runaway 窓の自己修復: 同 marker の重複窓を手で作る → reconcile で 1 本に
	mgr.NewWindow("dup", testWC("remoteA", "rs1", "projA"))
	if remoteWinCount(mgr) != 3 {
		t.Fatalf("重複窓を作れていない: %d", remoteWinCount(mgr))
	}
	ReconcileRemote(ctx, me, mgr, selfPC, testWC)
	if n := remoteWinCount(mgr); n != 2 {
		t.Fatalf("重複窓が自己修復されない: %d", n)
	}

	// PC 別識別色が実 tmux per-window option に入っている
	mwc, _ := mgr.MarkedWindows("claude-master cloud attach ")
	for id := range mwc {
		so, _ := exec.Command("tmux", "show-options", "-w", "-t",
			tsession+":"+id, "window-status-style").CombinedOutput()
		if !strings.Contains(string(so), colorFor("remoteA")) {
			t.Fatalf("リモート窓に識別色が無い: id=%s opt=%q", id, string(so))
		}
	}

	// リモート PC 消失 → reconcile で窓も消える
	ra.DeletePC(ctx)
	ReconcileRemote(ctx, me, mgr, selfPC, testWC)
	if n := remoteWinCount(mgr); n != 0 {
		t.Fatalf("リモート消失後も窓が残る: %d", n)
	}
}

// push: sessions 変更で WatchSessions の cb が発火し、ctx cancel で戻る。
func TestWatchSessionsPush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := state.New(ctx, projectID, "wpc")
	defer c.Close()
	fired := make(chan struct{}, 8)
	done := make(chan error, 1)
	go func() { done <- c.WatchSessions(ctx, func() { fired <- struct{}{} }) }()
	time.Sleep(1500 * time.Millisecond) // listener attach

	c.RegisterPC(ctx)
	c.PushStatus(ctx, []map[string]any{
		{"key": "w1", "session_id": "w1", "short_dir": "d", "is_active": true,
			"pid": float64(1), "cwd": "/x", "start_time": "t",
			"cpu_percent": float64(0), "mem_mb": float64(0)}})
	select {
	case <-fired:
	case <-time.After(8 * time.Second):
		t.Fatal("sessions 変更で push 通知が来ない")
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("ctx cancel で error: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctx cancel で WatchSessions が戻らない")
	}
}

// fail-safe: tmux list が失敗する周は窓を作らない（runaway 真因の修正）。
func TestReconcileAbortsOnTmuxListError(t *testing.T) {
	if err := tmux.CheckTmux(); err != nil {
		t.Skipf("tmux 不在: %v", err)
	}
	ctx := context.Background()
	ra, _ := state.New(ctx, projectID, "remoteB")
	defer ra.Close()
	ra.RegisterPC(ctx)
	ra.PushStatus(ctx, []map[string]any{
		{"key": "rb1", "session_id": "rb1", "short_dir": "p", "is_active": true,
			"pid": float64(1), "cwd": "/a", "start_time": "x",
			"cpu_percent": float64(0), "mem_mb": float64(0)}})
	me, _ := state.New(ctx, projectID, "this-pc2")
	defer me.Close()
	me.RegisterPC(ctx)

	// 存在しない tmux セッション → list-windows がエラー
	miss := "cm-noexist-" + strconv.Itoa(os.Getpid())
	mgr, err := tmux.NewManager(miss)
	if err != nil {
		t.Skip(err)
	}
	defer exec.Command("tmux", "kill-session", "-t", miss).Run()
	if _, e := mgr.MarkedWindows("x"); e == nil {
		t.Fatal("不在セッションの MarkedWindows がエラーを返さない")
	}
	// fail-safe: 作成されない（セッションも作られない）
	ReconcileRemote(ctx, me, mgr, "this-pc2", testWC)
	if exec.Command("tmux", "has-session", "-t", miss).Run() == nil {
		t.Fatal("list 失敗周なのに窓/セッションを作成した（runaway 未修正）")
	}
}
