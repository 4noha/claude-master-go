package ptyproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/4noha/claude-master-go/internal/screen"
)

// SESSION_LOG（ファイル転写・描画非依存で完全安全）。Python
// pty_proxy._open_session_log / _session_log_capture /
// _finalize_session_log の移植。忠実 VT モデルの確定行をプレーン
// テキストで追記するだけ＝ターミナル/native scrollback/live に一切
// 触れないので flow のような破綻が構造的に起きない。Claude の再
// ストリームは実出力どおり残す（dedup しない＝禁手回避）。

// openSessionLog は cfg.SessionLog が有効ならファイルを追記オープン。
// NewServer から呼ぶ（Python は PtyProxy.__init__ 相当）。
func (s *Server) openSessionLog() {
	spec := strings.TrimSpace(s.cfg.SessionLog)
	low := strings.ToLower(spec)
	if spec == "" || low == "false" || low == "0" || low == "off" || low == "no" {
		return
	}
	var path string
	if low == "true" || low == "1" || low == "on" || low == "yes" {
		path = filepath.Join(s.logsDir, fmt.Sprintf("session-%d.log", s.p.Pid()))
	} else {
		if strings.HasPrefix(spec, "~/") {
			if h, err := os.UserHomeDir(); err == nil {
				spec = filepath.Join(h, spec[2:])
			}
		}
		path = spec
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	fp, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	s.sessFp = fp
	s.sessFlusher = &screen.HistoryFlusher{}
	ts := time.Now().Format("2006-01-02 15:04:05")
	_, _ = fmt.Fprintf(fp, "\n===== claude-master session pid=%d start %s =====\n",
		s.p.Pid(), ts)
}

// sessionLogCaptureLocked は masterPump（_broadcast 相当）から毎チャンク。
// 確定スクロールアウト行を逐次ファイルへ（要 s.mu）。
func (s *Server) sessionLogCaptureLocked() {
	if s.sessFp == nil || s.sessFlusher == nil {
		return
	}
	s.sessFlusher.Capture(s.p.VT)
	if n := s.sessFlusher.Drain(); len(n) > 0 {
		_, _ = s.sessFp.WriteString(strings.Join(n, "\n") + "\n")
	}
}

// finalizeSessionLog は終了時: 残り確定行＋最終可視画面を書いて閉じる。
func (s *Server) finalizeSessionLog() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessFp == nil {
		return
	}
	if s.sessFlusher != nil {
		s.sessFlusher.Capture(s.p.VT)
		if tail := s.sessFlusher.Drain(); len(tail) > 0 {
			_, _ = s.sessFp.WriteString(strings.Join(tail, "\n") + "\n")
		}
		// 一度もスクロールアウトしていない最終可視画面の中身
		vis := s.p.VT.VisibleLines()
		for len(vis) > 0 && strings.TrimRight(vis[len(vis)-1], " ") == "" {
			vis = vis[:len(vis)-1]
		}
		if len(vis) > 0 {
			trimmed := make([]string, len(vis))
			for i, l := range vis {
				trimmed[i] = strings.TrimRight(l, " ")
			}
			_, _ = s.sessFp.WriteString(strings.Join(trimmed, "\n") + "\n")
		}
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	_, _ = fmt.Fprintf(s.sessFp, "===== session end %s =====\n", ts)
	_ = s.sessFp.Close()
	s.sessFp = nil
}
