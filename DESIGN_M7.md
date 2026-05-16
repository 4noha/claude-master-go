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

## M7i: dashboard へ外部セッション併合 ＋ 管理外 claude のタブ抑止

要望「tmux の dashboard に外部のセッションも載せる」「claude-master で
起動していない claude プロセスのタブは表示しない」。

- **外部セッション併合（疎結合・追加 Firestore 読み無し）**: cloud agent の
  `ReconcileRemote` が既に取得済みの他 PC セッションを
  `<SessionsDir>/remote_sessions.json`（`config.RemoteFile`）へ原子的
  （tmp→rename）に書く（`agent.SnapshotPath`、cloud agent 起動時のみ設定。
  テストは未設定＝no-op）。monitor の `Dashboard()` が STATUS_FILE と
  **独立**に同ファイルを読み `data["remote"]` へ併合、`RenderDashboard`
  が「リモート（他 PC）」節（`PC名 ↗short_dir [sid8]`）を追加。monitor↔
  cloud agent はファイル経由のまま疎結合。エラー時は snapshot を上書き
  しない（部分/空で消さない fail-safe）。
- **管理外タブ抑止**: `monitor.managedOnly` が `<pid>.sock`（＝
  claude-master proxy 経由起動の証跡）を持つセッションだけに絞り、
  `RunLoop`/`SyncOnce` はそれ以外のタブを作らない（旧 socket 無し＝
  対話シェル窓ブランチは廃止）。素の `claude` は管理外として無視。
- 検証: 実 socket ファイル＋実 tmux で `managedOnly`／実 schema で
  リモート節描画・枠幅不変（`TestManagedOnlyFiltersSocketless`/
  `TestRenderDashboardRemoteSection`）。稼働 launchd で実 dashboard が
  ローカル 1＋リモート 8（実 PC D24WT27C3J）を描画、残存 socket 無し窓も
  除去を実機確認。

### プロセス終了の同期（ghost 窓の除去）

旧: `PushStatus` は **upsert のみ**で終了セッション doc を消さず、
`ListSessions` が古い doc を返し続け ↗窓/dashboard 行が永久残留
（whole-PC `DeletePC` まで消えない）＝ghost。

修正: producer ループ（cloud agent）が **前 tick との in-memory 差分**で
「居なくなったキー」を `state.DeleteSession` で Firestore から削除。
起動時に `state.OwnSessionKeys` で `prev` を Firestore 実態で seed し
agent 再起動中に終了した分も次 tick で回収。削除は CollectionGroup
`sessions` の変更として **WatchSessions に push** され、各 consumer の
`ReconcileRemote` が `desired` から外れた窓を `KillWindowID`＋
`remote_sessions.json` を更新（dashboard も同期）。コスト: 追加読み
ゼロ（in-memory 差分）・終了時のみ Delete 1 書込＝near-$0 不変。

検証: 実エミュレータで PushStatus 2→DeleteSession→ListSessions/
OwnSessionKeys が確実に縮む＋空/不在キー安全（`TestDeleteSession
SyncsTermination`）。consumer 側の desired 縮小→窓 kill は既存テスト
担保。全 13 パッケージ緑。稼働 cloud agent を新バイナリで再起動し
クリーン稼働（log エラー 0・remote 窓 8 安定）を実機確認。
**注意**: 他 PC の終了セッションがこの PC の dashboard から消えるには
**その PC の cloud agent もこの版へ更新**が必要（producer 側削除のため）。

## M7j: Web コンソール UX（dir 表示／スワイプ切替）

要望を term.js / devices.js / ui.go(termHTML) で実装（relay・protocol
は無改変＝不変条件死守）。

- **ディレクトリ名表示**: devices.js の /term リンクに `&dir=` を付与、
  term.js が `qs.get("dir")` をバー（`#title`）と `document.title` に
  表示（`dir — pc`）。一覧の表示も `short_dir` 基準に統一。
- **コンソール切替（スワイプ＋‹›）**: term.js が /api/devices＋
  /api/sessions を一覧ページと同順で平坦化し `[{pc,sid,dir}]` を構築、
  現在地を (pc,sid) で特定し `#pos` に `(i/n)`。`‹/›` ボタンと
  左右スワイプ（touchstart/end のΔx>60 かつ |Δx|>|Δy|*1.4、左=次/
  右=前、巡回）で隣の /term へ location 遷移（WS/xterm はページ遷移で
  クリーン再接続）。取得失敗時は無効化しターミナルは使用可。
- **スクロール破壊の修正（ホイール→SCROLL 変換）**: proxy は毎フレーム
  絶対座標で全画面再描画（ミニ tmux）するため、xterm が自前
  scrollback でローカルスクロールすると衝突し表示が壊れる。
  `scrollback:0` で xterm 自前スクロールを無効化し、ホイール/
  トラックパッド（`wheel` を capture+preventDefault）と PageUp/PageDown
  を **SCROLL_MAGIC(0xff 0xfe + int16 BE dy)** へ変換して proxy の
  per-client ScrollRenderer を pan（socket_client の sendScroll と
  同一ワイヤ／符号: dy<0=古い・上, dy>0=新しい・下, 32767=live）。
  遡り中に実入力があれば先に FOLLOW(32767) を送って live 復帰
  （socket_client の pkScrolled リセットと同規律）。Home/End は
  claude の行編集を壊さないため非変換（Shift+Home/End のみ最古/live）。
  proxy 側 `parseClientInput` の scrollMagic 処理は `WheelScroll`
  config 非依存で常時有効＝relay 越し Web でもそのまま効く。

### RESIZE 送信に関する重要な訂正（誤診→撤回）

当初「Web は窓サイズ非送信にする」変更を入れたが、根拠が誤りだった
ため**撤回し socket-client 同様 RESIZE を送る実装へ戻した**。

- 誤った前提: 「SIZE_POLICY=client/largest で最後に resize した
  client へ PTY が追従しブラウザが claude/tmux を引きずる」。これは
  **Python 設計のセマンティクスで Go 移植版には存在しない**。
- Go の実機構（コード確認済）: `p.Setsize()` を呼ぶ唯一の経路は
  `run.go` の host SIGWINCH のみ。`server.go` の client RESIZE は
  `c.rows/c.cols` を更新し**その client 自身の per-client ビューポート
  を再描画するだけ**で PTY/claude を変えない。`SizePolicy` は
  `=="host"`（生パススルー）しか分岐せず `client/largest/smallest/
  latest` は未実装（config パースのみ存在）。よって live 設定
  `size_policy="largest"` でも実質 client と同じ＝PTY は host 追従・
  各 client は独立ミニ tmux。
- 帰結: Web の RESIZE は他者に無影響。送らないと proxy 既定 80x24 に
  固定され claude 画面が見切れる純粋なデグレだった。tmux socket-client
  も `client.go sendResize` で接続時＋SIGWINCH に RESIZE を送って
  おり、Web も同じプロトコルに揃えるのが正。

検証（多層）: ① go test（実 handler＋実 embed FS で swipe/dir/
prev-next の存在＋**RESIZE 送出コード `resizeFrame`/`0xff`/
`term.rows, term.cols` の存在**を機械検証）② node --check 構文
③ **実ブラウザ（chrome-devtools）ハーネス**で `#title=proj-beta —
PC-A`・`document.title`・`#pos=(2/4)`・JS エラー無し、`‹›`クリックと
**合成左スワイプ**双方が `/term?pc=PC-B&sid=b1&dir=gamma`（正しい
隣）へ遷移を確認。全 13 パッケージ緑。

## 不変条件継承

- relay はバイト透過のまま（ブラウザ viewer も同 protocol）。
  分類ヒューリスティック無し。
- 検証は実 API（エミュレータ）／実 PtyProxy＋実録画／display-oracle
  ／実ブラウザ（chrome-devtools）。合成では緑にしない。
- 稼働中の launchd（monitor / cloud agent）と既存 CLI 経路を壊さない
  （Web は追加・並存。relay 既存 /session は無改変）。
- SA 鍵・署名鍵はリポジトリ外。ブラウザに GCP 資格情報を渡さない。
- **PC 識別子（PCID）は安定であること**。`pcs/{pcID}` が PC の一意キー
  なので PCID が揺れると同一マシンが端末一覧に二重表示される。macOS は
  環境（launchd/ネットワーク/HostName 未設定）で `os.Hostname()` が
  `Mac-Studio` ↔ `Mac-Studio.local` と揺れる実バグがあった。
  `config.normalizeHost` が最初の `.` 以降（.local/DNS ドメイン）を
  落とし短ホスト名へ正規化（冪等）。明示したい場合は環境変数
  `PC_ID` で固定。既存の重複 doc は `DeletePC` で掃除（pcs/{id}＋
  sessions＋wake を削除。一時 `-tags manual` ヘルパで実施・非コミット）。
