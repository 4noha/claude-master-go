// Command cloud-relay は Cloud Run 上で動く WSS バイト透過リレー＋
// Web 管理 UI（M7）。relay は画面解釈をしない（不変条件）。Cloud Run
// は min-instances=0 でスケール・トゥ・ゼロ、WS 接続中のみ温存。
// 1 リクエスト最大 3600s（要 --timeout）→ 超過時は client 再接続。
package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/4noha/claude-master-go/internal/cloud/relay"
	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/cloud/web"
	"github.com/4noha/claude-master-go/internal/cloud/webauth"
)

// handler は Cloud Run サービスの http.Handler を組み立てる
// （main から分離＝ローカルで実検証可能にするため）。
// GCP_PROJECT と WEB_SIGNING_KEY が両方あれば Web 管理 UI を有効化し、
// /session 以外を web へ。無ければ relay のみ（/＝health, /session）。
func handler() http.Handler {
	rl := relay.NewServer()
	mux := http.NewServeMux()
	mux.Handle("/session", rl) // 既存 CLI/agent 経路（無改変・常時）

	proj := os.Getenv("GCP_PROJECT")
	key := os.Getenv("WEB_SIGNING_KEY")
	clientID := os.Getenv("GOOGLE_OAUTH_CLIENT_ID")
	allowed := os.Getenv("ALLOWED_EMAILS")
	if proj != "" && key != "" && clientID != "" && allowed != "" {
		st, err := state.New(context.Background(), proj, "relay")
		if err != nil {
			log.Printf("web 無効（Firestore 接続失敗）: %v", err)
		} else {
			ws := web.New(rl, st, webauth.NewSigner(key),
				clientID, allowed, nil, proj, os.Getenv("ENROLL_SA_JSON"))
			mux.Handle("/", ws.Handler()) // /,/login,/auth/google,/api,/ws
			log.Printf("web 管理 UI 有効（project=%s, allow=%s）", proj, allowed)
			return mux
		}
	} else {
		log.Printf("web 無効（GCP_PROJECT/WEB_SIGNING_KEY/GOOGLE_OAUTH_CLIENT_ID/ALLOWED_EMAILS 未設定）")
	}
	// relay のみ: ヘルスは "/"（/healthz は Google Front End が予約・遮断）
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	})
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
