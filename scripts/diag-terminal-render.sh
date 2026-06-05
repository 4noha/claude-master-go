#!/bin/bash
# diag-terminal-render.sh
#
# 各端末で「render-tick 基盤か byte-level か」「DECSET 2026 を honor するか」
# を視覚的に確認する empirical テスト。`claude-master tmux-wrap` の有効性
# 判定材料に使う。CLAUDE.md「tmux 経由ちらつき残課題と対策案」参照。
#
# 使い方:
#   1. iTerm2 / Mac Terminal.app / VSCode terminal それぞれで本 script 実行
#   2. test1-4 の見え方を観察し、下表に当てはめる
#
# 判定表:
#                              test1   test2   test3   test4
#   render-tick (atomic)        瞬時    瞬時    瞬時    瞬時 (2026 honor)
#   render-tick (no sync)       瞬時    一気    瞬時    1 行ずつ流れる
#   byte-level                  一気    一気    流れる   流れる
#
# - test1/3 が「一気」「流れる」なら byte-level (case A wrapper の効果限定)
# - test2 が「一気」なら render-tick (case A wrapper 有効)
# - test4 が「瞬時」なら DECSET 2026 honor (端末側で既に atomic)

set -e

clear

cat <<'EOF'

=== Terminal Render Diagnostic ===

これから 4 つのテストを順に実行します。各 test の見え方を観察してください。
（test 間の "Press Enter" で進行）

EOF
read -r -p "準備できたら Enter > "

# ---- test 1: 大きな全画面 redraw を 1 write で送る ----
clear
echo "test 1: 全画面 24 行を 1 write で送信 (~0.3KB)"
echo "  期待 render-tick: 瞬時に全行表示"
echo "  期待 byte-level: 上から下へ一気に書かれる"
sleep 1
{
  printf '\x1b[2J\x1b[H'
  for i in $(seq 1 24); do
    printf '\x1b[%d;1H\x1b[3%dmRow %02d - filling the row with text >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>\x1b[0m\n' \
      "$i" $((i % 7 + 1)) "$i"
  done
} > /dev/tty
sleep 1
read -r -p "Press Enter > "

# ---- test 2: 同内容を 1 cell ずつ syscall 分割 (sleep 無し) ----
clear
echo "test 2: 同内容を 24 行 × 別 syscall で送信 (sleep 無し・連続書き込み)"
echo "  期待 render-tick: 瞬時 (tick 内に集約)"
echo "  期待 byte-level: 行ごと visible"
sleep 1
printf '\x1b[2J\x1b[H' > /dev/tty
for i in $(seq 1 24); do
  printf '\x1b[%d;1H\x1b[3%dmRow %02d - filling the row with text >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>\x1b[0m\n' \
    "$i" $((i % 7 + 1)) "$i" > /dev/tty
done
sleep 1
read -r -p "Press Enter > "

# ---- test 3: 同内容を 1 行ずつ sleep 4ms ----
clear
echo "test 3: 同内容を 1 行ずつ sleep 4ms で送信 (case A wrapper の idle 既定値)"
echo "  期待 render-tick: tick 跨ぎで行ごと visible (= wrapper で改善可能)"
echo "  期待 byte-level: 行ごと visible (= wrapper でも限界)"
sleep 1
printf '\x1b[2J\x1b[H' > /dev/tty
for i in $(seq 1 24); do
  printf '\x1b[%d;1H\x1b[3%dmRow %02d - filling the row with text >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>\x1b[0m\n' \
    "$i" $((i % 7 + 1)) "$i" > /dev/tty
  python3 -c "import time; time.sleep(0.004)" 2>/dev/null || sleep 0.005
done
sleep 1
read -r -p "Press Enter > "

# ---- test 4: DECSET 2026 (synchronized output) で 1 cell ずつ送る ----
clear
echo "test 4: DECSET 2026 で同期出力ブロックに包む (sleep 8ms ずつ)"
echo "  期待 2026 honor: ESU まで何も見えず、最後に瞬時表示"
echo "  期待 2026 無視: 1 行ずつ流れる"
sleep 1
{
  printf '\x1b[?2026h'
  printf '\x1b[2J\x1b[H'
} > /dev/tty
for i in $(seq 1 24); do
  printf '\x1b[%d;1H\x1b[3%dmRow %02d - filling the row with text >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>\x1b[0m\n' \
    "$i" $((i % 7 + 1)) "$i" > /dev/tty
  python3 -c "import time; time.sleep(0.008)" 2>/dev/null || sleep 0.01
done
printf '\x1b[?2026l' > /dev/tty
sleep 1
read -r -p "Press Enter > "

clear
cat <<'EOF'

=== 判定 ===

判定表:
                              test1   test2   test3   test4
  render-tick (2026 honor)    瞬時    瞬時    瞬時    瞬時 (ESU で 1 回表示)
  render-tick (no 2026)       瞬時    瞬時    瞬時    1 行ずつ流れる
  byte-level                  一気    流れる   流れる   流れる

  - test4 が瞬時 → DECSET 2026 honor (端末側で既に atomic 描画可能)
  - test4 流れる + test2 瞬時 → render-tick だが 2026 非対応
      → case A wrapper (idle batch) で大幅改善期待
  - test2/3 流れる → byte-level 描画 (case A wrapper でも限定的)

報告フォーマット:
  iTerm2:           test1=__ test2=__ test3=__ test4=__
  VSCode terminal:  test1=__ test2=__ test3=__ test4=__
  Terminal.app:     test1=__ test2=__ test3=__ test4=__

EOF
