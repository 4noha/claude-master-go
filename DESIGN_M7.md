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

- **M7a ✅** 認証コア: `cloud pair`＋Firestore pairing＋webauth
  （code/hash/HMAC cookie）。暗号単体＋実 Firestore エミュレータ。
- **M7b ✅** Web backend: /login・/auth/code・/api/pcs・/api/sessions・
  /ws を relay main へ統合。実エミュレータ＋実 relay＋実 PtyProxy
  (実録画) Go ブラウザ相当 viewer→display-oracle。
- **M7c ✅** フロント SPA＋固定版 xterm.js を go:embed 配信。実
  エミュレータ＋本番同型 mux で SPA/静的配信検証。
- **M7d ✅** Cloud Run 再デプロイ（rev 00003、`GCP_PROJECT`＋
  `WEB_SIGNING_KEY` env、ランタイム SA に datastore.user）＋
  **chrome-devtools 実ブラウザ e2e**: 実 Chrome→実 Cloud Run→実
  Firestore で code 入力→`PC: webe2e`→セッション一覧→端末。xterm DOM
  に実録画フッター `⏵⏵ bypass permissions … esc to interrupt …` を
  確認（DOM display-oracle・合成なし）。
  - 公開 URL: `https://claude-master-relay-demo01-an.a.run.app`
    （`/login`）。署名鍵 `~/.claude-master/web_signing_key`
    （リポジトリ外・600・再デプロイ不変）。
- **M7e ✅** 3 ページ化: `/`＝アカウント端末一覧（`/api/devices`）、
  各セッションから `/term?pc=&sid=` の Web ターミナルへリンク。
- **M7f ✅ 認証を Google アカウントに置換**（pairing code 廃止）:
  GIS（client_id 埋込）→`POST /auth/google`（g_csrf_token 二重送信→
  `idtoken` で署名/aud/iss/exp 検証→`ALLOWED_EMAILS` allowlist
  ＝owner@example.com のみ→HMAC cookie scope="*"=全 PC）。
  webauth に GoogleVerifier（本番 idtoken/テスト fake）、state に
  ListPCs/RegisterPC/DeletePC（端末は起動時 RegisterPC で確実に
  一覧表示）。OAuth は Console（chrome-devtools 操作）で同意画面
  External/Testing＋テストユーザー＋ウェブクライアント作成。
  Client ID `000000000000-EXAMPLECLIENTID.
  apps.googleusercontent.com`、JS 生成元＝公開 URL、リダイレクト
  ＝`/auth/google`。Cloud Run 再デプロイ（GOOGLE_OAUTH_CLIENT_ID＋
  ALLOWED_EMAILS env、rev 00005）。**実 Chrome で実 Google サイン
  イン→端末一覧→Web ターミナルを開く→/term の xterm DOM に実録画
  フッターを確認**（DOM display-oracle・合成なし）。検証後 webe2e
  テスト端末は DeletePC で除去。稼働 launchd cloud agent も
  RegisterPC 対応版へ再ビルド・再起動（実 Mac-Studio が一覧に出る）。
  個人専用前提（External/Testing のまま審査・公開不要、リフレッシュ
  トークン不使用で 7 日失効の影響なし）。

- **M7g ✅ Web から端末を追加（enroll）**: ログイン中アカウントの
  端末一覧に「＋ 端末を追加」。`POST /api/enroll`（cookie 必須）が
  一回限り・15分の enroll コード＋新 PC 用コマンドを発行（pairing
  プリミティブ scope="enroll" 再利用）。新 PC は
  `claude-master cloud enroll <code> --relay wss://…` を実行→
  `POST /enroll`（無認証＝コードが機密）が ConsumePairing で一回
  消費し {gcp_project, relay_url, sa_json} を返す→CLI が
  `~/.claude-master/sa.json`(600) と `~/.claude-master.toml`
  (GCP_PROJECT/CLOUD_RELAY_URL・既存キー保持マージ) を自動配置。
  SA 鍵は Cloud Run env `ENROLL_SA_JSON`（無ければ
  `ENROLL_SA_JSON_B64` を base64 復号＝gcloud --set-env-vars 安全）。
  enroll は GCP_PROJECT ガードより前で処理（未設定 PC の入口）。
  検証: 実 Firestore エミュレータで /api/enroll 認可・一回消費を
  機械検証＋**実 Chrome で「端末を追加」→コード取得→実 relay と
  `cloud enroll` 交換→sa.json/toml 自動配置を実環境確認**（rev 00006）。
  既存の起動時 `RegisterPC` で enroll した PC は `cloud agent` 起動で
  端末一覧に出る。実 Mac-Studio が 4 セッションで一覧表示も確認済。
  セキュリティ: enroll コードはログイン所有者のみ発行・一回・短 TTL・
  TLS。共有 SA を配布する personal 前提（将来 relay 仲介＝鍵不送付の
  Option B で更に堅牢化可能）。

## 不変条件継承

- relay はバイト透過のまま（ブラウザ viewer も同 protocol）。
  分類ヒューリスティック無し。
- 検証は実 API（エミュレータ）／実 PtyProxy＋実録画／display-oracle
  ／実ブラウザ（chrome-devtools）。合成では緑にしない。
- 稼働中の launchd（monitor / cloud agent）と既存 CLI 経路を壊さない
  （Web は追加・並存。relay 既存 /session は無改変）。
- SA 鍵・署名鍵はリポジトリ外。ブラウザに GCP 資格情報を渡さない。
