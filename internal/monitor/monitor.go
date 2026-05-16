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

// SyncOnce は 1 回分の差分同期: 新規キー→AddWindow、消失キー→
// RemoveWindow（Python run_loop ループ本体の tmux 部分と同一）。
// 更新後の current（key→session）を返す。
func SyncOnce(cfg *config.Config, mgr *tmux.Manager,
	known map[string]scanner.ClaudeSession,
	sessions []scanner.ClaudeSession) map[string]scanner.ClaudeSession {

	current := map[string]scanner.ClaudeSession{}
	for _, s := range sessions {
		current[s.Key()] = s
	}
	for key, s := range current {
		if _, ok := known[key]; ok {
			continue
		}
		sock := sockPathFor(cfg, s.Pid)
		if _, err := os.Stat(sock); err != nil {
			sock = "" // socket 無し＝対話シェル window
		}
		mgr.AddWindow(s, sock)
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

// RunLoop は scan→同期→status 書出を PollInterval 間隔で回す。
// done が閉じたら終了（Python run_loop + シグナル）。
func RunLoop(cfg *config.Config, mgr *tmux.Manager, done <-chan struct{}) {
	known := map[string]scanner.ClaudeSession{}
	interval := time.Duration(cfg.PollInterval) * time.Second
	if interval <= 0 {
		interval = time.Second
	}
	for {
		sessions, _ := scanner.Scan(false)
		known = SyncOnce(cfg, mgr, known, sessions)
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

// Dashboard は STATUS_FILE を一定間隔で表組み表示（Python dashboard.py
// の最小等価。done で終了）。
func Dashboard(cfg *config.Config, done <-chan struct{}, stdout io.Writer) {
	for {
		b, err := os.ReadFile(cfg.StatusFile)
		fmt.Fprint(stdout, "\x1b[2J\x1b[H") // クリア
		if err == nil {
			var p struct {
				UpdatedAt string `json:"updated_at"`
				Sessions  []struct {
					Pid        int     `json:"pid"`
					ShortDir   string  `json:"short_dir"`
					StartTime  string  `json:"start_time"`
					CPUPercent float64 `json:"cpu_percent"`
					WindowName string  `json:"window_name"`
				} `json:"sessions"`
			}
			if json.Unmarshal(b, &p) == nil {
				fmt.Fprintf(stdout, "claude-master  更新: %s\n\n", p.UpdatedAt)
				fmt.Fprintf(stdout, "%-8s %-20s %-14s %6s  %s\n",
					"PID", "Dir", "Started", "CPU%", "Window")
				fmt.Fprintln(stdout, strings.Repeat("-", 70))
				for _, s := range p.Sessions {
					fmt.Fprintf(stdout, "%-8d %-20s %-14s %6.1f  %s\n",
						s.Pid, s.ShortDir, s.StartTime, s.CPUPercent, s.WindowName)
				}
			}
		} else {
			fmt.Fprintln(stdout, "（status 待機中…）")
		}
		select {
		case <-done:
			return
		case <-time.After(2 * time.Second):
		}
	}
}
