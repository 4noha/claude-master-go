package tmuxcc

import (
	"strings"
	"testing"
)

// TestEncodeSendKeysLiteral_Basic: literal byte が全部 \nnn octal に
// なって tmux send-keys -l に safe-quote される。
func TestEncodeSendKeysLiteral_Basic(t *testing.T) {
	cmd := EncodeSendKeysLiteral("%0", []byte("ab"))
	// 期待: send-keys -t %0 -l "\141\142"
	want := `send-keys -t %0 -l "\141\142"`
	if cmd != want {
		t.Fatalf("encode mismatch\n got=%q\nwant=%q", cmd, want)
	}
}

// TestEncodeSendKeysLiteral_ControlChars: ESC, CR, LF, TAB 等を含む
// keystroke が確実に octal で escape される。
func TestEncodeSendKeysLiteral_ControlChars(t *testing.T) {
	cmd := EncodeSendKeysLiteral("%0", []byte{0x1b, 0x0d, 0x09, 0x00, 0xff})
	want := `send-keys -t %0 -l "\033\015\011\000\377"`
	if cmd != want {
		t.Fatalf("ctrl encode mismatch\n got=%q\nwant=%q", cmd, want)
	}
}

// TestEncodeSendKeysLiteral_QuoteSafe: tmux quote 規則上 dangerous な
// `"` `\` `$` `` ` `` 等が octal 化されて含まれない。
func TestEncodeSendKeysLiteral_QuoteSafe(t *testing.T) {
	cmd := EncodeSendKeysLiteral("%0", []byte(`"\` + "`$"))
	// "(0x22), \(0x5c), `(0x60), $(0x24) → 全部 \nnn
	want := `send-keys -t %0 -l "\042\134\140\044"`
	if cmd != want {
		t.Fatalf("quote-safe encode mismatch\n got=%q\nwant=%q", cmd, want)
	}
	// dangerous 文字 ( " ` $ ) が **escape 無し**で残っていないことを
	// 確認 (\ は escape syntax の一部なので literal OK)。
	payload := cmd[strings.Index(cmd, `-l "`)+4 : len(cmd)-1]
	for _, c := range []string{`"`, "`", "$"} {
		if strings.Contains(payload, c) {
			t.Fatalf("dangerous char %q leaked to payload: %q", c, payload)
		}
	}
}

// TestResizeCommand: refresh-client -C <w>,<h> 形式。
func TestResizeCommand(t *testing.T) {
	got := ResizeCommand(120, 40)
	want := "refresh-client -C 120x40"
	if got != want {
		t.Fatalf("resize cmd: got=%q want=%q", got, want)
	}
}
