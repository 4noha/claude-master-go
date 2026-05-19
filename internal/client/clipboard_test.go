package client

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/ptyproxy"
)

// 実クリップボードを汚さない seam 注入（server.setClip と同思想）。
// readClipImage はパッケージ変数なので test 内で差し替え→ defer 復元。
func withFakeClip(t *testing.T, data []byte, code byte, ok bool) {
	t.Helper()
	orig := readClipImage
	readClipImage = func() ([]byte, byte, bool) { return data, code, ok }
	t.Cleanup(func() { readClipImage = orig })
}

func imgCfg() *config.Config {
	return &config.Config{
		SizePolicy: "client", NavKey: []byte{0x1c},
		ImgPasteKey: []byte{0x16}, // ctrl-v（ユーザー選択の既定）
	}
}

// 1) 単体・決定論: クリップボードに画像あり → term.js / server と完全
// 同一の IMAGE フレーム (0xff 0xfd|u32 len(BE)|u8 code|payload) を送出。
// 画像無し → トリガキー(0x16)を素通し。off(ImgPasteKey=nil) → 素通し。
// 旧コード（ImgPasteKey 分岐なし）ではケースAで 0x16 が素通しされ
// フレームが出ない＝本テストは赤になる（回帰検知が成立）。
func TestProcessStdinImagePasteFrameAndFallthrough(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nHELLO-CLIP-βマルチバイト")

	feed := func(cfg *config.Config) []byte {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		w := &connW{c: c1}
		d := &drain{}
		d.run(c2)
		nav, pk := false, false
		done := make(chan error, 1)
		go func() {
			_, e := processStdin(w, cfg, scrollKeys(cfg), cfg.NavKey,
				[]byte{0x16}, &nav, &pk)
			done <- e
		}()
		select {
		case e := <-done:
			if e != nil {
				t.Fatalf("processStdin: %v", e)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("processStdin が返らない（pipe write ブロック?）")
		}
		// パイプ書込は drain goroutine が消費済。少しだけ待って snapshot。
		time.Sleep(50 * time.Millisecond)
		return []byte(d.snap())
	}

	// ケース A: 画像あり → 完全一致フレーム
	withFakeClip(t, png, 1, true)
	want := append([]byte{0xff, 0xfd}, make([]byte, 5)...)
	binary.BigEndian.PutUint32(want[2:6], uint32(len(png)))
	want[6] = 1
	want = append(want, png...)
	if got := feed(imgCfg()); !bytes.Equal(got, want) {
		t.Fatalf("IMAGE フレーム不一致\n got=%#x\nwant=%#x", got, want)
	}

	// ケース B: 画像なし → 0x16 を素通し（Ctrl-V 通常動作を壊さない）
	withFakeClip(t, nil, 0, false)
	if got := feed(imgCfg()); !bytes.Equal(got, []byte{0x16}) {
		t.Fatalf("画像無し時は 0x16 素通しのはず: got=%#x", got)
	}

	// ケース C: off（ImgPasteKey=nil）→ 画像があっても素通し（オプトイン）
	withFakeClip(t, png, 1, true)
	off := imgCfg()
	off.ImgPasteKey = nil
	if got := feed(off); !bytes.Equal(got, []byte{0x16}) {
		t.Fatalf("ImgPasteKey=nil(off) は常に素通しのはず: got=%#x", got)
	}
}

// 2) 実キーパス e2e: 実 stdin バイト(0x16) → 実 client.processStdin →
// 実 connW → 実 unix socket → 実 ptyproxy.Server.parseClientInput →
// handleImagePaste → setClip seam。合成サーバ非使用。クリップボード
// seam だけ差し替えて実 GUI を汚さない。サーバ側コードは無改変なので、
// ここが緑＝tmux/端末からの画像送出が実プロトコルで成立する証明。
func TestClientClipboardBridgeRealServer(t *testing.T) {
	p, err := ptyproxy.Start([]string{"/bin/sh", "-c", "sleep 5"}, 80, 24)
	if err != nil {
		t.Skipf("Start 不可: %v", err)
	}
	defer p.Close()

	td := t.TempDir()
	cfg := &config.Config{
		SizePolicy: "client", NavKey: []byte{0x1c},
		ImgPasteKey:   []byte{0x16},
		SessionsDir:   filepath.Join(td, "sessions"),
		WebImagePaste: true, // サーバ側オプトイン
	}
	srv := ptyproxy.NewServer(p, cfg, nil, 0, 0)

	type capt struct {
		ext   string
		bytes []byte
		path  string
	}
	gotCh := make(chan capt, 1)
	srv.SetClipFunc(func(path, ext string) error {
		b, _ := os.ReadFile(path)
		select {
		case gotCh <- capt{ext: ext, bytes: b, path: path}:
		default:
		}
		return nil
	})

	sock := tmpSock(t)
	if err := srv.Serve(sock); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { srv.Stop(); p.Close() })

	conn, err := Connect(sock, true)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	png := []byte("\x89PNG\r\n\x1a\nREAL-WIRE-PNG-payload-0xff\xff\xfd embedded")
	withFakeClip(t, png, 1, true) // ローカル・クリップボードに png がある状態

	w := &connW{c: conn}
	nav, pk := false, false
	if _, e := processStdin(w, cfg, scrollKeys(cfg), cfg.NavKey,
		[]byte{0x16}, &nav, &pk); e != nil {
		t.Fatalf("processStdin(Ctrl-V): %v", e)
	}

	select {
	case g := <-gotCh:
		if g.ext != "png" {
			t.Fatalf("ext=%q want png", g.ext)
		}
		if !bytes.Equal(g.bytes, png) {
			t.Fatalf("payload 不一致（0xff 埋め込み含む再構成失敗?）\n got=%#x\nwant=%#x",
				g.bytes, png)
		}
		if dir := filepath.Join(td, "paste"); filepath.Dir(g.path) != dir {
			t.Fatalf("一時ファイル場所が想定外: %s want dir %s", g.path, dir)
		}
		if fi, e := os.Stat(g.path); e != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("一時ファイル不在/権限不正: %v %v", e, fi)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("実 socket→server→handleImagePaste→setClip に未到達" +
			"（旧コード=ImgPasteKey 分岐なしならここで赤）")
	}
}
