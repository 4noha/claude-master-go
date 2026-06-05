//go:build windows

package ttysync

import "errors"

// Opts は WrapStdio の調整パラメータ (Windows 版はまだ実装無し)。
type Opts struct {
	IdleMs int
}

// WrapStdio は Windows 未実装 (ConPTY 経由で同等実装可能・将来対応)。
// 主用途は Mac/Linux の tmux 経由 flicker 軽減なので Windows は priority
// 低い。Windows でも本機能を欲しくなったら ConPTY backend で実装。
func WrapStdio(argv []string, opts Opts) error {
	return errors.New("ttysync: not implemented on Windows yet (PR welcome)")
}

// PumpWithIdle は OS 非依存ロジック (wrap.go) なので Windows でも単体
// 使用可能。WrapStdio から呼び出されないだけ。
