//go:build !windows


package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/4noha/claude-master-go/internal/tmuxcc"
)

// runTmuxRender: claude-master tmux-render [-L socket] [-t session]
//
// L4-A' (本質改修) の核となる subcommand。tmux server に -CC mode で
// 接続し、`%output` で受信した **proxy frame の生 bytes** を自前の
// renderer (screen.VT 再利用) で 1 atomic write/frame で stdout に
// 描画する。tmux 自身の outer render path を bypass するため、tmux
// outer 区間で発生していた flicker (BSU/ESU 漏れ約 50%・CLAUDE.md
// 「tmux 経由ちらつき残課題」記載) が物理的に消える。
//
// 使い方:
//
//	claude-master tmux-render -t claude-master       # default socket
//	claude-master tmux-render -L diag -t claude-master  # 別 socket
//
// MVP (P4 時点): 単一 active pane の full-screen 描画。multi-pane
// layout 対応は P5 で追加。
//
// 既存 `claude-master tmux-wrap`(L1/L2 idle/hold batch wrapper) との
// 違い: tmux-wrap は tmux client の出力を batch するだけ (heuristic)。
// tmux-render は tmux client にならず tmux server に control mode で
// 接続し自前で描画する＝構造的 atomic 保証。
func runTmuxRender(args []string) {
	socket := ""
	session := ""
	rest := args
	for len(rest) > 0 {
		switch rest[0] {
		case "-L":
			if len(rest) < 2 {
				usageTmuxRender()
				os.Exit(2)
			}
			socket = rest[1]
			rest = rest[2:]
		case "-t":
			if len(rest) < 2 {
				usageTmuxRender()
				os.Exit(2)
			}
			session = rest[1]
			rest = rest[2:]
		case "-h", "--help":
			usageTmuxRender()
			return
		default:
			fmt.Fprintln(os.Stderr, "tmux-render: unknown arg:", rest[0])
			usageTmuxRender()
			os.Exit(2)
		}
	}

	// 1. 外側端末サイズを取得 (tmux に通知し、renderer の描画サイズ)
	stdinFd := int(os.Stdin.Fd())
	cols, rows, err := term.GetSize(stdinFd)
	if err != nil || cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}

	// 2. tmux -CC client 起動
	cli, err := tmuxcc.Start(tmuxcc.StartOpts{
		Socket: socket, Session: session, Cols: cols, Rows: rows,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmux-render:", err)
		os.Exit(1)
	}
	defer cli.Close()

	// 3. host stdin を raw 化 (各 keystroke を即送るため)
	if old, err := term.MakeRaw(stdinFd); err == nil {
		defer term.Restore(stdinFd, old)
	}

	// 4. renderer + event loop
	r := tmuxcc.NewRenderer(os.Stdout, cols, rows)

	// 5. SIGWINCH → tmux 側 client resize + renderer サイズ追従
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			newCols, newRows, err := term.GetSize(stdinFd)
			if err != nil || newCols <= 0 || newRows <= 0 {
				continue
			}
			_ = cli.Resize(newCols, newRows)
			_ = cli.Send(tmuxcc.ResizeCommand(newCols, newRows))
			r.SetSize(newCols, newRows)
		}
	}()

	// 6. stdin pump: 読んだ bytes を send-keys -l literal で active pane へ
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				active := r.Active()
				if active == "" {
					// 初期 active が決まる前は捨てる (tmux 接続直後)
					continue
				}
				cmd := tmuxcc.EncodeSendKeysLiteral(active, buf[:n])
				if e := cli.Send(cmd); e != nil {
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					fmt.Fprintln(os.Stderr, "tmux-render: stdin:", err)
				}
				return
			}
		}
	}()

	// 7. tmux event loop: %output → renderer に Feed
	for {
		select {
		case msg, ok := <-cli.Events:
			if !ok {
				return // tmux 接続終了
			}
			switch m := msg.(type) {
			case *tmuxcc.OutputMsg:
				r.HandleOutput(m.PaneID, m.Data)
			case *tmuxcc.LayoutChangeMsg:
				// pane 構成変更: layout parse して active を最初の leaf
				// pane に置く (MVP)。multi-pane composed render は将来
				// enhancement。
				if layout, err := tmuxcc.ParseLayout(m.Layout); err == nil {
					if leaves := layout.LeafPanes(); len(leaves) > 0 {
						r.SetActive(leaves[0])
					}
				}
			case *tmuxcc.WindowCloseMsg:
				// MVP: window close は無視 (pane 単位は別 msg で来る)
			case *tmuxcc.ExitMsg:
				fmt.Fprintln(os.Stderr, "tmux exit:", m.Reason)
				return
			case *tmuxcc.OtherMsg:
				if strings.HasPrefix(m.Type, "%READ-ERR") ||
					strings.HasPrefix(m.Type, "%PARSE-ERR") {
					fmt.Fprintln(os.Stderr, "tmux-render:", m.Type, m.Rest)
					return
				}
			}
		case <-cli.Done():
			return
		}
	}
}

func usageTmuxRender() {
	fmt.Fprintln(os.Stderr,
		"usage: claude-master tmux-render [-L socket] [-t session]\n"+
			"  tmux server に -CC mode で接続し proxy frame の生 bytes\n"+
			"  を自前 renderer (screen.VT 再利用) で 1 atomic write/frame\n"+
			"  で描画する。tmux outer render を完全 bypass＝flicker 構造\n"+
			"  解消 (L4-A')。MVP: 単一 active pane の full-screen 描画。\n"+
			"  multi-pane layout は将来対応。\n"+
			"  例: claude-master tmux-render -t claude-master")
}
