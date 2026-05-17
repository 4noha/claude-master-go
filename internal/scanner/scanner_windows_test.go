//go:build windows

package scanner

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// 共有純パーサが Windows でも正しく動くこと（exact-match・非
// ヒューリスティック）。
func TestWinClaudeBaseAndExtractSessionID(t *testing.T) {
	if !winClaudeBase(`C:\Users\x\AppData\Local\claude.exe`) {
		t.Fatal(`claude.exe を claude と判定できない`)
	}
	if !winClaudeBase(`claude.cmd`) {
		t.Fatal(`claude.cmd を claude と判定できない`)
	}
	if winClaudeBase(`C:\Program Files\nodejs\node.exe`) {
		t.Fatal(`node.exe を claude と誤判定`)
	}
	uuid := "1a2b3c4d-1111-2222-3333-444455556666"
	if got := extractSessionID([]string{`C:\x\claude.exe`, "--resume", uuid}); got != uuid {
		t.Fatalf("extractSessionID windows: got %q want %q", got, uuid)
	}
	if got := extractSessionID([]string{`claude.exe`, "--resume", "notauuid"}); got != "" {
		t.Fatalf("非 UUID を session id 化: %q", got)
	}
}

// 実 claude on Windows の実 argv 形（引用符付きフルパス）回帰。
// CIM の CommandLine は claude ランチャにより `"C:\...\claude.exe"
// --resume <uuid>` と明示引用符付きで出る。旧 strings.Fields だと
// fields[0] に引用符が残り winClaudeBase が false になった（M8d
// follow-up の実バグ）。splitWinCmdline で正しく分割されること。
func TestSplitWinCmdlineQuotedRealClaudeArgv(t *testing.T) {
	cases := []struct {
		name, cmdline, wantBase, wantSID string
	}{
		{
			"quoted-fullpath-resume",
			`"C:\Users\nokki\.local\bin\claude.exe" --resume cbb99b1c-69cd-4e92-9660-140b9bb5bfd8`,
			`C:\Users\nokki\.local\bin\claude.exe`,
			"cbb99b1c-69cd-4e92-9660-140b9bb5bfd8",
		},
		{
			"quoted-path-with-spaces",
			`"C:\Program Files\claude\claude.exe" --resume 1a2b3c4d-1111-2222-3333-444455556666`,
			`C:\Program Files\claude\claude.exe`,
			"1a2b3c4d-1111-2222-3333-444455556666",
		},
		{
			"unquoted-nospace",
			`C:\tools\claude.exe --resume 9f8e7d6c-aaaa-bbbb-cccc-ddddeeeeffff`,
			`C:\tools\claude.exe`,
			"9f8e7d6c-aaaa-bbbb-cccc-ddddeeeeffff",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := splitWinCmdline(c.cmdline)
			if len(f) == 0 || f[0] != c.wantBase {
				t.Fatalf("split argv[0]=%q want %q (fields=%q)", f[0], c.wantBase, f)
			}
			if !winClaudeBase(f[0]) {
				t.Fatalf("引用符/空白付き実 argv で claude を検出できない: %q", f[0])
			}
			if got := extractSessionID(f); got != c.wantSID {
				t.Fatalf("session id got=%q want=%q", got, c.wantSID)
			}
		})
	}
}

// 実 CIM(Win32_Process) 列挙＋winClaudeBase で、実際に起動した
// claude 名プロセスを Scan が検出すること（鉄則#2: 合成でなく実
// プロセス・実 CIM）。cmd.exe を <temp>\claude.exe に複製して
// ping で ~9s 生存させる確実な子。
func TestScanDetectsRealClaudeNamedProcess(t *testing.T) {
	src := `C:\Windows\System32\cmd.exe`
	dir := t.TempDir()
	dst := filepath.Join(dir, "claude.exe")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copy cmd.exe→claude.exe: %v", err)
	}
	cmd := exec.Command(dst, "/c", "ping -n 9 127.0.0.1 >NUL")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start claude.exe: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	want := cmd.Process.Pid

	found := false
	dl := time.Now().Add(8 * time.Second)
	for time.Now().Before(dl) {
		ss, err := Scan(false)
		if err != nil {
			t.Fatalf("Scan(CIM Win32_Process): %v", err)
		}
		for _, s := range ss {
			if s.Pid == want {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !found {
		t.Fatalf("実 CIM 列挙で claude 名プロセス(pid=%d)を Scan が検出できない", want)
	}
	t.Logf("実 CIM(Win32_Process)→winClaudeBase OK: pid=%d 検出（Windows）", want)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
