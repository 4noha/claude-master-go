package webauth

import (
	"strings"
	"testing"
	"time"
)

// 暗号プリミティブの決定的単体（合成 green ではなく仕様そのものの検証）。

func TestGenCodeCharsetAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c, err := GenCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != 8 {
			t.Fatalf("長さ不正: %q", c)
		}
		for _, r := range c {
			if !strings.ContainsRune(codeAlphabet, r) {
				t.Fatalf("英数字外: %q in %q", r, c)
			}
		}
		seen[c] = true
	}
	if len(seen) < 1990 { // 2000 中ほぼ全て一意（衝突は極小）
		t.Fatalf("一意性が低すぎる: %d/2000", len(seen))
	}
}

func TestNormalizeAndHashStable(t *testing.T) {
	h1 := HashCode("ABCDEFGH")
	for _, v := range []string{"abcdefgh", " ABCD-EFGH ", "ab cd ef gh", "ABCD-EFGH"} {
		if HashCode(v) != h1 {
			t.Fatalf("正規化不一致: %q→%s want %s", v, HashCode(v), h1)
		}
	}
	if HashCode("ABCDEFGH") == HashCode("ABCDEFGZ") {
		t.Fatal("異なる code が同一ハッシュ")
	}
	if len(h1) != 64 {
		t.Fatalf("sha256 hex 長さ不正: %d", len(h1))
	}
}

func TestSignerRoundTripTamperExpiry(t *testing.T) {
	s := NewSigner("secret-key-A")
	tok := s.Sign(Token{PC: "Mac-Studio", Scope: "Mac-Studio",
		Exp: time.Now().Add(time.Hour).Unix()})
	got, ok := s.Verify(tok)
	if !ok || got.PC != "Mac-Studio" || got.Scope != "Mac-Studio" {
		t.Fatalf("正常 round-trip 失敗: ok=%v got=%+v", ok, got)
	}
	// 署名改竄
	if _, ok := s.Verify(tok + "x"); ok {
		t.Fatal("署名改竄を検出できない")
	}
	bad := []byte(tok)
	bad[0] ^= 0x20 // payload 改竄
	if _, ok := s.Verify(string(bad)); ok {
		t.Fatal("payload 改竄を検出できない")
	}
	// 別鍵では検証不可
	if _, ok := NewSigner("secret-key-B").Verify(tok); ok {
		t.Fatal("別鍵で検証が通った")
	}
	// 期限切れ
	exp := s.Sign(Token{PC: "x", Exp: time.Now().Add(-time.Minute).Unix()})
	if _, ok := s.Verify(exp); ok {
		t.Fatal("期限切れ token が通った")
	}
	// exp=0 は無期限（有効）
	if _, ok := s.Verify(s.Sign(Token{PC: "x"})); !ok {
		t.Fatal("exp=0 が無効化された")
	}
	// 不正形式
	for _, b := range []string{"", "nodot", ".", "a."} {
		if _, ok := s.Verify(b); ok {
			t.Fatalf("不正形式 %q が通った", b)
		}
	}
}
