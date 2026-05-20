// アカウントに接続されている端末一覧。各セッションから Web ターミナル
// (/term) へリンク。cookie 認証前提（未認証は /api が 401→/login 誘導）。
"use strict";
const $ = (id) => document.getElementById(id);

async function jget(u) {
  const r = await fetch(u, { headers: { Accept: "application/json" } });
  if (r.status === 401) { location.href = "/login"; throw new Error("unauth"); }
  if (!r.ok) throw new Error(u + " -> " + r.status);
  return r.json();
}

function el(tag, props, txt) {
  const e = document.createElement(tag);
  if (props) Object.assign(e, props);
  if (txt != null) e.textContent = txt;
  return e;
}

// 目標版（最新 Release tag）。空＝判定不能→中立表示（誤って全 🔴 に
// しない）。
let TARGET = "";
// relNum: 先頭の X.Y.Z（リリース番号）のみ抽出。git describe のビルド
// 接尾辞 `-<N>-g<hash>` / `-dirty` や `dev` は無視＝**バージョン番号のみで
// 判定**（タグから N コミット先のビルド差で誤警告しない）。
const relNum = (v) => { const m = String(v || "").match(/^v?(\d+\.\d+\.\d+)/); return m ? m[1] : ""; };
// cmpRel: X.Y.Z を数値 semver 比較（-1:a<b / 0:= / 1:a>b）。
const cmpRel = (a, b) => {
  const pa = a.split(".").map(Number), pb = b.split(".").map(Number);
  for (let i = 0; i < 3; i++) { if (pa[i] !== pb[i]) return pa[i] < pb[i] ? -1 : 1; }
  return 0;
};
// 更新あり判定: 実行版が目標版より **古い時のみ** true（赤）。
// ・ビルド接尾辞 -<N>-g<hash>/-dirty は relNum で無視＝番号のみ判定
// ・先行（開発版が最新 Release より進む）/ 同一 は許容＝警告なし（緑）
// ・番号が取れない側（dev・不明）は判定不能＝false（誤って赤にしない）
function updateAvailable(v) {
  const a = relNum(v), b = relNum(TARGET);
  return a !== "" && b !== "" && cmpRel(a, b) < 0;
}
// 状態 ●: 更新あり→赤●（idle でも表示＝要更新機を可視）／でなければ
// 稼働中→緑●（今までどおり）／idle かつ最新→非表示（従来どおり）。
// 緑は既存 .dot(#22c55e)、赤は既存 .vbad(#ef4444。.dot より後定義で優先)。
function statusDot(active, v) {
  if (updateAvailable(v)) {
    return el("span", { className: "dot vbad",
      title: v + " → 要更新 " + TARGET }, " ●");
  }
  if (active) {
    return el("span", { className: "dot",
      title: "稼働中" + (v ? "（" + v + "）" : "") }, " ●");
  }
  return null; // idle かつ最新＝従来どおり ● 無し
}

// 診断: その行の生フィールド（window_name=タイトル等）を開閉表示。
function diagPre(x) {
  const keys = ["pid", "session_id", "key", "cm_version", "window_name",
    "short_dir", "cwd", "start_time", "is_active", "usage_percent",
    "reset_time", "updated_at"];
  const o = {};
  for (const k of keys) if (x[k] !== undefined) o[k] = x[k];
  o._target = TARGET || "(取得不可)";
  return el("pre", { className: "diag" }, JSON.stringify(o, null, 2));
}

// 遠隔命令投入（owner のみ・POST）。実行前 confirm は呼び元で。
async function postCmd(pc, cmd, sid) {
  const body = new URLSearchParams({ pc, cmd, sid: sid || "" });
  const r = await fetch("/api/command", {
    method: "POST", headers: { Accept: "application/json",
      "Content-Type": "application/x-www-form-urlencoded" },
    body: body.toString(),
  });
  if (r.status === 401) { location.href = "/login"; throw new Error("unauth"); }
  if (!r.ok) throw new Error("投入失敗 " + r.status);
  return r.json();
}

// 命令監査（新しい順）を開閉表示。
async function cmdAudit(pc) {
  const cs = await jget("/api/commands?pc=" + encodeURIComponent(pc));
  const box = el("div", { className: "diag" });
  if (!cs.length) { box.textContent = "（命令履歴なし）"; return box; }
  for (const c of cs) {
    box.appendChild(el("div", null,
      (c.ts || "") + "  " + c.cmd + (c.sid ? "(" + c.sid + ")" : "") +
      "  [" + c.status + "] " + (c.detail || "") +
      "  by " + (c.requested_by || "?")));
  }
  return box;
}

async function main() {
  try {
    try { TARGET = (await jget("/api/version")).target || ""; } catch (e) { TARGET = ""; }
    const devs = await jget("/api/devices");
    if (!devs.length) { $("stat").textContent = "端末がありません"; return; }
    $("stat").textContent = devs.length + " 台接続";
    const root = $("devices");
    for (const d of devs) {
      const card = el("div", { className: "dev" });
      const head = el("div", { className: "devhead" });
      const h2 = el("h2", null, d.id);
      // PC(agent): 稼働前提なので active=true 扱い→最新は緑●・更新あり赤●
      { const sd = statusDot(true, d.cm_version); if (sd) h2.appendChild(sd); }
      head.appendChild(h2);
      const ops = el("span");
      const mkOp = (label, cmd, msg) => {
        const b = el("button", { className: "diag-btn" }, label);
        b.onclick = async () => {
          if (!confirm(d.id + ": " + msg + "\nよろしいですか？")) return;
          b.disabled = true;
          try {
            await postCmd(d.id, cmd, "");
            $("stat").textContent = d.id + " へ " + cmd + " を投入（監査は履歴で）";
          } catch (e) {
            if (e.message !== "unauth") alert("エラー: " + e.message);
          } finally { b.disabled = false; }
        };
        return b;
      };
      ops.appendChild(mkOp("再起動", "restart-agent",
        "claude-master の launchd 2 デーモンを再起動します（数秒の同期断）。"));
      ops.appendChild(mkOp("更新", "self-update",
        "最新 Release へ自己更新し再起動します（古い場合のみ）。"));
      const hist = el("button", { className: "diag-btn" }, "履歴");
      const histBox = el("div");
      hist.onclick = async () => {
        if (histBox.firstChild) { histBox.textContent = ""; return; }
        try { histBox.appendChild(await cmdAudit(d.id)); }
        catch (e) { if (e.message !== "unauth") alert("エラー: " + e.message); }
      };
      ops.appendChild(hist);
      head.appendChild(ops);
      const del = el("button", { className: "del" }, "削除");
      del.onclick = async () => {
        if (!confirm(d.id + " のペアリングを削除します。\n" +
          "（一覧から消えます。その PC は再 enroll で復帰可能）")) return;
        del.disabled = true;
        try {
          const r = await fetch("/api/pc/delete?pc=" +
            encodeURIComponent(d.id), { method: "POST",
            headers: { Accept: "application/json" } });
          if (r.status === 401) { location.href = "/login"; return; }
          if (!r.ok) throw new Error("削除失敗 " + r.status);
          location.reload();
        } catch (e) {
          del.disabled = false;
          alert("エラー: " + e.message);
        }
      };
      head.appendChild(del);
      card.appendChild(head);
      card.appendChild(histBox);
      card.appendChild(el("div", { className: "meta" },
        "セッション " + d.sessions + " 件（稼働中 " + d.active + "）"));
      const ss = await jget("/api/sessions?pc=" + encodeURIComponent(d.id));
      if (!ss || !ss.length) {
        card.appendChild(el("div", { className: "meta" },
          "稼働中のセッションはありません（PC 側で claude 起動中？）"));
      } else {
        for (const x of ss) {
          const row = el("div", { className: "s" });
          const dir = x.short_dir || x.key || "session";
          const lbl = el("span", null, dir);
          // ● に集約: 稼働中=緑（従来どおり）/ 更新あり=赤 / idle最新=無
          { const sd = statusDot(x.is_active, x.cm_version); if (sd) lbl.appendChild(sd); }
          row.appendChild(lbl);
          const right = el("span");
          const pre = diagPre(x);
          pre.style.display = "none";
          const diagBtn = el("button", { className: "diag-btn" }, "診断");
          diagBtn.onclick = () => {
            pre.style.display = pre.style.display === "none" ? "block" : "none";
          };
          right.appendChild(diagBtn);
          // 全セッションに「復帰」。pid- は backend が claude の jsonl
          // から会話 UUID を自動解決して復帰（解決不可は履歴にエラー＝
          // kill せず保全）。devices.js には PC 全体の "再起動"
          // (restart-agent) が既に居るので、セッション単位の方は
          // "復帰" で短縮＆衝突回避（claude --resume の和訳ニュアンス）。
          {
            const rb = el("button", { className: "diag-btn" }, "復帰");
            rb.onclick = async () => {
              if (!confirm(d.id + " / " + dir + "\n現在の claude を終了し " +
                "--resume で別プロセスとして復帰します。\n" +
                "元の端末（VSCode 等）には戻りません（Web/cloud で続行）。\n" +
                "よろしいですか？")) return;
              rb.disabled = true;
              try {
                await postCmd(d.id, "restart-proxy", x.key);
                $("stat").textContent = dir + " へ restart-proxy 投入（履歴で監査）";
              } catch (e) {
                if (e.message !== "unauth") alert("エラー: " + e.message);
              } finally { rb.disabled = false; }
            };
            right.appendChild(rb);
          }
          const a = el("a", {
            href: "/term?pc=" + encodeURIComponent(d.id) +
              "&sid=" + encodeURIComponent(x.key) +
              "&dir=" + encodeURIComponent(dir),
          }, "開く");
          right.appendChild(a);
          row.appendChild(right);
          card.appendChild(row);
          card.appendChild(pre);
        }
      }
      root.appendChild(card);
    }
  } catch (e) {
    if (e.message !== "unauth") $("stat").textContent = "エラー: " + e.message;
  }
}

// 端末を追加: enroll コード発行 → 新 PC で実行するコマンドを表示
function setupAdd() {
  const btn = $("addbtn"), out = $("enroll");
  if (!btn) return;
  btn.onclick = async () => {
    btn.disabled = true;
    try {
      const r = await fetch("/api/enroll", {
        method: "POST", headers: { Accept: "application/json" },
      });
      if (!r.ok) throw new Error("発行失敗 " + r.status);
      const j = await r.json();
      out.style.display = "block";
      out.textContent =
        "新しい PC で claude-master を用意し、以下を実行してください" +
        "（" + (j.expires_in || "15m") + "・一回限り）:\n\n" +
        j.command +
        "\n\n完了後その PC で `claude-master cloud agent` を起動すると" +
        "この一覧に表示されます。";
    } catch (e) {
      out.style.display = "block";
      out.textContent = "エラー: " + e.message;
    } finally {
      btn.disabled = false;
    }
  };
}

main();
setupAdd();
