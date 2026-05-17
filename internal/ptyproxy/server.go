package ptyproxy

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
	"github.com/4noha/claude-master-go/internal/screen"
)

// プロトコル（Python pty_proxy/socket_client と一致）:
//
//	RESIZE_MAGIC = \xff\xff + uint16 rows + uint16 cols (big-endian)
//	SCROLL_MAGIC = \xff\xfe + int16 dy (big-endian)
//	IMAGE_MAGIC  = \xff\xfd + uint32 len(BE) + u8 extCode + payload
//	  extCode: 1=png 2=jpeg 3=gif（webp 等は v1 非対応）
//	  Web の貼付画像を **このホストの OS クリップボードへ載せ**、claude
//	  の pty へ Ctrl+V(0x16) を注入して Claude Code に添付させる
//	  （claude はパス文字列では添付しない＝実機検証で確定。クリップ
//	  ボード/ドラッグのみ）。`WebImagePaste`(既定 off) でオプトイン。
//
// それ以外のバイトは claude への入力として master へ転送。
var (
	resizeMagic = []byte{0xff, 0xff}
	scrollMagic = []byte{0xff, 0xfe}
	imageMagic  = []byte{0xff, 0xfd}
)

// maxImageBytes は IMAGE payload 上限（DoS/disk-fill 防止）。
const maxImageBytes = 8 << 20 // 8 MiB

type client struct {
	conn       net.Conn
	sr         *screen.ScrollRenderer
	cols, rows int
	in         []byte // magic 解析の繰越し
}

// Server は Proxy に unix socket 多重化と per-client 描画を足す
// （Python pty_proxy.py のミニ tmux）。
type Server struct {
	p        *Proxy
	cfg      *config.Config
	mu       sync.Mutex
	clients  map[*client]struct{}
	ln       net.Listener
	host     io.Writer // nil 可（テスト/非対話）
	hostSR   *screen.ScrollRenderer
	hCols    int
	hRows    int
	hostNav  bool // host managed scroll nav-mode（NAV_KEY トグル）
	logsDir  string
	sessFp      *os.File               // SESSION_LOG 出力先（nil=無効）
	sessFlusher *screen.HistoryFlusher // 確定行 capture/drain
	// 使用量ステータス（M5e）: <SessionsDir>/<pid>.status.json
	statusPath  string
	statusPid   int
	statusScan  *screen.StatusScanner
	statusLast  time.Time
	statusSig   string // 直近 payload の usage+active シグネチャ
	statusInit  bool   // 初回は必ず書く（Python の None 比較相当）
	done     chan struct{}
	doneOnce sync.Once
	// setClip は画像をこのホストの OS クリップボードへ載せる（差し替え
	// 可能＝テストで実クリップボードを汚さない seam。既定 macOS impl）。
	setClip func(path, ext string) error
}

func NewServer(p *Proxy, cfg *config.Config, host io.Writer, hostCols, hostRows int) *Server {
	if cfg == nil {
		cfg = config.Load()
	}
	logs := filepath.Join(".", "logs")
	if h, err := os.UserHomeDir(); err == nil {
		logs = filepath.Join(h, ".claude-master", "logs") // Python LOGS_DIR と同じ
	}
	s := &Server{
		p: p, cfg: cfg, clients: map[*client]struct{}{},
		host: host, hostSR: screen.NewScrollRenderer(),
		hCols: hostCols, hRows: hostRows, logsDir: logs,
		done:    make(chan struct{}),
		setClip: setMacClipboardImage,
	}
	return s
}

// Serve は sock を listen し、accept ループと master ポンプを起動。
// 子終了（master EOF）で done を閉じる。
func (s *Server) Serve(sockPath string) error {
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	s.ln = ln
	s.openSessionLog() // SESSION_LOG 有効時のみ（masterPump 前に開く）
	go s.acceptLoop()
	go s.masterPump()
	return nil
}

func (s *Server) Done() <-chan struct{} { return s.done }

// SetClipFunc は画像クリップボード seam を差し替える（テスト/統合での
// 実クリップボード非汚染・受領検証用。既定は setMacClipboardImage）。
// 内部の unexported seam をパッケージ跨ぎ実キーパステストへ最小公開。
func (s *Server) SetClipFunc(fn func(path, ext string) error) {
	if fn != nil {
		s.setClip = fn
	}
}

// SetHostSize は host 端末サイズを更新し即再描画（SIGWINCH 時）。
func (s *Server) SetHostSize(cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hCols, s.hRows = cols, rows
	if s.host != nil {
		_, _ = s.host.Write(s.hostSR.RenderANSI(s.p.VT, s.hRows, s.hCols))
	}
}

func (s *Server) Stop() {
	s.finalizeSessionLog() // 残り確定行＋最終可視画面を書いて閉じる
	s.doneOnce.Do(func() { close(s.done) })
	if s.ln != nil {
		_ = s.ln.Close()
	}
	s.mu.Lock()
	for c := range s.clients {
		_ = c.conn.Close()
	}
	s.mu.Unlock()
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		c := &client{
			conn: conn, sr: screen.NewScrollRenderer(),
			cols: 80, rows: 24, // RESIZE 受信まで既定
		}
		s.mu.Lock()
		s.clients[c] = struct{}{}
		s.renderClientLocked(c) // attach catch-up（現 VT を即送る）
		s.mu.Unlock()
		go s.clientReader(c)
	}
}

func (s *Server) masterPump() {
	buf := make([]byte, 4096)
	for {
		n, err := s.p.master.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.p.VT.Feed(buf[:n])
			s.sessionLogCaptureLocked() // 確定行をファイルへ（描画非依存）
			s.maybeWriteStatusLocked()  // 使用量 status（5s スロットル）
			s.broadcastLocked()
			s.mu.Unlock()
		}
		if err != nil {
			s.Stop()
			return
		}
	}
}

// broadcastLocked は host と全 client を各自サイズで再描画（要 s.mu）。
func (s *Server) broadcastLocked() {
	if s.host != nil {
		_, _ = s.host.Write(s.hostSR.RenderANSI(s.p.VT, s.hRows, s.hCols))
	}
	for c := range s.clients {
		s.renderClientLocked(c)
	}
}

func (s *Server) renderClientLocked(c *client) {
	frame := c.sr.RenderANSI(s.p.VT, c.rows, c.cols)
	_ = c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.conn.Write(frame); err != nil {
		_ = c.conn.Close()
		delete(s.clients, c)
	}
}

func (s *Server) dropClient(c *client) {
	s.mu.Lock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		_ = c.conn.Close()
	}
	s.mu.Unlock()
}

func (s *Server) clientReader(c *client) {
	rb := make([]byte, 4096)
	for {
		n, err := c.conn.Read(rb)
		if n > 0 {
			c.in = append(c.in, rb[:n]...)
			s.parseClientInput(c)
		}
		if err != nil {
			s.dropClient(c)
			return
		}
	}
}

// parseClientInput は in バッファから RESIZE/SCROLL マジックを剥がし、
// 残りを master へ転送（Python pty_proxy._handle_client_data 相当）。
func (s *Server) parseClientInput(c *client) {
	for {
		if len(c.in) >= 2 && c.in[0] == resizeMagic[0] && c.in[1] == resizeMagic[1] {
			if len(c.in) < 6 {
				return // マジック途中（次 read で補完）
			}
			rows := int(binary.BigEndian.Uint16(c.in[2:4]))
			cols := int(binary.BigEndian.Uint16(c.in[4:6]))
			c.in = c.in[6:]
			s.mu.Lock()
			c.rows, c.cols = rows, cols
			s.renderClientLocked(c) // 新サイズで再描画（catch-up）
			s.mu.Unlock()
			continue
		}
		if len(c.in) >= 2 && c.in[0] == scrollMagic[0] && c.in[1] == scrollMagic[1] {
			if len(c.in) < 4 {
				return
			}
			dy := int(int16(binary.BigEndian.Uint16(c.in[2:4])))
			c.in = c.in[4:]
			s.mu.Lock()
			c.sr.Scroll(dy)
			s.renderClientLocked(c) // pan を即反映
			s.mu.Unlock()
			continue
		}
		if len(c.in) >= 2 && c.in[0] == imageMagic[0] && c.in[1] == imageMagic[1] {
			if len(c.in) < 7 {
				return // ヘッダ未着（2 magic+4 len+1 ext）
			}
			n := int(binary.BigEndian.Uint32(c.in[2:6]))
			code := c.in[6]
			if n <= 0 || n > maxImageBytes {
				c.in = c.in[7:] // 不正長は header だけ捨て resync
				continue
			}
			if len(c.in) < 7+n {
				return // payload 未着（次 read で補完）
			}
			payload := append([]byte(nil), c.in[7:7+n]...)
			c.in = c.in[7+n:]
			s.handleImagePaste(payload, code)
			continue
		}
		// マジックでない先頭バイト群は次マジックまで master へ転送
		cut := len(c.in)
		for i := 0; i+1 < len(c.in); i++ {
			if c.in[i] == 0xff && (c.in[i+1] == 0xff ||
				c.in[i+1] == 0xfe || c.in[i+1] == 0xfd) {
				cut = i
				break
			}
		}
		if cut == 0 {
			return
		}
		_, _ = s.p.master.Write(c.in[:cut])
		c.in = c.in[cut:]
		if len(c.in) < 2 {
			return
		}
	}
}

// imageExtClass は extCode→(拡張子, AppleScript クリップボードクラス)。
// macOS osascript の `read … as <class>` 用。webp 等は v1 非対応。
func imageExtClass(code byte) (ext, asClass string, ok bool) {
	switch code {
	case 1:
		return "png", "«class PNGf»", true
	case 2:
		return "jpg", "«class JPEG»", true
	case 3:
		return "gif", "«class GIFf»", true
	}
	return "", "", false
}

// setMacClipboardImage は画像ファイルを macOS の OS クリップボードへ
// 載せる（osascript）。Linux/Win は将来拡張（v1 は macOS 主対象）。
// この seam を差し替えるとテストで実クリップボードを汚さない。
func setMacClipboardImage(path, ext string) error {
	var asClass string
	switch ext {
	case "png":
		asClass = "«class PNGf»"
	case "jpg":
		asClass = "«class JPEG»"
	case "gif":
		asClass = "«class GIFf»"
	default:
		return fmt.Errorf("未対応拡張子: %s", ext)
	}
	scr := fmt.Sprintf("set the clipboard to (read (POSIX file %q) as %s)",
		path, asClass)
	return exec.Command("osascript", "-e", scr).Run()
}

// handleImagePaste は Web から届いた画像を一時ファイル化し、このホスト
// の OS クリップボードへ載せ、claude の pty へ Ctrl+V(0x16) を注入して
// Claude Code に添付させる（パス文字列では添付しない＝実機検証で確定。
// 同じ轍を踏まないこと）。`WebImagePaste` 既定 off のオプトイン。
// 一時ファイルは 0600・固定命名（traversal 不可）・TTL 削除。
func (s *Server) handleImagePaste(payload []byte, code byte) {
	if !s.cfg.WebImagePaste { // 既定 off：完全に従来挙動
		return
	}
	ext, _, ok := imageExtClass(code)
	if !ok || len(payload) == 0 || len(payload) > maxImageBytes {
		return
	}
	dir := filepath.Join(filepath.Dir(s.cfg.SessionsDir), "paste")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	path := filepath.Join(dir, hex.EncodeToString(rnd[:])+"."+ext)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return
	}
	set := s.setClip
	if set == nil {
		set = setMacClipboardImage
	}
	if err := set(path, ext); err != nil {
		_ = os.Remove(path) // クリップボード失敗時は注入しない・残さない
		return
	}
	// Claude Code がホストのクリップボードを読み [Image #N] 添付
	_, _ = s.p.master.Write([]byte{0x16}) // Ctrl+V
	// claude が読み終えた頃に掃除（best-effort）
	time.AfterFunc(5*time.Minute, func() { _ = os.Remove(path) })
}

// ---- host stdin ディスパッチ（Python pty_proxy._handle_host_stdin 移植）----

var (
	hostNavOn  = []byte("\r\n\x1b[33m[NAV MODE ON — ↑↓/PgUp/PgDn/Home/End/jk でログをスクロール。同じキーで解除]\x1b[0m\r\n")
	hostNavOff = []byte("\r\n\x1b[33m[NAV MODE OFF]\x1b[0m\r\n")
	hostPgUp   = []byte("\x1b[5~")
	hostPgDn   = []byte("\x1b[6~")
)

// hostScrollDy は _HOST_SCROLL_KEYS（厳密キー一致）。Python と同一表。
func (s *Server) hostScrollDy(data []byte) (int, bool) {
	hs, hp := s.cfg.NavScrollStep, s.cfg.NavPageStep
	switch string(data) {
	case "\x1b[A", "k":
		return -hs, true
	case "\x1b[B", "j":
		return hs, true
	case "\x1b[5~":
		return -hp, true
	case "\x1b[6~":
		return hp, true
	case "\x1b[H", "g":
		return -1000000, true
	case "\x1b[F", "G":
		return 1000000, true
	}
	return 0, false
}

func (s *Server) renderHostLocked() {
	if s.host == nil {
		return
	}
	_, _ = s.host.Write(s.hostSR.RenderANSI(s.p.VT, s.hRows, s.hCols))
}

func (s *Server) forwardLocked(data []byte) {
	if len(data) > 0 {
		_, _ = s.p.master.Write(data)
	}
}

// hostWheelLocked: WHEEL_SCROLL（nav に入らずホイールで managed scroll）。
// pagekey より先に呼ぶ。raw/flow・無効・非ホイールは false（claude へ透過）。
func (s *Server) hostWheelLocked(data []byte) bool {
	if !s.cfg.WheelScroll || s.cfg.SizePolicy == "host" ||
		(s.cfg.HostFlowScrollbck && s.cfg.SizePolicy != "host") {
		return false
	}
	d, ok := screen.ClassifyWheel(data)
	if !ok {
		return false
	}
	s.hostSR.Scroll(d * s.cfg.NavWheelStep)
	s.renderHostLocked()
	return true
}

// hostPagekeyLocked: PAGEKEY_SCROLL（nav に入らず PgUp/PgDn で managed
// scroll）。ページキー以外は「nav 中でなく scrollback>0 かつ実操作」のとき
// だけ live 復帰してから false（Python と同順序・同条件）。
func (s *Server) hostPagekeyLocked(data []byte) bool {
	if !s.cfg.PageKeyScroll || s.cfg.SizePolicy == "host" ||
		(s.cfg.HostFlowScrollbck && s.cfg.SizePolicy != "host") {
		return false
	}
	if bytes.Equal(data, hostPgUp) {
		s.hostSR.Scroll(-s.cfg.NavPageStep)
		s.renderHostLocked()
		return true
	}
	if bytes.Equal(data, hostPgDn) {
		s.hostSR.Scroll(s.cfg.NavPageStep)
		s.renderHostLocked()
		return true
	}
	if !s.hostNav && s.hostSR.Scrollback() > 0 && screen.IsLiveResetKey(data) {
		s.hostSR.FollowBottom()
		s.renderHostLocked()
	}
	return false
}

// HandleHostInput は host stdin 1 読み取りを Python _handle_host_stdin と
// 同一規律で捌く。判定順は固定仕様: raw/flow 全転送 → wheel → pagekey →
// NAV_KEY トグル → nav-mode スクロール → 通常 claude 転送。順序自体が
// nav/pagekey/wheel 相互作用の仕様（M4b 統合テストで担保）。
func (s *Server) HandleHostInput(data []byte) {
	if len(data) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := s.cfg.SizePolicy == "host"
	flow := s.cfg.HostFlowScrollbck && !raw
	if raw || flow {
		s.forwardLocked(data) // 生パススルー/flow: native scrollback。全転送
		return
	}
	if s.hostWheelLocked(data) {
		return // WHEEL_SCROLL 消費（pagekey より先）
	}
	if s.hostPagekeyLocked(data) {
		return // PAGEKEY_SCROLL 消費
	}
	if bytes.Contains(data, s.cfg.NavKey) {
		s.hostNav = !s.hostNav
		if !s.hostNav {
			s.hostSR.FollowBottom()
		}
		if s.host != nil {
			if s.hostNav {
				_, _ = s.host.Write(hostNavOn)
			} else {
				_, _ = s.host.Write(hostNavOff)
			}
		}
		rest := bytes.ReplaceAll(data, s.cfg.NavKey, nil)
		if len(rest) > 0 && !s.hostNav {
			s.forwardLocked(rest)
		} else if s.hostNav {
			s.renderHostLocked()
		}
		return
	}
	if s.hostNav {
		if dy, ok := s.hostScrollDy(data); ok {
			s.hostSR.Scroll(dy)
			s.renderHostLocked()
		}
		return // nav 中の非スクロールキーは握り潰し（claude へ流さない）
	}
	s.forwardLocked(data)
}
