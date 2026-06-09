package app

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ceclinux/cecunsplash/internal/config"
	"github.com/ceclinux/cecunsplash/internal/hotkey"
	"github.com/ceclinux/cecunsplash/internal/unsplash"
	"github.com/ceclinux/cecunsplash/internal/wallpaper"
)

type App struct {
	Config config.Config
	Client *unsplash.Client
	Logger *log.Logger

	mu      sync.Mutex
	running bool
}

func New(cfg config.Config, logger *log.Logger) *App {
	return &App{
		Config: cfg,
		Client: unsplash.New(cfg.UnsplashAccessKey),
		Logger: logger,
	}
}

func (a *App) ChangeAll(ctx context.Context) error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return fmt.Errorf("wallpaper change is already running")
	}
	a.running = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.running = false
		a.mu.Unlock()
	}()

	if err := a.Config.Validate(); err != nil {
		return err
	}
	if err := WaitForNetwork(ctx, a.Logger); err != nil {
		return err
	}

	desktops, err := wallpaper.CountDesktops(ctx)
	if err != nil {
		return err
	}
	if desktops < 1 {
		desktops = 1
	}
	a.logf("found %d desktop workspace(s)", desktops)

	state := LoadState()
	photos, err := a.fetchEnoughPhotos(ctx, desktops, state)
	if err != nil {
		return err
	}
	stagingDir := filepath.Join(a.Config.WallpaperDir, ".current-"+time.Now().Format("20060102-150405"))
	applied := false
	defer func() {
		if !applied {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	paths := make([]string, 0, len(photos))
	for i, p := range photos {
		path, err := a.Client.Download(ctx, p, stagingDir, a.Config.MinWidth, a.Config.MinHeight, i)
		if err != nil {
			return err
		}
		a.logf("downloaded %s by %s to %s", p.ID, p.User.Name, path)
		paths = append(paths, path)
	}

	if err := wallpaper.SetDesktops(ctx, paths); err != nil {
		return err
	}
	applied = true
	if err := cleanWallpaperCache(a.Config.WallpaperDir, paths); err != nil {
		a.logf("cache cleanup failed: %v", err)
	}

	saved := make([]unsplash.SavedPhoto, 0, len(photos))
	for i, p := range photos {
		saved = append(saved, unsplash.SavedPhoto{Photo: p, Path: paths[i]})
	}
	history := updatedPhotoHistory(state, photos)
	state.LastChangedAt = time.Now()
	state.Photos = saved
	state.UsedPhotoIDs = history
	_ = SaveState(state)
	a.logf("updated %d workspace wallpaper(s)", desktops)
	return nil
}

func cleanWallpaperCache(dir string, keepPaths []string) error {
	if len(keepPaths) == 0 || dir == "" {
		return nil
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	keepRoots := map[string]bool{}
	for _, path := range keepPaths {
		absPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absDir, absPath)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
			continue
		}
		root := strings.Split(rel, string(os.PathSeparator))[0]
		if root != "" && root != "." {
			keepRoots[root] = true
		}
	}

	for _, entry := range entries {
		if keepRoots[entry.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(absDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

const maxPhotoHistory = 500

func usedPhotoIDSet(state State) map[string]bool {
	used := make(map[string]bool, len(state.UsedPhotoIDs)+len(state.Photos))
	for _, id := range state.UsedPhotoIDs {
		if id != "" {
			used[id] = true
		}
	}
	// Backward-compatible migration for state files written before used_photo_ids existed.
	for _, saved := range state.Photos {
		if saved.Photo.ID != "" {
			used[saved.Photo.ID] = true
		}
	}
	return used
}

func updatedPhotoHistory(state State, photos []unsplash.Photo) []string {
	history := make([]string, 0, maxPhotoHistory)
	seen := map[string]bool{}
	appendID := func(id string) {
		if id == "" || seen[id] || len(history) >= maxPhotoHistory {
			return
		}
		seen[id] = true
		history = append(history, id)
	}

	for _, photo := range photos {
		appendID(photo.ID)
	}
	for _, id := range state.UsedPhotoIDs {
		appendID(id)
	}
	for _, saved := range state.Photos {
		appendID(saved.Photo.ID)
	}
	return history
}

func (a *App) fetchEnoughPhotos(ctx context.Context, needed int, state State) ([]unsplash.Photo, error) {
	seen := map[string]bool{}
	recentlyUsed := usedPhotoIDSet(state)
	chosen := make([]unsplash.Photo, 0, needed)
	fallback := make([]unsplash.Photo, 0, needed)

	for attempt := 1; len(chosen) < needed && attempt <= 10; attempt++ {
		shortage := needed - len(chosen)
		requestCount := int(math.Min(30, math.Max(float64(shortage*4), 6)))
		photos, err := a.Client.RandomPhotos(ctx, a.Config.Query, a.Config.ContentFilter, requestCount)
		if err != nil {
			return nil, err
		}
		for _, p := range photos {
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			if p.Width < a.Config.MinWidth || p.Height < a.Config.MinHeight {
				a.logf("skip %s (%dx%d does not meet minimum %dx%d)", p.ID, p.Width, p.Height, a.Config.MinWidth, a.Config.MinHeight)
				continue
			}
			if recentlyUsed[p.ID] {
				a.logf("skip recently used photo %s", p.ID)
				fallback = append(fallback, p)
				continue
			}
			chosen = append(chosen, p)
			if len(chosen) == needed {
				break
			}
		}
	}

	for _, p := range fallback {
		if len(chosen) == needed {
			break
		}
		a.logf("reusing recent photo %s because not enough new suitable images were returned", p.ID)
		chosen = append(chosen, p)
	}
	if len(chosen) < needed {
		return nil, fmt.Errorf("unsplash returned only %d suitable images (%dx%d minimum) for %d workspace(s)", len(chosen), a.Config.MinWidth, a.Config.MinHeight, needed)
	}
	return chosen, nil
}

func (a *App) RunDaemon(ctx context.Context) error {
	if err := a.Config.Validate(); err != nil {
		return err
	}

	requests := make(chan string, 2)
	if a.Config.ShortcutEnabled {
		hotkeyEvents := make(chan struct{}, 1)
		if err := hotkey.Register(hotkeyEvents, a.Config.Shortcut); err != nil {
			a.logf("hotkey disabled: %v", err)
		} else {
			defer hotkey.Stop()
			a.logf("registered shortcut %s", hotkey.Normalize(a.Config.Shortcut))
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case <-hotkeyEvents:
						select {
						case requests <- "shortcut":
						default:
							a.logf("shortcut ignored; a change is already queued")
						}
					}
				}
			}()
		}
	}

	go a.scheduleLoop(ctx, requests)

	for {
		select {
		case <-ctx.Done():
			return nil
		case reason := <-requests:
			if reason != "shortcut" && sameLocalDate(LoadState().LastChangedAt, time.Now()) {
				a.logf("skipping %s; wallpaper already changed today", reason)
				continue
			}
			a.logf("starting wallpaper change (%s)", reason)
			changeCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
			err := a.ChangeAll(changeCtx)
			cancel()
			if err != nil {
				a.logf("wallpaper change failed: %v", err)
			} else {
				a.logf("wallpaper change complete")
			}
		}
	}
}

func (a *App) scheduleLoop(ctx context.Context, requests chan<- string) {
	for {
		next, dueNow := a.nextChangeTime(time.Now())
		if dueNow {
			a.queueRequest(ctx, requests, "missed schedule")
		} else {
			a.logf("next scheduled change at %s", next.Format(time.RFC1123))
			timer := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				a.queueRequest(ctx, requests, "schedule")
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func (a *App) nextChangeTime(now time.Time) (time.Time, bool) {
	hour, minute, _ := config.ParseChangeTime(a.Config.ChangeTime)
	today := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	state := LoadState()
	changedToday := sameLocalDate(state.LastChangedAt, now)
	if !changedToday && !now.Before(today) {
		return now, true
	}
	if now.Before(today) {
		return today, false
	}
	return today.Add(24 * time.Hour), false
}

func sameLocalDate(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	a = a.Local()
	b = b.Local()
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func (a *App) queueRequest(ctx context.Context, requests chan<- string, reason string) {
	select {
	case requests <- reason:
	case <-ctx.Done():
	default:
		a.logf("%s ignored; a change is already queued", reason)
	}
}

func WaitForNetwork(ctx context.Context, logger *log.Logger) error {
	client := &http.Client{Timeout: 8 * time.Second}
	first := true
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodHead, apiBaseHealthURL, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
		if first && logger != nil {
			logger.Printf("network unavailable; waiting before changing wallpaper")
			first = false
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Minute):
		}
	}
}

const apiBaseHealthURL = "https://api.unsplash.com/"

func (a *App) logf(format string, args ...any) {
	if a.Logger != nil {
		a.Logger.Printf(format, args...)
	}
}
