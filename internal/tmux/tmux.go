// Package tmux は tmux セッション・ウィンドウ管理を行う。Python
// tmux_manager.py の移植。tmux コマンドを薄くラップした CRUD のみ。
package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/4noha/claude-master-go/internal/scanner"
)

// out は tmux を実行し stdout を trim して返す（Python _tmux）。
func out(args ...string) string {
	s, _ := outErr(args...)
	return s
}

// outErr は tmux 実行結果とエラーを返す（呼び出し側がエラーと
// 「正常だが空」を区別する必要があるとき。runaway 防止の要）。
func outErr(args ...string) (string, error) {
	b, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ok は tmux を実行し終了コード 0 か（Python _tmux_ok）。
func ok(args ...string) bool {
	return exec.Command("tmux", args...).Run() == nil
}

// CheckTmux は tmux の存在確認（Python _check_tmux。ライブラリなので
// sys.exit せず error を返し呼び出し側に委ねる）。
func CheckTmux() error {
	_, err := exec.LookPath("tmux")
	return err
}

// shquote は POSIX シェル単引用（Python shlex.quote 相当）。
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

// Manager は tmux セッション/ウィンドウを管理（Python TmuxManager）。
// 識別キー（session_id 等）→ window 名のマップを保持。monitor から
// 単一ループで使うが将来の並行化に備え mutex で保護。
type Manager struct {
	Session     string
	mu          sync.Mutex
	keyToWindow map[string]string
}

// NewManager は tmux 存在確認の上で Manager を作る。
func NewManager(session string) (*Manager, error) {
	if err := CheckTmux(); err != nil {
		return nil, err
	}
	return &Manager{Session: session, keyToWindow: map[string]string{}}, nil
}

// EnsureSession はセッションが無ければ作る（二重作成防止に has-session 先行）。
func (m *Manager) EnsureSession() {
	if !ok("has-session", "-t", m.Session) {
		out("new-session", "-d", "-s", m.Session, "-n", "dashboard")
	}
}

// SetupDashboard は dashboard ウィンドウで指定コマンドを起動する
// （Python setup_dashboard。コマンド組み立ては呼び出し側＝M5d）。
func (m *Manager) SetupDashboard(command string) {
	target := m.Session + ":dashboard"
	if !ok("select-window", "-t", target) {
		out("new-window", "-t", m.Session, "-n", "dashboard")
	}
	out("send-keys", "-t", target, command, "Enter")
}

// ListWindows はセッションの window 名一覧。
func (m *Manager) ListWindows() []string {
	o := out("list-windows", "-t", m.Session, "-F", "#{window_name}")
	if o == "" {
		return nil
	}
	return strings.Split(o, "\n")
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// AddWindow はセッション用 window を作る（既存があれば再利用）。
// socketPath があれば socket_client を、無ければ cwd で対話シェルを起動。
// EnsureCmdWindow は key に対応する window で任意コマンドを実行する
// （無ければ base から一意名で作成、あれば再利用）。リモート PC の
// claude セッションを cloud attach で this PC の tmux へ同期するのに使う。
func (m *Manager) EnsureCmdWindow(key, base, command string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ex := m.keyToWindow[key]; ex != "" && contains(m.ListWindows(), ex) {
		return ex
	}
	name := m.uniqueName(base)
	out("new-window", "-t", m.Session, "-n", name, command)
	m.keyToWindow[key] = name
	return name
}

// remoteOpt は窓に付ける一意識別ユーザーオプション。pane_start_command は
// launchd 環境やプロセス遷移で空になり得る脆弱キーで runaway の真因
// だった。自分で必ず set する @cm_remote なら環境非依存・確実。
const remoteOpt = "@cm_remote"

// MarkedWindows は @cm_remote が設定された窓を **window_id → marker** で
// 返す（リモート同期の確実な stateless 在席判定）。window_id 一意キーで
// 重複窓も別個に列挙＝runaway 重複の自己修復が可能。list 失敗は error を
// 返し「窓ゼロ」と誤認させない（runaway 防止の核心）。
func (m *Manager) MarkedWindows() (map[string]string, error) {
	// 区切りは TAB 不可（tmux -F は非 tty 出力で literal TAB を落とすため
	// runaway の真因だった）。id=@\d+ / marker="cloud attach …" の双方に
	// 出現しない印字可能文字 '=' を区切りに使う。
	o, err := outErr("list-windows", "-t", m.Session, "-F",
		"#{window_id}=#{"+remoteOpt+"}")
	if err != nil {
		return nil, err
	}
	res := map[string]string{}
	if o == "" {
		return res, nil
	}
	for _, ln := range strings.Split(o, "\n") {
		i := strings.IndexByte(ln, '=')
		if i < 0 {
			continue
		}
		id, mark := ln[:i], ln[i+1:]
		if id != "" && mark != "" {
			res[id] = mark
		}
	}
	return res, nil
}

// LegacyAttachWindows は @cm_remote 未設定だが pane_start_command に
// "claude-master cloud attach" を含む旧 runaway 窓の id を返す
// （新方式移行後の一掃用。list 失敗は error）。
func (m *Manager) LegacyAttachWindows() ([]string, error) {
	// @cm_remote 無し かつ 窓名が "↗"（リモート窓の命名）= set 失敗等の
	// 取り残し。pane_start_command は '=' を含むので使わず name で判定。
	o, err := outErr("list-windows", "-t", m.Session, "-F",
		"#{window_id}=#{"+remoteOpt+"}=#{window_name}")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, ln := range strings.Split(o, "\n") {
		p := strings.SplitN(ln, "=", 3)
		if len(p) < 3 {
			continue
		}
		if p[1] == "" && strings.HasPrefix(p[2], "↗") {
			ids = append(ids, p[0])
		}
	}
	return ids, nil
}

// NewMarkedWindow は窓を作り @cm_remote=marker を付けて window_id を
// 返す（auto-rename off。marker が確実な在席キーになる）。
func (m *Manager) NewMarkedWindow(name, command, marker string) string {
	id, nerr := outErr("new-window", "-t", m.Session, "-n", name, "-P",
		"-F", "#{window_id}", command)
	if os.Getenv("CM_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[tmux] new-window id=%q err=%v\n", id, nerr)
	}
	if id != "" {
		t := m.Session + ":" + id
		_, e1 := outErr("set-option", "-w", "-t", t, remoteOpt, marker)
		_, e2 := outErr("set-option", "-w", "-t", t, "automatic-rename", "off")
		if os.Getenv("CM_DEBUG") != "" && (e1 != nil || e2 != nil) {
			fmt.Fprintf(os.Stderr, "[tmux] set-option @cm_remote err=%v automatic err=%v\n", e1, e2)
		}
	}
	return id
}

// NewWindow は @cm_remote 無しの素の窓（テスト/汎用）。
func (m *Manager) NewWindow(name, command string) string {
	return m.NewMarkedWindow(name, command, "")
}

// KillWindowID は window_id 指定で窓を閉じる。
func (m *Manager) KillWindowID(id string) {
	if id != "" {
		out("kill-window", "-t", m.Session+":"+id)
	}
}

// StyleWindowID は window_id 指定でステータス色を設定する。
func (m *Manager) StyleWindowID(id, style string) {
	if id == "" {
		return
	}
	t := m.Session + ":" + id
	out("set-option", "-w", "-t", t, "window-status-style", style)
	// アクティブ窓は反転でなく太字で強調（bg 指定時に反転だと
	// 配色が崩れるため）。
	out("set-option", "-w", "-t", t, "window-status-current-style",
		style+",bold")
}

// SetWindowStyle は key に対応する window のステータス色を設定する
// （リモート PC 窓を視覚的に区別。tmux の window-status-style /
// -current-style を per-window で上書き）。例 style="fg=colour213"。
func (m *Manager) SetWindowStyle(key, style string) {
	m.mu.Lock()
	name := m.keyToWindow[key]
	m.mu.Unlock()
	if name == "" {
		return
	}
	tgt := m.Session + ":" + name
	out("set-option", "-w", "-t", tgt, "window-status-style", style)
	out("set-option", "-w", "-t", tgt, "window-status-current-style",
		style+",reverse")
}

func (m *Manager) AddWindow(s scanner.ClaudeSession, socketPath string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ex := m.keyToWindow[s.Key()]; ex != "" && contains(m.ListWindows(), ex) {
		return ex // monitor 再起動後の復元: 既存 window 再利用
	}
	name := m.uniqueName(s.ShortDir())
	var cmd string
	if socketPath != "" {
		cmd = m.socketCmd(socketPath)
	} else {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		if s.Cwd != "" {
			cmd = "cd " + shquote(s.Cwd) + " && exec " + shell
		} else {
			cmd = "exec " + shell
		}
	}
	out("new-window", "-t", m.Session, "-n", name, cmd)
	m.keyToWindow[s.Key()] = name
	return name
}

// IsSocketClientRunning は window で socket client（=claude-master 自身）
// が動作中か。Python は python/bash を見たが Go の reattach は自バイナリ
// を直接起動するので自バイナリ basename を判定（実 launcher 準拠）。
func (m *Manager) IsSocketClientRunning(windowName string) bool {
	o := strings.ToLower(out("list-panes", "-t", m.Session+":"+windowName,
		"-F", "#{pane_current_command}"))
	self := "claude-master"
	if ex, err := os.Executable(); err == nil {
		self = strings.ToLower(baseName(ex))
	}
	return strings.Contains(o, self) || strings.Contains(o, "python") ||
		strings.Contains(o, "bash")
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// ReattachWindow は既存 window で socket client を再起動する。
func (m *Manager) ReattachWindow(windowName, socketPath string) {
	out("send-keys", "-t", m.Session+":"+windowName,
		m.socketCmd(socketPath), "Enter")
}

// socketCmd は `<self> socket-client --retry <sock>`（起動レース対策の
// retry 付き。Python は socket_client.py だったが Go は単一バイナリ）。
func (m *Manager) socketCmd(socketPath string) string {
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "claude-master"
	}
	return shquote(self) + " socket-client --retry " + shquote(socketPath)
}

// RenameWindow はキーに対応する window をリネーム（最大 30 文字）。
func (m *Manager) RenameWindow(key, newName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.keyToWindow[key]
	if old == "" || !contains(m.ListWindows(), old) {
		return
	}
	if len(newName) > 30 {
		newName = newName[:30]
	}
	out("rename-window", "-t", m.Session+":"+old, newName)
	m.keyToWindow[key] = newName
}

// RemoveWindow はキーに対応する window を kill。
func (m *Manager) RemoveWindow(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := m.keyToWindow[key]
	if name == "" {
		return
	}
	delete(m.keyToWindow, key)
	if contains(m.ListWindows(), name) {
		out("kill-window", "-t", m.Session+":"+name)
	}
}

// uniqueName は base（最大 20 文字）が衝突したら -2..-49 を付与
// （Python _unique_name と同一）。要 m.mu。
func (m *Manager) uniqueName(base string) string {
	if len(base) > 20 {
		base = base[:20]
	}
	existing := m.ListWindows()
	if !contains(existing, base) {
		return base
	}
	pre := base
	if len(pre) > 17 {
		pre = pre[:17]
	}
	for i := 2; i < 50; i++ {
		c := pre + "-" + itoa(i)
		if !contains(existing, c) {
			return c
		}
	}
	return base
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [4]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

// AdoptWindow はキー→既存 window 名の対応を登録する（monitor 再起動
// 時に STATUS_FILE から復元＝重複ウィンドウ防止。Python が
// _key_to_window へ直接代入していた箇所の公開版）。
func (m *Manager) AdoptWindow(key, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keyToWindow[key] = name
}

// WindowFor はキーに対応する window 名（無ければ ""）。
func (m *Manager) WindowFor(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.keyToWindow[key]
}
