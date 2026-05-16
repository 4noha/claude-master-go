// Package scanner は Claude Code CLI プロセスを検出する。Python
// process_scanner.py の移植。Go は psutil を持たないので Python の
// _scan_ps 経路（subprocess `ps aux` ＋ `lsof` で cwd）に一本化。
// 識別キーは PID でなく session_id（--resume 値）＝再起動で不変。
package scanner

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// getCwdLsof は `lsof -a -p <pid> -d cwd -Fn` の n 行から cwd を得る
// （Python _get_cwd_lsof と同一。SIP 保護等で取れなければ ""）。
func getCwdLsof(pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p",
		strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(ln, "n") {
			return ln[1:]
		}
	}
	return ""
}

// Scan は `ps aux` を実行して claude セッションを列挙、各 cwd を lsof で
// 解決して返す（Python scan→_scan_ps 経路）。ps 失敗時は空+err。
func Scan(includeVSCode bool) ([]ClaudeSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "aux").Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(out), "\n")
	var sessions []ClaudeSession
	for _, line := range lines[1:] { // 先頭はヘッダ
		s, ok := parsePSLine(line, includeVSCode)
		if !ok {
			continue
		}
		s.Cwd = getCwdLsof(s.Pid)
		sessions = append(sessions, s)
	}
	return sessions, nil
}
