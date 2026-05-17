//go:build !windows

package tmux

import (
	"fmt"
	"os"
	"strings"
)

// marker 保存/取得の OS 実体（unix=tmux の per-window user option
// @cm_remote）。本ファイルは M8f 前の tmux.go と body バイト同一
// （darwin/linux parity 厳守。runaway 防止の核心ゆえ挙動不変）。

// applyWindowDisplayFormat は unix では何もしない。unix の窓名は
// 可読ラベルのみ（marker は per-window option @cm_remote＝名前に
// 符号化しない）ため status-bar 整形が不要で、EnsureSession の発行
// コマンドを M8 前と完全同一に保つ（darwin/linux バイト同一 parity）。
func applyWindowDisplayFormat(_ *Manager) {}

// initialName は new-window -n に渡す初期窓名。unix は marker を
// per-window option @cm_remote で別途持つため **ラベルそのもの**を返す
// ＝`new-window -n name` は M8f 前と完全に同一の引数＝挙動バイト同一
// （darwin/linux parity 厳守。markWindow が従来通り @cm_remote を設定）。
func initialName(name, _ string) string { return name }

// markWindow は window へ marker を付与（@cm_remote）＋auto-rename off。
func markWindow(m *Manager, id, marker string) {
	t := m.Session + ":" + id
	_, e1 := outErr("set-option", "-w", "-t", t, remoteOpt, marker)
	_, e2 := outErr("set-option", "-w", "-t", t, "automatic-rename", "off")
	if os.Getenv("CM_DEBUG") != "" && (e1 != nil || e2 != nil) {
		fmt.Fprintf(os.Stderr, "[tmux] set-option @cm_remote err=%v automatic err=%v\n", e1, e2)
	}
}

// listWindowMarkers は @cm_remote 付き窓を window_id→marker で返す。
// 区切りは TAB 不可（tmux -F は非 tty 出力で literal TAB を落とす＝
// runaway 真因だった）。'=' を区切りに使う。
func listWindowMarkers(m *Manager) (map[string]string, error) {
	o, err := outErr("list-windows", "-t", m.Session, "-F",
		"#{window_id}=#{"+remoteOpt+"}")
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
		id, mark := ln[:i], ln[i+1:]
		if id != "" && mark != "" {
			res[id] = mark
		}
	}
	return res, nil
}

// legacyMarkerlessIDs は @cm_remote 未設定だが窓名が "↗"（リモート窓
// 命名）の旧 runaway 残骸 id を返す。
func legacyMarkerlessIDs(m *Manager) ([]string, error) {
	o, err := outErr("list-windows", "-t", m.Session, "-F",
		"#{window_id}=#{"+remoteOpt+"}=#{window_name}")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, ln := range strings.Split(o, "\n") {
		p := strings.SplitN(ln, "=", 3)
		if len(p) < 3 {
			continue
		}
		if p[1] == "" && strings.HasPrefix(p[2], "↗") {
			ids = append(ids, p[0])
		}
	}
	return ids, nil
}
