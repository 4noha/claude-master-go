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
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/4noha/claude-master-go/internal/client"
	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/monitor"
	"github.com/4noha/claude-master-go/internal/ptyproxy"
	"github.com/4noha/claude-master-go/internal/selfupdate"
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

func usage() {
	fmt.Fprintln(os.Stderr,
		"usage: claude-master {config|update|version|proxy|socket-client|monitor}")
}
