#!/usr/bin/env python3
# claude-master proxy の host stdout 流量を **非侵襲で観測**するウォッチャ。
# proxy には一切触らない（snap ファイルを polling するだけ）。VSCode 端末
# を巻き込むクラッシュ仮説（出力洪水）の現認用。
#
# 仕組み:
#   ・~/.claude-master/diag/<pid>.snap を 1 秒毎に読む（proxy は 30s 周期で
#     原子書出＝rename。読み手は中途半端な状態を見ない）。
#   ・snap の ts が変わったタイミングで host_out_bytes/goroutines/num_gc/
#     heap_alloc_bytes の差分を計算。
#   ・burst（>= 1MB/s）検出時に 1 行 ALERT、それ以外は 60 秒毎にサマリ。
#   ・SIGINT/SIGTERM で穏やかに止まる（pid ファイル後始末有）。
#
# 使い方:
#   nohup python3 scripts/watch-host-out.py > /tmp/cm-watch.log 2>&1 &
#   tail -f /tmp/cm-watch.log
#   kill <watcher-pid>   # ~/.claude-master/cm-watch.pid に保存される
#
# 鉄則#1: 推測でなく実観測。次回 burst の現物（時刻・PID・速度・heap/
# goroutine の連動）を残す。

import json
import os
import signal
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

HOME = Path.home()
DIAG = HOME / ".claude-master" / "diag"
PID_FILE = HOME / ".claude-master" / "cm-watch.pid"
INTERVAL = 1.0         # 秒・polling 周期
BURST_KBPS = 1024      # 1 MB/s 以上で BURST 扱い
SUMMARY_EVERY = 60.0   # 秒・通常サマリ間隔
DUMP_THRESHOLD_MB = 50 # 1 イベントで 50MB 以上の差分は CRITICAL


def parse_ts(s):
    """RFC3339Nano → epoch float（パース不能は None）。"""
    if not s:
        return None
    # Python の fromisoformat は nanoseconds と +09:00 を扱える（3.11+）。
    try:
        return datetime.fromisoformat(s).timestamp()
    except Exception:
        # 古い Python 用 fallback（マイクロ秒で打切り）。
        try:
            return datetime.strptime(s[:26], "%Y-%m-%dT%H:%M:%S.%f").timestamp()
        except Exception:
            return None


def list_targets():
    """diag ディレクトリ内の <pid>.snap を全部対象に（追加 proxy も自動補足）。"""
    if not DIAG.exists():
        return []
    out = []
    for f in DIAG.iterdir():
        if f.suffix != ".snap":
            continue
        try:
            pid = int(f.stem)
        except ValueError:
            continue
        out.append(pid)
    return sorted(out)


def read_snap(pid):
    """snap を読み tasks: dict|None。"""
    p = DIAG / f"{pid}.snap"
    if not p.exists():
        return None
    try:
        s = json.loads(p.read_text())
        s["_ts_s"] = parse_ts(s.get("ts"))
        return s
    except Exception:
        return None


def proc_alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except (OSError, ProcessLookupError):
        return False


def fmt_local(ts):
    """epoch → ローカル時刻 HH:MM:SS.mmm。"""
    if ts is None:
        return "??:??:??"
    return datetime.fromtimestamp(ts).strftime("%H:%M:%S.%f")[:-3]


def log(msg):
    line = f"[{datetime.now().strftime('%H:%M:%S')}] {msg}"
    print(line, flush=True)


def write_pidfile():
    try:
        PID_FILE.parent.mkdir(parents=True, exist_ok=True)
        PID_FILE.write_text(str(os.getpid()))
    except Exception as e:
        log(f"pidfile write fail: {e}")


def remove_pidfile():
    try:
        if PID_FILE.exists() and int(PID_FILE.read_text() or "0") == os.getpid():
            PID_FILE.unlink()
    except Exception:
        pass


stop = False


def on_sig(signum, frame):
    global stop
    stop = True
    log(f"signal {signum} received; stopping...")


def main():
    write_pidfile()
    signal.signal(signal.SIGINT, on_sig)
    signal.signal(signal.SIGTERM, on_sig)
    log(f"watcher start pid={os.getpid()} diag={DIAG} interval={INTERVAL}s burst>={BURST_KBPS}KB/s")
    # pid -> last snap dict
    prev = {}
    last_summary = 0.0
    cycles = 0
    while not stop:
        targets = list_targets()
        # 生きてるプロセスのみ
        alive = [p for p in targets if proc_alive(p)]
        # 死んだ proxy は prev からも除去（再利用 PID で誤検出回避）
        for p in list(prev):
            if p not in alive:
                log(f"proxy {p} no longer alive; dropping prev")
                del prev[p]

        for pid in alive:
            s = read_snap(pid)
            if not s:
                continue
            if pid in prev and prev[pid]["_ts_s"] and s["_ts_s"]:
                dt = s["_ts_s"] - prev[pid]["_ts_s"]
                if dt > 0.05:  # snap 進んだ瞬間だけ計算
                    d_host = s["host_out_bytes"] - prev[pid]["host_out_bytes"]
                    d_go = s["goroutines"] - prev[pid]["goroutines"]
                    d_gc = s["num_gc"] - prev[pid]["num_gc"]
                    d_heap = s["heap_alloc_bytes"] - prev[pid]["heap_alloc_bytes"]
                    rate_kbps = (d_host / 1024.0) / dt
                    note = ""
                    if d_host >= DUMP_THRESHOLD_MB * 1_000_000:
                        note = " CRITICAL"
                    elif rate_kbps >= BURST_KBPS:
                        note = " BURST"
                    if note:
                        log(
                            f"{note} pid={pid} dt={dt:.1f}s "
                            f"host_out+{d_host/1e6:.1f}MB ({rate_kbps/1024:.2f}MB/s) "
                            f"go{d_go:+d} gc+{d_gc} heap{d_heap/1e6:+.1f}MB "
                            f"total={s['host_out_bytes']/1e6:.1f}MB go={s['goroutines']}"
                        )
                    prev[pid] = s
                # else: 同じ snap、何もしない
            else:
                prev[pid] = s

        cycles += 1
        now = time.time()
        if now - last_summary >= SUMMARY_EVERY:
            parts = []
            for pid in alive:
                s = prev.get(pid) or read_snap(pid)
                if not s:
                    continue
                parts.append(
                    f"{pid}:{s['host_out_bytes']/1e6:.1f}MB/{s['goroutines']}g/{s['num_gc']}gc"
                    f"@{fmt_local(s['_ts_s'])}"
                )
            log("SUMMARY " + "  ".join(parts) if parts else "SUMMARY (no live proxies)")
            last_summary = now

        time.sleep(INTERVAL)

    log("watcher stopped")
    remove_pidfile()


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        log(f"FATAL {e!r}")
        raise
    finally:
        remove_pidfile()
