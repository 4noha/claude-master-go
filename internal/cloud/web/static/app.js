// claude-master Web 管理 UI（M7c）。pairing cookie 前提。
// /api/pcs → /api/sessions → 選択で /ws に WebSocket、xterm.js で端末。
// relay の RESIZE/SCROLL/frame protocol をそのまま話す（無改変）。
"use strict";

const $ = (id) => document.getElementById(id);
const enc = new TextEncoder();

async function jget(u) {
  const r = await fetch(u, { headers: { Accept: "application/json" } });
  if (!r.ok) throw new Error(u + " -> " + r.status);
  return r.json();
}

function resizeFrame(rows, cols) {
  const b = new Uint8Array(6);
  b[0] = 0xff; b[1] = 0xff;
  b[2] = (rows >> 8) & 0xff; b[3] = rows & 0xff;
  b[4] = (cols >> 8) & 0xff; b[5] = cols & 0xff;
  return b;
}

let term, fit, ws;

function openSession(pc, sid, label) {
  $("list").style.display = "none";
  $("term").style.display = "block";
  $("title").textContent = label + "  [" + sid + "]";

  term = new Terminal({ convertEol: false, cursorBlink: true,
    fontFamily: "Menlo,Consolas,monospace", fontSize: 13 });
  fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open($("term-host"));
  fit.fit();

  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const url = proto + "//" + location.host + "/ws?pc=" +
    encodeURIComponent(pc) + "&sid=" + encodeURIComponent(sid);
  ws = new WebSocket(url);
  ws.binaryType = "arraybuffer";

  ws.onopen = () => {
    ws.send(resizeFrame(term.rows, term.cols)); // 起動時サイズ通知
    $("stat").textContent = "接続済";
  };
  ws.onmessage = (ev) => {
    term.write(new Uint8Array(ev.data)); // ANSI フレームをそのまま描画
  };
  ws.onclose = () => { $("stat").textContent = "切断"; };
  ws.onerror = () => { $("stat").textContent = "エラー"; };

  term.onData((d) => {
    if (ws && ws.readyState === 1) ws.send(enc.encode(d)); // raw 入力
  });
  const onResize = () => {
    if (!fit) return;
    fit.fit();
    if (ws && ws.readyState === 1) ws.send(resizeFrame(term.rows, term.cols));
  };
  window.addEventListener("resize", onResize);
}

async function main() {
  try {
    const pcs = await jget("/api/pcs");
    const pc = (pcs[0] || {}).id;
    if (!pc) { $("stat").textContent = "PC がありません"; return; }
    $("stat").textContent = "PC: " + pc;
    const ss = await jget("/api/sessions?pc=" + encodeURIComponent(pc));
    const ul = $("list");
    if (!ss || !ss.length) {
      ul.innerHTML = "<li>セッションがありません（PC 側で claude 起動中？）</li>";
      return;
    }
    ss.forEach((x) => {
      const li = document.createElement("li");
      const b = document.createElement("button");
      b.textContent = (x.short_dir || x.key || "session") +
        (x.is_active ? "  ●" : "");
      b.onclick = () => openSession(pc, x.key, x.short_dir || x.key);
      li.appendChild(b);
      ul.appendChild(li);
    });
  } catch (e) {
    $("stat").textContent = "エラー: " + e.message;
  }
}
main();
