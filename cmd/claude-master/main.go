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

	"github.com/4noha/claude-master-go/internal/client"
	"github.com/4noha/claude-master-go/internal/config"
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
		fmt.Fprintln(os.Stderr, "proxy: monitor 配線待ち（M5d）。")
		os.Exit(1)
	case "socket-client":
		runSocketClient(os.Args[2:])
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
		"usage: claude-master {config|update|version|proxy|socket-client}")
}
