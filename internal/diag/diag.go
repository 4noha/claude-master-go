// Package diag は claude-master proxy の **次回クラッシュを実環境で
// 捕える**ための軽量診断機構。VSCode を巻き込む「窓が消える」現象は
// 親プロセス（Code/zsh）死亡→子（claude-master proxy）への SIGHUP で
// proxy も殺される連鎖が一因として疑われる。Go の既定では SIGHUP/
// SIGTERM/SIGINT は defer を走らせず即終了するため、現状は run.go の
// sock/status クリーンアップ defer も走らない＝事後追跡材料も残らない。
// 本パッケージは:
//
//	・atomic な軽量バイトカウンタ（host stdout / claude master / client）
//	・周期スナップショット（RSS/goroutine/カウンタ/uptime）を <pid>.snap
//	  へ原子書出（最新のみ・上書き、SIGKILL 等でも最後の snap は残る）
//	・SIGHUP/SIGTERM/SIGINT 捕捉時に goroutine 全 stack ＋ MemStats ＋
//	  カウンタを `<crash dir>/<pid>-<ts>-<reason>.dump` へダンプ
//
// を提供し、proxy 終了経路は signal を受け取って **runProxy へ抜ける**
// ことで内部 defer（run.go:66 sock/status クリーンアップ）も走らせる。
// 鉄則#1: 推測修正の前に実環境で現物を採取するための土台。
package diag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// Counters はホットパスで atomic に進むバイト計数と最終書込時刻（unix
// nano）。全フィールド 0 値が安全。pointer で渡し goroutine 間共有。
type Counters struct {
	HostOutBytes  atomic.Int64 // proxy → host stdout（VSCode 端末 PTY）
	HostOutLastNs atomic.Int64 // 最終 host stdout 書込（unix nano）
	MasterBytes   atomic.Int64 // claude master → proxy（claude 出力源）
	MasterLastNs  atomic.Int64
	ClientBytes   atomic.Int64 // socket clients への合算
	ClientLastNs  atomic.Int64
	ImagePaste    atomic.Int64 // 画像貼付回数（VSCode 端末への大 ESC ペイロード源）
}

func NewCounters() *Counters { return &Counters{} }

// CountingWriter は io.Writer ラッパで成功バイト数を ctr に加算し最終
// 時刻を ts に書く（atomic・lock-free）。ホットパスの追加コストは
// atomic 2 操作のみ＝VSCode 端末への書込流量に対し無視できる。
type CountingWriter struct {
	w   io.Writer
	cnt *atomic.Int64
	ts  *atomic.Int64
}

// WrapWriter は w をカウンタへ橋渡し（cnt/ts は nil-safe）。
func WrapWriter(w io.Writer, cnt, ts *atomic.Int64) io.Writer {
	if cnt == nil && ts == nil {
		return w
	}
	return &CountingWriter{w: w, cnt: cnt, ts: ts}
}

func (c *CountingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	if n > 0 {
		if c.cnt != nil {
			c.cnt.Add(int64(n))
		}
		if c.ts != nil {
			c.ts.Store(time.Now().UnixNano())
		}
	}
	return n, err
}

// Snap は WriteSnap が書く JSON の構造。後方互換のため field 追加は末尾。
type Snap struct {
	Pid         int            `json:"pid"`
	Ts          string         `json:"ts"`
	UptimeSec   int64          `json:"uptime_sec"`
	Goroutines  int            `json:"goroutines"`
	HeapAllocB  uint64         `json:"heap_alloc_bytes"`
	HeapInuseB  uint64         `json:"heap_inuse_bytes"`
	SysB        uint64         `json:"sys_bytes"`
	NumGC       uint32         `json:"num_gc"`
	HostOut     int64          `json:"host_out_bytes"`
	HostOutLast string         `json:"host_out_last"` // RFC3339Nano か "never"
	Master      int64          `json:"master_bytes"`
	MasterLast  string         `json:"master_last"`
	Client      int64          `json:"client_bytes"`
	ClientLast  string         `json:"client_last"`
	ImagePaste  int64          `json:"image_paste_count"`
	Extras      map[string]any `json:"extras,omitempty"`
}

var startTime = time.Now()

// SetStartTime はテストが uptime の初期値を制御するための seam。
func SetStartTime(t time.Time) { startTime = t }

func nsToHuman(ns int64) string {
	if ns <= 0 {
		return "never"
	}
	return time.Unix(0, ns).Format(time.RFC3339Nano)
}

// snapNow はカウンタ + MemStats + goroutine 数で Snap を構築。
func snapNow(pid int, c *Counters, extras map[string]any) Snap {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := Snap{
		Pid: pid, Ts: time.Now().Format(time.RFC3339Nano),
		UptimeSec:  int64(time.Since(startTime).Seconds()),
		Goroutines: runtime.NumGoroutine(),
		HeapAllocB: ms.HeapAlloc, HeapInuseB: ms.HeapInuse, SysB: ms.Sys, NumGC: ms.NumGC,
		Extras: extras,
	}
	if c != nil {
		s.HostOut, s.HostOutLast = c.HostOutBytes.Load(), nsToHuman(c.HostOutLastNs.Load())
		s.Master, s.MasterLast = c.MasterBytes.Load(), nsToHuman(c.MasterLastNs.Load())
		s.Client, s.ClientLast = c.ClientBytes.Load(), nsToHuman(c.ClientLastNs.Load())
		s.ImagePaste = c.ImagePaste.Load()
	}
	return s
}

// WriteSnap は <pid>.snap を原子的に書き出す（tmp → rename）。最新のみ
// 保持（上書き）。SIGKILL/SIGSEGV 等の uncatchable 致命でも、最後の
// 周期書出 snap は残る＝事後解析の最後の砦。fail-soft（err を返すが
// 呼出側は無視可能：診断失敗が本処理を止めてはならない）。
func WriteSnap(path string, pid int, c *Counters, extras map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(snapNow(pid, c, extras), "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// safeReason は filename 用にホワイトリスト化（filesystem 衝突回避）。
func safeReason(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// WriteDump は致命イベント時に **全 goroutine stack ＋ MemStats ＋
// counters** を `<dir>/<pid>-<ts>-<reason>.dump` へ書く（fail-soft）。
// 戻り値はファイルパス。reason は signal-hup / signal-term / panic /
// custom など短い識別子（filename 用にサニタイズされる）。
func WriteDump(dir string, pid int, reason string, c *Counters, extras map[string]any) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ts := time.Now().Format("20060102T150405.000")
	path := filepath.Join(dir, fmt.Sprintf("%d-%s-%s.dump", pid, ts, safeReason(reason)))
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "claude-master diagnostic dump\n")
	fmt.Fprintf(&buf, "pid=%d reason=%s ts=%s\n", pid, reason, time.Now().Format(time.RFC3339Nano))
	snap := snapNow(pid, c, extras)
	sb, _ := json.MarshalIndent(snap, "", "  ")
	fmt.Fprintf(&buf, "snapshot=%s\n", sb)
	fmt.Fprintf(&buf, "\n=== all goroutines ===\n")
	stk := make([]byte, 1<<16)
	for {
		n := runtime.Stack(stk, true)
		if n < len(stk) {
			stk = stk[:n]
			break
		}
		if len(stk) >= 8<<20 { // 8MB 上限
			break
		}
		stk = make([]byte, len(stk)*2)
	}
	buf.Write(stk)
	return path, os.WriteFile(path, buf.Bytes(), 0o644)
}

// StartPeriodicSnap は ctx が cancel されるまで interval 毎に WriteSnap。
// SIGKILL 等 uncatchable 致命でも「最後の周期 snap」を残す唯一の経路
// （catchable は WriteDump が役割）。失敗は黙殺＝診断はベストエフォート。
func StartPeriodicSnap(ctx context.Context, interval time.Duration, snapPath string,
	pid int, c *Counters, extrasFn func() map[string]any) {
	if interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				var ex map[string]any
				if extrasFn != nil {
					ex = extrasFn()
				}
				_ = WriteSnap(snapPath, pid, c, ex)
			}
		}
	}()
}
