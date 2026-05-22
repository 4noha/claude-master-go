#!/bin/sh
# ips-watch: ~/Library/Logs/DiagnosticReports に新規 .ips が現れたら
# ~/.claude-master/observe/ips-events.log に追記。30s 周期 polling。
# 用途: VSCode/ptyhost/Code Helper/claude-master 等のクラッシュ発生
# 瞬間を逃さず記録（fswatch 不在の代替）。

OUT="$HOME/.claude-master/observe/ips-events.log"
STAMP="$HOME/.claude-master/observe/ips-watch.stamp"
DIR="$HOME/Library/Logs/DiagnosticReports"

# 初期 stamp（既存ファイルは検出済として扱う）
touch "$STAMP"

echo "[$(date '+%Y-%m-%d %H:%M:%S')] ips-watch start pid=$$ dir=$DIR" >> "$OUT"

while true; do
  # stamp より新しい file を検出（.ips/.diag どちらも）
  find "$DIR" -type f \( -name "*.ips" -o -name "*.diag" \) -newer "$STAMP" 2>/dev/null | while read f; do
    BN=$(basename "$f")
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] NEW: $BN" >> "$OUT"
    # head 1 行（app_name / version 等 inline JSON 部分）
    head -1 "$f" 2>/dev/null | head -c 400 >> "$OUT"
    echo "" >> "$OUT"
  done
  touch "$STAMP"
  sleep 30
done
