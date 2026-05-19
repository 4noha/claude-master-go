package screen

import (
	"strings"
	"testing"
)

// Web コンソールの「ガクガク」回帰: follow 中に claude 出力が増えても
// **画面上の内容開始行が動かない**こと（全消去再描画モデルでは内容の
// 縦位置が毎フレーム変わると全再描画＝判じ込み＝judder＋カーソルが
// js/内部でズレる）。bottom-fill（pad=vrows-len を毎フレーム再計算）は
// 内容成長で先頭行が移動し judder を起こす＝撤去後はこのテストが緑。
// 合成でなく実 VT＋実フレームを VT 再パース（display-oracle）。
func firstNonEmptyRow(frame []byte, cols, rows int) int {
	v := NewModel(cols, rows)
	v.Feed(frame)
	for i, ln := range v.VisibleLines() {
		if strings.TrimRight(ln, " ") != "" {
			return i
		}
	}
	return -1
}

// 実 web 条件: VT モデルは小（claude 端末≈数行）、viewport は大
// （web=500）。total=hist+buf < vrows が常態で、streaming で hist が
// 増え total が成長する。
func TestNoJudderContentRowStableWhileStreaming(t *testing.T) {
	const cols, modelRows, vrows = 20, 4, 12
	r := NewScrollRenderer() // follow（既定）。bottom-fill 撤去後は pad 無
	v := NewModel(cols, modelRows)

	v.Feed([]byte("L1\r\nL2\r\nL3"))
	row1 := firstNonEmptyRow(r.RenderANSI(v, vrows, cols), cols, vrows)
	// 出力増→4 行モデルを溢れ hist が成長（total 増、なお <vrows）
	v.Feed([]byte("\r\nL4\r\nL5\r\nL6\r\nL7"))
	row2 := firstNonEmptyRow(r.RenderANSI(v, vrows, cols), cols, vrows)

	if row1 < 0 || row2 < 0 {
		t.Fatalf("内容行が見つからない row1=%d row2=%d", row1, row2)
	}
	// 内容成長で先頭行が動いてはならない（動く＝判じ込み/ガクガク）。
	if row1 != row2 {
		t.Fatalf("内容開始行が成長で移動＝judder: row1=%d row2=%d "+
			"（bottom-fill が pad を毎フレーム再計算している）", row1, row2)
	}
	// 上詰め＝先頭行は常に画面最上部（row 0）で安定。
	if row1 != 0 {
		t.Fatalf("内容が最上部に無い（row=%d）＝pad 混入", row1)
	}
}
