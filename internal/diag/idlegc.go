package diag

// 「閉じ忘れ proxy 累積」の自動 GC。C 案（claude-master start で detached
// proxy 化）の副作用＝VSCode タブを閉じても proxy は生存し続ける挙動の
// 累積を、**接続中 client 数 == 0 が threshold 継続**を指標として
// graceful kill する。v0.2.4 で host_out_last 指標から切替（v0.2.3 では
// observer 起因の broadcast で host_out_last が常に新しく機能不全だった）。
// 動作: <diagDir>/<pid>.snap の connected_clients == 0 かつ
// last_disconnect が threshold より古い live proxy に SIGTERM 送信。
// proxy 内部の NotifyFatal が WriteDump→proxyCancel→defer cleanup を
// 経て graceful exit。会話 jsonl は残るので再 attach 可能。
//
// 安全策:
//   - connected_clients > 0 → skip（観測者居る＝閉じ忘れではない）
//   - last_disconnect が "never" or 空＝起動以来 disconnect 経験無し
//     （= 起動直後 or 現在接続中）→ skip
//   - alive 判定が false （PID 不在）→ skip（Sweep が掃除する）
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
	PID            int
	Cwd            string
	LastDisconnect time.Time
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
			LastDisconnect   string `json:"last_disconnect"`
			ConnectedClients int32  `json:"connected_clients"`
			Cwd              string `json:"cwd"`
		}
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		// 観測者が居る = 閉じ忘れではない（user が見ている）
		if s.ConnectedClients > 0 {
			continue
		}
		// last_disconnect 未設定（起動直後で誰も繋いだことがない or 現
		// 在接続中）→ 誤殺回避で skip
		if s.LastDisconnect == "" || s.LastDisconnect == "never" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, s.LastDisconnect)
		if err != nil {
			continue
		}
		if ts.After(cutoff) {
			continue // まだ threshold 内
		}
		// GC 発動: SIGTERM で proxy の diag.NotifyFatal が捕捉→graceful exit
		if err := sendSIGTERM(pid); err != nil {
			continue
		}
		killed = append(killed, IdleGCResult{
			PID:            pid,
			Cwd:            s.Cwd,
			LastDisconnect: ts,
		})
	}
	return killed
}
