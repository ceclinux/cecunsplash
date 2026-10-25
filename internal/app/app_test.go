package app

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ceclinux/cecunsplash/internal/config"
	"github.com/ceclinux/cecunsplash/internal/unsplash"
)

// jpegBytes builds a w x h JPEG so app.ChangeAll can validate the downloaded image.
func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 9, G: 8, B: 7, A: 255})
		}
	}
	var buf strings.Builder
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 60}); err != nil {
		t.Fatal(err)
	}
	return []byte(buf.String())
}

// fakeUnsplashServer serves /photos/random metadata and a downloadable JPEG.
func fakeUnsplashServer(t *testing.T, img []byte, dimensions [][2]int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/photos/random", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Client-ID test-key" {
			t.Errorf("bad auth header: %q", r.Header.Get("Authorization"))
		}
		photos := make([]unsplash.Photo, 0, len(dimensions))
		for i, d := range dimensions {
			p := unsplash.Photo{
				ID:     fmt.Sprintf("pid%d", i),
				Width:  d[0],
				Height: d[1],
			}
			p.User.Name = "Tester"
			p.User.Username = "tester"
			p.URLs.Raw = "http://" + r.Host + "/raw/pid" + fmt.Sprint(i) + ".jpg"
			p.Links.DownloadLocation = "http://" + r.Host + "/download/pid" + fmt.Sprint(i)
			photos = append(photos, p)
		}
		// Also include a too-small photo to exercise the skip path.
		small := unsplash.Photo{
			ID:     "small",
			Width:  100,
			Height: 100,
		}
		small.URLs.Raw = "http://" + r.Host + "/raw/small.jpg"
		photos = append(photos, small)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(photos)
	})
	mux.HandleFunc("/raw/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(img)
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	return srv
}

func TestFailureBackoff(t *testing.T) {
	a := New(config.Config{}, log.New(io.Discard, "", 0))

	if wait := a.failureRetryIn(time.Minute); wait != 0 {
		t.Errorf("failureRetryIn before any failure = %v, want 0", wait)
	}

	a.markFailure()
	wait := a.failureRetryIn(time.Minute)
	if wait <= 0 || wait > time.Minute {
		t.Errorf("failureRetryIn after failure = %v, want (0, 1m]", wait)
	}

	a.clearFailure()
	if wait := a.failureRetryIn(time.Minute); wait != 0 {
		t.Errorf("failureRetryIn after clearFailure = %v, want 0", wait)
	}
}

func TestChangeAllRejectsUnsuitablePhotos(t *testing.T) {
	img := jpegBytes(t, 3840, 2160)
	// Every served photo is smaller than the configured minimum.
	srv := fakeUnsplashServer(t, img, [][2]int{{1000, 1000}})
	defer srv.Close()

	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("UNSPLASH_ACCESS_KEY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")

	cfg := config.Default()
	cfg.UnsplashAccessKey = "test-key"
	cfg.WallpaperDir = filepath.Join(root, "cache", "cecunsplash", "wallpapers")
	cfg.ShortcutEnabled = false

	app := New(cfg, log.New(io.Discard, "", 0))
	client := unsplash.New("test-key")
	client.HTTP = srv.Client()
	client.SetBaseURL(srv.URL)
	app.SetClient(client)
	app.WaitForNetworkFn = func(context.Context, *log.Logger) error { return nil }

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := app.ChangeAll(ctx)
	if err == nil {
		t.Fatal("expected ChangeAll to fail when no suitable photos are returned")
	}
	if !strings.Contains(err.Error(), "suitable images") {
		t.Errorf("unexpected error: %v", err)
	}
}
