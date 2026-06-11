> **STATUS (2026-06-11): FILED AS PR — https://github.com/tmux/tmux/pull/5195**
> (fix + repro included; branch `4noha/tmux:fix-sync-update-tty-leaks`).
> Root causes found by -vv log tracing: (1) ESU clears MODE_SYNC before
> pending collected items are discarded → flushed naked at end of input
> processing (2) server_client_reset_state follows pane cursor/modes
> after every input batch → naked cursor walk (the cursor-scatter)
> (3) screen_redraw_draw_pane force-stops sync and draws a partially
> updated (e.g. just-cleared) screen when a deferred redraw fires
> mid-frame. Fixes: discard-then-stop (screen_write_end_sync) / skip
> reset_state while MODE_SYNC / skip pane draw while MODE_SYNC.
> Measured: naked 72% -> 0.26%, half-drawn commits 0. Local patched
> build: /tmp/tmux-src/tmux (not installed).
>
> **Original note before the fix was written:** The pane-side DECSET 2026 support in master (next-3.7,
> issue 4744) engages, but **the bulk of the post-ESU redraw is emitted
> OUTSIDE the outer synchronized-update wrap**. Minimal repro (isolated
> server, 12fps producer emitting `BSU 2J + 20 rows + cursor ESU`):
> per producer frame the outer stream is
> `[6B naked][48B wrapped: scroll-region+S+cursor only][3600B naked:
> the actual row repaint!][1337B wrapped]` = **72% naked**. On a real
> client app (claude TUI), 13% naked including bare multi-CUP bursts
> (`[4;59H[12;68H[19;20H[22;68H`) = visible cursor scatter on terminals
> that DO honour 2026 (VSCode terminal verified honouring via a visual
> BSU-hold test). Workaround on our side: idle-batch + re-wrap layer
> (`tmux-wrap`) measured 0.0% naked, flush aligned 1:1 with frames.
> The original 3.6 report below is kept for methodology/history.

# (NEW, to file) master MODE_SYNC: post-ESU pane redraw partially emitted outside outer BSU/ESU wrap

Repro sketch (macOS, master @ 86128a7 via Homebrew --HEAD):

```sh
cat > /tmp/producer.sh <<'EOF'
#!/bin/sh
while :; do
  printf '\033[?2026h\033[?25l\033[2J\033[H'
  i=1
  while [ $i -le 20 ]; do
    printf '\033[%d;1HRow %02d ====================================================' $i $i
    i=$((i+1))
  done
  printf '\033[5;10H\033[?25h\033[?2026l'
  sleep 0.08
done
EOF
chmod +x /tmp/producer.sh
tmux -L sync new-session -d -s s -x 80 -y 24 /tmp/producer.sh
# terminal-features includes ',xterm*:sync'
script -q /tmp/outer.raw tmux -L sync attach -t s   # detach after ~5s
# analyze BSU/ESU coverage of /tmp/outer.raw → ~72% of bytes naked,
# repeating pattern: small wrapped commit (scroll+cursor) then large
# naked chunk containing the row content repaint.
```

Expected: with the new pane-2026 support, the entire per-frame redraw
(scroll AND content) should be inside one outer BSU/ESU block.
Observed: only the scroll/cursor portion is wrapped; the content
repaint follows naked, so 2026-honouring outer terminals still paint
the repaint incrementally (flicker/cursor scatter).

# tmux 3.6 outer redraw emits ~50% of bytes outside synchronized-output block, causing flicker even with `terminal-features '*:sync'`

## Summary

With `set -as terminal-features ',xterm*:sync'` declared and confirmed
in `#{client_termfeatures}`, tmux 3.6a still emits a significant
fraction (~50% in our measurements) of its outer terminal output
**outside** any `\x1b[?2026h ... \x1b[?2026l` block. The naked chunks
contain CUP + SGR + cell writes for content updates, not just one-shot
init or status-line refresh. In outer terminals that don't natively
buffer DECSET 2026 (xterm.js / Apple Terminal.app), this manifests as
visible per-cell rendering during scroll/redraw operations.

## Environment

- tmux: 3.6a (Homebrew on macOS)
- TERM: xterm-256color
- `set -as terminal-features ',xterm*:sync'` confirmed via
  `show-options -gv terminal-features` and
  `display -p '#{client_termfeatures}'` (contains `sync`)
- Reproducible inner-pane program: any continuously-emitting program
  (the one we use is `claude-master socket-client` from
  https://github.com/4noha/claude-master-go which is an in-house PTY
  multiplexer; the issue is **not specific to that program** — a
  simpler reproducer is described below)

## Reproduction

Minimal reproducer using a long-running producer:

```bash
# Terminal A: start an isolated tmux server with sync declared
tmux -L diag kill-server 2>/dev/null
TERM=xterm-256color tmux -L diag \
  set-option -g terminal-features ',xterm*:sync' \; \
  new-session -d -s s -x 100 -y 30 \
  'while true; do for i in $(seq 1 24); do
     printf "\x1b[%d;1H\x1b[3%dmRow %02d - filling text >>>>>>>>>>\x1b[0m" \
       "$i" $((i%7+1)) "$i";
   done; sleep 0.1; done'

# Terminal B: capture tmux outer bytes via script(1)
script -q /tmp/outer.raw tmux -L diag attach -t s
# (wait ~10s of producer running, then detach with Ctrl-b d)

# Analyze: BSU/ESU coverage of the captured bytes
python3 - <<'PY'
data = open('/tmp/outer.raw', 'rb').read()
bsu = b'\x1b[?2026h'
esu = b'\x1b[?2026l'
inside = 0
i, last = 0, 0
while True:
    j = data.find(bsu, i)
    if j < 0: break
    e = data.find(esu, j)
    if e < 0: break
    inside += e + len(esu) - j
    i = e + len(esu)
naked = len(data) - inside
print(f"Total {len(data)} bytes")
print(f"Inside BSU/ESU: {inside} ({100*inside/len(data):.0f}%)")
print(f"Naked stream:   {naked} ({100*naked/len(data):.0f}%)")
PY
```

Expected output (in our measurements):
```
Total 17243 bytes
Inside BSU/ESU: 9490 (55%)
Naked stream:   7753 (45%)
```

The naked region contains 1800-3200 byte chunks each, with content like:

```
\x1b[11;1H<spaces>\x1b[12;1H\x1b[38;2;153;153;153m<text>\x1b[39m
\x1b[13;1H<spaces>\x1b[14;1H...
```

i.e., consecutive multi-row updates (CUP + SGR + cells) emitted as a
naked stream rather than wrapped in `\x1b[?2026h ... \x1b[?2026l`.

## Expected Behavior

When `terminal-features` includes `sync`, **all** content updates emitted
by the tmux client to the outer terminal should be wrapped in BSU/ESU
so that 2026-aware outer terminals can commit them atomically. This is
necessary to prevent visible per-cell rendering in outer terminals that
honor 2026.

## Actual Behavior

Roughly half the outer output is emitted naked. Specifically observed:
- `\x1b[2J` full clear: emitted naked on initial attach (probably
  acceptable for one-shot init)
- Multi-row content updates after each BSU/ESU-wrapped frame: emitted
  naked (this is the problematic case — happens for every refresh
  cycle)

## Hypothesis on Root Cause

We have not read the tmux source carefully enough to pinpoint the exact
code path. Candidates we considered:

1. The sync-wrap might be applied per `tty_redraw_region()` /
   `tty_draw_pane()` calls but skipped for separate "secondary" redraw
   paths invoked from screen refresh, pane border updates, or
   status-line redraws that happen alongside pane content updates.
2. Possibly tmux emits the "main pane redraw" inside the wrap and a
   "secondary update" for adjacent cells outside the wrap.

Help from maintainers in identifying the actual emission path would be
valuable.

## Why This Matters

DECSET 2026 honor varies by outer terminal:
- iTerm2 3.4+: honored
- kitty / alacritty / WezTerm: honored
- Apple Terminal.app: **not honored** (still flickers on naked stream)
- xterm.js (used by VSCode integrated terminal, web terminals via
  Codespaces / vscode.dev / similar): **not honored** for raw
  inter-tty bytes (libraries like xterm-addon-sync exist but not
  bundled in many deployments)

In 2026-honoring terminals, fixing tmux's sync-wrap to be complete
would eliminate the flicker entirely. In non-honoring terminals, the
naked stream is rendered per-byte and is visible regardless of sync
declaration, but at least we'd know that fixing it on the terminal
side is the only remaining issue.

## Workaround We Implemented

We added an idle-based byte batching wrapper at the layer between
tmux client and the outer terminal (a `tmux-wrap` subcommand in our
project). It batches all tmux outer bytes for ~4ms and writes them in
a single syscall, which somewhat mitigates flicker on render-tick-based
terminals regardless of 2026 honor. This is not a fix for tmux but a
band-aid for our users.

## Attached Files

If maintainers find it useful, we can attach:
- The full captured `outer.raw` (~30KB)
- A python script that decomposes it into BSU/ESU-wrapped vs naked
  chunks for inspection

Thank you for tmux — it's an indispensable tool for our workflow.
