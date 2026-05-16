// Package web は Cloud Run relay に同居する管理 UI バックエンド（M7）。
// ブラウザは GCP 資格情報を持たず pairing code →（消費）→ HMAC 署名
// cookie で認証。Firestore はサーバ側 state.Client（Cloud Run ランタイム
// SA / ローカルはエミュレータ）経由のみ。/ws は認証後に既存 relay の
// viewer として中継（relay 本体・protocol は無改変＝不変条件死守）。
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/relay"
	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/cloud/webauth"
)

const cookieName = "cm_session"
const cookieTTL = 12 * time.Hour

type Server struct {
	rl     *relay.Server
	st     *state.Client
	signer *webauth.Signer
}

func New(rl *relay.Server, st *state.Client, signer *webauth.Signer) *Server {
	return &Server{rl: rl, st: st, signer: signer}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.root)
	mux.HandleFunc("/login", s.login)
	mux.HandleFunc("/auth/code", s.authCode)
	mux.HandleFunc("/auth/logout", s.logout)
	mux.HandleFunc("/api/pcs", s.apiGuard(s.apiPCs))
	mux.HandleFunc("/api/sessions", s.apiGuard(s.apiSessions))
	mux.HandleFunc("/ws", s.wsViewer)
	return mux
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// auth は cookie を検証して Token を返す。
func (s *Server) auth(r *http.Request) (webauth.Token, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return webauth.Token{}, false
	}
	return s.signer.Verify(c.Value)
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/",
		HttpOnly: true, Secure: isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(cookieTTL.Seconds()),
	})
}

func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.auth(r); !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(appHTML)) // M7c で xterm.js SPA に差し替え
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(loginHTML))
}

// authCode: pairing code を消費して cookie を発行（フォーム/JSON 両対応）。
func (s *Server) authCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST のみ", http.StatusMethodNotAllowed)
		return
	}
	code := r.FormValue("code")
	if code == "" {
		var body struct{ Code string `json:"code"` }
		_ = json.NewDecoder(r.Body).Decode(&body)
		code = body.Code
	}
	if code == "" {
		http.Error(w, "code が必要", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	pc, scope, ok, err := s.st.ConsumePairing(ctx, webauth.HashCode(code))
	if err != nil {
		http.Error(w, "認証処理エラー", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "コードが無効か期限切れです", http.StatusUnauthorized)
		return
	}
	tok := s.signer.Sign(webauth.Token{
		PC: pc, Scope: scope, Exp: time.Now().Add(cookieTTL).Unix(),
	})
	s.setCookie(w, r, tok)
	if r.Header.Get("Accept") == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"pc": pc, "scope": scope})
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// apiGuard は cookie 必須ラッパ（未認証は 401 JSON）。
func (s *Server) apiGuard(h func(http.ResponseWriter, *http.Request, webauth.Token)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ok := s.auth(r)
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		h(w, r, t)
	}
}

// apiPCs: スコープ内 PC 一覧（現状 scope=単一 PC）。
func (s *Server) apiPCs(w http.ResponseWriter, r *http.Request, t webauth.Token) {
	json.NewEncoder(w).Encode([]map[string]string{{"id": t.Scope}})
}

// apiSessions: ?pc=<PC> のセッション一覧（スコープ検証）。
func (s *Server) apiSessions(w http.ResponseWriter, r *http.Request, t webauth.Token) {
	pc := r.URL.Query().Get("pc")
	if pc == "" {
		pc = t.Scope
	}
	if pc != t.Scope {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ss, err := s.st.ListSessions(ctx, pc)
	if err != nil {
		http.Error(w, `{"error":"firestore"}`, http.StatusInternalServerError)
		return
	}
	if ss == nil {
		ss = []map[string]any{}
	}
	json.NewEncoder(w).Encode(ss)
}

// wsViewer: 認証済ブラウザの端末接続。cookie 検証→スコープ確認→
// wake 書込（相手 agent 起動）→ 既存 relay の viewer として中継。
func (s *Server) wsViewer(w http.ResponseWriter, r *http.Request) {
	t, ok := s.auth(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	pc := r.URL.Query().Get("pc")
	if pc == "" {
		pc = t.Scope
	}
	sid := r.URL.Query().Get("sid")
	if sid == "" || pc != t.Scope {
		http.Error(w, "pc(scope 内)/sid が必要", http.StatusBadRequest)
		return
	}
	// 相手 PC の agent を起こす（M6 と同じ wake 制御線）
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	_ = s.st.Wake(ctx, pc, sid)
	cancel()
	// 既存 relay の viewer として中継（relay/protocol 無改変）
	s.rl.Accept(w, r, sid, "viewer")
}
