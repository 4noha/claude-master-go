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
 .meta{color:#9aa;font-size:12px;margin-bottom:10px}
 .s{display:flex;justify-content:space-between;align-items:center;
  padding:8px 10px;border-top:1px solid #24242a}
 .s a{display:inline-block;padding:6px 12px;background:#2563eb;color:#fff;
  border-radius:6px;text-decoration:none;font-size:13px}
 .dot{color:#22c55e}
 a.logout{margin-left:auto;color:#7ab;font-size:13px}
 #stat{color:#9aa;font-size:13px}
</style>
<body>
<header><b>claude-master</b><span id="stat">読み込み中…</span>
 <a class="logout" href="/auth/logout">ログアウト</a></header>
<main>
 <p style="color:#9aa;font-size:13px">アカウントに接続されている端末です。
  セッションの「Web ターミナルを開く」から Web インターフェースに接続します。</p>
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
 /* pull-to-refresh / overscroll を CSS で無効化（JS preventDefault は
    方向確定前に間に合わずリロードが走るため、タイミング非依存で殺す）。
    body 固定＋overscroll-behavior:none＋1本指 preventDefault で
    リロードは死んだまま。一方 Web は端末を固定広幅でレンダーするので
    画面より広い。touch-action:pinch-zoom で **ブラウザのピンチズーム
    だけ許可**（1本指は従来どおり JS のスクロール/切替）。#term-host を
    overflow:auto にし、はみ出す広い端末を横パンで閲覧可能にする。 */
 html,body{margin:0;height:100%;background:#0b0b0b;color:#ddd;
  font-family:system-ui;overscroll-behavior:none;overflow:hidden;
  -webkit-overflow-scrolling:auto}
 body{position:fixed;inset:0}
 #term,#term *{touch-action:pinch-zoom}
 #bar{padding:6px 12px;background:#161616;display:flex;gap:10px;
  align-items:center;font-size:13px}
 #bar a{color:#7ab;text-decoration:none}
 #title{font-weight:600;color:#eee}
 #pos{color:#9aa;font-size:12px}
 .nav{background:#2a2a30;color:#cde;border:0;border-radius:6px;
  padding:4px 10px;font-size:14px;cursor:pointer}
 .nav:disabled{opacity:.35;cursor:default}
 #term{position:absolute;top:34px;left:0;right:0;bottom:0}
 #term-host{width:100%;height:100%;overflow:auto}
</style>
<body>
<div id="bar">
 <a href="/">← 一覧</a>
 <button class="nav" id="prev" title="前のコンソール">‹</button>
 <span id="title"></span><span id="pos"></span>
 <button class="nav" id="next" title="次のコンソール">›</button>
 <span id="stat" style="margin-left:auto"></span>
</div>
<div id="term"><div id="term-host"></div></div>
<script src="/static/xterm.js"></script>
<script src="/static/addon-fit.js"></script>
<script src="/static/term.js"></script>
</body></html>`
