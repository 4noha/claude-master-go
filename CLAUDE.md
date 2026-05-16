# claude-master-go プロジェクト

Claude Code セッション監視・tmux 自動同期 + ミニ tmux PTY プロキシ。
Python 版（`~/works/claude‐master`）の **Go 移植**。完全静的単一バイナリ
配布・GCP クラウド同期を目指す。設計詳細は [DESIGN.md](DESIGN.md)。

## ステータス / 移行方針（重要）

- **Python 版は放棄（新規投資しない）。知見は本ファイルに集約。**
- 稼働ベース（`claude-wrap`/launchd/このPC）の実切替は **Go が M2〜M5 を
  実録画検証で通ってから**。それまで Python を動かし続ける（環境を壊さない）。
  `~/.claude-master.toml` は両者同一キー＝設定移行不要（M1 で parity 確認済）。
- マイルストーン: **M1 config ✅** / **M2 VT モデル ✅** / **M3 ptyproxy ✅** /
  **M4 nav・pagekey・wheel・SESSION_LOG ✅** /
  **M5 monitor・tmux・socket_client・実行可能 proxy ✅（完全）** /
  **M6 GCP 同期（設計確定: `DESIGN_M6.md`。実装中）**。各 M は build＋
  実録画/実環境テスト緑で前進（合成では緑判定しない）。
  - M6 設計確定: wake=Firestore listener（FCM 不採用＝デスクトップ常駐
    向き・near-$0・PC 発 NAT 越え）、データ線=Cloud Run WSS で既存
    RESIZE/SCROLL/frame protocol をトンネル（`internal/client` 再利用・
    新プロトコル無し）、quiescence で開閉。
  - **M6a–d ✅**（実 WSS／gcloud Firestore エミュレータ＝実 API／実
    PtyProxy＋実録画／display-oracle で全検証。Cloud Functions は
    viewer 直接 wake 書込で不要化＝簡素化）。
  - **M6e ✅**（ユーザー承認の上 実 GCP デプロイ済）: 専用 project
    `example-gcp-project`／Cloud Run relay `claude-master-relay`
    （`https://claude-master-relay-demo01-an.a.run.app`、min0・
    timeout3600・no-cpu-throttling）／Firestore Native(asia-northeast1)
    ／SA `cm-agent`(datastore.user, 鍵 `~/.claude-master/sa.json`)。
    実 Cloud Run＋実 Firestore 経由 e2e（wake→トンネル→display-oracle）
    緑（`go test -tags manual` の `TestE2ERealGCP`）。**`/healthz` は
    GFE 予約で遮断＝ヘルスは `/`**。Python 版に無いクラウド同期を Go で
    新規達成。`cloud agent` の launchd 常駐化（本番運用化）は別途確認。
  - 稼働環境 cutover 実施済: proxy alias→Go、monitor→Go launchd
    （`~/Library/LaunchAgents/com.4noha.claude-master.plist`、KeepAlive
    自動復帰検証済）。Python 版は新規不使用（rollback 手順は会話/.bak）。
  - **Python 版との機能 parity 到達（M5e まで完了）= cutover 解禁。**
    cutover（claude-wrap/launchd を Go バイナリへ実切替）は稼働環境を
    変える不可逆・対外操作のためユーザー明示確認後に実施（それまで
    Python 稼働継続）。手順は「移行カットオーバー手順」節参照。
  - M5 内訳: M5a scanner（ps/lsof・実環境 5 セッション実検出）、M5b tmux
    （実 tmux 隔離セッション CRUD）、M5c socket_client（実 socket→実
    Server→pan を display-oracle）、M5d-1 実行可能 proxy（claude-wrap
    置換＝cutover 中核・実録画ラップ統合検証）、M5d-2 monitor ループ
    ＋start/stop/status＋dashboard（実 scan→実 tmux 同期）。
  - M5e 内訳: M5e-1 使用量 status（extract_usage/is_active＋
    _maybe_write_status、実録画で is_active 検証・usage は実 negative）、
    M5e-2 limit_watcher/resume_scheduler＋RunLoop 完全配線（実 status
    スキーマ・実 reset 形式・実 unix socket・実 tmux で中断/再開検証）、
    M5e-3 dashboard.py render 完全移植（実スキーマで枠整合・全要素）。
  - M4 内訳: M4a `IsLiveResetKey`/`ClassifyWheel`（Python 厳密ケース同値）、
    M4b `Server.HandleHostInput`（host nav/pagekey/wheel 状態機械。Python
    `_handle_host_stdin` と判定順含め 1:1、実 VT＋display-oracle 検証）、
    M4c `HistoryFlusher`＋`SESSION_LOG`（実 resume-burst 録画を本番
    capture 経路で転写・ANSI 無・dedup 無、`test_session_log.py` と 1:1）。
  - HOST_FLOW_SCROLLBACK は実 Claude で構造的に不完全（Python 既知制約）
    のため Go へは未移植（SESSION_LOG で代替＝描画非依存で安全）。

## 開発の鉄則（Python 版で何度も破って学んだ）

1. **推測修正をしない。** 「動かない」報告はまず原因を実再現してから直す。
2. **実テストで担保。** 単体ロジックでなく *実キーパス / 実 claude 録画*
   で検証。Python の `~/works/claude‐master/debug/fixtures/*/bytes.bin`
   （特に `resume-burst` = 実 `claude --resume` 録画）を Go 回帰へ流用し、
   Python の display-oracle と挙動一致を機械確認する。合成ストリームだけ
   で緑にしない。修正前に「旧コードでそのテストが落ちる」ことを確認。
3. **キー到達性は `sed -n l` で切り分け。** 端末でキーを押しエコーが
   出れば pty 到達、出なければ上位レイヤ（端末アプリ/Karabiner）が横取り
   ＝アプリ側では直せない。下記「端末キー到達性」参照。

## 不変条件（Python 版から継承・絶対）

- **ヒューリスティック分類は一切しない**（log/footer 推測・キーワード判定・
  lazy emission・内容 dedup）。これが脆さの根。あるのは「忠実な VT
  エミュレート＋ viewport 再描画」だけ。
- **スクロール位置は canvas 先頭（最古）からの絶対アンカー**。最下部基準
  だと遡り中に claude 出力で canvas が伸び表示がドリフトする（実録画で
  確認済の実バグ）。末尾追記で view が動かない＝tmux copy-mode と同じ。
- **quiescence ゲート**: 端末ネイティブ scrollback へ流すのは claude
  静止時のみ。ストリーミング中は確定行を内部に capture するだけ。
- **クリアは `\x1b[2J\x1b[9999;1H`**（全消去後カーソルを画面最下部へ）。
  claude は「カーソル最下部前提で下から描画」するため最上部だと崩れる。
- claude --resume は `\x1b[2J` せず絶対座標で会話を再ストリーム→ pyte/VT
  が同内容を複数回スクロールし history が重複。dedup は禁手なので
  ファイル転写（SESSION_LOG）か `SIZE_POLICY=host` 生パススルーで対処。
- **モード**（`SIZE_POLICY`、既定 `client`=tmux 基準）: client / host
  (生パススルー) / largest / smallest / latest。`largest` 等は host も
  ScrollRenderer 再描画。`_apply_pty_size` は **実 host 端末サイズを毎回
  読み直す**（古い値固定だと largest が client サイズに張り付く実バグ）。

## 端末キー到達性（Mac JIS / VSCode / Terminal.app）

nav-mode/PAGEKEY/WHEEL は「キーが pty まで届く」のが前提。届かない事象は
アプリ側では直せない。切り分けは常に `sed -n l`。

- **PageUp/PageDown/Home/End**: 端末アプリが既定でスクロールに割当て pty
  に送らない。対処は端末側設定:
  - VSCode `~/Library/Application Support/Code/User/keybindings.json`:
    `workbench.action.terminal.sendSequence` で `[5~`/`[6~`
    (when: terminalFocus)。
  - Terminal.app: 設定→プロファイル→キーボードで `page up`→「テキスト
    送信」`\033[5~`（Esc キーで `\033`）、`page down`→`\033[6~`。
- **矢印/文字/`Ctrl-\`(=\x1c)** は素で届く ⇒ nav-mode は設定不要で確実。
- **JIS keycode 実測**: `_`=`international1`(Ctrl→`\x1f`)、ANSI`\`=
  `backslash`(→`\x1d`)、`¥`=`international3`(→**`\x1c`** ✅)。`Ctrl-_`で
  NAV_KEY(`\x1c`)を出す Karabiner: `from international1`(mandatory
  control,optional shift)→`to international3`+`left_control`、端末/VSCode
  に scope。素シェルで `\x1c`=SIGQUIT だが raw モードの proxy には届く
  （検証は claude 内で）。
- claude は `?1004h`(focus)/`?2004h`(bracketed paste) を有効化 ⇒ 端末が
  `\x1b[I`/`\x1b[O` 等を自発送出。scroll の live 復帰は **実ユーザー操作
  のみ**で行い、focus/mouse/デバイス応答等 passive では戻さない
  （`is_live_reset_key` 相当を Go でも実装）。

## アーキテクチャ（Python → Go）

| Python | Go pkg | 状態/備考 |
|--------|--------|-----------|
| config.py | `internal/config` | ✅ M1。env > ~/.claude-master.toml > 既定。NAV_KEY パーサ移植・実 toml で Python と全項目一致 |
| pty_scroll.py / pty_emulator.py | `internal/screen` | ✅ M2(山場)。自前 VT モデル（vt10x/x/vt はデータで棄却）＋ history.top ＋ 先頭アンカー ＋ render_viewport。✅ M4c HistoryFlusher/line_to_text/IsLiveResetKey/ClassifyWheel |
| pty_proxy.py | `internal/ptyproxy` | ✅ M3 creack/pty fork+exec＋unix socket 多重化＋RESIZE/SCROLL。✅ M4b `HandleHostInput`（host nav/pagekey/wheel）＋ M4c `SESSION_LOG` |
| socket_client.py | `internal/client` | ✅ M5c unix socket + x/term raw・client 側 nav/pagekey/wheel（分類器は M4 共有）・実 socket→Server pan を display-oracle 検証 |
| process_scanner.py | `internal/scanner` | ✅ M5a ps/lsof・実環境 5 セッション実検出 |
| tmux_manager.py | `internal/tmux` | ✅ M5b 実 tmux 隔離セッション CRUD |
| monitor.py | `internal/monitor` | ✅ M5d-2 scan 差分→tmux 同期＋start/stop/status＋最小 dashboard。limit_watcher/resume_scheduler は M5e |
| pty_proxy.py main/run/_loop | `internal/ptyproxy` (RunProxy) | ✅ M5d-1 実行可能 proxy＝claude-wrap 置換（cutover 中核）。使用量 status は M5e |
| debug/ replay+display-oracle | `test/` + 流用 fixtures | 全 M で実録画回帰 |
| （新規）クラウド同期 | `internal/sync` 等 | M6。FCM wake + Cloud Run WSS + Firestore（DESIGN.md）|

`internal/selfupdate` + `claude-master update`、`install.sh`、
`.goreleaser.yaml`、`Makefile` は実装済（配布: 完全静的・CGO 不要・
darwin/linux × amd64/arm64・asset 名 `claude-master_<os>_<arch>` +
`checksums.txt` を install.sh/selfupdate/goreleaser/make dist で一致）。

## 配布 / 更新

- ワンライナー: `curl -fsSL https://raw.githubusercontent.com/4noha/claude-master-go/main/install.sh | sh`
- 更新: `claude-master update`（sha256 検証・原子置換）or 同ワンライナー再実行
- リリース: `git tag vX.Y.Z && git push --tags`（goreleaser CI 化は未）
  or ローカル `make dist`。**実 DL 経路は Release 発行後に有効。**

## ビルド / テスト

```sh
export PATH="/opt/homebrew/bin:$PATH"   # Homebrew Go
go test ./... && go vet ./...
make build VERSION=$(git describe --tags --always)
make dist                               # 配布物 + checksums.txt
```

## GCP クラウド同期（M6・設計確定済）

push-wake / 差分時のみ / NAT 越え。FCM(無料,wake) + Cloud Functions
(scale-to-zero,差分トリガ) + Cloud Run(min0,WSS データ線) + Firestore
(状態)。常時 VM ゼロ・課金は実同期量比例で小規模ほぼ$0。「差分なし＝
データ線切断」は quiescence 判定を再利用。詳細・コスト表は DESIGN.md。

## 移行カットオーバー手順（Go が M5 検証通過後）

1. `make dist` → Release 発行（or install.sh 経由で配置）。
2. `~/.claude-master.toml` はそのまま（キー互換・parity 済）。
3. `claude` エイリアス/`claude-wrap` を Go バイナリ呼び出しへ差し替え。
4. launchd plist の監視デーモンを Go の monitor へ差し替え、旧 Python
   デーモン停止。
5. 実 claude セッションで nav/pagekey/wheel/scroll/同期を実機確認。
6. 問題あれば即 Python ベースへロールバック（Python は当面残置）。
