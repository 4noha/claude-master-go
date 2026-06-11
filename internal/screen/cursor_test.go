package screen

import (
	"regexp"
	"strings"
	"testing"
)

// 末尾の同期終了直前に出るカーソル復元 CUP を取り出す（無ければ ""）。
// 復元時は `\x1b[r;cH\x1b[?25h\x1b[?2026l` の順（show cursor を伴う）。
var cupRe = regexp.MustCompile(`\x1b\[(\d+);(\d+)H\x1b\[\?25h\x1b\[\?2026l$`)

func trailingCUP(frame []byte) string {
	m := cupRe.FindSubmatch(frame)
	if m == nil {
		return ""
	}
	return string(m[1]) + ";" + string(m[2])
}

// RenderANSI はフレーム末尾で物理カーソルを VT モデルのカーソル位置へ
// 復元しなければならない（しないと右下に残り IME preedit がそこへ出て
// 日本語入力不能）。全角は表示桁で数える（rune 数ではない）こと。
func TestRenderANSIRestoresCursorIncludingWideChars(t *testing.T) {
	s := NewScrollRenderer()

	// 半角: CUP(2,1) 後 "abc" → cx=3,cy=1 → 期待 \x1b[2;4H
	v1 := NewModel(20, 5)
	v1.Feed([]byte("\x1b[2;1Habc"))
	if x, y := v1.Cursor(); x != 3 || y != 1 {
		t.Fatalf("前提崩れ(半角) cx=%d cy=%d", x, y)
	}
	if got := trailingCUP(s.RenderANSI(v1, 5, 20)); got != "2;4" {
		t.Fatalf("半角カーソル復元が誤り: got=%q want=2;4", got)
	}

	// 全角: CUP(3,1) 後 "あいう"(各 2 桁) → cx=6,cy=2 → 期待 \x1b[3;7H
	// （rune 数 3 で数えると誤って 3;4 になる＝表示桁で数える検証）。
	s2 := NewScrollRenderer()
	v2 := NewModel(20, 5)
	v2.Feed([]byte("\x1b[3;1Hあいう"))
	if x, y := v2.Cursor(); x != 6 || y != 2 {
		t.Fatalf("前提崩れ(全角) cx=%d cy=%d（runewidth 反映?）", x, y)
	}
	got := trailingCUP(s2.RenderANSI(v2, 5, 20))
	if got != "3;7" {
		t.Fatalf("全角カーソル復元が誤り: got=%q want=3;7（rune 数なら誤 3;4）", got)
	}

	// フレーム構造（同期囲い・クリア規律・cursor hide/show 規律）が
	// 壊れていないこと。BSU 直後の ?25l は sync 非対応外側端末経由
	// (例: tmux + VSCode) でフレーム描画中の cursor 散らかりを防ぐ
	// 必須要素なので不変条件に含める。
	frame := string(s2.RenderANSI(v2, 5, 20))
	if !strings.HasPrefix(frame, "\x1b[?2026h\x1b[?25l\x1b[2J\x1b[9999;1H\x1b[H") ||
		!strings.HasSuffix(frame, "\x1b[?25h\x1b[?2026l") {
		t.Fatalf("フレーム構造が崩れた: %q", frame)
	}
}

// nav 遡り中（カーソル行が viewport 外）は従来どおりカーソル復元を
// 出さない（読書中で IME 非使用。出すと遡り表示上で誤位置になる）。
// 加えて cursor も hide のまま ESU を出す（`?25h` を出さない＝読書中の
// 不要な cursor 表示を抑止。次フレーム live 復帰時に自動で show）。
func TestRenderANSINoCursorWhenScrolledOffViewport(t *testing.T) {
	s := NewScrollRenderer()
	v := NewModel(20, 4)
	// 4 行画面に 30 行流して history を作る（カーソルは最下部付近）。
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("line\r\n")
	}
	v.Feed([]byte(sb.String()))
	s.RenderANSI(v, 4, 20) // lastMaxOy/lastOy を確定
	s.Scroll(-100)         // 最上部まで遡る（follow 解除）
	frame := s.RenderANSI(v, 4, 20)
	if got := trailingCUP(frame); got != "" {
		t.Fatalf("viewport 外なのにカーソル復元を出した: %q", got)
	}
	if !strings.HasSuffix(string(frame), "\x1b[?2026l") {
		t.Fatalf("同期終了が無い: %q", string(frame))
	}
	// nav scrolled-off では `?25h` を出さない（hide のまま）。
	if strings.Contains(string(frame), "\x1b[?25h") {
		t.Fatalf("nav scrolled-off で cursor show を出した: %q", string(frame))
	}
	// 一方で `?25l`（フレーム冒頭の hide）は必ず出る。
	if !strings.Contains(string(frame), "\x1b[?25l") {
		t.Fatalf("フレーム冒頭の cursor hide が無い: %q", string(frame))
	}
}

// VT が claude の DECSET 2026（同期出力）を状態として追跡すること
// （セル内容には従来どおり非影響）。実録画で on/off 遷移回数も機械確認。
func TestVTSyncActiveTracking(t *testing.T) {
	v := NewModel(80, 24)
	if v.SyncActive() {
		t.Fatal("初期状態で sync active")
	}
	v.Feed([]byte("\x1b[?2026h"))
	if !v.SyncActive() {
		t.Fatal("BSU 後に active でない")
	}
	v.Feed([]byte("\x1b[2J\x1b[HHello"))
	if !v.SyncActive() {
		t.Fatal("sync 中の描画で状態が落ちた")
	}
	v.Feed([]byte("\x1b[?2026l"))
	if v.SyncActive() {
		t.Fatal("ESU 後に active のまま")
	}
	// チャンク境界でシーケンスが割れても追跡できる（pend 繰越し）
	v.Feed([]byte("\x1b[?20"))
	v.Feed([]byte("26h"))
	if !v.SyncActive() {
		t.Fatal("分割 BSU を追跡できない")
	}
	v.Feed([]byte("\x1b[?2026;1l")) // 複数パラメータでも 2026 を拾う
	if v.SyncActive() {
		t.Fatal("複数パラメータ ESU を追跡できない")
	}
	// 他の private mode はセル/状態に無影響
	v.Feed([]byte("\x1b[?25l\x1b[?2004h"))
	if v.SyncActive() {
		t.Fatal("無関係 mode で active になった")
	}
}
