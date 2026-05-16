// Web ターミナル本体（/term?pc=&sid=&dir=）。端末一覧からリンクで開く。
// relay の RESIZE/frame protocol をそのまま話す（無改変）。バー表示に
// ディレクトリ名、左右スワイプ／‹›でコンソール切替。
//
// RESIZE について: Go proxy では client（tmux socket-client/Web）の
// RESIZE は **その client 自身の per-client ビューポート描画サイズ**を
// 決めるだけで、PTY/claude のサイズは変えない（PTY は host=proxy 起動
// 端末の SIGWINCH のみ追従）。Python 設計の client/largest PTY 追従は
// Go へ未移植（host 生パススルーのみ特別扱い）。よって Web は
// socket-client と同様に窓サイズを送ってよい（他者影響なし）。送らない
// と proxy 既定 80x24 の小窓に固定され claude 画面が見切れるため送る。
"use strict";
const $ = (id) => document.getElementById(id);
const enc = new TextEncoder();
const qs = new URLSearchParams(location.search);
const pc = qs.get("pc"), sid = qs.get("sid"), dir = qs.get("dir") || "";

function resizeFrame(rows, cols) {
  const b = new Uint8Array(6);
  b[0] = 0xff; b[1] = 0xff;
  b[2] = (rows >> 8) & 0xff; b[3] = rows & 0xff;
  b[4] = (cols >> 8) & 0xff; b[5] = cols & 0xff;
  return b;
}

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
    ws.send(resizeFrame(term.rows, term.cols)); // 自分のビューポートサイズ
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
