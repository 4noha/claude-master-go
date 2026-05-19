//go:build !windows

// gcloud Firestore エミュレータをプロセスグループ単位で起動/掃除する
// 実 API e2e（POSIX 専用）。Windows での cloud e2e は M8f で扱う。
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

// Phase1: per-PC agent 版 と per-session proxy 版(cm_version)が実
// Firestore に載り、かつ near-$0（cm_version 変化時のみ version++）
// を実 API で確認（合成なし）。
func TestVersionReportingNearZero(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, "pc-cmver")

	// PC 単位 agent 版
	if err := c.RegisterPCVersion(ctx, "v0.1.3"); err != nil {
		t.Fatalf("RegisterPCVersion: %v", err)
	}
	snap, err := c.fs.Collection("pcs").Doc("pc-cmver").Get(ctx)
	if err != nil || snap.Data()["cm_version"] != "v0.1.3" {
		t.Fatalf("pcs.cm_version が載らない: %v err=%v", snap.Data(), err)
	}
	// 旧シグネチャ互換: version 無し → cm_version 書かない
	if err := c.RegisterPC(ctx); err != nil {
		t.Fatalf("RegisterPC: %v", err)
	}
	snap, _ = c.fs.Collection("pcs").Doc("pc-cmver").Get(ctx)
	if _, ok := snap.Data()["cm_version"]; ok {
		t.Fatalf("RegisterPC(無版) で cm_version が残置: %v", snap.Data())
	}

	// per-session proxy 版。同一 cm_version の再 push は near-$0（changed 0）
	withVer := func(v string) map[string]any {
		s := realSession("sid-1", 3.2, true)
		s["cm_version"] = v
		return s
	}
	if ch, err := c.PushStatus(ctx, []map[string]any{withVer("v0.1.2")}); err != nil || ch != 1 {
		t.Fatalf("初回 push changed=1: ch=%d err=%v", ch, err)
	}
	if ch, _ := c.PushStatus(ctx, []map[string]any{withVer("v0.1.2")}); ch != 0 {
		t.Fatalf("同一 cm_version 再 push で書込発生（near-$0 違反）: changed=%d", ch)
	}
	d := func() map[string]any {
		sn, _ := c.fs.Collection("pcs").Doc("pc-cmver").
			Collection("sessions").Doc("sid-1").Get(ctx)
		return sn.Data()
	}
	if d()["cm_version"] != "v0.1.2" {
		t.Fatalf("session.cm_version 未保存: %v", d())
	}
	if v, _ := d()["version"].(int64); v != 1 {
		t.Fatalf("無差分で version 変動: %v", d()["version"])
	}
	// cm_version 変化（旧 inode→更新検出相当）→ version++ ちょうど1回
	if ch, _ := c.PushStatus(ctx, []map[string]any{withVer("v0.1.3")}); ch != 1 {
		t.Fatalf("cm_version 変化で changed=1 のはず: %d", ch)
	}
	if v, _ := d()["version"].(int64); v != 2 {
		t.Fatalf("cm_version 変化で version++ されない: %v", d()["version"])
	}
}

// Phase2: 遠隔命令チャネルを実 Firestore で検証。投入→realtime watch
// が claim(pending→running)して1回だけ受信→二重 claim 不可→Ack 監査
// 書戻し→不正コマンド拒否→RecentCommands。合成なし。
func TestCommandChannelClaimOnceAndAudit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newClient(t, "pc-cmd")

	got := make(chan Command, 8)
	wErr := make(chan error, 1)
	go func() { wErr <- c.WatchCommands(ctx, func(cm Command) { got <- cm }) }()
	time.Sleep(1500 * time.Millisecond) // listener attach 待ち

	id, err := c.PushCommand(ctx, "pc-cmd", "restart-agent", "", "owner@example.com")
	if err != nil || id == "" {
		t.Fatalf("PushCommand: id=%q err=%v", id, err)
	}
	var rcv Command
	select {
	case rcv = <-got:
		if rcv.ID != id || rcv.Cmd != "restart-agent" ||
			rcv.RequestedBy != "owner@example.com" || rcv.Status != "running" {
			t.Fatalf("受信命令が想定外: %+v", rcv)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("realtime 命令を受信できない（制御線不成立）")
	}

	// 二重 claim 不可（再配信されても fn は再呼出されない＝既 running）
	if c.claimCommand(ctx, id) {
		t.Fatal("running 命令を再 claim できてしまう（二重実行リスク）")
	}

	// Ack 監査書戻し
	if err := c.AckCommand(ctx, id, "done", "kicked 2 daemons"); err != nil {
		t.Fatalf("AckCommand: %v", err)
	}
	rc, err := c.RecentCommands(ctx, "pc-cmd", 5)
	if err != nil || len(rc) == 0 {
		t.Fatalf("RecentCommands: n=%d err=%v", len(rc), err)
	}
	if rc[0].Status != "done" || rc[0].Detail != "kicked 2 daemons" ||
		rc[0].FinishedAt == "" {
		t.Fatalf("監査が記録されていない: %+v", rc[0])
	}

	// 不正コマンドは web 投入時点で拒否
	if _, err := c.PushCommand(ctx, "pc-cmd", "rm-rf", "", "owner@example.com"); err == nil {
		t.Fatal("未知コマンドが拒否されない（allowlist 不全）")
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

// relay グラント: 正規接続元(SA)が書いた短命許可を relay が検証する
// 公開 /session 認可の土台。実エミュレータで決定的に検証。
func TestRelayGrantPutCheckExpiry(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, "pc-grant")
	// 無ければ false（fail-closed）
	if c.CheckRelayGrant(ctx, "sidA", "viewer") {
		t.Fatal("grant 無しで true")
	}
	// 書けば true（同 sid,role のみ）
	if err := c.PutRelayGrant(ctx, "sidA", "viewer", time.Minute); err != nil {
		t.Fatalf("PutRelayGrant: %v", err)
	}
	if !c.CheckRelayGrant(ctx, "sidA", "viewer") {
		t.Fatal("有効 grant が false")
	}
	if c.CheckRelayGrant(ctx, "sidA", "source") {
		t.Fatal("role 不一致なのに true（viewer の grant で source 通過）")
	}
	if c.CheckRelayGrant(ctx, "sidB", "viewer") {
		t.Fatal("sid 不一致なのに true")
	}
	// 期限切れ（負 TTL）→ false
	if err := c.PutRelayGrant(ctx, "sidExp", "source", -time.Second); err != nil {
		t.Fatalf("PutRelayGrant exp: %v", err)
	}
	if c.CheckRelayGrant(ctx, "sidExp", "source") {
		t.Fatal("期限切れ grant が true")
	}
	// 不正引数は no-op / false（安全側）
	if c.PutRelayGrant(ctx, "", "viewer", time.Minute) != nil ||
		c.CheckRelayGrant(ctx, "x", "bogus") {
		t.Fatal("不正引数の安全側挙動が不正")
	}
}

// DeletePCByID: 管理 UI の「端末ペアリング削除」。任意 PC の
// pcs/{pc}＋sessions＋wake/{pc} を消し一覧から除去。空 ID は安全 no-op。
func TestDeletePCByID(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, "pc-del")
	if _, err := c.PushStatus(ctx, []map[string]any{
		realSession("s1", 1.0, true)}); err != nil {
		t.Fatalf("PushStatus: %v", err)
	}
	_ = c.Wake(ctx, "pc-del", "s1")
	pcs, _ := c.ListPCs(ctx)
	found := false
	for _, p := range pcs {
		if p == "pc-del" {
			found = true
		}
	}
	if !found {
		t.Fatalf("削除前に pc-del が一覧に無い: %v", pcs)
	}
	if c.DeletePCByID(ctx, "") != nil {
		t.Fatal("空 ID は no-op のはず")
	}
	if err := c.DeletePCByID(ctx, "pc-del"); err != nil {
		t.Fatalf("DeletePCByID: %v", err)
	}
	pcs, _ = c.ListPCs(ctx)
	for _, p := range pcs {
		if p == "pc-del" {
			t.Fatalf("削除後も pc-del が残存: %v", pcs)
		}
	}
	ss, _ := c.ListSessions(ctx, "pc-del")
	if len(ss) != 0 {
		t.Fatalf("sessions が消えていない: %v", ss)
	}
}

// 強制失効: SetRevoked で CheckRelayGrant が有効 grant でも拒否（relay
// 権威）、IsSelfRevoked で agent 自停止、ClearRevoked で再認可。
func TestRevocationEnforcement(t *testing.T) {
	ctx := context.Background()
	c := newClient(t, "pc-rev") // c.pcID="pc-rev"
	if c.IsRevoked(ctx, "pc-rev") || c.IsSelfRevoked(ctx) {
		t.Fatal("初期から失効扱い")
	}
	if err := c.PutRelayGrant(ctx, "sidR", "source", time.Minute); err != nil {
		t.Fatalf("PutRelayGrant: %v", err)
	}
	if !c.CheckRelayGrant(ctx, "sidR", "source") {
		t.Fatal("失効前に有効 grant が false")
	}
	// 失効 → 期限内 grant でも拒否、自 PC 判定も true
	if err := c.SetRevoked(ctx, "pc-rev"); err != nil {
		t.Fatalf("SetRevoked: %v", err)
	}
	if !c.IsRevoked(ctx, "pc-rev") || !c.IsSelfRevoked(ctx) {
		t.Fatal("SetRevoked 後に失効判定されない")
	}
	if c.CheckRelayGrant(ctx, "sidR", "source") {
		t.Fatal("失効中に grant が通った（relay 締め出し不成立）")
	}
	// 解除 → 復帰
	if err := c.ClearRevoked(ctx, "pc-rev"); err != nil {
		t.Fatalf("ClearRevoked: %v", err)
	}
	if c.IsRevoked(ctx, "pc-rev") ||
		!c.CheckRelayGrant(ctx, "sidR", "source") {
		t.Fatal("ClearRevoked 後に復帰しない")
	}
	if c.SetRevoked(ctx, "") != nil || c.IsRevoked(ctx, "") {
		t.Fatal("空 pc の安全側挙動が不正")
	}
}
