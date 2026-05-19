//go:build windows

package ptyproxy

import (
	"strings"
	"testing"
	"time"
)

// M8b 実テスト（鉄則#2: 合成でなく実 ConPTY＋実プログラム＋実 VT
// モデル）。生 x/sys 直叩きでなく UserExistsError/conpty backend で
// 実 cmd.exe を pseudoconsole 配下に起動し、Start→master→PumpToVT→
// screen.VT の本番経路が Windows で end-to-end に動くことを機械確認。
//
// 注意（DESIGN_M8）: ConPTY は unix PTY と異なりバイト透過でなく端末
// として再レンダリングする。よって unix の resume-burst pyte しきい値
// （proxy_test.go）は Windows へ直接適用しない。ここは「実 ConPTY 出力
// が VT モデルへ忠実に流れ既知文字列が描画される」ことを検証する。
func TestConPTYRealProgram_FeedsVTModel(t *testing.T) {
	// echo の本文を ConPTY が pump し終える前に子終了しないよう ping で
	// ~1s 生かす（PoC で確認済の ConPTY フラッシュ特性）。
	argv := []string{
		`C:\Windows\System32\cmd.exe`, "/c",
		"echo M8B_OK& ver& ping -n 2 127.0.0.1 >NUL",
	}
	p, err := Start(argv, 80, 25)
	if err != nil {
		t.Fatalf("Start(ConPTY backend): %v", err)
	}
	defer p.Close()

	done := make(chan error, 1)
	go func() { done <- p.PumpToVT() }()
	select {
	case e := <-done:
		if e != nil {
			t.Fatalf("PumpToVT: %v", e)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("PumpToVT timeout（ConPTY backend hang の疑い）")
	}
	_ = p.Wait()

	vis := strings.Join(p.VT.VisibleLines(), "\n")
	hist := strings.Join(p.VT.HistoryLines(), "\n")
	all := vis + "\n" + hist
	if !strings.Contains(all, "M8B_OK") {
		t.Fatalf("VT に実 ConPTY 出力 M8B_OK が描画されていない。\nvisible=%q\nhistory=%q", vis, hist)
	}
	t.Logf("ConPTY→Start→PumpToVT→screen.VT OK: M8B_OK 検出 "+
		"(visible %d 行 / history %d 行)",
		len(p.VT.VisibleLines()), len(p.VT.HistoryLines()))
}
