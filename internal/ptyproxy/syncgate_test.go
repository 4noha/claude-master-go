//go:build !windows

package ptyproxy

// 一層目ダブルバッファ（claude の DECSET 2026 BSU..ESU 中は frame を
// 放送しない）の実キーパス検証。合成ストリームではなく**実 claude 録画
// (resume-burst) の実 sync 区間**を実 PTY 経由で分割供給し、実 unix
// socket client が受ける frame 数の推移で機械判定する。
//
// 背景（2026-06-11 実報告「再描画中の描画ブロックが明確に出る」）:
// 実 claude は再描画を ?2026h..?2026l で括る（録画 90KB 中 35 対・最大
// 73KB）が、旧 server は master 4KB read 毎に放送＝再描画の中間状態を
// frame として送っていた。各 frame は転送的に atomic（チラつかない）が
// 意味的には半描画＝「ブロックが見える」の正体。

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
)

// findBigSyncRegion は録画から「BSU..ESU が 8KB 以上」の実 sync 区間を
// 探し、(BSU開始, ESU終端) を返す。
func findBigSyncRegion(t *testing.T, data []byte) (int, int) {
	t.Helper()
	bsu, esu := []byte("\x1b[?2026h"), []byte("\x1b[?2026l")
	i := 0
	for {
		j := bytes.Index(data[i:], bsu)
		if j < 0 {
			break
		}
		j += i
		e := bytes.Index(data[j:], esu)
		if e < 0 {
			break
		}
		e += j + len(esu)
		if e-j >= 8*1024 {
			return j, e
		}
		i = e
	}
	t.Skip("録画に 8KB 以上の sync 区間が無い")
	return 0, 0
}

func countFrames(s string) int { return strings.Count(s, "\x1b[?2026h") }

// 録画の実 sync 区間を 3 分割して時間差で吐く proxy を作り、client の
// 受信 frame 数を checkpoints で観測する共通ハーネス。
func startSplitServer(t *testing.T, parts [][]byte, gaps []string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	var cmd []string
	cmd = append(cmd, "sleep 0.6") // client 接続+RESIZE の猶予
	for i, p := range parts {
		f := filepath.Join(dir, "part"+string(rune('a'+i)))
		if err := os.WriteFile(f, p, 0o644); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			cmd = append(cmd, "sleep "+gaps[i-1])
		}
		cmd = append(cmd, "cat "+f)
	}
	cmd = append(cmd, "sleep 5")
	p, err := Start([]string{"/bin/sh", "-c", strings.Join(cmd, "; ")}, 164, 50)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	srv := NewServer(p, &config.Config{
		SizePolicy: "client", NavKey: []byte{0x1c},
		NavScrollStep: 1, NavPageStep: 10, NavWheelStep: 3,
	}, nil, 0, 0)
	sock := tmpSock(t)
	if err := srv.Serve(sock); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { srv.Stop(); p.Close() })
	return srv, sock
}

// claude の sync 区間の途中 read では frame を放送せず、ESU 到達の read
// で 1 回放送する（旧コードは途中 read 毎に放送＝このテストが落ちる）。
func TestSyncGateHoldsMidRedrawFrames(t *testing.T) {
	dir := fixtureDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "bytes.bin"))
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	b, e := findBigSyncRegion(t, data)
	region := data[b:e]
	// p1=BSU+先頭 4KB / p2=中間 4KB / p3=残り（ESU を含む）
	p1, p2, p3 := region[:4096], region[4096:8192], region[8192:]

	_, sock := startSplitServer(t, [][]byte{p1, p2, p3},
		[]string{"0.5", "0.5"})
	c := dial(t, sock)
	defer c.Close()
	d := newDrainer(c)
	if _, err := c.Write(resize(50, 164)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond) // attach catch-up + RESIZE 分が届く
	base := countFrames(d.snapshot())
	if base == 0 {
		t.Fatal("catch-up frame が来ない（前提崩れ）")
	}

	time.Sleep(500 * time.Millisecond) // p1 (BSU+中間) 供給後
	after1 := countFrames(d.snapshot())
	time.Sleep(500 * time.Millisecond) // p2 (中間) 供給後
	after2 := countFrames(d.snapshot())
	if after1 != base || after2 != base {
		t.Fatalf("sync 途中の中間状態が放送された（旧バグ）: base=%d p1後=%d p2後=%d",
			base, after1, after2)
	}

	dl := time.Now().Add(3 * time.Second) // p3 (ESU) 供給後
	for countFrames(d.snapshot()) == base && time.Now().Before(dl) {
		time.Sleep(50 * time.Millisecond)
	}
	final := countFrames(d.snapshot())
	if final <= base {
		t.Fatalf("ESU 後に frame が来ない: base=%d final=%d", base, final)
	}
}

// 安全弁: ESU が syncHoldMax を超えて来なければ中間状態でも放送を再開
// する（claude 異常停止で画面が固まり続けない）。
func TestSyncGateSafetyValve(t *testing.T) {
	dir := fixtureDir(t)
	data, err := os.ReadFile(filepath.Join(dir, "bytes.bin"))
	if err != nil {
		t.Skipf("fixture 未配置: %v", err)
	}
	b, e := findBigSyncRegion(t, data)
	region := data[b:e]
	p1 := region[:4096]
	p2 := region[4096:8192] // ESU を含まない
	if bytes.Contains(p2, []byte("\x1b[?2026l")) {
		t.Fatal("p2 に ESU が混入（分割位置要調整）")
	}

	// p1 → 1.4s 無音（> syncHoldMax=1s）→ p2（ESU 無し）
	_, sock := startSplitServer(t, [][]byte{p1, p2}, []string{"1.4"})
	c := dial(t, sock)
	defer c.Close()
	d := newDrainer(c)
	if _, err := c.Write(resize(50, 164)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	base := countFrames(d.snapshot())

	time.Sleep(500 * time.Millisecond) // p1 後（保留中）
	if n := countFrames(d.snapshot()); n != base {
		t.Fatalf("保留されるべき p1 で放送: %d != %d", n, base)
	}

	dl := time.Now().Add(3 * time.Second) // p2 は valve 超過後の read
	for countFrames(d.snapshot()) == base && time.Now().Before(dl) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := countFrames(d.snapshot()); n <= base {
		t.Fatalf("安全弁が効いていない（ESU 無しで放送ゼロのまま）: %d", n)
	}
}
