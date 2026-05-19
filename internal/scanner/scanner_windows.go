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

// splitWinCmdline は Windows の CommandLine 文字列を argv へ分割する
// （CommandLineToArgvW の引用符/バックスラッシュ規則に忠実。実 claude は
// `"C:\path\claude.exe" --resume <uuid>` と**明示引用符付き**で起動され、
// strings.Fields だと引用符が token に残り winClaudeBase が誤判定する
// ＝M8d follow-up で顕在化した実バグの修正）。ヒューリスティックではなく
// Windows 標準の字句規則そのもの（不変条件: 内容推測しない）。
func splitWinCmdline(s string) []string {
	var args []string
	var cur []rune
	inQuote := false
	bs := 0 // 直前に連続したバックスラッシュ数
	flushBS := func(beforeQuote bool) {
		if beforeQuote {
			for i := 0; i < bs/2; i++ {
				cur = append(cur, '\\')
			}
		} else {
			for i := 0; i < bs; i++ {
				cur = append(cur, '\\')
			}
		}
		bs = 0
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\':
			bs++
		case c == '"':
			if bs%2 == 1 { // 2n+1 個の \ → リテラル "
				flushBS(true)
				cur = append(cur, '"')
			} else { // 2n 個 → " は引用符トグル
				flushBS(true)
				inQuote = !inQuote
			}
		case (c == ' ' || c == '\t') && !inQuote:
			flushBS(false)
			if len(cur) > 0 {
				args = append(args, string(cur))
				cur = cur[:0]
			}
		default:
			flushBS(false)
			cur = append(cur, c)
		}
	}
	flushBS(false)
	if len(cur) > 0 {
		args = append(args, string(cur))
	}
	return args
}

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
		// strings.Fields ではなく Windows 字句規則で分割（引用符付き
		// フルパス argv[0] を正しく 1 token 化＝実 claude 検出の要）。
		fields := splitWinCmdline(cmdline)
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
