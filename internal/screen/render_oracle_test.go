package screen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Slice2a 検証: SGR 付き ANSI 描画器の正しさを
//  (1) 描画 ANSI を新 VT へ再パース → テキスト往復一致
//  (2) truecolor(\x1b[38;2;) と同期出力(\x1b[?2026h) が保持される
// で機械担保（合成でなく実 resume-burst 録画）。テキストオラクルは
// 別テストで非回帰確認済。

func TestRenderANSIRoundTripAndColorOnRealRecording(t *testing.T) {
	dir := fixtureDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "bytes.bin"))
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	var meta struct{ Width, Height int }
	mb, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	_ = json.Unmarshal(mb, &meta)
	W, H := meta.Width, meta.Height

	v := NewModel(W, H)
	v.Feed(data)
	want := v.VisibleLines()

	r := NewScrollRenderer() // follow（最下部=可視 buffer）
	frame := r.RenderANSI(v, H, W)

	// (2) 色/同期が保持されている
	fs := string(frame)
	if !strings.Contains(fs, "\x1b[?2026h") || !strings.Contains(fs, "\x1b[?2026l") {
		t.Fatalf("同期出力ブラケットが無い")
	}
	if !strings.Contains(fs, "\x1b[38;2;") {
		t.Fatalf("truecolor が剥がれている（色が保持されていない）")
	}
	if !strings.Contains(fs, "\x1b[2J\x1b[9999;1H") {
		t.Fatalf("_CLEAR_SEQ 規律（全消去→最下部）が無い")
	}

	// (1) 往復: 描画 ANSI を新 VT へ流し直し、可視テキストが一致
	v2 := NewModel(W, H)
	v2.Feed(frame)
	got := v2.VisibleLines()

	if len(got) != len(want) {
		t.Fatalf("往復 行数不一致 got=%d want=%d", len(got), len(want))
	}
	mism := 0
	var first []string
	for i := range want {
		w := denoise2(strings.TrimRight(want[i], " "))
		g := denoise2(strings.TrimRight(got[i], " "))
		if w != g {
			mism++
			if len(first) < 5 {
				first = append(first, "row"+itoa(i)+" want="+trunc(w)+" got="+trunc(g))
			}
		}
	}
	t.Logf("ANSI 往復: 可視 %d/%d 行一致（不一致 %d）", len(want)-mism, len(want), mism)
	for _, d := range first {
		t.Logf("  %s", d)
	}
	if mism != 0 {
		t.Fatalf("描画 ANSI 再パースでテキストが %d 行不一致（描画器/SGRパーサ不整合）", mism)
	}
}
