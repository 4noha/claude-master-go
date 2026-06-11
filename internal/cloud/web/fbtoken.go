package web

// Firestore 更新 push（M9 Web）: ブラウザが status doc を onSnapshot で
// 監視できるよう、relay が cookie 認証済みオーナーへ Firebase custom
// token を発行する。identity は全端末共通 uid=cm-owner（単一オーナー
// 設計＝端末ごとのユーザー管理をしない）。rules（deploy/firestore.
// rules）が cm-owner を pcs/** read-only に制限するので、SA 鍵そのもの
// （Firestore 全権）は決してブラウザへ渡らない。
//
// ネイティブ側の wake/同期（WatchWake/WatchSessions = サーバ SDK の
// snapshot listener）と同型の仕組みを、ブラウザに移植するための認証
// ブートストラップ。

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/webauth"
)

// fbOwnerUID は rules の request.auth.uid と一致させる固定 identity。
const fbOwnerUID = "cm-owner"

// firebaseCustomToken は SA 鍵 JSON（client_email/private_key）で RS256
// 署名した Firebase custom token を作る純関数（Admin SDK 相当の最小
// 実装＝依存追加なし・静的バイナリ維持）。now 注入で決定論テスト可能。
func firebaseCustomToken(saJSON []byte, uid string, now time.Time) (string, error) {
	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	if err := json.Unmarshal(saJSON, &sa); err != nil {
		return "", fmt.Errorf("SA 鍵 JSON: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return "", fmt.Errorf("SA 鍵 JSON に client_email/private_key が無い")
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", fmt.Errorf("SA private_key が PEM でない")
	}
	pk, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("SA private_key parse: %w", err)
	}
	rsaKey, ok := pk.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("SA private_key が RSA でない")
	}
	enc := func(v any) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(b), nil
	}
	head, err := enc(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	// aud は Firebase custom token の固定値（Identity Toolkit が検証）
	const aud = "https://identitytoolkit.googleapis.com/" +
		"google.identity.identitytoolkit.v1.IdentityToolkit"
	claims, err := enc(map[string]any{
		"iss": sa.ClientEmail, "sub": sa.ClientEmail, "aud": aud,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "uid": uid,
	})
	if err != nil {
		return "", err
	}
	signing := head + "." + claims
	h := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// apiFBToken は GET /api/fbtoken。owner cookie（apiGuard）必須。
// Firebase Web SDK 初期化用 config（公開値・rules で保護）と、
// signInWithCustomToken 用 custom token（1h）を返す。SA 鍵未設定 /
// config 未設定の構成では 404（push 無しでも Web は従来どおり動く）。
func (s *Server) apiFBToken(w http.ResponseWriter, r *http.Request, _ webauth.Token) {
	if s.enrollSA == "" || s.fbWebConfig == "" {
		http.Error(w, `{"error":"firebase 未設定"}`, http.StatusNotFound)
		return
	}
	tok, err := firebaseCustomToken([]byte(s.enrollSA), fbOwnerUID, time.Now())
	if err != nil {
		http.Error(w, `{"error":"token 生成失敗"}`, http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":  tok,
		"config": json.RawMessage(s.fbWebConfig),
	})
}
