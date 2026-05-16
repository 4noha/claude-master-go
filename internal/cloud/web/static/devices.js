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

async function main() {
  try {
    const devs = await jget("/api/devices");
    if (!devs.length) { $("stat").textContent = "端末がありません"; return; }
    $("stat").textContent = devs.length + " 台接続";
    const root = $("devices");
    for (const d of devs) {
      const card = el("div", { className: "dev" });
      card.appendChild(el("h2", null, d.id));
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
          row.appendChild(lbl);
          const a = el("a", {
            href: "/term?pc=" + encodeURIComponent(d.id) +
              "&sid=" + encodeURIComponent(x.key) +
              "&dir=" + encodeURIComponent(dir),
          }, "Web ターミナルを開く");
          row.appendChild(a);
          card.appendChild(row);
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
