// Command claude-master — Go 版 claude-master の CLI エントリ。
//
//	claude-master config        設定の解決値を表示（M1）
//	claude-master version       バージョン
//	claude-master update        最新リリースへ自己更新（sha256 検証）
//	claude-master proxy [args]  claude を PTY ラップ（M3 で実装）
//
// version は -ldflags "-X main.version=..." で埋め込む（goreleaser）。
package main

import (
	"fmt"
	"os"

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
		fmt.Fprintln(os.Stderr, "proxy: 未実装（DESIGN.md M3）。先に M2(VT モデル)。")
		os.Exit(1)
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

func usage() {
	fmt.Fprintln(os.Stderr,
		"usage: claude-master {config|update|version|proxy}")
}
