//go:build !windows

package tmux

import (
	"os"
	"strings"
)

// shquote は POSIX シェル単引用（Python shlex.quote 相当）。M8e 前の
// tmux.go と body バイト同一（darwin/linux parity 厳守）。
func shquote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '@' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// interactiveShell は socket 無し window で起動する対話シェルコマンド。
// M8e 前の AddWindow else 分岐と同一（$SHELL or /bin/zsh を cwd で exec）。
func interactiveShell(cwd string) string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	if cwd != "" {
		return "cd " + shquote(cwd) + " && exec " + shell
	}
	return "exec " + shell
}
