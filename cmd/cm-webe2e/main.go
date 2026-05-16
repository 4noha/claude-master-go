//go:build manual

// cm-webe2e は M7 実ブラウザ e2e 用の一時 source ヘルパ（手動・
// -tags manual）。実 Firestore＋実デプロイ relay に対し録画 PtyProxy
// を test PCID で登録し pairing code を発行、agent を常駐させて
// chrome-devtools からの接続を待つ。検証後に削除する使い捨て。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/agent"
	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/ptyproxy"
)

func main() {
	proj := os.Getenv("GCP_PROJECT")
	wss := os.Getenv("CLOUD_RELAY_URL")
	const pc, sid = "webe2e", "webe2e-sid"

	_, self, _, _ := runtime.Caller(0)
	bin := filepath.Join(filepath.Dir(self), "..", "..",
		"test", "fixtures", "resume-burst", "bytes.bin")
	p, err := ptyproxy.Start([]string{"/bin/sh", "-c", "cat " + bin + "; sleep 3600"}, 164, 50)
	if err != nil {
		panic(err)
	}
	cfg := &config.Config{SizePolicy: "client", NavKey: []byte{0x1c},
		NavScrollStep: 1, NavPageStep: 10, NavWheelStep: 3}
	srv := ptyproxy.NewServer(p, cfg, nil, 0, 0)
	sock := "/tmp/cm-webe2e.sock"
	os.Remove(sock)
	if err := srv.Serve(sock); err != nil {
		panic(err)
	}
	time.Sleep(400 * time.Millisecond)

	ctx := context.Background()
	st, err := state.New(ctx, proj, pc)
	if err != nil {
		panic(err)
	}
	if len(os.Args) > 1 && os.Args[1] == "cleanup" {
		if err := st.DeletePC(ctx); err != nil {
			panic(err)
		}
		fmt.Println("E2E_CLEANUP_DONE PC=" + pc)
		return
	}
	if err := st.RegisterPC(ctx); err != nil { // 端末一覧に確実に出す
		panic(err)
	}
	if _, err := st.PushStatus(ctx, []map[string]any{{
		"key": sid, "session_id": sid, "short_dir": "recording-demo",
		"pid": float64(1), "is_active": true, "cwd": "/demo",
		"start_time": "now", "cpu_percent": float64(0), "mem_mb": float64(0),
	}}); err != nil {
		panic(err)
	}
	// Google ログイン版: pairing 不要。ブラウザは実 Google アカウントで
	// サインイン（chrome-devtools 実ブラウザ e2e）。ここは端末を
	// Firestore に登録して agent を常駐させるだけ。
	fmt.Printf("E2E_READY PC=%s SID=%s\n", pc, sid)

	ag := &agent.Agent{St: st, RelayURL: wss,
		ResolveSock: func(s string) (string, bool) { return sock, s == sid },
		IdleClose:   10 * time.Minute}
	_ = ag.Run(ctx)
}
