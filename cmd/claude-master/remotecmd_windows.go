//go:build windows

package main

import "strings"

// remoteAttachCmd は他 PC のセッションを映すリモート窓で動かす再接続
// ループコマンド（windows=PowerShell）。
//
// psmux の default-shell は powershell.exe で、new-window のコマンドは
// それで実行される。POSIX sh 構文（`while true; do … ; sleep 30; done`）
// は PowerShell では即エラー→pane 終了→remain-on-exit off で**窓が
// 生成直後に消滅**→ReconcileRemote が毎周「窓無し」と誤判定して 6 窓を
// 際限なく再作成する runaway storm の**真因**だった（実 psmux で
// `while ($true){ … }` 窓は生存継続、POSIX 窓は即消滅を確証済）。
//
// 値は PowerShell 単一引用符でリテラル化（'…' は ' 以外すべてリテラル
// ＝Windows パスのバックスラッシュも安全。' は '' でエスケープ）。
// ヒューリスティックではなく PowerShell の字句規則そのもの。
func remoteAttachCmd(self, gcp, relay, sa, sid, pc string) string {
	q := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "while ($true) { " +
		"$env:GCP_PROJECT=" + q(gcp) + "; " +
		"$env:CLOUD_RELAY_URL=" + q(relay) + "; " +
		"$env:GOOGLE_APPLICATION_CREDENTIALS=" + q(sa) + "; " +
		"& " + q(self) + " cloud attach " + q(sid) + " --pc " + q(pc) + "; " +
		"Start-Sleep -Seconds 30 }"
}
