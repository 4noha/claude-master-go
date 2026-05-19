// Package ptyproxy は claude を PTY 配下で起動し、出力を忠実 VT モデルへ
// 流し、host stdin/stdout と unix socket(複数 client)を多重化する
// （Python pty_proxy.py の移植）。
//
// PTY/プロセス実体は OS 依存（unix=creack/pty・windows=ConPTY）。本
// ファイルは OS 非依存の骨格のみで、実体は ptyBackend 経由（M8b）。
// server.go/run.go は Proxy の公開面（master の Read/Write/Close と
// Setsize/Wait/Pid/Close）だけに依存し OS 差を意識しない。
package ptyproxy

import (
	"errors"
	"io"

	"github.com/4noha/claude-master-go/internal/screen"
)

// ptyBackend は OS 依存の PTY＋子プロセス実体。startBackend は OS-split
// （proxy_unix.go=creack/pty、proxy_windows.go=ConPTY）で実装する。
type ptyBackend interface {
	Master() io.ReadWriteCloser // 子の出力読取・入力書込
	Setsize(cols, rows int) error
	Wait() error // 子終了まで待つ。終了コードは ExitCode() で運ぶ
	Pid() int
	Close()
	// closedErr は err が「子終了で master が閉じた」を表すか
	// （unix=EIO 等。io.EOF は PumpToVT 側で別途扱う）。
	closedErr(err error) bool
}

// startBackend（argv を (cols,rows) の PTY 配下で起動）は OS-split で
// 実装する: proxy_unix.go=creack/pty、proxy_windows.go=ConPTY。

// Proxy は claude プロセスと忠実 VT モデルを保持する。
type Proxy struct {
	be     ptyBackend
	master io.ReadWriteCloser // == be.Master()。テストは直接注入可
	VT     *screen.VT
	cols   int
	rows   int
}

// Start は argv のコマンドを (cols,rows) の PTY 配下で起動する。子の
// 出力は **verbatim** に master へ流れる（録画リプレイ検証でバイト忠実）。
func Start(argv []string, cols, rows int) (*Proxy, error) {
	if len(argv) == 0 {
		return nil, errors.New("ptyproxy: 空 argv")
	}
	be, err := startBackend(argv, cols, rows)
	if err != nil {
		return nil, err
	}
	return &Proxy{
		be: be, master: be.Master(),
		VT:   screen.NewModel(cols, rows),
		cols: cols, rows: rows,
	}, nil
}

// Setsize は実行中 PTY と VT モデルのサイズを変更する。
func (p *Proxy) Setsize(cols, rows int) error {
	p.cols, p.rows = cols, rows
	if p.be == nil {
		return nil
	}
	return p.be.Setsize(cols, rows)
}

// PumpToVT は master を任意チャンクで読み、忠実 VT へ流す。子が終了し
// master が閉じる（EOF / OS 固有の閉鎖エラー）まで。
func (p *Proxy) PumpToVT() error {
	buf := make([]byte, 4096)
	for {
		n, err := p.master.Read(buf)
		if n > 0 {
			p.VT.Feed(buf[:n])
		}
		if err != nil {
			if err == io.EOF || (p.be != nil && p.be.closedErr(err)) {
				return nil
			}
			return err
		}
	}
}

// Wait は子プロセス終了を待つ。
func (p *Proxy) Wait() error {
	if p.be == nil {
		return nil
	}
	return p.be.Wait()
}

// Pid は子プロセス PID（未起動/テスト構築時は 0）。
func (p *Proxy) Pid() int {
	if p.be == nil {
		return 0
	}
	return p.be.Pid()
}

// Close はリソース解放。
func (p *Proxy) Close() {
	if p.be != nil {
		p.be.Close()
	}
}
