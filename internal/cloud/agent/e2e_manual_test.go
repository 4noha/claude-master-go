//go:build manual

package agent

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/relay"
	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/ptyproxy"
	"github.com/4noha/claude-master-go/internal/screen"
)

// 実 GCP 結線スモーク（手動・-tags manual）。実 Cloud Run relay ＋実
// Firestore（GOOGLE_APPLICATION_CREDENTIALS/GCP_PROJECT/CLOUD_RELAY_URL）
// を通して、wake→データ線→画面が viewer に届くことを実録画＋
// display-oracle で検証する。単一機だがトラフィックは GCP を通過。

func TestE2ERealGCP(t *testing.T) {
	proj := os.Getenv("GCP_PROJECT")
	wss := os.Getenv("CLOUD_RELAY_URL")
	if proj == "" || wss == "" || os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
		t.Skip("GCP_PROJECT/CLOUD_RELAY_URL/GOOGLE_APPLICATION_CREDENTIALS 必須")
	}
	_, self, _, _ := runtime.Caller(0)
	bin := filepath.Join(filepath.Dir(self), "..", "..", "..",
		"test", "fixtures", "resume-burst", "bytes.bin")
	p, err := ptyproxy.Start([]string{"/bin/sh", "-c", "cat " + bin + "; sleep 60"}, 164, 50)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SizePolicy: "client", NavKey: []byte{0x1c},
		NavScrollStep: 1, NavPageStep: 10, NavWheelStep: 3}
	srv := ptyproxy.NewServer(p, cfg, nil, 0, 0)
	usock := filepath.Join("/tmp", "cm-e2e.sock")
	os.Remove(usock)
	if err := srv.Serve(usock); err != nil {
		t.Fatal(err)
	}
	defer func() { srv.Stop(); p.Close(); os.Remove(usock) }()
	time.Sleep(400 * time.Millisecond)

	const pcID, sid = "e2e-pc", "e2e-sid"
	stA, err := state.New(context.Background(), proj, pcID)
	if err != nil {
		t.Fatal(err)
	}
	defer stA.Close()
	ag := &Agent{St: stA, RelayURL: wss,
		ResolveSock: func(s string) (string, bool) { return usock, s == sid },
		IdleClose:   20 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ag.Run(ctx)
	time.Sleep(2 * time.Second) // WatchWake attach（実 Firestore）

	viewer, err := relay.Dial(ctx, wss, sid, "viewer")
	if err != nil {
		t.Fatalf("viewer Dial(実 Cloud Run): %v", err)
	}
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

	stB, _ := state.New(context.Background(), proj, "viewer-pc")
	defer stB.Close()
	if err := stB.Wake(ctx, pcID, sid); err != nil { // 実 Firestore wake
		t.Fatalf("Wake: %v", err)
	}

	frameText := func() string {
		mu.Lock()
		s := string(append([]byte{}, buf...))
		mu.Unlock()
		fr := strings.Split(s, "\x1b[?2026h")
		if len(fr) < 2 {
			return ""
		}
		v := screen.NewModel(164, 50)
		v.Feed([]byte(fr[len(fr)-1]))
		return strings.Join(v.VisibleLines(), "\n")
	}
	rs := []byte{0xff, 0xff}
	var pp [4]byte
	binary.BigEndian.PutUint16(pp[0:2], 50)
	binary.BigEndian.PutUint16(pp[2:4], 164)
	viewer.Write(append(rs, pp[:]...))

	wait := func(sub string) bool {
		dl := time.Now().Add(20 * time.Second)
		for time.Now().Before(dl) {
			if strings.Contains(frameText(), sub) {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return false
	}
	if !wait("bypass permissions") {
		t.Fatalf("実 GCP 経由で画面が届かない: %.160q", frameText())
	}
	sc := []byte{0xff, 0xfe, 0x85, 0xee} // -31250 ≒ 大きく遡る
	viewer.Write(sc)
	if !wait("Claude Code v2.1.126") {
		t.Fatalf("実 GCP 経由 SCROLL 透過せず: %.200q", frameText())
	}
	t.Log("実 GCP（Cloud Run relay + Firestore）経由 e2e OK")
}
