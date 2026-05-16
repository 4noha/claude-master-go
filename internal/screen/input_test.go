package screen

import "testing"

// Python pty_scroll の is_live_reset_key / classify_wheel と同一ケースで
// 移植同値を機械担保（決定的・分類器なので合成可）。

func TestIsLiveResetKey(t *testing.T) {
	passive := []string{
		"\x1b[I", "\x1b[O", // focus in/out
		"\x1b[<0;3;4M",   // SGR mouse click
		"\x1b[<35;3;4M",  // SGR mouse motion
		"\x1b[M`! !",     // legacy mouse
		"\x1b[24;80R",    // cursor position report
		"\x1b[?1;2c",     // DA response
		"\x1b[0n",        // DSR
		"\x1b]11;rgb:0\x07", // OSC response (先頭 \x1b])
		"",               // 空
	}
	for _, s := range passive {
		if IsLiveResetKey([]byte(s)) {
			t.Errorf("IsLiveResetKey(%q)=true want false (passive)", s)
		}
	}
	live := []string{
		"a", "Z", "あ", "hello",
		"\r", "\n", "\x7f", "\b", "\t", // Enter/BS/Tab
		"\x1b[A", "\x1b[B", "\x1b[C", "\x1b[D", // arrows
		"\x1b[3~", "\x1b[1~", "\x1b[5~", // Del/Home/PgUp(編集/移動)
	}
	for _, s := range live {
		if !IsLiveResetKey([]byte(s)) {
			t.Errorf("IsLiveResetKey(%q)=false want true (実操作)", s)
		}
	}
}

func TestClassifyWheel(t *testing.T) {
	type tc struct {
		in   string
		dir  int
		ok   bool
	}
	cases := []tc{
		{"\x1b[<64;10;5M", -1, true}, // SGR wheel up
		{"\x1b[<65;10;5M", 1, true},  // SGR wheel down
		{"\x1b[<80;1;1M", -1, true},  // wheel up + ctrl(16)
		{"\x1b[<81;1;1M", 1, true},   // wheel down + ctrl
		{"\x1b[M`! !", -1, true},     // legacy up (0x60-32=64)
		{"\x1b[Ma! !", 1, true},      // legacy down (0x61-32=65)
		{"\x1b[<0;3;4M", 0, false},   // click → 透過
		{"\x1b[<35;3;4M", 0, false},  // motion → 透過
		{"abc", 0, false},
		{"\x1b[A", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		d, ok := ClassifyWheel([]byte(c.in))
		if d != c.dir || ok != c.ok {
			t.Errorf("ClassifyWheel(%q)=(%d,%v) want (%d,%v)",
				c.in, d, ok, c.dir, c.ok)
		}
	}
}
