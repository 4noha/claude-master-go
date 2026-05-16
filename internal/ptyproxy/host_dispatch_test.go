package ptyproxy

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/screen"
)

// M4b: host stdin ディスパッチ統合テスト。Python
// debug/tests/test_host_stdin_dispatch.py に 1:1 対応。
//
// 実 screen.VT に番号付き行 L0000..L0299 を流して history.top を作り
// （= claude が出力してスクロールアウトした状態の忠実再現。合成 ANSI
// ではなく実 VT モデルの履歴）、HandleHostInput を _loop と同一判定順で
// 駆動し、**host scroll viewport を再パースした display-oracle**（内部
// カウンタでなく「画面に何が見えるか」）で:
//   - PAGEKEY/nav の遡りが累積する（1 ステップで頭打ちにならない）
//   - 端末 passive レポート(focus/cursor/mouse)で遡りが消えない
//   - 実ユーザー操作では live 復帰
//   - 遡り中に VT が出力し続けても先頭行がドリフトしない（先頭アンカー）
// を機械検知する。

const (
	dR, dC = 24, 80
	tHS    = 1  // NavScrollStep（Python _HS 既定）
	tHP    = 10 // NavPageStep （Python _HP 既定）
)

var (
	dUP       = []byte("\x1b[A")
	dNAV      = []byte("\x1c")
	dPGUP     = []byte("\x1b[5~")
	dFocusIn  = []byte("\x1b[I")
	dFocusOut = []byte("\x1b[O")
	dCurRep   = []byte("\x1b[24;80R")
	dMouseClk = []byte("\x1b[<0;3;4M")
)

func dispatchCfg(pagekey, wheel bool) *config.Config {
	return &config.Config{
		SizePolicy:    "client",
		NavKey:        []byte{0x1c},
		NavScrollStep: tHS,
		NavPageStep:   tHP,
		NavWheelStep:  3,
		PageKeyScroll: pagekey,
		WheelScroll:   wheel,
	}
}

// dispatchSrv は実 VT(番号行投入済) + host バッファ付き Server を作る。
// master は pipe（forwardLocked の転送先・drain して詰まらせない）。
func dispatchSrv(t *testing.T, cfg *config.Config) (*Server, *bytes.Buffer) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	go func() { // claude 入力先を drain（pty 詰まり防止と同じ役目）
		b := make([]byte, 4096)
		for {
			if _, e := pr.Read(b); e != nil {
				return
			}
		}
	}()
	vt := screen.NewModel(dC, dR)
	lines := make([]string, 300)
	for i := 0; i < 300; i++ {
		lines[i] = "L" + pad4(i)
	}
	vt.Feed([]byte(strings.Join(lines, "\r\n"))) // history.top を作る
	p := &Proxy{master: pw, VT: vt}
	host := &bytes.Buffer{}
	srv := NewServer(p, cfg, &syncBuf{b: host}, dC, dR)
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	return srv, host
}

func pad4(i int) string {
	s := strconv.Itoa(i)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

// topLine は現 host scroll viewport を独立再描画→VT 再パースし先頭の
// 非空行を返す（Python _top: display-oracle）。dispatch 内 render とは
// 独立に毎回新規描画（_step の _top() と同じ）。
func topLine(srv *Server) string {
	frame := srv.hostSR.RenderANSI(srv.p.VT, srv.hRows, srv.hCols)
	d := screen.NewModel(dC, dR)
	d.Feed(frame)
	for _, ln := range d.VisibleLines() {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ""
}

// step は 1 キー dispatch → 先頭行（Python _step）。
func step(srv *Server, key []byte) string {
	srv.HandleHostInput(key)
	return topLine(srv)
}

func lnum(s string) (int, bool) {
	if !strings.HasPrefix(s, "L") || len(s) < 5 {
		return 0, false
	}
	n, err := strconv.Atoi(s[1:5])
	return n, err == nil
}

// 回帰本体: PAGEKEY_SCROLL=on で nav-mode に入り ↑ 連打すると、表示が
// 1 ステップで頭打ちにならず累積して上へ遡る（_loop 判定順依存の仕様）。
func TestNavmodeArrowAccumulatesWithPagekeyOn(t *testing.T) {
	srv, _ := dispatchSrv(t, dispatchCfg(true, false))
	step(srv, dNAV) // nav ON
	if !srv.hostNav {
		t.Fatal("NAV_KEY で nav ON にならない")
	}
	topLine(srv) // 初回 render で max_oy 確定
	var nums []int
	for i := 0; i < 8; i++ {
		s := step(srv, dUP)
		if n, ok := lnum(s); ok {
			nums = append(nums, n)
		}
	}
	if len(nums) < 6 {
		t.Fatalf("L 行が見えていない: %v", nums)
	}
	for i := 1; i < len(nums); i++ {
		if nums[i] > nums[i-1] {
			t.Fatalf("単調に遡っていない: %v", nums)
		}
	}
	if nums[0]-nums[len(nums)-1] < tHS*5 {
		t.Fatalf("1 ステップで頭打ち（累積していない）: %v", nums)
	}
}

// 対照群: PAGEKEY_SCROLL=off でも当然累積。
func TestNavmodeArrowAccumulatesWithPagekeyOff(t *testing.T) {
	srv, _ := dispatchSrv(t, dispatchCfg(false, false))
	step(srv, dNAV)
	topLine(srv)
	var nums []int
	for i := 0; i < 6; i++ {
		if n, ok := lnum(step(srv, dUP)); ok {
			nums = append(nums, n)
		}
	}
	if len(nums) < 4 {
		t.Fatalf("L 行が見えない: %v", nums)
	}
	for i := 1; i < len(nums); i++ {
		if nums[i] > nums[i-1] {
			t.Fatalf("単調に遡っていない: %v", nums)
		}
	}
	if nums[0] <= nums[len(nums)-1] {
		t.Fatalf("累積していない: %v", nums)
	}
}

func TestNavmodeExitReturnsToLive(t *testing.T) {
	srv, _ := dispatchSrv(t, dispatchCfg(true, false))
	step(srv, dNAV)
	topLine(srv)
	for i := 0; i < 5; i++ {
		step(srv, dUP)
	}
	if srv.hostSR.FollowActive() {
		t.Fatal("遡り中なのに follow active のまま")
	}
	step(srv, dNAV) // nav OFF
	if srv.hostNav {
		t.Fatal("nav OFF にならない")
	}
	if !srv.hostSR.FollowActive() {
		t.Fatal("nav OFF で live(follow) 復帰しない")
	}
	top := topLine(srv)
	if !strings.HasPrefix(top, "L02") {
		t.Fatalf("live 復帰で最新付近(L02xx)が見えない: %q", top)
	}
}

// 非 nav で PageUp 遡り中に focus/cursor-report/mouse が来ても表示位置が
// 動かない（戻ると「非 nav scroll が完璧に壊れる」）。
func TestPagekeyNotResetByPassiveReports(t *testing.T) {
	srv, _ := dispatchSrv(t, dispatchCfg(true, false))
	topLine(srv)
	step(srv, dPGUP)
	anchored := step(srv, dPGUP)
	if _, ok := lnum(anchored); !ok {
		t.Fatalf("PageUp 後に L 行が見えない: %q", anchored)
	}
	if srv.hostSR.FollowActive() {
		t.Fatal("PageUp 後に follow active のまま")
	}
	for _, passive := range [][]byte{dFocusIn, dFocusOut, dCurRep, dMouseClk} {
		got := step(srv, passive)
		if got != anchored {
			t.Fatalf("%q で表示位置が動いた=破綻: %q -> %q", passive, anchored, got)
		}
		if srv.hostSR.FollowActive() {
			t.Fatalf("%q で live 復帰してしまった=破綻", passive)
		}
	}
}

// 実ユーザー操作（文字・Enter・矢印・Tab）では live 復帰。
func TestPagekeyResetsOnRealUserInput(t *testing.T) {
	srv, _ := dispatchSrv(t, dispatchCfg(true, false))
	for _, trig := range [][]byte{[]byte("a"), []byte("\r"), dUP, []byte("\t")} {
		srv.hostSR.FollowBottom()
		topLine(srv)
		step(srv, dPGUP)
		if srv.hostSR.FollowActive() {
			t.Fatalf("PageUp 後も follow active: trig=%q", trig)
		}
		step(srv, trig)
		if !srv.hostSR.FollowActive() {
			t.Fatalf("%q で live 復帰しなかった", trig)
		}
	}
}

// 本命: 非 nav で遡って読んでいる間に VT が出力し続けても先頭行が動か
// ない（先頭アンカー）。旧来は最下部基準で canvas が伸びるたびドリフト。
func TestScrolledViewStableWhileVTOutputs(t *testing.T) {
	srv, _ := dispatchSrv(t, dispatchCfg(true, false))
	topLine(srv)
	for i := 0; i < 4; i++ {
		step(srv, dPGUP)
	}
	anchored := topLine(srv)
	if _, ok := lnum(anchored); !ok {
		t.Fatalf("遡り後に L 行が見えない: %q", anchored)
	}
	for k := 300; k < 360; k++ {
		srv.p.VT.Feed([]byte("NEW" + strconv.Itoa(k) + "\r\n"))
		if got := topLine(srv); got != anchored {
			t.Fatalf("VT 出力で表示がドリフト: %q -> %q (k=%d)", anchored, got, k)
		}
	}
	srv.hostSR.FollowBottom()
	frame := srv.hostSR.RenderANSI(srv.p.VT, dR, dC)
	d := screen.NewModel(dC, dR)
	d.Feed(frame)
	if !strings.Contains(strings.Join(d.VisibleLines(), "\n"), "NEW359") {
		t.Fatalf("live 復帰で最新(NEW359)が見えない: %q",
			strings.Join(d.VisibleLines(), "\n"))
	}
}

func TestRawModeForwardsAllNoScroll(t *testing.T) {
	cfg := dispatchCfg(true, false)
	cfg.SizePolicy = "host" // raw passthrough
	srv, _ := dispatchSrv(t, cfg)
	step(srv, dNAV)
	step(srv, dUP)
	if srv.hostNav {
		t.Fatal("raw mode なのに nav に入った")
	}
	if !srv.hostSR.FollowActive() {
		t.Fatal("raw mode で scroll が動いた（全転送のはず）")
	}
}

// syncBuf は host 出力先（並行 render に備えた最小ロック）。
type syncBuf struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
