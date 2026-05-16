package ptyproxy

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
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
//
// それ以外のバイトは claude への入力として master へ転送。
var (
	resizeMagic = []byte{0xff, 0xff}
	scrollMagic = []byte{0xff, 0xfe}
)

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
		done: make(chan struct{}),
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
		// マジックでない先頭バイト群は次マジックまで master へ転送
		cut := len(c.in)
		for i := 0; i+1 < len(c.in); i++ {
			if c.in[i] == 0xff && (c.in[i+1] == 0xff || c.in[i+1] == 0xfe) {
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
