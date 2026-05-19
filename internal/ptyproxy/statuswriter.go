package ptyproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	if s.cmVersion != "" {
		// per-proxy バイナリ版。プロセス毎に定数＝Firestore content_hash
		// は初回1回のみ変化（near-$0 維持）。旧 inode で動く proxy は
		// 旧版を書く＝web で 🔴（要更新セッション）検出に使う。
		payload["cm_version"] = s.cmVersion
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
