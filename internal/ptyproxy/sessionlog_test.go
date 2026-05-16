package ptyproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/screen"
)

// M4c: SESSION_LOG 本番経路テスト。Python debug/tests/test_session_log.py
// に 1:1 対応。実 `claude --resume` 録画(resume-burst/bytes.bin)を
// 本番の sessionLogCaptureLocked（masterPump が毎チャンク呼ぶ実メソッド）
// に通し、ファイルへ忠実プレーンテキスト転写・ANSI 無・終了時に最終
// 可視画面まで書いて閉じることを検証。描画に触れないので live 破壊・
// dedup の問題は構造的に無い（Claude の再ストリームは実出力どおり残す）。

func sessCfg(spec string) *config.Config {
	return &config.Config{
		SizePolicy: "client", NavKey: []byte{0x1c},
		NavScrollStep: 1, NavPageStep: 10, NavWheelStep: 3,
		SessionLog: spec,
	}
}

// feedChunked は masterPump と同一の per-chunk 本番経路（lock →
// VT.Feed → sessionLogCaptureLocked）で録画を流す。4096 は実
// PumpToVT/masterPump の read サイズ。
func feedChunked(srv *Server, data []byte) {
	const chunk = 4096
	for off := 0; off < len(data); off += chunk {
		end := off + chunk
		if end > len(data) {
			end = len(data)
		}
		srv.mu.Lock()
		srv.p.VT.Feed(data[off:end])
		srv.sessionLogCaptureLocked()
		srv.mu.Unlock()
	}
}

func TestSessionLogRealResumeRecording(t *testing.T) {
	dir := fixtureDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "bytes.bin"))
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	var meta struct{ Width, Height int }
	mb, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	_ = json.Unmarshal(mb, &meta)

	logfile := filepath.Join(t.TempDir(), "session.log")
	p := &Proxy{VT: screen.NewModel(meta.Width, meta.Height)}
	srv := NewServer(p, sessCfg(logfile), nil, meta.Width, meta.Height)
	srv.openSessionLog()
	if srv.sessFp == nil {
		t.Fatal("SESSION_LOG でファイルが開かれていない")
	}

	feedChunked(srv, data)
	srv.finalizeSessionLog()
	if srv.sessFp != nil {
		t.Fatal("finalize 後にクローズされていない")
	}

	b, err := os.ReadFile(logfile)
	if err != nil {
		t.Fatalf("ログ読めない: %v", err)
	}
	text := string(b)
	if strings.Contains(text, "\x1b") {
		t.Fatal("プレーンテキストでない（ANSI 混入）")
	}
	if !strings.Contains(text, "===== claude-master session") {
		t.Fatal("開始ヘッダが無い")
	}
	if !strings.Contains(text, "===== session end") {
		t.Fatal("終了フッタが無い（finalize flush されていない）")
	}
	if !strings.Contains(text, "Claude") {
		t.Fatal("実会話本文(Claude)が転写されていない")
	}
	if !strings.Contains(text, "claude-master") {
		t.Fatal("実会話本文(claude-master)が転写されていない")
	}
	var body int
	for _, l := range strings.Split(text, "\n") {
		if l != "" && !strings.HasPrefix(l, "=====") {
			body++
		}
	}
	if body <= 100 {
		t.Fatalf("転写が少なすぎる: %d 行", body)
	}
	t.Logf("SESSION_LOG 転写 %d 行（実録画忠実・ANSI 無・dedup 無）", body)
}

func TestSessionLogDisabledByDefault(t *testing.T) {
	p := &Proxy{VT: screen.NewModel(80, 24)}
	srv := NewServer(p, sessCfg(""), nil, 80, 24)
	srv.openSessionLog()
	if srv.sessFp != nil || srv.sessFlusher != nil {
		t.Fatal("SESSION_LOG 未設定なのに開いた（既定無効でない）")
	}
	srv.mu.Lock()
	srv.p.VT.Feed([]byte("hello\r\n"))
	srv.sessionLogCaptureLocked() // 例外なく no-op
	srv.mu.Unlock()
	srv.finalizeSessionLog() // no-op
}

func TestSessionLogTrueUsesAutoPath(t *testing.T) {
	tmp := t.TempDir()
	p := &Proxy{VT: screen.NewModel(80, 24)}
	srv := NewServer(p, sessCfg("true"), nil, 80, 24)
	srv.logsDir = tmp // LOGS_DIR を tmp へ（home 汚染回避・Python monkeypatch 相当）
	srv.openSessionLog()
	if srv.sessFp == nil {
		t.Fatal("SESSION_LOG=true でファイルが開かれていない")
	}
	srv.mu.Lock()
	srv.p.VT.Feed([]byte("alpha\r\nbeta\r\n"))
	srv.sessionLogCaptureLocked()
	srv.mu.Unlock()
	srv.finalizeSessionLog()

	f := filepath.Join(tmp, "session-0.log") // pid 0（cmd 無し）
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("自動パスにログが無い: %v", err)
	}
	if !strings.Contains(string(b), "alpha") {
		t.Fatalf("自動パスログに本文が無い: %q", string(b))
	}
}
