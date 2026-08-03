package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	d := Default()
	if d.MinWidth != 3840 || d.MinHeight != 2160 {
		t.Fatalf("default min size = %dx%d, want 3840x2160", d.MinWidth, d.MinHeight)
	}
	if d.ChangeTime != "02:00" {
		t.Fatalf("default change time = %q, want 02:00", d.ChangeTime)
	}
	if d.Query == "" {
		t.Fatal("default query is empty")
	}
	if d.Shortcut == "" {
		t.Fatal("default shortcut is empty")
	}
}

func TestParseChangeTime(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		h, m int
	}{
		{"02:00", true, 2, 0},
		{"23:59", true, 23, 59},
		{"00:00", true, 0, 0},
		{"24:00", false, 0, 0},
		{"02:60", false, 0, 0},
		{"2:3", true, 2, 3},
		{"", false, 0, 0},
		{"abc", false, 0, 0},
	}
	for _, c := range cases {
		h, m, err := ParseChangeTime(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseChangeTime(%q): unexpected err %v", c.in, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("ParseChangeTime(%q): expected error, got %d:%d", c.in, h, m)
			continue
		}
		if c.ok && (h != c.h || m != c.m) {
			t.Errorf("ParseChangeTime(%q): got %d:%d, want %d:%d", c.in, h, m, c.h, c.m)
		}
	}
}

func TestValidate(t *testing.T) {
	good := Default()
	good.UnsplashAccessKey = "abc"
	if err := good.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	noKey := good
	noKey.UnsplashAccessKey = ""
	if err := noKey.Validate(); err == nil {
		t.Fatal("missing access key should fail validation")
	}

	small := good
	small.MinWidth = 1000
	small.MinHeight = 1000
	if err := small.Validate(); err == nil {
		t.Fatal("too-small minimum size should fail validation")
	}

	badTime := good
	badTime.ChangeTime = "99:99"
	if err := badTime.Validate(); err == nil {
		t.Fatal("bad change time should fail validation")
	}

	shortcutOn := good
	shortcutOn.ShortcutEnabled = true
	shortcutOn.Shortcut = ""
	if err := shortcutOn.Validate(); err == nil {
		t.Fatal("empty shortcut with shortcut_enabled should fail validation")
	}

	shortcutOff := good
	shortcutOff.ShortcutEnabled = false
	shortcutOff.Shortcut = ""
	if err := shortcutOff.Validate(); err != nil {
		t.Fatalf("empty shortcut with shortcut disabled should be ok: %v", err)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// Redirect user config dir to the temp dir by setting HOME (UserConfigDir
	// uses os.UserConfigDir which respects XDG_CONFIG_HOME on Linux).
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("HOME", dir)
	t.Setenv("UNSPLASH_ACCESS_KEY", "") // avoid env override

	cfg := Default()
	cfg.UnsplashAccessKey = "roundtrip-key"
	cfg.Query = "test query"
	if err := Save(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.UnsplashAccessKey != "roundtrip-key" {
		t.Errorf("access key = %q, want roundtrip-key", loaded.UnsplashAccessKey)
	}
	if loaded.Query != "test query" {
		t.Errorf("query = %q, want 'test query'", loaded.Query)
	}
	if loaded.MinWidth != 3840 {
		t.Errorf("min width = %d, want 3840", loaded.MinWidth)
	}

	path, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file not written at %s: %v", path, err)
	}
}

func TestApplyEnvOverridesAccessKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("UNSPLASH_ACCESS_KEY", "env-key")
	loaded, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.UnsplashAccessKey != "env-key" {
		t.Errorf("env access key = %q, want env-key", loaded.UnsplashAccessKey)
	}
}
