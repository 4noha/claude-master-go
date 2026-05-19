package client

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
)

// 回帰: IMG_PASTE_KEY 有効時、stdin 処理中の readClipImage(=osascript 同期)
// が走っても **端末出力が止まらない**こと。旧 RunConn は単一 select で
// stdin 処理と出力を結合し、クリップボード読取の間 画面が固まった
// （症状: コンソール「出力でない/文字化け」・本番で img_paste_key 有効化
// 後に顕在化）。新 runLoop は出力を独立 goroutine 化して解消。
//
// 判別方法: 同一シナリオを「新 runLoop」と「旧結合の参照実装
// coupledLoop」で実行。出力が遅い readClipImage(800ms ブロック)中に
// サーバが送る出力が 300ms 以内に画面へ届くか。新=届く(緑)、
// 旧=飢餓で届かない(赤)＝このテストが回帰を機械検知する証明。

type safeBuf struct {
	mu sync.Mutex
	b  []byte
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *safeBuf) has(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Contains(s.b, []byte(sub))
}

func waitHas(s *safeBuf, sub string, to time.Duration) bool {
	dl := time.Now().Add(to)
	for time.Now().Before(dl) {
		if s.has(sub) {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s.has(sub)
}

// coupledLoop は **旧 RunConn ループの忠実な再現**（単一 select で
// stdin 処理と出力を結合）。回帰テストが旧挙動で赤になることを
// その場で証明するための参照（鉄則2: 修正前に落ちることを確認）。
func coupledLoop(stdin io.Reader, stdout io.Writer, conn net.Conn,
	w *connW, cfg *config.Config) error {
	keys := scrollKeys(cfg)
	navKey := cfg.NavKey
	navMode := false
	pkScrolled := false
	stdinCh := make(chan []byte)
	sockCh := make(chan []byte)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				stdinCh <- append([]byte{}, buf[:n]...)
			}
			if err != nil {
				close(stdinCh)
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				sockCh <- append([]byte{}, buf[:n]...)
			}
			if err != nil {
				close(sockCh)
				return
			}
		}
	}()
	for {
		select {
		case data, ok := <-stdinCh:
			if !ok {
				return nil
			}
			done, err := processStdin(w, cfg, keys, navKey, data,
				&navMode, &pkScrolled)
			if err != nil || done {
				return nil
			}
		case data, ok := <-sockCh:
			if !ok || data == nil {
				return nil
			}
			_, _ = stdout.Write(data)
		}
	}
}

func imgKeyCfg() *config.Config {
	return &config.Config{
		SizePolicy: "client", NavKey: []byte{0x1c},
		ImgPasteKey: []byte{0x16}, // 本番と同じ ctrl-v 有効
	}
}

// scenario: 出力 chunk1 が届く(baseline) → stdin に 0x16(=遅い
// readClipImage 800ms 発火) → その最中にサーバが chunk2 送出 →
// chunk2 が 300ms 以内に画面へ届くか。届けば出力は stdin 処理に
// 結合していない。
func runStarvationScenario(t *testing.T,
	loop func(io.Reader, io.Writer, net.Conn, *connW, *config.Config) error) bool {
	t.Helper()

	// 遅い osascript を seam で注入（実 GUI 非汚染・決定論）。
	orig := readClipImage
	readClipImage = func() ([]byte, byte, bool) {
		time.Sleep(800 * time.Millisecond)
		return nil, 0, false // 画像なし→processStdin はキー素通しへ
	}
	defer func() { readClipImage = orig }()

	srv, cli := net.Pipe()
	inR, inW := io.Pipe()
	out := &safeBuf{}
	cfg := imgKeyCfg()

	// サーバ側: client→server を drain（net.Pipe の書込ブロック回避）。
	go func() {
		b := make([]byte, 4096)
		for {
			if _, e := srv.Read(b); e != nil {
				return
			}
		}
	}()

	loopErr := make(chan error, 1)
	go func() { loopErr <- loop(inR, out, cli, &connW{c: cli}, cfg) }()

	// baseline: 結合前は出力が普通に流れる
	go func() { _, _ = srv.Write([]byte("BASELINE-OUT")) }()
	if !waitHas(out, "BASELINE-OUT", 2*time.Second) {
		t.Fatal("baseline 出力が届かない（テスト前提崩れ）")
	}

	// stdin に 0x16 → 入力処理が readClipImage で 800ms ブロック
	go func() { _, _ = inW.Write([]byte{0x16}) }()
	time.Sleep(30 * time.Millisecond) // ブロック開始を確実化

	// ブロック最中にサーバ出力。結合してなければ即届く。
	go func() { _, _ = srv.Write([]byte("DURING-BLOCK-OUT")) }()
	got := waitHas(out, "DURING-BLOCK-OUT", 300*time.Millisecond)

	_ = inW.Close()
	_ = cli.Close()
	_ = srv.Close()
	select {
	case <-loopErr:
	case <-time.After(2 * time.Second):
	}
	return got
}

func TestRunLoopOutputNotStarvedByClipboardRead(t *testing.T) {
	// 新 runLoop: クリップボード読取 800ms 中でも出力は届く（緑）。
	if !runStarvationScenario(t, runLoop) {
		t.Fatal("runLoop: readClipImage ブロック中に端末出力が飢餓" +
			"（出力ポンプが stdin 処理に結合している＝回帰）")
	}
}

func TestCoupledLoopReproducesRegression(t *testing.T) {
	// 旧結合実装: 同シナリオで出力が飢餓する（＝このテストが回帰を
	// 機械検知できる証明。鉄則2: 修正前に赤になることを確認）。
	if runStarvationScenario(t, coupledLoop) {
		t.Fatal("旧結合実装で出力が飢餓しない＝回帰を再現できておらず" +
			"テストが無効（想定外）")
	}
}
