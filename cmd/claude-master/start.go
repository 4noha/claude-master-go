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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/4noha/claude-master-go/internal/client"
	"github.com/4noha/claude-master-go/internal/cloud/agent"
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

	// 1. 既存 live session（cwd 一致）があれば attach。ただし proxy が
	//    **旧 cwd で起動済**（user が dir を移動した・claude Code harness
	//    の cwd 固定が旧 path に張り付いている）パターンを検出したら
	//    自動 restart-proxy で新 cwd 化してから attach。claude Code を
	//    ラップして「VSCode タブから見て常に正しい cwd で動く」を保証
	//    するのが claude-master の責務。
	if key, pid := findLiveSessionByCwd(cfg.StatusFile, cwd); key != "" {
		snapCwd := readSnapStartCwd(pid)
		if snapCwd != "" && snapCwd != cwd {
			fmt.Fprintf(os.Stderr,
				"claude-master: cwd 乖離検出（proxy 起動時=%s, 現 shell=%s）\n"+
					"  → 自動 restart-proxy で新 cwd へ claude を再起動します\n",
				snapCwd, cwd)
			if err := restartProxyByKey(cfg, key); err != nil {
				fmt.Fprintln(os.Stderr,
					"claude-master: 自動再起動 失敗:", err,
					"\n  → 旧 cwd の既存セッションへそのまま attach します")
			} else {
				// 新 sock の出現待ち（restart-proxy → detached spawn →
				// statuswriter → monitor scan → STATUS_FILE 反映の連鎖は
				// 通常 1〜数秒、最大 30s 待つ）
				newKey := waitKeyForCwd(cfg.StatusFile, cwd, 30*time.Second)
				if newKey != "" {
					fmt.Fprintf(os.Stderr,
						"claude-master: 新 cwd で復帰完了（key=%s）\n", newKey)
					attachAndExit(newKey, cfg)
				}
				fmt.Fprintln(os.Stderr,
					"claude-master: 新 cwd への登録待ち timeout＝旧 key で attach")
			}
		} else {
			fmt.Fprintf(os.Stderr, "claude-master: 既存セッション %s へ接続\n", key)
		}
		attachAndExit(key, cfg)
	}

	// 2. 新規セッション: proxy を detached spawn。
	//    args 空（= `claude` shim 経由）かつ cwd に既存会話 jsonl があれば
	//    --resume <uuid> を自動付与＝VSCode crash → 新タブ `claude` で
	//    **会話継続できる**（これが C 案完全自動化の核心）。args 非空は
	//    user 明示指定を尊重して touch しない。
	home, _ := os.UserHomeDir()
	projectsRoot := filepath.Join(home, ".claude", "projects")
	spawnArgs, resumedUUID := resolveResumeArgs(args, cwd, projectsRoot)
	if resumedUUID != "" {
		fmt.Fprintf(os.Stderr,
			"claude-master: cwd の既存会話 %s を resume\n", resumedUUID)
	}
	if err := spawnDetachedProxy(spawnArgs, cwd); err != nil {
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

// resolveResumeArgs は新規 spawn 時の args を決定する。args 空（= shim
// 経由の `claude` 単独実行）かつ projectsRoot 配下に cwd と一致する
// 会話 jsonl が見つかれば `--resume <uuid>` を付与＝**自動 resume**。
// args 非空（user 明示指定）は touch せず尊重。jsonl 不在は args
// そのまま（=完全新規セッション）。第2 戻り値は resume した UUID
// （表示用・空文字は resume せず）。agent.ResolveClaudeUUID 経由で
// claude 権威 cwd 突合せ＝サニタイズ規則の逆算をしない（不変条件）。
func resolveResumeArgs(args []string, cwd, projectsRoot string) ([]string, string) {
	if len(args) != 0 {
		return args, ""
	}
	if uuid, ok := agent.ResolveClaudeUUID(projectsRoot, cwd); ok {
		return []string{"--resume", uuid}, uuid
	}
	return args, ""
}

// findLiveKeyByCwd は STATUS_FILE の sessions[] から cwd 一致の key を
// 返す。複数あれば updated_at が新しい方を選ぶ（同 cwd の重複は restart-
// proxy collision で稀に起こるが、最新を採用するのが復帰の意図）。
// 不在/壊れ STATUS_FILE は空文字（=新規 spawn 経路へ進む）。
// 後方互換シム：findLiveSessionByCwd が key+pid を返すのに対し key のみ。
func findLiveKeyByCwd(statusFile, cwd string) string {
	k, _ := findLiveSessionByCwd(statusFile, cwd)
	return k
}

// findLiveSessionByCwd は cwd 一致の最新 session を (key, pid) で返す。
// pid は cwd 乖離検出（snap.cwd 読み込み）に使う。
func findLiveSessionByCwd(statusFile, cwd string) (string, int) {
	b, err := os.ReadFile(statusFile)
	if err != nil {
		return "", 0
	}
	var p struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if json.Unmarshal(b, &p) != nil {
		return "", 0
	}
	var bestKey, bestTs string
	var bestPid int
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
			if pf, ok := s["pid"].(float64); ok {
				bestPid = int(pf)
			}
		}
	}
	return bestKey, bestPid
}

// readSnapStartCwd は diag/<pid>.snap の cwd field（proxy 起動時に
// SetStartCwd で書かれた文字列）を返す。STATUS_FILE の cwd は scanner
// の lsof 経由＝inode 動的解決で move 後の新 path に追従するが、snap
// の cwd は proxy 起動時の os.Getwd() 文字列＝**移動前の旧 path のまま**
// で残る。両者の乖離 = ユーザが dir 移動した証拠。
func readSnapStartCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude-master", "diag",
		fmt.Sprintf("%d.snap", pid)))
	if err != nil {
		return ""
	}
	var s struct {
		Cwd string `json:"cwd"`
	}
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s.Cwd
}

// restartProxyByKey は ProxyRestarter（cloud agent と同経路）を直接呼ぶ。
// Spawn の cwd 引数は Lookup が返す cwd（=STATUS_FILE 由来＝scanner が
// 動的解決した移動後の新 path）なので、新 proxy は新 cwd で起動＝claude
// Code harness の cwd も新 path になる＝**自動 cwd 整合**。
func restartProxyByKey(cfg *config.Config, key string) error {
	pr := &agent.ProxyRestarter{
		Lookup: func(sid string) (int, string, bool) {
			b, err := os.ReadFile(cfg.StatusFile)
			if err != nil {
				return 0, "", false
			}
			var p struct {
				Sessions []map[string]any `json:"sessions"`
			}
			if json.Unmarshal(b, &p) != nil {
				return 0, "", false
			}
			for _, s := range p.Sessions {
				k, _ := s["key"].(string)
				if k != sid {
					continue
				}
				pidf, _ := s["pid"].(float64)
				scwd, _ := s["cwd"].(string)
				return int(pidf), scwd, true
			}
			return 0, "", false
		},
		ResolveUUID: func(cwd string) (string, bool) {
			home, _ := os.UserHomeDir()
			return agent.ResolveClaudeUUID(
				filepath.Join(home, ".claude", "projects"), cwd)
		},
		Kill:  killProxy,
		Spawn: spawnResumeProxy,
	}
	return pr.Restart(context.Background(), key)
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
