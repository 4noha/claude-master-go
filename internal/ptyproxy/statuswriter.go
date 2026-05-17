package ptyproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/4noha/claude-master-go/internal/screen"
)

// 使用量ステータス書出（Python pty_proxy._maybe_write_status の移植）。
// monitor の使用量上限 自動中断/再開（M5e）が読む <pid>.status.json を
// 5 秒スロットル＋変化検知で書く。VT モデルの read-only ヒューリスティック
// 走査のみ（描画には一切関与しない）。

// EnableStatus は status 書出を有効化（RunProxy が pid 確定後に呼ぶ）。
func (s *Server) EnableStatus(path string, pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusPath = path
	s.statusPid = pid
	s.statusScan = &screen.StatusScanner{}
	s.statusInit = true
}

// maybeWriteStatusLocked は masterPump から毎チャンク（要 s.mu）。
// Python _maybe_write_status と同一: 5 秒未満は skip、usage/is_active が
// 前回と同一なら skip、変化時のみ atomic に書く。
func (s *Server) maybeWriteStatusLocked() {
	if s.statusPath == "" || s.statusScan == nil {
		return
	}
	now := time.Now()
	if !s.statusInit && now.Sub(s.statusLast) < 5*time.Second {
		return
	}
	s.statusLast = now
	pct, hasPct, rt, rtz, found := s.statusScan.ExtractUsage(s.p.VT)
	active := screen.IsActive(s.p.VT)
	sig := fmt.Sprintf("%v|%d|%q|%q|%v", hasPct, pct, rt, rtz, active)
	if !s.statusInit && sig == s.statusSig {
		return
	}
	s.statusInit = false
	s.statusSig = sig

	payload := map[string]any{
		"pid":        s.statusPid,
		"updated_at": now.Format("2006-01-02 15:04:05"),
		"is_active":  active,
	}
	// Windows は scanner が他プロセスの cwd を解決できず（PEB 読取要・
	// M8d best-effort）short_dir が "unknown" になる。proxy は自分の
	// cwd＝ユーザーが claude を起動した場所を確実に知るので、ここで
	// status へ載せ monitor.WriteStatus の merge で "unknown" を上書き
	// させる（Web devices.js は short_dir をセッション名に表示）。unix
	// は scanner(lsof) が解決済＝載せない＝STATUS_FILE バイト不変
	// （darwin/linux parity 厳守）。
	if runtime.GOOS == "windows" {
		if wd, e := os.Getwd(); e == nil && wd != "" {
			payload["cwd"] = wd
			payload["short_dir"] = filepath.Base(wd)
		}
	}
	if found {
		if hasPct {
			payload["usage_percent"] = pct
		}
		if rt != "" {
			payload["reset_time"] = rt
			payload["reset_tz"] = rtz
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.statusPath), 0o755); err != nil {
		return
	}
	// atomic 書出（monitor が部分内容を読まないよう tmp→rename）
	tmp := s.statusPath + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, s.statusPath)
	}
}
