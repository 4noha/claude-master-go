// Command claude-master — Go 版 claude-master の CLI エントリ。
//
//	claude-master config        設定の解決値を表示（M1）
//	claude-master version       バージョン
//	claude-master update        最新リリースへ自己更新（sha256 検証）
//	claude-master proxy [args]  claude を PTY ラップ（M3 で実装）
//	claude-master socket-client [--retry] <sock>  PTY プロキシへ接続（M5c）
//
// version は -ldflags "-X main.version=..." で埋め込む（goreleaser）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/4noha/claude-master-go/internal/client"
	"github.com/4noha/claude-master-go/internal/cloud/agent"
	"github.com/4noha/claude-master-go/internal/cloud/relay"
	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/monitor"
	"github.com/4noha/claude-master-go/internal/ptyproxy"
	"github.com/4noha/claude-master-go/internal/selfupdate"
	"github.com/4noha/claude-master-go/internal/tmux"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Println("claude-master", version)
	case "config":
		printConfig()
	case "update":
		runUpdate()
	case "proxy":
		runProxy(os.Args[2:])
	case "socket-client":
		runSocketClient(os.Args[2:])
	case "monitor":
		runMonitor(os.Args[2:])
	case "cloud":
		runCloud(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func printConfig() {
	c := config.Load()
	fmt.Printf("ConfigFile        = %s\n", c.ConfigFile)
	fmt.Printf("SizePolicy        = %s\n", c.SizePolicy)
	fmt.Printf("HostFlowScrollbck = %v\n", c.HostFlowScrollbck)
	fmt.Printf("NavKey            = %#x\n", c.NavKey)
	fmt.Printf("NavScrollStep     = %d\n", c.NavScrollStep)
	fmt.Printf("NavPageStep       = %d\n", c.NavPageStep)
	fmt.Printf("PageKeyScroll     = %v\n", c.PageKeyScroll)
	fmt.Printf("WheelScroll       = %v\n", c.WheelScroll)
	fmt.Printf("NavWheelStep      = %d\n", c.NavWheelStep)
	fmt.Printf("SessionLog        = %q\n", c.SessionLog)
	fmt.Printf("TmuxSession       = %s\n", c.TmuxSession)
	fmt.Printf("PollInterval      = %d\n", c.PollInterval)
}

func runUpdate() {
	fmt.Printf("現在 %s。最新を確認中...\n", version)
	tag, updated, err := selfupdate.Update(version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "更新失敗:", err)
		os.Exit(1)
	}
	if !updated {
		fmt.Printf("既に最新です (%s)\n", tag)
		return
	}
	fmt.Printf("更新しました: %s → %s\n", version, tag)
}

// runProxy: claude-master proxy [claude args...]
// 実 claude(cfg.RealClaude) を PTY ラップして起動し、host stdout +
// sessions/<pid>.sock 多重化で中継（claude-wrap の置換＝cutover 中核）。
func runProxy(args []string) {
	cfg := config.Load()
	if _, err := os.Stat(cfg.RealClaude); err != nil {
		fmt.Fprintf(os.Stderr,
			"claude が見つかりません: %s（REAL_CLAUDE で指定）\n", cfg.RealClaude)
		os.Exit(1)
	}
	stdoutFd := int(os.Stdout.Fd())
	winSize := func() (int, int) {
		c, r, err := term.GetSize(stdoutFd)
		if err != nil {
			return 80, 24
		}
		return c, r
	}
	var restore func()
	if term.IsTerminal(int(os.Stdin.Fd())) {
		if st, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
			restore = func() { _ = term.Restore(int(os.Stdin.Fd()), st) }
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)

	code, err := ptyproxy.RunProxy(ptyproxy.ProxyOpts{
		Argv:     append([]string{cfg.RealClaude}, args...),
		Cfg:      cfg,
		HostIn:   os.Stdin,
		HostOut:  os.Stdout,
		WinSize:  winSize,
		Sigwinch: sig,
		Version:  version, // status.json の cm_version（per-proxy 版・🟢/🔴 用）
	})
	signal.Stop(sig)
	if restore != nil {
		restore()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "proxy:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// runMonitor: claude-master monitor [start|stop|status|--daemon|--dashboard]
func runMonitor(args []string) {
	cfg := config.Load()
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "start":
		exitErr(monitor.CmdStart(cfg, os.Stdout))
	case "stop":
		exitErr(monitor.CmdStop(cfg, os.Stdout))
	case "status":
		exitErr(monitor.CmdStatus(cfg, os.Stdout))
	case "--dashboard":
		done := sigDone()
		monitor.Dashboard(cfg, done, os.Stdout)
	case "", "--daemon":
		done := sigDone()
		exitErr(monitor.CmdRun(cfg, done, os.Stdout))
	default:
		fmt.Fprintln(os.Stderr,
			"usage: claude-master monitor [start|stop|status|--daemon]")
		os.Exit(2)
	}
}

// sigDone は SIGTERM/SIGINT で閉じる done チャネル。
func sigDone() <-chan struct{} {
	done := make(chan struct{})
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-ch; close(done) }()
	return done
}

func exitErr(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runSocketClient: claude-master socket-client [--retry] <sock>
func runSocketClient(args []string) {
	retry := false
	var sock string
	for _, a := range args {
		if a == "--retry" {
			retry = true
			continue
		}
		if sock == "" {
			sock = a
		}
	}
	if sock == "" {
		fmt.Fprintln(os.Stderr,
			"usage: claude-master socket-client [--retry] <socket_path>")
		os.Exit(2)
	}
	if err := client.Run(sock, retry, config.Load()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// sigCtx は SIGTERM/SIGINT で cancel される context。
func sigCtx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-ch; cancel() }()
	return ctx, cancel
}

// statusSessions は STATUS_FILE の sessions[] を返す（monitor 書出）。
func statusSessions(cfg *config.Config) []map[string]any {
	b, err := os.ReadFile(cfg.StatusFile)
	if err != nil {
		return nil
	}
	var p struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	return p.Sessions
}

// resolveSock は sid（session key）→ ローカル <pid>.sock。STATUS_FILE
// の sessions[] から key 一致の pid を引き、sock が在れば返す。
func resolveSock(cfg *config.Config) func(string) (string, bool) {
	return func(sid string) (string, bool) {
		for _, s := range statusSessions(cfg) {
			if k, _ := s["key"].(string); k != sid {
				continue
			}
			pidf, _ := s["pid"].(float64)
			sock := filepath.Join(cfg.SessionsDir,
				strconv.Itoa(int(pidf))+".sock")
			if _, err := os.Stat(sock); err == nil {
				return sock, true
			}
			return "", false
		}
		return "", false
	}
}

// runCloud: claude-master cloud {agent|attach <sid> [--pc <PC>]}
func runCloud(args []string) {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	// enroll は「未設定の新 PC」が設定を受け取る入口なので
	// GCP_PROJECT ガードより前で処理する。
	if sub == "enroll" {
		runCloudEnroll(args[1:])
		return
	}
	cfg := config.Load()
	if cfg.GCPProject == "" {
		fmt.Fprintln(os.Stderr,
			"cloud 無効: GCP_PROJECT 未設定（Web の『端末を追加』→ "+
				"`claude-master cloud enroll <code>` で設定可）")
		os.Exit(2)
	}
	switch sub {
	case "agent":
		runCloudAgent(cfg)
	case "attach":
		runCloudAttach(cfg, args[1:])
	default:
		fmt.Fprintln(os.Stderr,
			"usage: claude-master cloud {agent|attach <sid> [--pc <PC>]|enroll <code>}")
		os.Exit(2)
	}
}

// runCloudEnroll: Web で発行した enroll コードを relay と交換し、
// SA 鍵と設定（GCP_PROJECT/CLOUD_RELAY_URL）を自動配置する。
// 使い方: claude-master cloud enroll <code> --relay wss://<host>
func runCloudEnroll(args []string) {
	var code, relay string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--relay":
			if i+1 < len(args) {
				relay = args[i+1]
				i++
			}
		default:
			if code == "" {
				code = args[i]
			}
		}
	}
	if code == "" || relay == "" {
		fmt.Fprintln(os.Stderr,
			"usage: claude-master cloud enroll <code> --relay wss://<host>")
		os.Exit(2)
	}
	httpBase := strings.Replace(strings.Replace(relay,
		"wss://", "https://", 1), "ws://", "http://", 1)
	resp, err := http.PostForm(httpBase+"/enroll", url.Values{"code": {code}})
	if err != nil {
		exitErr(fmt.Errorf("enroll 接続失敗: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		exitErr(fmt.Errorf("enroll 失敗(%d): コードが無効か期限切れ", resp.StatusCode))
	}
	var b struct {
		GCPProject string `json:"gcp_project"`
		RelayURL   string `json:"relay_url"`
		SAJSON     string `json:"sa_json"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		exitErr(fmt.Errorf("enroll 応答解析失敗: %w", err))
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".claude-master")
	_ = os.MkdirAll(dir, 0o755)
	saPath := ""
	if b.SAJSON != "" {
		saPath = filepath.Join(dir, "sa.json")
		if err := os.WriteFile(saPath, []byte(b.SAJSON), 0o600); err != nil {
			exitErr(fmt.Errorf("SA 鍵書込失敗: %w", err))
		}
	}
	relayURL := b.RelayURL
	if relayURL == "" {
		relayURL = relay
	}
	if err := writeTomlKeys(filepath.Join(home, ".claude-master.toml"),
		map[string]string{
			"GCP_PROJECT":     b.GCPProject,
			"CLOUD_RELAY_URL": relayURL,
		}); err != nil {
		exitErr(fmt.Errorf("設定書込失敗: %w", err))
	}
	// owner 発行コードでの正規 enroll＝再認可。過去の強制失効を解除
	// （信頼の起点は owner 発行の一回限りコード。best-effort）。
	if saPath != "" && b.GCPProject != "" {
		_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", saPath)
		cfg := config.Load()
		if ctx, cancel := context.WithTimeout(context.Background(),
			10*time.Second); true {
			if est, e := state.New(ctx, b.GCPProject, cfg.PCID); e == nil {
				_ = est.ClearRevoked(ctx, cfg.PCID)
				est.Close()
			}
			cancel()
		}
	}
	fmt.Println("端末を登録しました（このアカウントに参加）。")
	fmt.Printf("  GCP_PROJECT=%s\n  CLOUD_RELAY_URL=%s\n", b.GCPProject, relayURL)
	if saPath != "" {
		fmt.Printf("  認証鍵: %s\n", saPath)
	}
	fmt.Println("次: `claude-master cloud agent` を起動（常駐化推奨）。" +
		"proxy/monitor も同 PC で動かすと端末一覧にセッションが出ます。")
}

// writeTomlKeys は既存 toml の同名キー行を置換し、無ければ追記する
// （他キーは保持。値は文字列としてクォート）。
func writeTomlKeys(path string, kv map[string]string) error {
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			drop := false
			for k := range kv {
				t := strings.TrimSpace(ln)
				if strings.HasPrefix(t, k+" ") || strings.HasPrefix(t, k+"=") {
					drop = true
				}
			}
			if !drop && strings.TrimSpace(ln) != "" {
				lines = append(lines, ln)
			}
		}
	}
	for k, v := range kv {
		lines = append(lines, fmt.Sprintf("%s = %q", k, v))
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func runCloudAgent(cfg *config.Config) {
	ctx, cancel := sigCtx()
	defer cancel()
	st, err := state.New(ctx, cfg.GCPProject, cfg.PCID)
	if err != nil {
		exitErr(err)
	}
	defer st.Close()
	// 強制失効済なら登録しない（管理 UI で解除された端末。owner が
	// 再 enroll するまでドーマント＝一覧にも出ない・コストも止まる）。
	if st.IsSelfRevoked(ctx) {
		fmt.Fprintln(os.Stderr,
			"この端末はペアリング解除済（dormant）。再 enroll で復帰します。")
	} else if err := st.RegisterPCVersion(ctx, version); err != nil { // 端末一覧＋agent 版
		exitErr(fmt.Errorf("PC 登録失敗: %w", err))
	}
	// セッション一覧をクラウドへ定期 upsert（差分は content_hash 判定）。
	// 加えて「前 tick に在ったが今 tick に居ない」キーは DeleteSession で
	// クラウドから消し、終了を consumer へ push 同期する（in-memory 差分
	// なので追加 Firestore 読み無し＝終了時の Delete 書込のみ）。prev は
	// 起動時に Firestore 実態で seed（agent 再起動中に終了した分も拾う）。
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		prev := map[string]bool{}
		if seed, e := st.OwnSessionKeys(ctx); e == nil {
			for _, k := range seed {
				prev[k] = true
			}
		}
		for {
			// 失効中は push/登録を一切しない（一覧から消えたまま・
			// コスト停止）。owner が ClearRevoked（再 enroll）したら
			// 次 tick で自然復帰。
			if st.IsSelfRevoked(ctx) {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				continue
			}
			ss := statusSessions(cfg)
			cur := map[string]bool{}
			for _, s := range ss {
				if k := state.SessionKeyOf(s); k != "" {
					cur[k] = true
				}
			}
			if len(ss) > 0 {
				_, _ = st.PushStatus(ctx, ss)
			}
			for k := range prev {
				if !cur[k] {
					_ = st.DeleteSession(ctx, k) // 終了セッションを同期 kill
				}
			}
			prev = cur
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	// 他 PC の claude セッションを this PC の tmux に窓として同期
	// （各窓は cloud attach の再接続ループ＝viewer）。
	if mgr, merr := tmux.NewManager(cfg.TmuxSession); merr == nil {
		mgr.EnsureSession()
		// dashboard が「外部 PC のセッション」も出せるよう同期側が
		// 取得済みの一覧をスナップショット出力（Firestore 追加読み無し）。
		agent.SnapshotPath = cfg.RemoteFile
		self, _ := os.Executable()
		sa := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		wc := func(pc, sid, dir string) string {
			// 再接続は 30s 間隔（切断時の Cloud Run 再接続＋Firestore
			// wake 書込のコスト churn を抑制。閲覧復帰は最大 30s 遅延）。
			return fmt.Sprintf("while true; do env GCP_PROJECT=%s "+
				"CLOUD_RELAY_URL=%s GOOGLE_APPLICATION_CREDENTIALS=%s "+
				"%s cloud attach %s --pc %s; sleep 30; done",
				cfg.GCPProject, cfg.CloudRelayURL, sa, self, sid, pc)
		}
		go agent.RunRemoteTmuxSync(ctx, st, mgr, cfg.PCID, wc)
	} else {
		fmt.Fprintln(os.Stderr, "remote tmux 同期スキップ（tmux 無し）:", merr)
	}
	ag := &agent.Agent{
		St: st, RelayURL: cfg.CloudRelayURL,
		ResolveSock: resolveSock(cfg), IdleClose: 30 * time.Second,
	}
	fmt.Printf("cloud agent: pc=%s project=%s relay=%s\n",
		cfg.PCID, cfg.GCPProject, cfg.CloudRelayURL)
	if err := ag.Run(ctx); err != nil && ctx.Err() == nil {
		exitErr(err)
	}
}

func runCloudAttach(cfg *config.Config, args []string) {
	var sid, targetPC string
	for i := 0; i < len(args); i++ {
		if args[i] == "--pc" && i+1 < len(args) {
			targetPC = args[i+1]
			i++
		} else if sid == "" {
			sid = args[i]
		}
	}
	if sid == "" {
		fmt.Fprintln(os.Stderr, "usage: claude-master cloud attach <sid> [--pc <PC>]")
		os.Exit(2)
	}
	if targetPC == "" {
		targetPC = cfg.PCID
	}
	if cfg.CloudRelayURL == "" {
		exitErr(fmt.Errorf("CLOUD_RELAY_URL 未設定（deploy 後に設定）"))
	}
	ctx, cancel := sigCtx()
	defer cancel()
	st, err := state.New(ctx, cfg.GCPProject, cfg.PCID)
	if err != nil {
		exitErr(err)
	}
	defer st.Close()
	if err := st.Wake(ctx, targetPC, sid); err != nil { // 相手 PC を起こす
		exitErr(fmt.Errorf("wake 失敗: %w", err))
	}
	// 公開 /session 認可のため viewer グラントを書いてから接続
	// （SA を持つ正規接続元のみ書ける＝relay が検証）。
	_ = st.PutRelayGrant(ctx, sid, "viewer", 60*time.Second)
	conn, err := relay.Dial(ctx, cfg.CloudRelayURL, sid, "viewer")
	if err != nil {
		exitErr(fmt.Errorf("relay 接続失敗: %w", err))
	}
	defer conn.Close()
	// 実証済 socket_client 本体を WSS conn でそのまま再利用
	if err := client.RunConn(conn, cfg); err != nil {
		exitErr(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr,
		"usage: claude-master {config|update|version|proxy|socket-client|monitor|cloud}")
}
