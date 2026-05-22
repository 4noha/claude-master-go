//go:build !windows

package diag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ListSessionsFromDir は alive PID の snap だけを返し、dead PID と
// 破損 snap は静かに無視する（fail-soft）。
func TestListSessionsFromDirAliveOnly(t *testing.T) {
	dir := t.TempDir()
	mine := os.Getpid()

	// alive (自プロセス) — 残るべき
	writeFullSnap(t, dir, mine, Snap{
		Pid: mine, Cwd: "/tmp/alive-proj", HostOut: 1234567,
		UptimeSec: 42, ConnectedClients: 1,
	})
	// dead — 除外されるべき
	dead := 9999999
	writeFullSnap(t, dir, dead, Snap{
		Pid: dead, Cwd: "/tmp/dead-proj", HostOut: 999,
	})
	// 破損 JSON — fail-soft で skip
	mustWrite(t, filepath.Join(dir, "12345.snap"), `{ not valid json`)
	// PID 形式でない — 無視
	mustWrite(t, filepath.Join(dir, "notes.txt"), `manual`)

	got, err := ListSessionsFromDir(dir)
	if err != nil {
		t.Fatalf("ListSessionsFromDir: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("件数想定外: %d (want 1) result=%+v", len(got), got)
	}
	if got[0].Pid != mine {
		t.Fatalf("PID 想定外: %d (want %d)", got[0].Pid, mine)
	}
	if got[0].Cwd != "/tmp/alive-proj" {
		t.Fatalf("cwd 想定外: %q", got[0].Cwd)
	}
	if got[0].HostOut != 1234567 {
		t.Fatalf("host_out 想定外: %d", got[0].HostOut)
	}
}

// 不在 dir は (nil, nil)＝呼出側 len==0 で「セッション無し」判定可。
func TestListSessionsFromDirMissing(t *testing.T) {
	got, err := ListSessionsFromDir(filepath.Join(t.TempDir(), "no-such"))
	if err != nil {
		t.Fatalf("不在 dir で err: %v", err)
	}
	if got != nil {
		t.Fatalf("不在 dir で nil 期待: %+v", got)
	}
}

// 空 dir は空 slice (or nil)。
func TestListSessionsFromDirEmpty(t *testing.T) {
	got, err := ListSessionsFromDir(t.TempDir())
	if err != nil {
		t.Fatalf("空 dir で err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("空 dir で 0 件 期待: %d", len(got))
	}
}

// PID 昇順ソートを確認（同じ alive PID を 2 つは作れないので、生存
// プロセスを 1 つ + dead を複数 で確認＝1 件しか出ないので、ここでは
// sort の trivial 経路のみ）。
func TestListSessionsSortedByPid(t *testing.T) {
	dir := t.TempDir()
	mine := os.Getpid()
	writeFullSnap(t, dir, mine, Snap{Pid: mine, Cwd: "/x"})
	got, err := ListSessionsFromDir(dir)
	if err != nil || len(got) != 1 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
	// pid 0 で書いてあった場合は ListSessions 側で filename から補正される。
	if got[0].Pid <= 0 {
		t.Fatalf("Pid 補正されてない: %d", got[0].Pid)
	}
}

// snap に "pid" 欠落でも filename PID で補正されることを確認。
func TestListSessionsRecoversPidFromFilename(t *testing.T) {
	dir := t.TempDir()
	mine := os.Getpid()
	// 意図的に "pid" 抜きで書く
	body := `{"cwd":"/from-filename","host_out_bytes":100}`
	mustWrite(t, filepath.Join(dir, strconv.Itoa(mine)+".snap"), body)
	got, err := ListSessionsFromDir(dir)
	if err != nil || len(got) != 1 {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
	if got[0].Pid != mine {
		t.Fatalf("filename から Pid 補正されず: %d (want %d)", got[0].Pid, mine)
	}
	if !strings.Contains(got[0].Cwd, "/from-filename") {
		t.Fatalf("cwd 読めず: %q", got[0].Cwd)
	}
}

func writeFullSnap(t *testing.T, dir string, pid int, s Snap) {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".snap"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}
