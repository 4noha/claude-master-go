# claude-master-go 設計

Python 版 `~/works/claude‐master`（~3857 行・119 履歴を squash 済）の
Go 移植。目的: **単一静的バイナリ配布**（CGO 不要・OS/arch 横断）と
GCP クラウド同期。

## 配布

- 純 Go + `creack/pty`（CGO_ENABLED=0）→ 完全静的。
- `GOOS/GOARCH` = darwin/linux × arm64/amd64 をクロスコンパイル。
- GoReleaser で GitHub Releases / GCS（署名 URL）配布。venv 不要。

## GCP クラウド同期（push-wake / 差分時のみ / NAT 越え）

サーバ→NAT 内 PC は直接不可。**制御線は常時・極軽量、データ線は差分時
のみ PC からアウトバウンド、静止(quiescence)で切断**。

| 役割 | サービス | 月額(小規模) |
|---|---|---|
| Push/wake（常時待受・NAT 越え）| **FCM** データメッセージ（旧 IoT Core は終了。MQTT 必須なら GCE 自前 broker でコスト増）| 無料 |
| 差分トリガ/通知 | Cloud Functions 2nd（scale-to-zero）| 待機$0 |
| データ同期 EP（PC が外向き WSS）| **Cloud Run** min-instances=0（1接続最大60分→再接続）| 同期中のみ |
| 状態（last-synced/トークン/メタ）| **Firestore** Native | 無料枠 |
| バイナリ配布 | Cloud Storage / GitHub Releases | ほぼ$0 |
| 認証 | Firebase Auth / デバイス短命トークン・PC発 mTLS | 無料 |

- 常時 VM ゼロ。課金は実同期量に比例（FCM 無料 / Run・Functions は
  scale-to-zero / Firestore 無料枠）。小〜中規模で実質ほぼ $0。
- 「差分なし＝切断」は claude-master 既存の **quiescence ゲート**
  （HistoryFlusher idle 判定）をデータ線開閉トリガに再利用。
- 完全ゼロ接続は wake 受信不可なので不可。制御線(FCM keepalive)だけは
  常時、これは無料。

### 各 PC 常駐に必要な新規要素

1. push エージェント（FCM 受信 → ローカル claude-master socket へ橋渡し）
2. 差分検出/トリガ（quiescence で同期要判定→相手 PC へ wake、静止で切断）
3. クラウド同期リレー（PC↔リモートセッションのストリーム中継・状態管理）

現 claude-master は同一ホストの Unix ドメインソケット前提（`socket_client`
ローカル専用・識別は `session_id`）。上記3点はクラウド層の新規実装。

## Python → Go 対応

| Python | Go pkg | 備考 |
|---|---|---|
| pty_proxy.py | internal/ptyproxy | creack/pty で fork+execv、x/sys/unix で TIOCSWINSZ |
| pty_scroll.py / pty_emulator.py | internal/screen | **pyte 相当が無い＝最大リスク**。VT モデル選定が肝 |
| socket_client.py | internal/client | unix socket + x/term raw mode |
| monitor/process_scanner/tmux_manager | internal/monitor | os/exec で ps/lsof/tmux |
| config.py | internal/config | env>toml>default（実装済 M1）|
| debug/ replay+display-oracle | test/ | **fixtures/*/bytes.bin を Go 回帰に流用** |

## マイルストーン（各々 build＆実録画テスト緑で進む）

- **M1 config**: env>toml>default を Python と同値（✅ scaffold 済、要 build 検証）
- **M2 VT モデル**(山場): 忠実画面モデル + history.top + 先頭アンカー。
  候補 `hinshun/vt10x` / `charmbracelet/x/vt` / 自前。`resume-burst`
  バイト列で Python display-oracle と挙動一致を機械検証してから進む。
- **M3 ptyproxy**: pty fork + raw I/O ループ + unix socket 多重化（最小 mini-tmux）
- **M4**: nav-mode/PAGEKEY/WHEEL/is_live_reset_key/quiescence 移植
- **M5**: monitor/tmux 同期
- **M6**: クラウド同期層（FCM wake + Cloud Run WSS + Firestore）

## 不変条件（Python 版から継承）

- ヒューリスティック分類はしない（脆さの根）。忠実 VT モデル + viewport
  再描画 + 先頭アンカーのみ。
- 「動かない」報告はまず `sed -n l` でキー到達を確認（端末/Karabiner 層）。
- 合成でなく実 claude 録画 + display-oracle で回帰検証。推測修正禁止。
