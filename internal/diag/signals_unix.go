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

// NonFatalSignals は **生きたままダンプを取る**ためのユーザ定義信号。
// SIGUSR1 を捕捉し WriteDump だけして proxy は継続させる用途（運用中の
// セッションを殺さず goroutine stack を採取）。Windows 対応無し（別ファイル）。
func NonFatalSignals() []os.Signal { return []os.Signal{syscall.SIGUSR1} }

// NotifyNonFatal は ch に NonFatalSignals を寄せる。呼び元 goroutine は
// ループで受信し WriteDump→継続（NotifyFatal とは別 ch を推奨）。
func NotifyNonFatal(ch chan os.Signal) { signal.Notify(ch, NonFatalSignals()...) }
