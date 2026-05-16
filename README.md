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
| `claude-master proxy`   | 🚧 M3 で実装（DESIGN.md）|

## リリース（メンテナ）

`.goreleaser.yaml` 同梱。`git tag vX.Y.Z && git push --tags` で CI
（goreleaser）が `claude-master_<os>_<arch>` + `checksums.txt` を発行。
ローカルでも `make dist` で同名・同形式の配布物を生成可能。

> 注: install.sh / `update` の DL 経路は **GitHub Release が 1 つ以上
> 発行されてから**有効。リポジトリ作成・tag push（外部公開操作）は
> 未実施。ローカルの build / test / クロスコンパイルは検証済み。
