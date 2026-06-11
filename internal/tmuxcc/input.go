package tmuxcc

import (
	"fmt"
	"strings"
)

// EncodeSendKeysLiteral は stdin から読んだ raw bytes を tmux の
// `send-keys -t <pane> -l "<text>"` の literal 引数に safe-quote する。
// tmux の -l フラグは引数を「リテラル文字列として pane に注入」(キー
// 名変換せず生 bytes として送信)。但し shell 風 quote 規則があり
// ダブルクォート/バックスラッシュは escape 必要。
//
// tmux の command 行 quote 規則 (man tmux "QUOTING"):
//   - 二重引用符内: \" でリテラル "、\\ でリテラル \、その他 literal
//   - 制御文字 (e.g. ESC=0x1b) は \nnn (octal) で書ける
//
// パフォーマンス重視: 1 byte 単位で全部 \nnn にして送る (safe・大量
// 入力でも問題ない速度)。
func EncodeSendKeysLiteral(target string, data []byte) string {
	var b strings.Builder
	b.WriteString(`send-keys -t `)
	b.WriteString(target)
	b.WriteString(` -l "`)
	for _, c := range data {
		// 全 byte を \nnn octal で表現 (確実に safe)
		fmt.Fprintf(&b, `\%03o`, c)
	}
	b.WriteString(`"`)
	return b.String()
}

// ResizeCommand は tmux に client サイズ変更を通知するコマンド。
// `refresh-client -C <width>,<height>` で control mode client が外側
// 端末サイズを tmux に伝える。pane size は tmux 側で再計算される。
func ResizeCommand(cols, rows int) string {
	return fmt.Sprintf("refresh-client -C %dx%d", cols, rows)
}
