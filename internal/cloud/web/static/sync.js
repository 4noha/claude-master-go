// 同期更新（DECSET 2026）シム。proxy は毎フレームを ESC[?2026h …
// ESC[?2026l で括る（internal/screen/scroll.go）。バンドル xterm.js は
// 2026 未実装で ESC[2J を即時実行→画面空→再描画＝チカチカ。ここで
// h…l 区間を 1 ブロックに蓄積し **1 回の term.write** で流す＝xterm が
// 1 render tick で反映＝web 側のダブルバッファ。proxy/不変条件は無改変。
// 純粋関数（DOM 非依存）＝node で決定論検証可能（実出荷コードのまま）。
(function (g) {
  "use strict";
  var BSU = [0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x68]; // ESC [ ? 2 0 2 6 h
  var ESU = [0x1b, 0x5b, 0x3f, 0x32, 0x30, 0x32, 0x36, 0x6c]; // ESC [ ? 2 0 2 6 l

  function indexOf(data, pat, from) {
    outer:
    for (var i = from; i + pat.length <= data.length; i++) {
      for (var j = 0; j < pat.length; j++) {
        if (data[i + j] !== pat[j]) continue outer;
      }
      return i;
    }
    return -1;
  }
  // data 末尾が pat 先頭 k(1..len-1) と一致する最大 k（部分マーカー保持）
  function tailPrefix(data, pat) {
    var max = Math.min(pat.length - 1, data.length);
    for (var k = max; k >= 1; k--) {
      var ok = true;
      for (var j = 0; j < k; j++) {
        if (data[data.length - k + j] !== pat[j]) { ok = false; break; }
      }
      if (ok) return k;
    }
    return 0;
  }
  function concat(a, b) {
    var r = new Uint8Array(a.length + b.length);
    r.set(a, 0); r.set(b, a.length);
    return r;
  }

  // makeSyncFilter(emit) → feed(Uint8Array)。emit は「確定して xterm に
  // 書いてよいバイト列」を入力順で受ける。同期ブロックは ESU 到達まで
  // 一切 emit せず（＝途中の ESC[2J 空状態を描かせない）、到達時に
  // h..l 全体を **1 回**で emit。マーカーが chunk 境界で割れても carry
  // で再結合。emit 群の連結は入力の連結と完全一致（無損失・順序保存）。
  function makeSyncFilter(emit) {
    var carry = new Uint8Array(0);
    var syncing = false;
    var acc = new Uint8Array(0);

    return function feed(chunk) {
      var data = carry.length ? concat(carry, chunk) : chunk;
      carry = new Uint8Array(0);
      var pos = 0;
      while (pos < data.length) {
        if (!syncing) {
          var i = indexOf(data, BSU, pos);
          if (i < 0) {
            var rest = data.subarray(pos);
            var hold = tailPrefix(rest, BSU);
            var cut = rest.length - hold;
            if (cut > 0) emit(rest.subarray(0, cut));
            carry = rest.subarray(cut).slice();
            return;
          }
          if (i > pos) emit(data.subarray(pos, i));
          syncing = true;
          acc = new Uint8Array(0);
          pos = i; // BSU から蓄積（xterm は 2026 を無害に無視）
        } else {
          var j = indexOf(data, ESU, pos);
          if (j < 0) {
            var rest2 = data.subarray(pos);
            var hold2 = tailPrefix(rest2, ESU);
            var cut2 = rest2.length - hold2;
            if (cut2 > 0) acc = concat(acc, rest2.subarray(0, cut2));
            carry = rest2.subarray(cut2).slice();
            return; // syncing 継続・emit しない＝チラ見せ防止の核心
          }
          var end = j + ESU.length;
          acc = concat(acc, data.subarray(pos, end));
          emit(acc); // h..l を 1 回で（原子描画）
          acc = new Uint8Array(0);
          syncing = false;
          pos = end;
        }
      }
    };
  }
  g.cmMakeSyncFilter = makeSyncFilter;
})(typeof window !== "undefined" ? window : globalThis);
