// Package client は PTY プロキシへの接続クライアント。tmux ウィンドウ
// 内で動作し双方向で Claude セッションへ繋ぐ。Python socket_client.py
// の移植。Python の select(2) は Go の 2 goroutine + channel で忠実再現
// （1 イベントずつ処理する挙動も含め同一）。分類器は internal/screen を
// host 側ディスパッチと共有（IsLiveResetKey/ClassifyWheel）。
package client

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
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

	rz, stopRz := watchResize()
	defer stopRz()
	go func() {
		for range rz {
			w.sendResize(fd)
		}
	}()

	return runLoop(os.Stdin, os.Stdout, conn, w, cfg)
}

// runLoop は RunConn のループ本体（stdin/stdout/conn を引数化＝テスト
// 可能）。**重要（回帰修正）**: 端末出力 conn→stdout を **独立 goroutine**
// で汲む。旧実装は単一 select で stdin 処理（processStdin → IMG_PASTE_KEY
// の readClipImage=osascript 同期実行）と出力を結合しており、クリップ
// ボード読取の間ループが回らず画面が固まった（"出力でない/文字化け"）。
// 出力を分離すると stdin/クリップボード遅延が何ms あっても描画は止まら
// ない。Python の「1 イベントずつ」順序は stdin 処理内では維持（出力は
// もともと別ストリームで結合不要＝忠実性を損なわない）。connW.write は
// mutex 直列化済、stdout は本 goroutine 単独 writer で競合なし。
func runLoop(stdin io.Reader, stdout io.Writer, conn net.Conn,
	w *connW, cfg *config.Config) error {

	keys := scrollKeys(cfg)
	navKey := cfg.NavKey
	navMode := false
	pkScrolled := false

	outDone := make(chan struct{})
	go func() { // 出力ポンプ（独立・stdin 処理に絶対ブロックされない）
		defer close(outDone)
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				_, _ = stdout.Write(buf[:n])
			}
			if err != nil {
				_, _ = stdout.Write([]byte(
					"\r\n\x1b[33m--- Claude session ended ---\x1b[0m\r\n"))
				return
			}
		}
	}()

	inDone := make(chan struct{})
	go func() { // 入力ポンプ（processStdin が osascript 等でブロックして
		defer close(inDone) // も出力ポンプは無関係に流れ続ける）
		buf := make([]byte, 1024)
		for {
			n, rerr := stdin.Read(buf)
			if n > 0 {
				done, werr := processStdin(w, cfg, keys, navKey,
					append([]byte{}, buf[:n]...), &navMode, &pkScrolled)
				if werr != nil || done {
					return // sock 書込失敗 / 通常終了（Python: return）
				}
			}
			if rerr != nil {
				return // stdin EOF（Python: os.read 失敗で return）
			}
		}
	}()

	select {
	case <-outDone: // sock 切断＝Claude セッション終了
	case <-inDone: // stdin EOF / 書込失敗 / done
	}
	return nil
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
	// IMG_PASTE_KEY（既定 off。nil=無効）: ローカル機のクリップボードに
	// 画像があれば term.js と同一の IMAGE フレームで送出（サーバが remote
	// クリップボードへ載せ Ctrl+V 注入）。画像が無ければトリガキーを
	// そのまま claude へ素通し＝Ctrl-V 等の通常動作を壊さない。nav-mode
	// 中は上で return 済（読書中は無効）。判定は NAV_KEY と同じ
	// Contains→ReplaceAll パターン。
	if cfg.ImgPasteKey != nil && bytes.Contains(data, cfg.ImgPasteKey) {
		if img, code, ok := readClipImage(); ok {
			if e := w.sendImage(img, code); e != nil {
				return false, e
			}
			rest := bytes.ReplaceAll(data, cfg.ImgPasteKey, nil)
			if len(rest) > 0 {
				if e := w.write(rest); e != nil {
					return false, e
				}
			}
			return false, nil
		}
		// 画像無し: 下の通常転送へフォールスルー（data を素通し）
	}
	if e := w.write(data); e != nil {
		return false, e
	}
	return false, nil
}
