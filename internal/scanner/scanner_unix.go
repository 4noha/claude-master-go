//go:build !windows

package scanner

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// getCwdLsof は `lsof -a -p <pid> -d cwd -Fn` の n 行から cwd を得る
// （Python _get_cwd_lsof と同一。SIP 保護等で取れなければ ""）。
// launchd 起動だと LANG/LC_* 未設定＝C ロケールで lsof が非ASCII を
// `\xNN` に化かす（U+2010 ハイフン等を含むパスが壊れる実バグ）。
// UTF-8 ロケールを明示し（lsof に生バイトを出させる）、保険で
// unescapeLsof も通す二重防御。
func getCwdLsof(pid int) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "lsof", "-a", "-p",
		strconv.Itoa(pid), "-d", "cwd", "-Fn")
	cmd.Env = append(os.Environ(),
		"LC_ALL=en_US.UTF-8", "LANG=en_US.UTF-8", "LC_CTYPE=en_US.UTF-8")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(ln, "n") {
			return unescapeLsof(ln[1:])
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
