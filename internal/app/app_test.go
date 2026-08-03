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
	"os"
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

func TestChangeAllEndToEndWithFakeServer(t *testing.T) {
	// Build a big-enough JPEG (>= 3840x2160 minimum). The image bytes are tiny
	// (solid color) but the decoder reads the configured width/height from the
	// JPEG header. We need a real 3840x2160 JPEG? That's expensive. Instead we
	// lower the minimum-size requirement via config to 8x6 so the test runs
	// fast and uses modest images, exactly matching what the client validates.
	img := jpegBytes(t, 3840, 2160)

	srv := fakeUnsplashServer(t, img, [][2]int{{3840, 2160}})
	defer srv.Close()

	// Redirect all the per-user dirs into a sandbox tmp tree.
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("UNSPLASH_ACCESS_KEY", "")

	wallpaperBinDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(wallpaperBinDir, 0o755)
	// A stub swaybg records its args so we can prove the image was applied.
	marker := filepath.Join(root, "swaybg-args.txt")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"" + marker + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(wallpaperBinDir, "swaybg"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wallpaperBinDir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	t.Setenv("DISPLAY", "")
	t.Setenv("XDG_CURRENT_DESKTOP", "")
	t.Setenv("NIRI_SOCKET", "")

	cfg := config.Default()
	cfg.UnsplashAccessKey = "test-key"
	cfg.WallpaperDir = filepath.Join(root, "cache", "cecunsplash", "wallpapers")
	cfg.ShortcutEnabled = false // avoid signal handler setup in this test
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	logger := log.New(io.Discard, "", 0)
	app := New(cfg, logger)
	// Point Unsplash client at the fake server.
	client := unsplash.New("test-key")
	client.HTTP = srv.Client()
	// We need the client to talk to the fake server; reuse its exported setter.
	client.SetBaseURL(srv.URL)
	app.SetClient(client)
	// No real network wait: the fake server is the only thing contacted.
	app.WaitForNetworkFn = func(context.Context, *log.Logger) error { return nil }

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := app.ChangeAll(ctx); err != nil {
		t.Fatalf("ChangeAll: %v", err)
	}

	// The wallpaper image should have been downloaded and applied via the stub.
	// swaybg is started detached, so poll briefly for its recorded args.
	var data []byte
	for i := 0; i < 50; i++ {
		if b, err := os.ReadFile(marker); err == nil {
			data = b
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if data == nil {
		t.Fatalf("swaybg stub never invoked")
	}
	args := strings.Join(strings.Fields(string(data)), " ")
	if !strings.Contains(args, "-i ") || !strings.Contains(args, "-m fill") {
		t.Errorf("swaybg invoked with unexpected args: %q", args)
	}
	if !strings.Contains(args, ".jpg") {
		t.Errorf("no jpg path passed to swaybg: %q", args)
	}

	// State file should record the applied photo.
	st := LoadState()
	if len(st.Photos) != 1 {
		t.Fatalf("state photos = %d, want 1", len(st.Photos))
	}
	if st.Photos[0].Photo.ID != "pid0" {
		t.Errorf("state photo id = %q, want pid0", st.Photos[0].Photo.ID)
	}
	if st.Photos[0].Path == "" {
		t.Error("state photo path is empty")
	}

	// The downloaded wallpaper file must exist on disk.
	if _, err := os.Stat(st.Photos[0].Path); err != nil {
		t.Errorf("applied wallpaper file missing: %v", err)
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
	t.Setenv("XDG_CURRENT_DESKTOP", "")
	t.Setenv("NIRI_SOCKET", "")

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
