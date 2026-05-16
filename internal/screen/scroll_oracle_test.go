package screen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Python 版で確立した不変条件を実録画で Go 再現:
//  1) 全 scrollback offset で viewport == 論理 canvas の該当スライス
//     （Python: resume-burst 全 507 offset 完全一致）
//  2) 遡り中に claude が出力し canvas が伸びても先頭行が動かない
//     （先頭アンカー。最下部基準だと「複数バッファ混ざる」実バグ）
//  3) 最下部復帰で follow＝最新可視

func loadRB(t *testing.T) (*VT, int, int) {
	t.Helper()
	dir := fixtureDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "bytes.bin"))
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	var meta struct{ Width, Height int }
	mb, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	_ = json.Unmarshal(mb, &meta)
	v := NewModel(meta.Width, meta.Height)
	v.Feed(data)
	return v, meta.Width, meta.Height
}

func TestScrollAllOffsetsMatchCanvas(t *testing.T) {
	v, _, h := loadRB(t)
	hist, vis := Canvas(v)
	total := len(hist) + len(vis)
	maxOy := total - h
	if maxOy < 0 {
		maxOy = 0
	}
	canvas := append(append([]string{}, hist...), vis...)

	bad := 0
	var first []string
	for oy := 0; oy <= maxOy; oy++ {
		r := NewScrollRenderer()
		r.ViewLines(hist, vis, h) // follow で lastMaxOy 確定
		r.Scroll(-(maxOy - oy))   // 目標 oy へ（先頭からの絶対位置）
		got := r.ViewLines(hist, vis, h)
		// 期待 = canvas[oy:oy+h]
		var exp []string
		for k := 0; k < h && oy+k < total; k++ {
			exp = append(exp, canvas[oy+k])
		}
		if !eqLines(got, exp) {
			bad++
			if len(first) < 5 {
				first = append(first, "oy="+itoa(oy)+" 先頭 want="+
					trunc(at(exp, 0))+" got="+trunc(at(got, 0)))
			}
		}
	}
	t.Logf("全 offset 照合: %d offsets 中 不一致 %d（Python は 0）", maxOy+1, bad)
	for _, d := range first {
		t.Logf("  %s", d)
	}
	if bad != 0 {
		t.Fatalf("viewport が論理 canvas と %d offset で不一致", bad)
	}
}

func TestScrollAnchorNoDriftWhileFeeding(t *testing.T) {
	dir := fixtureDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "bytes.bin"))
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	var meta struct{ Width, Height int }
	mb, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	_ = json.Unmarshal(mb, &meta)
	W, H := meta.Width, meta.Height

	// 録画を 2 分割し、前半 feed → 遡り → 後半 feed（history 成長）
	v := NewModel(W, H)
	half := len(data) / 2
	v.Feed(data[:half])
	r := NewScrollRenderer()
	hist, vis := Canvas(v)
	r.ViewLines(hist, vis, H) // follow で lastMaxOy 確定
	r.Scroll(-60)             // ユーザーが 60 行遡る（以後動かさない）
	if r.FollowActive() {
		t.Skip("前半 feed では history 不足で follow clamp（録画依存）")
	}
	h2, vi2 := Canvas(v)
	anchored := at(r.ViewLines(h2, vi2, H), 0)

	// claude が出力し続ける（history が伸びる）。ユーザーは不動。
	step := (len(data) - half) / 30
	if step < 1 {
		step = 1
	}
	off := half
	drift := 0
	for k := 0; k < 30 && off < len(data); k++ {
		end := off + step
		if end > len(data) {
			end = len(data)
		}
		v.Feed(data[off:end])
		off = end
		hh, vv := Canvas(v)
		cur := at(r.ViewLines(hh, vv, H), 0) // 操作なしで再描画
		if cur != anchored {
			drift++
		}
	}
	t.Logf("遡り中 30 回 feed・先頭行ドリフト: %d（先頭アンカーなら 0）", drift)
	if drift != 0 {
		t.Fatalf("claude 出力で表示がドリフト（先頭アンカー不全）: %d", drift)
	}

	// 最下部へ戻れば follow＝最新可視が見える
	r.FollowBottom()
	hf, vf := Canvas(v)
	out := r.ViewLines(hf, vf, H)
	last := ""
	for _, l := range out {
		if strings.TrimSpace(l) != "" {
			last = l
		}
	}
	if !strings.Contains(strings.Join(out, "\n"), "⏵⏵") &&
		!strings.Contains(last, "❯") && last == "" {
		t.Fatalf("follow 復帰で最新可視が出ない")
	}
}

// --- helpers ---

func eqLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if denoise2(strings.TrimRight(a[i], " ")) !=
			denoise2(strings.TrimRight(b[i], " ")) {
			return false
		}
	}
	return true
}
func at(ss []string, i int) string {
	if i < len(ss) {
		return ss[i]
	}
	return ""
}

