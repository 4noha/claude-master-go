//go:build !windows

package web

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
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
// PtyProxy（実 resume-burst 録画）＋本番同型 mux で検証。Google ID
// トークン検証は本番では idtoken（実 Google 公開鍵）で、ユニットでは
// 認可ロジックを fake verifier で決定的に検証し、実 Google サインインは
// chrome-devtools 実ブラウザ e2e（M7f）で担保（合成 green にしない）。

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

const allowEmail = "ok@example.com"

// fakeGV は Google ID トークン検証の差し替え（認可ロジックを決定的に）。
type fakeGV struct {
	email    string
	verified bool
	err      error
}

func (f fakeGV) Verify(_ context.Context, _, _ string) (string, bool, error) {
	return f.email, f.verified, f.err
}

func newWeb(t *testing.T, st *state.Client, gv webauth.GoogleVerifier) (*Server, *httptest.Server) {
	t.Helper()
	rl := relay.NewServer()
	ws := New(rl, st, webauth.NewSigner("test-key"), "test-cid", allowEmail, gv,
		"demo-cm", `{"type":"service_account","fake":true}`)
	ts := httptest.NewServer(prodMux(rl, ws))
	t.Cleanup(ts.Close)
	return ws, ts
}

// authCookie は Google 認証済（許可メール・scope=全 PC）の署名 cookie。
func authCookie(ws *Server) string {
	tok := ws.signer.Sign(webauth.Token{
		PC: allowEmail, Scope: accountScope,
		Exp: time.Now().Add(time.Hour).Unix(),
	})
	return cookieName + "=" + tok
}

func noRedir() *http.Client {
	return &http.Client{Jar: newJar(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
}

// Google 認証ゲート（fake verifier・CSRF 二重送信）を決定的に検証。
func TestGoogleAuthGate(t *testing.T) {
	st := newSt(t, "gauth")
	ws, ts := newWeb(t, st, fakeGV{email: allowEmail, verified: true})

	// /login は GIS（client_id 埋め込み）
	r, _ := http.Get(ts.URL + "/login")
	lb := bodyStr(r)
	if !strings.Contains(lb, `data-client_id="test-cid"`) ||
		!strings.Contains(lb, "accounts.google.com/gsi/client") {
		t.Fatalf("/login が Google サインインでない")
	}

	post := func(gv webauth.GoogleVerifier, csrfCookie, csrfBody, cred string) *http.Response {
		ws.gv = gv
		cl := noRedir()
		u, _ := url.Parse(ts.URL)
		if csrfCookie != "" {
			cl.Jar.SetCookies(u, []*http.Cookie{{Name: "g_csrf_token", Value: csrfCookie}})
		}
		form := url.Values{}
		if csrfBody != "" {
			form.Set("g_csrf_token", csrfBody)
		}
		form.Set("credential", cred)
		r, _ := cl.PostForm(ts.URL+"/auth/google", form)
		return r
	}

	// CSRF 欠落 → 403
	if r := post(fakeGV{email: allowEmail, verified: true}, "", "", "jwt"); r.StatusCode != 403 {
		t.Fatalf("CSRF 欠落が 403 でない: %d", r.StatusCode)
	}
	// CSRF 不一致 → 403
	if r := post(fakeGV{email: allowEmail, verified: true}, "A", "B", "jwt"); r.StatusCode != 403 {
		t.Fatalf("CSRF 不一致が 403 でない: %d", r.StatusCode)
	}
	// 検証エラー → 401
	if r := post(fakeGV{err: fmt.Errorf("bad")}, "T", "T", "jwt"); r.StatusCode != 401 {
		t.Fatalf("検証エラーが 401 でない: %d", r.StatusCode)
	}
	// email 未確認 → 401
	if r := post(fakeGV{email: allowEmail, verified: false}, "T", "T", "jwt"); r.StatusCode != 401 {
		t.Fatalf("未確認 email が 401 でない: %d", r.StatusCode)
	}
	// 許可外メール → 403
	if r := post(fakeGV{email: "evil@example.com", verified: true}, "T", "T", "jwt"); r.StatusCode != 403 {
		t.Fatalf("許可外メールが 403 でない: %d", r.StatusCode)
	}
	// 許可メール → 302 + cm_session cookie
	r = post(fakeGV{email: allowEmail, verified: true}, "T", "T", "jwt")
	if r.StatusCode != 302 {
		t.Fatalf("許可メールが 302 でない: %d", r.StatusCode)
	}
	var got string
	for _, c := range r.Cookies() {
		if c.Name == cookieName {
			got = c.Value
		}
	}
	if got == "" {
		t.Fatal("cm_session cookie が発行されない")
	}
	if tk, ok := ws.signer.Verify(got); !ok || tk.Scope != accountScope {
		t.Fatalf("発行 cookie が不正: %+v ok=%v", tk, ok)
	}
}

// Phase1: 実 Firestore に載った cm_version が /api 経由で出て、
// /api/version が目標版を返し、devices.js にバッジ/診断ロジックが
// 入っていることを実 API + 本番同型 mux で確認（合成なし。GitHub は
// latestTag seam で出さない）。
func TestVersionBadgeAndDiagnostics(t *testing.T) {
	ctx := context.Background()
	st := newSt(t, "verpc")
	ws, ts := newWeb(t, st, fakeGV{})
	ws.latestTag = func() (string, error) { return "v9.9.9", nil } // seam
	ck := authCookie(ws)
	do := func(path string) string {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set("Cookie", ck)
		r, _ := noRedir().Do(req)
		return bodyStr(r)
	}

	if err := st.RegisterPCVersion(ctx, "v9.9.9"); err != nil {
		t.Fatalf("RegisterPCVersion: %v", err)
	}
	st.PushStatus(ctx, []map[string]any{
		{"key": "s1", "session_id": "s1", "short_dir": "d1", "is_active": true,
			"pid": float64(1), "cwd": "/a", "start_time": "x",
			"cpu_percent": float64(0), "mem_mb": float64(0),
			"cm_version": "v0.0.1"}, // 旧 inode 相当（目標と不一致＝🔴）
	})

	if v := do("/api/version"); !strings.Contains(v, `"target":"v9.9.9"`) {
		t.Fatalf("/api/version 目標版が想定外: %s", v)
	}
	if d := do("/api/devices"); !strings.Contains(d, `"cm_version":"v9.9.9"`) {
		t.Fatalf("/api/devices に PC 版が出ない: %s", d)
	}
	if s := do("/api/sessions?pc=verpc"); !strings.Contains(s, `"cm_version":"v0.0.1"`) {
		t.Fatalf("/api/sessions に proxy 版が出ない: %s", s)
	}
	js := do("/static/devices.js")
	for _, want := range []string{"statusDot", "/api/version", "diagPre", "vbad"} {
		if !strings.Contains(js, want) {
			t.Fatalf("devices.js に %q が無い（バッジ/診断未配線）", want)
		}
	}
}

// Phase2: 遠隔命令 API。owner(cookie)のみ POST 可・GET 不可・不正
// コマンド拒否・未認証 401・監査に requested_by。実 Firestore＋本番
// mux（合成なし）。
func TestRemoteCommandOwnerOnly(t *testing.T) {
	st := newSt(t, "cmdweb")
	ws, ts := newWeb(t, st, fakeGV{})
	ck := authCookie(ws)
	form := func(cmd string) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+"/api/command",
			strings.NewReader("pc=cmdweb&cmd="+cmd))
		req.Header.Set("Cookie", ck)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r, _ := noRedir().Do(req)
		return r
	}
	// 未認証 POST → 401（apiGuard）
	r, _ := http.Post(ts.URL+"/api/command", "application/x-www-form-urlencoded",
		strings.NewReader("pc=cmdweb&cmd=restart-agent"))
	if r.StatusCode != 401 {
		t.Fatalf("未認証 /api/command が 401 でない: %d", r.StatusCode)
	}
	// GET 不可（破壊的＝POST 限定）
	greq, _ := http.NewRequest("GET", ts.URL+"/api/command?pc=cmdweb&cmd=restart-agent", nil)
	greq.Header.Set("Cookie", ck)
	if g, _ := noRedir().Do(greq); g.StatusCode != 405 {
		t.Fatalf("GET /api/command が 405 でない: %d", g.StatusCode)
	}
	// 不正コマンド → 400
	if br := form("rm-rf"); br.StatusCode != 400 {
		t.Fatalf("不正コマンドが 400 でない: %d", br.StatusCode)
	}
	// 正常: owner POST → 200 + id
	ok := form("restart-agent")
	if ok.StatusCode != 200 || !strings.Contains(bodyStr(ok), `"ok":true`) {
		t.Fatalf("owner restart-agent 投入が 200/ok でない: %d", ok.StatusCode)
	}
	// 監査: requested_by に login email、status pending（agent 未起動）
	areq, _ := http.NewRequest("GET", ts.URL+"/api/commands?pc=cmdweb", nil)
	areq.Header.Set("Cookie", ck)
	ar, _ := noRedir().Do(areq)
	ab := bodyStr(ar)
	if !strings.Contains(ab, `"requested_by":"`+allowEmail+`"`) ||
		!strings.Contains(ab, `"cmd":"restart-agent"`) {
		t.Fatalf("命令監査が想定外: %s", ab)
	}
	// devices.js に投入ロジックがある
	jq, _ := http.NewRequest("GET", ts.URL+"/static/devices.js", nil)
	jq.Header.Set("Cookie", ck)
	jr, _ := noRedir().Do(jq)
	js := bodyStr(jr)
	for _, want := range []string{"postCmd", "/api/command", "restart-agent",
		"self-update", "restart-proxy"} {
		if !strings.Contains(js, want) {
			t.Fatalf("devices.js に %q が無い（命令 UI 未配線）", want)
		}
	}
}

// 同期更新シム（DECSET 2026 ＝ web 側ダブルバッファ）が配信・配線
// されていることを本番 mux で確認（バイト挙動の決定論検証は
// `node internal/cloud/web/sync_test.mjs`＝実出荷 sync.js を実行）。
func TestSyncShimWiredAndServed(t *testing.T) {
	st := newSt(t, "syncpc")
	ws, ts := newWeb(t, st, fakeGV{})
	ck := authCookie(ws)
	get := func(p string) string {
		req, _ := http.NewRequest("GET", ts.URL+p, nil)
		req.Header.Set("Cookie", ck)
		r, _ := noRedir().Do(req)
		return bodyStr(r)
	}
	if js := get("/static/sync.js"); !strings.Contains(js, "cmMakeSyncFilter") ||
		!strings.Contains(js, "2026") {
		t.Fatalf("/static/sync.js が同期シムでない")
	}
	if tj := get("/static/term.js"); !strings.Contains(tj, "cmMakeSyncFilter") {
		t.Fatal("term.js が同期シムを使っていない（ws.onmessage 直 write のまま）")
	}
	if h := get("/term?pc=syncpc&sid=s1"); !strings.Contains(h, "/static/sync.js") {
		t.Fatal("/term HTML が sync.js を読み込んでいない（xterm.js→sync.js→term.js 順）")
	}
}

func TestAPIScopeStaticWithGoogleCookie(t *testing.T) {
	st := newSt(t, "web1")
	ws, ts := newWeb(t, st, fakeGV{})
	ck := authCookie(ws)
	do := func(path string) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set("Cookie", ck)
		c := noRedir()
		r, _ := c.Do(req)
		return r
	}
	// 未認証ガード
	if r, _ := http.Get(ts.URL + "/api/devices"); r.StatusCode != 401 {
		t.Fatalf("未認証 /api/devices が 401 でない: %d", r.StatusCode)
	}
	if r, _ := noRedir().Get(ts.URL + "/"); r.StatusCode != 302 {
		t.Fatalf("未認証 / が /login へ誘導しない: %d", r.StatusCode)
	}
	// 実 Firestore に 2 セッション（稼働 1）
	st.PushStatus(context.Background(), []map[string]any{
		{"key": "s1", "session_id": "s1", "short_dir": "d1", "is_active": true,
			"pid": float64(1), "cwd": "/a", "start_time": "x",
			"cpu_percent": float64(0), "mem_mb": float64(0)},
		{"key": "s2", "session_id": "s2", "short_dir": "d2", "is_active": false,
			"pid": float64(2), "cwd": "/b", "start_time": "y",
			"cpu_percent": float64(0), "mem_mb": float64(0)},
	})
	// scope="*" の /api/devices は全 PC（ここでは web1）を集計
	db := bodyStr(do("/api/devices"))
	if !strings.Contains(db, `"id":"web1"`) ||
		!strings.Contains(db, `"sessions":2`) ||
		!strings.Contains(db, `"active":1`) {
		t.Fatalf("/api/devices 集計が想定外: %s", db)
	}
	// /api/sessions?pc=web1 → 200、pc 無し（scope=*）→ 403
	if r := do("/api/sessions?pc=web1"); r.StatusCode != 200 {
		t.Fatalf("/api/sessions?pc=web1 が 200 でない: %d", r.StatusCode)
	}
	if r := do("/api/sessions"); r.StatusCode != 403 {
		t.Fatalf("scope=* で pc 無し /api/sessions が 403 でない: %d", r.StatusCode)
	}
	// / は端末一覧、/term は xterm
	if !strings.Contains(bodyStr(do("/")), "/static/devices.js") {
		t.Fatal("認証後 / が端末一覧でない")
	}
	tb := bodyStr(do("/term?pc=web1&sid=s1"))
	if !strings.Contains(tb, "/static/xterm.js") || !strings.Contains(tb, "/static/term.js") {
		t.Fatal("/term が xterm ターミナルでない")
	}
	// /term に前後コンソール切替ボタンがある
	if !strings.Contains(tb, `id="prev"`) || !strings.Contains(tb, `id="next"`) {
		t.Fatal("/term にコンソール切替ボタン（prev/next）が無い")
	}
	// モバイル画像取り込み: 📷 ボタン＋写真ピッカー input
	if !strings.Contains(tb, `id="img"`) || !strings.Contains(tb, `id="imgfile"`) ||
		!strings.Contains(tb, `accept="image/*"`) {
		t.Fatal("/term に画像ボタン/ファイル入力が無い（モバイル経路欠落）")
	}
	// 固定論理サイズ＋native スクロール設計の CSS。pull-to-refresh は
	// document 固定＋overscroll-behavior:none、#term-host は
	// overscroll-behavior:contain でスクロール連鎖も断つ。native
	// スクロール/ズームのため touch-action は pan-x pan-y pinch-zoom。
	for _, css := range []string{"overscroll-behavior:none",
		"overscroll-behavior:contain", "position:fixed",
		"touch-action:pan-x pan-y pinch-zoom"} {
		if !strings.Contains(tb, css) {
			t.Fatalf("/term に固定サイズ＋native スクロール CSS %q が無い", css)
		}
	}
	// 静的アセット（dir 表示・固定論理サイズの存在を検証）
	for _, a := range []struct{ p, want string }{
		{"/static/xterm.css", ".xterm"},
		{"/static/term.js", "WebSocket"},
		{"/static/term.js", `qs.get("dir")`},  // ディレクトリ名表示
		{"/static/term.js", "scrollback: 0"},  // xterm 自前スクロール無効
		{"/static/term.js", "WEB_COLS"},       // 固定桁
		{"/static/term.js", "WEB_ROWS"},       // 固定行（背高グリッド）
		{"/static/term.js", "0xfd"},           // 画像貼付 IMAGE フレーム
		{"/static/term.js", `"paste"`},        // デスクトップ paste 捕捉
		{"/static/term.js", "sendImageBlob"},  // 画像送信共通化
		{"/static/term.js", "clipboard.read"}, // モバイル: Clipboard API
		{"/static/term.js", "imgfile"},        // モバイル: 写真ピッカー fallback
		{"/static/term.js", "scrollHeight"},   // 読込時スクロール計算
		{"/static/term.js", "cursorY"},        // ライブ行へ着地（空白回避）
		{"/static/devices.js", "/term?pc="},
		{"/static/devices.js", "&dir="},       // 一覧→term へ dir 受渡し
		{"/static/devices.js", "/api/pc/delete"}, // ペアリング削除呼出
		{"/static/devices.js", "\"削除\""},   // 削除ボタン（旧:ペアリング削除）
	} {
		if !strings.Contains(bodyStr(do(a.p)), a.want) {
			t.Fatalf("%s の内容が想定外（%q 無し）", a.p, a.want)
		}
	}
	tj := bodyStr(do("/static/term.js"))
	// RESIZE は固定論理サイズを接続時 1 回（resizeFrame(WEB_ROWS,
	// WEB_COLS)）。
	if !strings.Contains(tj, "resizeFrame(WEB_ROWS, WEB_COLS)") {
		t.Fatal("term.js に固定論理サイズ RESIZE 送出が無い")
	}
	// 暴走の原因＝「自分の寸法を測って RESIZE 逆流」「SCROLL 変換」を
	// 構造的に排除したことを機械検証（これらが復活したら回帰）。
	for _, banned := range []string{"scrollFrame(", "new FitAddon",
		"loadAddon(", `addEventListener("wheel"`,
		`addEventListener("resize"`, `"touchmove"`} {
		if strings.Contains(tj, banned) {
			t.Fatalf("term.js に暴走要因 %q が残存（固定サイズ＋native スクロール設計に反する）", banned)
		}
	}
	if n := bodyLen(do("/static/xterm.js")); n < 100000 {
		t.Fatalf("/static/xterm.js が実体でない（%d bytes）", n)
	}
}

func TestWSViewerWithGoogleCookieRealRecording(t *testing.T) {
	bin := fixtureBin(t)
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	st := newSt(t, "web2")
	ws, ts := newWeb(t, st, fakeGV{})
	base := "ws" + strings.TrimPrefix(ts.URL, "http")

	// 未認証 /ws → 拒否
	if _, _, err := websocket.Dial(context.Background(),
		base+"/ws?pc=PCY&sid=S", nil); err == nil {
		t.Fatal("未認証 /ws が拒否されない")
	}
	ck := authCookie(ws) // Google 認証済 cookie（scope=*）

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
	go relay.BridgeSource(ctx, base, "S", uname)

	vc, _, err := websocket.Dial(ctx, base+"/ws?pc=PCY&sid=S",
		&websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {ck}}})
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
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("認証済 /ws で実録画フレームが届かない（Web 経路 protocol 透過失敗）")
}

// --- helpers ---

func bodyHas(r *http.Response, sub string) bool {
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return strings.Contains(string(b), sub)
}

func bodyLen(r *http.Response) int {
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return len(b)
}

func bodyStr(r *http.Response) string {
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
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

func TestEnrollFlowRealFirestore(t *testing.T) {
	st := newSt(t, "enr")
	ws, ts := newWeb(t, st, fakeGV{})
	ck := authCookie(ws)

	// 未認証 /api/enroll → 401
	if r, _ := http.Post(ts.URL+"/api/enroll", "", nil); r.StatusCode != 401 {
		t.Fatalf("未認証 /api/enroll が 401 でない: %d", r.StatusCode)
	}
	// cookie 付き /api/enroll → code+command
	req, _ := http.NewRequest("POST", ts.URL+"/api/enroll", nil)
	req.Header.Set("Cookie", ck)
	r, err := http.DefaultClient.Do(req)
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("/api/enroll が 200 でない: %v %v", err, r)
	}
	var iss struct{ Code, Command string }
	body := bodyStr(r)
	_ = json.Unmarshal([]byte(body), &iss)
	if iss.Code == "" || !strings.Contains(iss.Command, "cloud enroll "+iss.Code) ||
		!strings.Contains(iss.Command, "--relay ws") {
		t.Fatalf("/api/enroll 応答が想定外: %s", body)
	}

	// 誤コードで /enroll → 401
	if r := postForm(ts.URL+"/enroll", "code", "BADCODE99"); r.StatusCode != 401 {
		t.Fatalf("誤 enroll コードが 401 でない: %d", r.StatusCode)
	}
	// 正コードで /enroll → bootstrap（project/relay/sa_json）
	r = postForm(ts.URL+"/enroll", "code", iss.Code)
	if r.StatusCode != 200 {
		t.Fatalf("正 enroll が 200 でない: %d", r.StatusCode)
	}
	eb := bodyStr(r)
	if !strings.Contains(eb, `"gcp_project":"demo-cm"`) ||
		!strings.Contains(eb, `"relay_url":"ws`) ||
		!strings.Contains(eb, `"sa_json"`) {
		t.Fatalf("/enroll bootstrap が想定外: %s", eb)
	}
	// 一回消費: 同コード再交換は 401
	if r := postForm(ts.URL+"/enroll", "code", iss.Code); r.StatusCode != 401 {
		t.Fatalf("消費済 enroll コードが再利用できた: %d", r.StatusCode)
	}
}

func postForm(u, k, v string) *http.Response {
	r, _ := http.PostForm(u, url.Values{k: {v}})
	return r
}

// 端末ペアリング削除 API: cookie 必須・POST 限定・スコープ検証で
// pcs/{pc}＋sessions＋wake を削除し一覧から消す。
func TestApiDeletePC(t *testing.T) {
	st := newSt(t, "web1")
	ws, ts := newWeb(t, st, fakeGV{})
	ck := authCookie(ws)
	if _, err := st.PushStatus(context.Background(), []map[string]any{
		{"key": "s1", "session_id": "s1", "short_dir": "d1",
			"is_active": true, "pid": float64(1), "cwd": "/a",
			"start_time": "x", "cpu_percent": float64(0),
			"mem_mb": float64(0)}}); err != nil {
		t.Fatalf("PushStatus: %v", err)
	}
	post := func(path string, withCk bool) *http.Response {
		req, _ := http.NewRequest("POST", ts.URL+path, nil)
		if withCk {
			req.Header.Set("Cookie", ck)
		}
		r, _ := noRedir().Do(req)
		return r
	}
	// 未認証 → 401
	if r := post("/api/pc/delete?pc=web1", false); r.StatusCode != 401 {
		t.Fatalf("未認証 delete が 401 でない: %d", r.StatusCode)
	}
	// GET（cookie 有）→ 405（POST 限定）
	req, _ := http.NewRequest("GET", ts.URL+"/api/pc/delete?pc=web1", nil)
	req.Header.Set("Cookie", ck)
	if r, _ := noRedir().Do(req); r.StatusCode != 405 {
		t.Fatalf("GET delete が 405 でない: %d", r.StatusCode)
	}
	// pc 無し → 403
	if r := post("/api/pc/delete", true); r.StatusCode != 403 {
		t.Fatalf("pc 無し delete が 403 でない: %d", r.StatusCode)
	}
	// 正常: POST + cookie → 200、Firestore から消える
	if r := post("/api/pc/delete?pc=web1", true); r.StatusCode != 200 {
		t.Fatalf("認証済 delete が 200 でない: %d", r.StatusCode)
	}
	pcs, _ := st.ListPCs(context.Background())
	for _, p := range pcs {
		if p == "web1" {
			t.Fatalf("削除後も web1 が一覧に残存: %v", pcs)
		}
	}
	// 単なる削除でなく **強制失効** が立つ（生きた agent が再登録
	// しても relay が拒否＝復活しない）。
	if !st.IsRevoked(context.Background(), "web1") {
		t.Fatal("delete 後に web1 が revoked になっていない（復活防止不成立）")
	}
}
