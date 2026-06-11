// 出荷 static/term.js の cmReconnectGate（自動再接続の可否判定）を
// そのまま抽出し node で決定論検証。実行:
//   node internal/cloud/web/reconnectgate_test.mjs
//
// 仕様（Firestore 更新 push / 入力での自動再接続。near-$0 維持が前提）:
//  ①CONNECTING/OPEN 中は張らない（多重接続を構造的に作らない）
//  ②閉鎖中は契機（push/入力）があれば張る。ただし backoff
//    (1s×2^attempts・上限 30s) 未経過なら張らない（失敗連打抑止）
//  ③connect 時 attempts+1（成功 onopen で呼び元が 0 に戻す）
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const src = readFileSync(path.join(here, "static", "term.js"), "utf8");
const m = src.match(
  /function cmReconnectGate\(s\) \{[\s\S]*?\n  return \{ connect: true, attempts: s\.attempts \+ 1 \};\n\}/);
if (!m) { console.error("FAIL: cmReconnectGate を抽出できない"); process.exit(1); }
const gate = new Function(m[0] + "\nreturn cmReconnectGate;")();

let failed = 0;
const check = (cond, desc) => {
  if (!cond) failed++;
  console.log(`${cond ? "ok  " : "FAIL"} ${desc}`);
};

// ① 接続中/接続済みは絶対に張らない（push が連打されても無駄打ちゼロ）
for (const st of [0, 1]) {
  const r = gate({ wsState: st, now: 1e9, lastAttemptAt: 0, attempts: 0 });
  check(!r.connect && r.attempts === 0, `wsState=${st} では張らない`);
}

// ② 閉鎖(-1/2/3)中、十分時間が経っていれば張る（attempts+1）
for (const st of [-1, 2, 3]) {
  const r = gate({ wsState: st, now: 10000, lastAttemptAt: 0, attempts: 0 });
  check(r.connect && r.attempts === 1, `wsState=${st} 閉鎖中は張る（attempts 0→1）`);
}

// ②' backoff: attempts=0 は 1s。1s 未満は張らない/1s 以上で張る
{
  const r1 = gate({ wsState: 3, now: 999, lastAttemptAt: 0, attempts: 0 });
  check(!r1.connect, "attempts=0: 999ms では張らない（backoff 1s）");
  const r2 = gate({ wsState: 3, now: 1000, lastAttemptAt: 0, attempts: 0 });
  check(r2.connect, "attempts=0: 1000ms で張る");
}

// ②'' 指数 backoff: attempts=3 → 8s、attempts=10 → 上限 30s
{
  const r1 = gate({ wsState: 3, now: 7999, lastAttemptAt: 0, attempts: 3 });
  const r2 = gate({ wsState: 3, now: 8000, lastAttemptAt: 0, attempts: 3 });
  check(!r1.connect && r2.connect, "attempts=3: backoff 8s（7999ms✗/8000ms✓）");
  const r3 = gate({ wsState: 3, now: 29999, lastAttemptAt: 0, attempts: 10 });
  const r4 = gate({ wsState: 3, now: 30000, lastAttemptAt: 0, attempts: 10 });
  check(!r3.connect && r4.connect, "attempts=10: backoff 上限 30s");
}

// ③ 失敗を重ねるシナリオ: 張るたび attempts が増え間隔が伸びる
{
  let attempts = 0, lastAttemptAt = 0, now = 0;
  const tries = [];
  for (let i = 0; i < 2000; i++) {
    now += 1000; // 1s ごとに push 契機が来る最悪ケース
    const r = gate({ wsState: 3, now, lastAttemptAt, attempts });
    if (r.connect) {
      attempts = r.attempts;
      lastAttemptAt = now;
      tries.push(now);
    }
  }
  // 2000 秒で接続試行は高々 10 回前後（1+2+4+...+30s 上限）に収まる
  check(tries.length <= 70,
    `push 連打でも試行は backoff で抑制（2000s で ${tries.length} 回 ≤ 70）`);
  const gaps = tries.slice(1).map((t, i) => t - tries[i]);
  check(gaps.every((g, i) => i === 0 || g >= Math.min(30000, gaps[i - 1])) ||
    gaps[gaps.length - 1] === 30000,
    `試行間隔が単調拡大→30s 飽和（末尾 gap=${gaps[gaps.length - 1]}ms）`);
}

if (failed) { console.error(`${failed} 件 FAIL`); process.exit(1); }
console.log("all ok");
