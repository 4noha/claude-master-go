package tmuxcc

import (
	"bytes"
	"strings"
	"testing"
)

// TestDecodeOctalEscapes_Basic: tmux -CC が emit する代表的 octal escape
// (ESC=\033, CR=\015, LF=\012, BS=\010) を生 bytes に戻す。PoC fixture
// と一致。
func TestDecodeOctalEscapes_Basic(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		{`\033[?2026h`, []byte{0x1b, '[', '?', '2', '0', '2', '6', 'h'}},
		{`hello\015\012`, []byte{'h', 'e', 'l', 'l', 'o', 0x0d, 0x0a}},
		{`\033[31mRED\033[0m`, []byte("\x1b[31mRED\x1b[0m")},
		{`plain text`, []byte("plain text")},
		{``, []byte{}},
		{`\\path\\to\\file`, []byte(`\path\to\file`)},
		{`\000`, []byte{0x00}},
		{`\177`, []byte{0x7f}},
	}
	for _, c := range cases {
		got, err := DecodeOctalEscapes(c.in)
		if err != nil {
			t.Fatalf("err for %q: %v", c.in, err)
		}
		if !bytes.Equal(got, c.want) {
			t.Fatalf("decode %q\n got=%q\nwant=%q", c.in, got, c.want)
		}
	}
}

// TestDecodeOctalEscapes_PoCFixture: 実 PoC で取った %output 行の data
// 部分を decode して proxy frame の構造 (BSU + SGR + cells + ESU) が
// 復元されることを確認。
func TestDecodeOctalEscapes_PoCFixture(t *testing.T) {
	// PoC で取った data: \033[?2026h\033[31mhello %S.%N\033[0m\033[?2026l\015\012
	in := `\033[?2026h\033[31mhello %S.%N\033[0m\033[?2026l\015\012`
	got, err := DecodeOctalEscapes(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("\x1b[?2026h\x1b[31mhello %S.%N\x1b[0m\x1b[?2026l\r\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("PoC fixture mismatch\n got=%q\nwant=%q", got, want)
	}
	// 中身に BSU/ESU が含まれることを確認
	if !bytes.Contains(got, []byte("\x1b[?2026h")) {
		t.Fatal("BSU not preserved")
	}
	if !bytes.Contains(got, []byte("\x1b[?2026l")) {
		t.Fatal("ESU not preserved")
	}
}

func TestDecodeOctalEscapes_Errors(t *testing.T) {
	// trailing backslash
	if _, err := DecodeOctalEscapes(`abc\`); err == nil {
		t.Fatal("expected error for trailing backslash")
	}
	// incomplete octal
	if _, err := DecodeOctalEscapes(`\03`); err == nil {
		t.Fatal("expected error for incomplete octal")
	}
}

// TestParseLine_Output: %output 行を parse して PaneID と decoded Data
// を確認。
func TestParseLine_Output(t *testing.T) {
	line := `%output %0 \033[31mred\033[0m\015\012` + "\r\n"
	m, err := ParseLine(line)
	if err != nil {
		t.Fatal(err)
	}
	om, ok := m.(*OutputMsg)
	if !ok {
		t.Fatalf("not OutputMsg: %T", m)
	}
	if om.PaneID != "%0" {
		t.Fatalf("pane id: %q", om.PaneID)
	}
	want := []byte("\x1b[31mred\x1b[0m\r\n")
	if !bytes.Equal(om.Data, want) {
		t.Fatalf("data mismatch\n got=%q\nwant=%q", om.Data, want)
	}
}

// TestParseLine_Begin_End_Session: handshake/応答の境界 msg。
func TestParseLine_Begin_End_Session(t *testing.T) {
	tests := []struct {
		in   string
		want Msg
	}{
		{"%begin 1234 5 6\r\n", &BeginMsg{Time: "1234", Num: "5", Flags: "6"}},
		{"%end 1234 5 6\r\n", &EndMsg{Time: "1234", Num: "5", Flags: "6"}},
		{"%session-changed $0 main\r\n", &SessionChangedMsg{ID: "$0", Name: "main"}},
		{"%window-add @3\r\n", &WindowAddMsg{WindowID: "@3"}},
		{"%window-close @5\r\n", &WindowCloseMsg{WindowID: "@5"}},
		{"%window-renamed @3 foo\r\n", &WindowRenameMsg{WindowID: "@3", Name: "foo"}},
		{"%exit normal\r\n", &ExitMsg{Reason: "normal"}},
		{"%unknown-thing data here\r\n", &OtherMsg{Type: "%unknown-thing", Rest: "data here"}},
	}
	for _, tc := range tests {
		got, err := ParseLine(tc.in)
		if err != nil {
			t.Fatalf("err %q: %v", tc.in, err)
		}
		if !sameMsg(got, tc.want) {
			t.Fatalf("ParseLine %q\n got=%+v (%T)\nwant=%+v (%T)", tc.in, got, got, tc.want, tc.want)
		}
	}
}

// TestParseLine_Layout: %layout-change の 3 フィールド分割。
func TestParseLine_Layout(t *testing.T) {
	m, err := ParseLine("%layout-change @0 f30x100,0,0,0 b3c5\r\n")
	if err != nil {
		t.Fatal(err)
	}
	lm, ok := m.(*LayoutChangeMsg)
	if !ok {
		t.Fatalf("not LayoutChangeMsg: %T", m)
	}
	if lm.WindowID != "@0" || lm.Layout != "f30x100,0,0,0" || lm.Extra != "b3c5" {
		t.Fatalf("layout fields: %+v", lm)
	}
}

// TestParseLine_NonControl: 非 % 行 (空行や応答本文) は (nil, nil)。
func TestParseLine_NonControl(t *testing.T) {
	for _, in := range []string{"", "\r\n", "hello world\r\n", "data"} {
		m, err := ParseLine(in)
		if err != nil {
			t.Fatalf("unexpected err for %q: %v", in, err)
		}
		if m != nil {
			t.Fatalf("expected nil msg for %q, got %+v", in, m)
		}
	}
}

func sameMsg(a, b Msg) bool {
	// 単純比較: type + 内部 field を string 化して compare
	type stringer interface {
		String() string
	}
	if a == nil || b == nil {
		return a == b
	}
	// 型ベース手動比較
	switch ax := a.(type) {
	case *BeginMsg:
		bx, ok := b.(*BeginMsg)
		return ok && *ax == *bx
	case *EndMsg:
		bx, ok := b.(*EndMsg)
		return ok && *ax == *bx
	case *SessionChangedMsg:
		bx, ok := b.(*SessionChangedMsg)
		return ok && *ax == *bx
	case *WindowAddMsg:
		bx, ok := b.(*WindowAddMsg)
		return ok && *ax == *bx
	case *WindowCloseMsg:
		bx, ok := b.(*WindowCloseMsg)
		return ok && *ax == *bx
	case *WindowRenameMsg:
		bx, ok := b.(*WindowRenameMsg)
		return ok && *ax == *bx
	case *ExitMsg:
		bx, ok := b.(*ExitMsg)
		return ok && *ax == *bx
	case *OtherMsg:
		bx, ok := b.(*OtherMsg)
		return ok && *ax == *bx
	case *OutputMsg:
		bx, ok := b.(*OutputMsg)
		return ok && ax.PaneID == bx.PaneID && bytes.Equal(ax.Data, bx.Data)
	case *LayoutChangeMsg:
		bx, ok := b.(*LayoutChangeMsg)
		return ok && *ax == *bx
	}
	_ = strings.Builder{}
	return false
}
