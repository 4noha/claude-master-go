package screen

import "regexp"

// 入力分類（Python pty_scroll.is_live_reset_key / classify_wheel 移植）。
// nav/pagekey/wheel スクロールの「実ユーザー操作のみ live 復帰／端末の
// passive レポートでは復帰しない」「ホイール方向判定」に使う。

var (
	sgrMouseRe  = regexp.MustCompile(`^\x1b\[<(\d+);\d+;\d+[Mm]`)
	devReportRe = regexp.MustCompile(`^\x1b\[[\d;?>=]*[Rcny]$`)
)

// IsLiveResetKey は data が「ユーザーの実操作（文字/Enter/BS/Tab/矢印/
// 編集キー等）」で scroll を live(最下部)へ戻すべきか。端末が自発的に
// 送る passive（focus 入/出, mouse, カーソル位置/DA/DSR 応答, OSC/DCS）
// は false（戻さない・透過）。空は false。Python と同一仕様。
func IsLiveResetKey(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	s := string(data)
	if s == "\x1b[I" || s == "\x1b[O" { // focus in/out (?1004h)
		return false
	}
	if len(data) >= 3 && data[0] == 0x1b && data[1] == '[' && data[2] == 'M' {
		return false // legacy mouse
	}
	if sgrMouseRe.MatchString(s) { // SGR mouse
		return false
	}
	if len(data) >= 2 && data[0] == 0x1b && (data[1] == ']' || data[1] == 'P') {
		return false // OSC / DCS 応答
	}
	if devReportRe.MatchString(s) { // CSI デバイス応答(R/c/n/y)
		return false
	}
	return true // 実操作 → live 復帰
}

// ClassifyWheel はマウスホイール入力を判定。dir=-1(上=過去へ)/+1(下=
// 新しい方へ)、ok=false ならホイールでない（クリック/ドラッグ/非マウス
// ＝claude へ透過）。SGR(?1006h) と legacy(?1000h) 両対応。Cb bit6=拡張
// (ホイール)・bit5=motion、修飾(shift4/alt8/ctrl16)は無視。Python と同一。
func ClassifyWheel(data []byte) (dir int, ok bool) {
	var cb int
	if m := sgrMouseRe.FindSubmatch(data); m != nil {
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
		cb = n
	} else {
		i := indexOf(data, []byte("\x1b[M"))
		if i < 0 || len(data) < i+4 {
			return 0, false
		}
		cb = int(data[i+3]) - 32
	}
	if cb&64 != 0 && cb&32 == 0 { // 拡張ボタン かつ motion でない
		switch cb & 0b11 {
		case 0:
			return -1, true // ホイール上 = 過去へ
		case 1:
			return 1, true // ホイール下 = 新しい方へ
		}
	}
	return 0, false
}

func indexOf(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return i
		}
	}
	return -1
}
