package relay

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/ptyproxy"
	"github.com/4noha/claude-master-go/internal/screen"
)

// 合成は使わない: 実 `claude --resume` 録画を実 PtyProxy に流し、
// viewer→WSS→relay→WSS→BridgeSource→unix→PtyProxy.Server の経路で
// 既存 RESIZE/SCROLL/frame protocol が **無改変のまま WSS 透過** する
// ことを、サーバ出力フレームの VT 再パース（display-oracle）で検証。

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "..", "..", "..",
		"test", "fixtures", "resume-burst")
}

func tmpSock(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("/tmp", "cmr*.sock")
	if err != nil {
		t.Fatal(err)
	}
	n := f.Name()
	f.Close()
	os.Remove(n)
	t.Cleanup(func() { os.Remove(n) })
	return n
}

func resizeFrame(rows, cols int) []byte {
	b := []byte{0xff, 0xff}
	var p [4]byte
	binary.BigEndian.PutUint16(p[0:2], uint16(rows))
	binary.BigEndian.PutUint16(p[2:4], uint16(cols))
	return append(b, p[:]...)
}
func scrollFrame(dy int) []byte {
	if dy < -32768 {
		dy = -32768
	}
	if dy > 32767 {
		dy = 32767
	}
	b := []byte{0xff, 0xfe}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(int16(dy)))
	return append(b, p[:]...)
}

type drain struct {
	mu  sync.Mutex
	buf []byte
}

func (d *drain) run(c net.Conn) {
	go func() {
		b := make([]byte, 8192)
		for {
			n, e := c.Read(b)
			if n > 0 {
				d.mu.Lock()
				d.buf = append(d.buf, b[:n]...)
				d.mu.Unlock()
			}
			if e != nil {
				return
			}
		}
	}()
}
func (d *drain) snap() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(append([]byte{}, d.buf...))
}

func frameText(snap string, cols, rows int) string {
	fr := strings.Split(snap, "\x1b[?2026h")
	if len(fr) < 2 {
		return ""
	}
	v := screen.NewModel(cols, rows)
	v.Feed([]byte(fr[len(fr)-1]))
	return strings.Join(v.VisibleLines(), "\n")
}

func waitText(d *drain, sub string, cols, rows int, to time.Duration) bool {
	dl := time.Now().Add(to)
	for time.Now().Before(dl) {
		if strings.Contains(frameText(d.snap(), cols, rows), sub) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func relayCfg() *config.Config {
	return &config.Config{
		SizePolicy: "client", NavKey: []byte{0x1c},
		NavScrollStep: 1, NavPageStep: 10, NavWheelStep: 3,
	}
}

func TestProtocolTunnelsOverWSSRealRecording(t *testing.T) {
	dir := fixtureDir(t)
	bin := filepath.Join(dir, "bytes.bin")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}

	// 実 relay（httptest）
	rl := NewServer()
	hs := httptest.NewServer(http.HandlerFunc(rl.ServeHTTP))
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http")

	// 実 PtyProxy に実録画を流す（M5c と同じ起動）
	p, err := ptyproxy.Start([]string{"/bin/sh", "-c", "cat " + bin + "; sleep 5"}, 164, 50)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv := ptyproxy.NewServer(p, relayCfg(), nil, 0, 0)
	usock := tmpSock(t)
	if err := srv.Serve(usock); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { srv.Stop(); p.Close() })
	time.Sleep(300 * time.Millisecond) // 録画 feed

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sid := "sess-A"
	// source ブリッジ: relay ⇄ ローカル unix socket
	go func() { _ = BridgeSource(ctx, wsURL, sid, usock) }()

	// viewer: relay へ WSS 接続（= socket_client 同等のバイト経路）
	var viewer net.Conn
	for i := 0; i < 50; i++ {
		viewer, err = Dial(ctx, wsURL, sid, "viewer")
		if err == nil {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("viewer Dial: %v", err)
	}
	defer viewer.Close()
	d := &drain{}
	d.run(viewer)

	if _, err := viewer.Write(resizeFrame(50, 164)); err != nil {
		t.Fatalf("resize 送信: %v", err)
	}
	if !waitText(d, "bypass permissions", 164, 50, 5*time.Second) {
		t.Fatalf("WSS 経由で初期 live フレームが届かない: %.140q",
			frameText(d.snap(), 164, 50))
	}
	// SCROLL_MAGIC を WSS で送る → relay 透過 → PtyProxy が history を pan
	if _, err := viewer.Write(scrollFrame(-100000)); err != nil {
		t.Fatalf("scroll 送信: %v", err)
	}
	if !waitText(d, "Claude Code v2.1.126", 164, 50, 5*time.Second) {
		t.Fatalf("WSS 経由 SCROLL で最古 history が出ない（protocol 透過失敗）: %.200q",
			frameText(d.snap(), 164, 50))
	}
}

func TestRelayPairsBySessionID(t *testing.T) {
	rl := NewServer()
	hs := httptest.NewServer(http.HandlerFunc(rl.ServeHTTP))
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http")
	ctx := context.Background()

	// 別 sid は混線しない（A の source は A の viewer のみへ）
	srcA, err := Dial(ctx, wsURL, "A", "source")
	if err != nil {
		t.Fatal(err)
	}
	defer srcA.Close()
	vwB, err := Dial(ctx, wsURL, "B", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer vwB.Close()
	vwA, err := Dial(ctx, wsURL, "A", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	defer vwA.Close()

	go srcA.Write([]byte("hello-A"))
	got := make([]byte, 16)
	_ = vwA.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := vwA.Read(got)
	if err != nil || string(got[:n]) != "hello-A" {
		t.Fatalf("同 sid の source→viewer 中継失敗: n=%d err=%v got=%q", n, err, got[:n])
	}
	// B viewer には A のデータが来ない
	_ = vwB.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, e := vwB.Read(got); e == nil && n > 0 {
		t.Fatalf("別 sid に混線: %q", got[:n])
	}
}

// 公開 /session は Grant 注入時に (sid,role) を検証し、false なら 403。
// 認証済 Web /ws は Accept 直叩きで ServeHTTP を通らない＝無影響。
func TestSessionRequiresGrant(t *testing.T) {
	rl := NewServer()
	called := ""
	rl.Grant = func(_ context.Context, sid, role string) bool {
		called = sid + ":" + role
		return false // 不許可
	}
	hs := httptest.NewServer(http.HandlerFunc(rl.ServeHTTP))
	defer hs.Close()
	r, err := http.Get(hs.URL + "/session?sid=secret-sid&role=viewer")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("grant false なのに 403 でない: %d", r.StatusCode)
	}
	if called != "secret-sid:viewer" {
		t.Fatalf("Grant に sid/role が渡っていない: %q", called)
	}
	// Grant 許可なら 403 にはならない（WS upgrade 失敗で別コードはOK）
	rl.Grant = func(_ context.Context, _, _ string) bool { return true }
	r2, err := http.Get(hs.URL + "/session?sid=s&role=source")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode == http.StatusForbidden {
		t.Fatal("grant true なのに 403")
	}
}
