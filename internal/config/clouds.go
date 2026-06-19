package config

// 複数クラウド fan-out 設定。1 台の PC（cloud agent）が **複数の独立した
// クラウド**（別 Google アカウント＝別 GCP プロジェクト/別 relay/別 SA 鍵）
// へ同時にセッションを push し、各々の relay でトンネル/コマンドを受ける
// ための設定リスト。クラウド側は一切改変不要（PC 側 agent のみの機能）。
//
// 優先順位:
//   - ~/.claude-master/clouds.json が存在し非空 → そのリストを使う
//     （primary = 先頭。remote-tmux-sync は primary のみ＝窓衝突回避）
//   - 無ければ従来どおり単一クラウド（env/toml の GCP_PROJECT 等）
//     ＝既存単一クラウド構成は挙動完全不変（後方互換）

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Cloud は 1 つのクラウド接続先。SAKeyPath は **クライアント個別**の SA 鍵
// （プロセス global の GOOGLE_APPLICATION_CREDENTIALS とは別＝複数併存可）。
type Cloud struct {
	Project   string `json:"project"`              // GCP プロジェクト ID
	RelayURL  string `json:"relay_url"`            // Cloud Run relay の wss:// URL
	SAKeyPath string `json:"sa_key_path,omitempty"` // この PC がこのクラウドへ書く SA 鍵（空=ADC）
	PCName    string `json:"pc_name,omitempty"`     // クラウド上の端末名（空=cfg.PCID）
}

// CloudsFile は clouds.json のパス（~/.claude-master/clouds.json）。
func CloudsFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude-master", "clouds.json")
}

// LoadClouds は接続先クラウドのリストを返す。clouds.json があればそれ、
// 無ければ env/toml の単一クラウド（GCP_PROJECT 未設定なら空）。
// 各エントリの PCName 既定は cfg.PCID、SAKeyPath 既定は env の
// GOOGLE_APPLICATION_CREDENTIALS（単一クラウド後方互換）。
func (c *Config) LoadClouds() []Cloud {
	if f := CloudsFile(); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			var cs []Cloud
			if json.Unmarshal(b, &cs) == nil {
				out := cs[:0]
				for _, cl := range cs {
					if cl.Project == "" || cl.RelayURL == "" {
						continue // 不完全エントリは無視
					}
					if cl.PCName == "" {
						cl.PCName = c.PCID
					}
					out = append(out, cl)
				}
				if len(out) > 0 {
					return out
				}
			}
		}
	}
	// 後方互換: env/toml の単一クラウド。
	if c.GCPProject == "" {
		return nil
	}
	return []Cloud{{
		Project:   c.GCPProject,
		RelayURL:  c.CloudRelayURL,
		SAKeyPath: defaultSAKeyPath(),
		PCName:    c.PCID,
	}}
}

// defaultSAKeyPath は単一クラウドの SA 鍵パスを返す。env
// GOOGLE_APPLICATION_CREDENTIALS があればそれ、無ければ従来の既定
// ~/.claude-master/sa.json（存在時）。enroll は対話シェルで実行され
// env が無いことが多く、その時に既存クラウドの鍵参照を空で seed すると
// 2 つ目追加で 1 つ目が ADC 失敗で繋がらなくなる事故を防ぐ。
func defaultSAKeyPath() string {
	if p := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	cand := filepath.Join(home, ".claude-master", "sa.json")
	if _, e := os.Stat(cand); e == nil {
		return cand
	}
	return ""
}

// AppendCloud は clouds.json にクラウドを追記する（project で dedupe＝
// 同 project は上書き更新）。clouds.json が無ければ existing（通常は env
// の単一クラウド）を seed してから追記＝enroll で 2 つ目以降を足しても
// 既存クラウドが消えない。書込は tmp→rename で原子的。
func (c *Config) AppendCloud(add Cloud, existing []Cloud) error {
	f := CloudsFile()
	if f == "" {
		return os.ErrInvalid
	}
	var list []Cloud
	if b, err := os.ReadFile(f); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	if len(list) == 0 {
		list = append(list, existing...) // 初回: env 単一クラウドを seed
	}
	merged := list[:0]
	replaced := false
	for _, cl := range list {
		if cl.Project == add.Project {
			merged = append(merged, add) // 同 project は更新
			replaced = true
			continue
		}
		merged = append(merged, cl)
	}
	if !replaced {
		merged = append(merged, add)
	}
	b, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}
