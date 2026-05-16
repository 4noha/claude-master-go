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
          const name = (x.short_dir || x.key || "session") +
            (x.is_active ? "" : "");
          const lbl = el("span", null, name);
          if (x.is_active) lbl.appendChild(el("span", { className: "dot" }, " ●"));
          row.appendChild(lbl);
          const a = el("a", {
            href: "/term?pc=" + encodeURIComponent(d.id) +
              "&sid=" + encodeURIComponent(x.key),
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
main();
