// クリップボード・ブリッジ: tmux/端末から「ローカル機のクリップボード
// 画像」を proxy へ送る。ブラウザの term.js（Clipboard API→IMAGE フレー
// ム）と同じ役割を socket-client が担う。サーバ側は無改変
// （server.parseClientInput が 0xff 0xfd フレームを既に解釈し
// handleImagePaste でリモートのクリップボードへ載せ Ctrl+V 注入）。
// v1 は macOS 主対象（osascript。server.setMacClipboardImage の対）。
package client

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// maxImageBytes は IMAGE payload 上限。term.js / server.maxImageBytes と
// 一致（DoS/disk-fill 防止）。
const maxImageBytes = 8 << 20 // 8 MiB

// readClipImage はクリップボード画像読取の seam（server.setClip と同思想
// ＝実テストで実 GUI クリップボードを汚さず注入差し替え可能）。既定は
// macOS 実装。非 macOS/非GUI/画像無しは ok=false。
var readClipImage = macReadClipboardImage

// sendImage は term.js / server.parseClientInput と同一の IMAGE フレーム
// (0xff 0xfd | u32 len(BE) | u8 code | payload) を直列化送出。connW の
// ロックで RESIZE/SCROLL フレームと混線しない。
func (w *connW) sendImage(data []byte, code byte) error {
	fr := make([]byte, 7+len(data))
	fr[0], fr[1] = 0xff, 0xfd
	binary.BigEndian.PutUint32(fr[2:6], uint32(len(data)))
	fr[6] = code
	copy(fr[7:], data)
	return w.write(fr)
}

// macReadClipboardImage は macOS のクリップボード画像を osascript で取得。
// 1) `clipboard info` で利用可能型を判定（PNG>JPEG>GIF 優先）、2) その型で
// 一時ファイルへ書き出し、3) バイト列を読み戻す。画像が無い/非GUI/失敗・
// 空・上限超過は ok=false（呼び出し側はトリガキーを通常どおり素通し）。
// code は server/term.js と同じ 1=png 2=jpeg 3=gif。
func macReadClipboardImage() (data []byte, code byte, ok bool) {
	info, err := exec.Command("osascript", "-e", "clipboard info").Output()
	if err != nil {
		return nil, 0, false // 非GUI/エラー＝画像取得不可
	}
	s := string(info)
	var asClass string
	switch {
	case strings.Contains(s, "PNGf"):
		asClass, code = "«class PNGf»", 1
	case strings.Contains(s, "JPEG"):
		asClass, code = "«class JPEG»", 2
	case strings.Contains(s, "GIFf"):
		asClass, code = "«class GIFf»", 3
	default:
		return nil, 0, false // クリップボードに画像型が無い
	}
	f, err := os.CreateTemp("", "cm-clip-*.bin") // 0600
	if err != nil {
		return nil, 0, false
	}
	path := f.Name()
	_ = f.Close()
	defer os.Remove(path)
	// 各行を個別 -e で渡す（改行/エスケープ事故回避）。POSIX file の
	// 引数は %q（server.setMacClipboardImage と同じ Go quote）。
	if err := exec.Command("osascript",
		"-e", fmt.Sprintf("set ff to (open for access (POSIX file %q) with write permission)", path),
		"-e", "set eof ff to 0",
		"-e", "write (the clipboard as "+asClass+") to ff",
		"-e", "close access ff",
	).Run(); err != nil {
		return nil, 0, false
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || len(b) > maxImageBytes {
		return nil, 0, false
	}
	return b, code, true
}
