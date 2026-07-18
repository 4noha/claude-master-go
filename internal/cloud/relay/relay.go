// Package relay は Cloud Run WSS リレーへの **client / source ブリッジ**。
// NAT 内 PC は wake を受けて **アウトバウンド**で WSS dial し、ローカル unix
// socket（ptyproxy.Server の <pid>.sock）と双方向にバイト透過ポンプする。
// 既存の RESIZE/SCROLL マジック＋画面フレーム protocol を新プロトコルを足さず
// そのまま WSS でトンネルする（coder/websocket の NetConn でストリーム化）。
//
// ⚠ relay サーバ本体（sid で source⇄viewer を突合する byte 透過中継・Google
// ログイン Web UI・認証・デプロイ）は共通リポジトリ **drover-cloud**
// （github.com/4noha/drover-cloud）へ切り出し済み＝本パッケージには client
// （Dial / BridgeSource）だけを残す。稼働中の Cloud Run relay も drover-cloud
// のビルドで動作する。cm はメンテナンス終了（README のお知らせ参照）。
package relay

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

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
