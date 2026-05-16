package screen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// M2 評価ゲート: 実 claude --resume 録画(resume-burst)を vt10x に流し、
// pyte が出した既知正(expected_visible.txt)と可視 buffer が一致するか。
// これが十分一致すれば vt10x を VT 基盤に採用し history 層を自前で被せる。
// 不一致が大きければ別ライブラリ/自前 VT へ（合成では判定しない）。

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	// internal/screen/ から repo ルートへ → test/fixtures/resume-burst
	root := filepath.Join(filepath.Dir(self), "..", "..")
	return filepath.Join(root, "test", "fixtures", "resume-burst")
}

func TestVT10xVisibleMatchesPyteOnRealRecording(t *testing.T) {
	dir := fixtureDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "bytes.bin"))
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	var meta struct{ Width, Height int }
	mb, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mb, &meta); err != nil {
		t.Fatal(err)
	}
	exp, err := os.ReadFile(filepath.Join(dir, "expected_visible.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(string(exp), "\n")

	v := NewVT(meta.Width, meta.Height)
	v.Feed(data)
	got := v.VisibleLines()

	if len(got) != len(want) {
		t.Fatalf("行数不一致: got %d want %d", len(got), len(want))
	}

	denoise := func(s string) string { // 空白/全角差を均す
		return strings.Join(strings.Fields(s), " ")
	}
	exact, normEq, chromeOK := 0, 0, 0
	var garbled []string
	for i := range want {
		w := strings.TrimRight(want[i], " ")
		g := strings.TrimRight(got[i], " ")
		switch {
		case g == w:
			exact++
			normEq++
		case denoise(g) == denoise(w):
			normEq++ // 空白/全角の描画差のみ（内容一致）
		case w != "" && strings.HasPrefix(denoise(g), denoise(w)):
			// vt10x が pyte 本文を接頭辞に含み末尾に余分（入力枠 chrome の
			// 1行ズレ。本文は破損しておらず scrollback には影響しない）
			chromeOK++
			t.Logf("CHROME row %d: want=%q got=%q（本文prefix一致・許容）",
				i, trunc(w), trunc(g))
		default:
			garbled = append(garbled,
				"row "+itoa(i)+": want="+trunc(w)+" got="+trunc(g))
		}
	}
	total := len(want)
	t.Logf("M2 eval (resume-burst %dx%d): 完全一致 %d/%d, 内容一致(正規化) %d/%d, "+
		"入力枠 chrome 許容 %d, 破損 %d",
		meta.Width, meta.Height, exact, total, normEq, total,
		chromeOK, len(garbled))
	for _, d := range garbled {
		t.Logf("GARBLED %s", d)
	}

	// 原則ゲート（実録画 vs pyte）:
	//  - 本文行は内容一致（normEq）。会話/ログ＝scrollback に乗る内容が忠実。
	//  - 残差は「pyte 本文を接頭辞に含む入力枠 chrome の行ズレ」のみ許容
	//    （live の絶対座標描画差。Python 版でも上位の mini-tmux 再描画＋
	//    カーソル規律で吸収していた領域）。
	//  - それ以外（本文の破損/取り違え）は 0 でなければ不採用。
	if len(garbled) != 0 {
		t.Fatalf("vt10x 不採用: 本文破損 %d 行（x/vt or 自前 VT 評価へ）",
			len(garbled))
	}
	if normEq+chromeOK != total {
		t.Fatalf("分類漏れ: normEq=%d chrome=%d total=%d", normEq, chromeOK, total)
	}
	if chromeOK > 3 {
		t.Fatalf("入力枠 chrome ズレが %d 行（>3）。live 描画差が大きすぎ"+
			"＝vt10x 再評価", chromeOK)
	}
	t.Logf("判定: vt10x 採用可（本文 %d/%d 忠実・chrome ズレ %d 行のみ）。"+
		"次: history.top + 先頭アンカー層", normEq, total, chromeOK)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func trunc(s string) string {
	r := []rune(s)
	if len(r) > 90 {
		return string(r[:90]) + "…"
	}
	return s
}
