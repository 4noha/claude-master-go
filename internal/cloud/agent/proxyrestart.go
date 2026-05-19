package agent

// restart-proxy（Phase3）のオーケストレーション。当該 sid の claude
// proxy を終了し `claude-master proxy --resume <uuid>` を別プロセスで
// 再起動＝会話を復帰。**UUID 鍵セッションのみ**（pid- 鍵は復帰元 UUID が
// 無く原理的に不可＝web で非表示/拒否）。実 kill/spawn は seam＝テストで
// 実プロセスを起こさない。claude --resume 自体（resume-burst 再ストリーム/
// SESSION_LOG/dedup 不変条件）は既存 ptyproxy fixture で担保済。

import (
	"context"
	"fmt"
	"regexp"
)

// claudeUUIDRe は scanner.sessionIDRe と同形（8-4-… の claude session
// UUID）。これに合致する key だけ --resume で復帰できる。
var claudeUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-`)

// IsResumable は sid（session key）が claude UUID 形＝復帰可能か。
func IsResumable(sid string) bool { return claudeUUIDRe.MatchString(sid) }

type ProxyRestarter struct {
	// Lookup は sid→(pid, cwd, ok)。ok=false は「未発見 or UUID でない」
	// ＝復帰不可（本番は STATUS_FILE 参照＋UUID 検査で注入）。
	Lookup func(sid string) (pid int, cwd string, ok bool)
	// Kill は proxy プロセス終了（SIGTERM→猶予→SIGKILL を本番実装）。
	Kill func(pid int) error
	// Spawn は `claude-master proxy --resume <sid>` を cwd で detached
	// 起動（setsid・stdio=/dev/null。PTY は ptyproxy が内部で開く）。
	Spawn func(sid, cwd string) error
}

// Restart は sid の proxy を終了→--resume 再起動。UUID でなければ kill/
// spawn せず明示エラー（誤って無関係プロセスを殺さない）。
func (p *ProxyRestarter) Restart(_ context.Context, sid string) error {
	if !IsResumable(sid) {
		return fmt.Errorf("復帰不可: %q は UUID 鍵でない（--resume 不可。"+
			"新規 claude や pid- セッションは対象外）", sid)
	}
	if p.Lookup == nil || p.Kill == nil || p.Spawn == nil {
		return fmt.Errorf("proxy restart 未配線")
	}
	pid, cwd, ok := p.Lookup(sid)
	if !ok {
		return fmt.Errorf("対象セッション未発見/復帰不可: %s", sid)
	}
	if err := p.Kill(pid); err != nil {
		return fmt.Errorf("proxy(pid=%d) 終了失敗: %w", pid, err)
	}
	if err := p.Spawn(sid, cwd); err != nil {
		return fmt.Errorf("proxy --resume 起動失敗: %w", err)
	}
	return nil
}
