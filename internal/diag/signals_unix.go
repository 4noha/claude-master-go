//go:build !windows

package diag

import (
	"os"
	"os/signal"
	"syscall"
)

// FatalSignals は「Go の既定では proxy を即殺するが catch 可能で
// `signal.Notify` で吸収できる」シグナル群。SIGHUP は **親（VSCode の
// 端末/zsh）が死ぬと proxy へ届く**最重要シグナル（=今回の主犯候補）。
// SIGKILL/SIGSEGV は catch 不能なので含めない（=StartPeriodicSnap が
// 唯一の事後証拠）。Windows 版は別ファイルで HUP を除外する。
func FatalSignals() []os.Signal {
	return []os.Signal{syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT}
}

// NotifyFatal は ch に FatalSignals を寄せる薄いラッパ（OS 差分の境界）。
func NotifyFatal(ch chan os.Signal) { signal.Notify(ch, FatalSignals()...) }
