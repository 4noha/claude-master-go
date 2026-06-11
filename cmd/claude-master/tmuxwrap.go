package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/4noha/claude-master-go/internal/ttysync"
)

// runTmuxWrap: claude-master tmux-wrap [--idle-ms N] -- <cmd> [args...]
//
// 子プロセス cmd を PTY 経由で起動し、子→host stdout を idle (default 4ms)
// で batch して flicker 軽減する wrapper。tmux/screen/別 multiplexer の
// 出力をまとめて 1 write に集約することで、DECSET 2026 未対応端末
// (xterm.js / Mac Terminal.app 等) でも render tick 内で atomic に近い
// 描画を狙う。詳細は internal/ttysync/wrap.go と CLAUDE.md「tmux 経由
// ちらつき残課題」節。
//
// 想定用法:
//
//	claude-master tmux-wrap -- tmux attach -t claude-master
//	claude-master tmux-wrap --idle-ms 8 -- tmux -L diag attach
//
// 主に Mac Terminal.app / VSCode terminal のような 2026 未対応端末で
// 使う。iTerm2 等の sync 対応端末では効果は限定的だが副作用も無いので
// 害は無い。
func runTmuxWrap(args []string) {
	idleMs := 4
	holdMs := 0    // 0 = 既定値 (32ms) を internal/ttysync 側で決定
	maxHoldMs := 0 // 0 = 既定値 (50ms)
	syncWrap := true
	rest := args
	for len(rest) > 0 {
		switch rest[0] {
		case "--idle-ms":
			if len(rest) < 2 {
				usageTmuxWrap()
				os.Exit(2)
			}
			n, err := parseIntFlag(rest[1])
			if err != nil || n < 0 {
				fmt.Fprintln(os.Stderr, "tmux-wrap: --idle-ms は非負整数")
				os.Exit(2)
			}
			idleMs = n
			rest = rest[2:]
		case "--hold-ms":
			if len(rest) < 2 {
				usageTmuxWrap()
				os.Exit(2)
			}
			n, err := parseIntFlag(rest[1])
			if err != nil {
				fmt.Fprintln(os.Stderr, "tmux-wrap: --hold-ms は整数 (負で無効化)")
				os.Exit(2)
			}
			if n == 0 {
				// 0 を「無効化」として渡したい場合は -1 を使う規約
				// (Opts 仕様)。0 そのままだと既定値 32ms 適用。
				holdMs = -1
			} else {
				holdMs = n
			}
			rest = rest[2:]
		case "--max-hold-ms":
			if len(rest) < 2 {
				usageTmuxWrap()
				os.Exit(2)
			}
			n, err := parseIntFlag(rest[1])
			if err != nil {
				fmt.Fprintln(os.Stderr, "tmux-wrap: --max-hold-ms は整数 (負で無効化)")
				os.Exit(2)
			}
			if n == 0 {
				maxHoldMs = -1
			} else {
				maxHoldMs = n
			}
			rest = rest[2:]
		case "--no-sync-wrap":
			syncWrap = false
			rest = rest[1:]
		case "--":
			rest = rest[1:]
			goto done
		case "-h", "--help":
			usageTmuxWrap()
			return
		default:
			// `--` 区切り無しでも先頭が flag 系で無ければ cmd 開始扱い
			goto done
		}
	}
done:
	if len(rest) == 0 {
		usageTmuxWrap()
		os.Exit(2)
	}
	if _, err := exec.LookPath(rest[0]); err != nil {
		fmt.Fprintln(os.Stderr, "tmux-wrap: command not found:", rest[0])
		os.Exit(2)
	}
	err := ttysync.WrapStdio(rest, ttysync.Opts{
		IdleMs:                 idleMs,
		HoldAfterDestructiveMs: holdMs,
		SyncWrap:               syncWrap,
		MaxHoldMs:              maxHoldMs,
	})
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "tmux-wrap:", err)
		os.Exit(1)
	}
}

func usageTmuxWrap() {
	fmt.Fprintln(os.Stderr,
		"usage: claude-master tmux-wrap [flags] -- <cmd> [args...]\n"+
			"  <cmd> を PTY 経由で起動し、子→stdout を idle (default 4ms)\n"+
			"  で batch して flicker 軽減。flush は BSU/ESU (DECSET 2026)\n"+
			"  で囲み (内側 marker は除去)、2026 honor 端末 (iTerm2 等) で\n"+
			"  100% atomic 描画にする。画面クリア検出時は hold (default\n"+
			"  32ms) で「クリア→redraw」を同一 batch に集約＝blackout 抑止。\n"+
			"  flags:\n"+
			"    --idle-ms N      idle 検出 (default 4)\n"+
			"    --hold-ms N      destructive 後の hold (default 32、負で無効)\n"+
			"    --max-hold-ms N  最大滞留 (default 50、負で無効)\n"+
			"    --no-sync-wrap   BSU/ESU wrap を無効化\n"+
			"  例: claude-master tmux-wrap -- tmux attach -t claude-master")
}

func parseIntFlag(s string) (int, error) {
	return strconv.Atoi(s)
}
