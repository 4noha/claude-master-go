package web

// loginHTML はコード入力。appHTML は xterm.js 端末 SPA（M7c）。
// アセットは internal/cloud/web/static を go:embed で /static/ 配信。

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

const appHTML = `<!doctype html><html lang="ja"><meta charset="utf-8">
<title>claude-master</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<link rel="stylesheet" href="/static/xterm.css">
<style>
 html,body{margin:0;height:100%;background:#0b0b0b;color:#ddd;
  font-family:system-ui}
 #bar{padding:6px 12px;background:#161616;display:flex;gap:12px;
  align-items:center;font-size:13px}
 #list{padding:16px}#list button{font:14px system-ui;padding:8px 12px;
  margin:4px 0;cursor:pointer}
 #term{display:none;position:absolute;top:34px;left:0;right:0;bottom:0}
 #term-host{width:100%;height:100%}
 a{color:#7ab}
</style>
<body>
<div id="bar">
 <b>claude-master</b><span id="title"></span>
 <span id="stat" style="margin-left:auto"></span>
 <a href="/auth/logout">logout</a>
</div>
<ul id="list">読み込み中…</ul>
<div id="term"><div id="term-host"></div></div>
<script src="/static/xterm.js"></script>
<script src="/static/addon-fit.js"></script>
<script src="/static/app.js"></script>
</body></html>`
