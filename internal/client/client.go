// Package client は PTY プロキシへの接続クライアント。tmux ウィンドウ
// 内で動作し双方向で Claude セッションへ繋ぐ。Python socket_client.py
// の移植。Python の select(2) は Go の 2 goroutine + channel で忠実再現
// （1 イベントずつ処理する挙動も含め同一）。分類器は internal/screen を
// host 側ディスパッチと共有（IsLiveResetKey/ClassifyWheel）。
package client

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/screen"
)

var (
	resizeMagic = []byte{0xff, 0xff}
	scrollMagic = []byte{0xff, 0xfe}
)

const followDy = 32767 // SCROLL_MAGIC で送ると proxy 側 scrollback→0（live 復帰）

var (
	navOnMsg  = []byte("\r\n\x1b[33m[NAV MODE ON — ↑↓/PgUp/PgDn/Home/End/jk でログをスクロール。同じキーで解除]\x1b[0m\r\n")
	navOffMsg = []byte("\r\n\x1b[33m[NAV MODE OFF]\x1b[0m\r\n")
	pgUp      = []byte("\x1b[5~")
	pgDn      = []byte("\x1b[6~")
)

// connW は sock 書き込みを直列化（SIGWINCH ハンドラと主ループが同時に
// 書いてもフレーム（RESIZE/SCROLL マジック）が崩れない。Python は
// signal 割り込みで同等の競合があるが Go では明示直列化＝堅牢化。
// ワイヤ上のバイト列は Python と同一）。
type connW struct {
	mu sync.Mutex
	c  net.Conn
}

func (w *connW) write(b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.c.Write(b)
	return err
}

func clamp16(dy int) int {
	if dy < -32768 {
		return -32768
	}
	if dy > 32767 {
		return 32767
	}
	return dy
}

func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sendScroll は SCROLL_MAGIC + !h(dy)（int16・big-endian。server
// parseClientInput と一致）。
func (w *connW) sendScroll(dy int) error {
	d := clamp16(dy)
	b := append([]byte{}, scrollMagic...)
	b = append(b, byte(uint16(int16(d))>>8), byte(uint16(int16(d))))
	return w.write(b)
}

// sendResize は RESIZE_MAGIC + !HH(rows, cols)（Python _send_resize と
// 同順。server は rows=BE(2:4)/cols=BE(4:6) で読む）。
func (w *connW) sendResize(fd int) {
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		return
	}
	b := append([]byte{}, resizeMagic...)
	b = append(b, byte(uint16(rows)>>8), byte(uint16(rows)),
		byte(uint16(cols)>>8), byte(uint16(cols)))
	_ = w.write(b)
}

// scrollKeys は nav-mode 中のキー → dy（Python _SCROLL_KEYS と同一表。
// Home/End の ±1000000 は送信時 clamp16 で最古/live へ）。
func scrollKeys(cfg *config.Config) map[string]int {
	s, p := cfg.NavScrollStep, cfg.NavPageStep
	return map[string]int{
		"\x1b[A": -s, "k": -s,
		"\x1b[B": s, "j": s,
		"\x1b[5~": -p,
		"\x1b[6~": p,
		"\x1b[H": -1000000, "g": -1000000,
		"\x1b[F": 1000000, "G": 1000000,
	}
}

// Connect は unix socket へ接続（retry 時は最大 30 回・1 秒間隔＝起動
// レース対策。Python _connect 同等。ライブラリなので sys.exit せず error）。
func Connect(sockPath string, retry bool) (net.Conn, error) {
	maxAttempts := 1
	if retry {
		maxAttempts = 30
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		c, err := net.Dial("unix", sockPath)
		if err == nil {
			return c, nil
		}
		lastErr = err
		if attempt+1 < maxAttempts {
			time.Sleep(time.Second)
		}
	}
	return nil, fmt.Errorf("接続失敗: %s（プロキシ未起動の可能性）: %w",
		sockPath, lastErr)
}

// Run は接続して双方向中継する。stdin を raw 化し、nav/pagekey/wheel を
// SCROLL_MAGIC へ、SIGWINCH を RESIZE_MAGIC へ。Python main() と同一規律。
func Run(sockPath string, retry bool, cfg *config.Config) error {
	conn, err := Connect(sockPath, retry)
	if err != nil {
		return err
	}
	defer conn.Close()
	return RunConn(conn, cfg)
}

// RunConn は確立済み conn（unix socket / WSS relay 等）に対して
// socket_client と同一の双方向中継を行う。`cloud attach`（viewer が
// WSS relay 越しに繋ぐ）でも実証済の nav/pagekey/wheel/SCROLL_MAGIC/
// RESIZE をそのまま再利用するための共通本体。
func RunConn(conn net.Conn, cfg *config.Config) error {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode 失敗: %w", err)
	}
	defer term.Restore(fd, old)

	w := &connW{c: conn}
	w.sendResize(fd)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	defer signal.Stop(sig)
	go func() {
		for range sig {
			w.sendResize(fd)
		}
	}()

	// Python の select(2)（stdin / sock を 1 イベントずつ）を 2 reader
	// goroutine + unbuffered channel で忠実再現。
	stdinCh := make(chan []byte)
	sockCh := make(chan []byte)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				stdinCh <- append([]byte{}, buf[:n]...)
			}
			if err != nil {
				close(stdinCh)
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				sockCh <- append([]byte{}, buf[:n]...)
			}
			if err != nil {
				close(sockCh)
				return
			}
		}
	}()

	keys := scrollKeys(cfg)
	navKey := cfg.NavKey
	navMode := false
	pkScrolled := false

	for {
		select {
		case data, ok := <-stdinCh:
			if !ok {
				return nil // stdin EOF（Python: os.read 失敗で return）
			}
			done, err := processStdin(w, cfg, keys, navKey, data,
				&navMode, &pkScrolled)
			if err != nil {
				return nil // sock 書き込み失敗（Python: return）
			}
			if done {
				return nil
			}
		case data, ok := <-sockCh:
			if !ok || data == nil {
				_, _ = os.Stdout.Write([]byte(
					"\r\n\x1b[33m--- Claude session ended ---\x1b[0m\r\n"))
				return nil
			}
			_, _ = os.Stdout.Write(data)
		}
	}
}

// processStdin は stdin 1 読み取りを Python main() のループ本体と同一
// 判定順で処理。done=true は通常終了、err は sock 書込失敗（return 相当）。
func processStdin(w *connW, cfg *config.Config, keys map[string]int,
	navKey, data []byte, navMode, pkScrolled *bool) (done bool, err error) {

	// WHEEL_SCROLL: nav に入らずホイールで managed scroll（PageUp/Dn 判定
	// より先に消費）。
	if cfg.WheelScroll {
		if wd, ok := screen.ClassifyWheel(data); ok {
			if e := w.sendScroll(wd * mini(32767, cfg.NavWheelStep)); e != nil {
				return false, e
			}
			*pkScrolled = true
			return false, nil
		}
	}
	// PAGEKEY_SCROLL: nav に入らず PageUp/PageDown で managed scroll。
	if cfg.PageKeyScroll && (bytes.Equal(data, pgUp) || bytes.Equal(data, pgDn)) {
		dy := mini(32767, cfg.NavPageStep)
		if bytes.Equal(data, pgUp) {
			dy = -dy
		}
		if e := w.sendScroll(dy); e != nil {
			return false, e
		}
		*pkScrolled = true
		return false, nil
	}
	// ページ/ホイール以外: スクロール中かつ実ユーザー操作なら live 復帰
	// してから（このキー自体は下で通常処理）。passive レポートで誤発火
	// させない。
	if (cfg.PageKeyScroll || cfg.WheelScroll) && *pkScrolled &&
		screen.IsLiveResetKey(data) {
		if e := w.sendScroll(followDy); e != nil {
			return false, e
		}
		*pkScrolled = false
	}
	// NAV_KEY で nav-mode トグル
	if bytes.Contains(data, navKey) {
		*navMode = !*navMode
		if *navMode {
			_, _ = os.Stdout.Write(navOnMsg)
		} else {
			_, _ = os.Stdout.Write(navOffMsg)
			*pkScrolled = false
			if e := w.sendScroll(followDy); e != nil { // nav 解除→live 復帰
				return false, e
			}
		}
		rest := bytes.ReplaceAll(data, navKey, nil)
		if len(rest) > 0 && !*navMode {
			if e := w.write(rest); e != nil {
				return false, e
			}
		}
		return false, nil
	}
	// nav-mode 中: スクロールキーは SCROLL_MAGIC、他は claude へ送らない
	if *navMode {
		if dy, ok := keys[string(data)]; ok {
			if e := w.sendScroll(dy); e != nil {
				return false, e
			}
		}
		return false, nil
	}
	if e := w.write(data); e != nil {
		return false, e
	}
	return false, nil
}
