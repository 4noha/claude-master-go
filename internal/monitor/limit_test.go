//go:build !windows

// limit_watcher/resume の unix 専用テスト（testMgr を monitor_test.go と
// 共有＝両方 !windows で一貫）。Mac/linux 従来通り＝parity（他環境クリーン）。
package monitor

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/scanner"
)

// 合成 green は作らない。LimitWatcher/ResumeScheduler は決定的アルゴリズム
// なので「M5e-1 が実際に書く status.json の実スキーマ」「claude footer の
// 実 reset 文字列形式」「実 unix socket round-trip」「実 tmux」で検証する。

func limCfg() *config.Config {
	return &config.Config{LimitWarnPct: 80, LimitInterruptPct: 90}
}

// status.json は JSON 復号されるので usage_percent は float64（実スキーマ）。
func realStatus(pct float64, active bool) map[string]any {
	return map[string]any{
		"usage_percent": pct, "is_active": active,
		"reset_time": "8:30 pm", "reset_tz": "UTC+9",
	}
}

func TestLimitWatcherEscalationOnly(t *testing.T) {
	w := NewLimitWatcher(limCfg())
	k := "sess-1"
	if w.Check(k, map[string]any{}) != nil {
		t.Fatal("usage_percent 欠落で event を返した")
	}
	if w.Check(k, realStatus(50, true)) != nil {
		t.Fatal("閾値未満で event")
	}
	e := w.Check(k, realStatus(85, true))
	if e == nil || e.Level != LevelApproaching || e.UsagePercent != 85 {
		t.Fatalf("85%% で approaching でない: %+v", e)
	}
	if w.Check(k, realStatus(86, true)) != nil {
		t.Fatal("同レベル内で再通知（昇格のみのはず）")
	}
	e = w.Check(k, realStatus(92, true))
	if e == nil || e.Level != LevelInterrupt {
		t.Fatalf("92%% で interrupt でない: %+v", e)
	}
	e = w.Check(k, realStatus(100, true))
	if e == nil || e.Level != LevelReached {
		t.Fatalf("100%% で reached でない: %+v", e)
	}
	if w.Check(k, realStatus(100, true)) != nil {
		t.Fatal("reached 維持で再通知")
	}
	// 閾値未満へ戻ると notified クリア→再び approaching を通知
	if w.Check(k, realStatus(10, true)) != nil {
		t.Fatal("低下時に event")
	}
	if e = w.Check(k, realStatus(85, true)); e == nil || e.Level != LevelApproaching {
		t.Fatalf("クリア後の再 approaching が出ない: %+v", e)
	}
}

func TestResetDatetimeRealFormats(t *testing.T) {
	if off := parseTZOffset("UTC+9"); off != 9 {
		t.Fatalf("UTC+9 オフセット: %v", off)
	}
	if off := parseTZOffset("UTC-5:30"); off != -5.5 {
		t.Fatalf("UTC-5:30 オフセット: %v", off)
	}
	if off := parseTZOffset(""); off != 0 {
		t.Fatalf("空 tz: %v", off)
	}
	// claude footer の実形式（_RESET_RE が捕捉する形）
	for _, ts := range []string{"8:30 pm", "8:30pm", "5 am", "5am", "20:30"} {
		at, ok := parseResetDatetime(ts, "UTC+9")
		if !ok {
			t.Fatalf("実形式 %q を解釈できない", ts)
		}
		if !at.After(time.Now().Add(-time.Second)) {
			t.Fatalf("%q の reset_at が未来でない: %v", ts, at)
		}
		_, off := at.Zone()
		if off != 9*3600 {
			t.Fatalf("%q の tz オフセットが UTC+9 でない: %d", ts, off)
		}
	}
	if _, ok := parseResetDatetime("", "UTC+9"); ok {
		t.Fatal("空時刻を解釈してしまった")
	}
}

func TestResumeSchedulerPersistRealFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pending.json")
	s := NewResumeScheduler(file)
	ev := &LimitEvent{SessionKey: "k1", Level: LevelInterrupt,
		ResetTime: "8:30 pm", ResetTZ: "UTC+9"}
	if _, ok := s.Schedule(ev, "/tmp/x.sock"); !ok {
		t.Fatal("Schedule 失敗")
	}
	if !s.IsPending("k1") {
		t.Fatal("登録後 IsPending=false")
	}
	// 再起動相当: 別インスタンスが永続ファイルから復元
	s2 := NewResumeScheduler(file)
	if !s2.IsPending("k1") {
		t.Fatal("永続化から復元できない")
	}
	// 過去 due → 返却＋削除
	s2.pending["k2"] = pendingEntry{resetAt: time.Now().Add(-time.Minute), sock: "/tmp/y.sock"}
	due := s2.Due(time.Now())
	found := false
	for _, d := range due {
		if d[0] == "k2" && d[1] == "/tmp/y.sock" {
			found = true
		}
	}
	if !found {
		t.Fatalf("過去 due が返らない: %v", due)
	}
	if s2.IsPending("k2") {
		t.Fatal("due 後も pending に残る")
	}
	s2.Remove("k1")
	if s2.IsPending("k1") {
		t.Fatal("Remove 後も pending")
	}
}

func TestSendToSocketRealUnixSocket(t *testing.T) {
	sockf, _ := os.CreateTemp("/tmp", "cmls*.sock")
	path := sockf.Name()
	sockf.Close()
	os.Remove(path)
	defer os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	var got []byte
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		c, e := ln.Accept()
		if e != nil {
			close(done)
			return
		}
		b := make([]byte, 256)
		n, _ := c.Read(b)
		mu.Lock()
		got = append(got, b[:n]...)
		mu.Unlock()
		c.Close()
		close(done)
	}()
	if !SendToSocket(path, []byte("\x1bhello")) {
		t.Fatal("実 socket への送信が false")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("受信側がデータを受け取らない")
	}
	mu.Lock()
	defer mu.Unlock()
	if string(got) != "\x1bhello" {
		t.Fatalf("送受信バイト不一致: %q", got)
	}
	// 接続不可は false
	if SendToSocket(filepath.Join(t.TempDir(), "none.sock"), []byte("x")) {
		t.Fatal("未 listen ソケットで true")
	}
}

// handleLimitEvent / resumeSessions を実 tmux + 実 unix socket で検証。
func TestHandleLimitAndResumeRealTmuxSocket(t *testing.T) {
	cfg := testCfg(t)
	cfg.LimitWarnPct = 80
	cfg.LimitInterruptPct = 90
	// AF_UNIX sun_path は ~104 byte 制限。t.TempDir() の
	// /var/folders/... は長すぎるので sessions は短い /tmp に置く。
	shortSess, err := os.MkdirTemp("/tmp", "cmsess")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(shortSess)
	cfg.SessionsDir = shortSess
	mgr, done := testMgr(t, cfg)
	defer done()

	s := scanner.ClaudeSession{Pid: 4242, Cwd: "/tmp/cm-limit-dir",
		SessionID: "deadbeef-0000-1111-2222-333344445555"}
	os.MkdirAll(s.Cwd, 0o755)
	defer os.RemoveAll(s.Cwd)
	win := mgr.AddWindow(s, "")

	// proxy の代わりに実 unix socket を sessions/<pid>.sock に立てて受信
	os.MkdirAll(cfg.SessionsDir, 0o755)
	sock := sockPathFor(cfg, s.Pid)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	recv := make(chan string, 4)
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			b := make([]byte, 512)
			n, _ := c.Read(b)
			recv <- string(b[:n])
			c.Close()
		}
	}()

	sch := NewResumeScheduler(cfg.PendingFile)
	w := NewLimitWatcher(cfg)
	status := realStatus(95, true) // active な interrupt 級
	ev := &LimitEvent{SessionKey: s.Key(), Level: LevelInterrupt,
		UsagePercent: 95, ResetTime: "8:30 pm", ResetTZ: "UTC+9"}

	handleLimitEvent(cfg, mgr, sch, ev, s, status)

	select {
	case msg := <-recv:
		if !strings.HasPrefix(msg, "\x1b") || !strings.Contains(msg, "Usage limit at 95%") {
			t.Fatalf("中断メッセージが実 socket に届かない/不正: %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("実 socket に中断メッセージが届かない")
	}
	if !sch.IsPending(s.Key()) {
		t.Fatal("interrupt で再開スケジュールが登録されない")
	}
	if wn := mgr.WindowFor(s.Key()); !strings.HasSuffix(wn, "[PAUSED]") {
		t.Fatalf("window が [PAUSED] にリネームされない: %q", wn)
	}

	// 再開: pending を過去 due にして resumeSessions
	sch.pending[s.Key()] = pendingEntry{
		resetAt: time.Now().Add(-time.Minute), sock: sock}
	cur := map[string]scanner.ClaudeSession{s.Key(): s}
	resumeSessions(sch, cur, mgr, w)
	_ = win

	select {
	case msg := <-recv:
		if !strings.Contains(msg, "リセットされました") {
			t.Fatalf("再開メッセージが不正: %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("実 socket に再開メッセージが届かない")
	}
	if mgr.WindowFor(s.Key()) != s.ShortDir() {
		t.Fatalf("再開で window 名が ShortDir に戻らない: %q",
			mgr.WindowFor(s.Key()))
	}
	if sch.IsPending(s.Key()) {
		t.Fatal("resume 後も pending に残る")
	}
}
