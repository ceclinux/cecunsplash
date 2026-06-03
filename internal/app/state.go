package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ceclinux/cecunsplash/internal/config"
	"github.com/ceclinux/cecunsplash/internal/unsplash"
)

type State struct {
	LastChangedAt time.Time             `json:"last_changed_at"`
	Photos        []unsplash.SavedPhoto `json:"photos"`
	UsedPhotoIDs  []string              `json:"used_photo_ids"`
}

func statePath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

func LoadState() State {
	path, err := statePath()
	if err != nil {
		return State{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var st State
	if json.Unmarshal(data, &st) != nil {
		return State{}
	}
	return st
}

func SaveState(st State) error {
	path, err := statePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
