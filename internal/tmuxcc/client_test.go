//go:build !windows

package tmuxcc

import (
	"bytes"
	"os/exec"
	"testing"
	"time"
)

// TestClient_RealTmuxLifecycle: 実 tmux server に -CC 接続し、handshake
// (begin/end/session-changed) と %%output を受信できることを確認。
// 単純な continuous-output pane を用意し、本実装で実バイトを取れるか。
func TestClient_RealTmuxLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not installed: %v", err)
	}
	sock := "cmtmuxcc-test"
	// cleanup before/after
	cleanup := func() {
		_ = exec.Command("tmux", "-L", sock, "kill-server").Run()
	}
	cleanup()
	defer cleanup()

	// continuous output session を作る
	if err := exec.Command("tmux", "-L", sock,
		"new-session", "-d", "-s", "s", "-x", "80", "-y", "24",
		"sh", "-c",
		`while true; do printf "\033[?2026h\033[31mhello\033[0m\033[?2026l\n"; sleep 0.1; done`,
	).Run(); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	// 少し待つ (session が起動するまで)
	time.Sleep(300 * time.Millisecond)

	c, err := Start(StartOpts{Socket: sock, Session: "s", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	// 2 秒間 msg 収集
	deadline := time.After(2 * time.Second)
	gotSessionChanged := false
	var gotOutputs []*OutputMsg
loop:
	for {
		select {
		case msg, ok := <-c.Events:
			if !ok {
				break loop
			}
			switch m := msg.(type) {
			case *SessionChangedMsg:
				gotSessionChanged = true
			case *OutputMsg:
				gotOutputs = append(gotOutputs, m)
			}
		case <-deadline:
			break loop
		}
	}

	if !gotSessionChanged {
		t.Error("did not receive [pct]session-changed")
	}
	if len(gotOutputs) == 0 {
		t.Error("did not receive any [pct]output")
	}
	// %%output の中身に BSU/ESU が完全保存されているか
	for i, om := range gotOutputs {
		if !bytes.Contains(om.Data, []byte("\x1b[?2026h")) {
			t.Errorf("output[%d] missing BSU: %q", i, om.Data)
		}
		if !bytes.Contains(om.Data, []byte("\x1b[?2026l")) {
			t.Errorf("output[%d] missing ESU: %q", i, om.Data)
		}
		if !bytes.Contains(om.Data, []byte("hello")) {
			t.Errorf("output[%d] missing content: %q", i, om.Data)
		}
	}
	t.Logf("captured %d output msgs, session-changed=%v",
		len(gotOutputs), gotSessionChanged)
}

// TestClient_SendCommand: send-keys コマンド経由でキー入力を pane に
// 送り、その echo が %%output で返ってくることを確認 (input forward の
// 基本動作)。pane は cat (echo back) を実行。
func TestClient_SendCommand(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not installed: %v", err)
	}
	sock := "cmtmuxcc-test2"
	cleanup := func() {
		_ = exec.Command("tmux", "-L", sock, "kill-server").Run()
	}
	cleanup()
	defer cleanup()

	// pane で cat (stdin を echo) を起動
	if err := exec.Command("tmux", "-L", sock,
		"new-session", "-d", "-s", "s", "-x", "80", "-y", "24", "cat",
	).Run(); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	c, err := Start(StartOpts{Socket: sock, Session: "s", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	// handshake 待ち
	gotSession := false
	for !gotSession {
		select {
		case msg, ok := <-c.Events:
			if !ok {
				t.Fatal("client closed before handshake")
			}
			if _, ok := msg.(*SessionChangedMsg); ok {
				gotSession = true
			}
		case <-time.After(1 * time.Second):
			t.Fatal("handshake timeout")
		}
	}

	// send-keys で文字列送信。-l literal で escape 不要文字列。
	// pane が cat なので入力 echo + 改行で出力に出る
	if err := c.Send(`send-keys -t %0 -l "PING"`); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Send(`send-keys -t %0 Enter`); err != nil {
		t.Fatalf("send Enter: %v", err)
	}

	// "PING" の echo を受信
	deadline := time.After(2 * time.Second)
	gotPing := false
	for !gotPing {
		select {
		case msg, ok := <-c.Events:
			if !ok {
				t.Fatal("client closed")
			}
			if om, ok := msg.(*OutputMsg); ok {
				if bytes.Contains(om.Data, []byte("PING")) {
					gotPing = true
				}
			}
		case <-deadline:
			t.Fatal("PING echo timeout")
		}
	}
	t.Log("PING echo received via output msg")
}

// TestClient_QueryReplyWithPercentLine: display-message の応答が "%5" の
// ような % 始まり行でも ReplyLineMsg として届く (応答 block 内は全行が
// 本文)。「active pane の取得に失敗します」の実報告を再現する回帰テスト
// (旧コードは OtherMsg に誤分類して応答が空になった)。
func TestClient_QueryReplyWithPercentLine(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not installed: %v", err)
	}
	sock := "cmtmuxcc-test3"
	cleanup := func() {
		_ = exec.Command("tmux", "-L", sock, "kill-server").Run()
	}
	cleanup()
	defer cleanup()

	if err := exec.Command("tmux", "-L", sock,
		"new-session", "-d", "-s", "s", "-x", "80", "-y", "24", "cat",
	).Run(); err != nil {
		t.Fatalf("new-session: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	c, err := Start(StartOpts{Socket: sock, Session: "s", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()

	// handshake 待ち
	for {
		msg, ok := <-c.Events
		if !ok {
			t.Fatal("closed before handshake")
		}
		if _, yes := msg.(*SessionChangedMsg); yes {
			break
		}
	}

	if err := c.Send("display-message -p '#{pane_id}'"); err != nil {
		t.Fatalf("send: %v", err)
	}

	deadline := time.After(2 * time.Second)
	began := false
	var got string
	for got == "" {
		select {
		case msg, ok := <-c.Events:
			if !ok {
				t.Fatal("closed")
			}
			switch m := msg.(type) {
			case *BeginMsg:
				began = true
			case *ReplyLineMsg:
				if began {
					got = m.Text
				}
			case *EndMsg:
				if began && got == "" {
					t.Fatal("reply block 終了したが ReplyLineMsg が来ない (旧 bug)")
				}
			}
		case <-deadline:
			t.Fatal("reply timeout")
		}
	}
	if got == "" || got[0] != '%' {
		t.Fatalf("pane id 形式でない: %q", got)
	}
	t.Logf("pane id reply: %s", got)
}
