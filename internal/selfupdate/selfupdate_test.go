package selfupdate

import (
	"net/http"
	"testing"
)

// setGHHeaders 必須挙動: UA 明示・Accept・GITHUB_TOKEN 有無で
// Authorization を自動制御。UA 未設定 = GitHub が即 403 で拒否する規約
// に対する境界（実環境 D24WT27C3J で `更新` ボタンが github api: 403
// Forbidden を 3 連発した真因の固定回帰）。
func TestSetGHHeadersDefault(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	setGHHeaders(req)
	if got := req.Header.Get("User-Agent"); got != UserAgent {
		t.Fatalf("UA: got=%q want=%q", got, UserAgent)
	}
	if got := req.Header.Get("Accept"); got != "application/vnd.github+json" {
		t.Fatalf("Accept: got=%q want=%q", got, "application/vnd.github+json")
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("GITHUB_TOKEN 未設定で Authorization 付与: %q", got)
	}
}

func TestSetGHHeadersWithToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "abc123")
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	setGHHeaders(req)
	if got := req.Header.Get("Authorization"); got != "Bearer abc123" {
		t.Fatalf("Authorization: got=%q want=%q", got, "Bearer abc123")
	}
}

// httpErr は body 先頭 256B を error に含める＝旧実装が
// `github api: 403 Forbidden` だけだったのを「rate limit / UA 拒否 /
// 認証/権限」のどれかを判別可能にする境界。
func TestHttpErrIncludesBody(t *testing.T) {
	resp := &http.Response{
		Status: "403 Forbidden",
		Body:   http.NoBody, // 空 body でも prefix/status は出る
	}
	err := httpErr("github api", resp)
	if err == nil {
		t.Fatal("error が nil")
	}
	s := err.Error()
	if !contains(s, "github api") || !contains(s, "403 Forbidden") || !contains(s, "body=") {
		t.Fatalf("error 形式が想定外: %q", s)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
