#!/usr/bin/env python3
"""measure-render-path.sh で取った *.raw を構造解析する standalone script。
heredoc escape 問題を回避するため Python ファイルに分離。"""
import sys, re

if len(sys.argv) < 2:
    print("usage: analyze-render-capture.py <file.raw>")
    sys.exit(2)

path = sys.argv[1]
d = open(path, 'rb').read()
n = len(d)
if n == 0:
    print(f"  EMPTY: {path}")
    sys.exit(0)


def count(pat):
    return d.count(pat)


def rcount(pat):
    return len(re.findall(pat, d))


ESC = b'\x1b'
print(f"=== {path} ===")
print(f"  Total bytes        : {n}")
print(f"  ---")
print(f"  BSU ESC[?2026h     : {count(ESC + b'[?2026h')}")
print(f"  ESU ESC[?2026l     : {count(ESC + b'[?2026l')}")
print(f"  ?25l (cursor hide) : {count(ESC + b'[?25l')}")
print(f"  ?25h (cursor show) : {count(ESC + b'[?25h')}")
print(f"  ?7l (wrap-off)     : {count(ESC + b'[?7l')}")
print(f"  ?7h (wrap-on)      : {count(ESC + b'[?7h')}")
print(f"  ESC[2J full clear  : {count(ESC + b'[2J')}")
print(f"  ESC[J  partial cl  : {count(ESC + b'[J')}")
print(f"  ESC[*K line clear  : {rcount(rb'\x1b\[\d*K')}")
print(f"  CUP ESC[r;cH       : {rcount(rb'\x1b\[\d+;\d+H')}")
print(f"  CHA ESC[NH         : {rcount(rb'\x1b\[\d+H')}")
print(f"  SGR ESC[*m         : {rcount(rb'\x1b\[[\d;]*m')}")
print(f"  Scroll IND ESC D   : {count(ESC + b'D')}")
print(f"  Scroll RI ESC M    : {count(ESC + b'M')}")
print(f"  Alt screen ?1049h  : {count(ESC + b'[?1049h')}")
print(f"  Alt screen ?1049l  : {count(ESC + b'[?1049l')}")
print(f"  Mouse on ?1000h    : {count(ESC + b'[?1000h')}")

# BSU/ESU 内部 vs 外部 byte ratio
inside = 0
i = 0
bsu, esu = ESC + b'[?2026h', ESC + b'[?2026l'
while True:
    j = d.find(bsu, i)
    if j < 0:
        break
    e = d.find(esu, j)
    if e < 0:
        break
    inside += e + len(esu) - j
    i = e + len(esu)
naked = n - inside
print(f"  ---")
print(f"  inside BSU/ESU     : {inside} bytes ({100 * inside / n:.0f}%)")
print(f"  naked stream       : {naked} bytes ({100 * naked / n:.0f}%)")

nf = count(bsu)
if nf > 0:
    print(f"  BSU-bounded frames : {nf}")
    print(f"  bytes/frame avg    : {inside // nf if nf else 0}")
