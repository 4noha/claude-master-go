//go:build !windows

package tmuxcc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

// Client は tmux server に -CC mode で接続する子プロセスのラッパ。
//
// 設計:
//   - tmux はクライアント側に real tty を要求 (PoC で確認: pipe では
//     "tcgetattr failed: Operation not supported on socket")
//   - したがって creack/pty で PTY を確保し子の stdin/stdout/stderr に
//     設定。既存 internal/ptyproxy の pattern を流用
//   - 子の stdout を行単位で読み、ParseLine で Msg にして Events に push
//   - command 送信は子の stdin に「コマンド文字列+\n」を書く (tmux の
//     control mode は改行区切り text protocol)
//
// 使い方:
//
//	c, err := Start(StartOpts{Socket: "", Session: "claude-master"})
//	for msg := range c.Events { ... }
//	c.Send("send-keys -t %0 -l foo")
//	c.Close()
type Client struct {
	cmd  *exec.Cmd
	ptmx *os.File

	Events chan Msg // 受信メッセージ (本 Client が close する)

	wmu       sync.Mutex // ptmx.Write 直列化 (Send / async send 競合避け)
	closeOnce sync.Once
	closed    chan struct{}
}

// StartOpts は Client.Start のパラメータ。
type StartOpts struct {
	Socket  string // tmux -L <socket> (空なら default)
	Session string // tmux attach -t <session>
	// PTY 初期サイズ。0 なら 80x24 既定。tmux 接続時 client_width/
	// client_height として通知され、tmux 側 pane size 決定に使われる。
	Cols, Rows int
}

// Start は tmux -CC attach を子起動して Client を返す。
func Start(opts StartOpts) (*Client, error) {
	args := []string{}
	if opts.Socket != "" {
		args = append(args, "-L", opts.Socket)
	}
	args = append(args, "-CC", "attach")
	if opts.Session != "" {
		args = append(args, "-t", opts.Session)
	}
	cmd := exec.Command("tmux", args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cols, rows := opts.Cols, opts.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("tmuxcc: start tmux -CC: %w", err)
	}
	c := &Client{
		cmd:    cmd,
		ptmx:   ptmx,
		Events: make(chan Msg, 64),
		closed: make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// readLoop は ptmx から line を読み、ParseLine で Msg にして Events
// に push。EOF or read error で Events close + closed signal。
func (c *Client) readLoop() {
	defer func() {
		c.closeOnce.Do(func() { close(c.closed) })
		close(c.Events)
	}()
	br := bufio.NewReader(c.ptmx)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			msg, perr := ParseLine(line)
			if perr != nil {
				// parse error はログ相当 (将来 channel 経由 propagate)
				// 今は raw として OtherMsg 化
				c.Events <- &OtherMsg{Type: "%PARSE-ERR", Rest: perr.Error()}
			} else if msg != nil {
				c.Events <- msg
			} else {
				// 非 % 行 = %begin/%end 間のコマンド応答本文
				// (display-message / capture-pane の結果)
				trimmed := strings.TrimRight(line, "\r\n")
				if trimmed != "" {
					c.Events <- &ReplyLineMsg{Text: trimmed}
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, syscall.EIO) {
				// 通信エラーも OtherMsg で通知
				c.Events <- &OtherMsg{Type: "%READ-ERR", Rest: err.Error()}
			}
			return
		}
	}
}

// Send は tmux に control mode コマンドを送る (改行 1 個区切り)。複数
// コマンドを連続で送る場合は 1 行ずつ Send 呼出。tmux は %begin/%end
// で応答を区切る。
func (c *Client) Send(cmd string) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.ptmx.Write([]byte(cmd + "\n"))
	return err
}

// SendBytes は wire への raw 書き込み (test 等で全行制御したい場合)。
// 通常は Send を使う。
func (c *Client) SendBytes(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.ptmx.Write(b)
	return err
}

// Resize は PTY サイズを変更 (host SIGWINCH → tmux 側 client size 更新)。
func (c *Client) Resize(cols, rows int) error {
	return pty.Setsize(c.ptmx, &pty.Winsize{
		Rows: uint16(rows), Cols: uint16(cols),
	})
}

// Done は readLoop 終了 (子の exit / pty close) で close される。
func (c *Client) Done() <-chan struct{} { return c.closed }

// Close は子プロセス終了 + PTY close。
func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
	}
	if c.ptmx != nil {
		_ = c.ptmx.Close()
	}
	if c.cmd != nil {
		_ = c.cmd.Wait()
	}
	return nil
}
