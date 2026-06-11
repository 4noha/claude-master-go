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

## リリース（メンテナ）

`.goreleaser.yaml` 同梱。`git tag vX.Y.Z && git push --tags` で CI
（goreleaser）が `claude-master_<os>_<arch>` + `checksums.txt` を発行。
ローカルでも `make dist` で同名・同形式の配布物を生成可能。

最新リリースは [Releases](https://github.com/4noha/claude-master-go/releases)。
install.sh / `update` の DL 経路は Release 発行済みで有効。
