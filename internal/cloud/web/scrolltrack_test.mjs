// 出荷 static/term.js の cmTrackScroll（ライブ行追従スクロール計算）を
// そのまま抽出し node で決定論検証。実行:
//   node internal/cloud/web/scrolltrack_test.mjs
// 症状: スマホで「下に黒いエリア」「入力位置がちょくちょくズレる」。
// 真因: proxy は bottom-fill 撤去後 上詰めの短いフレームを毎フレーム
// 送り（内容長＝カーソル行が変動）、クライアントが一度しかスクロール
// を合わせないとカーソルが固定スクロールに対しドリフト＝入力ズレ／
// 内容下の空きグリッド＝黒。仕様（ScrollRenderer follow を native
// スクロールへ適用）:
//  ①following 中は沈静毎にカーソルを可視下端へ再ピン＝viewport 内
//    カーソル位置が常に一定（＝ズレない・空きを見ない）
//  ②ユーザが履歴へ遡る（カーソルが可視域より下に隠れる）と following
//    解除→settle はスクロールを一切動かさない（読書を妨げない）
//  ③最下部へ戻すと再 following
// 合成でなく実 static を抽出し、まず旧挙動（1回着地のみ）でドリフト
// が起きることを示してから新挙動で解消されることを確認する。
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const src = readFileSync(path.join(here, "static", "term.js"), "utf8");
const m = src.match(
  /function cmTrackScroll\(s\) \{[\s\S]*?\n  return \{ scrollTop: s\.scrollTop, following: false \};\n\}/);
if (!m) { console.error("FAIL: cmTrackScroll を抽出できない"); process.exit(1); }
const cmTrackScroll = new Function(m[0] + "\nreturn cmTrackScroll;")();

let failed = 0;
const check = (cond, desc) => {
  if (!cond) failed++;
  console.log(`${cond ? "ok  " : "FAIL"} ${desc}`);
};

// 実 web 条件: 1 行 20px、可視 600px(=30 行)、固定 500 行=10000px。
const cellH = 20, clientHeight = 600, scrollHeight = 10000;
const cursorPx = (cy) => (cy + 1) * cellH;

// ── 旧挙動の再現（1回だけ着地→以後 scrollTop 固定）でドリフト発生 ──
// 着地時 cy=10 → scrollTop を一度合わせる。以後 claude 出力で内容が
// 伸び cy=40,70 になっても scrollTop は不動＝カーソルが可視域から
// 下へはみ出す（=入力位置ズレ／その下が黒）。これを機械再現。
{
  let cy = 10;
  let scrollTop = Math.max(0, cursorPx(cy) - clientHeight); // 旧 toCursor 相当
  const drift = (c) => cursorPx(c) - (scrollTop + clientHeight); // >0=可視下端より下
  check(drift(10) <= 0, "旧:着地直後はカーソル可視（drift<=0）");
  check(drift(40) > 0 && drift(70) > drift(40),
    `旧:内容成長でカーソルが可視域外へドリフト（10→40→70 で ${drift(40)},${drift(70)}px はみ出し＝入力ズレ/黒の真因）`);
}

// ── 新挙動: following 中は沈静毎に再ピン＝viewport 内カーソル位置一定 ──
{
  let following = true, scrollTop = 0;
  const settle = (cy) => {
    const r = cmTrackScroll({ mode: "settle", following, cy, cellH,
      clientHeight, scrollHeight, scrollTop });
    following = r.following; scrollTop = r.scrollTop;
    return cursorPx(cy) - scrollTop; // カーソルの viewport 内 px 位置
  };
  const p1 = settle(10), p2 = settle(40), p3 = settle(70), p4 = settle(120);
  // 内容が 11→121 行へ伸びてもカーソルの画面内位置が一定（ズレない）。
  // cy=10 は pinTop が 0 clamp されるので可視内（<=clientHeight）であれば可。
  check(p1 <= clientHeight, `新:cy=10 はカーソル可視（${p1}px<=${clientHeight}）`);
  check(p2 === clientHeight && p3 === clientHeight && p4 === clientHeight,
    `新:成長後もカーソルは常に可視下端で一定＝入力ズレ無（${p2},${p3},${p4}）`);
  check(following === true, "新:following 維持");
}

// ── ②ユーザが履歴へ遡ると following 解除＝settle はスクロール不動 ──
{
  const cy = 70; // cursorPx=1420
  // ユーザが最上部へドラッグ（scrollTop=0）。可視下端 600 < 1420 ＝
  // カーソルは画面外下方＝履歴閲覧中。
  let r = cmTrackScroll({ mode: "userscroll", following: true, cy, cellH,
    clientHeight, scrollHeight, scrollTop: 0 });
  check(r.following === false, "②遡り（カーソルが可視域より下）で following 解除");
  // この状態で沈静が来てもユーザのスクロール位置を動かさない。
  r = cmTrackScroll({ mode: "settle", following: false, cy, cellH,
    clientHeight, scrollHeight, scrollTop: 0 });
  check(r.scrollTop === 0 && r.following === false,
    "②following=false の settle はスクロール不動（読書を妨げない）");
}

// ── ③最下部へ戻すと再 following→次の settle で再ピン ──
{
  const cy = 70; // cursorPx=1420 → 下端に合わせる scrollTop=820
  let r = cmTrackScroll({ mode: "userscroll", following: false, cy, cellH,
    clientHeight, scrollHeight, scrollTop: 820 });
  check(r.following === true, "③最下部へ戻すと再 following");
  r = cmTrackScroll({ mode: "settle", following: true, cy, cellH,
    clientHeight, scrollHeight, scrollTop: 820 });
  check(r.scrollTop === 820, "③再 following 後の settle はカーソルを下端へ再ピン");
}

// ── プログラム的ピン直後の scroll イベントは following を壊さない ──
// （ピン適用後 scrollTop=cursorPx-clientHeight＝viewBottom=cursorPx）。
{
  const cy = 70, scrollTop = cursorPx(70) - clientHeight; // ピン直後
  const r = cmTrackScroll({ mode: "userscroll", following: true, cy, cellH,
    clientHeight, scrollHeight, scrollTop });
  check(r.following === true,
    "ピン由来 scroll でも geometry 上 following 維持＝guard 不要");
}

// ── clamp: 端でも範囲内（maxTop=scrollHeight-clientHeight=9400） ──
{
  const r0 = cmTrackScroll({ mode: "settle", following: true, cy: 0, cellH,
    clientHeight, scrollHeight, scrollTop: 0 });
  check(r0.scrollTop === 0, "clamp:先頭付近は 0 以下にしない");
  const rN = cmTrackScroll({ mode: "settle", following: true, cy: 499, cellH,
    clientHeight, scrollHeight, scrollTop: 0 });
  check(rN.scrollTop === scrollHeight - clientHeight,
    "clamp:最下行でも maxTop を超えない");
}

console.log(failed ? `\n${failed} FAILED` : "\nALL PASS");
process.exit(failed ? 1 : 0);
