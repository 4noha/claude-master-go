package ptyproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// M3 Slice1 検証: 実 PTY 配下で子プロセス(=録画を吐く cat)を fork+exec
// し、master を任意チャンクで読んで忠実 VT へ流す。結果が pyte 既知正
// (expected_visible/history.txt)と一致すること＝
//   実 pty fork + 任意チャンク読取 + UTF-8 繰越し + VT
// が end-to-end で正しい。合成でなく実 pty・実録画で検証。

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "..", "..",
		"test", "fixtures", "resume-burst")
}

func denoise(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestProxyForkReadFeed_RealRecordingMatchesPyte(t *testing.T) {
	dir := fixtureDir(t)
	bin := filepath.Join(dir, "bytes.bin")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	var meta struct{ Width, Height int }
	mb, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	_ = json.Unmarshal(mb, &meta)
	expV := strings.Split(readFile(t, filepath.Join(dir, "expected_visible.txt")), "\n")
	expH := strings.Split(readFile(t, filepath.Join(dir, "expected_history.txt")), "\n")

	// raw pty 経由なら cat の出力は録画バイト verbatim（OPOST 無効化済）
	p, err := Start([]string{"/bin/cat", bin}, meta.Width, meta.Height)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Close()
	if err := p.PumpToVT(); err != nil {
		t.Fatalf("PumpToVT: %v", err)
	}
	_ = p.Wait()

	gotV := p.VT.VisibleLines()
	gotH := p.VT.HistoryLines()

	// 可視: 内容一致（vt10x/pyte 基準と同様、空白正規化）
	vNorm := 0
	for i := range expV {
		if i >= len(gotV) {
			break
		}
		if denoise(strings.TrimRight(gotV[i], " ")) ==
			denoise(strings.TrimRight(expV[i], " ")) {
			vNorm++
		}
	}
	// history: 非空・順序一致率
	nz := func(ss []string) []string {
		var o []string
		for _, s := range ss {
			if x := denoise(strings.TrimRight(s, " ")); x != "" {
				o = append(o, x)
			}
		}
		return o
	}
	gh, eh := nz(gotH), nz(expH)
	m, j := 0, 0
	for i := range eh {
		for j < len(gh) && gh[j] != eh[i] {
			j++
		}
		if j < len(gh) {
			m++
			j++
		}
	}
	rate := 0.0
	if len(eh) > 0 {
		rate = float64(m) / float64(len(eh))
	}
	t.Logf("実 pty fork+chunk read: 可視 内容一致 %d/%d, history 順序一致 "+
		"%d/%d (%.1f%%), hist行数 self=%d pyte=%d",
		vNorm, len(expV), m, len(eh), rate*100, len(gh), len(eh))

	if vNorm < 49 {
		t.Fatalf("可視 内容一致 %d/50（<49）", vNorm)
	}
	if rate < 0.95 {
		t.Fatalf("history 順序一致 %.1f%%（<95%%）", rate*100)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
