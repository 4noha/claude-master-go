//go:build windows

package client

import (
	"sync/atomic"
	"testing"
	"time"
)

// pollResize の決定的検証（実ロジック・合成 interactive 主張ではない）:
// 安定時は無通知 / サイズ変化で通知 / 変化後安定で再通知しない / stop。
func TestPollResize_DetectsChangeNotStable(t *testing.T) {
	var w, h atomic.Int32
	w.Store(80)
	h.Store(25)
	get := func() (int, int, bool) { return int(w.Load()), int(h.Load()), true }

	out := make(chan struct{}, 1)
	done := make(chan struct{})
	go pollResize(get, 15*time.Millisecond, out, done)
	defer close(done)

	// 安定: 通知が来ないこと（baseline は初回 get で確定済）。
	select {
	case <-out:
		t.Fatal("安定サイズなのに resize 通知が来た")
	case <-time.After(90 * time.Millisecond):
	}

	// 変化 → 通知が来ること。
	w.Store(120)
	select {
	case <-out:
	case <-time.After(600 * time.Millisecond):
		t.Fatal("サイズ変化したのに resize 通知が来ない")
	}

	// 変化後また安定: 追加通知が来ないこと。
	select {
	case <-out:
		t.Fatal("変化後の安定サイズで再通知が来た")
	case <-time.After(120 * time.Millisecond):
	}
}
