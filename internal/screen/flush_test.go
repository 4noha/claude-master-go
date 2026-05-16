package screen

import (
	"strconv"
	"strings"
	"testing"
)

// HistoryFlusher 単体（決定的・実 VT モデルへ番号行を流して history.top
// を作る＝合成 ANSI でなく忠実モデルの確定行）。Python pty_scroll の
// HistoryFlusher 仕様（arm で既存 skip / delta 抽出 / drain で空 /
// reset は pending 保持・identity 再 arm）を機械担保。

func feedLines(v *VT, prefix string, lo, hi int) {
	parts := make([]string, 0, hi-lo)
	for i := lo; i < hi; i++ {
		parts = append(parts, prefix+strconv.Itoa(i))
	}
	v.Feed([]byte(strings.Join(parts, "\r\n") + "\r\n"))
}

func TestHistoryFlusherArmSkipsExistingThenDelta(t *testing.T) {
	v := NewModel(20, 3)
	feedLines(v, "A", 0, 10) // 既存 backlog を作る
	if v.HistLen() == 0 {
		t.Fatal("前提: history.top が出来ていない")
	}
	f := &HistoryFlusher{}
	f.Capture(v) // 初回 = arm。既存 backlog は流さない
	if f.HasPending() {
		t.Fatalf("arm 時に既存 backlog を流した: %d", f.PendingLen())
	}
	base := v.HistTotal()
	feedLines(v, "A", 10, 30) // claude が出力継続
	f.Capture(v)
	got := f.Drain()
	if f.HasPending() {
		t.Fatal("drain 後も pending が残る")
	}
	// drain 内容は「arm 以降に確定した行」＝ HistoryLines の該当区間と
	// 完全一致（dedup/分類なし＝忠実モデルそのもの）。
	hl := v.HistoryLines()
	firstIdx := v.HistTotal() - len(hl)
	want := hl[base-firstIdx:]
	if len(got) != len(want) {
		t.Fatalf("delta 行数不一致 got=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delta[%d] 不一致 got=%q want=%q", i, got[i], want[i])
		}
	}
	if len(got) == 0 || !strings.HasPrefix(got[0], "A") {
		t.Fatalf("確定行が想定外: %d 行先頭=%q", len(got), firstOr(got))
	}
}

func firstOr(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func TestHistoryFlusherResetKeepsPendingReArms(t *testing.T) {
	v := NewModel(20, 3)
	f := &HistoryFlusher{}
	f.Capture(v) // arm（空）
	feedLines(v, "B", 0, 15)
	f.Capture(v)
	if !f.HasPending() {
		t.Fatal("確定行が pending に入っていない")
	}
	n := f.PendingLen()
	f.Reset() // identity 再 arm。pending は保持（Python と同一）
	if f.PendingLen() != n {
		t.Fatalf("reset で pending が消えた: %d -> %d", n, f.PendingLen())
	}
	feedLines(v, "B", 15, 25)
	f.Capture(v) // 再 arm 直後 = 既存 skip。pending は保持のまま
	if f.PendingLen() != n {
		t.Fatalf("reset 後の再 arm で skip されず増減: %d -> %d", n, f.PendingLen())
	}
	if got := f.Drain(); len(got) != n {
		t.Fatalf("保持 pending が drain で取れない: %d", len(got))
	}
}

// Python test_line_to_text_plain_and_widechar 同値: 全角継続セルで割れ
// ない・末尾空白除去・ANSI 無し・空行安全（lineText は line_to_text 移植）。
func TestLineTextPlainAndWidechar(t *testing.T) {
	v := NewModel(12, 2)
	v.Feed([]byte("ねこ ab  \r\n"))
	got := v.VisibleLines()[0]
	if got != "ねこ ab" {
		t.Fatalf("widechar 行転写が不正: %q", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ANSI が混入: %q", got)
	}
	if strings.Contains(got, "ね こ") {
		t.Fatalf("全角継続セルで割れている: %q", got)
	}
	if s := lineText(make([]cell, 5)); s != "" {
		t.Fatalf("空行が空文字でない: %q", s)
	}
}
