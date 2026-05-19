package agent

// restart-proxy（Phase3）のオーケストレーション。当該 sid の claude
// proxy を終了し `claude-master proxy --resume <uuid>` を別プロセスで
// 再起動＝会話を復帰。**全セッション対応**: UUID 鍵はそのまま、pid- 鍵
// （--resume 無し起動で会話 UUID 不明）は claude 自身が cwd ごとに書く
// `~/.claude/projects/<...>/<sessionId>.jsonl` から会話 UUID を解決して
// 復帰する。実 kill/spawn/解決は seam＝テストで実プロセス/実 FS を
// 起こさない。claude --resume 自体（resume-burst 再ストリーム/
// SESSION_LOG/dedup 不変条件）は既存 ptyproxy fixture で担保済。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// claudeUUIDRe は scanner.sessionIDRe と同形（8-4-… の claude session
// UUID）。sid/解決値がこれに合致すれば --resume で復帰できる。
var claudeUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-`)

// IsResumable は sid（session key）が claude UUID 形か。
func IsResumable(sid string) bool { return claudeUUIDRe.MatchString(sid) }

// normPath はパス比較用正規化（区切り統一＋末尾スラッシュ除去）。
func normPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	for len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	return p
}

// pathEqual は claude 記録 cwd と proxy cwd の同一判定（Windows は
// 大文字小文字無視。同一マシン・同一起動由来なので正規化で十分）。
func pathEqual(a, b string) bool {
	na, nb := normPath(a), normPath(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(na, nb)
	}
	return na == nb
}

// ResolveClaudeUUID は projectsRoot（=~/.claude/projects）配下の全
// `<...>/<uuid>.jsonl` を走査し、**jsonl が記録した権威 cwd が target と
// 一致する中で最新 mtime** のセッション UUID を返す。サニタイズ規則を
// 逆算せず claude 自身の記録 cwd で突き合わせる原則的解決（ヒューリス
// ティック分類ではない＝不変条件遵守）。同一 cwd 同時複数 claude のみ
// 最新 mtime で曖昧（稀・既知制約）。見つからなければ ok=false。
func ResolveClaudeUUID(projectsRoot, cwd string) (string, bool) {
	projDirs, err := os.ReadDir(projectsRoot)
	if err != nil {
		return "", false
	}
	var best string
	var bestMod int64 = -1
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		dir := filepath.Join(projectsRoot, pd.Name())
		ents, e := os.ReadDir(dir)
		if e != nil {
			continue
		}
		for _, ent := range ents {
			name := ent.Name()
			if ent.IsDir() || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			uuid := strings.TrimSuffix(name, ".jsonl")
			if !IsResumable(uuid) {
				continue // セッション jsonl のみ（uuid 形）
			}
			fp := filepath.Join(dir, name)
			rc, ok := jsonlCwd(fp)
			if !ok || !pathEqual(rc, cwd) {
				continue
			}
			fi, fe := os.Stat(fp)
			if fe != nil {
				continue
			}
			if m := fi.ModTime().UnixNano(); m > bestMod {
				bestMod, best = m, uuid
			}
		}
	}
	return best, best != ""
}

// jsonlCwd は jsonl 先頭付近のレコードから権威 cwd を取り出す（cwd は
// type=user/isMeta の早期レコードにある）。jsonl=1行1 JSON だが
// json.Decoder は連結 JSON を順次読めるので行長に依存せず安全。先頭
// 30 レコードまでで打ち切り。
func jsonlCwd(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	for i := 0; i < 30; i++ {
		var m map[string]any
		if e := dec.Decode(&m); e != nil {
			if e == io.EOF {
				break
			}
			return "", false
		}
		if c, ok := m["cwd"].(string); ok && c != "" {
			return c, true
		}
	}
	return "", false
}

type ProxyRestarter struct {
	// Lookup は sid→(pid, cwd, ok)。STATUS_FILE 参照で注入。pid- でも
	// ok=true（cwd を UUID 解決に使う）。未発見のみ ok=false。
	Lookup func(sid string) (pid int, cwd string, ok bool)
	// ResolveUUID は cwd→会話 UUID（本番は ResolveClaudeUUID を注入）。
	// pid- セッションの復帰元 UUID 解決に使う。nil 不可（pid- 時必須）。
	ResolveUUID func(cwd string) (string, bool)
	// Kill は proxy プロセス終了（SIGTERM→猶予→SIGKILL を本番実装）。
	Kill func(pid int) error
	// Spawn は `claude-master proxy --resume <uuid>` を cwd で detached
	// 起動（PTY は ptyproxy が内部で開く）。
	Spawn func(uuid, cwd string) error
}

// Restart は sid の proxy を終了→--resume 再起動。UUID sid はそのまま、
// pid- は cwd から会話 UUID を解決。**解決できなければ kill せずエラー**
// （復帰不能のまま claude を殺さない＝セッション保全。誤って無関係
// プロセスも殺さない）。
func (p *ProxyRestarter) Restart(_ context.Context, sid string) error {
	if p.Lookup == nil || p.Kill == nil || p.Spawn == nil {
		return fmt.Errorf("proxy restart 未配線")
	}
	pid, cwd, ok := p.Lookup(sid)
	if !ok {
		return fmt.Errorf("対象セッション未発見: %s", sid)
	}
	uuid := sid
	if !IsResumable(sid) { // pid- 等: claude の jsonl から会話 UUID 解決
		if p.ResolveUUID == nil {
			return fmt.Errorf("UUID 解決 未配線（pid- 復帰不可）")
		}
		u, ok2 := p.ResolveUUID(cwd)
		if !ok2 || !IsResumable(u) {
			return fmt.Errorf("会話 UUID 未特定（cwd=%q の claude jsonl 無し）"+
				"＝kill せず保全", cwd)
		}
		uuid = u
	}
	if err := p.Kill(pid); err != nil {
		return fmt.Errorf("proxy(pid=%d) 終了失敗: %w", pid, err)
	}
	if err := p.Spawn(uuid, cwd); err != nil {
		return fmt.Errorf("proxy --resume 起動失敗: %w", err)
	}
	return nil
}
