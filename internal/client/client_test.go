//go:build !windows

// 実 ptyproxy.Start(/bin/sh 等)＋unix socket(/tmp)で nav/pagekey/wheel/
// 再接続を検証する unix 専用テスト。Windows の client 側検証は
// resize_windows_test.go（pollResize）＋ M8c 統合テスト。Mac/linux では
// 従来通り全実行＝parity 無影響（他環境クリーン）。
package client

import (
	"net"
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

// 合成は使わない: 実 unix socket・実 PtyProxy Server・実 `claude --resume`
// 録画。client の processStdin（実キーパス本体）→ SCROLL_MAGIC →
// server.parseClientInput → ScrollRenderer pan を、サーバ出力フレームを
// VT 再パースした display-oracle（ユーザーが実際に見るテキスト）で検証。

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "..", "..",
		"test", "fixtures", "resume-burst")
}

func cliCfg() *config.Config {
	return &config.Config{
		SizePolicy: "client", NavKey: []byte{0x1c},
		NavScrollStep: 1, NavPageStep: 10, NavWheelStep: 3,
		PageKeyScroll: true, WheelScroll: true,
	}
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

// frameText: 最後のフレームを VT 再パースし可視テキスト（display-oracle）。
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

func tmpSock(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("/tmp", "cmc*.sock")
	if err != nil {
		t.Fatal(err)
	}
	n := f.Name()
	_ = f.Close()
	_ = os.Remove(n)
	t.Cleanup(func() { _ = os.Remove(n) })
	return n
}

// 実 Server を録画再生付きで起動し socket を返す。
func startRealServer(t *testing.T) (string, *config.Config) {
	t.Helper()
	dir := fixtureDir(t)
	bin := filepath.Join(dir, "bytes.bin")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	p, err := ptyproxy.Start([]string{"/bin/sh", "-c", "cat " + bin + "; sleep 5"}, 164, 50)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv := ptyproxy.NewServer(p, cliCfg(), nil, 0, 0)
	sock := tmpSock(t)
	if err := srv.Serve(sock); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { srv.Stop(); p.Close() })
	return sock, cliCfg()
}

// 実 RESIZE フレーム（client.connW.sendResize と同一ワイヤ）を直接送る。
func resizeFrame(rows, cols int) []byte {
	b := append([]byte{}, resizeMagic...)
	return append(b, byte(uint16(rows)>>8), byte(uint16(rows)),
		byte(uint16(cols)>>8), byte(uint16(cols)))
}

// 本命: nav-mode ↑ で実サーバ履歴が遡れる（processStdin→SCROLL_MAGIC→
// server pan→フレーム）。実キーパス・実録画・display-oracle。
func TestClientNavScrollPansRealServer(t *testing.T) {
	sock, cfg := startRealServer(t)
	time.Sleep(300 * time.Millisecond) // 録画 feed

	conn, err := Connect(sock, true) // 実 Connect（retry 経路）
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	d := &drain{}
	d.run(conn)

	if _, err := conn.Write(resizeFrame(50, 164)); err != nil {
		t.Fatalf("resize 送信: %v", err)
	}
	if !waitText(d, "bypass permissions", 164, 50, 3*time.Second) {
		t.Fatalf("初期 live フレーム未達: %.120q",
			frameText(d.snap(), 164, 50))
	}

	w := &connW{c: conn}
	keys := scrollKeys(cfg)
	nav, pk := false, false

	// NAV_KEY で nav 突入 → ↑ を多数 → 最古 history が見えるはず
	if _, e := processStdin(w, cfg, keys, cfg.NavKey, cfg.NavKey, &nav, &pk); e != nil {
		t.Fatalf("nav enter: %v", e)
	}
	if !nav {
		t.Fatal("NAV_KEY で nav に入っていない")
	}
	for i := 0; i < 600; i++ {
		if _, e := processStdin(w, cfg, keys, cfg.NavKey,
			[]byte("\x1b[A"), &nav, &pk); e != nil {
			t.Fatalf("nav up: %v", e)
		}
	}
	if !waitText(d, "Claude Code v2.1.126", 164, 50, 3*time.Second) {
		t.Fatalf("nav ↑ で最古 history が出ない: %.200q",
			frameText(d.snap(), 164, 50))
	}

	// NAV_KEY 再投入 → followDy 送出 → live（footer）へ復帰
	if _, e := processStdin(w, cfg, keys, cfg.NavKey, cfg.NavKey, &nav, &pk); e != nil {
		t.Fatalf("nav exit: %v", e)
	}
	if nav {
		t.Fatal("NAV_KEY 再投入で nav を抜けていない")
	}
	if !waitText(d, "bypass permissions", 164, 50, 3*time.Second) {
		t.Fatalf("nav 解除で live 復帰しない: %.160q",
			frameText(d.snap(), 164, 50))
	}
}

// PAGEKEY / WHEEL も実サーバを pan し、実ユーザー操作で live 復帰。
func TestClientPagekeyWheelAndLiveResetRealServer(t *testing.T) {
	sock, cfg := startRealServer(t)
	time.Sleep(300 * time.Millisecond)
	conn, err := Connect(sock, true)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()
	d := &drain{}
	d.run(conn)
	_, _ = conn.Write(resizeFrame(50, 164))
	if !waitText(d, "bypass permissions", 164, 50, 3*time.Second) {
		t.Fatalf("初期フレーム未達")
	}

	w := &connW{c: conn}
	keys := scrollKeys(cfg)
	nav, pk := false, false

	// PageUp を連打（nav に入らず）→ 過去へ
	for i := 0; i < 120; i++ {
		if _, e := processStdin(w, cfg, keys, cfg.NavKey,
			[]byte("\x1b[5~"), &nav, &pk); e != nil {
			t.Fatalf("pageup: %v", e)
		}
	}
	if !pk {
		t.Fatal("PAGEKEY で pkScrolled が立っていない")
	}
	if !waitText(d, "Claude Code v2.1.126", 164, 50, 3*time.Second) {
		t.Fatalf("PageUp で最古 history が出ない: %.160q",
			frameText(d.snap(), 164, 50))
	}
	// 実ユーザー操作 'a'（IsLiveResetKey）→ followDy → live 復帰
	if _, e := processStdin(w, cfg, keys, cfg.NavKey,
		[]byte("a"), &nav, &pk); e != nil {
		t.Fatalf("live reset key: %v", e)
	}
	if pk {
		t.Fatal("実操作後も pkScrolled が残る")
	}
	if !waitText(d, "bypass permissions", 164, 50, 3*time.Second) {
		t.Fatalf("実操作で live 復帰しない: %.160q",
			frameText(d.snap(), 164, 50))
	}

	// ホイール上（SGR）→ また過去へ（WHEEL_SCROLL）
	for i := 0; i < 200; i++ {
		if _, e := processStdin(w, cfg, keys, cfg.NavKey,
			[]byte("\x1b[<64;10;5M"), &nav, &pk); e != nil {
			t.Fatalf("wheel up: %v", e)
		}
	}
	if !pk {
		t.Fatal("WHEEL で pkScrolled が立っていない")
	}
	if !waitText(d, "Claude Code v2.1.126", 164, 50, 3*time.Second) {
		t.Fatalf("ホイール上で最古 history が出ない: %.160q",
			frameText(d.snap(), 164, 50))
	}
}

func TestConnectRetryThenSucceedRealSocket(t *testing.T) {
	sock := tmpSock(t)
	// まだ listen していない → retry=false は即失敗
	if _, err := Connect(sock, false); err == nil {
		t.Fatal("未 listen で Connect 成功してしまった")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		c, e := ln.Accept()
		if e == nil {
			c.Close()
		}
	}()
	c, err := Connect(sock, true)
	if err != nil {
		t.Fatalf("listen 後 Connect 失敗: %v", err)
	}
	c.Close()
}

// nav-mode 中に実ユーザーキーを握り潰した時、banner を throttled 再表示
// する (「無反応＝壊れた」誤認→窓 kill 連鎖の根絶・2026-06-05 実害)。
// 握り潰し挙動自体 (claude へ送らない) は不変＝Python parity 維持。
func TestNavModeSwallowedKeyRemindsBannerThrottled(t *testing.T) {
	cfg := cliCfg()
	keys := scrollKeys(cfg)
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()
	go func() { b := make([]byte, 4096); for { if _, e := srv.Read(b); e != nil { return } } }() // 書込を吸う
	w := &connW{c: cli}

	var buf strings.Builder
	oldOut, oldLast, oldInt := navRemind.out, navRemind.last, navRemind.interval
	navRemind.out = &buf
	navRemind.last = time.Time{} // throttle 起点リセット
	navRemind.interval = 2 * time.Second
	defer func() {
		navRemind.out, navRemind.last, navRemind.interval = oldOut, oldLast, oldInt
	}()

	nav, pk := true, false
	// 1) 実ユーザーキー "x" 握り潰し → banner 出る
	if _, e := processStdin(w, cfg, keys, cfg.NavKey, []byte("x"), &nav, &pk); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(buf.String(), "NAV MODE ON") {
		t.Fatalf("swallowed key で banner が出ない: %q", buf.String())
	}
	first := buf.Len()

	// 2) 直後の連打 → throttle (2s) 内なので出ない
	if _, e := processStdin(w, cfg, keys, cfg.NavKey, []byte("y"), &nav, &pk); e != nil {
		t.Fatal(e)
	}
	if buf.Len() != first {
		t.Fatalf("throttle 内なのに再表示された: %d → %d", first, buf.Len())
	}

	// 3) throttle 経過後 → 再表示
	navRemind.mu.Lock()
	navRemind.last = time.Now().Add(-3 * time.Second)
	navRemind.mu.Unlock()
	if _, e := processStdin(w, cfg, keys, cfg.NavKey, []byte("z"), &nav, &pk); e != nil {
		t.Fatal(e)
	}
	if buf.Len() <= first {
		t.Fatal("throttle 経過後に再表示されない")
	}

	// 4) passive レポート (focus in \x1b[I) では出ない
	before := buf.Len()
	if _, e := processStdin(w, cfg, keys, cfg.NavKey, []byte("\x1b[I"), &nav, &pk); e != nil {
		t.Fatal(e)
	}
	if buf.Len() != before {
		t.Fatal("passive レポートで banner が出た (IsLiveResetKey 違反)")
	}

	// 5) スクロールキー (j) では出ない (視覚 feedback は pan 自体)
	navRemind.mu.Lock()
	navRemind.last = time.Time{}
	navRemind.mu.Unlock()
	before = buf.Len()
	if _, e := processStdin(w, cfg, keys, cfg.NavKey, []byte("j"), &nav, &pk); e != nil {
		t.Fatal(e)
	}
	if buf.Len() != before {
		t.Fatal("スクロールキーで banner が出た")
	}
}
