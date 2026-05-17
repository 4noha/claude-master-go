//go:build windows

package selfupdate

import (
	"fmt"
	"os"
)

// placeBinary (windows): 実行中 .exe は直接 rename/上書き不可（共有
// 違反＝Access denied）。Windows は「実行中 exe を *別名へ* rename」は
// 許す（イメージは旧名のままマップ＝プロセス継続）ことを利用し、
//  1) 旧 .old 残骸を除去（前回更新分。旧プロセス終了済なら消える）
//  2) 実行中 exe を <exe>.old へ退避（パスを空ける）
//  3) 新バイナリ(tmp)を <exe> へ rename（次回起動から新版）
//  4) .old を best-effort 削除（実行中は失敗＝次回 update/再起動で消える）
// 失敗時は退避をロールバック。
func placeBinary(tmpName, exe string) error {
	old := exe + ".old"
	_ = os.Remove(old) // 前回更新の残骸（旧プロセス終了済なら消える）

	renamedOld := false
	if err := os.Rename(exe, old); err == nil {
		renamedOld = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("実行中 exe の退避失敗 %s: %w", exe, err)
	}

	if err := os.Rename(tmpName, exe); err != nil {
		if renamedOld {
			_ = os.Rename(old, exe) // ロールバック
		}
		return fmt.Errorf("置換失敗 %s: %w", exe, err)
	}

	_ = os.Remove(old) // 実行中は失敗。best-effort（次回 update/再起動で消える）
	return nil
}
