package client

// session key（UUID or pid-<N>）で sock を STATUS_FILE 経由で解決し、
// socket-client として接続する経路（VSCode 端末タブ継続）。proxy が
// detached spawn で別 PID に restart-proxy 再生成されても、STATUS_FILE
// の key→pid マップを再解決して新 sock へ自動再接続する。proxy 自身は
// 旧来通り detached spawn＝**新バイナリで自動立ち上がる（self-update 反映
// が破壊されない）**＝C 案の核心：proxy 永続化せず attach client を
// 永続化する。
//
// 設計上の判断:
//   - Run の retry は固定 sock path 内の起動レース用（最大 30s）。新
//     sock に切り替えるには外側で再解決が要る＝本ファイルの責務。
//   - 同 key が STATUS_FILE から消えて 10s 続いたら session 完全終了と
//     みなし exit（pid- key の restart-proxy では key が UUID に変わる
//     ため自動 exit する＝V1 既知制約。UUID key で attach すれば不変）。
//   - poll 500ms＝monitor scan 周期（数秒）とのバランス。
//
// Windows でも動く（unix socket は M8c で対応・Go 標準 I/O のみ）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/4noha/claude-master-go/internal/config"
)

// RunByKey は session key から sock を解決し socket-client として接続。
// proxy 死亡（detached spawn による restart-proxy 等）→ 新 sock 自動
// 再接続。key が STATUS_FILE から 10s 消えたら session 終了とみなし nil。
func RunByKey(key string, cfg *config.Config) error {
	if key == "" {
		return fmt.Errorf("attach: session key 必須")
	}
	notFoundSince := time.Time{} // key が STATUS_FILE から消えた起点
	for {
		sock, ok := ResolveSockByKey(cfg, key)
		if !ok {
			if notFoundSince.IsZero() {
				notFoundSince = time.Now()
			} else if time.Since(notFoundSince) > 10*time.Second {
				// 10 秒探しても key 不在＝session 完全終了とみなす
				// （正常 claude exit／restart-proxy で UUID へ key 変化）。
				return nil
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		notFoundSince = time.Time{} // 再解決成功でリセット
		// 既存 Run の retry=true は同 path 内で起動レース耐性（30s）。
		// 切断時はエラー return → 外側で新 sock を再解決して再接続。
		if err := Run(sock, true, cfg); err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		// 正常切断（claude exit / EOF）= attach 終了。
		return nil
	}
}

// ResolveSockByKey は STATUS_FILE の sessions[] から key 一致の pid を
// 引き、<SessionsDir>/<pid>.sock を返す。sock ファイル存在も検証する。
// 引数で渡せる純粋関数として独立＝テスト容易（実 STATUS_FILE/Sessions
// パスを書き換えて検証）。
func ResolveSockByKey(cfg *config.Config, key string) (string, bool) {
	b, err := os.ReadFile(cfg.StatusFile)
	if err != nil {
		return "", false
	}
	var p struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if json.Unmarshal(b, &p) != nil {
		return "", false
	}
	for _, s := range p.Sessions {
		k, _ := s["key"].(string)
		if k != key {
			continue
		}
		pid := pidFromAny(s["pid"])
		if pid <= 0 {
			return "", false
		}
		sock := filepath.Join(cfg.SessionsDir, strconv.Itoa(pid)+".sock")
		if _, err := os.Stat(sock); err == nil {
			return sock, true
		}
		return "", false
	}
	return "", false
}

// pidFromAny は JSON unmarshal 後の pid フィールドを int 化。Go 標準は
// number → float64 にする＝数値経由 cast、文字列フォールバックも対応。
func pidFromAny(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}
