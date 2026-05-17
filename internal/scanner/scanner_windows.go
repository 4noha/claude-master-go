//go:build windows

package scanner

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// winClaudeBase は argv[0] が claude かを **完全一致**で判定する
// （ヒューリスティック内容推測はしない＝不変条件遵守）。Windows パスは
// `\`・拡張子付き（claude.exe/.cmd/.bat）なので basename を正規化。
// 併せて unix 形（*/claude）も共有 isClaudeCmdBase で許容。
func winClaudeBase(arg0 string) bool {
	if isClaudeCmdBase(arg0) {
		return true
	}
	b := strings.ToLower(filepath.Base(arg0))
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		b = strings.TrimSuffix(b, ext)
	}
	return b == "claude"
}

// Scan は CIM(Win32_Process) で全プロセスの PID＋CommandLine を取得し、
// 共有純パーサ（extractSessionID/isVSCodeSession）で claude セッションを
// 列挙する（unix の `ps aux`＋parsePSLine 経路に対応する Windows 実体）。
// cwd は Windows では他プロセスの取得が PEB 読取を要し脆いため M8d では
// 空（ShortDir は "unknown"）＝best-effort。StartTime/CPU/Mem も同様に
// 省略（Key/SessionID/同期に不要・Python は表示用のみ）。
func Scan(includeVSCode bool) ([]ClaudeSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// PID<TAB>CommandLine を 1 行ずつ。CommandLine は空白を含むので
	// 数値 PID と TAB 区切り（cmdline に TAB は通常出ない）。
	script := "Get-CimInstance Win32_Process | ForEach-Object { " +
		"\"$($_.ProcessId)`t$($_.CommandLine)\" }"
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile",
		"-NonInteractive", "-Command", script).Output()
	if err != nil {
		return nil, err
	}
	var sessions []ClaudeSession
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimRight(ln, "\r")
		i := strings.IndexByte(ln, '\t')
		if i <= 0 {
			continue
		}
		pid, e := strconv.Atoi(strings.TrimSpace(ln[:i]))
		if e != nil {
			continue
		}
		cmdline := strings.TrimSpace(ln[i+1:])
		if cmdline == "" {
			continue
		}
		fields := strings.Fields(cmdline)
		if len(fields) == 0 || !winClaudeBase(fields[0]) {
			continue
		}
		if isVSCodeSession(fields) && !includeVSCode {
			continue
		}
		sessions = append(sessions, ClaudeSession{
			Pid:       pid,
			SessionID: extractSessionID(fields),
			// Cwd/StartTime/CPU/Mem は Windows では best-effort 省略。
		})
	}
	return sessions, nil
}
