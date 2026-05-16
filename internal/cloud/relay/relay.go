// Package relay は Cloud Run 上で動く WSS バイト透過リレーと、その
// client/source ブリッジ。NAT 内 PC は wake を受けて **アウトバウンド**
// で WSS dial し、relay が session id で source⇄viewer を突合して
// バイトをそのまま中継する（画面解釈はしない＝不変条件死守）。
//
// 既存の RESIZE/SCROLL マジック＋画面フレーム protocol（unix socket で
// 実証済）を **新プロトコルを足さずそのまま** WSS でトンネルする。
// coder/websocket の NetConn でストリーム化するので、`internal/client`
// や `ptyproxy.Server.parseClientInput` のバイトストリーム処理は無改変。
package relay

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Server は session id ごとに source 1 + viewer 1 を突合し中継する
// （小規模単一インスタンス。多インスタンス化は Pub/Sub fanout を将来）。
type Server struct {
	mu       sync.Mutex
	sessions map[string]*sess
}

type sess struct {
	source net.Conn
	viewer net.Conn
	done   chan struct{}
}

func NewServer() *Server { return &Server{sessions: map[string]*sess{}} }

// ServeHTTP は GET /session?sid=<id>&role=source|viewer を WSS 化。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	role := r.URL.Query().Get("role")
	if sid == "" || (role != "source" && role != "viewer") {
		http.Error(w, "sid と role(source|viewer) が必要", http.StatusBadRequest)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return
	}
	// ctx は接続生存期間。NetConn でバイトストリーム化（WS の message
	// 境界を隠蔽＝既存 protocol を無改変で流せる）。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nc := websocket.NetConn(ctx, c, websocket.MessageBinary)
	s.serve(sid, role, nc)
}

func (s *Server) serve(sid, role string, nc net.Conn) {
	s.mu.Lock()
	se := s.sessions[sid]
	if se == nil {
		se = &sess{done: make(chan struct{})}
		s.sessions[sid] = se
	}
	if role == "source" {
		se.source = nc
	} else {
		se.viewer = nc
	}
	both := se.source != nil && se.viewer != nil
	s.mu.Unlock()

	if both {
		pump(se.source, se.viewer) // どちらか EOF までブロック
		close(se.done)
		s.mu.Lock()
		delete(s.sessions, sid)
		s.mu.Unlock()
		se.source.Close()
		se.viewer.Close()
		return
	}
	// 相手待ち（先着側）。相手到着で pump 完了＝done、無ければ失効。
	select {
	case <-se.done:
	case <-time.After(2 * time.Minute):
	}
	nc.Close()
}

// pump は a⇄b をバイト透過で双方向中継。片方が閉じたら戻る。
func pump(a, b net.Conn) {
	d := make(chan struct{}, 2)
	go func() { io.Copy(a, b); d <- struct{}{} }()
	go func() { io.Copy(b, a); d <- struct{}{} }()
	<-d
}

// idlePump は a⇄b を透過中継しつつ、両方向で idle 秒バイトが流れなければ
// 両 conn を閉じて戻る（= データ線の quiescence 切断）。idle<=0 なら
// 通常 pump と同じ（無期限）。
func idlePump(a, b net.Conn, idle time.Duration) {
	if idle <= 0 {
		pump(a, b)
		return
	}
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	bump := func() { last.Store(time.Now().UnixNano()) }
	d := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				bump()
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		d <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	tick := time.NewTicker(idle / 2)
	defer tick.Stop()
	for {
		select {
		case <-d:
			a.Close()
			b.Close()
			return
		case <-tick.C:
			if time.Since(time.Unix(0, last.Load())) >= idle {
				a.Close() // 静止 → データ線解放
				b.Close()
				<-d
				return
			}
		}
	}
}

// Dial は relay へ WSS 接続して net.Conn（バイトストリーム）を返す。
// baseURL 例: ws://host:port （/session は付けない）。
func Dial(ctx context.Context, baseURL, sid, role string) (net.Conn, error) {
	u := baseURL + "/session?sid=" + sid + "&role=" + role
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{})
	if err != nil {
		return nil, err
	}
	return websocket.NetConn(ctx, c, websocket.MessageBinary), nil
}

// BridgeSource は source PC 側: relay へ source として dial し、ローカル
// unix socket（ptyproxy.Server の <pid>.sock）と双方向ポンプする。
// wake を受けた claude-master が呼ぶ想定（M6c agent が利用）。ctx 終了 /
// どちらか切断で戻る。
func BridgeSource(ctx context.Context, baseURL, sid, unixSock string) error {
	ws, err := Dial(ctx, baseURL, sid, "source")
	if err != nil {
		return err
	}
	defer ws.Close()
	uc, err := net.Dial("unix", unixSock)
	if err != nil {
		return err
	}
	defer uc.Close()
	pump(ws, uc) // unix socket ⇄ WSS をバイト透過
	return nil
}

// BridgeSourceIdle は BridgeSource と同じだが、idle 秒 無通信で
// データ線を閉じて戻る（quiescence 切断＝次の wake まで解放）。
func BridgeSourceIdle(ctx context.Context, baseURL, sid, unixSock string, idle time.Duration) error {
	ws, err := Dial(ctx, baseURL, sid, "source")
	if err != nil {
		return err
	}
	defer ws.Close()
	uc, err := net.Dial("unix", unixSock)
	if err != nil {
		return err
	}
	defer uc.Close()
	idlePump(ws, uc, idle)
	return nil
}
