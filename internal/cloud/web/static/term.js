// Web ターミナル本体（/term?pc=&sid=）。端末一覧ページからリンクで開く。
// relay の RESIZE/frame protocol をそのまま話す（無改変）。
"use strict";
const $ = (id) => document.getElementById(id);
const enc = new TextEncoder();
const qs = new URLSearchParams(location.search);
const pc = qs.get("pc"), sid = qs.get("sid");

function resizeFrame(rows, cols) {
  const b = new Uint8Array(6);
  b[0] = 0xff; b[1] = 0xff;
  b[2] = (rows >> 8) & 0xff; b[3] = rows & 0xff;
  b[4] = (cols >> 8) & 0xff; b[5] = cols & 0xff;
  return b;
}

function run() {
  if (!pc || !sid) { $("stat").textContent = "pc/sid がありません"; return; }
  $("title").textContent = pc + " : " + sid;

  const term = new Terminal({ cursorBlink: true,
    fontFamily: "Menlo,Consolas,monospace", fontSize: 13 });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open($("term-host"));
  fit.fit();

  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(proto + "//" + location.host + "/ws?pc=" +
    encodeURIComponent(pc) + "&sid=" + encodeURIComponent(sid));
  ws.binaryType = "arraybuffer";

  ws.onopen = () => {
    ws.send(resizeFrame(term.rows, term.cols));
    $("stat").textContent = "接続済";
  };
  ws.onmessage = (ev) => term.write(new Uint8Array(ev.data));
  ws.onclose = () => { $("stat").textContent = "切断"; };
  ws.onerror = () => { $("stat").textContent = "エラー"; };

  term.onData((d) => {
    if (ws.readyState === 1) ws.send(enc.encode(d));
  });
  window.addEventListener("resize", () => {
    fit.fit();
    if (ws.readyState === 1) ws.send(resizeFrame(term.rows, term.cols));
  });
}
run();
