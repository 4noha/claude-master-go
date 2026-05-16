package screen

import (
	"regexp"
	"strconv"
	"strings"
)

// 使用量上限ステータス抽出（Python pty_emulator.extract_usage /
// is_active の移植）。これは描画ではなく VT 画面モデルの read-only
// ヒューリスティック走査（monitor の自動中断/再開＝M5e のための
// <pid>.status.json 用）。「分類は一切しない」不変条件は描画経路の
// 話であり、ここはレンダリングに一切関与しない別レイヤ。
//
// 注意: 実 resume-burst 録画には使用量 footer（"used N% of your
// session limit"）が存在しないため ExtractUsage の正例は実録画では
// 検証できない（regex は Python の _USAGE_RE/_RESET_RE を 1:1 移植）。
// 実録画では「使用量 footer が無い→ any=false」という実 negative と、
// active footer（"esc to interrupt"）での IsActive=true を検証する。

var (
	// Python _USAGE_RE = r"used\s+(\d+)%\s+of\s+your\s+session\s+limit"
	usageRe = regexp.MustCompile(`used\s+(\d+)%\s+of\s+your\s+session\s+limit`)
	// Python _RESET_RE = r"resets\s+(\d{1,2}(?::\d{2})?\s*(?:am|pm))\s*\(([^)]+)\)"
	resetRe = regexp.MustCompile(`resets\s+(\d{1,2}(?::\d{2})?\s*(?:am|pm))\s*\(([^)]+)\)`)
	// Python _TRANSIENT_TAIL_RE = r"…\s*(\([^)]*\))?\s*$"
	transientTailRe = regexp.MustCompile(`…\s*(\([^)]*\))?\s*$`)
)

// Python _FOOTER_SPINNER_CHARS | {"⏺"}
const activePrefixChars = "✻✽✶✷✸✹✺✢✣✤✥✦✧✩✪✫✬✭✮✯✰✱✲✳✴✵·⏺"

// Python _ACTIVE_FOOTER_KEYWORDS
var activeFooterKeywords = []string{
	"esc to interrupt",
	"Do you want to proceed?",
	"accept edits",
	"don't ask again for:",
}

// StatusScanner は usage% / reset 時刻を蓄積する（Python TuiEmulator の
// _last_usage_percent(最大)/_last_reset_time/_last_reset_tz 相当）。
type StatusScanner struct {
	lastPct   int
	hasPct    bool
	resetTime string
	resetTZ   string
}

// ExtractUsage は history.top + 可視 buffer 全行を走査し usage/reset を
// 更新して現在値を返す（Python extract_usage と同一: pct は単調最大、
// reset は最後にマッチした値）。any=false なら未検出（status に出さない）。
func (sc *StatusScanner) ExtractUsage(v *VT) (pct int, hasPct bool, resetTime, resetTZ string, any bool) {
	scan := func(text string) {
		if m := usageRe.FindStringSubmatch(text); m != nil {
			if p, err := strconv.Atoi(m[1]); err == nil {
				if !sc.hasPct || p > sc.lastPct {
					sc.lastPct = p
					sc.hasPct = true
				}
			}
		}
		if m := resetRe.FindStringSubmatch(text); m != nil {
			sc.resetTime = strings.TrimSpace(m[1])
			sc.resetTZ = strings.TrimSpace(m[2])
		}
	}
	for _, ln := range v.HistoryLines() {
		scan(ln)
	}
	for _, ln := range v.VisibleLines() {
		scan(ln)
	}
	if sc.hasPct {
		pct, hasPct, any = sc.lastPct, true, true
	}
	if sc.resetTime != "" {
		resetTime, resetTZ, any = sc.resetTime, sc.resetTZ, true
	}
	return
}

// IsActive は可視 buffer 下部最大 6 行を直接走査し、アクティブな
// タスク実行中か判定（Python is_active と同一: spinner/⏺ 先頭・
// 末尾 transient(…)・active キーワードのいずれか）。raw passthrough
// 構成のため _extract に依存せず可視バッファを直接見る。
func IsActive(v *VT) bool {
	vis := v.VisibleLines()
	n := len(vis)
	for y := n - 1; y >= 0 && y > n-7; y-- {
		text := strings.TrimSpace(vis[y])
		if text == "" {
			continue
		}
		first := []rune(text)[0]
		if strings.ContainsRune(activePrefixChars, first) {
			return true
		}
		if transientTailRe.MatchString(text) {
			return true
		}
		for _, kw := range activeFooterKeywords {
			if strings.Contains(text, kw) {
				return true
			}
		}
	}
	return false
}
