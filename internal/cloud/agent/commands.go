package agent

// 遠隔命令の購読・実行・監査。owner 限定は web 側、ここは多層防御として
// revocation を再検査し allowlist 分岐のみ実行する。実行系は seam＝
// テストで実 launchctl/自己バイナリ置換をしない。破壊的命令
// (restart-agent/self-update) は **Ack を先行**してから実行＝再起動で
// agent が死んでも監査が done で残る（後 Ack だと永遠に running）。

import (
	"context"

	"github.com/4noha/claude-master-go/internal/cloud/state"
)

type CommandRunner struct {
	St      *state.Client
	Revoked func(context.Context) bool // 既定 St.IsSelfRevoked
	// DoRestart は launchd 2 デーモンを kickstart -k（自己も含むため
	// 戻らないことがある＝Ack 先行が前提）。
	DoRestart func(context.Context) error
	// DoUpdate は selfupdate.Update（戻り値: tag, 更新有無, err）。
	DoUpdate func(context.Context) (tag string, updated bool, err error)
	// DoProxy は当該 sid の proxy を --resume 再起動（Phase3。nil 可）。
	DoProxy func(ctx context.Context, sid string) error
}

// Run は命令制御線（WatchCommands）を回す。claim 済命令のみ handle。
func (cr *CommandRunner) Run(ctx context.Context) error {
	return cr.St.WatchCommands(ctx, func(cm state.Command) {
		cr.handle(ctx, cm)
	})
}

func (cr *CommandRunner) handle(ctx context.Context, cm state.Command) {
	revoked := cr.Revoked
	if revoked == nil {
		revoked = cr.St.IsSelfRevoked
	}
	if revoked(ctx) {
		_ = cr.St.AckCommand(ctx, cm.ID, "error", "revoked: 実行拒否")
		return
	}

	switch cm.Cmd {
	case "restart-agent":
		if cr.DoRestart == nil {
			_ = cr.St.AckCommand(ctx, cm.ID, "error", "restart 未配線")
			return
		}
		// Ack 先行（kickstart -k は自己を SIGKILL し戻らない）
		_ = cr.St.AckCommand(ctx, cm.ID, "done", "launchd 2デーモン再起動を実行")
		_ = cr.DoRestart(ctx)

	case "self-update":
		if cr.DoUpdate == nil {
			_ = cr.St.AckCommand(ctx, cm.ID, "error", "update 未配線")
			return
		}
		tag, up, err := cr.DoUpdate(ctx)
		if err != nil {
			_ = cr.St.AckCommand(ctx, cm.ID, "error", "更新失敗: "+err.Error())
			return
		}
		if !up {
			_ = cr.St.AckCommand(ctx, cm.ID, "done", "既に最新: "+tag)
			return
		}
		// 更新成功→再起動で新バイナリを読む。再起動前に Ack。
		_ = cr.St.AckCommand(ctx, cm.ID, "done", "更新 "+tag+"→再起動を実行")
		if cr.DoRestart != nil {
			_ = cr.DoRestart(ctx)
		}

	case "restart-proxy":
		if cr.DoProxy == nil {
			_ = cr.St.AckCommand(ctx, cm.ID, "error",
				"restart-proxy 未配線(Phase3)")
			return
		}
		if err := cr.DoProxy(ctx, cm.SID); err != nil {
			_ = cr.St.AckCommand(ctx, cm.ID, "error",
				"proxy 再起動失敗: "+err.Error())
			return
		}
		_ = cr.St.AckCommand(ctx, cm.ID, "done",
			"proxy を --resume 再起動: "+cm.SID)

	default:
		_ = cr.St.AckCommand(ctx, cm.ID, "error", "未知のコマンド: "+cm.Cmd)
	}
}
