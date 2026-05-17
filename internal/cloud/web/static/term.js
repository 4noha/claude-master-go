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

// SCROLL_MAGIC(0xff 0xfe) + int16 BE dy。proxy(server.go
// parseClientInput)が受けて per-client ScrollRenderer を pan＝
// ミニ tmux のスクリーン内スクロール。socket_client の sendScroll と
// 同一ワイヤ形式（dy<0=古い/上, dy>0=新しい/下, 32767=live 復帰）。
// xterm 自前スクロールは絶対座標再描画と衝突し表示破壊するので使わず
// 必ずこの変換を通す（他環境＝socket_client/nav と同じ規律）。
const SCROLL_STEP = 3;       // ホイール 1 ノッチあたり行
const PAGE_STEP = 10;        // PageUp/PageDown 1 回あたり行
const FOLLOW_DY = 32767;     // live（最下部）復帰
const TOP_DY = -32768;       // 最古へ（Home 相当・clamp16 と同値）
const TOUCH_ROW_PX = 16;     // 指の縦移動 何px で 1 行スクロールするか
function scrollFrame(dy) {
  const v = dy & 0xffff;     // int16 二の補数 下位16bit
  return new Uint8Array([0xff, 0xfe, (v >> 8) & 0xff, v & 0xff]);
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

  // scrollback:0 ＝ xterm 自前スクロールバックを持たない。proxy が
  // 毎フレーム絶対座標で全画面再描画する（ミニ tmux）ため、xterm が
  // ローカルスクロールすると衝突して表示が壊れる。スクロールは proxy
  // 側の managed scroll に一本化する（他環境の socket_client と同じ）。
  const term = new Terminal({ cursorBlink: true, scrollback: 0,
    fontFamily: "Menlo,Consolas,monospace", fontSize: 13 });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open($("term-host"));
  fit.fit();

  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(proto + "//" + location.host + "/ws?pc=" +
    encodeURIComponent(pc) + "&sid=" + encodeURIComponent(sid));
  ws.binaryType = "arraybuffer";
  const wsend = (b) => { if (ws.readyState === 1) ws.send(b); };

  let scrolled = false; // proxy 側で遡り中（タイプで live 復帰させる）
  const doScroll = (dy) => { wsend(scrollFrame(dy)); scrolled = true; };

  ws.onopen = () => {
    ws.send(resizeFrame(term.rows, term.cols)); // 自分のビューポートサイズ
    $("stat").textContent = "接続済";
  };
  ws.onmessage = (ev) => term.write(new Uint8Array(ev.data));
  ws.onclose = () => { $("stat").textContent = "切断"; };
  ws.onerror = () => { $("stat").textContent = "エラー"; };

  term.onData((d) => {
    // 遡り中に実入力 → まず live 復帰させてからキーを送る
    // （socket_client の pkScrolled リセットと同規律）。
    if (scrolled) { wsend(scrollFrame(FOLLOW_DY)); scrolled = false; }
    wsend(enc.encode(d));
  });

  // ホイール/トラックパッド → proxy の managed scroll へ変換。
  // xterm/ブラウザのネイティブスクロールは止める（衝突＝表示破壊源）。
  term.element.addEventListener("wheel", (e) => {
    e.preventDefault();
    doScroll(e.deltaY > 0 ? SCROLL_STEP : -SCROLL_STEP);
  }, { passive: false, capture: true });

  // PageUp/PageDown は claude へ送らずスクリーン内スクロールへ変換
  // （ユーザー要望「PageUp のときのように」）。Home/End 等は claude の
  // 行編集を壊さないため変換しない。
  term.attachCustomKeyEventHandler((e) => {
    if (e.type !== "keydown") return true;
    if (e.key === "PageUp") { doScroll(-PAGE_STEP); return false; }
    if (e.key === "PageDown") { doScroll(PAGE_STEP); return false; }
    if (e.shiftKey && e.key === "Home") { doScroll(TOP_DY); return false; }
    if (e.shiftKey && e.key === "End") {
      wsend(scrollFrame(FOLLOW_DY)); scrolled = false; return false;
    }
    return true;
  });

  // スマホ等のタッチ縦ドラッグ → proxy の managed scroll へ変換。
  // 横スワイプ（setupSwitch のコンソール切替）と衝突しないよう、縦が
  // 横より優位になった時だけスクロール扱い（その間は preventDefault で
  // ページスクロール/pull-to-refresh を抑止）。指を下げる=過去を見る
  // ＝SCROLL 負（content が指に追従。tmux copy-mode と同じ自然方向）。
  const thost = $("term");
  let tx0 = 0, ty0 = 0, tly = 0, tact = false, tvert = false, tacc = 0;
  thost.addEventListener("touchstart", (e) => {
    if (e.touches.length !== 1) { tact = false; return; }
    const t = e.touches[0];
    tx0 = t.clientX; ty0 = t.clientY; tly = t.clientY;
    tact = true; tvert = false; tacc = 0;
  }, { passive: true });
  thost.addEventListener("touchmove", (e) => {
    if (!tact || e.touches.length !== 1) return;
    const t = e.touches[0];
    const totDx = t.clientX - tx0, totDy = t.clientY - ty0;
    if (!tvert) {
      if (Math.abs(totDy) > 12 && Math.abs(totDy) > Math.abs(totDx)) {
        tvert = true; // 縦ドラッグ確定
      } else {
        return; // まだ横スワイプの可能性 → 触らない（切替に委ねる）
      }
    }
    e.preventDefault(); // ページスクロール/pull-to-refresh 抑止
    tacc += t.clientY - tly;
    tly = t.clientY;
    const rows = (tacc / TOUCH_ROW_PX) | 0; // 切り捨て（符号保持）
    if (rows !== 0) {
      tacc -= rows * TOUCH_ROW_PX;
      doScroll(-rows); // 指↓(rows>0)=過去=負 / 指↑=新しい=正
    }
  }, { passive: false });
  thost.addEventListener("touchend", () => { tact = false; },
    { passive: true });

  window.addEventListener("resize", () => {
    fit.fit();
    if (ws.readyState === 1) ws.send(resizeFrame(term.rows, term.cols));
  });
}
run();
