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

// cmTrackScroll: ライブ行（カーソル）を **ブラウザ native スクロール**で
// 追従し続ける純粋計算。proxy は bottom-fill 撤去後 *上詰めの短い*
// フレーム（内容 < 500 行・パディング無＝Python parity）を毎フレーム
// 送り、claude のストリーム/全画面再描画で内容長＝カーソル行が変動
// する。クライアントが一度しかスクロールを合わせないと:
//  ・カーソル行が固定スクロールに対し上下する＝**入力位置がズレる**
//  ・短い内容の下＝固定 500 行グリッドの空き＝**下に黒いエリア**
// ScrollRenderer の follow/scrollback 不変条件を native スクロールへ
// 適用して解消する: following 中は沈静バースト毎にカーソル行を可視
// 下端へ再ピン（位置が常に一定＝ズレない・空きを見ない）／ユーザが
// 履歴へ遡り（カーソルが可視域より下に隠れる）と following 解除＝
// スクロールを一切動かさず読書を妨げない／最下部へ戻すと再 following。
// 純粋＝node で決定論テスト（合成でなく実 static を抽出）。
// s.mode: "settle"=沈静後の再ピン / "userscroll"=ユーザ操作後の follow
//         再判定。返り値 {scrollTop, following} を呼び元が適用する。
function cmTrackScroll(s) {
  const cellH = s.cellH > 0 ? s.cellH : 1;
  const maxTop = Math.max(0, s.scrollHeight - s.clientHeight);
  const clamp = (t) => (t < 0 ? 0 : t > maxTop ? maxTop : t);
  const cursorPx = (s.cy + 1) * cellH;             // ライブ行の下端 px
  const pinTop = clamp(cursorPx - s.clientHeight); // カーソル行を可視下端へ
  if (s.mode === "userscroll") {
    // ユーザ操作の結果位置から follow を再判定（スクロールは動かさ
    // ない）。可視下端がカーソル行に届いていれば=ライブ追跡中(緑)、
    // カーソルが可視域より 1 行以上下へ隠れる程遡ったら=履歴閲覧中
    // →follow 解除。プログラム的ピン直後の scroll でも geometry は
    // viewBottom≈cursorPx となり following=true で不変＝guard 不要。
    const viewBottom = s.scrollTop + s.clientHeight;
    return { scrollTop: s.scrollTop, following: viewBottom >= cursorPx - cellH };
  }
  // mode "settle": following 中だけカーソルを下端へ再ピン。遡り中
  // (following=false)はユーザのスクロール位置を一切動かさない。
  if (s.following) return { scrollTop: pinTop, following: true };
  return { scrollTop: s.scrollTop, following: false };
}

// cmReconnectGate: 再接続して良いかの純粋判定（node で決定論テスト）。
// データ線は無通信 30s で quiescence 切断される設計（near-$0）なので、
// **閉じたら張りっぱなしに戻さない**。張り直すのは契機（Firestore push
// ＝セッション status 変化／ユーザ入力）があった時だけ。その上で:
//  ・CONNECTING(0)/OPEN(1) 中は張らない（多重接続は relay takeover の
//    無駄打ち）
//  ・失敗連打を backoff (1s×2^attempts・上限 30s) で抑える
// s: {wsState:-1|0..3, now, lastAttemptAt, attempts}
// 返り値: {connect, attempts}（connect 時は attempts+1。成功(onopen)で
// 呼び元が attempts=0 に戻す）
function cmReconnectGate(s) {
  if (s.wsState === 0 || s.wsState === 1) {
    return { connect: false, attempts: s.attempts };
  }
  const backoff = Math.min(30000, 1000 * Math.pow(2, s.attempts));
  if (s.now - s.lastAttemptAt < backoff) {
    return { connect: false, attempts: s.attempts };
  }
  return { connect: true, attempts: s.attempts + 1 };
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

  // ---- データ線（/ws）。quiescence（無通信 30s）でサーバ側から閉じる
  // 設計なので、閉鎖は異常ではない。再接続は cmReconnectGate の規律で
  // 「契機があった時だけ」: ①Firestore push（status doc 変化＝
  // セッションが動いた） ②ユーザ入力（キー/画像）。アイドル中は接続
  // ゼロ＝Cloud Run も温まらない（near-$0 維持のままネイティブの
  // WatchSessions 同等の push 復帰）。
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  let ws = null;
  let attempts = 0, lastAttemptAt = 0;
  let inputQ = [], inputQBytes = 0; // 未接続中の入力（接続後に送出）
  const QMAX = 9 << 20; // 画像 8MB + キー入力余裕

  const wsState = () => (ws ? ws.readyState : -1);

  const flushQ = () => {
    while (inputQ.length && ws && ws.readyState === 1) {
      const b = inputQ.shift();
      inputQBytes -= b.length;
      ws.send(b);
    }
  };

  const connect = () => {
    const sock = new WebSocket(proto + "//" + location.host + "/ws?pc=" +
      encodeURIComponent(pc) + "&sid=" + encodeURIComponent(sid));
    ws = sock;
    sock.binaryType = "arraybuffer";
    // 同期シムは **接続ごとに新規**。旧接続の取りかけ frame (acc) を
    // 新接続のバイト列と継ぎ合わせない（frame 境界の保全）。さらに
    // 全ハンドラを「自分が現役 (ws === sock) の時だけ」動かす＝古い
    // 接続の遅延イベントが新しい接続へ干渉しない（relay 側 takeover
    // 修正と同じ規律のブラウザ版）。
    const feed = cmMakeSyncFilter((b) => term.write(b, scheduleLand));
    sock.onopen = () => {
      if (ws !== sock) return;
      attempts = 0; // 成功で backoff リセット
      // 固定論理サイズを **接続のたび 1 回だけ** 送る（完全 frame での
      // catch-up を兼ねる）。以後 resize/ズーム/スクロール/URL バーで
      // 再送しない（暴走ループを断つ核心）。
      sock.send(resizeFrame(WEB_ROWS, WEB_COLS));
      flushQ();
      $("stat").textContent = "接続済";
    };
    sock.onmessage = (ev) => {
      if (ws !== sock) return;
      feed(new Uint8Array(ev.data));
    };
    sock.onclose = () => {
      if (ws !== sock) return;
      $("stat").textContent = "待機（更新/入力で自動再接続）";
      // 未送出の入力が残っていれば backoff 後に自走再試行（入力を
      // 失わない）。それ以外は push/次の入力まで張らない。
      if (inputQ.length) setTimeout(() => requestConnect("input"), 1100);
    };
    sock.onerror = () => {
      if (ws !== sock) return;
      $("stat").textContent = "エラー（自動再接続待機）";
    };
  };

  const requestConnect = (reason) => {
    const r = cmReconnectGate({
      wsState: wsState(), now: Date.now(),
      lastAttemptAt, attempts,
    });
    if (!r.connect) return;
    attempts = r.attempts;
    lastAttemptAt = Date.now();
    $("stat").textContent = "再接続中…（" + reason + "）";
    connect();
  };

  // 送信（キー入力/画像）。未接続なら queue して接続を要求＝タイプ
  // すれば線が再び開く。
  const sendBytes = (b) => {
    if (ws && ws.readyState === 1) {
      ws.send(b);
      return true;
    }
    if (inputQBytes + b.length > QMAX) {
      $("stat").textContent = "未接続のため送信破棄（再接続待ち）";
      return false;
    }
    inputQ.push(b);
    inputQBytes += b.length;
    requestConnect("input");
    return true;
  };
  // ライブ行（カーソル行）を native スクロールで **追従し続ける**。
  // 固定 500 行グリッドより idle セッションの内容は短く、proxy は
  // 上詰めの短いフレームを毎フレーム送る（内容長＝カーソル行が変動）。
  // 一度しか着地しないと固定スクロールに対しカーソルが流れ「入力位置
  // ズレ」、内容下の空きグリッドが「黒いエリア」になる。沈静バースト
  // 毎に cmTrackScroll で再ピンし（位置一定＝ズレない・空きを見ない）、
  // ユーザが履歴へ遡ったら following 解除して勝手に動かさない。
  // attach 直後は 80x24 catch-up→RESIZE 後 500x160 と複数フレームが
  // 来るため、最初のフレームでなく最後から 180ms 静止で沈静確定。
  const host = $("term-host");
  let following = true, landTimer = 0;
  const cursorRow = () => {
    try {
      const a = term.buffer.active;
      return ((a.baseY | 0) + (a.cursorY | 0)) | 0; // scrollback:0 で baseY=0
    } catch (e) { return 0; }
  };
  const stateNow = (mode) => ({
    mode, following, cy: cursorRow(),
    cellH: host.scrollHeight / WEB_ROWS, // 1 行 px（全高/行数。固定）
    clientHeight: host.clientHeight,
    scrollHeight: host.scrollHeight,
    scrollTop: host.scrollTop,
  });
  // 沈静バースト後の再ピン（following 中のみスクロールを動かす）。
  const settle = () => {
    const r = cmTrackScroll(stateNow("settle"));
    following = r.following;
    if (r.scrollTop !== host.scrollTop) host.scrollTop = r.scrollTop;
  };
  const scheduleLand = () => {
    clearTimeout(landTimer);
    landTimer = setTimeout(
      () => requestAnimationFrame(() => requestAnimationFrame(settle)),
      180); // 最後のフレームから静止したら沈静確定（idle: RESIZE 後すぐ）
  };
  setTimeout(settle, 4000); // 連続出力で沈静しなくても 4s で 1 回着地
  // ユーザの native スクロールから follow 状態だけ再判定（スクロール
  // 位置は動かさない）。内容成長は scrollTop を変えず scroll を発火
  // しない＝このハンドラはピン代入かユーザ操作のみ＝geometry で安全。
  host.addEventListener("scroll", () => {
    const r = cmTrackScroll(stateNow("userscroll"));
    following = r.following;
  }, { passive: true });
  // 初回接続（同期シム cmMakeSyncFilter は connect() 内で接続ごとに
  // 新規生成: ws メッセージ境界でマーカーが割れても carry で再結合し、
  // 旧接続の取りかけ frame と新接続を継ぎ合わせない）。
  lastAttemptAt = Date.now();
  connect();

  // Firestore 更新 push: ネイティブの WatchSessions（snapshot listener）
  // と同型。relay が cookie 認証済みオーナーへ発行する custom token
  // （uid=cm-owner・全端末共通・rules で pcs/** read-only）で Firebase
  // に直結し、このセッションの status doc 変化＝「セッションが動いた」
  // を push で受けて切断中なら自動再接続する。未設定/失敗時は従来
  // どおり（push 無し・手動リロード）に degrade。
  (async () => {
    try {
      const r = await fetch("/api/fbtoken", { headers: { Accept: "application/json" } });
      if (!r.ok) return; // 未設定（404）等 → push なしで従来動作
      const { token, config } = await r.json();
      const base = "https://www.gstatic.com/firebasejs/11.6.1/";
      const [appM, authM, fsM] = await Promise.all([
        import(base + "firebase-app.js"),
        import(base + "firebase-auth.js"),
        import(base + "firebase-firestore.js"),
      ]);
      const app = appM.initializeApp(config);
      await authM.signInWithCustomToken(authM.getAuth(app), token);
      const db = fsM.getFirestore(app);
      fsM.onSnapshot(fsM.doc(db, "pcs", pc, "sessions", sid), () => {
        requestConnect("更新push");
      });
    } catch (e) { /* push なしでも従来どおり動く */ }
  })();

  term.onData((d) => { sendBytes(enc.encode(d)); });

  // 「Esc」中断ボタン: Esc のバイト(0x1b=ESC)を入力経路へ送る。claude の
  // 「esc to interrupt」＝生成中断に対応。モバイルには Esc キーが無く、
  // ブラウザでも打ちづらいので専用ボタンで補う（サーバ無改変＝term.onData
  // と同じ sendBytes 経路）。送信後はターミナルにフォーカスを戻す。
  const intrB = $("intr");
  if (intrB) {
    intrB.onclick = () => {
      sendBytes(new Uint8Array([0x1b]));
      try { term.focus(); } catch (e) { /* focus 無くても送信は成立 */ }
    };
  }

  // フローティング操作パッド（ESC＋十字キー）。モバイル等で物理キーが
  // 無くても矢印/ESC を送れる。各ボタンは sendBytes へ（term.onData と同
  // 入力経路・サーバ無改変）。矢印は CSI 標準シーケンス（ESC[A/B/C/D。
  // claude=Ink は CSI/SS3 両対応）。position:fixed なので native スクロール
  // に追従し、grip ドラッグで移動・位置は localStorage に記憶する。
  const PAD_KEYS = {
    esc: [0x1b],
    up: [0x1b, 0x5b, 0x41], // ESC [ A
    down: [0x1b, 0x5b, 0x42], // ESC [ B
    right: [0x1b, 0x5b, 0x43], // ESC [ C
    left: [0x1b, 0x5b, 0x44], // ESC [ D
  };
  const pad = $("cmpad"), grip = $("cmpad-grip");
  if (pad) {
    pad.querySelectorAll("button[data-k]").forEach((b) => {
      b.addEventListener("click", () => {
        const seq = PAD_KEYS[b.getAttribute("data-k")];
        if (seq) {
          sendBytes(new Uint8Array(seq));
          try { term.focus(); } catch (e) { /* 送信は成立 */ }
        }
      });
    });
    // 位置の復元（保存済みなら left/top 絶対指定へ切替）。viewport 外に
    // ならないよう clamp（端末回転/リサイズで枠外固定を防ぐ）。
    const clampPad = (x, y) => [
      Math.max(0, Math.min(x, window.innerWidth - pad.offsetWidth)),
      Math.max(0, Math.min(y, window.innerHeight - pad.offsetHeight)),
    ];
    const placePad = (x, y) => {
      const [cx, cy] = clampPad(x, y);
      pad.style.left = cx + "px"; pad.style.top = cy + "px";
      pad.style.right = "auto"; pad.style.bottom = "auto";
    };
    try {
      const p = JSON.parse(localStorage.getItem("cm-pad-pos") || "null");
      if (p && typeof p.left === "number") placePad(p.left, p.top);
    } catch (e) { /* 既定の右下のまま */ }
    // grip ドラッグで移動（pointer events＝マウス/タッチ統一）。ボタンは
    // grip と別要素なのでタップ/ドラッグの取り違えは起きない。
    if (grip) {
      let dragging = false, sx = 0, sy = 0, ox = 0, oy = 0;
      grip.addEventListener("pointerdown", (e) => {
        dragging = true;
        try { grip.setPointerCapture(e.pointerId); } catch (er) { /* 無くても可 */ }
        const r = pad.getBoundingClientRect();
        ox = r.left; oy = r.top; sx = e.clientX; sy = e.clientY;
        placePad(ox, oy); // right/bottom 指定→left/top 指定へ確定
        e.preventDefault();
      });
      grip.addEventListener("pointermove", (e) => {
        if (!dragging) return;
        placePad(ox + (e.clientX - sx), oy + (e.clientY - sy));
      });
      const endDrag = () => {
        if (!dragging) return;
        dragging = false;
        try {
          localStorage.setItem("cm-pad-pos", JSON.stringify({
            left: parseInt(pad.style.left, 10),
            top: parseInt(pad.style.top, 10),
          }));
        } catch (e) { /* 記憶できなくても動作は継続 */ }
      };
      grip.addEventListener("pointerup", endDrag);
      grip.addEventListener("pointercancel", endDrag);
    }
  }

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
    if (sendBytes(fr)) {
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
