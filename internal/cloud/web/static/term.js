// Web ターミナル本体（/term?pc=&sid=&dir=）。端末一覧からリンクで開く。
// relay の frame protocol をそのまま話す（無改変）。ただし Web は
// RESIZE を送らない受動ビューア（理由は下記 VIEW_* コメント）。
// バー表示にディレクトリ名、左右スワイプ／‹›でコンソール切替。
"use strict";
const $ = (id) => document.getElementById(id);
const enc = new TextEncoder();
const qs = new URLSearchParams(location.search);
const pc = qs.get("pc"), sid = qs.get("sid"), dir = qs.get("dir") || "";

// Web コンソールは **ウィンドウサイズを送信しない**（RESIZE 不送出）。
// SIZE_POLICY=client では「最後に resize した client に PTY が追従」
// するため、ブラウザがサイズを送ると相手 PC の claude / tmux が
// ブラウザ窓サイズに引きずられる。Web は受動ビューアに徹し、proxy
// 既定（80x24）の viewport をそのまま受け取る。
const VIEW_COLS = 80, VIEW_ROWS = 24;

async function jget(u) {
  const r = await fetch(u, { headers: { Accept: "application/json" } });
  if (!r.ok) throw new Error(u + " -> " + r.status);
  return r.json();
}

// アカウント内の全コンソールを端末一覧と同じ順で平坦化（pc→session）。
// スワイプ/ボタンで前後のコンソールへ location 遷移して切り替える。
async function buildConsoleList() {
  const devs = await jget("/api/devices");
  const list = [];
  for (const d of devs) {
    const ss = await jget("/api/sessions?pc=" + encodeURIComponent(d.id));
    for (const x of ss || []) {
      list.push({
        pc: d.id, sid: x.key,
        dir: x.short_dir || x.key || "session",
      });
    }
  }
  return list;
}

function termURL(c) {
  return "/term?pc=" + encodeURIComponent(c.pc) +
    "&sid=" + encodeURIComponent(c.sid) +
    "&dir=" + encodeURIComponent(c.dir);
}

// コンソール切替（前後）。一覧取得失敗時は単独表示のまま無効化。
function setupSwitch() {
  let list = [], idx = -1;
  const prevB = $("prev"), nextB = $("next");
  const go = (delta) => {
    if (idx < 0 || list.length < 2) return;
    const n = (idx + delta + list.length) % list.length; // 巡回
    location.href = termURL(list[n]);
  };
  prevB.disabled = nextB.disabled = true;
  prevB.onclick = () => go(-1);
  nextB.onclick = () => go(1);

  buildConsoleList().then((l) => {
    list = l;
    idx = list.findIndex((c) => c.pc === pc && c.sid === sid);
    if (list.length > 1) {
      prevB.disabled = nextB.disabled = false;
      if (idx >= 0) $("pos").textContent = " (" + (idx + 1) + "/" + list.length + ")";
    }
  }).catch(() => { /* 切替不可でもターミナルは使える */ });

  // 左右スワイプ: 横移動が縦より優位かつ閾値超で前後へ。
  const host = $("term");
  let sx = 0, sy = 0, st = 0;
  host.addEventListener("touchstart", (e) => {
    const t = e.changedTouches[0];
    sx = t.clientX; sy = t.clientY; st = Date.now();
  }, { passive: true });
  host.addEventListener("touchend", (e) => {
    const t = e.changedTouches[0];
    const dx = t.clientX - sx, dy = t.clientY - sy;
    if (Date.now() - st < 800 &&
        Math.abs(dx) >= 60 && Math.abs(dx) > Math.abs(dy) * 1.4) {
      go(dx < 0 ? 1 : -1); // 左フリック=次 / 右フリック=前
    }
  }, { passive: true });
}

function run() {
  if (!pc || !sid) { $("stat").textContent = "pc/sid がありません"; return; }
  const label = dir ? (dir + " — " + pc) : (pc + " : " + sid);
  $("title").textContent = label;
  document.title = (dir || sid) + " — claude-master";

  setupSwitch();

  // サイズ非送信のため固定 80x24（proxy 既定 viewport と一致させ
  // 表示・カーソル整合を保つ）。FitAddon は使わない（窓追従＝サイズ
  // 変化送出になるため）。
  const term = new Terminal({ cursorBlink: true, cols: VIEW_COLS,
    rows: VIEW_ROWS, fontFamily: "Menlo,Consolas,monospace", fontSize: 13 });
  term.open($("term-host"));

  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(proto + "//" + location.host + "/ws?pc=" +
    encodeURIComponent(pc) + "&sid=" + encodeURIComponent(sid));
  ws.binaryType = "arraybuffer";

  ws.onopen = () => {
    // RESIZE フレームは送らない（受動ビューア）。
    $("stat").textContent = "接続済";
  };
  ws.onmessage = (ev) => term.write(new Uint8Array(ev.data));
  ws.onclose = () => { $("stat").textContent = "切断"; };
  ws.onerror = () => { $("stat").textContent = "エラー"; };

  term.onData((d) => {
    if (ws.readyState === 1) ws.send(enc.encode(d));
  });
  // window resize でも RESIZE は送出しない（意図的に何もしない）。
}
run();
