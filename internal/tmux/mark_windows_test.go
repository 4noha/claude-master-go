//go:build windows

package tmux

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// M8f(2) gate（鉄則#2: 合成でなく実 psmux）。cloud agent の
// remotesync reconcile が依存する marker 機構を psmux 上で実証:
// NewMarkedWindow→MarkedWindows の **marker 往復厳密一致**、同一
// marker 重複窓の列挙（dedupe 自己修復の前提）、KillWindowID 反映。
// psmux 非忠実な @cm_remote を使わず window 名 base32 符号化で実現
// できていることの機械確認。cloud agent test 本体は !windows
// （実 GCP 要＝M8f(3)）なので本テストは tmux 層を自己完結で担保。
func TestPsmuxMarkerRoundTripAndReconcile(t *testing.T) {
	if err := CheckTmux(); err != nil {
		t.Skipf("tmux(psmux) 不在: %v", err)
	}
	sess := "cmtest-m8f-" + strconv.Itoa(os.Getpid())
	m, err := NewManager(sess)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-session", "-t", sess).Run()
	})
	m.EnsureSession()

	live := "cmd /c ping -n 30 127.0.0.1 >NUL" // 窓を試験中生存させる
	mkA := "cloud attach rs1 --pc remoteA"
	mkB := "cloud attach rs2 --pc remoteB=x/y" // '=' 等を含む marker も往復可

	id1 := m.NewMarkedWindow("dirA", live, mkA)
	id2 := m.NewMarkedWindow("dirA", live, mkA) // 同 marker 重複（runaway 相当）
	id3 := m.NewMarkedWindow("dirB", live, mkB)
	if id1 == "" || id2 == "" || id3 == "" || id1 == id2 {
		t.Fatalf("NewMarkedWindow id 異常: %q %q %q", id1, id2, id3)
	}

	mw, err := m.MarkedWindows()
	if err != nil {
		t.Fatalf("MarkedWindows: %v", err)
	}
	// marker 往復厳密一致
	if mw[id1] != mkA || mw[id2] != mkA || mw[id3] != mkB {
		t.Fatalf("marker 往復不一致: id1=%q id2=%q id3=%q (want A=%q B=%q)\nmw=%v",
			mw[id1], mw[id2], mw[id3], mkA, mkB, mw)
	}
	// 同一 marker の重複が別 window_id で 2 本（dedupe 自己修復の前提）
	dupA := 0
	for _, v := range mw {
		if v == mkA {
			dupA++
		}
	}
	if dupA != 2 {
		t.Fatalf("同一 marker 重複が 2 本でない: %d（mw=%v）", dupA, mw)
	}

	// KillWindowID（重複の片方を除去）→ 反映
	m.KillWindowID(id2)
	mw2, err := m.MarkedWindows()
	if err != nil {
		t.Fatalf("MarkedWindows 後: %v", err)
	}
	if _, ok := mw2[id2]; ok {
		t.Fatalf("KillWindowID 後も id2 残存: %v", mw2)
	}
	if mw2[id1] != mkA || mw2[id3] != mkB {
		t.Fatalf("kill 後の残存 marker 不正: %v", mw2)
	}
	t.Logf("psmux marker 往復/dedupe/kill OK（@cm_remote 不使用・window名 "+
		"base32 符号化）: id1=%s id3=%s session=%s", id1, id3, sess)
}

// winName は window_id の現在の窓名を実 psmux から取得（テスト用）。
func winName(sess, id string) string {
	o, _ := exec.Command("tmux", "list-windows", "-t", sess, "-F",
		"#{window_id}=#{window_name}").Output()
	for _, ln := range strings.Split(strings.TrimSpace(string(o)), "\n") {
		k := strings.IndexByte(ln, '=')
		if k >= 0 && ln[:k] == id {
			return ln[k+1:]
		}
	}
	return ""
}

// M8f(2) cosmetic 案 B の実 psmux 検証（鉄則#2）: 窓名が **可読ラベルで
// 始まり** marker は厳密往復、stateless 再構築（新 Manager＝再起動模擬）、
// dedup、旧 `cmr1_<b32>` 単体名の後方互換、を機械確認。
func TestPsmuxReadableNameOptionB(t *testing.T) {
	if err := CheckTmux(); err != nil {
		t.Skipf("tmux(psmux) 不在: %v", err)
	}
	sess := "cmtest-m8fb-" + strconv.Itoa(os.Getpid())
	m, err := NewManager(sess)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", sess).Run() })
	m.EnsureSession()

	live := "cmd /c ping -n 30 127.0.0.1 >NUL"
	mkA := "cloud attach d20200d1-5501-4cc6-b1aa-67223cbb4809 --pc Mac-Studio"

	id1 := m.NewMarkedWindow("worktree", live, mkA)
	if id1 == "" {
		t.Fatal("NewMarkedWindow id 空")
	}
	nm := winName(sess, id1)
	if !strings.HasPrefix(nm, "worktree") || strings.HasPrefix(nm, winMarkPrefix) {
		t.Fatalf("窓名が可読ラベルで始まらない: %q", nm)
	}
	if !strings.Contains(nm, winMarkPrefix) {
		t.Fatalf("符号化トークン無し: %q", nm)
	}
	mw, err := m.MarkedWindows()
	if err != nil || mw[id1] != mkA {
		t.Fatalf("marker 往復不一致: mw[%s]=%q want %q err=%v", id1, mw[id1], mkA, err)
	}

	id2 := m.NewMarkedWindow("worktree", live, mkA)
	mw, _ = m.MarkedWindows()
	dup := 0
	for _, v := range mw {
		if v == mkA {
			dup++
		}
	}
	if id2 == "" || id1 == id2 || dup != 2 {
		t.Fatalf("dedup 前提崩れ: id1=%s id2=%s dup=%d mw=%v", id1, id2, dup, mw)
	}

	// stateless 再構築: 新 Manager（in-memory 状態無し＝再起動模擬）。
	m2, err := NewManager(sess)
	if err != nil {
		t.Fatalf("NewManager(2): %v", err)
	}
	mw2, err := m2.MarkedWindows()
	if err != nil || mw2[id1] != mkA || mw2[id2] != mkA {
		t.Fatalf("再起動模擬で marker 再構築不可: mw2=%v err=%v", mw2, err)
	}

	// 後方互換: 旧 `cmr1_<b32>` 単体名（ラベル無し）も復号できる。
	mkC := "cloud attach pid-999 --pc OldStyle"
	if _, e := outErr("new-window", "-t", sess, "-n", encMarkerToken(mkC), live); e != nil {
		t.Fatalf("旧形式窓 作成: %v", e)
	}
	mw3, _ := m2.MarkedWindows()
	found := false
	for _, v := range mw3 {
		if v == mkC {
			found = true
		}
	}
	if !found {
		t.Fatalf("旧形式 cmr1_ 単体名が復号されない: %v", mw3)
	}

	t.Logf("案B OK: 窓名=%q（可読先頭＋cmr1_末尾）/ 厳密往復 / dedup=2 / "+
		"再起動模擬 stateless 復元 / 旧形式後方互換", nm)
}
