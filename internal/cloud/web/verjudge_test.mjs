// 出荷 static/devices.js の版判定（relNum/cmpRel/updateAvailable）を
// そのまま抽出し node で決定論検証。実行: node internal/cloud/web/verjudge_test.mjs
// 仕様: ①ビルド接尾辞 -<N>-g<hash>/-dirty は無視（番号のみ判定）
//       ②開発版の先行（実行版 > 目標版）/同一 は許容＝警告なし（緑）
//       ③実行版が目標版より古い時のみ updateAvailable=true（赤）
//       ④番号が取れない側(dev/不明)は判定不能＝false（誤って赤にしない）
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const src = readFileSync(path.join(here, "static", "devices.js"), "utf8");
const m = src.match(/const relNum =[\s\S]*?\nfunction updateAvailable\(v\) \{[\s\S]*?\n\}/);
if (!m) { console.error("FAIL: 版判定ブロックを抽出できない"); process.exit(1); }
// TARGET をクロージャに与え updateAvailable を取り出す純粋ファクトリ。
const make = new Function("T", "let TARGET=T;\n" + m[0] + "\nreturn updateAvailable;");

let failed = 0;
const t = (target, v, want, desc) => {
  const got = make(target)(v);
  const ok = got === want;
  if (!ok) failed++;
  console.log(`${ok ? "ok  " : "FAIL"} ${desc} | target=${JSON.stringify(target)} v=${JSON.stringify(v)} -> ${got} (want ${want})`);
};

t("v0.1.3", "v0.1.3-2-g564cca2", false, "ビルド接尾辞差は許容(緑)");
t("v0.1.3", "v0.1.3", false, "同一(緑)");
t("v0.1.3", "v0.1.3-dirty", false, "-dirty も許容(緑)");
t("v0.1.3", "v0.1.4-1-gabc", false, "開発版が先行(緑・許容)");
t("v0.1.3", "v0.2.0", false, "先行 minor(緑・許容)");
t("v0.1.3", "v0.1.2", true, "実行版が古い→更新あり(赤)");
t("v0.1.3", "v0.0.9-5-gx", true, "古い patch/minor(赤)");
t("v0.2.0", "v0.1.9-9-gx", true, "minor 古い(赤)");
t("v0.1.10", "v0.1.9", true, "数値比較(10>9 なので 9 は古い=赤)");
t("v0.1.9", "v0.1.10-1-gx", false, "数値比較(10>9 で先行=緑)");
t("v0.1.3", "dev", false, "dev=番号取れず→判定不能(警告なし)");
t("", "v0.1.3", false, "target 不明→判定不能(警告なし)");
t("v0.1.3", "", false, "version 空→判定不能(警告なし)");

console.log(failed ? `\n${failed} FAILED` : "\nALL PASS");
process.exit(failed ? 1 : 0);
