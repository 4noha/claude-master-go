//go:build !manual && !windows

package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/4noha/claude-master-go/internal/cloud/state"
	"github.com/4noha/claude-master-go/internal/screen"
)

// 合成は使わない: 実 Firestore エミュレータ（実 API）＋ローカル WSS
// relay ＋実 PtyProxy（実 resume-burst 録画）で、wake→データ線 open→
// unix socket トンネル→ display-oracle 検証→静止で自動切断、を
// end-to-end 機械検証する。

const projectID = "demo-cm"

func java21Bin() string {
	for _, d := range []string{
		"/opt/homebrew/opt/openjdk/bin",
		"/opt/homebrew/opt/openjdk@25/bin",
		"/opt/homebrew/opt/openjdk@21/bin",
	} {
		j := d + "/java"
		if fi, err := os.Stat(j); err == nil && !fi.IsDir() {
			out, _ := exec.Command(j, "-version").CombinedOutput()
			s := string(out)
			for _, v := range []string{"\"21", "\"22", "\"23", "\"24", "\"25", "\"26"} {
				if strings.Contains(s, v) {
					return d
				}
			}
		}
	}
	return ""
}

func freePort() int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

var emuCmd *exec.Cmd

func TestMain(m *testing.M) {
	jbin := java21Bin()
	if _, err := exec.LookPath("gcloud"); err != nil || jbin == "" {
		fmt.Println("SKIP: gcloud / Java21+ 不在のため Firestore emulator 検証不可")
		os.Exit(0)
	}
	port := freePort()
	host := fmt.Sprintf("127.0.0.1:%d", port)
	emuCmd = exec.Command("gcloud", "beta", "emulators", "firestore", "start",
		"--host-port="+host, "--quiet")
	emuCmd.Env = append(os.Environ(),
		"PATH="+jbin+":"+os.Getenv("PATH"),
		"CLOUDSDK_CORE_DISABLE_PROMPTS=1")
	emuCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := emuCmd.Start(); err != nil {
		fmt.Println("SKIP: emulator 起動不可:", err)
		os.Exit(0)
	}
	ready := false
	for i := 0; i < 80; i++ {
		if c, err := http.Get("http://" + host + "/"); err == nil {
			c.Body.Close()
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		_ = syscall.Kill(-emuCmd.Process.Pid, syscall.SIGKILL)
		fmt.Println("SKIP: emulator が ready にならない")
		os.Exit(0)
	}
	os.Setenv("FIRESTORE_EMULATOR_HOST", host)
	code := m.Run()
	_ = syscall.Kill(-emuCmd.Process.Pid, syscall.SIGKILL)
	os.Exit(code)
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(self), "..", "..", "..",
		"test", "fixtures", "resume-burst")
}

func tmpSock(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("/tmp", "cma*.sock")
	if err != nil {
		t.Fatal(err)
	}
	n := f.Name()
	f.Close()
	os.Remove(n)
	t.Cleanup(func() { os.Remove(n) })
	return n
}

func resizeFrame(rows, cols int) []byte {
	b := []byte{0xff, 0xff}
	var p [4]byte
	binary.BigEndian.PutUint16(p[0:2], uint16(rows))
	binary.BigEndian.PutUint16(p[2:4], uint16(cols))
	return append(b, p[:]...)
}
func scrollFrame(dy int) []byte {
	if dy < -32768 {
		dy = -32768
	}
	b := []byte{0xff, 0xfe}
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], uint16(int16(dy)))
	return append(b, p[:]...)
}

type drain struct {
	mu  sync.Mutex
	buf []byte
}

func (d *drain) run(c net.Conn) {
	go func() {
		b := make([]byte, 8192)
		for {
			n, e := c.Read(b)
			if n > 0 {
				d.mu.Lock()
				d.buf = append(d.buf, b[:n]...)
				d.mu.Unlock()
			}
			if e != nil {
				return
			}
		}
	}()
}
func (d *drain) snap() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(append([]byte{}, d.buf...))
}

func frameText(snap string, cols, rows int) string {
	fr := strings.Split(snap, "\x1b[?2026h")
	if len(fr) < 2 {
		return ""
	}
	v := screen.NewModel(cols, rows)
	v.Feed([]byte(fr[len(fr)-1]))
	return strings.Join(v.VisibleLines(), "\n")
}
func waitText(d *drain, sub string, cols, rows int, to time.Duration) bool {
	dl := time.Now().Add(to)
	for time.Now().Before(dl) {
		if strings.Contains(frameText(d.snap(), cols, rows), sub) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func newState(t *testing.T, pc string) *state.Client {
	t.Helper()
	c, err := state.New(context.Background(), projectID, pc)
	if err != nil {
		t.Fatalf("state.New: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

