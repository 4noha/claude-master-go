// Package monitor は Claude セッション監視デーモン。Python monitor.py
// の中核（run_loop の scan 差分 → tmux 同期 + status ファイル書出 +
// 再起動時 window 名復元）と start/stop/status を移植。使用量上限の
// 自動中断/再開（limit_watcher / resume_scheduler）は M5e（未移植）。
package monitor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/scanner"
	"github.com/4noha/claude-master-go/internal/tmux"
)

// sockPathFor は <SessionsDir>/<pid>.sock。
func sockPathFor(cfg *config.Config, pid int) string {
	return filepath.Join(cfg.SessionsDir, strconv.Itoa(pid)+".sock")
}

// readSessionStatus は proxy が書く <pid>.status.json（M5e）を読む。
// 未存在/不正は空（Python _read_session_status と同一）。
func readSessionStatus(cfg *config.Config, pid int) map[string]any {
	b, err := os.ReadFile(filepath.Join(cfg.SessionsDir,
		strconv.Itoa(pid)+".status.json"))
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return map[string]any{}
	}
	return m
}

func sessionDict(s scanner.ClaudeSession) map[string]any {
	return map[string]any{
		"pid": s.Pid, "cwd": s.Cwd, "short_dir": s.ShortDir(),
		"session_id": s.SessionID, "start_time": s.StartTime,
		"cpu_percent": s.CPUPercent, "mem_mb": s.MemMB, "key": s.Key(),
	}
}

// WriteStatus は STATUS_FILE に現セッション一覧を書く（Python
// _write_status と同一構造: updated_at + sessions[]（dict + window_name
// + <pid>.status.json マージ））。
func WriteStatus(cfg *config.Config, mgr *tmux.Manager, sessions []scanner.ClaudeSession) error {
	arr := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		d := sessionDict(s)
		d["window_name"] = mgr.WindowFor(s.Key())
		for k, v := range readSessionStatus(cfg, s.Pid) {
			d[k] = v
		}
		arr = append(arr, d)
	}
	payload := map[string]any{
		"updated_at": time.Now().Format("2006-01-02 15:04:05"),
		"sessions":   arr,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cfg.StatusFile, b, 0o644)
}

// managedOnly は claude-master の proxy 経由で起動したセッション
// （<pid>.sock を持つ）だけに絞る。proxy を介さない素の `claude` は
// claude-master 管理外なので tmux タブも dashboard も作らない
// （ユーザー要望「claude-master で起動していない claude プロセスの
// タブは表示しない」）。socket は proxy が claude 起動直後に作るので
// 立ち上げ直後 1 周だけ漏れることがあるが次周で拾う（許容）。
func managedOnly(cfg *config.Config, ss []scanner.ClaudeSession) []scanner.ClaudeSession {
	out := make([]scanner.ClaudeSession, 0, len(ss))
	for _, s := range ss {
		if _, err := os.Stat(sockPathFor(cfg, s.Pid)); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// SyncOnce は 1 回分の差分同期: 新規キー→AddWindow、消失キー→
// RemoveWindow（Python run_loop ループ本体の tmux 部分と同一）。
// 更新後の current（key→session）を返す。
func SyncOnce(cfg *config.Config, mgr *tmux.Manager,
	known map[string]scanner.ClaudeSession,
	sessions []scanner.ClaudeSession) map[string]scanner.ClaudeSession {

	sessions = managedOnly(cfg, sessions) // 管理外 claude はタブを作らない
	current := map[string]scanner.ClaudeSession{}
	for _, s := range sessions {
		current[s.Key()] = s
	}
	for key, s := range current {
		if _, ok := known[key]; ok {
			continue
		}
		mgr.AddWindow(s, sockPathFor(cfg, s.Pid)) // 管理対象＝socket あり
	}
	for key := range known {
		if _, ok := current[key]; !ok {
			mgr.RemoveWindow(key)
		}
	}
	return current
}

// restoreWindows は STATUS_FILE から前回 window 名を復元（Python cmd_run
// の重複ウィンドウ防止）。
func restoreWindows(cfg *config.Config, mgr *tmux.Manager) {
	b, err := os.ReadFile(cfg.StatusFile)
	if err != nil {
		return
	}
	var prev struct {
		Sessions []struct {
			Key        string `json:"key"`
			WindowName string `json:"window_name"`
		} `json:"sessions"`
	}
	if json.Unmarshal(b, &prev) != nil {
		return
	}
	existing := map[string]bool{}
	for _, w := range mgr.ListWindows() {
		existing[w] = true
	}
	for _, s := range prev.Sessions {
		if s.Key != "" && s.WindowName != "" && existing[s.WindowName] {
			mgr.AdoptWindow(s.Key, s.WindowName)
		}
	}
}

const (
	escByte      = "\x1b"
	interruptFmt = "\n⚠ Usage limit at %d%% (resets: %s). 現在の作業状態を要約して一時停止してください。\n"
	resumeMsg    = "プラン制限がリセットされました。中断前の作業を再開してください。\n"
)

// handleLimitEvent は Python _handle_limit_event の移植。approaching /
// idle は警告リネームのみ、active な interrupt/reached は socket へ
// 中断メッセージ送信＋再開スケジュール＋[PAUSED] リネーム。
func handleLimitEvent(cfg *config.Config, mgr *tmux.Manager,
	sch *ResumeScheduler, ev *LimitEvent, s scanner.ClaudeSession,
	status map[string]any) {

	reset := strings.TrimSpace(statusStr(status, "reset_time") + " " +
		statusStr(status, "reset_tz"))
	// is_active は明示 false の時のみ idle（旧版互換で既定 true）
	isActive := true
	if v, ok := status["is_active"].(bool); ok {
		isActive = v
	}
	if ev.Level == LevelApproaching {
		mgr.RenameWindow(s.Key(), fmt.Sprintf("%s[⚠%d%%]", s.ShortDir(), ev.UsagePercent))
		return
	}
	if !isActive {
		mgr.RenameWindow(s.Key(), fmt.Sprintf("%s[⚠%d%%]", s.ShortDir(), ev.UsagePercent))
		return
	}
	sock := sockPathFor(cfg, s.Pid)
	if _, err := os.Stat(sock); err == nil {
		SendToSocket(sock, []byte(escByte+
			fmt.Sprintf(interruptFmt, ev.UsagePercent, reset)))
		sch.Schedule(ev, sock)
	}
	mgr.RenameWindow(s.Key(), s.ShortDir()+"[PAUSED]")
}

// resumeSessions は Python _resume_sessions の移植。
func resumeSessions(sch *ResumeScheduler, cur map[string]scanner.ClaudeSession,
	mgr *tmux.Manager, w *LimitWatcher) {
	for _, kv := range sch.Due(time.Now()) {
		key, sock := kv[0], kv[1]
		if _, err := os.Stat(sock); err == nil {
			SendToSocket(sock, []byte(resumeMsg))
		}
		w.Clear(key)
		if s, ok := cur[key]; ok {
			mgr.RenameWindow(key, s.ShortDir())
		}
	}
}

// RunLoop は scan→同期→上限処理→再開→status 書出を PollInterval 間隔で
// 回す（Python run_loop 完全移植）。done が閉じたら終了。
func RunLoop(cfg *config.Config, mgr *tmux.Manager, done <-chan struct{}) {
	known := map[string]scanner.ClaudeSession{}
	w := NewLimitWatcher(cfg)
	sch := NewResumeScheduler(cfg.PendingFile)
	interval := time.Duration(cfg.PollInterval) * time.Second
	if interval <= 0 {
		interval = time.Second
	}
	for {
		sessions, _ := scanner.Scan(false)
		sessions = managedOnly(cfg, sessions) // 管理外 claude はタブ非生成
		current := map[string]scanner.ClaudeSession{}
		for _, s := range sessions {
			current[s.Key()] = s
		}
		for key, s := range current {
			if _, ok := known[key]; !ok {
				mgr.AddWindow(s, sockPathFor(cfg, s.Pid))
				continue
			}
			status := readSessionStatus(cfg, s.Pid)
			if ev := w.Check(key, status); ev != nil {
				handleLimitEvent(cfg, mgr, sch, ev, s, status)
			} else if sch.IsPending(key) {
				if a, ok := status["is_active"].(bool); ok && a {
					// スケジュール済みだが active = 手動再開
					sch.Remove(key)
					w.Clear(key)
					mgr.RenameWindow(key, s.ShortDir())
				}
			}
		}
		for key := range known {
			if _, ok := current[key]; !ok {
				mgr.RemoveWindow(key)
				w.Clear(key)
				sch.Remove(key)
			}
		}
		resumeSessions(sch, current, mgr, w)
		known = current
		cur := make([]scanner.ClaudeSession, 0, len(known))
		for _, s := range known {
			cur = append(cur, s)
		}
		_ = WriteStatus(cfg, mgr, cur)
		select {
		case <-done:
			return
		case <-time.After(interval):
		}
	}
}

// CmdRun はフォアグラウンド実行（Python cmd_run）。done でループ終了。
func CmdRun(cfg *config.Config, done <-chan struct{}, stdout io.Writer) error {
	mgr, err := tmux.NewManager(cfg.TmuxSession)
	if err != nil {
		return fmt.Errorf("tmux: %w", err)
	}
	mgr.EnsureSession()
	restoreWindows(cfg, mgr)
	self, _ := os.Executable()
	if self != "" {
		mgr.SetupDashboard(self + " monitor --dashboard")
	}
	fmt.Fprintf(stdout, "監視開始 (tmux session: %s)\n", cfg.TmuxSession)
	fmt.Fprintf(stdout, "ダッシュボード: tmux attach -t %s\n", cfg.TmuxSession)
	RunLoop(cfg, mgr, done)
	fmt.Fprintln(stdout, "停止しました")
	return nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// CmdStart はデーモンを背後起動（Python cmd_start）。多重起動は PID
// ファイル + kill(0) で防ぐ。
func CmdStart(cfg *config.Config, stdout io.Writer) error {
	if b, err := os.ReadFile(cfg.PidFile); err == nil {
		if pid, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil && pidAlive(pid) {
			fmt.Fprintf(stdout, "すでに起動中です (PID=%d)\n", pid)
			return nil
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	lf, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()
	cmd := newDetached(self, []string{"monitor", "--daemon"}, lf)
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = os.WriteFile(cfg.PidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	fmt.Fprintf(stdout, "起動しました (PID=%d)\nログ: %s\n",
		cmd.Process.Pid, cfg.LogFile)
	return nil
}

// CmdStop はデーモン停止（Python cmd_stop）。
func CmdStop(cfg *config.Config, stdout io.Writer) error {
	b, err := os.ReadFile(cfg.PidFile)
	if err != nil {
		fmt.Fprintln(stdout, "起動していません")
		return nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		_ = os.Remove(cfg.PidFile)
		fmt.Fprintln(stdout, "PID ファイルが不正（削除しました）")
		return nil
	}
	if syscall.Kill(pid, syscall.SIGTERM) != nil {
		_ = os.Remove(cfg.PidFile)
		fmt.Fprintln(stdout, "プロセスが見つかりません（PIDファイルを削除しました）")
		return nil
	}
	_ = os.Remove(cfg.PidFile)
	fmt.Fprintf(stdout, "停止しました (PID=%d)\n", pid)
	return nil
}

// CmdStatus は現セッション一覧を表示（Python cmd_status）。
func CmdStatus(cfg *config.Config, stdout io.Writer) error {
	sessions, err := scanner.Scan(false)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Fprintln(stdout, "Claude CLI セッションが見つかりません")
		return nil
	}
	fmt.Fprintf(stdout, "%-8s %-20s %-14s %6s %8s  %s\n",
		"PID", "Dir", "Started", "CPU%", "Mem MB", "接続")
	fmt.Fprintln(stdout, strings.Repeat("-", 75))
	for _, s := range sessions {
		mode := "shell のみ"
		if _, e := os.Stat(sockPathFor(cfg, s.Pid)); e == nil {
			mode = "PTY proxy"
		}
		fmt.Fprintf(stdout, "%-8d %-20s %-14s %6.1f %8.1f  %s\n",
			s.Pid, s.ShortDir(), s.StartTime, s.CPUPercent, s.MemMB, mode)
	}
	return nil
}

const dashMinW = 78

// rcount は Python len(str)（rune 数）。Python の str.format 幅は文字数
// 基準なので忠実移植のため表示幅でなく rune 数で揃える。
func rcount(s string) int { return len([]rune(s)) }

func ljust(s string, n int) string {
	if d := n - rcount(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
func rjust(s string, n int) string {
	if d := n - rcount(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}
func center(s string, n int) string {
	d := n - rcount(s)
	if d <= 0 {
		return s
	}
	l := d / 2
	return strings.Repeat(" ", l) + s + strings.Repeat(" ", d-l)
}
func runeCut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func jstr(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}
func jflt(m map[string]any, k string) float64 {
	switch n := m[k].(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

// RenderDashboard は Python dashboard.render の忠実移植（純関数）。
// data は STATUS_FILE の JSON 復号 map（pid 等は float64）。gitMsg は
// 任意のツール更新文（空可）。
func RenderDashboard(data map[string]any, w int, gitMsg string) string {
	W := w
	if W < dashMinW {
		W = dashMinW
	}
	sessions, _ := data["sessions"].([]any)
	updated := "—"
	if u := jstr(data, "updated_at"); u != "" {
		updated = u
	}
	const resetW, overhead = 10, 56
	dirW := W - overhead
	if dirW < 16 {
		dirW = 16
	}
	hdr := "║ " + ljust("PID", 6) + " " + ljust("Dir", dirW) + " " +
		ljust("Started", 13) + " " + rjust("CPU%", 5) + " " +
		rjust("Mem MB", 7) + " " + rjust("Use%", 5) + " " +
		rjust("Resets", resetW) + " ║"
	lines := []string{
		"╔" + strings.Repeat("═", W-2) + "╗",
		"║" + center(" Claude Code Sessions ", W-2) + "║",
		"╠" + strings.Repeat("═", W-2) + "╣",
		hdr,
		"╠" + strings.Repeat("─", W-2) + "╣",
	}
	if len(sessions) == 0 {
		lines = append(lines, "║"+ljust("  (CLI セッションなし)", W-2)+"║")
	} else {
		for _, sv := range sessions {
			s, _ := sv.(map[string]any)
			usageStr := "—"
			if v, ok := s["usage_percent"]; ok && v != nil {
				usageStr = fmt.Sprintf("%d%%", int(jflt(s, "usage_percent")))
			}
			resetStr := "—"
			if r := jstr(s, "reset_time"); r != "" {
				resetStr = r
			}
			row := "║ " + ljust(fmt.Sprintf("%d", int(jflt(s, "pid"))), 6) + " " +
				ljust(runeCut(jstr(s, "short_dir"), dirW), dirW) + " " +
				ljust(jstr(s, "start_time"), 13) + " " +
				rjust(fmt.Sprintf("%4.1f%%", jflt(s, "cpu_percent")), 5) + " " +
				rjust(fmt.Sprintf("%7.1f", jflt(s, "mem_mb")), 7) + " " +
				rjust(usageStr, 5) + " " + rjust(resetStr, resetW) + " ║"
			lines = append(lines, row)
			sid := ""
			if id := jstr(s, "session_id"); id != "" {
				sid = "[" + runeCut(id, 8) + "]"
			}
			parts := []string{"Limit: " + usageStr, "Resets: " + resetStr}
			if tz := jstr(s, "reset_tz"); tz != "" {
				parts = append(parts, "("+tz+")")
			}
			var sb []string
			if sid != "" {
				sb = append(sb, sid)
			}
			sb = append(sb, parts...)
			sub := strings.TrimSpace(strings.Join(sb, "  "))
			lines = append(lines, "║  "+ljust(sub, W-4)+"║")
		}
	}
	// 外部 PC のセッション（cloud agent の snapshot を Dashboard が
	// data["remote"] に併合）。ローカルとは別セクションで列挙する。
	remote, _ := data["remote"].([]any)
	if len(remote) > 0 {
		lines = append(lines,
			"╠"+strings.Repeat("─", W-2)+"╣",
			"║"+center(" リモート（他 PC）", W-2)+"║",
			"╠"+strings.Repeat("─", W-2)+"╣")
		for _, rv := range remote {
			r, _ := rv.(map[string]any)
			pc := runeCut(jstr(r, "pc"), 16)
			dir := jstr(r, "short_dir")
			sid := runeCut(jstr(r, "session_id"), 8)
			row := ljust(pc, 16) + "  ↗" + dir
			if sid != "" {
				row += "  [" + sid + "]"
			}
			lines = append(lines, "║ "+ljust(runeCut(row, W-3), W-3)+"║")
		}
	}
	footer := fmt.Sprintf("更新: %s  セッション数: %d", updated, len(sessions))
	if len(remote) > 0 {
		footer += fmt.Sprintf("  リモート: %d", len(remote))
	}
	if gitMsg != "" {
		footer += "  ツール: " + gitMsg
	}
	lines = append(lines,
		"╠"+strings.Repeat("─", W-2)+"╣",
		"║ "+ljust(footer, W-3)+"║",
		"╚"+strings.Repeat("═", W-2)+"╝")
	return strings.Join(lines, "\n")
}

// Dashboard は STATUS_FILE を 3 秒ごとに整形表示（Python dashboard.py
// --loop。done で終了）。
func Dashboard(cfg *config.Config, done <-chan struct{}, stdout io.Writer) {
	for {
		var data map[string]any
		if b, err := os.ReadFile(cfg.StatusFile); err == nil {
			_ = json.Unmarshal(b, &data)
		}
		if data == nil {
			data = map[string]any{}
		}
		// cloud agent が書く他 PC セッション snapshot を併合（任意・
		// 無ければリモート節は出ない）。STATUS_FILE と独立ファイルなので
		// monitor と cloud agent は疎結合のまま。
		if b, err := os.ReadFile(cfg.RemoteFile); err == nil {
			var rm map[string]any
			if json.Unmarshal(b, &rm) == nil {
				data["remote"] = rm["sessions"]
			}
		}
		fmt.Fprint(stdout, "\x1b[2J\x1b[H")
		fmt.Fprintln(stdout, RenderDashboard(data, dashMinW, ""))
		select {
		case <-done:
			return
		case <-time.After(3 * time.Second):
		}
	}
}
