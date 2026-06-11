// Package tmuxcc は tmux control mode (-CC) client を実装する。
// 目的は「tmux→外側端末 区間の flicker を構造的に解消」する L4-A'。
//
// 設計根拠 (CLAUDE.md「tmux 経路の既知制約」「ちらつき防止の層モデル」):
//   - 通常 tmux client: proxy frame → tmux outer render (50% 裸) → 端末
//     ＝tmux outer 区間で BSU/ESU が漏れて flicker
//   - L4-A' 経路: proxy frame → tmux -CC %output (生 bytes 保持) →
//     wrapper が screen.VT に Feed → RenderANSI で 1 atomic write to
//     stdout ＝tmux outer render を完全 bypass＝端末は完璧 atomic frame
//     のみ受信
//
// PoC (2026-06-05) で `%output %0 \033[?2026h\033[31mhello...\033[0m
// \033[?2026l\015\012` 形式を確認＝proxy frame の BSU/ESU 構造が
// octal escape された text として完全保存される＝既存 internal/screen
// の VT で原版同様にパース可能。
//
// 本 file は wire protocol parser (msg 行 → 型付きメッセージ + octal
// escape decoder)。
package tmuxcc

import (
	"errors"
	"strconv"
	"strings"
)

// Msg は tmux -CC client が受信する制御メッセージの interface。
// 具体的 type は OutputMsg / BeginMsg / EndMsg / SessionChangedMsg /
// WindowAddMsg / LayoutChangeMsg / ExitMsg / その他 OtherMsg。
type Msg interface{ msg() }

// OutputMsg: %output %<pane-id> <escaped-bytes>
// PaneID は "%0" など先頭 % 付き。Data は octal escape を decode した
// 生 bytes (proxy frame そのもの)。
type OutputMsg struct {
	PaneID string
	Data   []byte
}

func (*OutputMsg) msg() {}

// BeginMsg / EndMsg: %begin <time> <num> <flags> / %end ...
// tmux のコマンド応答 frame の開始/終了 marker。本 wrapper では
// session/layout 問合せ応答の境界として使う。
type BeginMsg struct{ Time, Num, Flags string }
type EndMsg struct{ Time, Num, Flags string }

func (*BeginMsg) msg() {}
func (*EndMsg) msg()   {}

// SessionChangedMsg: %session-changed $<id> <name>
type SessionChangedMsg struct {
	ID, Name string
}

func (*SessionChangedMsg) msg() {}

// WindowAddMsg / WindowCloseMsg / WindowRenameMsg
type WindowAddMsg struct{ WindowID string }
type WindowCloseMsg struct{ WindowID string }
type WindowRenameMsg struct{ WindowID, Name string }

func (*WindowAddMsg) msg()    {}
func (*WindowCloseMsg) msg()  {}
func (*WindowRenameMsg) msg() {}

// LayoutChangeMsg: %layout-change @<win> <layout-string> [visible_size]
// layout-string は tmux pane tree の text 表現 (e.g.
// "f30x100,0,0,0" 単一 pane、"f30x100,0,0{20x100,0,0,0,9x100,21,0,1}"
// 横分割 2 pane 等)。本 file ではフィールド切り出しだけ、parse は
// 別 file (layout.go) で。
type LayoutChangeMsg struct {
	WindowID string
	Layout   string
	Extra    string // 残りフィールド (visible size 等)
}

func (*LayoutChangeMsg) msg() {}

// ExitMsg: %exit [reason]
type ExitMsg struct{ Reason string }

func (*ExitMsg) msg() {}

// ReplyLineMsg は %begin/%end の間に届くコマンド応答本文 (% で始まら
// ない行)。display-message / capture-pane 等の問合せ結果の運搬用。
// client が ParseLine=nil の非空行を包んで Events へ流す。
type ReplyLineMsg struct{ Text string }

func (*ReplyLineMsg) msg() {}

// WindowPaneChangedMsg: %window-pane-changed @<win> %<pane>
type WindowPaneChangedMsg struct{ WindowID, PaneID string }

func (*WindowPaneChangedMsg) msg() {}

// SessionWindowChangedMsg: %session-window-changed $<sess> @<win>
type SessionWindowChangedMsg struct{ SessionID, WindowID string }

func (*SessionWindowChangedMsg) msg() {}

// OtherMsg: 上記いずれにも match しない % 行 (将来 tmux で追加された
// msg type など)。Type は 先頭の token (例 "%window-renamed")、Rest は
// 残り全体。
type OtherMsg struct{ Type, Rest string }

func (*OtherMsg) msg() {}

// ParseLine は tmux -CC が emit する 1 行を Msg に変換する。先頭が %
// で始まらない行 (改行 / 空行 / 通常コマンド応答本文) は (nil, nil)。
// 不明形式は OtherMsg として返す (error にしない＝forward 安全)。
func ParseLine(line string) (Msg, error) {
	// 末尾 \r\n を除去
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "%") {
		return nil, nil
	}
	// 先頭 token (msg type) を取り出し
	sp := strings.IndexByte(line, ' ')
	var typ, rest string
	if sp < 0 {
		typ, rest = line, ""
	} else {
		typ, rest = line[:sp], line[sp+1:]
	}
	switch typ {
	case "%output":
		// %output %<pane-id> <bytes>
		sp2 := strings.IndexByte(rest, ' ')
		if sp2 < 0 {
			return nil, errors.New("tmuxcc: %output missing data")
		}
		pid := rest[:sp2]
		data, err := DecodeOctalEscapes(rest[sp2+1:])
		if err != nil {
			return nil, err
		}
		return &OutputMsg{PaneID: pid, Data: data}, nil
	case "%begin":
		f := strings.Fields(rest)
		m := &BeginMsg{}
		if len(f) > 0 {
			m.Time = f[0]
		}
		if len(f) > 1 {
			m.Num = f[1]
		}
		if len(f) > 2 {
			m.Flags = f[2]
		}
		return m, nil
	case "%end":
		f := strings.Fields(rest)
		m := &EndMsg{}
		if len(f) > 0 {
			m.Time = f[0]
		}
		if len(f) > 1 {
			m.Num = f[1]
		}
		if len(f) > 2 {
			m.Flags = f[2]
		}
		return m, nil
	case "%session-changed":
		f := strings.SplitN(rest, " ", 2)
		m := &SessionChangedMsg{}
		if len(f) > 0 {
			m.ID = f[0]
		}
		if len(f) > 1 {
			m.Name = f[1]
		}
		return m, nil
	case "%window-add":
		return &WindowAddMsg{WindowID: strings.TrimSpace(rest)}, nil
	case "%window-close":
		return &WindowCloseMsg{WindowID: strings.TrimSpace(rest)}, nil
	case "%window-renamed":
		f := strings.SplitN(rest, " ", 2)
		m := &WindowRenameMsg{}
		if len(f) > 0 {
			m.WindowID = f[0]
		}
		if len(f) > 1 {
			m.Name = f[1]
		}
		return m, nil
	case "%layout-change":
		// %layout-change @<win> <layout-string> [extra...]
		f := strings.SplitN(rest, " ", 3)
		m := &LayoutChangeMsg{}
		if len(f) > 0 {
			m.WindowID = f[0]
		}
		if len(f) > 1 {
			m.Layout = f[1]
		}
		if len(f) > 2 {
			m.Extra = f[2]
		}
		return m, nil
	case "%window-pane-changed":
		f := strings.Fields(rest)
		m := &WindowPaneChangedMsg{}
		if len(f) > 0 {
			m.WindowID = f[0]
		}
		if len(f) > 1 {
			m.PaneID = f[1]
		}
		return m, nil
	case "%session-window-changed":
		f := strings.Fields(rest)
		m := &SessionWindowChangedMsg{}
		if len(f) > 0 {
			m.SessionID = f[0]
		}
		if len(f) > 1 {
			m.WindowID = f[1]
		}
		return m, nil
	case "%exit":
		return &ExitMsg{Reason: rest}, nil
	default:
		return &OtherMsg{Type: typ, Rest: rest}, nil
	}
}

// DecodeOctalEscapes は tmux -CC %output の text encoding を生 bytes
// に戻す。tmux protocol 仕様:
//   - 0x00-0x1f / 0x7f-0xff は 3 桁 octal "\NNN" (例 ESC=\033)
//   - "\" 自身は "\\" (octal でなく literal escape)
//   - その他 printable はそのまま
//
// 入力末尾の partial escape (例 "\03" で切れた) は invalid input。
// PoC で確認した実 tmux 出力は常に 3 桁 octal なのでそれ前提。
func DecodeOctalEscapes(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = append(out, s[i])
			continue
		}
		// '\\' の次
		if i+1 >= len(s) {
			return nil, errors.New("tmuxcc: trailing backslash in escape")
		}
		c := s[i+1]
		if c == '\\' {
			out = append(out, '\\')
			i++
			continue
		}
		// 3 桁 octal NNN
		if c < '0' || c > '7' {
			// tmux は他 escape を emit しない想定。安全のため literal
			// として通す (forward-safe)
			out = append(out, s[i], c)
			i++
			continue
		}
		if i+3 >= len(s) {
			return nil, errors.New("tmuxcc: incomplete octal escape")
		}
		o, err := strconv.ParseUint(s[i+1:i+4], 8, 16)
		if err != nil {
			return nil, errors.New("tmuxcc: bad octal escape: " + s[i:i+4])
		}
		out = append(out, byte(o))
		i += 3
	}
	return out, nil
}
