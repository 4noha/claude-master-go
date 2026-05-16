package state

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// 合成は使わない: 実 Firestore API（gcloud Cloud Firestore エミュレータ）
// を TestMain で起動し、PushStatus の version 据置/増分（差分判定の土台）
// と WatchWake の real-time wake 受信（常時・PC 発・NAT 越えの制御線）を
// 決定的に検証する。Java 21+ / gcloud emulator が無い環境のみ skip。

const projectID = "demo-cm"

// java21Bin は Java 21+ の bin ディレクトリ（Firestore emulator 要件）。
func java21Bin() string {
	cands := []string{
		"/opt/homebrew/opt/openjdk/bin",
		"/opt/homebrew/opt/openjdk@25/bin",
		"/opt/homebrew/opt/openjdk@21/bin",
	}
	for _, d := range cands {
		j := d + "/java"
		if fi, err := os.Stat(j); err == nil && !fi.IsDir() {
			out, _ := exec.Command(j, "-version").CombinedOutput()
			// "openjdk version \"NN" の NN>=21 を雑に判定
			s := string(out)
			for _, v := range []string{"\"21", "\"22", "\"23", "\"24", "\"25", "\"26"} {
				if contains(s, v) {
					return d
				}
			}
		}
	}
	return ""
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
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
	for i := 0; i < 80; i++ { // 最大 40s
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

func newClient(t *testing.T, pc string) *Client {
	t.Helper()
	c, err := New(context.Background(), projectID, pc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// 実 STATUS スキーマ（monitor._write_status と同形・json 復号で float64）。
func realSession(key string, cpu float64, active bool) map[string]any {
	return map[string]any{
		"key": key, "session_id": key, "pid": float64(4242),
		"short_dir": "claude-master-go", "cwd": "/Users/x/works/claude-master-go",
		"start_time": "05-16 20:00", "cpu_percent": cpu,
		"mem_mb": float64(0), "is_active": active,
	}
}

func TestPushStatusVersioning(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, "pc-ver")
	s := realSession("sid-1", 3.2, true)

	if ch, err := c.PushStatus(ctx, []map[string]any{s}); err != nil || ch != 1 {
		t.Fatalf("初回 push changed=1 のはず: ch=%d err=%v", ch, err)
	}
	// 同一内容 → version 据置・changed 0
	if ch, err := c.PushStatus(ctx, []map[string]any{realSession("sid-1", 3.2, true)}); err != nil || ch != 0 {
		t.Fatalf("無差分なのに changed=%d err=%v", ch, err)
	}
	snap, err := c.fs.Collection("pcs").Doc("pc-ver").
		Collection("sessions").Doc("sid-1").Get(ctx)
	if err != nil {
		t.Fatalf("doc 取得: %v", err)
	}
	d := snap.Data()
	if v, _ := d["version"].(int64); v != 1 {
		t.Fatalf("無差分で version が動いた: %v", d["version"])
	}
	if d["content_hash"] == nil || d["short_dir"] != "claude-master-go" {
		t.Fatalf("実スキーマが忠実に保存されていない: %v", d)
	}
	// 内容変化 → version++ ・ changed 1
	if ch, err := c.PushStatus(ctx, []map[string]any{realSession("sid-1", 9.9, false)}); err != nil || ch != 1 {
		t.Fatalf("差分ありなのに changed=%d err=%v", ch, err)
	}
	snap, _ = c.fs.Collection("pcs").Doc("pc-ver").
		Collection("sessions").Doc("sid-1").Get(ctx)
	if v, _ := snap.Data()["version"].(int64); v != 2 {
		t.Fatalf("差分で version が ++ されない: %v", snap.Data()["version"])
	}
}

func TestWatchWakeReceivesRealtimePush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pc := "pc-wake"
	cw := newClient(t, pc)

	got := make(chan string, 8)
	wErr := make(chan error, 1)
	go func() { wErr <- cw.WatchWake(ctx, func(sid string) { got <- sid }) }()
	time.Sleep(1500 * time.Millisecond) // listener attach 待ち

	// 別クライアント（Cloud Functions 相当）が wake を書く
	cf := newClient(t, "pc-other")
	if err := cf.Wake(ctx, pc, "sess-X"); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	select {
	case s := <-got:
		if s != "sess-X" {
			t.Fatalf("受信 sid 不一致: %q", s)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("real-time wake を受信できない（制御線不成立）")
	}
	// 2 回目も受信（listener 継続）
	if err := cf.Wake(ctx, pc, "sess-Y"); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != "sess-Y" {
			t.Fatalf("2 回目 sid 不一致: %q", s)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("2 回目 wake 未受信")
	}
	// ctx cancel で watcher はクリーンに戻る
	cancel()
	select {
	case e := <-wErr:
		if e != nil {
			t.Fatalf("ctx cancel で error: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctx cancel しても WatchWake が戻らない")
	}
}

// pairing（Web コード認証）を実 Firestore エミュレータで検証。
func TestPairingCreateConsumeOnce(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, "pc-pair")
	const h = "deadbeefhash-create-consume"
	if err := c.CreatePairing(ctx, h, "Mac-Studio", "Mac-Studio", 10*time.Minute); err != nil {
		t.Fatalf("CreatePairing: %v", err)
	}
	pc, scope, ok, err := c.ConsumePairing(ctx, h)
	if err != nil || !ok || pc != "Mac-Studio" || scope != "Mac-Studio" {
		t.Fatalf("初回 consume 失敗: ok=%v pc=%q scope=%q err=%v", ok, pc, scope, err)
	}
	// 一回消費＝2 回目は不可
	_, _, ok2, _ := c.ConsumePairing(ctx, h)
	if ok2 {
		t.Fatal("pairing が一回消費されていない（再利用できた）")
	}
}

func TestPairingExpiredRejected(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, "pc-pair2")
	const h = "deadbeefhash-expired"
	// 既に期限切れ（ttl 負）
	if err := c.CreatePairing(ctx, h, "PC1", "PC1", -time.Minute); err != nil {
		t.Fatal(err)
	}
	_, _, ok, err := c.ConsumePairing(ctx, h)
	if err != nil || ok {
		t.Fatalf("期限切れ pairing が通った: ok=%v err=%v", ok, err)
	}
	// 期限切れも掃除されている（再 consume も false）
	if _, _, ok2, _ := c.ConsumePairing(ctx, h); ok2 {
		t.Fatal("期限切れ doc が削除されていない")
	}
}

func TestConsumeMissingPairing(t *testing.T) {
	_, _, ok, err := newClient(t, "pc-pair3").ConsumePairing(context.Background(), "no-such-hash")
	if err != nil || ok {
		t.Fatalf("不在 pairing が ok: ok=%v err=%v", ok, err)
	}
}

// プロセス終了の同期: PushStatus で 2 セッション → 1 つを DeleteSession
// → ListSessions/OwnSessionKeys から確実に消える（窓 kill 同期の土台）。
// 実エミュレータで delete 反映を検証（合成 stub に頼らない）。
func TestDeleteSessionSyncsTermination(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, "pc-term")
	a := realSession("sid-alive", 1.0, true)
	b := realSession("sid-ended", 2.0, true)
	if _, err := c.PushStatus(ctx, []map[string]any{a, b}); err != nil {
		t.Fatalf("PushStatus: %v", err)
	}
	keys, err := c.OwnSessionKeys(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("OwnSessionKeys 初期 2 のはず: %v err=%v", keys, err)
	}
	// プロセス終了相当 → 消滅キーを Delete
	if err := c.DeleteSession(ctx, "sid-ended"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	ss, err := c.ListSessions(ctx, "pc-term")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(ss) != 1 || SessionKeyOf(ss[0]) != "sid-alive" {
		t.Fatalf("終了セッションが残存（同期 kill 不成立）: %v", ss)
	}
	keys, _ = c.OwnSessionKeys(ctx)
	if len(keys) != 1 || keys[0] != "sid-alive" {
		t.Fatalf("OwnSessionKeys に終了分が残る: %v", keys)
	}
	// 空キー/不在キー Delete は安全（no-op・エラー無し）
	if err := c.DeleteSession(ctx, ""); err != nil {
		t.Fatalf("空キー Delete はエラー無しのはず: %v", err)
	}
	if err := c.DeleteSession(ctx, "sid-ended"); err != nil {
		t.Fatalf("不在キー再 Delete はエラー無しのはず: %v", err)
	}
}
