//go:build windows

package ptyproxy

import (
	"encoding/binary"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/screen"
)

// M8c headless ゲート（鉄則#2: 合成でなく実 ConPTY＋実 AF_UNIX）。
// 実 cmd.exe を ConPTY backend(M8b)で起動 → Proxy/VT → server の
// AF_UNIX listener(server.go net.Listen("unix"))→ 実 client
// (net.Dial("unix")) → 受信フレームを screen.VT で再パースし、子の
// 既知出力 M8C_OK が per-client 描画に現れることを機械確認する。
// server_test.go の helper は !windows タグで非対象＝本ファイルは
// 自己完結。
//
// 注意（DESIGN_M8）: ConPTY は再レンダリングのため unix の pyte
// しきい値は使わず「実出力の既知文字列が VT に描画される」で判定。
// 対話的コンソール resize→再描画は実端末必須＝手動検証項目。

type winDrainer struct {
	mu  sync.Mutex
	buf []byte
}

func newWinDrainer(c net.Conn) *winDrainer {
	d := &winDrainer{}
	go func() {
		b := make([]byte, 8192)
		for {
			n, err := c.Read(b)
			if n > 0 {
				d.mu.Lock()
				d.buf = append(d.buf, b[:n]...)
				d.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return d
}

func (d *winDrainer) snapshot() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(append([]byte{}, d.buf...))
}

// winFrameText は受信バイト中の最後のフレームを VT 再パースし可視
// テキストを返す（display-oracle 方式。server_test.go の frameText と
// 同手法）。
func winFrameText(snap string, cols, rows int) string {
	fr := strings.Split(snap, "\x1b[?2026h")
	if len(fr) < 2 {
		return ""
	}
	v := screen.NewModel(cols, rows)
	v.Feed([]byte(fr[len(fr)-1]))
	return strings.Join(v.VisibleLines(), "\n")
}

func winResizeFrame(rows, cols int) []byte {
	b := append([]byte{}, resizeMagic...)
	var p [4]byte
	binary.BigEndian.PutUint16(p[0:2], uint16(rows))
	binary.BigEndian.PutUint16(p[2:4], uint16(cols))
	return append(b, p[:]...)
}

func TestConPTYProxyOverAFUnix_ClientReceivesRender(t *testing.T) {
	const cols, rows = 120, 30

	// 子は echo 後 ping で ~7s 生存（client 接続→feed→受信の猶予）。
	argv := []string{
		`C:\Windows\System32\cmd.exe`, "/c",
		"echo M8C_OK& ver& ping -n 8 127.0.0.1 >NUL",
	}
	p, err := Start(argv, cols, rows)
	if err != nil {
		t.Fatalf("Start(ConPTY): %v", err)
	}
	srv := NewServer(p, &config.Config{
		SizePolicy: "client", NavKey: []byte{0x1c},
		NavScrollStep: 1, NavPageStep: 10, NavWheelStep: 3,
	}, nil, 0, 0)

	// AF_UNIX 短パス（sun_path 制限）。作成→削除して空け、Serve に渡す。
	f, err := os.CreateTemp("", "cmw*.sock")
	if err != nil {
		t.Fatal(err)
	}
	sock := f.Name()
	_ = f.Close()
	_ = os.Remove(sock)
	t.Cleanup(func() { _ = os.Remove(sock) })

	if err := srv.Serve(sock); err != nil {
		t.Fatalf("Serve(AF_UNIX on Windows): %v", err)
	}
	t.Cleanup(func() { srv.Stop(); p.Close() })

	time.Sleep(400 * time.Millisecond) // ConPTY 出力 feed 待ち

	var c net.Conn
	for i := 0; i < 100; i++ {
		if c, err = net.Dial("unix", sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c == nil {
		t.Fatalf("net.Dial(unix) on Windows: %v", err)
	}
	defer c.Close()
	d := newWinDrainer(c)

	// per-client viewport を確立（RESIZE）→ サーバが catch-up 描画。
	if _, err := c.Write(winResizeFrame(rows, cols)); err != nil {
		t.Fatalf("client Write RESIZE: %v", err)
	}

	ok := false
	dl := time.Now().Add(6 * time.Second)
	for time.Now().Before(dl) {
		if strings.Contains(winFrameText(d.snapshot(), cols, rows), "M8C_OK") {
			ok = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("実 ConPTY 出力 M8C_OK が AF_UNIX 経由 client の描画に出ない。\nframe=%.200q",
			winFrameText(d.snapshot(), cols, rows))
	}
	t.Logf("ConPTY→Proxy/VT→server AF_UNIX listener→net.Dial(unix) client→"+
		"再パース描画 OK: M8C_OK 受信（Windows end-to-end）")
}
