//go:build linux

package app

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ceclinux/cecunsplash/internal/config"
	"github.com/ceclinux/cecunsplash/internal/unsplash"
)

// TestChangeAllEndToEndWithFakeServer exercises the Linux swaybg backend, so it
// only runs on Linux. The other app tests are platform-neutral.
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
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	t.Setenv("NIRI_SOCKET", "")
	t.Setenv("UNSPLASH_ACCESS_KEY", "")

	wallpaperBinDir := filepath.Join(root, "bin")
	_ = os.MkdirAll(wallpaperBinDir, 0o755)
	// A stub swaybg records its args so we can prove the image was applied.
	marker := filepath.Join(root, "swaybg-args.txt")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"" + marker + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(wallpaperBinDir, "swaybg"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wallpaperBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	t.Setenv("DISPLAY", "")

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
