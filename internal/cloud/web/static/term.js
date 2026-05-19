// Web ターミナル本体（/term?pc=&sid=&dir=）。端末一覧からリンクで開く。
// relay の frame protocol をそのまま話す（無改変）。
//
// 設計（暴走の原因除去）: Web は **固定論理サイズ 160×500 の素朴な
// ビューア**。自分の DOM 寸法を一切測らず（FitAddon 不使用）、RESIZE は
// 接続時に 1 回だけ送る。ブラウザの resize/ズーム/スクロール/モバイル
// URL バー出入りでは **再 RESIZE しない**＝「測る→RESIZE→proxy 全消去
// 再描画→レイアウト変化→また測る」のフィードバック暴走を構造的に断つ。
// proxy は 160×500 の viewport を絶対座標で再描画するだけ（モデル→
// viewport のまま＝claude --resume 再ストリームでも重複しない）。背の
// 高い固定グリッドを #term-host の overflow で **ブラウザ native スク
// ロール**して読む（セル書換は scrollTop を動かさないので崩れない）。
// 横の見切れは固定広幅＋ブラウザのピンチズーム/横パンで閲覧。
// コンソール切替は ‹/› ボタン（横スワイプは native スクロールと競合
// するため廃止）。
"use strict";
const $ = (id) => document.getElementById(id);
const enc = new TextEncoder();
const qs = new URLSearchParams(location.search);
const pc = qs.get("pc"), sid = qs.get("sid"), dir = qs.get("dir") || "";

// 固定論理サイズ。?cols=/?rows= で上書き可（1..2000）。既定 160×500。
// cols がモデル幅以上なら横は見切れず全文到達（余りは背景空白）。
// rows ぶんの最新行を native スクロールで読める（大きいほど深く読める
// が毎フレーム cols×rows 送信で重くなる＝500 が実用バランス）。
const clampNum = (v, def) => {
  const n = parseInt(v, 10);
  return n > 0 && n <= 2000 ? n : def;
};
const WEB_COLS = clampNum(qs.get("cols"), 160);
const WEB_ROWS = clampNum(qs.get("rows"), 500);

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
// ‹/› ボタンで前後のコンソールへ location 遷移して切り替える。
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
    "&dir=" + encodeURIComponent(c.dir) +
    (qs.get("cols") ? "&cols=" + encodeURIComponent(qs.get("cols")) : "") +
    (qs.get("rows") ? "&rows=" + encodeURIComponent(qs.get("rows")) : "");
}

// コンソール切替（前後ボタン）。一覧取得失敗時は単独表示のまま無効化。
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
}

function run() {
  if (!pc || !sid) { $("stat").textContent = "pc/sid がありません"; return; }
  const label = dir ? (dir + " — " + pc) : (pc + " : " + sid);
  $("title").textContent = label;
  document.title = (dir || sid) + " — claude-master";

  setupSwitch();

  // 固定論理グリッド 160×500（scrollback:0＝xterm 自前スクロール無し。
  // proxy が viewport を絶対再描画する。背の高い要素を #term-host の
  // overflow で native スクロールする）。FitAddon は使わない（自分の
  // 寸法を測って RESIZE 逆流させると暴走するため）。
  const term = new Terminal({ cursorBlink: true, scrollback: 0,
    cols: WEB_COLS, rows: WEB_ROWS,
    fontFamily: "Menlo,Consolas,monospace", fontSize: 13 });
  term.open($("term-host"));

  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(proto + "//" + location.host + "/ws?pc=" +
    encodeURIComponent(pc) + "&sid=" + encodeURIComponent(sid));
  ws.binaryType = "arraybuffer";

  ws.onopen = () => {
    // 固定論理サイズを **接続時 1 回だけ** 送る。以後 resize/ズーム/
    // スクロール/URL バーで再送しない（暴走ループを断つ核心）。
    ws.send(resizeFrame(WEB_ROWS, WEB_COLS));
    $("stat").textContent = "接続済";
  };
  // ページ読込時は claude の **ライブ領域（カーソル行）** が見える位置
  // へ着地させる。固定 500 行グリッドより idle セッションの内容は短い
  // ので、物理最下部（=空白パディング）へ飛ばすと真っ白になる
  // （「バッファが大きすぎ」の正体）。カーソル行を viewport 下端に
  // 置けば、内容が短くても長くても常にライブ領域＋上に履歴が見える。
  // また attach 直後は 80x24 catch-up→RESIZE 後 500x160 の順で複数
  // フレームが来るため、**最初のフレームでなくバースト沈静後に 1 回**
  // 着地する（最後のフレームから 180ms 静止 or 接続 4s で確定）。
  const host = $("term-host");
  let landing = true, landTimer = 0;
  const toCursor = () => {
    if (!landing) return;
    landing = false;
    const cellH = host.scrollHeight / WEB_ROWS; // 1 行 px（全高/行数）
    let cy = 0;
    try { cy = term.buffer.active.cursorY | 0; } catch (e) { cy = 0; }
    const target = (cy + 1) * cellH - host.clientHeight;
    host.scrollTop = target > 0 ? target : 0; // ライブ行を下端へ
  };
  const scheduleLand = () => {
    if (!landing) return;
    clearTimeout(landTimer);
    landTimer = setTimeout(
      () => requestAnimationFrame(() => requestAnimationFrame(toCursor)),
      180); // 最後のフレームから静止したら確定（idle: RESIZE 後すぐ）
  };
  setTimeout(toCursor, 4000); // 連続出力で沈静しなくても 4s で 1 回着地
  // 同期更新シム（DECSET 2026）: proxy の ESC[?2026h..l フレームを 1 回の
  // term.write に束ね、xterm.js(2026 未対応)の ESC[2J チラ見せ＝チカチカを
  // 解消。ws メッセージ境界でマーカーが割れても carry で再結合（sync.js）。
  const syncFeed = cmMakeSyncFilter((b) => term.write(b, scheduleLand));
  ws.onmessage = (ev) => {
    syncFeed(new Uint8Array(ev.data));
  };
  ws.onclose = () => { $("stat").textContent = "切断"; };
  ws.onerror = () => { $("stat").textContent = "エラー"; };

  term.onData((d) => {
    if (ws.readyState === 1) ws.send(enc.encode(d));
  });

  // 画像送信: Blob を IMAGE フレーム(0xff 0xfd|u32 len|u8 ext|bytes)で
  // proxy へ。proxy がリモートホストのクリップボードへ載せ Ctrl+V 注入
  // で claude に添付（パス文字列では添付不可＝実機確定）。サーバ側
  // WebImagePaste 既定 off の時は無視される。
  const extOf = { "image/png": 1, "image/jpeg": 2, "image/gif": 3 };
  const sendImageBlob = async (blob) => {
    if (!blob) return false;
    const code = extOf[blob.type];
    if (!code) { $("stat").textContent = "未対応画像形式(" + blob.type + ")"; return false; }
    const buf = new Uint8Array(await blob.arrayBuffer());
    if (buf.length === 0 || buf.length > (8 << 20)) {
      $("stat").textContent = "画像サイズ超過/空"; return false;
    }
    const fr = new Uint8Array(7 + buf.length);
    fr[0] = 0xff; fr[1] = 0xfd;
    fr[2] = (buf.length >>> 24) & 0xff;
    fr[3] = (buf.length >>> 16) & 0xff;
    fr[4] = (buf.length >>> 8) & 0xff;
    fr[5] = buf.length & 0xff;
    fr[6] = code;
    fr.set(buf, 7);
    if (ws.readyState === 1) {
      ws.send(fr);
      $("stat").textContent = "画像を送信（リモートで Ctrl+V 注入）";
      return true;
    }
    return false;
  };

  // デスクトップ: paste(⌘V/Ctrl+V) で捕捉（モバイルは paste イベントが
  // 画像を渡さないので下のボタン経路を使う）。
  document.addEventListener("paste", async (e) => {
    const items = (e.clipboardData && e.clipboardData.items) || [];
    for (const it of items) {
      if (it.kind === "file" && it.type.indexOf("image/") === 0) {
        e.preventDefault();
        await sendImageBlob(it.getAsFile());
        return;
      }
    }
  }, true);

  // モバイル/汎用: 「📷」ボタン → まず Clipboard API(read)、不可なら
  // 写真ピッカー(<input type=file accept=image/*> capture 無し)。
  // iOS Safari/Android Chrome は paste では画像不可だがこの経路は可。
  const imgBtn = $("img"), imgFile = $("imgfile");
  if (imgBtn && imgFile) {
    imgBtn.onclick = async () => {
      try {
        if (navigator.clipboard && navigator.clipboard.read) {
          const list = await navigator.clipboard.read();
          for (const it of list) {
            const t = it.types.find((x) => x.indexOf("image/") === 0);
            if (t) { await sendImageBlob(await it.getType(t)); return; }
          }
        }
      } catch (e) { /* 権限拒否/未対応 → ピッカーへ */ }
      imgFile.click(); // 写真/カメラから選択（モバイル確実経路）
    };
    imgFile.onchange = async () => {
      const f = imgFile.files && imgFile.files[0];
      if (f) await sendImageBlob(f);
      imgFile.value = "";
    };
  }

  // 「再起動」: このセッションを restart-proxy（--resume で別プロセス
  // 復帰）。**全セッションに表示**。pid- は backend が claude の jsonl
  // から会話 UUID を自動解決して復帰（解決不可は履歴にエラー＝kill
  // せず保全）。既存 owner 限定 POST /api/command を再利用（無改変）。
  const rstB = $("restart");
  if (rstB) {
    rstB.style.display = "";
    rstB.onclick = async () => {
      if (!confirm((dir || sid) + "\n現在の claude を終了し --resume で別" +
        "プロセスとして復帰します。\nこの画面/元の端末には自動では戻り" +
        "ません（復帰後あらためて開いてください）。\nよろしいですか？")) return;
      rstB.disabled = true;
      try {
        const body = new URLSearchParams({ pc, cmd: "restart-proxy", sid });
        const r = await fetch("/api/command", {
          method: "POST", headers: { Accept: "application/json",
            "Content-Type": "application/x-www-form-urlencoded" },
          body: body.toString(),
        });
        if (r.status === 401) { location.href = "/login"; return; }
        if (!r.ok) throw new Error("投入失敗 " + r.status);
        $("stat").textContent = "restart-proxy 投入（復帰は別プロセス・履歴で監査）";
      } catch (e) {
        if (e.message !== "unauth") alert("エラー: " + e.message);
      } finally { rstB.disabled = false; }
    };
  }

  // window resize / ズーム / スクロール / URL バーでは **何もしない**
  // （意図的にハンドラ無し＝RESIZE 逆流の暴走を構造的に防止）。
}
run();
