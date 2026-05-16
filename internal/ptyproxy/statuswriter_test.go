package ptyproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
)

// M5e-1 統合: 実録画を実 PTY ラップ→本番 status writer 経路で
// <SessionsDir>/<pid>.status.json が実データに即して書かれることを
// 機械検証（合成不使用）。録画は active footer を含み使用量 footer は
// 含まない＝ is_active:true / usage_percent 不在 が実データの正解。
//
// 注意: status writer は Python _maybe_write_status と同一の 5 秒
// スロットル。録画 burst は数 ms で流れ active footer は末尾なので、
// 初回書込（statusInit で即時）は途中状態＝ is_active:false が正しい
// （Python も同挙動）。スロットル経過後の追加 1 バイトで writer が
// 完成画面を再評価し is_active:true に更新される。これを実時間で
// 検証する（スロットルを縮める＝挙動改変はしない＝parity 維持）。
func TestRunProxyWritesStatusRealRecording(t *testing.T) {
	dir := fixtureDir(t)
	bin := filepath.Join(dir, "bytes.bin")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	sessions := t.TempDir()
	cfg := &config.Config{
		SizePolicy: "client", NavKey: []byte{0x1c},
		NavScrollStep: 1, NavPageStep: 10, NavWheelStep: 3,
		SessionsDir: sessions,
	}
	sock := tmpSock(t)
	codeCh := make(chan int, 1)
	go func() {
		code, err := RunProxy(ProxyOpts{
			// 5s スロットル経過後に 1 バイト出して writer に完成画面を
			// 再評価させ、その後も生存させて true status を観測可能にする
			// （録画は実データのまま・スロットルは不改変＝Python parity）。
			Argv:     []string{"/bin/sh", "-c", "cat " + bin + "; sleep 6; printf x; sleep 3"},
			Cfg:      cfg,
			HostOut:  &hostBuf{},
			SockPath: sock,
			WinSize:  func() (int, int) { return 164, 50 },
		})
		if err != nil {
			t.Errorf("RunProxy: %v", err)
		}
		codeCh <- code
	}()

	// 初回書込は途中画面（is_active:false）が正しい。スロットル経過後の
	// 追加バイトで完成画面が再評価され is_active:true へ更新される。
	// それを実時間でポーリング（最大 ~10s）。
	var statusFile string
	var last map[string]any
	sawActive := false
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		m, _ := filepath.Glob(filepath.Join(sessions, "*.status.json"))
		if len(m) > 0 {
			statusFile = m[0]
			if b, err := os.ReadFile(statusFile); err == nil {
				var p map[string]any
				if json.Unmarshal(b, &p) == nil {
					last = p
					if p["is_active"] == true {
						sawActive = true
						break
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if statusFile == "" {
		t.Fatal("status.json が生成されない")
	}
	if _, ok := last["pid"]; !ok {
		t.Fatalf("pid 欠落: %v", last)
	}
	if last["updated_at"] == "" {
		t.Fatalf("updated_at 空: %v", last)
	}
	if !sawActive {
		t.Fatalf("スロットル経過後も実録画 active footer が反映されない: %v", last)
	}
	if _, ok := last["usage_percent"]; ok {
		t.Fatalf("使用量 footer 無い実録画で usage_percent が出た: %v", last)
	}

	select {
	case <-codeCh:
	case <-time.After(5 * time.Second):
		t.Fatal("RunProxy が返らない")
	}
	if _, err := os.Stat(statusFile); err == nil {
		t.Fatal("終了後も status.json が残る（後始末漏れ）")
	}
}
