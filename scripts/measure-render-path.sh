#!/bin/bash
# measure-render-path.sh: 3 経路 (bare-tmux / tmux-render / tmux-wrap) で
# script で stdout 録画→構造解析。L4 系の真因特定のための baseline
# 計測 (CLAUDE.md 鉄則 #1「推測修正をしない・実再現してから直す」遵守)。
#
# 使い方:
#   bash measure-render-path.sh <label>
#   <label> は capture 区別用 (e.g. "vscode-claude-streaming")。
#   script で attach→user が 10 秒程度 scroll/streaming 再現→Ctrl-b d で detach
#   →自動解析結果を表示
#
# 録画 path: /tmp/measure-<label>-<経路>.raw
# 解析結果は標準出力に。
set -e
LABEL="${1:-default}"
DIR=/tmp/measure-$LABEL
mkdir -p "$DIR"

usage() {
  cat <<EOF
usage: bash measure-render-path.sh <label> <経路>

経路:
  bare      = 素 tmux attach (baseline)
  render    = claude-master tmux-render (L4-A')
  wrap      = claude-master tmux-wrap -- tmux attach (L2)
  analyze   = 既存 capture を解析比較のみ

label が同じ複数経路を回して比較する想定。

例:
  bash measure-render-path.sh m1 bare      # 1. 素 tmux で 10s capture (detach で終了)
  bash measure-render-path.sh m1 render    # 2. tmux-render で同様
  bash measure-render-path.sh m1 wrap      # 3. tmux-wrap で同様
  bash measure-render-path.sh m1 analyze   # 4. 3 件の解析結果を比較表示
EOF
}

mode="${2:-}"
case "$mode" in
  bare)
    OUT="$DIR/bare.raw"
    echo "=== capture bare tmux attach to /tmp/measure-$LABEL/bare.raw ==="
    echo "    10 秒程度 scroll/typing で flicker 再現してから Ctrl-b d で detach"
    sleep 1
    script -q "$OUT" tmux attach -t claude-master
    echo "captured: $(ls -la $OUT)"
    ;;
  render)
    OUT="$DIR/render.raw"
    echo "=== capture claude-master tmux-render to /tmp/measure-$LABEL/render.raw ==="
    echo "    10 秒程度 scroll/typing で flicker 再現してから Ctrl+C で抜ける"
    sleep 1
    script -q "$OUT" /Users/4noha/works/claude-master-go/claude-master tmux-render -t claude-master
    echo "captured: $(ls -la $OUT)"
    ;;
  wrap)
    OUT="$DIR/wrap.raw"
    echo "=== capture claude-master tmux-wrap to /tmp/measure-$LABEL/wrap.raw ==="
    echo "    10 秒程度 scroll/typing で flicker 再現してから Ctrl-b d で detach"
    sleep 1
    script -q "$OUT" /Users/4noha/works/claude-master-go/claude-master tmux-wrap -- tmux attach -t claude-master
    echo "captured: $(ls -la $OUT)"
    ;;
  analyze|"")
    if [ -z "$mode" ]; then usage; exit 0; fi
    for f in "$DIR/bare.raw" "$DIR/render.raw" "$DIR/wrap.raw"; do
      if [ ! -f "$f" ]; then
        echo "missing: $f (まだ capture してないので skip)"
        continue
      fi
      echo ""
      echo "=== analyze $f ==="
      python3 - "$f" <<'PY'
import sys, re
path = sys.argv[1]
d = open(path, 'rb').read()
n = len(d)
if n == 0:
    print("  EMPTY"); sys.exit(0)
def count(pat): return d.count(pat)
def rcount(pat): return len(re.findall(pat, d))

print(f"  Total bytes        : {n}")
print(f"  ---")
print(f"  BSU (\\x1b[?2026h) : {count(b'\\x1b[?2026h')}")
print(f"  ESU (\\x1b[?2026l) : {count(b'\\x1b[?2026l')}")
print(f"  ?25l (hide)        : {count(b'\\x1b[?25l')}")
print(f"  ?25h (show)        : {count(b'\\x1b[?25h')}")
print(f"  ?7l (wrap-off)     : {count(b'\\x1b[?7l')}")
print(f"  ?7h (wrap-on)      : {count(b'\\x1b[?7h')}")
print(f"  \\x1b[2J full clear : {count(b'\\x1b[2J')}")
print(f"  \\x1b[J  partial    : {count(b'\\x1b[J')}")
print(f"  \\x1b[K  line clear : {rcount(rb'\\x1b\\[\\d*K')}")
print(f"  CUP \\x1b[r;cH      : {rcount(rb'\\x1b\\[\\d+;\\d+H')}")
print(f"  CHA \\x1b[NH        : {rcount(rb'\\x1b\\[\\d+H')}")
print(f"  SGR \\x1b[*m        : {rcount(rb'\\x1b\\[[\\d;]*m')}")
print(f"  Scroll IND \\x1bD   : {count(b'\\x1bD')}")
print(f"  Scroll RI \\x1bM    : {count(b'\\x1bM')}")
print(f"  Alt screen ?1049h  : {count(b'\\x1b[?1049h')}")
print(f"  Alt screen ?1049l  : {count(b'\\x1b[?1049l')}")
print(f"  Mouse on ?1000h    : {count(b'\\x1b[?1000h')}")

# BSU/ESU 内部 vs 外部 byte ratio
inside = 0
i = 0
bsu, esu = b'\\x1b[?2026h', b'\\x1b[?2026l'
while True:
    j = d.find(bsu, i)
    if j < 0: break
    e = d.find(esu, j)
    if e < 0: break
    inside += e + len(esu) - j
    i = e + len(esu)
naked = n - inside
print(f"  ---")
print(f"  inside BSU/ESU     : {inside} ({100*inside/n:.0f}%)")
print(f"  naked stream       : {naked} ({100*naked/n:.0f}%)")

# frame 推定 (BSU 個数 = frame 数)
nf = count(b'\\x1b[?2026h')
if nf > 0:
    print(f"  estimated frames   : {nf} (BSU pair)")
    print(f"  bytes/frame avg    : {n//nf}")
PY
    done
    echo ""
    echo "=== 解析完了。3 経路の構造を比較してください ==="
    ;;
  *)
    usage; exit 1
    ;;
esac
