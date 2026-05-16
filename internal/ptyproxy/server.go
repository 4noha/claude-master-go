package ptyproxy

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

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
	mu       sync.Mutex
	clients  map[*client]struct{}
	ln       net.Listener
	host     io.Writer // nil 可（テスト/非対話）
	hostSR   *screen.ScrollRenderer
	hCols    int
	hRows    int
	done     chan struct{}
	doneOnce sync.Once
}

func NewServer(p *Proxy, host io.Writer, hostCols, hostRows int) *Server {
	return &Server{
		p: p, clients: map[*client]struct{}{},
		host: host, hostSR: screen.NewScrollRenderer(),
		hCols: hostCols, hRows: hostRows,
		done: make(chan struct{}),
	}
}

// Serve は sock を listen し、accept ループと master ポンプを起動。
// 子終了（master EOF）で done を閉じる。
func (s *Server) Serve(sockPath string) error {
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	s.ln = ln
	go s.acceptLoop()
	go s.masterPump()
	return nil
}

func (s *Server) Done() <-chan struct{} { return s.done }

func (s *Server) Stop() {
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
