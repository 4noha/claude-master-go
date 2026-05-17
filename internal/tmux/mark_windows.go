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
// 制約（cosmetic・honest）: Windows のリモート窓表示名は符号化名に
// なる（@cm_remote 方式のような表示名併存は psmux 非対応のため）。
// 機能（reconcile 厳密一致・dedupe・restart 跨ぎ）は保持。

const winMarkPrefix = "cmr1_"

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func encMarker(marker string) string {
	return winMarkPrefix + b32.EncodeToString([]byte(marker))
}

func decMarker(name string) (string, bool) {
	if !strings.HasPrefix(name, winMarkPrefix) {
		return "", false
	}
	b, err := b32.DecodeString(name[len(winMarkPrefix):])
	if err != nil {
		return "", false
	}
	return string(b), true
}

// markWindow は window 名を marker 符号化名へ rename し auto-rename off。
func markWindow(m *Manager, id, marker string) {
	t := m.Session + ":" + id
	_, _ = outErr("rename-window", "-t", t, encMarker(marker))
	_, _ = outErr("set-option", "-w", "-t", t, "automatic-rename", "off")
}

// listWindowMarkers は符号化名を持つ窓を window_id→marker で返す
// （'=' 区切り＝unix と同じく非 tty -F の TAB 脱落回避）。
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

// legacyMarkerlessIDs は符号化名でない "↗" リモート窓（旧/取り残し）。
func legacyMarkerlessIDs(m *Manager) ([]string, error) {
	o, err := outErr("list-windows", "-t", m.Session, "-F",
		"#{window_id}=#{window_name}")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, ln := range strings.Split(o, "\n") {
		i := strings.IndexByte(ln, '=')
		if i < 0 {
			continue
		}
		id, nm := ln[:i], ln[i+1:]
		if _, ok := decMarker(nm); !ok && strings.HasPrefix(nm, "↗") {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
