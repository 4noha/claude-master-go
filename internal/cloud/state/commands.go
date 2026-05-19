package state

// 遠隔命令チャネル（owner 限定＋実行前確認＋監査）。WatchWake と同系の
// 「常時・無料の制御線」。relay/byte tunnel は無改変（不変条件死守）＝
// 命令は Firestore commands/{pc}/q/{id} 経由のみ。near-$0: 人手起因の
// 稀イベントで数書込/命令。claim transaction で再配信時の二重実行を防ぐ。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
)

// Command は遠隔命令の1件。status: pending→running→done|error。
// json タグは devices.js（c.cmd/c.requested_by 等 snake_case）と一致
// させること＝API の契約。firestore タグと同名（保存/表示で一貫）。
type Command struct {
	ID          string `firestore:"id" json:"id"`
	Cmd         string `firestore:"cmd" json:"cmd"`
	SID         string `firestore:"sid" json:"sid"`
	RequestedBy string `firestore:"requested_by" json:"requested_by"`
	TS          string `firestore:"ts" json:"ts"`
	Status      string `firestore:"status" json:"status"`
	Detail      string `firestore:"detail" json:"detail"`
	FinishedAt  string `firestore:"finished_at" json:"finished_at"`
}

// ValidCommands は許可コマンド（web/agent 双方で検証）。
var ValidCommands = map[string]bool{
	"restart-agent": true, // launchd 2 デーモン kickstart -k
	"self-update":   true, // selfupdate.Update→自己/monitor 再起動
	"restart-proxy": true, // 当該 claude proxy を --resume で再起動（Phase3）
}

var errAlreadyClaimed = errors.New("already claimed")

func (c *Client) cmdCol(pc string) *firestore.CollectionRef {
	return c.fs.Collection("commands").Doc(pc).Collection("q")
}

// PushCommand は owner 認証済 web が遠隔命令を投入（status=pending）。
// requestedBy（ログイン email）を監査に残す。戻り値は命令 id。
func (c *Client) PushCommand(ctx context.Context, pc, cmd, sid, requestedBy string) (string, error) {
	if !ValidCommands[cmd] {
		return "", fmt.Errorf("未知のコマンド: %s", cmd)
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	_, err := c.cmdCol(pc).Doc(id).Set(ctx, map[string]any{
		"id": id, "cmd": cmd, "sid": sid, "requested_by": requestedBy,
		"ts": time.Now().UTC().Format(time.RFC3339Nano), "status": "pending",
	})
	return id, err
}

// WatchCommands は自 PC の pending 命令を realtime 監視（常時・無料）。
// fn は claim 成功（pending→running を transaction で1度だけ）した命令
// のみ受ける＝Snapshot 再配信や複数 agent でも二重実行しない。
func (c *Client) WatchCommands(ctx context.Context, fn func(Command)) error {
	return keepSubscribed(ctx, func() (func() error, func()) {
		it := c.cmdCol(c.pcID).Where("status", "==", "pending").Snapshots(ctx)
		pump := func() error {
			for {
				qs, err := it.Next()
				if err != nil {
					return err // 終端 → keepSubscribed が再購読（resident 死なない）
				}
				if qs == nil {
					continue
				}
				for _, ch := range qs.Changes {
					if ch.Kind == firestore.DocumentRemoved {
						continue
					}
					var cm Command
					if e := ch.Doc.DataTo(&cm); e != nil || cm.Status != "pending" {
						continue
					}
					if !c.claimCommand(ctx, cm.ID) {
						continue
					}
					cm.Status = "running"
					fn(cm)
				}
			}
		}
		return pump, func() { it.Stop() }
	})
}

// claimCommand は pending→running を transaction で1度だけ成功させる。
func (c *Client) claimCommand(ctx context.Context, id string) bool {
	ref := c.cmdCol(c.pcID).Doc(id)
	err := c.fs.RunTransaction(ctx,
		func(ctx context.Context, tx *firestore.Transaction) error {
			snap, err := tx.Get(ref)
			if err != nil {
				return err
			}
			if st, _ := snap.Data()["status"].(string); st != "pending" {
				return errAlreadyClaimed
			}
			return tx.Set(ref, map[string]any{"status": "running"},
				firestore.MergeAll)
		})
	return err == nil
}

// AckCommand は実行結果を監査として書き戻す（status=done|error）。
// agent が claim した命令にのみ呼ぶ。
func (c *Client) AckCommand(ctx context.Context, id, status, detail string) error {
	_, err := c.cmdCol(c.pcID).Doc(id).Set(ctx, map[string]any{
		"status": status, "detail": detail,
		"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
	}, firestore.MergeAll)
	return err
}

// RecentCommands は監査表示用に新しい順 n 件（web 用）。
func (c *Client) RecentCommands(ctx context.Context, pc string, n int) ([]Command, error) {
	docs, err := c.cmdCol(pc).OrderBy("ts", firestore.Desc).
		Limit(n).Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	out := make([]Command, 0, len(docs))
	for _, d := range docs {
		var cm Command
		if d.DataTo(&cm) == nil {
			out = append(out, cm)
		}
	}
	return out, nil
}
