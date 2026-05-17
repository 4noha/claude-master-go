//go:build windows

package selfupdate

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// M8f 実テスト（鉄則#2: 合成でなく実行中の実 .exe を置換）。
// cmd.exe を <tmp>\app.exe に複製し ping で生存させた状態で
// placeBinary を呼び、(1)エラー無し (2)app.exe が新内容 (3)実行中
// イメージが <app.exe>.old へ退避（実行中で削除できず残る）を機械確認。
// 実行中 .exe を直接 rename/上書きできない Windows 制約を Windows
// 流の退避ダンスで解消できていることの実証。
func TestPlaceBinaryReplacesRunningExe(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "app.exe")
	if err := cp(`C:\Windows\System32\cmd.exe`, app); err != nil {
		t.Fatalf("copy cmd.exe→app.exe: %v", err)
	}
	cmd := exec.Command(app, "/c", "ping -n 6 127.0.0.1 >NUL")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start running app.exe: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	time.Sleep(300 * time.Millisecond) // 実行中＝イメージ lock を確実に

	newContent := []byte("M8F-NEW-BINARY-CONTENT")
	tmpName := filepath.Join(dir, ".claude-master-new-xyz")
	if err := os.WriteFile(tmpName, newContent, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := placeBinary(tmpName, app); err != nil {
		t.Fatalf("placeBinary（実行中 exe 置換）失敗: %v", err)
	}

	got, err := os.ReadFile(app)
	if err != nil {
		t.Fatalf("置換後 app.exe 読取: %v", err)
	}
	if string(got) != string(newContent) {
		t.Fatalf("app.exe が新内容でない: %q", string(got))
	}
	if _, err := os.Stat(app + ".old"); err != nil {
		t.Fatalf("実行中イメージの退避先 %s.old が無い（退避ダンス未経由）: %v", app, err)
	}
	t.Logf("実行中 .exe を Windows 退避ダンスで置換 OK: app.exe=新内容・"+
		"%s.old に旧実行中イメージ退避", app)
}

func cp(src, dst string) error {
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
