package screen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// M2 核の検証: 自前 VT(pyte 準拠)に実 claude --resume 録画を流し、
// pyte 既知正(expected_visible/history.txt)と byte 一致するか。
// 合成では判定しない。これが本機能(history.top+先頭アンカー)の土台。

func nz(ss []string) []string { // 非空行を順序保持で（正規化）
	var o []string
	for _, s := range ss {
		if t := denoise2(strings.TrimRight(s, " ")); t != "" {
			o = append(o, t)
		}
	}
	return o
}
func denoise2(s string) string { return strings.Join(strings.Fields(s), " ") }

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func orderMatch(want, got []string) (m int) {
	j := 0
	for i := range want {
		for j < len(got) && got[j] != want[i] {
			j++
		}
		if j < len(got) {
			m++
			j++
		}
	}
	return
}

func TestCustomVTMatchesPyteOnRealRecording(t *testing.T) {
	dir := fixtureDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "bytes.bin"))
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	var meta struct{ Width, Height int }
	mb, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	_ = json.Unmarshal(mb, &meta)
	expV := strings.Split(readFile(t, filepath.Join(dir, "expected_visible.txt")), "\n")
	expH := strings.Split(readFile(t, filepath.Join(dir, "expected_history.txt")), "\n")

	v := NewModel(meta.Width, meta.Height)
	v.Feed(data)
	gotV := v.VisibleLines()
	gotH := v.HistoryLines()

	// ---- 可視 buffer ----
	vExact, vNorm := 0, 0
	var vdiff []string
	for i := range expV {
		if i >= len(gotV) {
			break
		}
		w := strings.TrimRight(expV[i], " ")
		g := strings.TrimRight(gotV[i], " ")
		switch {
		case g == w:
			vExact++
			vNorm++
		case denoise2(g) == denoise2(w):
			vNorm++
		default:
			if len(vdiff) < 6 {
				vdiff = append(vdiff, "v"+itoa(i)+" want="+trunc(w)+" got="+trunc(g))
			}
		}
	}
	t.Logf("可視: 完全 %d/%d, 内容一致 %d/%d (x/vt 比: vt10x=49/50)",
		vExact, len(expV), vNorm, len(expV))
	for _, d := range vdiff {
		t.Logf("  DIFF %s", d)
	}

	// ---- history.top（決定的）----
	t.Logf("history 行数: 自前=%d  pyte=%d", len(gotH), len(expH))
	gh, eh := nz(gotH), nz(expH)
	hm := orderMatch(eh, gh)
	rate := 0.0
	if len(eh) > 0 {
		rate = float64(hm) / float64(len(eh))
	}
	t.Logf("history 本文 順序一致: %d/%d (%.1f%%)  非空: 自前=%d pyte=%d",
		hm, len(eh), rate*100, len(gh), len(eh))
	for i := 0; i < 3 && i < len(eh); i++ {
		t.Logf("  pyte[%d]=%q", i, trunc(eh[i]))
	}
	for i := 0; i < 3 && i < len(gh); i++ {
		t.Logf("  self[%d]=%q", i, trunc(gh[i]))
	}

	// 採用ゲート: pyte 準拠移植なので高水準を要求。
	// history 本文 95%+ 順序一致 かつ 可視 内容一致 49/50+。
	if rate < 0.95 {
		t.Fatalf("history 不一致 %.1f%%（<95%%）。VT 移植を実録画で要修正", rate*100)
	}
	if vNorm < 49 {
		t.Fatalf("可視 内容一致 %d/50（<49）。VT 移植を実録画で要修正", vNorm)
	}
}
