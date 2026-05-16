package config

import (
	"os"
	"path/filepath"
	"testing"
)

// withConfig は tmp の toml を指し env を掃除して Load() する。
func withConfig(t *testing.T, toml string, env map[string]string) *Config {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"SIZE_POLICY", "NAV_SCROLL_STEP", "NAV_PAGE_STEP",
		"HOST_FLOW_SCROLLBACK", "NAV_KEY", "TMUX_SESSION", "SESSION_LOG"} {
		t.Setenv(k, "") // 一旦セット→下で上書き/Unsetenv
		os.Unsetenv(k)
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	if toml == "" {
		t.Setenv("CLAUDE_MASTER_CONFIG", filepath.Join(dir, "absent.toml"))
	} else {
		p := filepath.Join(dir, "cm.toml")
		if err := os.WriteFile(p, []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CLAUDE_MASTER_CONFIG", p)
	}
	return Load()
}

func TestDefaults(t *testing.T) {
	c := withConfig(t, "", nil)
	if c.SizePolicy != "client" {
		t.Errorf("SizePolicy=%q want client", c.SizePolicy)
	}
	if c.NavScrollStep != 1 || c.NavPageStep != 10 {
		t.Errorf("nav steps = %d/%d want 1/10", c.NavScrollStep, c.NavPageStep)
	}
	if len(c.NavKey) != 1 || c.NavKey[0] != 0x1c {
		t.Errorf("NavKey=%#x want 0x1c", c.NavKey)
	}
	if c.PageKeyScroll || c.WheelScroll || c.HostFlowScrollbck {
		t.Error("flags should default false")
	}
}

func TestFileOverridesDefault(t *testing.T) {
	c := withConfig(t, "size_policy = \"largest\"\nnav_scroll_step = 3\npagekey_scroll = true\n", nil)
	if c.SizePolicy != "largest" || c.NavScrollStep != 3 || !c.PageKeyScroll {
		t.Errorf("file not applied: %+v", c)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	c := withConfig(t, "nav_scroll_step = 3\n", map[string]string{"NAV_SCROLL_STEP": "9"})
	if c.NavScrollStep != 9 {
		t.Errorf("NavScrollStep=%d want 9 (env > file)", c.NavScrollStep)
	}
}

func TestMalformedFileIgnored(t *testing.T) {
	c := withConfig(t, "this is = not [[[ valid toml\n", nil)
	if c.SizePolicy != "client" || c.NavScrollStep != 1 {
		t.Errorf("malformed file should fall back to defaults: %+v", c)
	}
}

func TestTableSection(t *testing.T) {
	c := withConfig(t, "[claude-master]\nsize_policy = \"host\"\nnav_page_step = 4\n", nil)
	if c.SizePolicy != "host" || c.NavPageStep != 4 {
		t.Errorf("[claude-master] table not honored: %+v", c)
	}
}

func TestIntClamp(t *testing.T) {
	c := withConfig(t, "nav_scroll_step = 99999\n", nil)
	if c.NavScrollStep != 1000 {
		t.Errorf("NavScrollStep=%d want clamped 1000", c.NavScrollStep)
	}
	c = withConfig(t, "nav_scroll_step = \"abc\"\n", nil)
	if c.NavScrollStep != 1 {
		t.Errorf("bad int should fall back to default 1, got %d", c.NavScrollStep)
	}
}

func TestParseNavKey(t *testing.T) {
	cases := map[string]byte{
		"":          0x1c,
		`\x1c`:      0x1c,
		"ctrl-]":    0x1d,
		"c-]":       0x1d,
		"^]":        0x1d,
		`\x1d`:      0x1d,
		"0x1d":      0x1d,
		"29":        0x1d,
		"ctrl-g":    0x07,
		"ctrl-\\":   0x1c,
		"g":         'g',
		"badlonger": 0x1c,
	}
	for spec, want := range cases {
		got := ParseNavKey(spec)
		if len(got) != 1 || got[0] != want {
			t.Errorf("ParseNavKey(%q)=%#x want %#x", spec, got, want)
		}
	}
}
