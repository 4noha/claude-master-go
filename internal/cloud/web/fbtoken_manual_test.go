//go:build manual

package web

// 実 GCP での Firestore 更新 push 認証チェーン検証（手動・-tags manual）。
// ブラウザを使わずに browser 相当の経路を機械確認する:
//
//	firebaseCustomToken(実 SA 鍵)
//	  → 実 Identity Toolkit signInWithCustomToken（ID token 交換）
//	  → 実 Firestore REST read（rules: cm-owner read 許可）
//	  → 実 Firestore REST write（rules: write 拒否＝403 を確認）
//
// 必要 env:
//	FIREBASE_API_KEY                 … Web アプリの公開 apiKey
//	GCP_PROJECT                      … claude-master-4noha
//	GOOGLE_APPLICATION_CREDENTIALS   … SA 鍵 JSON（cm-agent）
// 任意 env:
//	CM_TEST_PC（既定 Mac-Studio）

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestFBTokenRealExchangeAndRules(t *testing.T) {
	apiKey := os.Getenv("FIREBASE_API_KEY")
	proj := os.Getenv("GCP_PROJECT")
	saPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if apiKey == "" || proj == "" || saPath == "" {
		t.Skip("FIREBASE_API_KEY/GCP_PROJECT/GOOGLE_APPLICATION_CREDENTIALS 必須")
	}
	pc := os.Getenv("CM_TEST_PC")
	if pc == "" {
		pc = "Mac-Studio"
	}
	sa, err := os.ReadFile(saPath)
	if err != nil {
		t.Fatal(err)
	}

	// 1. custom token mint（本番 /api/fbtoken と同じ関数）
	tok, err := firebaseCustomToken(sa, fbOwnerUID, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// 2. 実 Identity Toolkit で ID token へ交換（ブラウザの
	//    signInWithCustomToken 相当）
	body, _ := json.Marshal(map[string]any{
		"token": tok, "returnSecureToken": true,
	})
	r, err := http.Post(
		"https://identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken?key="+apiKey,
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("signInWithCustomToken: %d %s", r.StatusCode, rb)
	}
	var st struct {
		IDToken string `json:"idToken"`
	}
	if err := json.Unmarshal(rb, &st); err != nil || st.IDToken == "" {
		t.Fatalf("idToken 取得失敗: %v %s", err, rb)
	}
	t.Log("custom token → ID token 交換 OK（実 Identity Toolkit）")

	// 3. 実 Firestore REST read: rules が cm-owner に pcs/** read を許可
	//    していること（200/404=許可・403=拒否）
	fsURL := fmt.Sprintf(
		"https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/pcs/%s",
		proj, pc)
	req, _ := http.NewRequest("GET", fsURL, nil)
	req.Header.Set("Authorization", "Bearer "+st.IDToken)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode == 403 {
		t.Fatalf("rules が cm-owner の read を拒否: %s", b2)
	}
	if r2.StatusCode != 200 && r2.StatusCode != 404 {
		t.Fatalf("read 予期しない応答: %d %s", r2.StatusCode, b2)
	}
	t.Logf("Firestore read 許可 OK（%d）", r2.StatusCode)

	// 4. write は rules で拒否される（SA 鍵を渡さない設計の防御線）
	wbody := []byte(`{"fields":{"hack":{"stringValue":"x"}}}`)
	wreq, _ := http.NewRequest("PATCH",
		fsURL+"/sessions/rules-probe", bytes.NewReader(wbody))
	wreq.Header.Set("Authorization", "Bearer "+st.IDToken)
	wreq.Header.Set("Content-Type", "application/json")
	r3, err := http.DefaultClient.Do(wreq)
	if err != nil {
		t.Fatal(err)
	}
	b3, _ := io.ReadAll(r3.Body)
	r3.Body.Close()
	if r3.StatusCode != 403 {
		t.Fatalf("write が拒否されない（rules 破れ）: %d %s", r3.StatusCode, b3)
	}
	t.Log("Firestore write 拒否 OK（rules read-only 実証）")

	// 5. pcs/** 以外（wake）も read 拒否
	wkURL := fmt.Sprintf(
		"https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/wake/%s",
		proj, pc)
	req4, _ := http.NewRequest("GET", wkURL, nil)
	req4.Header.Set("Authorization", "Bearer "+st.IDToken)
	r4, err := http.DefaultClient.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, r4.Body)
	r4.Body.Close()
	if r4.StatusCode != 403 {
		t.Fatalf("wake/** の read が拒否されない: %d", r4.StatusCode)
	}
	t.Log("pcs/** 以外の read 拒否 OK")
}
