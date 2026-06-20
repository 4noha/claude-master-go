package web

// 3 ページ構成:
//   /login    … pairing code 入力
//   /         … アカウントに接続された端末一覧（ランディング）。
//               各セッションから Web ターミナルへリンク。
//   /term     … Web ターミナル本体（xterm.js）。/ からリンクして開く。
// 静的アセットは internal/cloud/web/static を go:embed で /static/ 配信。

// loginHTMLTmpl は Google Sign-In（GIS）。%s に OAuth Web Client ID。
// GIS が credential(IDトークン)＋g_csrf_token を /auth/google へ POST。
const loginHTMLTmpl = `<!doctype html><html lang="ja"><meta charset="utf-8">
<title>claude-master — ログイン</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<body style="font-family:system-ui;max-width:420px;margin:16vh auto;padding:0 16px;text-align:center">
<h1 style="font-size:20px">claude-master</h1>
<p style="color:#666;font-size:14px">Google アカウントでログインしてください。</p>
<script src="https://accounts.google.com/gsi/client" async></script>
<div id="g_id_onload"
     data-client_id="%s"
     data-login_uri="/auth/google"
     data-ux_mode="redirect"></div>
<div class="g_id_signin" data-type="standard" data-size="large"
     data-text="signin_with" data-shape="pill"
     style="display:inline-block;margin-top:16px"></div>
<noscript>JavaScript を有効にしてください。</noscript>
</body></html>`

// devicesHTML: アカウントに接続されている端末一覧＋Web ターミナルへの
// リンク。Webインターフェース（/term）はこのページから開く。
const devicesHTML = `<!doctype html><html lang="ja"><meta charset="utf-8">
<title>claude-master — 端末一覧</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
 body{font-family:system-ui;margin:0;background:#0d0d0f;color:#e6e6e6}
 header{padding:12px 18px;background:#17171b;display:flex;gap:14px;
  align-items:center}
 header b{font-size:16px}
 main{max-width:860px;margin:24px auto;padding:0 16px}
 .dev{background:#17171b;border:1px solid #2a2a30;border-radius:10px;
  padding:14px 16px;margin:14px 0}
 .dev h2{font-size:15px;margin:0 0 4px}
 .devhead{display:flex;align-items:center;justify-content:space-between;
  gap:10px}
 .del{background:#3a1f22;color:#f3b4b4;border:1px solid #5a2a2e;
  border-radius:6px;padding:4px 10px;font-size:12px;cursor:pointer}
 .del:disabled{opacity:.4;cursor:default}
 .meta{color:#9aa;font-size:12px;margin-bottom:10px}
 .s{display:flex;justify-content:space-between;align-items:center;
  padding:8px 10px;border-top:1px solid #24242a}
 .s a{display:inline-block;padding:6px 12px;background:#2563eb;color:#fff;
  border-radius:6px;text-decoration:none;font-size:13px}
 .dot{color:#22c55e}
 .ver{font-size:11px;color:#9aa;margin-left:6px}
 .vok{color:#22c55e}
 .vbad{color:#ef4444}
 .diag-btn{background:#222;color:#9cf;border:1px solid #345;border-radius:6px;
  padding:4px 8px;font-size:12px;cursor:pointer;margin-right:8px}
 .diag{background:#101014;border:1px solid #24242a;border-radius:6px;
  margin:0 10px 8px;padding:8px 10px;color:#bcd;font-size:11px;
  white-space:pre-wrap;overflow-x:auto}
 a.logout{margin-left:auto;color:#7ab;font-size:13px}
 #stat{color:#9aa;font-size:13px}
</style>
<body>
<header><b>claude-master</b><span id="stat">読み込み中…</span>
 <a class="logout" href="/auth/logout">ログアウト</a></header>
<main>
 <p style="color:#9aa;font-size:13px">アカウントに接続されている端末です。
  セッションの「開く」から Web インターフェースに接続します。</p>
 <button id="addbtn" style="padding:8px 14px;font:14px system-ui;
  background:#2563eb;color:#fff;border:0;border-radius:6px;cursor:pointer">
  ＋ 端末を追加</button>
 <pre id="enroll" style="display:none;white-space:pre-wrap;background:#17171b;
  border:1px solid #2a2a30;border-radius:8px;padding:12px;margin-top:12px;
  color:#cde;font-size:12px"></pre>
 <div id="devices" style="margin-top:8px"></div>
</main>
<script src="/static/devices.js"></script>
</body></html>`

// termHTML: Web ターミナル本体（/term?pc=&sid=）。/ からリンクで開く。
const termHTML = `<!doctype html><html lang="ja"><meta charset="utf-8">
<title>claude-master — ターミナル</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<link rel="stylesheet" href="/static/xterm.css">
<style>
 /* Web は固定論理グリッド 160×500。背の高い端末を #term-host の
    overflow で **ブラウザ native スクロール**して読む（縦/横/ピンチ
    ズーム＝ブラウザ任せ。proxy へスクロール位置は一切返さない）。
    pull-to-refresh は document を固定＋overscroll-behavior で殺し、
    #term-host は overscroll-behavior:contain でスクロール連鎖も断つ
    （ので native スクロールしてもリロードしない）。 */
 html,body{margin:0;height:100%;background:#0b0b0b;color:#ddd;
  font-family:system-ui;overscroll-behavior:none;overflow:hidden}
 body{position:fixed;inset:0}
 #term,#term-host{touch-action:pan-x pan-y pinch-zoom}
 #bar{padding:6px 12px;background:#161616;display:flex;gap:10px;
  align-items:center;font-size:13px}
 #bar a{color:#7ab;text-decoration:none}
 #ctrls{margin-left:auto;display:flex;gap:10px;align-items:center}
 #title{font-weight:600;color:#eee}
 #pos{color:#9aa;font-size:12px}
 .nav{background:#2a2a30;color:#cde;border:0;border-radius:6px;
  padding:4px 10px;font-size:14px;cursor:pointer}
 .nav:disabled{opacity:.35;cursor:default}
 .intr{background:#3a1f22;color:#f3b4b4;border:1px solid #5a2a2e}
 #term{position:absolute;top:34px;left:0;right:0;bottom:0}
 #term-host{width:100%;height:100%;overflow:auto;
  overscroll-behavior:contain}
 /* フローティング操作パッド（ESC＋十字キー）。position:fixed＝native
    スクロールに追従（画面固定）。grip でドラッグ移動・位置は記憶。
    touch-action:none でパッド操作中にページがスクロール/ズームしない。 */
 #cmpad{position:fixed;right:12px;bottom:16px;z-index:50;
  display:flex;flex-direction:column;align-items:center;gap:4px;
  padding:6px;background:rgba(22,22,26,.78);border:1px solid #2a2a30;
  border-radius:12px;touch-action:none;user-select:none;
  -webkit-user-select:none;backdrop-filter:blur(2px)}
 #cmpad .cmrow{display:flex;gap:4px}
 #cmpad button{width:44px;height:40px;font-size:18px;color:#cde;
  background:#2a2a30;border:1px solid #3a3a42;border-radius:8px;
  cursor:pointer;touch-action:none;-webkit-tap-highlight-color:transparent}
 #cmpad button:active{background:#3a3a44}
 #cmpad button.esc{font-size:14px;color:#f3b4b4;background:#3a1f22;
  border-color:#5a2a2e}
 #cmpad-grip{width:100%;text-align:center;color:#777;font-size:12px;
  line-height:12px;cursor:move;padding:2px 0}
</style>
<body>
<div id="bar">
 <a href="/">← 一覧</a>
 <span id="title"></span><span id="pos"></span>
 <span id="ctrls">
  <button class="nav" id="prev" title="前のコンソール">‹</button>
  <button class="nav" id="next" title="次のコンソール">›</button>
  <button class="nav intr" id="intr" title="中断 (Esc を送信＝claude の生成を止める)">Esc</button>
  <button class="nav" id="img" title="画像を貼る/選ぶ（モバイル可）">📷</button>
  <button class="nav" id="restart" title="このセッションを復帰(--resume で別プロセス再起動)" style="display:none">復帰</button>
  <input type="file" accept="image/*" id="imgfile" style="display:none">
  <span id="stat"></span>
 </span>
</div>
<div id="term"><div id="term-host"></div></div>
<div id="cmpad" aria-label="操作パッド（ESC・十字キー）">
 <div id="cmpad-grip" title="ドラッグで移動">⠿⠿⠿</div>
 <div class="cmrow"><button data-k="up" aria-label="上">↑</button></div>
 <div class="cmrow">
  <button data-k="left" aria-label="左">←</button>
  <button class="esc" data-k="esc" aria-label="Esc（中断）">Esc</button>
  <button data-k="right" aria-label="右">→</button>
 </div>
 <div class="cmrow"><button data-k="down" aria-label="下">↓</button></div>
</div>
<script src="/static/xterm.js"></script>
<script src="/static/sync.js"></script>
<script src="/static/term.js"></script>
</body></html>`
