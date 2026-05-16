// Package config は claude-master の設定を解決する。
//
// 優先度: 環境変数 > 設定ファイル(~/.claude-master.toml) > 既定値。
// Python 版 config.py と同一のキー/既定値/挙動を保つ（移植 M1）。
// 環境変数キーは大文字 (SIZE_POLICY)、ファイルキーは小文字 (size_policy)。
package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// Config は解決済みの設定値。Python 版の定数群に対応。
type Config struct {
	PollInterval      int
	TmuxSession       string
	IncludeVSCode     bool
	AutoAttach        bool
	LimitWarnPct      int
	LimitInterruptPct int

	SizePolicy        string
	HostFlowScrollbck bool
	NavKey            []byte // 既定 Ctrl-\ (0x1c)
	NavScrollStep     int
	NavPageStep       int
	PageKeyScroll     bool
	WheelScroll       bool
	NavWheelStep      int
	SessionLog        string // "" 無効 / "true" 自動 / パス

	LogFile     string
	StatusFile  string
	PidFile     string
	ConfigFile  string
	RealClaude  string // 本物の claude バイナリ（proxy がラップする対象）
	SessionsDir string // <pid>.sock / <pid>.status.json の置き場
}

func home(p string) string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, p)
}

// Load は env > file > default で Config を構築する。ファイル不在/不正は
// 黙って既定にフォールバック（Python 版と同じ）。
func Load() *Config {
	cf := os.Getenv("CLAUDE_MASTER_CONFIG")
	if cf == "" {
		cf = home(".claude-master.toml")
	}
	file := loadFile(cf)

	get := func(key string) (string, bool) {
		if v, ok := os.LookupEnv(strings.ToUpper(key)); ok {
			return v, true
		}
		if v, ok := file[strings.ToLower(key)]; ok {
			return tomlToString(v), true
		}
		return "", false
	}
	str := func(key, def string) string {
		if v, ok := get(key); ok {
			return v
		}
		return def
	}
	boolean := func(key string, def bool) bool {
		if v, ok := get(key); ok {
			return strings.EqualFold(strings.TrimSpace(v), "true")
		}
		return def
	}
	integer := func(key string, def, lo, hi int) int {
		if v, ok := get(key); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				if n < lo {
					n = lo
				}
				if n > hi {
					n = hi
				}
				return n
			}
		}
		return def
	}

	return &Config{
		PollInterval:      integer("POLL_INTERVAL", 1, 0, 86400),
		TmuxSession:       str("TMUX_SESSION", "claude-master"),
		IncludeVSCode:     boolean("INCLUDE_VSCODE", false),
		AutoAttach:        boolean("AUTO_ATTACH", false),
		LimitWarnPct:      integer("LIMIT_WARN_PERCENT", 80, 0, 100),
		LimitInterruptPct: integer("LIMIT_INTERRUPT_PERCENT", 90, 0, 100),

		SizePolicy:        strings.ToLower(str("SIZE_POLICY", "client")),
		HostFlowScrollbck: boolean("HOST_FLOW_SCROLLBACK", false),
		NavKey:            ParseNavKey(str("NAV_KEY", `\x1c`)),
		NavScrollStep:     integer("NAV_SCROLL_STEP", 1, 1, 1000),
		NavPageStep:       integer("NAV_PAGE_STEP", 10, 1, 100000),
		PageKeyScroll:     boolean("PAGEKEY_SCROLL", false),
		WheelScroll:       boolean("WHEEL_SCROLL", false),
		NavWheelStep:      integer("NAV_WHEEL_STEP", 3, 1, 1000),
		SessionLog:        strings.TrimSpace(str("SESSION_LOG", "")),

		LogFile:     home(".claude-master.log"),
		StatusFile:  home(".claude-master.status.json"),
		PidFile:     home(".claude-master.pid"),
		ConfigFile:  cf,
		RealClaude:  str("REAL_CLAUDE", home(".local/bin/claude")),
		SessionsDir: home(".claude-master/sessions"),
	}
}

func loadFile(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var raw map[string]any
	if err := toml.Unmarshal(b, &raw); err != nil {
		return map[string]any{}
	}
	if sub, ok := raw["claude-master"].(map[string]any); ok {
		raw = sub
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[strings.ToLower(k)] = v
	}
	return out
}

func tomlToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

var navCtrlRe = regexp.MustCompile(`(?i)^(?:ctrl-|c-|\^)(.)$`)

// ParseNavKey は nav-mode トグルキー指定を 1 バイトへ。Python 版
// _parse_nav_key と同一仕様: "ctrl-]"/"^]"/`\x1d`/"0x1d"/"29"/単一文字。
// 不正・空は既定 \x1c。
func ParseNavKey(spec string) []byte {
	s := strings.TrimSpace(spec)
	if s == "" {
		return []byte{0x1c}
	}
	low := strings.ToLower(s)
	if m := navCtrlRe.FindStringSubmatch(low); m != nil {
		c := strings.ToUpper(m[1])[0]
		return []byte{c & 0x1f}
	}
	if strings.HasPrefix(low, `\x`) {
		if n, err := strconv.ParseInt(low[2:], 16, 32); err == nil {
			return []byte{byte(n & 0xff)}
		}
	}
	if strings.HasPrefix(low, "0x") {
		if n, err := strconv.ParseInt(low[2:], 16, 32); err == nil {
			return []byte{byte(n & 0xff)}
		}
	}
	if n, err := strconv.Atoi(low); err == nil {
		return []byte{byte(n & 0xff)}
	}
	if len(s) == 1 {
		return []byte{s[0]}
	}
	return []byte{0x1c}
}
