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
	"runtime"
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
	WebImagePaste     bool // Web からの画像貼付（既定 off・macOS 主対象）
	NavWheelStep      int
	SessionLog        string // "" 無効 / "true" 自動 / パス

	LogFile     string
	StatusFile  string
	PidFile     string
	ConfigFile  string
	RealClaude  string // 本物の claude バイナリ（proxy がラップする対象）
	SessionsDir string // <pid>.sock / <pid>.status.json の置き場
	PendingFile string // 再開スケジュール永続化（monitor 再起動跨ぎ）
	RemoteFile  string // cloud agent が書く他 PC セッション一覧（dashboard 用）
	// M6 クラウド同期（未設定なら cloud 機能はオプトイン無効）
	GCPProject    string // Firestore プロジェクト ID
	PCID          string // この PC の識別子（既定 hostname）
	CloudRelayURL string // Cloud Run relay の wss:// URL
	GoogleClientID string // Web ログインの OAuth Web Client ID
	AllowedEmails  string // ログイン許可 Google メール（カンマ区切り）
}

func home(p string) string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, p)
}

// normalizeHost は PC_ID 用にホスト名を安定化する。macOS は環境
// （launchd / ネットワーク / HostName 未設定）により os.Hostname() が
// "Mac-Studio" だったり "Mac-Studio.local"（mDNS 形）だったり揺れ、
// 同一マシンが別 PC として二重登録される。最初の '.' 以降（.local や
// DNS ドメイン）を落とした短ホスト名へ正規化し、前後空白を除去する
// （冪等: "Mac-Studio"→"Mac-Studio"）。空になれば def。
func normalizeHost(h, def string) string {
	h = strings.TrimSpace(h)
	if i := strings.IndexByte(h, '.'); i >= 0 {
		h = h[:i]
	}
	h = strings.TrimSpace(h)
	if h == "" {
		return def
	}
	return h
}

// hostnameOr は os.Hostname() を normalizeHost で安定化（取得不可なら
// def）。PC_ID 既定値用。明示指定したいときは環境変数 PC_ID で上書き。
func hostnameOr(def string) string {
	if h, err := os.Hostname(); err == nil {
		return normalizeHost(h, def)
	}
	return def
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

	// 同梱 claude バイナリの既定パス。Windows は実体が claude.exe で
	// proxy の os.Stat は拡張子を補完しないため .exe を付ける。unix
	// （darwin/linux）は従来値とバイト同一＝parity 維持。
	realClaudeDef := home(".local/bin/claude")
	if runtime.GOOS == "windows" {
		realClaudeDef = home(".local/bin/claude.exe")
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
		WebImagePaste:     boolean("WEB_IMAGE_PASTE", false),
		NavWheelStep:      integer("NAV_WHEEL_STEP", 3, 1, 1000),
		SessionLog:        strings.TrimSpace(str("SESSION_LOG", "")),

		LogFile:     home(".claude-master.log"),
		StatusFile:  home(".claude-master.status.json"),
		PidFile:     home(".claude-master.pid"),
		ConfigFile:  cf,
		RealClaude:  str("REAL_CLAUDE", realClaudeDef),
		SessionsDir: home(".claude-master/sessions"),
		PendingFile: home(".claude-master/pending_resumes.json"),
		RemoteFile:  home(".claude-master/sessions/remote_sessions.json"),

		GCPProject:     str("GCP_PROJECT", ""),
		PCID:           str("PC_ID", hostnameOr("pc")),
		CloudRelayURL:  str("CLOUD_RELAY_URL", ""),
		GoogleClientID: str("GOOGLE_OAUTH_CLIENT_ID", ""),
		AllowedEmails:  str("ALLOWED_EMAILS", ""),
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
