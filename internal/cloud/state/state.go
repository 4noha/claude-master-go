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
	"google.golang.org/api/option"
)

type Client struct {
	fs    *firestore.Client
	pcID  string
}

// New は projectID/pcID で Firestore クライアントを作る。
// FIRESTORE_EMULATOR_HOST があればエミュレータへ（資格情報不要）。
// 資格情報はプロセス global の ADC（GOOGLE_APPLICATION_CREDENTIALS）由来。
func New(ctx context.Context, projectID, pcID string) (*Client, error) {
	fs, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &Client{fs: fs, pcID: pcID}, nil
}

// NewWithCredentials は **クライアント個別**の SA 鍵ファイルで Firestore
// クライアントを作る（複数クラウド fan-out 用＝1 プロセスで別 GCP
// プロジェクト/別 SA 鍵に同時接続するため。GOOGLE_APPLICATION_CREDENTIALS
// は global で 1 つしか持てないので option.WithCredentialsFile で個別注入）。
// saKeyPath が空なら New と同じ（ADC/エミュレータ）＝後方互換・テスト無影響。
func NewWithCredentials(ctx context.Context, projectID, pcID, saKeyPath string) (*Client, error) {
	if saKeyPath == "" {
		return New(ctx, projectID, pcID)
	}
	fs, err := firestore.NewClient(ctx, projectID,
		option.WithCredentialsFile(saKeyPath))
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
	// pcs/{pc} は subcollection 書込では暗黙の非存在 doc になり
	// Collection("pcs") 列挙に出ない。端末一覧（account scope）で
	// 拾えるよう、変化があった時だけ親 doc を明示書込（near-$0 維持）。
	if changed > 0 {
		// MergeAll＝id/updated_at のみ更新し RegisterPCVersion が入れた
		// cm_version 等の他フィールドを消さない（全置換 Set だと agent
		// 版が毎 producer tick で消える実バグになる）。
		_, _ = c.fs.Collection("pcs").Doc(c.pcID).Set(ctx, map[string]any{
			"id":         c.pcID,
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		}, firestore.MergeAll)
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

// RegisterPC は pcs/{pcID} 親 doc を明示作成する（端末一覧に確実に
// 出すため。agent/helper 起動時に 1 回呼ぶ＝1 書込で near-$0。
// PushStatus の差分ゲートだと未変更セッションや再起動で消える問題を解消）。
func (c *Client) RegisterPC(ctx context.Context) error {
	return c.RegisterPCVersion(ctx, "")
}

// RegisterPCVersion は RegisterPC に agent バイナリ版 cm_version を併記
// （起動時1回＝near-$0。session が無い idle PC でも web で版/🔴 判定可）。
func (c *Client) RegisterPCVersion(ctx context.Context, agentVersion string) error {
	doc := map[string]any{
		"id":         c.pcID,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	}
	if agentVersion != "" {
		doc["cm_version"] = agentVersion
	}
	_, err := c.fs.Collection("pcs").Doc(c.pcID).Set(ctx, doc)
	return err
}

// deletePC は pcs/{pcID}（sessions サブコレクション含む）と wake/{pcID}
// を削除する内部実装。
func (c *Client) deletePC(ctx context.Context, pcID string) error {
	if pcID == "" {
		return nil
	}
	col := c.fs.Collection("pcs").Doc(pcID).Collection("sessions")
	docs, err := col.Documents(ctx).GetAll()
	if err == nil {
		for _, d := range docs {
			_, _ = d.Ref.Delete(ctx)
		}
	}
	_, _ = c.fs.Collection("pcs").Doc(pcID).Delete(ctx)
	_, err = c.fs.Collection("wake").Doc(pcID).Delete(ctx)
	return err
}

// DeletePC は自 PC（テスト端末の後始末・端末解除用）。
func (c *Client) DeletePC(ctx context.Context) error {
	return c.deletePC(ctx, c.pcID)
}

// DeletePCByID は任意 PC の登録（pcs/{pc}＋sessions＋wake/{pc}）を削除
// する。Web 管理 UI の「端末ペアリング削除」用（古い/不要/失効端末を
// 一覧から消す）。短命 relaygrants は TTL で自然失効するので対象外。
func (c *Client) DeletePCByID(ctx context.Context, pcID string) error {
	return c.deletePC(ctx, pcID)
}

// WatchSessions は全 PC の sessions コレクショングループを real-time
// 監視し、変更（追加/更新/削除）のたび cb を呼ぶ（5s ポーリング廃止＝
// push 駆動）。初回スナップショットでも 1 回 cb（初期同期）。ctx 終了で
// クリーンに戻る。連続変更はバースト的に来るので呼び出し側で冪等な
// reconcile を行うこと。
func (c *Client) WatchSessions(ctx context.Context, cb func()) error {
	return keepSubscribed(ctx, func() (func() error, func()) {
		it := c.fs.CollectionGroup("sessions").Snapshots(ctx)
		pump := func() error {
			for {
				if _, err := it.Next(); err != nil {
					return err // 終端 → keepSubscribed が再購読（resident は死なない）
				}
				cb()
			}
		}
		return pump, func() { it.Stop() }
	})
}

// ListPCs は pcs/* の PC（端末）id 一覧（アカウント全体 scope 用）。
func (c *Client) ListPCs(ctx context.Context) ([]string, error) {
	docs, err := c.fs.Collection("pcs").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Ref.ID)
	}
	return out, nil
}

// ListSessions は pcs/{pc}/sessions/* を返す（Web /api 用。画面解釈は
// せずメタのみ）。古い→新しい順は問わない。
func (c *Client) ListSessions(ctx context.Context, pc string) ([]map[string]any, error) {
	docs, err := c.fs.Collection("pcs").Doc(pc).
		Collection("sessions").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Data())
	}
	return out, nil
}

// PCVersion は pcs/{pc}.cm_version（agent バイナリ版）を返す。未登録/
// 未設定は ""（エラーにしない＝idle/古い端末も一覧表示は継続）。
func (c *Client) PCVersion(ctx context.Context, pc string) (string, error) {
	snap, err := c.fs.Collection("pcs").Doc(pc).Get(ctx)
	if err != nil || !snap.Exists() {
		return "", nil
	}
	v, _ := snap.Data()["cm_version"].(string)
	return v, nil
}

// SessionKeyOf は session マップから doc id（＝同期キー）を返す。
// PushStatus が doc id に使う sessionKey と同一規則（producer 側で
// 「今 tick の生存キー集合」を作り終了検出するために公開）。
func SessionKeyOf(s map[string]any) string { return sessionKey(s) }

// DeleteSession は自 PC の pcs/{pcID}/sessions/{key} を削除する。
// claude プロセス終了を「セッション消滅」としてクラウドへ伝播させ、
// 各 consumer の WatchSessions→ReconcileRemote が窓を kill できる
// （producer 側 in-memory 差分で終了時のみ呼ぶ＝追加読み無し near-$0）。
func (c *Client) DeleteSession(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	_, err := c.fs.Collection("pcs").Doc(c.pcID).
		Collection("sessions").Doc(key).Delete(ctx)
	return err
}

// OwnSessionKeys は自 PC の現存 session doc id 一覧。agent 再起動跨ぎで
// 「停止中に終了したセッション」を取りこぼさないよう、producer ループ
// 起動時に prev 集合を Firestore 実態で seed するのに使う（起動時 1 回
// のみ＝tick 毎の追加読みは発生しない）。
func (c *Client) OwnSessionKeys(ctx context.Context) ([]string, error) {
	docs, err := c.fs.Collection("pcs").Doc(c.pcID).
		Collection("sessions").Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Ref.ID)
	}
	return out, nil
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
	return keepSubscribed(ctx, func() (func() error, func()) {
		it := c.fs.Collection("wake").Doc(c.pcID).Snapshots(ctx)
		pump := func() error {
			for {
				snap, err := it.Next()
				if err != nil {
					return err // 終端（iterator.Done 等）→ keepSubscribed が再購読
				}
				if snap == nil || !snap.Exists() {
					continue
				}
				if sid, ok := snap.Data()["sid"].(string); ok && sid != "" {
					cb(sid)
				}
			}
		}
		return pump, func() { it.Stop() }
	})
}

// relayGrantID は relaygrants/ の doc id。sid と role の組で一意
// （`/` は doc id 不可なので `:` 区切り）。
func relayGrantID(sid, role string) string { return sid + ":" + role }

// PutRelayGrant は「この (sid,role) で relay /session に接続してよい」
// という短命の許可を Firestore に書く。SA（Firestore アクセス）を持つ
// 正規の接続元（agent の source / `cloud attach` viewer）だけが書ける
// ＝公開 /session の認可根拠。relay が CheckRelayGrant で検証する。
// 接続のたびに呼ぶ前提（再接続で上書き）。TTL は接続レイテンシ＋
// 再接続間隔をカバーする短さ。
func (c *Client) PutRelayGrant(ctx context.Context, sid, role string, ttl time.Duration) error {
	if sid == "" || (role != "source" && role != "viewer") {
		return nil
	}
	_, err := c.fs.Collection("relaygrants").Doc(relayGrantID(sid, role)).
		Set(ctx, map[string]any{
			"sid": sid, "role": role, "pc": c.pcID,
			"exp": time.Now().Add(ttl).UTC().Format(time.RFC3339Nano),
		})
	return err
}

// SetRevoked は端末を強制失効させる（管理 UI の「ペアリング解除」）。
// revoked/{pc} を立てると CheckRelayGrant がその pc の grant を拒否し
// （relay が権威）、当該 agent も自停止する（防御多重）。owner が
// 意図的に再 enroll するまで有効。
func (c *Client) SetRevoked(ctx context.Context, pc string) error {
	if pc == "" {
		return nil
	}
	_, err := c.fs.Collection("revoked").Doc(pc).Set(ctx, map[string]any{
		"pc": pc, "at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	return err
}

// ClearRevoked は失効解除（owner 発行コードでの再 enroll 時に呼ぶ＝
// 信頼の起点は owner 発行の一回限りコード）。
func (c *Client) ClearRevoked(ctx context.Context, pc string) error {
	if pc == "" {
		return nil
	}
	_, err := c.fs.Collection("revoked").Doc(pc).Delete(ctx)
	return err
}

// IsRevoked は pc が失効済みか。取得失敗は false（可用性優先＝主たる
// 認可は grant。失効は付加的 deny）。
func (c *Client) IsRevoked(ctx context.Context, pc string) bool {
	if pc == "" {
		return false
	}
	snap, err := c.fs.Collection("revoked").Doc(pc).Get(ctx)
	return err == nil && snap != nil && snap.Exists()
}

// IsSelfRevoked は自 PC（c.pcID）が失効済みか（agent 自停止判定用）。
func (c *Client) IsSelfRevoked(ctx context.Context) bool {
	return c.IsRevoked(ctx, c.pcID)
}

// CheckRelayGrant は (sid,role) の有効な許可が存在するか（期限内か）。
// relay の公開 /session ハンドラが Accept 前に呼ぶ。doc 無し/期限切れ/
// 取得失敗は false（fail-closed＝認可されない）。
func (c *Client) CheckRelayGrant(ctx context.Context, sid, role string) bool {
	if sid == "" || (role != "source" && role != "viewer") {
		return false
	}
	snap, err := c.fs.Collection("relaygrants").
		Doc(relayGrantID(sid, role)).Get(ctx)
	if err != nil || snap == nil || !snap.Exists() {
		return false
	}
	d := snap.Data()
	es, _ := d["exp"].(string)
	t, perr := time.Parse(time.RFC3339Nano, es)
	if perr != nil || !time.Now().Before(t) {
		return false
	}
	// 強制失効: grant が指す pc が revoked なら期限内でも拒否
	// （relay が権威。生きた agent が grant を書いても締め出せる）。
	if pc2, _ := d["pc"].(string); pc2 != "" && c.IsRevoked(ctx, pc2) {
		return false
	}
	return true
}
