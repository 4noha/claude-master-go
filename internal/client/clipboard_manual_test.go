//go:build manual

// 実 GUI クリップボードを使う回帰（合成しない）。CI/通常 `go test` から
// 隔離（macOS GUI セッション必須＝TestE2ERealGCP と同じ manual タグ運用）。
// 実行: GUI ログイン中の Mac で
//
//	go test -tags manual ./internal/client/ -run TestMacReadClipboardImageReal -v
//
// 実機の osascript で 1x1 PNG を実クリップボードへ載せ、macReadClipboardImage
// が PNG(code=1) を実取得できることを確認（server.setMacClipboardImage の対）。
package client

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func TestMacReadClipboardImageReal(t *testing.T) {
	// 最小の有効 1x1 PNG（pasteboard が画像として受理する実データ）。
	png, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatalf("PNG decode: %v", err)
	}
	src, err := os.CreateTemp("", "cm-src-*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(src.Name())
	if _, err := src.Write(png); err != nil {
		t.Fatal(err)
	}
	_ = src.Close()

	// server.setMacClipboardImage と同一手順で実クリップボードへ。
	scr := fmt.Sprintf("set the clipboard to (read (POSIX file %q) as «class PNGf»)", src.Name())
	if out, err := exec.Command("osascript", "-e", scr).CombinedOutput(); err != nil {
		t.Skipf("実クリップボードへ載せられず（非GUI?）: %v %s", err, out)
	}

	data, code, ok := macReadClipboardImage()
	if !ok {
		t.Fatal("実クリップボードの PNG を macReadClipboardImage が取得できない")
	}
	if code != 1 {
		t.Fatalf("code=%d want 1(png)", code)
	}
	// pasteboard が再エンコードし得るため厳密一致は要求しない。実 PNG
	// として成立（署名 + 非空 + 上限内）を実検証。
	if len(data) == 0 || len(data) > maxImageBytes {
		t.Fatalf("取得サイズ異常: %d", len(data))
	}
	if string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("PNG 署名が無い: %#x", data[:mini(8, len(data))])
	}
}
