#!/bin/sh
# claude-master ワンライナー導入 / 更新スクリプト。
#
#   curl -fsSL https://raw.githubusercontent.com/4noha/claude-master-go/main/install.sh | sh
#
# 再実行で最新へ更新（冪等）。`claude-master update` でも自己更新可。
# 環境変数:
#   CM_REPO     OWNER/REPO（既定 4noha/claude-master-go）
#   CM_VERSION  入れたい tag（既定 latest）
#   CM_BINDIR   インストール先（既定 ~/.local/bin、無ければ /usr/local/bin）
set -eu

REPO="${CM_REPO:-4noha/claude-master-go}"
VER="${CM_VERSION:-latest}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) echo "未対応 OS: $os" >&2; exit 1;; esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "未対応 arch: $arch" >&2; exit 1 ;;
esac
asset="claude-master_${os}_${arch}"

if [ "$VER" = "latest" ]; then
  base="https://github.com/${REPO}/releases/latest/download"
else
  base="https://github.com/${REPO}/releases/download/${VER}"
fi

bindir="${CM_BINDIR:-$HOME/.local/bin}"
sudo=""
if [ ! -d "$bindir" ]; then
  if mkdir -p "$bindir" 2>/dev/null; then :; else
    bindir=/usr/local/bin
    [ -w "$bindir" ] || sudo="sudo"
  fi
elif [ ! -w "$bindir" ]; then
  sudo="sudo"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "↓ ${REPO} ${VER} (${os}/${arch}) を取得中..."
curl -fSL --proto '=https' "$base/$asset"        -o "$tmp/cm"
curl -fSL --proto '=https' "$base/checksums.txt" -o "$tmp/sums"

# sha256 検証（改竄/破損防止）
want=$(grep " ${asset}\$" "$tmp/sums" | awk '{print $1}')
[ -n "$want" ] || { echo "checksums に $asset が無い" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/cm" | awk '{print $1}')
else
  got=$(shasum -a 256 "$tmp/cm" | awk '{print $1}')
fi
[ "$got" = "$want" ] || { echo "sha256 不一致（中止）" >&2; exit 1; }

chmod 0755 "$tmp/cm"
$sudo mkdir -p "$bindir"
$sudo mv "$tmp/cm" "$bindir/claude-master"
echo "✔ $bindir/claude-master"
"$bindir/claude-master" version 2>/dev/null || true

case ":$PATH:" in
  *":$bindir:"*) ;;
  *) echo "  ※ PATH に $bindir を追加してください（例: echo 'export PATH=\"$bindir:\$PATH\"' >> ~/.zshrc）" ;;
esac

# ---- claude シム（alias）の自動設定 ----
# v0.2.1+: `claude-master start` 経路を C 案完全自動化の前提とする
# （VSCode タブから見た restart-proxy 透過）。既存の `alias claude=
# '... proxy ...'` は `start` に置換、どこにも未設定なら新規追加。
# idempotent（複数回走らせ安全）。
cm_bin="$bindir/claude-master"
rc_target=""
shim_changed=0
for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.bash_profile"; do
  [ -f "$rc" ] || continue
  if grep -q "alias claude=.*claude-master[[:space:]]\\+\\(proxy\\|start\\)" "$rc" 2>/dev/null; then
    if ! grep -q "alias claude='$cm_bin start'" "$rc" 2>/dev/null; then
      sed -i.bak "s|alias claude=.*claude-master[[:space:]]\\+\\(proxy\\|start\\).*|alias claude='$cm_bin start'|" "$rc"
      echo "  shim: $rc を 'alias claude=$cm_bin start' に更新（.bak 退避）"
      shim_changed=1
    fi
    rc_target="$rc"
  fi
done
if [ -z "$rc_target" ]; then
  rc_target="$HOME/.zshrc"
  [ -f "$rc_target" ] || rc_target="$HOME/.bashrc"
  printf "\n# claude-master v0.2.1+: start 経由で C 案 (VSCode タブ自動復帰)\nalias claude='%s start'\n" \
    "$cm_bin" >> "$rc_target"
  echo "  shim: $rc_target に alias claude を新規追加"
  shim_changed=1
fi
if [ "$shim_changed" = 1 ]; then
  echo "  → 既存ターミナルでは 'source $rc_target' or 新規タブで反映"
fi

echo "更新は同じワンライナー再実行、または: claude-master update"
