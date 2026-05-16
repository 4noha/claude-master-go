# M6: GCP クラウド同期 — 実装設計

DESIGN.md「GCP クラウド同期」節の具体化。**制御線=常時/極軽量/無料、
データ線=差分時のみ PC からアウトバウンド WSS、quiescence で切断**。
NAT 内 PC へサーバから直結不可という制約を、PC 発のアウトバウンドだけで
解く。既存の RESIZE/SCROLL マジック＋画面フレーム protocol（unix socket
で実証済）を **そのまま WSS でトンネル**＝`internal/client` の framing を
再利用し新規プロトコルを作らない（脆さを足さない）。

## 確定アーキテクチャ

| 線 | 実体 | 役割 | コスト |
|---|---|---|---|
| 制御線（常時） | **Firestore リスナ**（`wake/{pcId}` doc を streaming watch）| wake 受信。NAT 越えは「PC 発の idle gRPC stream」で実現。FCM はデスクトップ常駐に不向き（受信側が結局接続保持必要）なため Firestore listener を採用 | 無料枠（idle watch ≒ 0 read、変更時のみ少量）|
| データ線（差分時のみ）| **Cloud Run** WSS `min-instances=0` | PC が wake を受けてアウトバウンド WSS。viewer も同 relay へ。1 接続最大 60 分→再接続ループ | 同期中のみ秒課金・scale-to-zero |
| 差分トリガ | **Cloud Functions 2nd**（Firestore onWrite）| session 状態 doc 更新→対象 PC の `wake` doc 書込 | 待機 $0 |
| 状態 | **Firestore Native** | PC/セッション登録・STATUS スキーマ・画面 version | 無料枠 |
| 認証 | Firebase Auth 匿名+カスタムトークン / SA。Security Rules で PC は自分の subtree のみ | 無料 |

> DESIGN.md は wake=FCM と書いていたが、**FCM はモバイル前提でデスクトップ
> 常駐デーモンの受信に余分な常時接続を要し利点が薄い**。Firestore の
> real-time listener は (a) 既に状態 store として使う (b) idle stream が
> 実質無料 (c) PC 発アウトバウンドで NAT 越え — の三点で上位互換。
> よって **wake も Firestore に一本化**（FCM はオプション将来拡張）。

## データフロー

```
[source PC] claude-master(launchd)
  ├─ 制御線: Firestore.Listen(wake/{pcId})            ← 常時・idle・無料
  ├─ 毎 poll: STATUS(scanner+VT status) を
  │           Firestore pcs/{pcId}/sessions/{sid} へ upsert（version++）
  └─ wake 受信 or 自分の差分検出時のみ:
        WSS dial  wss://relay/session/{sid}?role=source
        ├─ ローカル unix socket(<pid>.sock) へ socket_client 同等接続
        └─ unix socket ⇄ WSS を双方向ポンプ（RESIZE/SCROLL/frame 透過）
        quiescence(HistoryFlusher idle) N 秒で WSS close（データ線解放）

[viewer]（別 PC の claude-master / 将来 web）
  └─ WSS dial wss://relay/session/{sid}?role=viewer
        relay が source⇄viewer をセッション id で突合し中継

[Cloud Functions] Firestore onWrite(sessions/{sid}) で diff 判定
  → wake/{ownerPcId} に {sid, ts} を書込 → source PC の listener 発火
```

- **差分なし＝切断**: 既存 `screen.HistoryFlusher` の idle 判定
  （`_flush_host_flow` の quiescence ゲートと同じ根拠）をデータ線開閉に
  再利用。新ヒューリスティックは足さない（不変条件死守）。
- relay は**ステートレス・バイト透過**のみ（画面解釈をクラウドでしない）。
  単一インスタンスでセッション id 突合（小規模）。多インスタンス化が要る
  規模になったら Pub/Sub fanout を足す（設計余地のみ確保、当面不要）。

## ローカル Go パッケージ（M6a–c で実装・ローカル実検証可能）

| pkg | 役割 | 検証方法（合成 green 不使用） |
|---|---|---|
| `internal/cloud/relay` | WSS client + Go relay server。framing は `internal/client` の RESIZE/SCROLL/frame を再利用 | 実 PtyProxy(実 resume-burst 録画) → 実 WSS relay → client、display-oracle で pan/フレーム検証（GCP 不要） |
| `internal/cloud/state` | Firestore client ラッパ（STATUS upsert / wake listen）| **Firestore エミュレータ**（`gcloud beta emulators firestore`＝実 Firestore API・無料・決定的）で upsert/listen 往復 |
| `internal/cloud/agent` | wake→relay open→unix socket ポンプ→quiescence close 統括 | エミュレータ＋ローカル relay＋実 PtyProxy＋実録画で wake→中継→静止切断を end-to-end |
| `cmd/claude-master cloud …` | サブコマンド配線（`cloud agent` 常駐 / `cloud attach <sid>` viewer）| 実バイナリ起動 smoke |

依存追加: `github.com/coder/websocket`（旧 nhooyr、軽量・client/server・
context 対応）、`cloud.google.com/go/firestore`。CGO 不要・静的維持。

## クラウド側（M6d で実装・デプロイは GCP プロジェクト確認後）

```
cloud/relay/        Cloud Run 用 Go（WSS、session id 突合、min-inst=0）
cloud/functions/    Cloud Functions 2nd（Firestore onWrite→wake 書込）
deploy/firestore.rules  PC は pcs/{自分} と wake/{自分} のみ RW
deploy/*.sh         gcloud run deploy / functions deploy / rules deploy
```

実 `gcloud`/Firebase デプロイは**対外・課金・要 GCP プロジェクト**＝
ユーザーに project_id / region / 課金確認を取ってから（M6d ゲート）。
M6a–c は全てローカル（エミュレータ＋ローカル WSS）で先行可能。

## サブマイルストーン（各々 build＋実検証緑で前進）

- **M6a** WSS relay protocol: relay server + WSS client。実 PtyProxy
  (実録画)→relay→client を display-oracle 検証（nav/scroll/frame が
  unix socket と同値に WSS で透過）。
- **M6b** Firestore state: STATUS スキーマ upsert / wake listener を
  Firestore エミュレータ（実 API）で往復検証。Security Rules 雛形。
- **M6c** cloud agent: wake→WSS open→unix socket ポンプ→quiescence
  close をエミュレータ＋ローカル relay＋実録画で end-to-end。
- **M6d** クラウド実体: Cloud Run relay / Functions / rules / deploy
  スクリプト。**デプロイは GCP プロジェクト確認後**。
- **M6e** 実 GCP 結線・本番 cutover（実 project で 2 PC 間同期実証）。

## 不変条件（M1–M5 から継承）

- クラウドで画面を解釈しない（relay はバイト透過）。分類ヒューリスティック
  を足さない。quiescence は既存 HistoryFlusher を再利用。
- 検証は実録画/実 API（エミュレータ）/実 socket。合成では緑にしない。
- 稼働環境（launchd 化済 monitor / proxy alias）を壊さない。クラウド層は
  オプトイン（`cloud` サブコマンド未起動なら従来どおりローカルのみ）。
```
