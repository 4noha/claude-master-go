//go:build windows

package client

import (
	"os"
	"time"

	"golang.org/x/term"
)

// watchResize (windows): Windows に SIGWINCH は無い。コンソールの
// stdin から定期的にサイズを読み、変化したら通知する polling 実装
// （ReadConsoleInput は client の raw stdin 読取経路と競合するため
// 不採用。resize は低頻度なので polling で十分かつ安全）。
//
// 署名は unix と同一＝client.go / resize_unix.go へ変更ゼロ（他環境
// クリーン）。Windows 専用ファイル＝unix/darwin では非コンパイル。
func watchResize() (<-chan struct{}, func()) {
	out := make(chan struct{}, 1)
	done := make(chan struct{})
	fd := int(os.Stdin.Fd())
	get := func() (int, int, bool) {
		w, h, err := term.GetSize(fd)
		if err != nil {
			return 0, 0, false
		}
		return w, h, true
	}
	go pollResize(get, 400*time.Millisecond, out, done)
	return out, func() { close(done) }
}

// pollResize はサイズ変化を検出して out へ通知する純ループ（テスト
// 可能なよう get/interval/チャネルを注入）。sendResize は都度現サイズ
// を読むため、通知の合体は無害（buffer1＋non-blocking send）。
func pollResize(get func() (int, int, bool), interval time.Duration,
	out chan<- struct{}, done <-chan struct{}) {
	lw, lh, have := get()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			w, h, ok := get()
			if !ok {
				continue
			}
			if !have || w != lw || h != lh {
				lw, lh, have = w, h, true
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}
}
