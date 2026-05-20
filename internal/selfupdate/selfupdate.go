// Package selfupdate は GitHub Releases から最新の静的バイナリを取得し、
// sha256 検証して実行中バイナリを原子的に置換する（`claude-master update`）。
// 依存は標準ライブラリのみ。
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Repo は OWNER/REPO。リリースのホスト先。環境変数 CM_REPO で上書き可。
var Repo = "4noha/claude-master-go"

func repo() string {
	if r := os.Getenv("CM_REPO"); r != "" {
		return r
	}
	return Repo
}

// asset 名は install.sh / install.ps1 / make dist と一致させること:
//   claude-master_<goos>_<goarch>[.exe]
// Windows のみ .exe 拡張子付き（実行可能形式の OS 規約・Makefile dist
// も windows ターゲットは .exe 付きで出す・checksums.txt も同名）。
func assetName() string {
	name := fmt.Sprintf("claude-master_%s_%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

var httpc = &http.Client{Timeout: 60 * time.Second}

// UserAgent は GitHub API/Releases へ送る固有 UA。**UA 未指定は GitHub
// が即 403 で拒否**（規約・実証済）／既定の `Go-http-client/1.1` は IP
// 共有枠のレート(匿名 60/h)を他ツールと食い合うため固有名で分離する。
var UserAgent = "claude-master-go"

// setGHHeaders は GitHub API/Releases へ送る共通ヘッダを付ける。
//
//   - Accept: 公式推奨の application/vnd.github+json（Releases JSON 強制）。
//   - User-Agent: 固有名（無 UA = 403／generic Go UA = 他ツールと共有枠）。
//   - Authorization: GITHUB_TOKEN env があれば Bearer 認証（匿名 60/h →
//     認証 5000/h で実質枯渇しない）。**運用機械への配備推奨**。
func setGHHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", UserAgent)
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// httpErr は non-200 のとき body 先頭 256B を error に含めて診断容易にする
// （旧実装は `403 Forbidden` だけで rate limit / UA 拒否 / 権限を区別不能）。
func httpErr(prefix string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return fmt.Errorf("%s: %s (body=%q)", prefix, resp.Status, body)
}

// LatestTag は GitHub API で最新リリースの tag を返す。
func LatestTag() (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo())
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	setGHHeaders(req)
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", httpErr("github api", resp)
	}
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}
	if r.TagName == "" {
		return "", fmt.Errorf("リリースが見つかりません")
	}
	return r.TagName, nil
}

// dl はリリース asset を取得。GitHub Releases も同じく UA 必須・
// 認証で枠拡張。設定が悪い場合の body も error に含む。
func dl(url string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	setGHHeaders(req)
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, httpErr("download "+url, resp)
	}
	return io.ReadAll(resp.Body)
}

// expectedSHA は checksums.txt（`<sha256>  <name>` 行）から asset の
// 期待ハッシュを取り出す。
func expectedSHA(checksums []byte, name string) (string, bool) {
	for _, ln := range strings.Split(string(checksums), "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && f[1] == name {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// Update は current(=現在の埋め込みバージョン) と最新を比べ、必要なら
// 自分自身を置換する。戻り値 (更新後バージョン, 更新したか, error)。
func Update(current string) (string, bool, error) {
	tag, err := LatestTag()
	if err != nil {
		return "", false, err
	}
	if normalize(tag) == normalize(current) {
		return tag, false, nil // 既に最新
	}
	base := fmt.Sprintf("https://github.com/%s/releases/latest/download", repo())
	name := assetName()
	bin, err := dl(base + "/" + name)
	if err != nil {
		return "", false, err
	}
	sums, err := dl(base + "/checksums.txt")
	if err != nil {
		return "", false, fmt.Errorf("checksums 取得失敗: %w", err)
	}
	want, ok := expectedSHA(sums, name)
	if !ok {
		return "", false, fmt.Errorf("checksums に %s が無い", name)
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != want {
		return "", false, fmt.Errorf("sha256 不一致（破損/改竄の疑い）")
	}
	if err := replaceSelf(bin); err != nil {
		return "", false, err
	}
	return tag, true, nil
}

// replaceSelf は実行中バイナリを新バイナリで原子的に置き換える。
// 同 FS なら rename、跨ぐ場合はコピー。実行中の旧 inode は保持される
// ので安全（次回起動から新版）。
func replaceSelf(newBin []byte) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".claude-master-new-*")
	if err != nil {
		// 書込不可（/usr/local/bin 等）→ 明示エラーで sudo/install.sh 案内
		return fmt.Errorf("%s に書込不可: %w（install.sh 再実行 or sudo）", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(newBin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 原子置換の最終段は OS-split（unix=直接 rename で byte 同一／
	// windows=実行中 exe を退避してから新配置）。
	// place_unix.go / place_windows.go。
	return placeBinary(tmpName, exe)
}

func normalize(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}
