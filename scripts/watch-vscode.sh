#!/bin/sh
# VSCode (Code main / Code Helper / ptyhost / utility 等) のプロセス
# 推移を 30s 毎にサンプリング。crash 直前の RSS/CPU トラジェクトリで
# 「terminal renderer overload」仮説の確証/反証を取る。
#
# 出力: ~/.claude-master/observe/vscode-proc.log
#   [ts] PID|RSS_MB|CPU%|name|cmd_short
# 30s 毎・PID ごとに 1 行。VSCode 関連プロセスのみフィルタ。
#
# Code は process tree が多い:
#  - "Code" (main, GUI)
#  - "Code Helper" / "Code Helper (Renderer)" / "Code Helper (GPU)"
#    / "Code Helper (Plugin)" / utility (node.mojom.NodeService = ptyhost)
# pgrep -fl で "Visual Studio Code.app" を含む command を全部拾う。
#
# 停止: kill $(cat ~/.claude-master/observe/vscode-proc.pid)

OBS="$HOME/.claude-master/observe"
mkdir -p "$OBS"
OUT="$OBS/vscode-proc.log"
PID_FILE="$OBS/vscode-proc.pid"

echo $$ > "$PID_FILE"
echo "[$(date '+%Y-%m-%d %H:%M:%S')] watch-vscode start pid=$$" >> "$OUT"

while true; do
  TS=$(date '+%Y-%m-%d %H:%M:%S')
  # VSCode 関連プロセスを ps から抽出（command に "Visual Studio Code" を含む）
  ps -A -o pid,rss,pcpu,comm,command 2>/dev/null | \
    awk -v ts="$TS" '
      /Visual Studio Code/ {
        pid=$1; rss=$2; cpu=$3; comm=$4;
        # 残り全部を command として連結
        cmd=""; for(i=5;i<=NF;i++) cmd=cmd" "$i;
        # subtype hint を抽出（--type=utility 等）
        type="-";
        if (cmd ~ /--type=/) {
          match(cmd, /--type=[^ ]+/);
          type = substr(cmd, RSTART+7, RLENGTH-7);
        }
        if (cmd ~ /utility-sub-type=node.mojom.NodeService/) type = "node(ptyhost?)";
        # type 不明（=--type フラグ無し）の場合は command 末尾 100 文字を
        # 追記して正体を取れるようにする。CPU 暴走 PID（前回 31498/
        # 31522 等）の出所追跡用＝末尾には --renderer-client-id=N /
        # --vscode-window-config=vscode:UUID / --enable-feature=... 等の
        # 特徴的 flag が並ぶ。CPU 高負荷時に絞らない＝静的プロセスでも
        # 正体不明を解消（次回 crash 解析時に救う）。
        tail_hint = "";
        if (type == "-") {
          n = length(cmd);
          if (n > 100) tail_hint = " tail=..." substr(cmd, n-96);
          else tail_hint = " tail=" cmd;
        }
        printf "[%s] pid=%-6d rss=%6.1fMB cpu=%5.1f%% type=%-20s name=%s%s\n",
          ts, pid, rss/1024, cpu, type, comm, tail_hint;
      }
    ' >> "$OUT"
  echo "" >> "$OUT"
  sleep 30
done
