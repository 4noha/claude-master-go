package agent

// restart-proxy オーケストレーションの決定論検証（実プロセス kill/spawn
// は seam で注入＝起こさない）。claude --resume 自体の挙動は ptyproxy の
// resume-burst fixture+display-oracle で別途担保済。

import (
	"context"
	"errors"
	"testing"
)

func TestIsResumable(t *testing.T) {
	ok := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"0a1b2c3d-4e5f-1111-2222-333344445555",
	}
	ng := []string{"pid-1234", "", "abc", "zzzzzzzz-0000-",
		"550E8400-E29B-..." /* 大文字は claude 形でない */}
	for _, s := range ok {
		if !IsResumable(s) {
			t.Errorf("UUID 鍵が復帰可能と判定されない: %q", s)
		}
	}
	for _, s := range ng {
		if IsResumable(s) {
			t.Errorf("非 UUID が復帰可能と誤判定: %q", s)
		}
	}
}

func TestProxyRestarterOrchestration(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"

	t.Run("非UUIDは kill/spawn せず拒否", func(t *testing.T) {
		killed, spawned := 0, 0
		pr := &ProxyRestarter{
			Lookup: func(string) (int, string, bool) { return 1, "/x", true },
			Kill:   func(int) error { killed++; return nil },
			Spawn:  func(string, string) error { spawned++; return nil },
		}
		if err := pr.Restart(context.Background(), "pid-99"); err == nil {
			t.Fatal("非 UUID が拒否されない（無関係プロセス kill リスク）")
		}
		if killed != 0 || spawned != 0 {
			t.Fatalf("非 UUID で副作用: kill=%d spawn=%d", killed, spawned)
		}
	})

	t.Run("未発見は kill/spawn せず拒否", func(t *testing.T) {
		killed := 0
		pr := &ProxyRestarter{
			Lookup: func(string) (int, string, bool) { return 0, "", false },
			Kill:   func(int) error { killed++; return nil },
			Spawn:  func(string, string) error { return nil },
		}
		if err := pr.Restart(context.Background(), uuid); err == nil {
			t.Fatal("未発見セッションが拒否されない")
		}
		if killed != 0 {
			t.Fatalf("未発見で kill 発火: %d", killed)
		}
	})

	t.Run("未配線は拒否", func(t *testing.T) {
		pr := &ProxyRestarter{} // seam 全 nil
		if err := pr.Restart(context.Background(), uuid); err == nil {
			t.Fatal("未配線が拒否されない")
		}
	})

	t.Run("正常: 正しい pid/cwd/sid で kill→spawn", func(t *testing.T) {
		var gotKillPid int
		var gotSid, gotCwd string
		pr := &ProxyRestarter{
			Lookup: func(sid string) (int, string, bool) {
				if sid != uuid {
					t.Fatalf("Lookup に渡る sid 不一致: %q", sid)
				}
				return 4242, "/Users/x/proj", true
			},
			Kill:  func(pid int) error { gotKillPid = pid; return nil },
			Spawn: func(sid, cwd string) error { gotSid, gotCwd = sid, cwd; return nil },
		}
		if err := pr.Restart(context.Background(), uuid); err != nil {
			t.Fatalf("正常系で失敗: %v", err)
		}
		if gotKillPid != 4242 {
			t.Fatalf("kill 対象 pid 不一致: %d", gotKillPid)
		}
		if gotSid != uuid || gotCwd != "/Users/x/proj" {
			t.Fatalf("spawn 引数不一致: sid=%q cwd=%q", gotSid, gotCwd)
		}
	})

	t.Run("kill 失敗なら spawn しない", func(t *testing.T) {
		spawned := 0
		pr := &ProxyRestarter{
			Lookup: func(string) (int, string, bool) { return 7, "/x", true },
			Kill:   func(int) error { return errors.New("perm") },
			Spawn:  func(string, string) error { spawned++; return nil },
		}
		if err := pr.Restart(context.Background(), uuid); err == nil {
			t.Fatal("kill 失敗が伝播しない")
		}
		if spawned != 0 {
			t.Fatalf("kill 失敗後に spawn 発火: %d", spawned)
		}
	})
}
