//go:build !windows

package selfupdate

import (
	"fmt"
	"os"
)

// placeBinary は新バイナリ(tmp)を実行中バイナリ位置へ原子 rename する。
// unix は実行中でも rename 可（旧 inode は走行中プロセスが保持＝安全、
// 次回起動から新版）。M8f 前の replaceSelf 末尾と body バイト同一
// （darwin/linux parity 厳守）。
func placeBinary(tmpName, exe string) error {
	if err := os.Rename(tmpName, exe); err != nil {
		return fmt.Errorf("置換失敗 %s: %w", exe, err)
	}
	return nil
}
