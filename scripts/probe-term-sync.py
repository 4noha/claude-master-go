#!/usr/bin/env python3
"""probe-term-sync.py — 端末が DECSET 2026 (synchronized output) を
認識するかを DECRQM で機械判定する (目視判定不要)。

使い方:
    python3 scripts/probe-term-sync.py
    （調べたい端末 = VSCode terminal / iTerm2 / Terminal.app で、
      **tmux の外** で直接実行すること。tmux 内だと tmux が応答して
      外側端末ではなく tmux の対応状況を測ってしまう）

仕組み:
  1. DA1 (CSI c) を送る — ほぼ全端末が応答する＝応答チャネルの生存確認
  2. DECRQM (CSI ? 2026 $ p) を送る — 対応端末は CSI ? 2026 ; Ps $ y で応答
     Ps: 0=非認識 / 1=set 中 / 2=reset 中 / 3=恒久 set / 4=恒久 reset
     → Ps が 1-4 なら「2026 を認識する端末」= BSU/ESU で atomic 描画可能

判定:
  SUPPORTED      : 2026 を認識 (Ps=1/2/3/4)。BSU/ESU wrap が効く
  NOT-RECOGNIZED : DECRQM には応答したが 2026 は非認識 (Ps=0)
  NO-REPLY       : DECRQM 自体に未応答 (DECRQM 未実装の可能性)。
                   この場合のみ scripts/diag-terminal-render.sh の
                   test4 (目視) で最終判定すること
"""
import os
import select
import sys
import termios
import tty

TIMEOUT_SEC = 1.0


def read_reply(fd, timeout=TIMEOUT_SEC):
    """fd から timeout 秒間 bytes を読み集めて返す（応答終端は時間で判断）。"""
    buf = b""
    deadline_grace = 0.15  # 最初の byte 到着後の追加読み待ち
    # 最初の byte を timeout まで待つ
    r, _, _ = select.select([fd], [], [], timeout)
    if not r:
        return buf
    while True:
        try:
            chunk = os.read(fd, 256)
        except OSError:
            break
        if not chunk:
            break
        buf += chunk
        r, _, _ = select.select([fd], [], [], deadline_grace)
        if not r:
            break
    return buf


def main():
    if os.environ.get("TMUX"):
        print("⚠ tmux 内で実行されています。これは tmux の応答を測って")
        print("  しまい、外側端末の判定になりません。tmux の外 (detach した")
        print("  素の端末) で再実行してください。")
        print()

    print(f"TERM_PROGRAM = {os.environ.get('TERM_PROGRAM', '(unset)')}")
    print(f"TERM         = {os.environ.get('TERM', '(unset)')}")
    print()

    try:
        fd = os.open("/dev/tty", os.O_RDWR)
    except OSError as e:
        print(f"NG: /dev/tty が開けない: {e}")
        sys.exit(2)

    old = termios.tcgetattr(fd)
    try:
        tty.setraw(fd)

        # 1. DA1 — 応答チャネル生存確認
        os.write(fd, b"\x1b[c")
        da1 = read_reply(fd)
        da1_ok = b"\x1b[?" in da1

        # 2. DECRQM ?2026
        os.write(fd, b"\x1b[?2026$p")
        rep = read_reply(fd)
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old)
        os.close(fd)

    print(f"DA1 応答       : {'あり' if da1_ok else '無し (異常: 応答チャネル未確立)'}")
    printable = rep.replace(b"\x1b", b"ESC").decode("ascii", "replace")
    print(f"DECRQM 生応答  : {printable!r}")

    # CSI ? 2026 ; Ps $ y を探す
    marker = b"\x1b[?2026;"
    idx = rep.find(marker)
    if idx >= 0:
        rest = rep[idx + len(marker):]
        ps = rest.split(b"$", 1)[0].decode("ascii", "replace")
        if ps in ("1", "2", "3", "4"):
            print()
            print(f"判定: SUPPORTED (Ps={ps}) — この端末は DECSET 2026 を認識します。")
            print("      BSU/ESU で囲まれた出力は atomic に描画されます。")
            sys.exit(0)
        else:
            print()
            print(f"判定: NOT-RECOGNIZED (Ps={ps}) — DECRQM には応答しますが")
            print("      2026 モードは非認識＝BSU/ESU wrap は無効です。")
            sys.exit(1)
    else:
        print()
        print("判定: NO-REPLY — DECRQM 自体に未応答。DECRQM 未実装の端末でも")
        print("      2026 を実装している場合があるため、最終判定は")
        print("      scripts/diag-terminal-render.sh の test4 (目視) で。")
        sys.exit(3)


if __name__ == "__main__":
    main()
