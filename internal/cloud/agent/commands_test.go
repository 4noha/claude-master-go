package agent

// 実 Firestore エミュレータ＋fake seam（実 launchctl/自己置換しない）で
// 命令 dispatch・多層 revocation・self-update・未配線(Phase3)を検証。
// 合成サーバなし＝実 WatchCommands/claim/Ack 経路を通す。

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/state"
)

func waitCmdStatus(t *testing.T, st *state.Client, pc, id string,
	to time.Duration) state.Command {
	t.Helper()
	dl := time.Now().Add(to)
	for time.Now().Before(dl) {
		cs, _ := st.RecentCommands(context.Background(), pc, 20)
		for _, c := range cs {
			if c.ID == id && (c.Status == "done" || c.Status == "error") {
				return c
			}
		}
		time.Sleep(120 * time.Millisecond)
	}
	t.Fatalf("命令 %s が done/error に至らない（dispatch 不成立）", id)
	return state.Command{}
}

func TestCommandRunnerDispatchAndRevocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pc := "cmd-agent"
	st := newState(t, pc)

	var restarts, updates atomic.Int32
	var revoked atomic.Bool
	cr := &CommandRunner{
		St:      st,
		Revoked: func(context.Context) bool { return revoked.Load() },
		DoRestart: func(context.Context) error {
			restarts.Add(1)
			return nil
		},
		DoUpdate: func(context.Context) (string, bool, error) {
			updates.Add(1)
			return "v9.9.9", true, nil
		},
		// DoProxy 未設定＝restart-proxy は未配線エラー(Phase3)
	}
	go func() { _ = cr.Run(ctx) }()
	time.Sleep(1500 * time.Millisecond) // listener attach

	push := func(cmd, sid string) string {
		id, err := st.PushCommand(ctx, pc, cmd, sid, "owner@example.com")
		if err != nil {
			t.Fatalf("PushCommand(%s): %v", cmd, err)
		}
		return id
	}

	// restart-agent: DoRestart 発火・Ack done（Ack 先行設計）
	c := waitCmdStatus(t, st, pc, push("restart-agent", ""), 8*time.Second)
	if c.Status != "done" || restarts.Load() != 1 {
		t.Fatalf("restart-agent: status=%s restarts=%d", c.Status, restarts.Load())
	}

	// self-update: DoUpdate 発火・tag が監査に残る・更新後 restart も発火
	c = waitCmdStatus(t, st, pc, push("self-update", ""), 8*time.Second)
	if c.Status != "done" || updates.Load() != 1 ||
		!strings.Contains(c.Detail, "v9.9.9") || restarts.Load() != 2 {
		t.Fatalf("self-update: %+v updates=%d restarts=%d",
			c, updates.Load(), restarts.Load())
	}

	// restart-proxy 未配線(Phase3) → error
	c = waitCmdStatus(t, st, pc, push("restart-proxy", "sid-x"), 8*time.Second)
	if c.Status != "error" || !strings.Contains(c.Detail, "Phase3") {
		t.Fatalf("restart-proxy 未配線が error にならない: %+v", c)
	}

	// revocation: 失効中は実行拒否（DoRestart 増えない・error revoked）
	revoked.Store(true)
	c = waitCmdStatus(t, st, pc, push("restart-agent", ""), 8*time.Second)
	if c.Status != "error" || !strings.Contains(c.Detail, "revoked") {
		t.Fatalf("失効中に実行拒否されない: %+v", c)
	}
	if restarts.Load() != 2 {
		t.Fatalf("失効中なのに DoRestart が発火: %d", restarts.Load())
	}
}
