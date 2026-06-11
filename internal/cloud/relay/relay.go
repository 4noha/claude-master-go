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
	// Grant が非 nil なら **公開 /session**（ServeHTTP）接続時に
	// (sid,role) を検証し、false なら 403。認証済 Web /ws は Accept を
	// 直接呼ぶため対象外（無影響）。nil なら従来どおり無認証（テスト/
	// Firestore 無し構成）。本番は state.CheckRelayGrant を注入。
	Grant func(ctx context.Context, sid, role string) bool
}

type sess struct {
	source net.Conn
	viewer net.Conn
	// change は slot 変化（接続/置換/解放）の broadcast。変化のたびに
	// close して張り替える。相手待ちの読み手はこれで起きて現況を再評価。
	change chan struct{}
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
	// 公開 /session の認可: Firestore グラント（SA を持つ正規接続元のみ
	// 書ける短命許可）を検証。Web /ws は Accept 直叩きで通らない。
	if s.Grant != nil && !s.Grant(r.Context(), sid, role) {
		http.Error(w, "未認可（grant 無効）", http.StatusForbidden)
		return
	}
	s.Accept(w, r, sid, role)
}

// Accept は WS をアップグレードして sid/role でペアリング中継する
// （Web の認証済 /ws viewer からも再利用＝relay 本体は無改変）。
func (s *Server) Accept(w http.ResponseWriter, r *http.Request, sid, role string) {
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

// serve は nc を sid の role slot に登録し、**nc の唯一の読み手**として
// 「その瞬間の現役の相手 slot」へ転送する。conn ごとに読み手が 1 つ＝
// 再接続（タブ再読込・コンソール切替・agent 再接続）で旧 conn の pump と
// chunk を奪い合う構造を持たない（旧実装は slot を黙って上書きして第 2
// pump を並走させ、①同じ source を 2 つの io.Copy が read して新 viewer
// の stream に歯抜け＝frame 中間欠落＝表示破壊 ②旧 pump 終了時に現役
// conn まで close ③双方の pump が close(done) を呼び panic、を起こした
// — 2026-06-11 実報告「Web の表示が壊れる」の真因）。
//
// 置換時は旧 conn を close するだけ。旧 conn の読み手は read error で
// 退出し、自分が現役でないことを確認して**相手側には波及させない**。
// 現役のままの自然死だけが従来 semantics（どちらか死で両方を畳む＝
// viewer 死で source も切れ agent が次の wake まで解放）を実行する。
func (s *Server) serve(sid, role string, nc net.Conn) {
	s.mu.Lock()
	se := s.sessions[sid]
	if se == nil {
		se = &sess{change: make(chan struct{})}
		s.sessions[sid] = se
	}
	var old net.Conn
	if role == "source" {
		old, se.source = se.source, nc
	} else {
		old, se.viewer = se.viewer, nc
	}
	close(se.change) // slot 変化を相手待ちへ broadcast
	se.change = make(chan struct{})
	s.mu.Unlock()
	if old != nil {
		old.Close() // 置換: 旧 conn の読み手は read error で退出する
	}

	// 相手が 2 分以内に来なければ自 conn を畳む（従来の先着失効
	// semantics。読み手が nc.Read でブロックしたままでも解放される）。
	loneTimer := time.AfterFunc(2*time.Minute, func() {
		s.mu.Lock()
		lone := s.sessions[sid] == se && s.isCurrentLocked(se, role, nc) &&
			(role == "source" && se.viewer == nil ||
				role == "viewer" && se.source == nil)
		s.mu.Unlock()
		if lone {
			nc.Close()
		}
	})
	defer loneTimer.Stop()

	buf := make([]byte, 32*1024)
	for {
		n, rerr := nc.Read(buf)
		if n > 0 {
			if !s.writePeer(sid, se, role, nc, buf[:n]) {
				break // 置換された／相手が来ない／相手書込不能
			}
		}
		if rerr != nil {
			break
		}
	}

	// 退出: 現役のままの自然死なら相手も畳んで session を解放。置換済み
	// なら何もしない（新 conn が引き継いでいる）。
	s.mu.Lock()
	var peer net.Conn
	if s.sessions[sid] == se && s.isCurrentLocked(se, role, nc) {
		if role == "source" {
			peer = se.viewer
		} else {
			peer = se.source
		}
		delete(s.sessions, sid)
		close(se.change) // 相手待ちを起こして現況再評価させる
		se.change = make(chan struct{})
	}
	s.mu.Unlock()
	nc.Close()
	if peer != nil {
		peer.Close()
	}
}

// isCurrentLocked は nc がまだ role slot の現役か（要 s.mu）。
func (s *Server) isCurrentLocked(se *sess, role string, nc net.Conn) bool {
	if role == "source" {
		return se.source == nc
	}
	return se.viewer == nc
}

// writePeer は相手 slot の現役 conn へ p を書く。相手不在なら到着を最大
// 2 分待つ（従来の先着待ち semantics）。false は「読み手は退出せよ」:
// 自分が置換された／相手が来ない／相手への書込が失敗（相手の読み手が
// 畳む。自分も従来 semantics どおり終了）。
func (s *Server) writePeer(sid string, se *sess, role string, nc net.Conn, p []byte) bool {
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		if s.sessions[sid] != se || !s.isCurrentLocked(se, role, nc) {
			s.mu.Unlock()
			return false // 置換された: 新 conn が現役。黙って退出
		}
		var peer net.Conn
		if role == "source" {
			peer = se.viewer
		} else {
			peer = se.source
		}
		ch := se.change
		s.mu.Unlock()
		if peer != nil {
			_, err := peer.Write(p)
			return err == nil
		}
		select {
		case <-ch: // slot 変化 → 再評価
		case <-deadline.C:
			return false
		}
	}
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
