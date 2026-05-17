// Package scanner は Claude Code CLI プロセスを検出する。Python
// process_scanner.py の移植。識別キーは PID でなく session_id
// （--resume 値）＝再起動で不変。
//
// 本ファイルは OS 非依存の共有部（型＋純パーサ: parsePSLine /
// extractSessionID / splitWSN / unescapeLsof 等）。プロセス列挙の
// OS 実体は build-tag 分割: scanner_unix.go=`ps aux`＋`lsof`（M8d 前と
// バイト同一＝darwin/linux parity）、scanner_windows.go=CIM
// (Win32_Process)。
package scanner

import (
	"regexp"
	"strconv"
	"strings"
)

// ClaudeSession は 1 つの claude プロセス。Python ClaudeSession 同等。
type ClaudeSession struct {
	Pid        int
	Cwd        string
	SessionID  string // --resume の UUID（無ければ ""＝新規）
	StartTime  string
	CPUPercent float64
	MemMB      float64
}

// Key は同期の識別キー。session_id があればそれ、無ければ pid-<n>
// （Python ClaudeSession.key と同一）。
func (s ClaudeSession) Key() string {
	if s.SessionID != "" {
		return s.SessionID
	}
	return "pid-" + strconv.Itoa(s.Pid)
}

// ShortDir は cwd 末尾ディレクトリ名（Python short_dir と同一）。
func (s ClaudeSession) ShortDir() string {
	d := strings.TrimRight(s.Cwd, "/")
	if d == "" {
		return "unknown"
	}
	parts := strings.Split(d, "/")
	last := parts[len(parts)-1]
	if last == "" {
		return "unknown"
	}
	return last
}

var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-`)

// extractSessionID は cmdline から `--resume <uuid>` を取り出す
// （Python _extract_session_id と同一: 値が UUID 形でなければ無視）。
func extractSessionID(cmdline []string) string {
	for i, arg := range cmdline {
		if arg == "--resume" && i+1 < len(cmdline) {
			v := cmdline[i+1]
			if sessionIDRe.MatchString(v) {
				return v
			}
		}
	}
	return ""
}

// isVSCodeSession: VS Code 拡張は `--output-format stream-json`（TTY 無し
// ＝tmux 同期対象外）。Python _is_vscode_session と同一（join して両方含む）。
func isVSCodeSession(cmdline []string) bool {
	j := strings.Join(cmdline, " ")
	return strings.Contains(j, "--output-format") && strings.Contains(j, "stream-json")
}

// isClaudeCmdBase: COMMAND 先頭トークンが claude か */claude
// （Python _scan_ps の cmd_base 判定と同一）。
func isClaudeCmdBase(cmdBase string) bool {
	return cmdBase == "claude" || strings.HasSuffix(cmdBase, "/claude")
}

// splitWSN は Python str.split(None, n) 同等: 連続空白を 1 区切り、
// 先頭空白無視、最大 n 回分割（余りは最後の要素にそのまま残す）。
// macOS `ps aux` は列内に空白を含むため strings.Fields では COMMAND が
// 壊れる（忠実移植のため自前）。
func splitWSN(s string, n int) []string {
	var out []string
	i, L := 0, len(s)
	for {
		for i < L && (s[i] == ' ' || s[i] == '\t') { // 先頭/区切り空白を飛ばす
			i++
		}
		if i >= L {
			break
		}
		if len(out) == n { // これ以上分割しない＝残り全部を最後の要素に
			out = append(out, s[i:])
			break
		}
		j := i
		for j < L && s[j] != ' ' && s[j] != '\t' {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

// parsePSLine は `ps aux` 1 行を ClaudeSession へ（cwd は未解決）。
// claude でない / VS Code（includeVSCode=false 時）/ 不正は ok=false。
// Python _scan_ps のループ本体と同一規律（純関数＝実 ps 出力で検証可能）。
func parsePSLine(line string, includeVSCode bool) (ClaudeSession, bool) {
	parts := splitWSN(line, 10)
	if len(parts) < 11 {
		return ClaudeSession{}, false
	}
	pidStr, cpuStr, command := parts[1], parts[2], parts[10]
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ClaudeSession{}, false
	}
	if !isClaudeCmdBase(fields[0]) {
		return ClaudeSession{}, false
	}
	if isVSCodeSession(fields) && !includeVSCode {
		return ClaudeSession{}, false
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return ClaudeSession{}, false
	}
	cpu, _ := strconv.ParseFloat(cpuStr, 64)
	return ClaudeSession{
		Pid:        pid,
		SessionID:  extractSessionID(fields),
		StartTime:  parts[8],
		CPUPercent: cpu,
	}, true
}

// unescapeLsof は lsof が非印字/非ASCII バイトを出す `\xNN` 形式を
// 元バイトへ戻す（lsof は C/POSIX ロケールだと UTF-8 パス例 U+2010
// `‐`=E2 80 90 を文字列 "\xe2\x80\x90" に化かす）。`\xNN` のみ復元し
// 他は素通し（`\\` 等の特殊化は macOS lsof のパスでは実害が無く、
// 取り違えると逆破壊のため最小限）。launchd は LANG 不設定＝C なので
// ロケール強制と併用する二重防御。
func unescapeLsof(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b []byte
	for i := 0; i < len(s); {
		if i+3 < len(s) && s[i] == '\\' && s[i+1] == 'x' &&
			isHex(s[i+2]) && isHex(s[i+3]) {
			b = append(b, hexVal(s[i+2])<<4|hexVal(s[i+3]))
			i += 4
			continue
		}
		b = append(b, s[i])
		i++
	}
	return string(b)
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') ||
		(c >= 'A' && c <= 'F')
}
func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// getCwdLsof / Scan は OS 依存（プロセス列挙・cwd 解決）のため
// build-tag 分割: scanner_unix.go（`ps aux`＋`lsof`・M8d 前と
// バイト同一＝darwin/linux parity）/ scanner_windows.go（CIM
// Win32_Process）。`unescapeLsof`/`isHex`/`hexVal` は純関数ゆえ本
// ファイルに残置（Windows では未使用だが無害）。
