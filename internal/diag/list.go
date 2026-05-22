package diag

// `claude-master sessions` の実装基盤。~/.claude-master/diag/<pid>.snap
// を走査し、生存 PID の最新 Snap だけを返す。proxy に問合せず ps も
// 叩かない＝負荷ゼロ・「どの cwd の claude が生きているか」を決定論的
// に把握。死亡判定は Sweep と同じ isAlive (OS-split) で一貫。
//
// fail-soft: 破損 snap / read 失敗 / json 不整合は個別に skip し、残る
// 健全な snap は返す（診断系の宿命＝1 個壊れて全部見えなくなる方が
// 害悪）。dir 不在は (nil, nil)＝呼出側で len==0 判定。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ListSessions は既定の diag dir (`~/.claude-master/diag`) を走査する。
func ListSessions() ([]Snap, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return ListSessionsFromDir(filepath.Join(home, ".claude-master", "diag"))
}

// ListSessionsFromDir はテスト用に diag dir を明示できる版。
func ListSessionsFromDir(dir string) ([]Snap, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Snap
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".snap") {
			continue
		}
		pid, ok := extractPID(name, []string{".snap"})
		if !ok {
			continue
		}
		if !isAlive(pid) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var s Snap
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		if s.Pid == 0 {
			s.Pid = pid
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pid < out[j].Pid })
	return out, nil
}
