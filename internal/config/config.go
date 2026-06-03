package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	AppName       = "cecunsplash"
	LaunchAgentID = "com.ceclinux.cecunsplash"
)

type Config struct {
	UnsplashAccessKey string `json:"unsplash_access_key"`
	Query             string `json:"query"`
	ChangeTime        string `json:"change_time"`
	MinWidth          int    `json:"min_width"`
	MinHeight         int    `json:"min_height"`
	WallpaperDir      string `json:"wallpaper_dir"`
	ContentFilter     string `json:"content_filter"`
	ShortcutEnabled   bool   `json:"shortcut_enabled"`
}

func Default() Config {
	return Config{
		Query:           "nature landscape",
		ChangeTime:      "02:00",
		MinWidth:        3840,
		MinHeight:       2160,
		WallpaperDir:    DefaultWallpaperDir(),
		ContentFilter:   "high",
		ShortcutEnabled: true,
	}
}

func DefaultWallpaperDir() string {
	base, err := os.UserCacheDir()
	if err == nil && base != "" {
		return filepath.Join(base, AppName, "wallpapers")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Caches", AppName, "wallpapers")
}

func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, AppName), nil
}

func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func Load() (Config, error) {
	cfg := Default()
	path, err := Path()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.applyEnv()
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	cfg.applyDefaults()
	cfg.applyEnv()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	def := Default()
	if c.Query == "" {
		c.Query = def.Query
	}
	if c.ChangeTime == "" {
		c.ChangeTime = def.ChangeTime
	}
	if c.MinWidth == 0 {
		c.MinWidth = def.MinWidth
	}
	if c.MinHeight == 0 {
		c.MinHeight = def.MinHeight
	}
	if c.WallpaperDir == "" {
		c.WallpaperDir = def.WallpaperDir
	}
	if c.ContentFilter == "" {
		c.ContentFilter = def.ContentFilter
	}
}

func (c *Config) applyEnv() {
	if key := strings.TrimSpace(os.Getenv("UNSPLASH_ACCESS_KEY")); key != "" {
		c.UnsplashAccessKey = key
	}
}

func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	cfg.applyDefaults()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.UnsplashAccessKey) == "" {
		return fmt.Errorf("missing Unsplash access key; set UNSPLASH_ACCESS_KEY or run `%s configure --access-key YOUR_KEY`", AppName)
	}
	if c.MinWidth < 3840 || c.MinHeight < 2160 {
		return fmt.Errorf("minimum size must be at least 3840x2160")
	}
	if _, _, err := ParseChangeTime(c.ChangeTime); err != nil {
		return err
	}
	return nil
}

func ParseChangeTime(s string) (hour, minute int, err error) {
	_, err = fmt.Sscanf(s, "%d:%d", &hour, &minute)
	if err != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid change_time %q, expected HH:MM", s)
	}
	return hour, minute, nil
}
