# M7: Web 管理 UI ＋ コード認証

目的: 別 PC / ブラウザから **「インストール・SA 鍵不要」** で、
コードを入力するだけでリモートの claude セッション一覧を見て端末に
接続できる。M6 のギャップ（SA 鍵配布／session_id を知る必要／配布物）
をブラウザと pairing code で解消する。

## 方針

- 既存 Cloud Run `cloud/relay` を **Web も兼ねるサービス**へ拡張
  （1 サービス・scale-to-zero 維持・追加コストほぼ無し）。
- ブラウザは GCP 資格情報を一切持たない。Firestore は **Cloud Run
  ランタイム SA**（サーバ側）経由のみ。ブラウザ↔サーバは cookie。
- ブラウザ viewer は **既存 RESIZE/SCROLL/frame protocol をそのまま**
  WS で話す（relay は無改変＝不変条件死守）。端末描画は xterm.js。
  socket_client / cloud attach（CLI）は従来どおり並存。

## コード認証（pairing code）

OAuth は重いので personal 用途に合う **pairing code** 方式（gh の
device code に近い）。Google アカウント不要。

```
[agent 側] claude-master cloud pair [--ttl 10m]
   → ランダム code 生成（8 文字 base32 ≒ 40bit）
   → Firestore pairings/{sha256(code)} = {pc, scope, expiresAt}
   → 画面に code と Web URL を表示（ユーザーがブラウザで入力）

[Web] GET /login → code 入力
   POST /auth/code {code}
     → sha256 で pairings/{hash} 検索・期限/スコープ検証
     → 一回使用なら doc 削除（消費）
     → HMAC 署名 cookie {pc, scope, exp} を Set-Cookie(HttpOnly,
        Secure, SameSite=Lax)。署名鍵は env WEB_SIGNING_KEY
        （Secret Manager → Cloud Run）。cookie TTL 12h。
```

- 失効/誤用対策: code は一回消費・短 TTL・ハッシュ保存（平文非保存）。
  cookie は署名・短命・HttpOnly。Cloud Run は TLS 終端済。
- **セキュリティ既定（ユーザー異議あれば変更）**: code=8 base32 一回・
  TTL10分、cookie 12h、接続は**対話（フル操作可）**＝claude-master の
  本来用途。ネットワーク edge は allow-unauthenticated のままだが
  /api・/ws はアプリ層 cookie 必須、未認証は /login と POST /auth/code
  のみ。pairing code が機密。

## エンドポイント（relay サービスに追加）

| パス | 認証 | 役割 |
|---|---|---|
| `GET /` | cookie 有→SPA / 無→/login | 管理 UI |
| `GET /login` | 無 | code 入力ページ |
| `POST /auth/code` | 無（code が鍵）| 検証→cookie 発行 |
| `POST /auth/logout` | cookie | cookie 失効 |
| `GET /api/pcs` | cookie | スコープ内 PC 一覧（Firestore pcs/*）|
| `GET /api/pcs/{pc}/sessions` | cookie | セッション一覧 |
| `GET /ws?pc=&sid=` | cookie | viewer WSS。内部は既存 relay viewer。先に Firestore へ wake を書き相手 agent を起こす |
| `GET /session?...` | （現状）| 既存 CLI/agent 経路（不変・並存）|
| `GET /static/*` | 無 | xterm.js 等（ピン留めベンダリング）|

`/ws` 受信時: cookie 検証 → `wake/{pc}` に sid を書く（M6 と同じ）→
relay の viewer として source とペアリング → ブラウザ⇄WSS⇄relay⇄
agent⇄unix socket⇄PtyProxy（バイト透過・xterm.js が描画）。

## フロント

- `cloud/web/static/`: `index.html` / `app.js` / 固定版 `xterm.js`
  ＋`xterm.css`（CDN 依存を避け自己完結・再現可能）。
- app.js: /api/pcs → 一覧 → 選択で /ws へ WebSocket、RESIZE 送信、
  入力(raw)送信、受信バイトを xterm.write。PageUp/wheel→SCROLL_MAGIC
  は段階的拡張（まず raw＋RESIZE）。

## サブマイルストーン（各 build＋実検証緑で前進）

- **M7a** 認証コア: `cloud pair` CLI＋Firestore pairing schema＋
  サーバ verifyCode／cookie 署名・検証。単体＋実 Firestore
  エミュレータで検証（合成 green 不使用）。
- **M7b** Web バックエンド: 上記ハンドラを relay main へ統合。
  httptest＋エミュレータ。Go 製「ブラウザ相当」viewer が /ws→
  実 PtyProxy(実録画)→display-oracle で protocol 透過を検証。
- **M7c** フロント SPA＋xterm.js（ベンダリング・最小操作）。
- **M7d** Cloud Run 再デプロイ＋ chrome-devtools で実ブラウザ
  e2e（実 GCP: code 入力→一覧→端末に実録画が見える）。

## 不変条件継承

- relay はバイト透過のまま（ブラウザ viewer も同 protocol）。
  分類ヒューリスティック無し。
- 検証は実 API（エミュレータ）／実 PtyProxy＋実録画／display-oracle
  ／実ブラウザ（chrome-devtools）。合成では緑にしない。
- 稼働中の launchd（monitor / cloud agent）と既存 CLI 経路を壊さない
  （Web は追加・並存。relay 既存 /session は無改変）。
- SA 鍵・署名鍵はリポジトリ外。ブラウザに GCP 資格情報を渡さない。
