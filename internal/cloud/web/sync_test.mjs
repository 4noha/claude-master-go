// 実出荷 static/sync.js を node でそのまま読み込み決定論検証。
// 合成でなく shipped コードを実行。go test には載らない（JS）ので
// `node internal/cloud/web/sync_test.mjs` で実行（CI/手動）。
// 検証: ①無損失＋順序保存 ②BSU..ESU フレームは 1 emit（原子）
// ③同期ブロック未閉なら emit しない（ESC[2J チラ見せ防止の核心）
// ④マーカーが chunk 境界で割れても再結合 ⑤素通しは即時。

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const src = readFileSync(path.join(here, "static", "sync.js"), "utf8");
(0, eval)(src); // IIFE が globalThis.cmMakeSyncFilter を生やす
const make = globalThis.cmMakeSyncFilter;
if (typeof make !== "function") {
  console.error("FAIL: cmMakeSyncFilter 未定義");
  process.exit(1);
}

const BSU = [0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x68];
const ESU = [0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x6c];
const cat = (arrs) => {
  let n = 0;
  for (const a of arrs) n += a.length;
  const r = new Uint8Array(n);
  let o = 0;
  for (const a of arrs) { r.set(a, o); o += a.length; }
  return r;
};
const u8 = (x) => (x instanceof Uint8Array ? x : Uint8Array.from(x));
const B = (...parts) => cat(parts.map(u8));
const txt = (s) => Uint8Array.from(Buffer.from(s, "latin1"));
const eq = (a, b) => a.length === b.length && a.every((v, i) => v === b[i]);

let failed = 0;
function check(name, cond, extra) {
  if (cond) console.log("ok  - " + name);
  else { failed++; console.error("FAIL- " + name + (extra ? "  " + extra : "")); }
}

function run(inputBytes, chunkSizes) {
  const emits = [];
  const feed = make((b) => emits.push(b.slice()));
  let p = 0;
  for (const sz of chunkSizes) {
    feed(inputBytes.subarray(p, p + sz));
    p += sz;
  }
  if (p < inputBytes.length) feed(inputBytes.subarray(p));
  return emits;
}
function sizes(total, step) {
  const a = [];
  for (let i = 0; i < total; i += step) a.push(Math.min(step, total - i));
  return a;
}

// 1: 素通し（マーカー無し）無損失・順序保存
{
  const inp = txt("hello world 日本語 \x07\x00 tail");
  const em = run(inp, [3, 5, 100]);
  check("passthrough lossless/order", eq(cat(em), inp));
}

// 2: 1 フレームは 1 emit（原子）。あらゆる分割で不変
{
  const frame = B(BSU, txt("\x1b[2J\x1b[9999;1Hcontent 行1\r\n行2"), ESU);
  for (const step of [1, 2, 3, 7, 8, 9, 1000]) {
    const em = run(frame, sizes(frame.length, step));
    check("frame atomic@step=" + step,
      em.length === 1 && eq(em[0], frame), "emits=" + em.length);
  }
}

// 3: 同期ブロック未閉なら emit しない（チラ見せ防止の核心）
{
  const emits = [];
  const feed = make((b) => emits.push(b));
  feed(B(BSU, txt("partial redraw だけ（ESU まだ）")));
  check("no emit while sync open", emits.length === 0, "emits=" + emits.length);
  feed(B(ESU));
  check("emit once on close", emits.length === 1);
}

// 4: マーカーが chunk 境界で割れても再結合（無損失・原子）
{
  const inp = B(txt("pre"), BSU, txt("X"), ESU, txt("post"));
  const em = run(inp, sizes(inp.length, 1)); // 1byte ずつ＝最悪分割
  check("split-marker lossless", eq(cat(em), inp));
  const frame = B(BSU, txt("X"), ESU);
  check("split-marker frame atomic", em.some((e) => eq(e, frame)),
    "emits=" + em.map((e) => e.length).join(","));
}

// 5: 連続複数フレーム＋間テキスト：無損失＆各フレーム原子
{
  const f = (s) => B(BSU, txt(s), ESU);
  const inp = B(f("F1"), txt("between"), f("F2 longer payload"), txt("END"));
  const em = run(inp, sizes(inp.length, 4));
  check("multi-frame lossless", eq(cat(em), inp));
  check("multi-frame F1 atomic", em.some((e) => eq(e, f("F1"))));
  check("multi-frame F2 atomic", em.some((e) => eq(e, f("F2 longer payload"))));
}

console.log(failed ? `\n${failed} FAILED` : "\nALL PASS");
process.exit(failed ? 1 : 0);
