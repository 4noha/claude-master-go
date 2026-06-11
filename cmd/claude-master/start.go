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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/4noha/claude-master-go/internal/client"
	"github.com/4noha/claude-master-go/internal/cloud/agent"
	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/scanner"
)

// startScanSessions は scanner.Scan の seam（findLiveManagedByUUID の
// テストで結果/エラーを注入する。scanner 自体は実 ps/lsof・実 CIM の
// 既存テストが担保）。
var startScanSessions = scanner.Scan

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

	// 2. cwd 完全一致なし → 子孫 cwd に live session があれば picker で
	//    user に選ばせる（VSCode terminal が crash で project root に
	//    cwd リセットされる制約への対策）。args 非空（user 明示指定）は
	//    新規 spawn を優先＝subtree チェック skip。
	//
	//    **必ず picker を出す**（1 件でも勝手に attach しない）＝user は
	//    「親 dir で新規セッション開始」の意思を picker の "0" で表明可。
	//    1 件自動 attach は「子の作業中セッションに親から横入りされる」
	//    挙動になり親 dir での新規開始を阻むため廃止（rule of least
	//    surprise）。非対話的 (CI/pipe) は promptSubtreePick が -1 を
	//    返す＝新規 spawn fallback で安全。
	if len(args) == 0 {
		subs := findLiveSessionsInSubtree(cfg.StatusFile, cwd)
		if len(subs) > 0 {
			idx := promptSubtreePick(subs, cwd, os.Stdin, os.Stderr)
			if idx >= 0 {
				fmt.Fprintf(os.Stderr,
					"claude-master: %s に attach（key=%s）\n",
					subs[idx].Cwd, subs[idx].Key)
				attachAndExit(subs[idx].Key, cfg)
			}
			// idx == -1: 新規 spawn 経路へ続行（user の選択 or 非対話的）
		}
	}

	// 3. 新規セッション: proxy を detached spawn。
	//    args 空（= `claude` shim 経由）かつ cwd に既存会話 jsonl があれば
	//    --resume <uuid> を自動付与＝VSCode crash → 新タブ `claude` で
	//    **会話継続できる**（これが C 案完全自動化の核心）。args 非空は
	//    user 明示指定を尊重して touch しない。
	home, _ := os.UserHomeDir()
	projectsRoot := filepath.Join(home, ".claude", "projects")
	spawnArgs, resumedUUID := resolveResumeArgs(args, cwd, projectsRoot)
	if resumedUUID != "" {
		// dup 防止 backstop（STATUS_FILE 非依存）: 同 UUID を resume 中の
		// live proxy が既に居れば spawn せず attach。STATUS が一時空
		// （monitor 不調・scan 失敗）だと cwd 一致を見落として同一会話へ
		// 二重 resume spawn し、失敗のたびに dup proxy が増えて scan 負荷
		// →さらに STATUS 劣化、の正帰還が成立する（2026-06-12 実害）。
		if pid, ok := findLiveManagedByUUID(cfg, resumedUUID); ok {
			fmt.Fprintf(os.Stderr,
				"claude-master: %s は live proxy あり（claude pid=%d）＝新規 spawn せず attach\n",
				resumedUUID, pid)
			attachLiveUUIDAndExit(cfg, pid, resumedUUID)
		}
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
		// STATUS 反映 timeout でも spawn 自体は成功している事が多い
		// （monitor 不調/scan 遅延）。UUID resume なら sock を直接探して
		// attach＝spawn 済み proxy を孤児（dup の種）にしない。
		if resumedUUID != "" {
			if pid, ok := findLiveManagedByUUID(cfg, resumedUUID); ok {
				fmt.Fprintf(os.Stderr,
					"claude-master: STATUS 未反映だが spawn 済み proxy を検出（claude pid=%d）＝"+
						"直接 attach。monitor の状態を確認してください（~/.claude-master.log）\n", pid)
				attachLiveUUIDAndExit(cfg, pid, resumedUUID)
			}
		}
		fmt.Fprintln(os.Stderr,
			"start: 30s 待っても session が STATUS_FILE に出ない（spawn 失敗の可能性）。"+
				"`claude-master sessions` で proxy 生存を確認し、`claude-master attach <key>` "+
				"または ~/.claude-master.log（monitor）を確認してください。")
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

// findLiveManagedByUUID は STATUS_FILE **非依存**で「<uuid> を resume 中の
// 管理下 live セッション（<pid>.sock あり）」を探す。scanner.Scan は
// OS-split 済（unix=ps/lsof・windows=CIM）＝両 OS で動く。scan エラーは
// 不検出扱い（呼び元が従来経路へ安全 fallback）。
func findLiveManagedByUUID(cfg *config.Config, uuid string) (int, bool) {
	if uuid == "" {
		return 0, false
	}
	ss, err := startScanSessions(false)
	if err != nil {
		return 0, false
	}
	for _, s := range ss {
		if s.SessionID != uuid {
			continue
		}
		sock := filepath.Join(cfg.SessionsDir, strconv.Itoa(s.Pid)+".sock")
		if _, err := os.Stat(sock); err == nil {
			return s.Pid, true
		}
	}
	return 0, false
}

// attachLiveUUIDAndExit は backstop で見つけた live セッションへ attach。
// まず sock 直接（STATUS 非依存＝monitor 不調でも繋がる）、sock が死んで
// いたら（restart-proxy 等）key 再解決の RunByKey へ fallback。
func attachLiveUUIDAndExit(cfg *config.Config, pid int, uuid string) {
	sock := filepath.Join(cfg.SessionsDir, strconv.Itoa(pid)+".sock")
	if err := client.Run(sock, true, cfg); err == nil {
		os.Exit(0)
	}
	attachAndExit(uuid, cfg)
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

// SubtreeCandidate は findLiveSessionsInSubtree が返す候補 1 件の info。
// VSCode terminal が crash 後にプロジェクトルートへ cwd リセットされる
// 制約への対策＝user が「子ディレクトリで作業していた session」を root
// から resume できるようにする。
type SubtreeCandidate struct {
	Key       string // session key (UUID or pid-N)
	Cwd       string // session の実 cwd（root を含む絶対 path）
	UpdatedAt string // STATUS_FILE の updated_at（並び順用）
	Pid       int    // proxy or claude pid（display only）
}

// findLiveSessionsInSubtree は STATUS_FILE から **cwd の子孫** に該当
// する live session を updated_at 降順で返す。exact match は除外
// （findLiveSessionByCwd が先に処理する設計）。VSCode crash 後に terminal
// が project root に戻ってしまった時、root から「どの子 cwd の作業を
// 復帰しますか？」を user に選ばせる対話 picker の入力源。
//
// 判定: filepath.Clean で正規化した上で `cwd + "/"` で始まる path のみ
// 採用＝「/foo/bar」と「/foo/barr」を取り違えない。
func findLiveSessionsInSubtree(statusFile, cwd string) []SubtreeCandidate {
	b, err := os.ReadFile(statusFile)
	if err != nil {
		return nil
	}
	var p struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	rootClean := filepath.Clean(cwd)
	prefix := rootClean
	if prefix != "/" {
		prefix = prefix + "/"
	}
	out := make([]SubtreeCandidate, 0, len(p.Sessions))
	for _, s := range p.Sessions {
		sCwd, _ := s["cwd"].(string)
		if sCwd == "" {
			continue
		}
		sCwdClean := filepath.Clean(sCwd)
		if sCwdClean == rootClean {
			continue // exact match は別経路（findLiveSessionByCwd）
		}
		if !strings.HasPrefix(sCwdClean, prefix) {
			continue
		}
		key, _ := s["key"].(string)
		if key == "" {
			continue
		}
		ts, _ := s["updated_at"].(string)
		c := SubtreeCandidate{Key: key, Cwd: sCwdClean, UpdatedAt: ts}
		if pf, ok := s["pid"].(float64); ok {
			c.Pid = int(pf)
		}
		out = append(out, c)
	}
	// updated_at 降順（最近活動した順＝user が直近作業していた session）
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

// pickSubtreeCandidate は picker の純粋ロジック。入力文字列（user の
// stdin 1 行）と candidates 数から「選ばれた index または -1（=新規 spawn）」
// を返す。tty 非対話的・empty/parse 失敗時の fallback はここで吸収。
//
//   - input == ""（Enter 単独）        → index 0（先頭 = updated_at 最新）
//   - input == "0" or "n" or "N"       → -1（新規 spawn）
//   - input == 数値（1..count）         → index 数値-1
//   - その他                           → 安全側で -1（新規 spawn）
func pickSubtreeCandidate(input string, count int) int {
	in := strings.TrimSpace(input)
	if in == "" {
		return 0 // default = updated_at 最新
	}
	if in == "0" || in == "n" || in == "N" {
		return -1
	}
	n, err := strconv.Atoi(in)
	if err != nil || n < 1 || n > count {
		return -1
	}
	return n - 1
}

// promptSubtreePick は candidates を表形式で stderr に表示し、stdin
// から 1 行受け取って index を返す。tty でない時は -1（=新規 spawn fallback）。
// reader を seam にしてテスト容易（実呼出は os.Stdin）。
func promptSubtreePick(cands []SubtreeCandidate, cwd string, in io.Reader, out io.Writer) int {
	fmt.Fprintf(out,
		"claude-master: cwd 配下に live session %d 件（cwd=%s）:\n",
		len(cands), cwd)
	for i, c := range cands {
		rel := c.Cwd
		if strings.HasPrefix(rel, cwd+"/") {
			rel = strings.TrimPrefix(rel, cwd+"/")
		}
		fmt.Fprintf(out, "  %d) %-30s  key=%s  updated=%s\n",
			i+1, rel, c.Key, c.UpdatedAt)
	}
	fmt.Fprintf(out, "  0) [新規セッションを %s で開始]\n", cwd)
	fmt.Fprintf(out, "番号 [Enter で 1]: ")

	// 非対話的（test / CI / pipe）なら新規 spawn fallback
	if f, ok := in.(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
		fmt.Fprintln(out, "  (非対話的＝新規 spawn fallback)")
		return -1
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return -1
	}
	return pickSubtreeCandidate(line, len(cands))
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
