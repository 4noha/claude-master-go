package screen

import (
	"os"
	"path/filepath"
	"testing"
)

// 合成 green は作らない。実 resume-burst 録画で:
//   - IsActive=true（録画末尾の footer に "esc to interrupt" がある）
//   - ExtractUsage any=false（録画に使用量 footer が無い＝実 negative。
//     false positive を出さないことの実データ検証）
// を担保する。usage footer を含む実録画資産が無いため正例の usage 抽出
// は regex 1:1 移植（_USAGE_RE/_RESET_RE）に留め、合成では緑にしない。

func TestIsActiveOnRealRecording(t *testing.T) {
	dir := fixtureDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "bytes.bin"))
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	v := NewModel(164, 50)
	v.Feed(data)
	if !IsActive(v) {
		// 末尾可視が "esc to interrupt" を含む footer のはず
		vis := v.VisibleLines()
		t.Fatalf("実録画末尾が active footer なのに IsActive=false。末尾3行=%q",
			vis[len(vis)-3:])
	}
}

func TestExtractUsageRealRecordingNegative(t *testing.T) {
	dir := fixtureDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "bytes.bin"))
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	v := NewModel(164, 50)
	v.Feed(data)
	sc := &StatusScanner{}
	pct, hasPct, rt, rtz, found := sc.ExtractUsage(v)
	if found || hasPct || pct != 0 || rt != "" || rtz != "" {
		t.Fatalf("使用量 footer の無い実録画で false positive: "+
			"found=%v pct=%d(%v) rt=%q rtz=%q", found, pct, hasPct, rt, rtz)
	}
}

// 空画面は非アクティブ（実データの自明真。active footer 不在⇒false）。
func TestIsActiveEmptyModel(t *testing.T) {
	if IsActive(NewModel(80, 24)) {
		t.Fatal("空モデルを active と誤判定")
	}
}
