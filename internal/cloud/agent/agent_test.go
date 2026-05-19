//go:build !manual && !windows

package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/relay"
	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/ptyproxy"
	"github.com/4noha/claude-master-go/internal/screen"
)

// 合成は使わない: 実 Firestore エミュレータ（実 API）＋ローカル WSS
// relay ＋実 PtyProxy（実 resume-burst 録画）で、wake→データ線 open→
// unix socket トンネル→ display-oracle 検証→静止で自動切断、を
// end-to-end 機械検証する。

const projectID = "demo-cm"

func java21Bin() string {
	for _, d := range []string{
		"/opt/homebrew/opt/openjdk/bin",
		"/opt/homebrew/opt/openjdk@25/bin",
		"/opt/homebrew/opt/openjdk@21/bin",
	} {
		j := d + "/java"
		if fi, err := os.Stat(j); err == nil && !fi.IsDir() {
			out, _ := exec.Command(j, "-version").CombinedOutput()
			s := string(out)
			for _, v := range []string{"\"21", "\"22", "\"23", "\"24", "\"25", "\"26"} {
				if strings.Contains(s, v) {
					return d
				}
			}
		}
	}
	return ""
}

func freePort() int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

var emuCmd *exec.Cmd

func TestMain(m *testing.M) {
	jbin := java21Bin()
	if _, err := exec.LookPath("gcloud"); err != nil || jbin == "" {
		fmt.Println("SKIP: gcloud / Java21+ 不在のため Firestore emulator 検証不可")
		os.Exit(0)
	}
	port := freePort()
	host := fmt.Sprintf("127.0.0.1:%d", port)
	emuCmd = exec.Command("gcloud", "beta", "emulators", "firestore", "start",
		"--host-port="+host, "--quiet")
	emuCmd.Env = append(os.Environ(),
		"PATH="+jbin+":"+os.Getenv("PATH"),
		"CLOUDSDK_CORE_DISABLE_PROMPTS=1")
	emuCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := emuCmd.Start(); err != nil {
		fmt.Println("SKIP: emulator 起動不可:", err)
		os.Exit(0)
	}
	ready := false
	for i := 0; i < 80; i++ {
		if c, err := http.Get("http://" + host + "/"); err == nil {
			c.Body.Close()
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		_ = syscall.Kill(-emuCmd.Process.Pid, syscall.SIGKILL)
		fmt.Println("SKIP: emulator が ready にならない")
		os.Exit(0)
	}
	os.Setenv("FIRESTORE_EMULATOR_HOST", host)
	code := m.Run()
	_ = syscall.Kill(-emuCmd.Process.Pid, syscall.SIGKILL)
	os.Exit(code)
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "..", "..", "..",
		"test", "fixtures", "resume-burst")
}

func tmpSock(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("/tmp", "cma*.sock")
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

func newState(t *testing.T, pc string) *state.Client {
	t.Helper()
	c, err := state.New(context.Background(), projectID, pc)
	if err != nil {
		t.Fatalf("state.New: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// wake→データ線 open→WSS トンネル→display-oracle→静止で自動切断 を
// end-to-end 検証。
func TestAgentWakeBridgeAndQuiescenceClose(t *testing.T) {
	dir := fixtureDir(t)
	bin := filepath.Join(dir, "bytes.bin")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}

	// 実 relay
	rl := relay.NewServer()
	hs := httptest.NewServer(http.HandlerFunc(rl.ServeHTTP))
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http")

	// 実 PtyProxy に実録画
	p, err := ptyproxy.Start([]string{"/bin/sh", "-c", "cat " + bin + "; sleep 30"}, 164, 50)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv := ptyproxy.NewServer(p, relayCfg(), nil, 0, 0)
	usock := tmpSock(t)
	if err := srv.Serve(usock); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { srv.Stop(); p.Close() })
	time.Sleep(300 * time.Millisecond)

	const pcID, sid = "src-pc", "S"
	closed := make(chan string, 4)
	ag := &Agent{
		St:          newState(t, pcID),
		RelayURL:    wsURL,
		ResolveSock: func(s string) (string, bool) { return usock, s == sid },
		IdleClose:   2 * time.Second,
		OnDataClosed: func(s string) { closed <- s },
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- ag.Run(ctx) }()
	time.Sleep(1500 * time.Millisecond) // WatchWake attach 待ち

	// viewer が relay へ先に接続（source 不在で pairing 待ち）
	vctx, vcancel := context.WithCancel(context.Background())
	defer vcancel()
	viewer, err := relay.Dial(vctx, wsURL, sid, "viewer")
	if err != nil {
		t.Fatalf("viewer Dial: %v", err)
	}
	d := &drain{}
	d.run(viewer)

	// 別クライアント（Cloud Functions 相当）が wake を書く
	cf := newState(t, "cf")
	if err := cf.Wake(ctx, pcID, sid); err != nil {
		t.Fatalf("Wake: %v", err)
	}

	// wake→agent→BridgeSourceIdle→relay pairing 後、フレームが届く
	if _, err := viewer.Write(resizeFrame(50, 164)); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if !waitText(d, "bypass permissions", 164, 50, 8*time.Second) {
		t.Fatalf("wake 後にデータ線でフレームが届かない: %.140q",
			frameText(d.snap(), 164, 50))
	}
	if _, err := viewer.Write(scrollFrame(-100000)); err != nil {
		t.Fatalf("scroll: %v", err)
	}
	if !waitText(d, "Claude Code v2.1.126", 164, 50, 5*time.Second) {
		t.Fatalf("WSS トンネルで SCROLL が透過しない: %.200q",
			frameText(d.snap(), 164, 50))
	}

	// 以降 viewer は沈黙 → 録画も静止 → IdleClose でデータ線が閉じる
	select {
	case s := <-closed:
		if s != sid {
			t.Fatalf("閉じた sid 不一致: %q", s)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("静止後もデータ線が閉じない（quiescence 切断不成立）")
	}

	// ctx cancel で制御線（WatchWake）もクリーンに戻る
	cancel()
	select {
	case e := <-runErr:
		if e != nil {
			t.Fatalf("Run が error 終了: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctx cancel しても Run が戻らない")
	}
}

// この PC に無い sid の wake はデータ線を開かない（ResolveSock=false）。
func TestAgentIgnoresUnknownSession(t *testing.T) {
	rl := relay.NewServer()
	hs := httptest.NewServer(http.HandlerFunc(rl.ServeHTTP))
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http")

	const pcID = "src-pc2"
	closed := make(chan string, 2)
	ag := &Agent{
		St:           newState(t, pcID),
		RelayURL:     wsURL,
		ResolveSock:  func(string) (string, bool) { return "", false },
		IdleClose:    time.Second,
		OnDataClosed: func(s string) { closed <- s },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ag.Run(ctx)
	time.Sleep(1500 * time.Millisecond)

	cf := newState(t, "cf2")
	if err := cf.Wake(ctx, pcID, "ghost"); err != nil {
		t.Fatal(err)
	}
	// handleWake は走るが ResolveSock=false で即 return（OnDataClosed は
	// defer で呼ばれる＝データ線は開かなかった）
	select {
	case s := <-closed:
		if s != "ghost" {
			t.Fatalf("sid 不一致: %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未知 sid の handleWake が完了しない")
	}
}
