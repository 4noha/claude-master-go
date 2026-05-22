package main

// `claude-master sessions [--json]` — 現存 proxy の cwd 一覧。
//
//   PID      uptime      host_out    cli  cwd
//   13119    1d5h        24.8MB      1    /Users/4noha/works/claude-master-go
//   ...
//
// 出典は ~/.claude-master/diag/<pid>.snap（proxy が 30s 周期で原子書出）
// で、proxy に問合せず ps も叩かない＝負荷ゼロ・どの cwd の claude が
// 生きているか即時把握。死亡 PID は isAlive で除外（Sweep と一貫）。
//
// --json はスクリプト連携用に Snap 配列を JSON 出力（cli フィルタや
// monitor 連携で `jq` 可能）。

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/4noha/claude-master-go/internal/diag"
)

func runSessions(args []string) {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "-h", "--help":
			fmt.Println("usage: claude-master sessions [--json]")
			fmt.Println("  現存 proxy の PID/uptime/host_out/clients/cwd を一覧。")
			fmt.Println("  --json: スクリプト連携用に Snap 配列を JSON 出力。")
			return
		default:
			fmt.Fprintln(os.Stderr, "sessions: unknown arg:", a)
			os.Exit(2)
		}
	}
	snaps, err := diag.ListSessions()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sessions:", err)
		os.Exit(1)
	}
	// cwd でソート（同プロジェクトのセッションが隣接して見やすい）
	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].Cwd != snaps[j].Cwd {
			return snaps[i].Cwd < snaps[j].Cwd
		}
		return snaps[i].Pid < snaps[j].Pid
	})

	if asJSON {
		if snaps == nil {
			snaps = []diag.Snap{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(snaps)
		return
	}
	if len(snaps) == 0 {
		fmt.Println("(セッション無し)")
		return
	}
	fmt.Printf("%-7s  %-10s  %-9s  %-3s  %s\n", "PID", "uptime", "host_out", "cli", "cwd")
	for _, s := range snaps {
		fmt.Printf("%-7d  %-10s  %-9s  %-3d  %s\n",
			s.Pid, fmtUptime(s.UptimeSec),
			fmtMB(s.HostOut), s.ConnectedClients, s.Cwd)
	}
}

// fmtUptime: 秒 → "Ns" / "Nm" / "NhMm" / "NdHh"（日跨ぎは分を捨てる＝
// 視認性優先・正確値が要るなら --json）。
func fmtUptime(sec int64) string {
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	d := sec / 86400
	h := (sec % 86400) / 3600
	m := (sec % 3600) / 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd%dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

func fmtMB(b int64) string {
	return fmt.Sprintf("%.1fMB", float64(b)/1e6)
}
