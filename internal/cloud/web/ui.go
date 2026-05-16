package web

// M7b は最小 HTML（動作確認用）。M7c で xterm.js SPA に差し替える。

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
<body style="font-family:system-ui;margin:24px">
<h1 style="font-size:18px">claude-master 管理</h1>
<p id="s">読み込み中…</p>
<ul id="list"></ul>
<form method="post" action="/auth/logout"><button>ログアウト</button></form>
<script>
fetch('/api/pcs').then(r=>r.json()).then(pcs=>{
  const pc=(pcs[0]||{}).id; document.getElementById('s').textContent='PC: '+pc;
  return fetch('/api/sessions?pc='+encodeURIComponent(pc)).then(r=>r.json());
}).then(ss=>{
  const ul=document.getElementById('list');
  (ss||[]).forEach(x=>{const li=document.createElement('li');
   li.textContent=(x.short_dir||x.key)+'  ['+(x.key||'')+']';ul.appendChild(li);});
  if(!ss||!ss.length){document.getElementById('s').textContent+='（セッション無し）';}
}).catch(e=>{document.getElementById('s').textContent='エラー: '+e;});
</script>
</body></html>`
