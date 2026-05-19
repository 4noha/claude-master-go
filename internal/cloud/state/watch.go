package state

// resident watcher の耐障害ループ。Firestore Snapshots の it.Next() は
// ctx 以外の理由でストリームが終端すると iterator.Done
// （"no more items in iterator"）を返す。旧実装はこれを致命 return し、
// WatchWake→ag.Run→runCloudAgent.exitErr で **agent プロセスが終了**。
// Mac は launchd KeepAlive で即復帰し症状が隠蔽されていたが、Windows
// S4U（AtLogOn のみ・KeepAlive 無し）では復帰せず＝クラウド同期が死ぬ
// 真因。resident な制御線は終端で死なず **再購読** すべき。
//
// keepSubscribed は ctx 終了でのみ nil を返し、それ以外（iterator.Done
// 含む任意のエラー）は購読を Stop→指数バックオフ→再購読する。protocol/
// 不変条件は不変（制御線の耐障害性のみ）。純粋（ctx＋クロージャ＋time）
// ＝fake で決定論ユニットテスト可能（実 Firestore で Done を強制するのは
// 非決定的なため、再購読ロジック自体を実コードのまま検証する）。

import (
	"context"
	"time"
)

const (
	watchBackoffMin = 200 * time.Millisecond
	watchBackoffMax = 5 * time.Second
)

// sleepCtx は d 待つ。ctx 終了で false（待たず中断）。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// keepSubscribed は subscribe で購読を作り pump() を回し切り、終端したら
// stop()→backoff→再購読、を ctx 終了まで繰り返す。pump は 1 購読分を
// 処理（各スナップショットで cb 等を呼ぶ）し、終端理由を返す。戻り値は
// ctx 終了時のみ nil（resident は「終端で死なない」＝致命 return しない）。
func keepSubscribed(ctx context.Context,
	subscribe func() (pump func() error, stop func())) error {
	bo := watchBackoffMin
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
		_ = err // iterator.Done でも他エラーでも resident は再購読（死なない）
		if !sleepCtx(ctx, bo) {
			return nil
		}
		if bo *= 2; bo > watchBackoffMax {
			bo = watchBackoffMax
		}
	}
}
