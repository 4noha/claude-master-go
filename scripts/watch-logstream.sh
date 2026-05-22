#!/bin/sh
# launchd 配下から `log stream` を起動する wrapper。subshell や bash
# 経由でなく **`log` を直接 exec** して launchd の KeepAlive 監視を
# 効かせる（exec で PID 1 = launchd の直子になり、log が死んだら確実
# に再起動される）。
#
# predicate は watch-vscode 等と同じ「VSCode 関連 + kernel/jetsam の
# crash 系」を抽出。VSCode crash の連鎖（terminated process / OOM）の
# 瞬間を記録する用途。
#
# 出力: ~/.claude-master/observe/log-stream.log
# 停止: launchctl bootout gui/$(id -u) com.4noha.claude-master.watch-logstream
exec /usr/bin/log stream --style syslog --predicate '
process == "Code Helper" OR process == "Code" OR process BEGINSWITH "node" OR
((subsystem == "com.apple.kernel" OR subsystem == "com.apple.runningboard" OR
  composedMessage CONTAINS "jetsam" OR composedMessage CONTAINS "EXC_CRASH" OR
  composedMessage CONTAINS "SIGSEGV" OR composedMessage CONTAINS "memorystatus_thread") AND
 (composedMessage CONTAINS "Code" OR composedMessage CONTAINS "ptyhost" OR
  composedMessage CONTAINS "claude-master"))'
