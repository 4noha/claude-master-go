package web

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/4noha/claude-master-go/internal/cloud/relay"
	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/cloud/webauth"
	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/ptyproxy"
	"github.com/4noha/claude-master-go/internal/screen"
)

// 合成は使わない: 実 Firestore エミュレータ（実 API）＋実 relay＋実
// PtyProxy（実 resume-burst 録画）＋本番同型 mux で、コード認証→cookie
// →/api→/ws ブラウザ端末（display-oracle）まで end-to-end 検証。

const projectID = "demo-cm"

func java21Bin() string {
	for _, d := range []string{"/opt/homebrew/opt/openjdk/bin",
		"/opt/homebrew/opt/openjdk@25/bin", "/opt/homebrew/opt/openjdk@21/bin"} {
		if fi, err := os.Stat(d + "/java"); err == nil && !fi.IsDir() {
			out, _ := exec.Command(d+"/java", "-version").CombinedOutput()
			for _, v := range []string{"\"21", "\"22", "\"23", "\"24", "\"25", "\"26"} {
				if strings.Contains(string(out), v) {
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

func TestMain(m *testing.M) {
	jbin := java21Bin()
	if _, err := exec.LookPath("gcloud"); err != nil || jbin == "" {
		fmt.Println("SKIP: gcloud/Java21+ 不在")
		os.Exit(0)
	}
	host := fmt.Sprintf("127.0.0.1:%d", freePort())
	c := exec.Command("gcloud", "beta", "emulators", "firestore", "start",
		"--host-port="+host, "--quiet")
	c.Env = append(os.Environ(), "PATH="+jbin+":"+os.Getenv("PATH"),
		"CLOUDSDK_CORE_DISABLE_PROMPTS=1")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		fmt.Println("SKIP:", err)
		os.Exit(0)
	}
	ok := false
	for i := 0; i < 80; i++ {
		if r, e := http.Get("http://" + host + "/"); e == nil {
			r.Body.Close()
			ok = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ok {
		syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		fmt.Println("SKIP: emulator not ready")
		os.Exit(0)
	}
	os.Setenv("FIRESTORE_EMULATOR_HOST", host)
	code := m.Run()
	syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	os.Exit(code)
}

func fixtureBin(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "..", "..", "..",
		"test", "fixtures", "resume-burst", "bytes.bin")
}

// prodMux は cloud/relay/main.handler() と同型（/session→relay、他→web）。
func prodMux(rl *relay.Server, ws *Server) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/session", rl)
	mux.Handle("/", ws.Handler())
	return mux
}

func newSt(t *testing.T, pc string) *state.Client {
	t.Helper()
	c, err := state.New(context.Background(), projectID, pc)
	if err != nil {
		t.Fatalf("state.New: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestAuthCodeCookieAndAPIScope(t *testing.T) {
	st := newSt(t, "web1")
	rl := relay.NewServer()
	ws := New(rl, st, webauth.NewSigner("test-key"))
	ts := httptest.NewServer(prodMux(rl, ws))
	defer ts.Close()

	// pairing 発行（PC スコープ）
	code := "ABCD2345"
	if err := st.CreatePairing(context.Background(),
		webauth.HashCode(code), "PCX", "PCX", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	jar := newJar()
	cl := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	// 未認証 /api/pcs → 401
	if r, _ := cl.Get(ts.URL + "/api/pcs"); r == nil || r.StatusCode != 401 {
		t.Fatalf("未認証 /api/pcs が 401 でない: %v", r)
	}
	// 誤コード → 401
	r, _ := cl.PostForm(ts.URL+"/auth/code", url.Values{"code": {"WRONGXXX"}})
	if r.StatusCode != 401 {
		t.Fatalf("誤コードが 401 でない: %d", r.StatusCode)
	}
	// 正コード → 302 + cookie
	r, _ = cl.PostForm(ts.URL+"/auth/code", url.Values{"code": {code}})
	if r.StatusCode != 302 {
		t.Fatalf("正コードが 302 でない: %d", r.StatusCode)
	}
	// cookie で /api/pcs → スコープ PC
	r, _ = cl.Get(ts.URL + "/api/pcs")
	if r.StatusCode != 200 || !bodyHas(r, `"id":"PCX"`) {
		t.Fatalf("/api/pcs がスコープを返さない: %d", r.StatusCode)
	}
	// /api/sessions?pc=PCX → 200（空配列）
	r, _ = cl.Get(ts.URL + "/api/sessions?pc=PCX")
	if r.StatusCode != 200 {
		t.Fatalf("/api/sessions が 200 でない: %d", r.StatusCode)
	}
	// スコープ外 PC → 403
	r, _ = cl.Get(ts.URL + "/api/sessions?pc=OTHER")
	if r.StatusCode != 403 {
		t.Fatalf("スコープ外が 403 でない: %d", r.StatusCode)
	}
	// 一回消費: 同コード再投入は 401
	r, _ = cl.PostForm(ts.URL+"/auth/code", url.Values{"code": {code}})
	if r.StatusCode != 401 {
		t.Fatalf("消費済コードが再利用できた: %d", r.StatusCode)
	}
}

func TestWSViewerAuthGatedRealRecording(t *testing.T) {
	bin := fixtureBin(t)
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	st := newSt(t, "web2")
	rl := relay.NewServer()
	ws := New(rl, st, webauth.NewSigner("test-key2"))
	ts := httptest.NewServer(prodMux(rl, ws))
	defer ts.Close()
	base := "ws" + strings.TrimPrefix(ts.URL, "http")

	// 未認証 /ws → 401（cookie 必須）
	if _, _, err := websocket.Dial(context.Background(),
		base+"/ws?pc=PCY&sid=S", nil); err == nil {
		t.Fatal("未認証 /ws が拒否されない")
	}

	// pairing→cookie
	code := "WSAUTH22"
	st.CreatePairing(context.Background(), webauth.HashCode(code),
		"PCY", "PCY", 10*time.Minute)
	jar := newJar()
	cl := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	cl.PostForm(ts.URL+"/auth/code", url.Values{"code": {code}})
	var cookie string
	u, _ := url.Parse(ts.URL)
	for _, c := range jar.Cookies(u) {
		if c.Name == cookieName {
			cookie = c.Name + "=" + c.Value
		}
	}
	if cookie == "" {
		t.Fatal("cookie 取得失敗")
	}

	// 実 PtyProxy（実録画）を source bridge で relay へ
	p, err := ptyproxy.Start([]string{"/bin/sh", "-c", "cat " + bin + "; sleep 10"}, 164, 50)
	if err != nil {
		t.Fatal(err)
	}
	srv := ptyproxy.NewServer(p, &config.Config{SizePolicy: "client",
		NavKey: []byte{0x1c}, NavScrollStep: 1, NavPageStep: 10, NavWheelStep: 3}, nil, 0, 0)
	usock, _ := os.CreateTemp("/tmp", "cmw*.sock")
	uname := usock.Name()
	usock.Close()
	os.Remove(uname)
	if err := srv.Serve(uname); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Stop(); p.Close(); os.Remove(uname) })
	time.Sleep(300 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.BridgeSource(ctx, base, "S", uname) // 起こされた agent 相当

	// 認証済ブラウザ相当: cookie 付きで /ws viewer 接続
	vc, _, err := websocket.Dial(ctx, base+"/ws?pc=PCY&sid=S",
		&websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {cookie}}})
	if err != nil {
		t.Fatalf("認証済 /ws 接続失敗: %v", err)
	}
	viewer := websocket.NetConn(ctx, vc, websocket.MessageBinary)
	defer viewer.Close()

	var mu sync.Mutex
	var buf []byte
	go func() {
		b := make([]byte, 8192)
		for {
			n, e := viewer.Read(b)
			if n > 0 {
				mu.Lock()
				buf = append(buf, b[:n]...)
				mu.Unlock()
			}
			if e != nil {
				return
			}
		}
	}()
	rs := []byte{0xff, 0xff}
	var pp [4]byte
	binary.BigEndian.PutUint16(pp[0:2], 50)
	binary.BigEndian.PutUint16(pp[2:4], 164)
	viewer.Write(append(rs, pp[:]...))

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		s := string(append([]byte{}, buf...))
		mu.Unlock()
		fr := strings.Split(s, "\x1b[?2026h")
		if len(fr) >= 2 {
			v := screen.NewModel(164, 50)
			v.Feed([]byte(fr[len(fr)-1]))
			if strings.Contains(strings.Join(v.VisibleLines(), "\n"),
				"bypass permissions") {
				return // 認証済ブラウザ経路で実録画が描画された
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("認証済 /ws で実録画フレームが届かない（Web 経路 protocol 透過失敗）")
}

// --- helpers ---

func bodyHas(r *http.Response, sub string) bool {
	defer r.Body.Close()
	b := make([]byte, 4096)
	n, _ := r.Body.Read(b)
	return strings.Contains(string(b[:n]), sub)
}

type cookieJar struct {
	mu sync.Mutex
	cs map[string]*http.Cookie
}

func newJar() *cookieJar { return &cookieJar{cs: map[string]*http.Cookie{}} }
func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cs {
		j.cs[c.Name] = c
	}
}
func (j *cookieJar) Cookies(_ *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []*http.Cookie
	for _, c := range j.cs {
		out = append(out, c)
	}
	return out
}
