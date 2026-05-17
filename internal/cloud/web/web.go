// Package web は Cloud Run relay に同居する管理 UI バックエンド（M7）。
// 認証は **Google アカウントログイン**（GIS の ID トークンを idtoken で
// 検証→許可メール allowlist→HMAC 署名 cookie）。ブラウザは GCP 資格
// 情報を持たず、Firestore はサーバ側 state.Client（Cloud Run ランタイム
// SA / ローカルはエミュレータ）経由のみ。/ws は認証後に既存 relay の
// viewer として中継（relay 本体・protocol は無改変＝不変条件死守）。
// cookie scope="*" はアカウント全体（全 PC）を表す。
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/relay"
	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/cloud/webauth"
)

//go:embed static
var staticFS embed.FS

const cookieName = "cm_session"
const cookieTTL = 12 * time.Hour
const accountScope = "*" // 全 PC（アカウント）スコープ

type Server struct {
	rl         *relay.Server
	st         *state.Client
	signer     *webauth.Signer
	clientID   string          // Google OAuth Web Client ID
	allowed    map[string]bool // ログイン許可メール（小文字）
	gv         webauth.GoogleVerifier
	gcpProject string // enroll が新 PC へ渡す GCP プロジェクト
	enrollSA   string // enroll が新 PC へ渡す SA 鍵 JSON（env 由来・任意）
}

// New は Google ログイン版。allowedEmails はカンマ区切り、gv が nil なら
// 本番 idtoken 検証器。gcpProject/enrollSA は端末追加(enroll)で新 PC へ
// 配布するブートストラップ（enrollSA 未設定なら enroll は config のみ返す）。
func New(rl *relay.Server, st *state.Client, signer *webauth.Signer,
	clientID, allowedEmails string, gv webauth.GoogleVerifier,
	gcpProject, enrollSA string) *Server {
	am := map[string]bool{}
	for _, e := range strings.Split(allowedEmails, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			am[e] = true
		}
	}
	if gv == nil {
		gv = webauth.DefaultGoogleVerifier
	}
	return &Server{rl: rl, st: st, signer: signer,
		clientID: clientID, allowed: am, gv: gv,
		gcpProject: gcpProject, enrollSA: enrollSA}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.root)
	mux.HandleFunc("/login", s.login)
	mux.HandleFunc("/auth/google", s.authGoogle)
	mux.HandleFunc("/auth/logout", s.logout)
	mux.HandleFunc("/term", s.term)
	mux.HandleFunc("/api/pcs", s.apiGuard(s.apiPCs))
	mux.HandleFunc("/api/devices", s.apiGuard(s.apiDevices))
	mux.HandleFunc("/api/sessions", s.apiGuard(s.apiSessions))
	mux.HandleFunc("/api/pc/delete", s.apiGuard(s.apiDeletePC)) // 端末ペアリング削除
	mux.HandleFunc("/api/enroll", s.apiGuard(s.apiEnroll)) // 端末追加コード発行
	mux.HandleFunc("/enroll", s.enroll)                    // 新 PC が code 交換
	mux.HandleFunc("/ws", s.wsViewer)
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
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
	w.Write([]byte(devicesHTML)) // アカウントの端末一覧（ランディング）
}

// term は Web ターミナル本体（xterm.js）。devices ページからリンク。
func (s *Server) term(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.auth(r); !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(termHTML))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, loginHTMLTmpl, s.clientID)
}

// authGoogle: GIS の credential(ID トークン)を検証し、許可メールのみ
// cookie 発行（scope=アカウント全体）。
func (s *Server) authGoogle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST のみ", http.StatusMethodNotAllowed)
		return
	}
	// GIS の二重送信 CSRF トークン検証（cookie==body）。
	cc, _ := r.Cookie("g_csrf_token")
	if cc == nil || cc.Value == "" || cc.Value != r.FormValue("g_csrf_token") {
		http.Error(w, "CSRF 検証失敗", http.StatusForbidden)
		return
	}
	cred := r.FormValue("credential")
	if cred == "" {
		http.Error(w, "credential が必要", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	email, verified, err := s.gv.Verify(ctx, cred, s.clientID)
	if err != nil || email == "" || !verified {
		http.Error(w, "Google 認証に失敗しました", http.StatusUnauthorized)
		return
	}
	if !s.allowed[strings.ToLower(email)] {
		http.Error(w, "このアカウントは許可されていません", http.StatusForbidden)
		return
	}
	tok := s.signer.Sign(webauth.Token{
		PC: email, Scope: accountScope, Exp: time.Now().Add(cookieTTL).Unix(),
	})
	s.setCookie(w, r, tok)
	http.Redirect(w, r, "/", http.StatusFound)
}

// allows は cookie scope が pc を許可するか（"*"=全 PC）。
func (s *Server) allows(t webauth.Token, pc string) bool {
	return t.Scope == accountScope || t.Scope == pc
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

// devicePCs は scope に応じた対象 PC 群（"*"=全 PC）。
func (s *Server) devicePCs(ctx context.Context, t webauth.Token) []string {
	if t.Scope == accountScope {
		ps, _ := s.st.ListPCs(ctx)
		return ps
	}
	return []string{t.Scope}
}

// apiPCs: スコープ内 PC 一覧。
func (s *Server) apiPCs(w http.ResponseWriter, r *http.Request, t webauth.Token) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := []map[string]string{}
	for _, pc := range s.devicePCs(ctx, t) {
		out = append(out, map[string]string{"id": pc})
	}
	json.NewEncoder(w).Encode(out)
}

// apiDevices: アカウントに接続されている端末一覧＋セッション数/稼働数。
func (s *Server) apiDevices(w http.ResponseWriter, r *http.Request, t webauth.Token) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := []map[string]any{}
	for _, pc := range s.devicePCs(ctx, t) {
		ss, _ := s.st.ListSessions(ctx, pc)
		active := 0
		for _, x := range ss {
			if b, _ := x["is_active"].(bool); b {
				active++
			}
		}
		out = append(out, map[string]any{
			"id": pc, "sessions": len(ss), "active": active,
		})
	}
	json.NewEncoder(w).Encode(out)
}

// apiSessions: ?pc=<PC> のセッション一覧（スコープ検証）。
func (s *Server) apiSessions(w http.ResponseWriter, r *http.Request, t webauth.Token) {
	pc := r.URL.Query().Get("pc")
	if pc == "" && t.Scope != accountScope {
		pc = t.Scope
	}
	if pc == "" || !s.allows(t, pc) {
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

// apiDeletePC: 端末ペアリング削除（pcs/{pc}＋sessions＋wake/{pc}）。
// 破壊的・状態変更なので **POST 限定**（GET だと画像/CSRF で誤発火）。
// cookie 必須（apiGuard）＋スコープ検証。短命 relaygrants は TTL で
// 自然失効するため対象外。失効/不要/旧端末を一覧から消すための機能。
func (s *Server) apiDeletePC(w http.ResponseWriter, r *http.Request, t webauth.Token) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"post only"}`, http.StatusMethodNotAllowed)
		return
	}
	pc := r.URL.Query().Get("pc")
	if pc == "" {
		pc = r.FormValue("pc")
	}
	if pc == "" || !s.allows(t, pc) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.st.DeletePCByID(ctx, pc); err != nil {
		http.Error(w, `{"error":"firestore"}`, http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "pc": pc})
}

func relayWSS(r *http.Request) string {
	if isHTTPS(r) {
		return "wss://" + r.Host
	}
	return "ws://" + r.Host
}

// apiEnroll: ログイン中のアカウントに端末を追加するための一回限り
// enroll コードを発行（cookie 必須＝アカウント所有者のみ）。新 PC で
// 表示コマンドを実行すると enroll で交換される。
func (s *Server) apiEnroll(w http.ResponseWriter, r *http.Request, t webauth.Token) {
	code, err := webauth.GenCode()
	if err != nil {
		http.Error(w, `{"error":"gen"}`, http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	// scope="enroll" として 15 分・一回消費（pairing プリミティブ再利用）
	if err := s.st.CreatePairing(ctx, webauth.HashCode(code),
		"", "enroll", 15*time.Minute); err != nil {
		http.Error(w, `{"error":"store"}`, http.StatusInternalServerError)
		return
	}
	cmd := "claude-master cloud enroll " + code +
		" --relay " + relayWSS(r)
	json.NewEncoder(w).Encode(map[string]any{
		"code": code, "command": cmd, "expires_in": "15m",
	})
}

// enroll: 新 PC が enroll コードを交換してブートストラップを取得
// （無認証＝コードが機密・一回消費・短 TTL）。relay/project と
// （設定時のみ）SA 鍵 JSON を返す。これにより新 PC は鍵の手動配布
// なしでアカウントに参加できる。
func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST のみ", http.StatusMethodNotAllowed)
		return
	}
	code := r.FormValue("code")
	if code == "" {
		var b struct{ Code string `json:"code"` }
		_ = json.NewDecoder(r.Body).Decode(&b)
		code = b.Code
	}
	if code == "" {
		http.Error(w, "code が必要", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	_, scope, ok, err := s.st.ConsumePairing(ctx, webauth.HashCode(code))
	if err != nil {
		http.Error(w, "enroll 処理エラー", http.StatusInternalServerError)
		return
	}
	if !ok || scope != "enroll" {
		http.Error(w, "コードが無効か期限切れです", http.StatusUnauthorized)
		return
	}
	resp := map[string]any{
		"gcp_project": s.gcpProject,
		"relay_url":   relayWSS(r),
	}
	if s.enrollSA != "" {
		resp["sa_json"] = s.enrollSA
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
	if pc == "" && t.Scope != accountScope {
		pc = t.Scope
	}
	sid := r.URL.Query().Get("sid")
	if sid == "" || pc == "" || !s.allows(t, pc) {
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
