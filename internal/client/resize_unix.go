//go:build !windows

package client

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize は端末リサイズ通知を struct{} チャネルで返す。
// unix=SIGWINCH（Python socket_client の signal 経路と同一トリガ。
// シグナルチャネルは buffer1＝原実装の取りこぼし特性も踏襲。
// sendResize は都度現サイズを読むため合体は無害）。stop で解除。
func watchResize() (<-chan struct{}, func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	out := make(chan struct{}, 1)
	go func() {
		for range sig {
			out <- struct{}{}
		}
	}()
	return out, func() { signal.Stop(sig) }
}
