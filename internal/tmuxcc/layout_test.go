package tmuxcc

import (
	"reflect"
	"testing"
)

// TestParseLayout_SinglePane: 単一 leaf pane の最小ケース。
func TestParseLayout_SinglePane(t *testing.T) {
	// "5b53,30x100,0,0,0" → 30x100 bbox、pane id 0
	l, err := ParseLayout("5b53,30x100,0,0,0")
	if err != nil {
		t.Fatal(err)
	}
	if l.W != 30 || l.H != 100 || l.X != 0 || l.Y != 0 {
		t.Fatalf("bbox: %+v", l)
	}
	if !l.IsLeaf() {
		t.Fatal("expected leaf")
	}
	if l.PaneID != "%0" {
		t.Fatalf("pane id: %q", l.PaneID)
	}
}

// TestParseLayout_HorizontalSplit: 横分割 (左右並び)。
func TestParseLayout_HorizontalSplit(t *testing.T) {
	// "5b53,30x100,0,0{20x100,0,0,0,9x100,21,0,1}" → 左 (20x100 @ 0,0 pane 0)
	// と右 (9x100 @ 21,0 pane 1)
	l, err := ParseLayout("5b53,30x100,0,0{20x100,0,0,0,9x100,21,0,1}")
	if err != nil {
		t.Fatal(err)
	}
	if l.IsLeaf() {
		t.Fatal("expected split")
	}
	if l.Vertical {
		t.Fatal("expected horizontal (Vertical=false)")
	}
	if len(l.Children) != 2 {
		t.Fatalf("children: %d", len(l.Children))
	}
	c0 := l.Children[0]
	if c0.W != 20 || c0.H != 100 || c0.X != 0 || c0.PaneID != "%0" {
		t.Fatalf("child[0]: %+v", c0)
	}
	c1 := l.Children[1]
	if c1.W != 9 || c1.H != 100 || c1.X != 21 || c1.PaneID != "%1" {
		t.Fatalf("child[1]: %+v", c1)
	}
}

// TestParseLayout_VerticalSplit: 縦分割 (上下並び)。
func TestParseLayout_VerticalSplit(t *testing.T) {
	l, err := ParseLayout("5b53,100x30,0,0[100x14,0,0,0,100x15,0,15,1]")
	if err != nil {
		t.Fatal(err)
	}
	if !l.Vertical {
		t.Fatal("expected Vertical")
	}
	if len(l.Children) != 2 {
		t.Fatalf("children: %d", len(l.Children))
	}
	if l.Children[0].PaneID != "%0" || l.Children[1].PaneID != "%1" {
		t.Fatalf("pane ids: %q, %q", l.Children[0].PaneID, l.Children[1].PaneID)
	}
}

// TestParseLayout_Nested: 縦分割の中に横分割 (3 pane: 上に 1、下に左右 2)。
func TestParseLayout_Nested(t *testing.T) {
	// "5b53,100x30,0,0[100x14,0,0,0,100x15,0,15{50x15,0,15,1,50x15,51,15,2}]"
	l, err := ParseLayout("5b53,100x30,0,0[100x14,0,0,0,100x15,0,15{50x15,0,15,1,50x15,51,15,2}]")
	if err != nil {
		t.Fatal(err)
	}
	if !l.Vertical {
		t.Fatal("expected outer vertical")
	}
	if len(l.Children) != 2 {
		t.Fatalf("outer children: %d", len(l.Children))
	}
	// 1 番目は leaf pane 0
	if l.Children[0].PaneID != "%0" {
		t.Fatalf("child[0]: %+v", l.Children[0])
	}
	// 2 番目は horizontal split (pane 1 と pane 2)
	if l.Children[1].IsLeaf() {
		t.Fatal("child[1] should be split")
	}
	if l.Children[1].Vertical {
		t.Fatal("child[1] should be horizontal")
	}
	if len(l.Children[1].Children) != 2 {
		t.Fatalf("inner children: %d", len(l.Children[1].Children))
	}
	if l.Children[1].Children[0].PaneID != "%1" || l.Children[1].Children[1].PaneID != "%2" {
		t.Fatalf("inner pane ids: %+v", l.Children[1].Children)
	}
}

// TestLeafPanes_Order: tree から pane id 一覧が DFS 順で取れる。
func TestLeafPanes_Order(t *testing.T) {
	l, _ := ParseLayout("5b53,100x30,0,0[100x14,0,0,0,100x15,0,15{50x15,0,15,1,50x15,51,15,2}]")
	got := l.LeafPanes()
	want := []string{"%0", "%1", "%2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LeafPanes: got=%v want=%v", got, want)
	}
}

// TestParseLayout_Errors: 不正 input は error 返す (panic しない)。
func TestParseLayout_Errors(t *testing.T) {
	for _, in := range []string{
		"",
		"abc",
		"5b53,30x100",     // bbox 不完全
		"5b53,30x100,0,0{", // unbalanced
		"5b53,30x100,0,0[", // unbalanced
	} {
		if _, err := ParseLayout(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}
