//go:build windows

package tmux

import (
	"encoding/base32"
	"strings"
)

// marker 保存/取得の OS 実体（windows）。psmux はカスタム per-window
// option `@cm_remote` が per-window でない（全窓へ漏洩・spike 実証）。
// 一方 `#{window_name}` は per-window 忠実・`rename-window`/`window_id`/
// `kill-window` も忠実（spike 済）。よって marker を **window 名へ
// base32 符号化**して保持する（exact-match 復号＝非ヒューリスティック・
// 不変条件遵守。runaway 防止の per-window stateless 在席判定を psmux
// 忠実プリミティブのみで再現）。
//
// M8f(2) cosmetic 改善（案 B・DESIGN_M8）: 窓名 =
// `<可読ラベル><SEP>cmr1_<base32(marker)>`。可読ラベルは生成時に
// `new-window -n <name>` が付けた人間可読名（agent の shortName(dir)）。
// marker は依然 name 内に完全保持＝**stateless/再起動耐性/厳密一致/
// dedup は現行同等**。`mark_unix.go`/`tmux.go` 共有は無改修＝unix
// バイト同一（parity）。復号は最後の `cmr1_` トークンを厳密 base32
// 復号（先頭ラベルは無視・旧 `cmr1_<b32>` 単体名も後方互換）。
// tmux ステータスバーは左から表示＝可読部が見え符号化末尾は幅で切れる。

const (
	winMarkPrefix = "cmr1_"
	winMarkSep    = " " // 可読ラベルと符号化トークンの窓名内区切り
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// encMarkerToken は marker の符号化トークン（cmr1_<base32>）のみ。
func encMarkerToken(marker string) string {
	return winMarkPrefix + b32.EncodeToString([]byte(marker))
}

// decMarker は窓名から marker を復号。`<ラベル><SEP>cmr1_<b32>` でも
// 旧 `cmr1_<b32>` 単体でも可（最後の cmr1_ を厳密復号＝先頭ラベル無視・
// 非ヒューリスティック）。
func decMarker(name string) (string, bool) {
	i := strings.LastIndex(name, winMarkPrefix)
	if i < 0 {
		return "", false
	}
	b, err := b32.DecodeString(name[i+len(winMarkPrefix):])
	if err != nil {
		return "", false
	}
	return string(b), true
}

// applyWindowDisplayFormat は status-bar の窓名表示から marker
// トークン（` cmr1_<base32>`）を隠す。窓名の**実体は不変**（marker
// 保持）で `#{window_name}` を読む reconcile（listWindowMarkers）に
// 一切影響しない＝表示のみ整形。psmux の format 置換
// `#{s/ cmr1_.*//:window_name}` は実機検証済（raw 名は保持される）。
// #I=index、#F=flag(*現/-前/空) を残し #W 相当だけ置換名へ。
// set-option は冪等（EnsureSession 毎回同値）。
func applyWindowDisplayFormat(m *Manager) {
	const f = "#I:#{s/ cmr1_.*//:window_name}#F"
	_, _ = outErr("set-option", "-t", m.Session, "window-status-format", f)
	_, _ = outErr("set-option", "-t", m.Session,
		"window-status-current-format", f)
}

// initialName は new-window -n に渡す初期窓名。windows は marker を
// **窓名へ符号化**して保持するため、生成時点から `<label><SEP>cmr1_<b32>`
// を返す。これで marker-less な窓が一瞬も存在せず、MarkedWindows が
// 生成直後から decode 可＝ReconcileRemote が自分の作った窓を見失わず
// KEEP する（2 段付与の隙間が「窓無し」誤判定→毎周再作成→legacy
// 誤殺の自走ストームの真因だった）。marker=="" は素のラベル
// （NewWindow 等）。markWindow は冪等に同名へ再 rename するだけ。
func initialName(name, marker string) string {
	if marker == "" {
		return name
	}
	tok := encMarkerToken(marker)
	if name != "" {
		return name + winMarkSep + tok
	}
	return tok
}

// currentName は window_id の現在名（new-window -n が付けた可読ラベル）
// を返す（psmux 忠実な list-windows -F #{window_name} 経由）。
func currentName(m *Manager, id string) string {
	o, err := outErr("list-windows", "-t", m.Session, "-F",
		"#{window_id}=#{window_name}")
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(o, "\n") {
		k := strings.IndexByte(ln, '=')
		if k >= 0 && ln[:k] == id {
			return ln[k+1:]
		}
	}
	return ""
}

// markWindow は可読ラベル（現窓名＝shortName(dir)）を保持しつつ marker
// 符号化トークンを末尾へ付与して rename し auto-rename off。
func markWindow(m *Manager, id, marker string) {
	t := m.Session + ":" + id
	label := currentName(m, id)
	// 既に符号化済（再 mark）なら可読部だけ取り出して付け直す（冪等）。
	if i := strings.LastIndex(label, winMarkPrefix); i >= 0 {
		label = strings.TrimRight(label[:i], winMarkSep)
	}
	name := encMarkerToken(marker)
	if label != "" {
		name = label + winMarkSep + name
	}
	_, _ = outErr("rename-window", "-t", t, name)
	_, _ = outErr("set-option", "-w", "-t", t, "automatic-rename", "off")
}

// listWindowMarkers は符号化トークンを持つ窓を window_id→marker で返す
// （'=' 区切り。window_id は @\d+ で '=' を含まないため name に '=' や
// 空白があっても先頭 '=' で安全分割）。
func listWindowMarkers(m *Manager) (map[string]string, error) {
	o, err := outErr("list-windows", "-t", m.Session, "-F",
		"#{window_id}=#{window_name}")
	if err != nil {
		return nil, err
	}
	res := map[string]string{}
	if o == "" {
		return res, nil
	}
	for _, ln := range strings.Split(o, "\n") {
		i := strings.IndexByte(ln, '=')
		if i < 0 {
			continue
		}
		id, nm := ln[:i], ln[i+1:]
		if mk, ok := decMarker(nm); ok && id != "" {
			res[id] = mk
		}
	}
	return res, nil
}

// legacyMarkerlessIDs は windows では常に空。
//
// 「legacy（@cm_remote 未設定の取り残し）」は unix の per-window
// option 方式に固有の概念で、windows は marker を**最初から窓名へ
// 符号化**する単一方式しか存在しない（移行 legacy が無い）。一方で
// 「符号化トークンを持たない ↗ 窓」を毎 reconcile で kill すると、
// initialName 以前は new-window→markWindow の隙間に居る**正当な
// 作成直後の窓を誤殺**し、再作成ストームを自走増幅させていた
// （実測 12.5s/55 窓の真因の一つ）。initialName で隙間自体を消した
// 上で、本関数も無効化し増幅器を完全に断つ。重複/消失/余剰の整理は
// ReconcileRemote の cur(marker decode) ベース kill＋CAP ガードが
// 厳密に担う（marker は生成直後から decode 可＝確実）。
func legacyMarkerlessIDs(_ *Manager) ([]string, error) {
	return nil, nil
}
