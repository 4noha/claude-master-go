package main

// claude-master start [proxy args...]
//
// VSCode 端末タブ等の foreground 端末から起動する「人手いらず」標準
// コマンド。proxy は最初から **detached** で別プロセスとして spawn し、
// 本コマンドは attach client として foreground で走る。restart-proxy で
// proxy が別 PID に再生成されても、attach client は STATUS_FILE 再解決
// で新 sock へ自動再接続＝**同タブで会話継続**（ユーザの手動 attach
// 操作不要）。proxy 自身は detached spawn 維持なので self-update 反映
// も壊れない＝C 案完全自動化。
//
// 動作:
//   1. cwd 一致の live session が STATUS_FILE にあれば → attach 直行
//      （既存セッションへ復帰＝再起動後の再 open でも会話継続）
//   2. 無ければ → spawnDetachedProxy で新規 proxy 起動 → STATUS_FILE 登録
//      （cwd 一致 key）を最大 30s 待って → attach
//
// 既存ユーザの `claude` shim を `claude-master proxy %*` から
// `claude-master start %*` へ切り替えれば人手復帰不要に。

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/4noha/claude-master-go/internal/client"
	"github.com/4noha/claude-master-go/internal/config"
)

// runStart: claude-master start [args...]
func runStart(args []string) {
	cfg := config.Load()
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start: cwd 取得失敗:", err)
		os.Exit(1)
	}

	// 1. 既存 live session（cwd 一致）があれば attach 直行
	if key := findLiveKeyByCwd(cfg.StatusFile, cwd); key != "" {
		fmt.Fprintf(os.Stderr, "claude-master: 既存セッション %s へ接続\n", key)
		attachAndExit(key, cfg)
	}

	// 2. 新規セッション: proxy を detached spawn
	if err := spawnDetachedProxy(args, cwd); err != nil {
		fmt.Fprintln(os.Stderr, "start: proxy spawn 失敗:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "claude-master: 新規 proxy 起動中…")

	// 3. STATUS_FILE 登録待ち（最大 30s。proxy→child claude→statuswriter→
	//    monitor scan→STATUS_FILE 更新の連鎖は通常数秒で完了）。
	key := waitKeyForCwd(cfg.StatusFile, cwd, 30*time.Second)
	if key == "" {
		fmt.Fprintln(os.Stderr,
			"start: 30s 待っても session が STATUS_FILE に出ない（spawn 失敗 or "+
				"monitor 未稼働）。手動で `claude-master attach <key>` してください。")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "claude-master: セッション %s に接続\n", key)
	attachAndExit(key, cfg)
}

func attachAndExit(key string, cfg *config.Config) {
	if err := client.RunByKey(key, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "attach:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// findLiveKeyByCwd は STATUS_FILE の sessions[] から cwd 一致の key を
// 返す。複数あれば updated_at が新しい方を選ぶ（同 cwd の重複は restart-
// proxy collision で稀に起こるが、最新を採用するのが復帰の意図）。
// 不在/壊れ STATUS_FILE は空文字（=新規 spawn 経路へ進む）。
func findLiveKeyByCwd(statusFile, cwd string) string {
	b, err := os.ReadFile(statusFile)
	if err != nil {
		return ""
	}
	var p struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if json.Unmarshal(b, &p) != nil {
		return ""
	}
	var bestKey, bestTs string
	for _, s := range p.Sessions {
		if sCwd, _ := s["cwd"].(string); sCwd != cwd {
			continue
		}
		key, _ := s["key"].(string)
		if key == "" {
			continue
		}
		ts, _ := s["updated_at"].(string)
		if ts > bestTs {
			bestKey = key
			bestTs = ts
		}
	}
	return bestKey
}

// waitKeyForCwd は cwd 一致 key が STATUS_FILE に現れるまで poll
// （500ms 周期、最大 timeout）。timeout で空文字。
func waitKeyForCwd(statusFile, cwd string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if k := findLiveKeyByCwd(statusFile, cwd); k != "" {
			return k
		}
		time.Sleep(500 * time.Millisecond)
	}
	return ""
}
