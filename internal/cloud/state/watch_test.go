package state

// keepSubscribed の決定論検証（Firestore 不要・純粋ロジック）。実 Firestore
// で iterator.Done を強制するのは非決定的なため、再購読ループそのものを
// 実コードのまま fake イテレータで検証する（合成サーバではなく実関数）。
// 旧挙動の参照実装も併置し「修正前なら致命 return＝赤」を機械的に示す
// （回帰検知の証明・鉄則2）。

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// 旧 Watch* と同型: pump エラーは ctx 以外なら致命 return（= agent 死）。
func oldStyleWatch(ctx context.Context,
	subscribe func() (func() error, func())) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		pump, stop := subscribe()
		err := pump()
		stop()
		if ctx.Err() != nil {
			return nil
		}
		return err // ← iterator.Done をそのまま致命伝播（真因）
	}
}

func TestKeepSubscribedResubscribesAndNeverFatal(t *testing.T) {
	// iterator.Done と同じ文言（google.golang.org/api/iterator.Done）
	done := errors.New("no more items in iterator")
	ctx, cancel := context.WithCancel(context.Background())

	var subs, stops int32
	subscribe := func() (func() error, func()) {
		atomic.AddInt32(&subs, 1)
		pump := func() error { return done } // 即終端（ストリーム終端を模倣）
		stop := func() { atomic.AddInt32(&stops, 1) }
		return pump, stop
	}

	ret := make(chan error, 1)
	go func() { ret <- keepSubscribed(ctx, subscribe) }()

	// 終端を返し続けても死なず再購読し続けること（>=3 回）。
	dl := time.Now().Add(6 * time.Second)
	for atomic.LoadInt32(&subs) < 3 {
		if time.Now().After(dl) {
			t.Fatalf("再購読されない（subs=%d）＝致命 return 疑い（真因未修正）",
				atomic.LoadInt32(&subs))
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case e := <-ret:
		t.Fatalf("ctx 生存中に return した（resident が死ぬ）: %v", e)
	default:
	}

	// ctx 終了でのみ nil を返す（致命エラーは絶対返さない）。
	cancel()
	select {
	case e := <-ret:
		if e != nil {
			t.Fatalf("ctx 終了時の戻り値が nil でない: %v", e)
		}
	case <-time.After(watchBackoffMax + 3*time.Second):
		t.Fatal("ctx 終了後も keepSubscribed が戻らない")
	}
	if atomic.LoadInt32(&stops) != atomic.LoadInt32(&subs) {
		t.Fatalf("購読/停止が不対応: subs=%d stops=%d",
			atomic.LoadInt32(&subs), atomic.LoadInt32(&stops))
	}

	// 旧実装はこの fake で初回終端を致命 return（＝Windows で agent 死）。
	// これが「修正前は赤」の機械的証明。
	if e := oldStyleWatch(context.Background(), subscribe); e != done {
		t.Fatalf("旧実装が致命 return しない＝回帰再現不能（想定外）: %v", e)
	}
}

func TestKeepSubscribedCtxAlreadyDoneNoSubscribe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var subs int32
	err := keepSubscribed(ctx, func() (func() error, func()) {
		atomic.AddInt32(&subs, 1)
		return func() error { return nil }, func() {}
	})
	if err != nil {
		t.Fatalf("ctx 既終了で nil 以外: %v", err)
	}
	if subs != 0 {
		t.Fatalf("ctx 既終了なのに購読した: subs=%d", subs)
	}
}
