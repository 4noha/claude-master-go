//go:build windows

package tmuxcc

import "errors"

// Windows は tmux 不在のため stub。実装するなら ConPTY + psmux -CC
// (psmux 側の control mode サポート要)。当面 unix のみ。
type Client struct {
	Events chan Msg
}

type StartOpts struct {
	Socket, Session string
	Cols, Rows      int
}

func Start(opts StartOpts) (*Client, error) {
	return nil, errors.New("tmuxcc: not supported on Windows")
}

func (c *Client) Send(cmd string) error                  { return errors.New("not supported") }
func (c *Client) SendBytes(b []byte) error               { return errors.New("not supported") }
func (c *Client) Resize(cols, rows int) error            { return errors.New("not supported") }
func (c *Client) Done() <-chan struct{}                  { return nil }
func (c *Client) Close() error                           { return nil }
