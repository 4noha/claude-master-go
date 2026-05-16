package ptyproxy

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/screen"
)

// 実 unix socket での多重 client 検証（Python test_multi_client 相当）。
// 子は録画を吐いた後 sleep（master EOF で server が落ちないように）。
// 合成でなく実 socket・実録画資産。

type drainer struct {
	mu  sync.Mutex
	buf []byte
}

func newDrainer(c net.Conn) *drainer {
	d := &drainer{}
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
func (d *drainer) snapshot() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(append([]byte{}, d.buf...))
}

func tmpSock(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("/tmp", "cms*.sock") // 短パス（AF_UNIX 104 制限）
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	t.Cleanup(func() { _ = os.Remove(name) })
	return name
}

func resize(rows, cols int) []byte {
	b := append([]byte{}, resizeMagic...)
	var p [4]byte
	binary.BigEndian.PutUint16(p[0:2], uint16(rows))
	binary.BigEndian.PutUint16(p[2:4], uint16(cols))
	return append(b, p[:]...)
}
func scroll(dy int) []byte {
	// 実 socket_client と同じく int16 にクランプして送る
	if dy < -32768 {
		dy = -32768
	}
	if dy > 32767 {
		dy = 32767
	}
	b := append([]byte{}, scrollMagic...)
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(int16(dy)))
	return append(b, p[:]...)
}

func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := fixtureDir(t)
	bin := filepath.Join(dir, "bytes.bin")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	// 録画を verbatim 吐出後 sleep（pty open のまま＝server 維持）
	p, err := Start([]string{"/bin/sh", "-c", "cat " + bin + "; sleep 5"}, 164, 50)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv := NewServer(p, nil, nil, 0, 0) // cfg 既定・host 出力なし
	sock := tmpSock(t)
	if err := srv.Serve(sock); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { srv.Stop(); p.Close() })
	return srv, sock
}

func dial(t *testing.T, sock string) net.Conn {
	t.Helper()
	var c net.Conn
	var err error
	for i := 0; i < 50; i++ {
		c, err = net.Dial("unix", sock)
		if err == nil {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial: %v", err)
	return nil
}

func waitContains(d *drainer, sub string, to time.Duration) bool {
	dl := time.Now().Add(to)
	for time.Now().Before(dl) {
		if strings.Contains(d.snapshot(), sub) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// frameText は受信バイト中の最後のフレームを VT で再パースし可視テキスト
// を返す（display-oracle 方式: 生バイトには SGR が挟まり部分文字列照合
// できないため、ユーザーが実際に見るテキストで判定）。
func frameText(snap string, cols, rows int) string {
	fr := strings.Split(snap, "\x1b[?2026h")
	if len(fr) < 2 {
		return ""
	}
	body := fr[len(fr)-1]
	v := screen.NewModel(cols, rows)
	v.Feed([]byte(body))
	return strings.Join(v.VisibleLines(), "\n")
}

// waitFrameText は最後のフレームの可視テキストに sub が現れるまで待つ。
func waitFrameText(d *drainer, sub string, cols, rows int, to time.Duration) bool {
	dl := time.Now().Add(to)
	for time.Now().Before(dl) {
		if strings.Contains(frameText(d.snapshot(), cols, rows), sub) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func TestMultiClientBroadcastAndResize(t *testing.T) {
	_, sock := startServer(t)
	time.Sleep(300 * time.Millisecond) // 録画 feed を待つ

	a := dial(t, sock)
	b := dial(t, sock)
	da, db := newDrainer(a), newDrainer(b)

	// 別サイズで RESIZE → 各自サイズで catch-up 描画されるはず
	_, _ = a.Write(resize(50, 164))
	_, _ = b.Write(resize(20, 100))

	// 録画の最終可視（フッター）が各 client サイズで届く（display-oracle）
	if !waitFrameText(da, "bypass permissions", 164, 50, 3*time.Second) {
		t.Fatalf("client A: 録画本文未受信(text): %.140q",
			frameText(da.snapshot(), 164, 50))
	}
	if !waitFrameText(db, "bypass permissions", 100, 20, 3*time.Second) {
		t.Fatalf("client B: 録画本文未受信(text): %.140q",
			frameText(db.snapshot(), 100, 20))
	}
	// 別サイズ＝フレーム内容（行幅）が異なる sanity
	if da.snapshot() == db.snapshot() {
		t.Fatalf("別サイズ client が同一フレーム（per-client 描画でない）")
	}
}

func TestClientScrollMagicPansHistory(t *testing.T) {
	_, sock := startServer(t)
	time.Sleep(300 * time.Millisecond)

	a := dial(t, sock)
	d := newDrainer(a)
	_, _ = a.Write(resize(50, 164))
	if !waitContains(d, "\x1b[?2026h", 3*time.Second) {
		t.Fatal("初期フレーム未受信")
	}
	// 大きく上へ scroll → 最古 history（起動バナー）が可視テキストに出る
	_, _ = a.Write(scroll(-100000))
	if !waitFrameText(d, "Claude Code v2.1.126", 164, 50, 3*time.Second) {
		t.Fatalf("SCROLL 後に最古 history が可視に出ない: %.200q",
			frameText(d.snapshot(), 164, 50))
	}
}
