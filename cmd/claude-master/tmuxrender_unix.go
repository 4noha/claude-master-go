//go:build !windows

package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/4noha/claude-master-go/internal/tmuxcc"
)

// runTmuxRender: claude-master tmux-render [-L socket] [-t session]
//
// tmux -CC (control mode) の %output で届く **proxy frame の生 bytes**
// を一切再描画せず verbatim で端末へ転送する「中間層」(Web の sync.js
// と同じ役割)。proxy frame は元から BSU+?25l+2J+全行+cursor 復元+?25h+
// ESU の完全 atomic 単位なので、素通しすれば DECSET 2026 honor 端末
// (iTerm2 documented・VSCode terminal は DECRQM 実測済) で Web と同一の
// 描画品質になる。cls (2J) は frame 内に密閉＝単独で可視化される瞬間が
// 構造的に存在しない。
//
// tmux 通常 attach 経路との違い: tmux outer は proxy frame の境界を
// 消費・破壊して 64% を裸 emit する (m1 実測)＝時間基準 batch
// (tmux-wrap) では frame 途中の commit 境界＝中間状態 flicker が原理的
// に残る。-CC は境界が生き残る唯一の経路。
//
// MVP 制約:
//   - 単一 pane viewer (attach 時の active pane に固定)。tmux prefix
//     キーは tmux に解釈されない (全キーが pane へ literal 送信) ため
//     window 切替不可。別 window を見たい時は別途通常 attach。
//   - 終了は別 terminal から pkill -TERM -f tmux-render
//     (キーは全て claude へ届くため)。将来 escape キー実装予定。
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

	stdinFd := int(os.Stdin.Fd())
	cols, rows, err := term.GetSize(stdinFd)
	if err != nil || cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}

	cli, err := tmuxcc.Start(tmuxcc.StartOpts{
		Socket: socket, Session: session, Cols: cols, Rows: rows,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmux-render:", err)
		os.Exit(1)
	}
	defer cli.Close()

	// 1. handshake: %session-changed を待つ
	if !waitSessionChanged(cli, 5*time.Second) {
		fmt.Fprintln(os.Stderr, "tmux-render: handshake timeout")
		os.Exit(1)
	}

	// 2. active pane を確定 (display-message 応答 = ReplyLineMsg)。
	//    この段階は serial (stdin pump 未開始) なので相関は自明。
	pane := queryOneLine(cli, "display-message -p '#{pane_id}'", 3*time.Second)
	if pane == "" || !strings.HasPrefix(pane, "%") {
		fmt.Fprintln(os.Stderr, "tmux-render: active pane 取得失敗:", pane)
		os.Exit(1)
	}

	fwd := tmuxcc.NewForwarder(os.Stdout)
	fwd.SetActive(pane)

	// 3. catch-up: 現 pane 内容を capture-pane で取得し、proxy frame と
	//    同じ規律 (BSU+?25l+2J+content+?25h+ESU) で 1 atomic write。
	//    以降は claude の次 frame が完全な状態を上書きする。
	if lines := queryLines(cli, "capture-pane -p -e -t "+pane, 3*time.Second); len(lines) > 0 {
		var b strings.Builder
		b.WriteString("\x1b[?2026h\x1b[?25l\x1b[2J\x1b[H")
		for i, l := range lines {
			b.WriteString(l)
			if i+1 < len(lines) {
				b.WriteString("\r\n")
			}
		}
		b.WriteString("\x1b[?25h\x1b[?2026l")
		_, _ = os.Stdout.WriteString(b.String())
	}

	// 4. host stdin raw 化 + 入力 pump (全キーを active pane へ literal)
	if old, err := term.MakeRaw(stdinFd); err == nil {
		defer term.Restore(stdinFd, old)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if e := cli.Send(tmuxcc.EncodeSendKeysLiteral(fwd.Active(), buf[:n])); e != nil {
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

	// 5. SIGWINCH → tmux 側 client size 更新 (pane が追随 resize →
	//    proxy が新サイズの frame を吐く)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)
	go func() {
		for range sigCh {
			c, r, err := term.GetSize(stdinFd)
			if err != nil || c <= 0 || r <= 0 {
				continue
			}
			_ = cli.Resize(c, r)
			_ = cli.Send(tmuxcc.ResizeCommand(c, r))
		}
	}()

	// 6. main loop: %output → verbatim 転送
	for {
		select {
		case msg, ok := <-cli.Events:
			if !ok {
				return
			}
			switch m := msg.(type) {
			case *tmuxcc.OutputMsg:
				fwd.HandleOutput(m.PaneID, m.Data)
			case *tmuxcc.ExitMsg:
				fmt.Fprintln(os.Stderr, "\r\ntmux exit:", m.Reason)
				return
			case *tmuxcc.OtherMsg:
				if strings.HasPrefix(m.Type, "%READ-ERR") {
					fmt.Fprintln(os.Stderr, "\r\ntmux-render:", m.Type, m.Rest)
					return
				}
			}
		case <-cli.Done():
			return
		}
	}
}

// waitSessionChanged は handshake の %session-changed を待つ。
func waitSessionChanged(cli *tmuxcc.Client, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case msg, ok := <-cli.Events:
			if !ok {
				return false
			}
			if _, yes := msg.(*tmuxcc.SessionChangedMsg); yes {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// queryOneLine はコマンドを送り、最初の応答行 (ReplyLineMsg) を返す。
// serial phase (stdin pump 未開始) 専用＝相関は in-flight 1 件で自明。
func queryOneLine(cli *tmuxcc.Client, cmd string, timeout time.Duration) string {
	lines := queryLines(cli, cmd, timeout)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

// queryLines はコマンドを送り、%end までの応答行を集めて返す。
func queryLines(cli *tmuxcc.Client, cmd string, timeout time.Duration) []string {
	if err := cli.Send(cmd); err != nil {
		return nil
	}
	deadline := time.After(timeout)
	began := false
	var lines []string
	for {
		select {
		case msg, ok := <-cli.Events:
			if !ok {
				return lines
			}
			switch m := msg.(type) {
			case *tmuxcc.BeginMsg:
				began = true
			case *tmuxcc.ReplyLineMsg:
				if began {
					lines = append(lines, m.Text)
				}
			case *tmuxcc.EndMsg:
				if began {
					return lines
				}
			}
		case <-deadline:
			return lines
		}
	}
}

func usageTmuxRender() {
	fmt.Fprintln(os.Stderr,
		"usage: claude-master tmux-render [-L socket] [-t session]\n"+
			"  tmux -CC の出力 (= proxy frame の生 bytes) を再描画せず\n"+
			"  verbatim 転送する中間層 (Web の sync.js 相当)。proxy frame\n"+
			"  は完全 atomic 単位なので、DECSET 2026 対応端末 (iTerm2 /\n"+
			"  VSCode terminal 実測済) で Web と同一の描画品質になる。\n"+
			"  MVP: 単一 pane viewer (attach 時の active pane 固定・tmux\n"+
			"  prefix キー不可)。終了は別 terminal から:\n"+
			"    pkill -TERM -f tmux-render\n"+
			"  例: claude-master tmux-render -t claude-master")
}
