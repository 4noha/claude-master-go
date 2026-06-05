//go:build windows

package main

import (
	"fmt"
	"os"
)

// Windows は tmux 不在のため stub。
func runTmuxRender(args []string) {
	fmt.Fprintln(os.Stderr,
		"tmux-render: Windows non-supported (tmux 不在のため)。\n"+
			"主に Mac/Linux の tmux session 向けの flicker 構造解消機能。\n"+
			"Windows では psmux 経路あり ConPTY 描画なので不要。")
	os.Exit(2)
}
