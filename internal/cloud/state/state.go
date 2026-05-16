// Package state は Firestore 上のクラウド同期状態を扱う。
//
//   - 制御線（常時・無料）: WatchWake が wake/{pcId} を real-time
//     listener で監視。NAT 越えは「PC 発の idle gRPC stream」で実現。
//   - 状態 upsert: PushStatus が STATUS スキーマを
//     pcs/{pcId}/sessions/{sid} へ。content_hash 不変なら version 据置
//     ＝ Cloud Functions / agent の差分判定の土台。
//
// 画面解釈はしない（保存するのは scanner+VT status のメタのみ＝不変条件）。
// FIRESTORE_EMULATOR_HOST が立っていれば自動でエミュレータへ繋ぐので、
// 検証は実 Firestore API（エミュレータ）で決定的に行える。
package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"cloud.google.com/go/firestore"
)

type Client struct {
	fs    *firestore.Client
	pcID  string
}

// New は projectID/pcID で Firestore クライアントを作る。
// FIRESTORE_EMULATOR_HOST があればエミュレータへ（資格情報不要）。
func New(ctx context.Context, projectID, pcID string) (*Client, error) {
	fs, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &Client{fs: fs, pcID: pcID}, nil
}

func (c *Client) Close() error { return c.fs.Close() }

// contentHash は version/updated_at を除いた安定 JSON の sha256。
func contentHash(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == "version" || k == "updated_at" || k == "content_hash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ord := make([][2]any, 0, len(keys))
	for _, k := range keys {
		ord = append(ord, [2]any{k, m[k]})
	}
	b, _ := json.Marshal(ord)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// sessionKey は session doc の id（session_id 優先、無ければ pid-N）。
func sessionKey(s map[string]any) string {
	if v, ok := s["key"].(string); ok && v != "" {
		return v
	}
	if v, ok := s["session_id"].(string); ok && v != "" {
		return v
	}
	return "unknown"
}

// PushStatus は STATUS スキーマの各セッションを
// pcs/{pcId}/sessions/{key} へ upsert。content_hash が前回と同じなら
// version を据え置く（＝差分なし。Functions/agent がこれで開閉判断）。
// 戻り値 changed は今回 version が上がった session 数。
func (c *Client) PushStatus(ctx context.Context, sessions []map[string]any) (changed int, err error) {
	col := c.fs.Collection("pcs").Doc(c.pcID).Collection("sessions")
	for _, s := range sessions {
		id := sessionKey(s)
		h := contentHash(s)
		ref := col.Doc(id)
		ver := int64(1)
		snap, gerr := ref.Get(ctx)
		if gerr == nil && snap.Exists() {
			d := snap.Data()
			pv, _ := d["version"].(int64)
			ph, _ := d["content_hash"].(string)
			if ph == h {
				// 差分なし＝Firestore へ書かない（near-$0 維持。毎 tick
				// Set すると updated_at で常時書込＋無駄 listener wake）。
				continue
			}
			ver = pv + 1
			changed++
		} else {
			changed++
		}
		doc := map[string]any{}
		for k, v := range s {
			doc[k] = v
		}
		doc["version"] = ver
		doc["content_hash"] = h
		doc["updated_at"] = time.Now().UTC().Format(time.RFC3339)
		if _, werr := ref.Set(ctx, doc); werr != nil {
			return changed, werr
		}
	}
	return changed, nil
}

// CreatePairing は pairings/{codeHash} に {pc, scope, expires_at} を
// 書く（M7 Web コード認証。code 平文は保存しない）。
func (c *Client) CreatePairing(ctx context.Context, codeHash, pc, scope string, ttl time.Duration) error {
	_, err := c.fs.Collection("pairings").Doc(codeHash).Set(ctx, map[string]any{
		"pc":         pc,
		"scope":      scope,
		"expires_at": time.Now().Add(ttl).UTC().Format(time.RFC3339),
	})
	return err
}

// ConsumePairing は codeHash を検索し、期限内なら pc/scope を返して
// **doc を削除**（一回消費）。期限切れ/不在は ok=false（期限切れも
// 掃除のため削除）。
func (c *Client) ConsumePairing(ctx context.Context, codeHash string) (pc, scope string, ok bool, err error) {
	ref := c.fs.Collection("pairings").Doc(codeHash)
	snap, gerr := ref.Get(ctx)
	if gerr != nil || !snap.Exists() {
		return "", "", false, nil
	}
	d := snap.Data()
	_, _ = ref.Delete(ctx) // 一回消費（成否問わず掃除）
	exp, _ := d["expires_at"].(string)
	if t, perr := time.Parse(time.RFC3339, exp); perr != nil || time.Now().After(t) {
		return "", "", false, nil
	}
	p, _ := d["pc"].(string)
	sc, _ := d["scope"].(string)
	return p, sc, true, nil
}

// Wake は wake/{pcId} に {sid, ts} を書く（Cloud Functions / テストが
// 呼ぶ）。対象 PC の WatchWake listener が即発火する。
func (c *Client) Wake(ctx context.Context, targetPC, sid string) error {
	_, err := c.fs.Collection("wake").Doc(targetPC).Set(ctx, map[string]any{
		"sid": sid,
		"ts":  time.Now().UTC().Format(time.RFC3339Nano),
	})
	return err
}

// WatchWake は wake/{pcId} を real-time 監視（常時・無料の制御線）。
// 変更ごとに cb(sid) を呼ぶ。ctx 終了でクリーンに戻る。これが
// 「PC 発 idle gRPC stream」で NAT を越える wake 受信経路。
func (c *Client) WatchWake(ctx context.Context, cb func(sid string)) error {
	it := c.fs.Collection("wake").Doc(c.pcID).Snapshots(ctx)
	defer it.Stop()
	for {
		snap, err := it.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil // 正常終了（ctx cancel）
			}
			return err
		}
		if snap == nil || !snap.Exists() {
			continue
		}
		if sid, ok := snap.Data()["sid"].(string); ok && sid != "" {
			cb(sid)
		}
	}
}
