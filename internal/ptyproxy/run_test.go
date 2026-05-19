//go:build !windows

package ptyproxy

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/screen"
)

// M5d-1: 実行可能 proxy（claude-wrap 置換＝cutover 中核）の統合検証。
// 合成は使わない: 実 `claude --resume` 録画を吐く子プロセスを実 PTY
// ラップし、実 unix socket の client と host stdout の双方へ per-client
// 描画されること、子終了で終了コードを返し sock を後始末することを、
// サーバ出力フレームを VT 再パースした display-oracle で機械検証する。

func runProxyCfg() *config.Config {
	return &config.Config{
		SizePolicy: "client", NavKey: []byte{0x1c},
		NavScrollStep: 1, NavPageStep: 10, NavWheelStep: 3,
	}
}

// hostBuf は HostOut（並行 broadcast に備え最小ロック）。
type hostBuf struct {
	mu sync.Mutex
	b  []byte
}

func (h *hostBuf) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.b = append(h.b, p...)
	return len(p), nil
}
func (h *hostBuf) text(cols, rows int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	snap := string(append([]byte{}, h.b...))
	fr := strings.Split(snap, "\x1b[?2026h")
	if len(fr) < 2 {
		return ""
	}
	v := screen.NewModel(cols, rows)
	v.Feed([]byte(fr[len(fr)-1]))
	return strings.Join(v.VisibleLines(), "\n")
}

func TestRunProxyHostAndSocketRealRecording(t *testing.T) {
	dir := fixtureDir(t)
	bin := filepath.Join(dir, "bytes.bin")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	host := &hostBuf{}
	pr, pw, _ := os.Pipe() // host stdin（書かない＝goroutine は blocked のまま）
	defer pw.Close()
	sock := tmpSock(t)

	codeCh := make(chan int, 1)
	go func() {
		code, err := RunProxy(ProxyOpts{
			Argv:    []string{"/bin/sh", "-c", "cat " + bin + "; sleep 1"},
			Cfg:     runProxyCfg(),
			HostIn:  pr,
			HostOut: host,
			SockPath: sock,
			WinSize: func() (int, int) { return 164, 50 },
		})
		if err != nil {
			t.Errorf("RunProxy err: %v", err)
		}
		codeCh <- code
	}()

	// socket が作られるまで待つ
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	a := dial(t, sock)
	d := newDrainer(a)
	_, _ = a.Write(resize(50, 164))

	// client（per-client 描画）に録画本文が届く
	if !waitFrameText(d, "bypass permissions", 164, 50, 3*time.Second) {
		t.Fatalf("client に録画本文が届かない: %.140q",
			frameText(d.snapshot(), 164, 50))
	}
	// host stdout（HostOut）にも同録画が per-host 描画される
	hostOK := false
	for d2 := time.Now().Add(3 * time.Second); time.Now().Before(d2); {
		if strings.Contains(host.text(164, 50), "bypass permissions") {
			hostOK = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !hostOK {
		t.Fatalf("host stdout に録画本文が描画されない: %.140q",
			host.text(164, 50))
	}

	// 子（sleep 1）が終わると RunProxy は終了し sock を後始末する
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("正常終了の子で exit code=%d", code)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("子終了後も RunProxy が返らない")
	}
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("終了後も socket ファイルが残っている（後始末漏れ）")
	}
}

func TestRunProxyReturnsChildExitCode(t *testing.T) {
	sock := tmpSock(t)
	code, err := RunProxy(ProxyOpts{
		Argv:     []string{"/bin/sh", "-c", "sleep 0.2; exit 7"},
		Cfg:      runProxyCfg(),
		HostOut:  &hostBuf{},
		SockPath: sock,
		WinSize:  func() (int, int) { return 80, 24 },
	})
	if err != nil {
		t.Fatalf("RunProxy err: %v", err)
	}
	if code != 7 {
		t.Fatalf("子 exit 7 を伝播していない: code=%d", code)
	}
	if _, e := os.Stat(sock); e == nil {
		t.Fatal("終了後も socket が残る")
	}
}
