//go:build windows

package tmux

import "strings"

// shquote (windows): psmux が `new-window` のコマンドを Windows シェルで
// 実行するため Windows 流クォート。スペース等を含む時のみ二重引用で
// 包み内側 `"` を `""` へ（cmd/pwsh 共通に安全な単純パス前提）。`\` `:`
// `/` は通常 Windows パス構成文字なので safe（不要な引用を避ける）。
func shquote(s string) string {
	if s == "" {
		return `""`
	}
	safe := true
	for _, r := range s {
		if !(r == '_' || r == '-' || r == '.' || r == '/' || r == '\\' ||
			r == ':' || r == '@' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9')) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// interactiveShell (windows): socket 無し window で起動する対話シェル。
// cwd 指定時はそこへ移動して cmd を維持（unix の `cd && exec $SHELL`
// 相当）。psmux のデフォルトシェル差異を避け cmd を明示。
func interactiveShell(cwd string) string {
	if cwd != "" {
		return "cmd /k cd /d " + shquote(cwd)
	}
	return "cmd"
}
