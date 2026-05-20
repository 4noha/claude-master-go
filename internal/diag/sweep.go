package diag

// stale ファイルの累積回避（proxy 起動時 sweep）。VSCode SIGHUP 連鎖や
// SIGKILL では run.go の defer が走らず <pid>.sock/<pid>.status.json/
// <pid>.snap が残骸として蓄積する（2026-05-20 の事故では 17 日で 249
// 件まで肥大）。提案フロー: 新規 proxy が runProxy 入口で Sweep を 1 度
// 呼ぶ＝**自分の PID は alive 判定なので必ず除外**＝race-safe（複数
// proxy の同時 sweep でも rm は ENOENT 無視＝harmless）。
//
// 「PID が live でない」判定は OS-split: unix=Signal(0)（=kill -0 同等）、
// windows=OpenProcess+GetExitCodeProcess (alive_windows.go)。
//
// 制約: PID 再利用で別プロセスが alive 扱いになる「偽 live」は許容
// （手で消すまで残る・実害は軽微＝scanner はファイル中身の updated_at
// が古いものを ignore する）。決定論的厳密化は将来 issue（mtime + age
// 閾値で「明らかに古い PID」を破棄）。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sweep は sessionsDir 配下の <pid>.sock/<pid>.status.json と diagDir
// 配下の <pid>.snap のうち、PID が live でないものを削除する。戻り値は
// (sessions 削除数, snap 削除数)。dir が無ければ 0/0 返し。PID 形式以外
// （remote_sessions.json 等）は触らない。fail-soft: 個別 rm 失敗は黙殺。
func Sweep(sessionsDir, diagDir string) (sessRemoved, snapRemoved int) {
	if sessionsDir != "" {
		if ents, err := os.ReadDir(sessionsDir); err == nil {
			for _, ent := range ents {
				if ent.IsDir() {
					continue
				}
				name := ent.Name()
				pid, ok := extractPID(name, []string{".sock", ".status.json"})
				if !ok {
					continue // PID 形式でない＝remote_sessions.json 等は対象外
				}
				if isAlive(pid) {
					continue
				}
				if os.Remove(filepath.Join(sessionsDir, name)) == nil {
					sessRemoved++
				}
			}
		}
	}
	if diagDir != "" {
		if ents, err := os.ReadDir(diagDir); err == nil {
			for _, ent := range ents {
				if ent.IsDir() {
					continue
				}
				name := ent.Name()
				pid, ok := extractPID(name, []string{".snap"})
				if !ok {
					continue
				}
				if isAlive(pid) {
					continue
				}
				if os.Remove(filepath.Join(diagDir, name)) == nil {
					snapRemoved++
				}
			}
		}
	}
	return
}

// extractPID は filename から「<pid><suffix>」の pid を抽出。suffix は
// 長い順で試す（.status.json を .json より先に剥がす）。pid 形式でない
// 場合は ok=false。
func extractPID(name string, suffixes []string) (int, bool) {
	for _, suf := range suffixes {
		if strings.HasSuffix(name, suf) {
			base := strings.TrimSuffix(name, suf)
			if pid, err := strconv.Atoi(base); err == nil && pid > 0 {
				return pid, true
			}
			return 0, false
		}
	}
	return 0, false
}
