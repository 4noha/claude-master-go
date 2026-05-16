// Command cloud-relay は Cloud Run 上で動く WSS バイト透過リレー。
// internal/cloud/relay.Server をそのまま HTTP で公開する（画面解釈は
// しない＝不変条件）。Cloud Run は min-instances=0 でスケール・トゥ・
// ゼロ、WS 接続中のみインスタンスが温存される。1 リクエスト最大
// 3600s（要 --timeout）→ それを超える視聴は client 側で再接続
// （PtyProxy.Server が新規 client へ catch-up 再描画するので継続可）。
package main

import (
	"io"
	"log"
	"net/http"
	"os"

	"github.com/4noha/claude-master-go/internal/cloud/relay"
)

// handler は Cloud Run サービスの http.Handler を組み立てる
// （main から分離＝ローカルで実検証可能にするため）。
func handler() http.Handler {
	rl := relay.NewServer()
	mux := http.NewServeMux()
	// ヘルスは "/"。`/healthz` は Google Front End が予約・遮断する
	// （Cloud Run 実測: /healthz は GFE 404、他パスは正常到達）ため使わない。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	})
	mux.Handle("/session", rl) // GET /session?sid=&role=source|viewer を WSS 化
	return mux
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Cloud Run 既定
	}
	srv := &http.Server{Addr: ":" + port, Handler: handler()}
	log.Printf("cloud-relay listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("relay 終了: %v", err)
	}
}
