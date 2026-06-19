# claude-master (Go)

Python 版 `claude-master` の Go 移植。**単一静的バイナリ**配布
（CGO 不要・依存なし・OS/arch 横断）。設計と移植ロードマップは
[DESIGN.md](DESIGN.md)。

## インストール（ワンライナー）

```sh
curl -fsSL https://raw.githubusercontent.com/4noha/claude-master-go/main/install.sh | sh
```

`~/.local/bin/claude-master` に入ります（書込不可なら `/usr/local/bin`
へ sudo）。PATH 未追加なら案内が出ます。

## アップデート（簡単）

どちらでも最新へ:

```sh
claude-master update                     # 自己更新（sha256 検証つき）
# または同じワンライナーを再実行（冪等）
curl -fsSL https://raw.githubusercontent.com/4noha/claude-master-go/main/install.sh | sh
```

`claude-master update` は GitHub Releases の最新 tag を見て、現バージョン
と同じなら何もしません。違えば該当 OS/arch バイナリを取得し
`checksums.txt` で sha256 検証してから実行中バイナリを原子的に置換します。

## サブコマンド（現状）

| cmd | 状態 |
|-----|------|
| `claude-master config`  | ✅ 設定解決値表示（env > ~/.claude-master.toml > 既定）|
| `claude-master version` | ✅ |
| `claude-master update`  | ✅ 自己更新（要: GitHub Release 発行済み）|
| `claude-master proxy`   | ✅ claude を PTY ラップ（cutover 中核）|
| `claude-master start`   | ✅ `claude` shim 経由の自動 attach / resume |
| `claude-master monitor` | ✅ session 監視・tmux 自動同期 |
| `claude-master cloud`   | ✅ クラウド同期（Web からセッションに接続）。[下記](#クラウド同期web-管理任意) |
| `claude-master tmux-render` | ✅ tmux -CC 中間層（古い tmux 用 flicker-free fallback。下記）|
| `claude-master tmux-wrap`   | ✅ idle batch wrapper（部分緩和）|

## tmux 経由のちらつき（重要）

`tmux attach` で Claude セッションを見るとき、**tmux のバージョンで
描画品質が変わります**。

- **tmux 3.7 以降（推奨）**: 素の `tmux attach` でちらつきゼロ。
  tmux 3.7 が pane 側 DECSET 2026（synchronized output）を実装し、
  proxy が出す完全な画面フレームの境界を保ったまま端末へ渡すため。
  端末側も DECSET 2026 対応であること（iTerm2 / 最近の VSCode
  terminal / WezTerm / kitty / alacritty。`scripts/probe-term-sync.py`
  で機械判定可能）。
- **tmux 3.6 以前**: 素の `tmux attach` は**ちらつきます**。tmux が
  pane の DECSET 2026 を解釈せずフレーム境界を壊し、画面の 40-64% を
  中間状態のまま端末へ流すため（実測）。この場合は **fallback** を
  使ってください:

  ```sh
  # tmux -CC 制御モードの %output（フレーム無傷）を再描画せず
  # verbatim 転送する中間層。古い tmux でもちらつきゼロ（単一 pane）。
  claude-master tmux-render -t claude-master
  ```

  終了は別端末から `pkill -TERM -f tmux-render`（全キーが claude へ
  渡るため）。tmux の prefix キー（window 切替等）は使えない単一 pane
  viewer の MVP です。

**まとめ**: ちらつき解消は全経路で可能。tmux 3.7 なら素の attach、
3.6 以前なら `tmux-render`、ブラウザなら Web コンソール、どれも
ちらつきゼロ。「素の attach だけで済むか、コマンドを 1 つ覚えるか」の
違いだけです。Homebrew なら `brew install tmux --HEAD`（3.7 リリース
前の暫定）→ リリース後 `brew upgrade tmux` で stable へ。

## クラウド同期・Web 管理（任意）

借りた PC やスマホのブラウザから、自分の Claude セッションに接続できます
（Google ログイン）。常時 VM ゼロ・差分時のみ通信で小規模ほぼ **$0**
（Cloud Run min-instances=0 + Firestore。無通信 30s で自動切断、入力で
自動復帰）。クラウド側のデプロイは所有者が一度だけ行います
（`deploy/deploy.sh`・GCP プロジェクト要）。

**仕組み**: PC 側の `claude-master cloud agent`（常駐）が、ローカルの
セッション一覧を Firestore へ push し、Cloud Run relay 経由の WSS で
画面フレームをトンネルします（既存の RESIZE/SCROLL/frame protocol を
そのまま使用＝新プロトコル無し）。クラウドは「画面の解釈」をせず
メタ情報のみ保持（不変条件）。

**端末を追加（enroll）**: Web の端末一覧で「＋ 端末を追加」を押すと
一回限りの enroll コードが出ます。追加したい PC で:

```sh
claude-master cloud enroll <code> --relay wss://<your-relay-host>
claude-master cloud agent          # 常駐化推奨（launchd / Windows は S4U タスク）
```

SA 鍵と設定（GCP プロジェクト / relay URL）が自動配置され、その PC の
セッションが Web の端末一覧に出ます。

**Web ターミナル**: 端末一覧の「開く」から xterm.js のターミナルへ。
ESC（生成中断）ボタン・画像貼付（モバイル可）・セッション復帰
（restart-proxy）に対応。DECSET 2026 非対応の端末でもブラウザ側
（`sync.js`）が atomic 描画＝ちらつき無し。

**複数クラウド（複数 Google アカウント）**: 1 台の PC を複数の独立した
クラウド（別 Google アカウント＝別 GCP プロジェクト / relay / SA 鍵）へ
同時接続できます。2 つ目以降のアカウントのクラウドで enroll すると
`~/.claude-master/clouds.json` に追記され、`cloud agent` が全クラウドへ
fan-out（同じセッションがどのアカウントの Web からも見える）。クラウド側
は無改変＝PC 側設定のみ。`clouds.json` が無ければ従来どおり単一クラウド
（env）で動きます。

| cmd | 説明 |
|-----|------|
| `claude-master cloud agent` | セッションを各クラウドへ push＋relay で wake/tunnel（常駐） |
| `claude-master cloud enroll <code> --relay wss://…` | Web 発行コードで設定 / SA 鍵を自動配置（2 つ目以降は `clouds.json` へ追記） |
| `claude-master cloud attach <sid> [--pc <PC>]` | 他 PC のセッションへ接続（viewer） |

## リリース（メンテナ）

`.goreleaser.yaml` 同梱。`git tag vX.Y.Z && git push --tags` で CI
（goreleaser）が `claude-master_<os>_<arch>` + `checksums.txt` を発行。
ローカルでも `make dist` で同名・同形式の配布物を生成可能。

最新リリースは [Releases](https://github.com/4noha/claude-master-go/releases)。
install.sh / `update` の DL 経路は Release 発行済みで有効。
