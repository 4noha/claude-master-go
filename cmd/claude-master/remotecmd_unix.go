//go:build !windows

package main

import "fmt"

// remoteAttachCmd は他 PC のセッションを映すリモート窓で動かす再接続
// ループコマンド（unix=POSIX sh）。M8 前に main.go へ inline されていた
// 文字列と **バイト同一**（darwin/linux parity 厳守。tmux は new-window
// のコマンドを /bin/sh -c で実行する）。
func remoteAttachCmd(self, gcp, relay, sa, sid, pc string) string {
	return fmt.Sprintf("while true; do env GCP_PROJECT=%s "+
		"CLOUD_RELAY_URL=%s GOOGLE_APPLICATION_CREDENTIALS=%s "+
		"%s cloud attach %s --pc %s; sleep 30; done",
		gcp, relay, sa, self, sid, pc)
}
