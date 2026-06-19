package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// HOME を temp に向けて CloudsFile() を隔離する。
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// 念のため GOOGLE_APPLICATION_CREDENTIALS をクリア（env fallback の SA）
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	if err := os.MkdirAll(filepath.Join(home, ".claude-master"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

func writeClouds(t *testing.T, cs []Cloud) {
	t.Helper()
	b, _ := json.MarshalIndent(cs, "", "  ")
	if err := os.WriteFile(CloudsFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// clouds.json 不在 → env 単一クラウドにフォールバック（後方互換）。
func TestLoadCloudsEnvFallback(t *testing.T) {
	withTempHome(t)
	// GCP_PROJECT 未設定 → クラウド無し
	cfg := &Config{PCID: "pc1"}
	if cs := cfg.LoadClouds(); len(cs) != 0 {
		t.Fatalf("project 未設定で非空: %v", cs)
	}
	// 単一 env クラウド
	cfg = &Config{PCID: "pc1", GCPProject: "proj-a", CloudRelayURL: "wss://a"}
	cs := cfg.LoadClouds()
	if len(cs) != 1 || cs[0].Project != "proj-a" || cs[0].RelayURL != "wss://a" ||
		cs[0].PCName != "pc1" {
		t.Fatalf("env 単一クラウドが不正: %+v", cs)
	}
}

// env GOOGLE_APPLICATION_CREDENTIALS 不在でも、既定 sa.json があれば
// それを SAKeyPath に seed する（enroll 対話シェルで env 不在でも 1 つ目
// クラウドの鍵参照が空にならず、2 つ目追加で 1 つ目が壊れない）。
func TestLoadCloudsSeedsDefaultSAWhenEnvEmpty(t *testing.T) {
	home := withTempHome(t) // GOOGLE_APPLICATION_CREDENTIALS="" 済み
	cfg := &Config{PCID: "pc1", GCPProject: "proj-a", CloudRelayURL: "wss://a"}
	// sa.json 不在 → SAKeyPath 空（フォールバック対象が無い）
	if cs := cfg.LoadClouds(); len(cs) != 1 || cs[0].SAKeyPath != "" {
		t.Fatalf("sa.json 不在で SAKeyPath が空でない: %+v", cs)
	}
	// 既定 sa.json を置く → SAKeyPath がそれに解決
	sa := filepath.Join(home, ".claude-master", "sa.json")
	if err := os.WriteFile(sa, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cs := cfg.LoadClouds()
	if len(cs) != 1 || cs[0].SAKeyPath != sa {
		t.Fatalf("既定 sa.json が seed されない: %+v (want %s)", cs, sa)
	}
	// env が明示されていればそちらを優先
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/explicit/k.json")
	if cs = cfg.LoadClouds(); cs[0].SAKeyPath != "/explicit/k.json" {
		t.Fatalf("env 明示が優先されない: %+v", cs)
	}
}

// clouds.json があればそれを使い、PCName 既定補完・不完全エントリ除外。
func TestLoadCloudsFromFile(t *testing.T) {
	withTempHome(t)
	writeClouds(t, []Cloud{
		{Project: "proj-a", RelayURL: "wss://a", SAKeyPath: "/k/a.json"},
		{Project: "proj-b", RelayURL: "wss://b", PCName: "namedpc"},
		{Project: "", RelayURL: "wss://x"}, // 不完全（project 空）→除外
		{Project: "proj-c"},                // 不完全（relay 空）→除外
	})
	cfg := &Config{PCID: "default-pc", GCPProject: "env-proj", CloudRelayURL: "wss://env"}
	cs := cfg.LoadClouds()
	if len(cs) != 2 {
		t.Fatalf("有効エントリ数が想定外: %d (%+v)", len(cs), cs)
	}
	if cs[0].Project != "proj-a" || cs[0].PCName != "default-pc" {
		t.Fatalf("PCName 既定補完が効いていない: %+v", cs[0])
	}
	if cs[1].PCName != "namedpc" {
		t.Fatalf("明示 PCName が尊重されない: %+v", cs[1])
	}
	// clouds.json がある時は env 単一クラウドは無視される
	for _, c := range cs {
		if c.Project == "env-proj" {
			t.Fatal("clouds.json 優先のはずが env クラウドが混入")
		}
	}
}

// AppendCloud: 初回は existing を seed、project で dedupe（更新）、原子書込。
func TestAppendCloud(t *testing.T) {
	withTempHome(t)
	cfg := &Config{PCID: "pc1"}
	envCloud := Cloud{Project: "proj-a", RelayURL: "wss://a",
		SAKeyPath: "/k/sa.json", PCName: "pc1"}

	// 初回: clouds.json 無し → existing(env 単一) を seed しつつ新規追加
	add := Cloud{Project: "proj-b", RelayURL: "wss://b",
		SAKeyPath: "/k/sa-b.json", PCName: "pc1"}
	if err := cfg.AppendCloud(add, []Cloud{envCloud}); err != nil {
		t.Fatal(err)
	}
	cs := cfg.LoadClouds()
	if len(cs) != 2 || !hasProj(cs, "proj-a") || !hasProj(cs, "proj-b") {
		t.Fatalf("seed+追加が不正: %+v", cs)
	}

	// 既存 project の再追加 → 上書き更新（件数不変・relay 更新）
	upd := Cloud{Project: "proj-b", RelayURL: "wss://b2",
		SAKeyPath: "/k/sa-b2.json", PCName: "pc1"}
	if err := cfg.AppendCloud(upd, nil); err != nil {
		t.Fatal(err)
	}
	cs = cfg.LoadClouds()
	if len(cs) != 2 {
		t.Fatalf("dedupe されず増えた: %+v", cs)
	}
	for _, c := range cs {
		if c.Project == "proj-b" && c.RelayURL != "wss://b2" {
			t.Fatalf("既存 project が更新されていない: %+v", c)
		}
		if c.Project == "proj-a" && c.RelayURL != "wss://a" {
			t.Fatalf("他クラウドが壊れた: %+v", c)
		}
	}

	// 3 つ目を追加（clouds.json 既存 → seed 不要・そのまま追記）
	if err := cfg.AppendCloud(Cloud{Project: "proj-c", RelayURL: "wss://c",
		PCName: "pc1"}, nil); err != nil {
		t.Fatal(err)
	}
	if cs = cfg.LoadClouds(); len(cs) != 3 {
		t.Fatalf("3 つ目追加が不正: %+v", cs)
	}
}

func hasProj(cs []Cloud, p string) bool {
	for _, c := range cs {
		if c.Project == p {
			return true
		}
	}
	return false
}
