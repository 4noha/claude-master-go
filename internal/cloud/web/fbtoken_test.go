package web

// /api/fbtoken（Firestore 更新 push 用 custom token 発行）の検証。
// 実 RSA 鍵で署名→公開鍵で機械検証（合成・モック署名なし）。

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/relay"
	"github.com/4noha/claude-master-go/internal/cloud/webauth"
	"net/http/httptest"
)

// testSAJSON は実 RSA 2048 鍵から SA 鍵 JSON（PKCS8 PEM）を作る。
func testSAJSON(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	sa, _ := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "cm-agent@test.iam.gserviceaccount.com",
		"private_key":  pemStr,
	})
	return string(sa), &key.PublicKey
}

func decodeSeg(t *testing.T, seg string) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("JWT segment decode: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("JWT segment JSON: %v", err)
	}
	return m
}

// verifyJWT は RS256 署名を公開鍵で検証し claims を返す。
func verifyJWT(t *testing.T, tok string, pub *rsa.PublicKey) (head, claims map[string]any) {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT が 3 セグメントでない: %d", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig); err != nil {
		t.Fatalf("RS256 署名検証失敗: %v", err)
	}
	return decodeSeg(t, parts[0]), decodeSeg(t, parts[1])
}

func TestFirebaseCustomTokenMintAndVerify(t *testing.T) {
	sa, pub := testSAJSON(t)
	now := time.Unix(1765432100, 0)
	tok, err := firebaseCustomToken([]byte(sa), fbOwnerUID, now)
	if err != nil {
		t.Fatal(err)
	}
	head, claims := verifyJWT(t, tok, pub)
	if head["alg"] != "RS256" || head["typ"] != "JWT" {
		t.Fatalf("header 不正: %v", head)
	}
	wantAud := "https://identitytoolkit.googleapis.com/" +
		"google.identity.identitytoolkit.v1.IdentityToolkit"
	if claims["aud"] != wantAud {
		t.Fatalf("aud 不正: %v", claims["aud"])
	}
	if claims["iss"] != "cm-agent@test.iam.gserviceaccount.com" ||
		claims["sub"] != claims["iss"] {
		t.Fatalf("iss/sub 不正: %v", claims)
	}
	if claims["uid"] != fbOwnerUID {
		t.Fatalf("uid 不正: %v（rules の cm-owner と一致必須）", claims["uid"])
	}
	if int64(claims["iat"].(float64)) != now.Unix() ||
		int64(claims["exp"].(float64)) != now.Add(time.Hour).Unix() {
		t.Fatalf("iat/exp 不正: %v", claims)
	}

	// 不正鍵は error（panic しない）
	if _, err := firebaseCustomToken([]byte(`{"client_email":"x"}`), "u", now); err == nil {
		t.Fatal("private_key 無しで error にならない")
	}
	if _, err := firebaseCustomToken([]byte(`not-json`), "u", now); err == nil {
		t.Fatal("非 JSON で error にならない")
	}
}

// /api/fbtoken: cookie 必須・config 未設定は 404・設定済は検証可能な
// token + config JSON を返す。
func TestAPIFBTokenEndpoint(t *testing.T) {
	sa, pub := testSAJSON(t)
	rl := relay.NewServer()
	ws := New(rl, nil, webauth.NewSigner("test-key"), "cid", allowEmail, fakeGV{},
		"demo-cm", sa)
	ts := httptest.NewServer(ws.Handler())
	defer ts.Close()

	get := func(cookie string) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+"/api/fbtoken", nil)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	// 未認証 → 401
	if r := get(""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未認証が %d", r.StatusCode)
	}

	// config 未設定 → 404（push 任意機能＝従来構成を壊さない）
	if r := get(authCookie(ws)); r.StatusCode != http.StatusNotFound {
		t.Fatalf("config 未設定が %d", r.StatusCode)
	}

	// config 設定 → 200 + 検証可能 token + config そのまま
	ws.SetFirebaseWebConfig(`{"apiKey":"AIza-test","projectId":"demo-cm"}`)
	r := get(authCookie(ws))
	if r.StatusCode != http.StatusOK {
		t.Fatalf("認証済が %d", r.StatusCode)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	var resp struct {
		Token  string          `json:"token"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("応答 JSON: %v %s", err, body)
	}
	_, claims := verifyJWT(t, resp.Token, pub)
	if claims["uid"] != fbOwnerUID {
		t.Fatalf("uid: %v", claims["uid"])
	}
	var cfg map[string]string
	if err := json.Unmarshal(resp.Config, &cfg); err != nil || cfg["apiKey"] != "AIza-test" {
		t.Fatalf("config 透過失敗: %s", resp.Config)
	}

	// 不正 JSON config は SetFirebaseWebConfig が拒否（404 のまま）
	ws2 := New(rl, nil, webauth.NewSigner("k2"), "cid", allowEmail, fakeGV{}, "p", sa)
	ws2.SetFirebaseWebConfig(`{broken`)
	ts2 := httptest.NewServer(ws2.Handler())
	defer ts2.Close()
	req, _ := http.NewRequest("GET", ts2.URL+"/api/fbtoken", nil)
	req.Header.Set("Cookie", authCookie(ws2))
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("不正 config が拒否されない: %d", r2.StatusCode)
	}
}
