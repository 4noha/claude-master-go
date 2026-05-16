package web

// 3 ページ構成:
//   /login    … pairing code 入力
//   /         … アカウントに接続された端末一覧（ランディング）。
//               各セッションから Web ターミナルへリンク。
//   /term     … Web ターミナル本体（xterm.js）。/ からリンクして開く。
// 静的アセットは internal/cloud/web/static を go:embed で /static/ 配信。

const loginHTML = `<!doctype html><html lang="ja"><meta charset="utf-8">
<title>claude-master</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<body style="font-family:system-ui;max-width:420px;margin:12vh auto;padding:0 16px">
<h1 style="font-size:20px">claude-master</h1>
<p>PC 側で <code>claude-master cloud pair</code> を実行し、表示された
コードを入力してください（一回限り・短時間有効）。</p>
<form method="post" action="/auth/code">
  <input name="code" autofocus autocomplete="off" placeholder="PAIRING CODE"
   style="font-size:18px;letter-spacing:2px;padding:10px;width:100%;box-sizing:border-box;text-transform:uppercase">
  <button style="margin-top:12px;padding:10px 16px;font-size:16px">接続</button>
</form>
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
 <div id="devices"></div>
</main>
<script src="/static/devices.js"></script>
</body></html>`

// termHTML: Web ターミナル本体（/term?pc=&sid=）。/ からリンクで開く。
const termHTML = `<!doctype html><html lang="ja"><meta charset="utf-8">
<title>claude-master — ターミナル</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<link rel="stylesheet" href="/static/xterm.css">
<style>
 html,body{margin:0;height:100%;background:#0b0b0b;color:#ddd;
  font-family:system-ui}
 #bar{padding:6px 12px;background:#161616;display:flex;gap:12px;
  align-items:center;font-size:13px}
 #bar a{color:#7ab;text-decoration:none}
 #term{position:absolute;top:34px;left:0;right:0;bottom:0}
 #term-host{width:100%;height:100%}
</style>
<body>
<div id="bar">
 <a href="/">← 端末一覧</a><span id="title"></span>
 <span id="stat" style="margin-left:auto"></span>
</div>
<div id="term"><div id="term-host"></div></div>
<script src="/static/xterm.js"></script>
<script src="/static/addon-fit.js"></script>
<script src="/static/term.js"></script>
</body></html>`
