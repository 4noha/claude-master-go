// Package webauth は Web 管理 UI の pairing code とセッション cookie を
// 扱う（M7）。ブラウザに GCP 資格情報を渡さず、短命 code →（消費）→
// HMAC 署名 cookie で認証する。暗号プリミティブのみ（Firestore 連携は
// internal/cloud/state、HTTP は relay 側）。
package webauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/api/idtoken"
)

// codeAlphabet は曖昧文字（0/O/1/I/L）を除いた 30 文字。8 文字で ~39bit。
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// GenCode は crypto/rand で 8 文字の pairing code を生成する。
func GenCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 8)
	for i, x := range b {
		out[i] = codeAlphabet[int(x)%len(codeAlphabet)]
	}
	return string(out), nil
}

// NormalizeCode は入力ゆれ（小文字・空白・ハイフン）を吸収する。
func NormalizeCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// HashCode は code の sha256 hex（Firestore には平文を保存しない）。
func HashCode(code string) string {
	h := sha256.Sum256([]byte(NormalizeCode(code)))
	return hex.EncodeToString(h[:])
}

// Token は署名 cookie のペイロード。
type Token struct {
	PC    string `json:"pc"`
	Scope string `json:"scope"`
	Exp   int64  `json:"exp"` // unix 秒
}

// Signer は cookie の HMAC-SHA256 署名・検証。
type Signer struct{ key []byte }

func NewSigner(secret string) *Signer { return &Signer{key: []byte(secret)} }

func (s *Signer) mac(b []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(b)
	return m.Sum(nil)
}

// Sign は base64url(payload) + "." + base64url(hmac)。
func (s *Signer) Sign(t Token) string {
	pj, _ := json.Marshal(t)
	p := base64.RawURLEncoding.EncodeToString(pj)
	sig := base64.RawURLEncoding.EncodeToString(s.mac([]byte(p)))
	return p + "." + sig
}

// Verify は署名と有効期限を検証して Token を返す。
func (s *Signer) Verify(tok string) (Token, bool) {
	var z Token
	i := strings.IndexByte(tok, '.')
	if i <= 0 || i == len(tok)-1 {
		return z, false
	}
	p, sig := tok[:i], tok[i+1:]
	want := base64.RawURLEncoding.EncodeToString(s.mac([]byte(p)))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return z, false
	}
	pj, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return z, false
	}
	var t Token
	if json.Unmarshal(pj, &t) != nil {
		return z, false
	}
	if t.Exp != 0 && time.Now().Unix() > t.Exp {
		return z, false // 期限切れ
	}
	return t, true
}

// --- Google アカウント ID トークン検証（M7f） ---

// GoogleVerifier は GIS の credential（ID トークン）を検証して
// メールアドレスを返す。テストは fake を注入できる。
type GoogleVerifier interface {
	// Verify は idToken を audience(=OAuth Client ID) で検証し、
	// (email, emailVerified, err) を返す。
	Verify(ctx context.Context, idToken, audience string) (string, bool, error)
}

// idtokenVerifier は google.golang.org/api/idtoken による本番実装
// （署名・aud・iss・exp を Google 公開鍵で検証）。
type idtokenVerifier struct{}

func (idtokenVerifier) Verify(ctx context.Context, idToken, audience string) (string, bool, error) {
	p, err := idtoken.Validate(ctx, idToken, audience)
	if err != nil {
		return "", false, err
	}
	if iss, _ := p.Claims["iss"].(string); iss != "accounts.google.com" &&
		iss != "https://accounts.google.com" {
		return "", false, nil
	}
	email, _ := p.Claims["email"].(string)
	ev, _ := p.Claims["email_verified"].(bool)
	return email, ev, nil
}

// DefaultGoogleVerifier は本番の Google 検証器。
var DefaultGoogleVerifier GoogleVerifier = idtokenVerifier{}
