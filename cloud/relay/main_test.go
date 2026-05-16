package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/relay"
)

// Cloud Run サービスの配線（/healthz と /session WSS）をローカル実
// httptest で検証（合成不使用＝実 HTTP/実 WSS）。protocol 透過の
// 本検証は internal/cloud/relay の実録画 e2e で別途担保済み。

func TestCloudRelayHealthz(t *testing.T) {
	ts := httptest.NewServer(handler())
	defer ts.Close()
	r, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("/healthz 異常: code=%d body=%q", r.StatusCode, b)
	}
}

func TestCloudRelaySessionRoundTrip(t *testing.T) {
	ts := httptest.NewServer(handler())
	defer ts.Close()
	ws := "ws" + strings.TrimPrefix(ts.URL, "http")
	ctx := context.Background()

	src, err := relay.Dial(ctx, ws, "X", "source")
	if err != nil {
		t.Fatalf("source dial: %v", err)
	}
	defer src.Close()
	vw, err := relay.Dial(ctx, ws, "X", "viewer")
	if err != nil {
		t.Fatalf("viewer dial: %v", err)
	}
	defer vw.Close()

	go src.Write([]byte("ping"))
	buf := make([]byte, 8)
	_ = vw.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := vw.Read(buf)
	if err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("Cloud Run handler 経由の中継失敗: n=%d err=%v got=%q", n, err, buf[:n])
	}
}
