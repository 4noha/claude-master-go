package diag

// 「閉じ忘れ proxy 累積」の自動 GC。C 案（claude-master start で detached
// proxy 化）の副作用＝VSCode タブを閉じても proxy は生存し続ける挙動の
// 累積を、host_out_last 経過時間で判定して graceful kill する。
// 動作: <diagDir>/<pid>.snap の host_out_last が threshold より古く、
// かつ proxy が live なら SIGTERM 送信。proxy 内部の signal handler
// (v0.2.1+ の diag.NotifyFatal) が WriteDump→proxyCancel→defer cleanup
// を経て graceful exit。会話 jsonl は残るので再 attach 可能。
//
// 安全策:
//   - host_out_last が "never" or 空＝起動直後 or 完全 idle → skip
//     （正当に起動中 proxy を誤殺しない・保守的）
//   - alive 判定が false （つまり PID 不在）→ skip（Sweep が掃除する）
//   - SIGTERM 失敗（権限等）→ skip・log

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// IdleGCResult は GC で kill した 1 件の記録（log/監査用）。
type IdleGCResult struct {
	PID         int
	Cwd         string
	HostOutLast time.Time
}

// IdleGCSweep は diagDir/<pid>.snap を走査し host_out_last が threshold
// より古い live proxy を SIGTERM kill。threshold <= 0 は no-op。
// 戻り値は kill した proxy の (PID, cwd, last_active) リスト。
func IdleGCSweep(diagDir string, threshold time.Duration) []IdleGCResult {
	if threshold <= 0 || diagDir == "" {
		return nil
	}
	ents, err := os.ReadDir(diagDir)
	if err != nil {
		return nil
	}
	var killed []IdleGCResult
	cutoff := time.Now().Add(-threshold)
	for _, ent := range ents {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".snap") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(name, ".snap"))
		if err != nil || pid <= 0 {
			continue
		}
		if !isAlive(pid) {
			continue // dead は通常 Sweep が cleanup
		}
		// snap 読みは fail-soft（壊れ/部分書き等は skip）
		b, err := os.ReadFile(filepath.Join(diagDir, name))
		if err != nil {
			continue
		}
		var s struct {
			HostOutLast string `json:"host_out_last"`
			Cwd         string `json:"cwd"`
		}
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		// 完全 idle / 起動直後 / "never" は誤殺回避で skip
		if s.HostOutLast == "" || s.HostOutLast == "never" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, s.HostOutLast)
		if err != nil {
			continue
		}
		if ts.After(cutoff) {
			continue // まだ active
		}
		// GC 発動: SIGTERM で proxy の diag.NotifyFatal が捕捉→graceful exit
		if err := sendSIGTERM(pid); err != nil {
			continue
		}
		killed = append(killed, IdleGCResult{
			PID:         pid,
			Cwd:         s.Cwd,
			HostOutLast: ts,
		})
	}
	return killed
}
