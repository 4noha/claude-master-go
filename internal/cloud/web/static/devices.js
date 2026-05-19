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
// しない）。stripV で先頭 v を無視して比較（v0.1.3 == 0.1.3）。
let TARGET = "";
const stripV = (v) => String(v || "").replace(/^v/, "");
function verBadge(v) {
  const sp = el("span", { className: "ver" });
  if (!v) { sp.textContent = " ?"; sp.title = "版不明"; return sp; }
  if (!TARGET) { sp.textContent = " " + v; sp.title = "目標版取得不可"; return sp; }
  const ok = stripV(v) === stripV(TARGET);
  sp.textContent = ok ? " 🟢" : " 🔴";
  sp.className = "ver " + (ok ? "vok" : "vbad");
  sp.title = ok ? v + "（最新）" : v + " → 要更新 " + TARGET;
  return sp;
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
      h2.appendChild(verBadge(d.cm_version)); // PC(agent) 版
      head.appendChild(h2);
      const del = el("button", { className: "del" }, "ペアリング削除");
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
          if (x.is_active) lbl.appendChild(el("span", { className: "dot" }, " ●"));
          lbl.appendChild(verBadge(x.cm_version)); // per-proxy 版（旧 inode→🔴）
          row.appendChild(lbl);
          const right = el("span");
          const pre = diagPre(x);
          pre.style.display = "none";
          const diagBtn = el("button", { className: "diag-btn" }, "診断");
          diagBtn.onclick = () => {
            pre.style.display = pre.style.display === "none" ? "block" : "none";
          };
          right.appendChild(diagBtn);
          const a = el("a", {
            href: "/term?pc=" + encodeURIComponent(d.id) +
              "&sid=" + encodeURIComponent(x.key) +
              "&dir=" + encodeURIComponent(dir),
          }, "Web ターミナルを開く");
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
