//go:build linux

package wallpaper

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeStub writes an executable shell script named name into dir that records
// its arguments (one per line) into markerPath and then exits 0.
func makeStub(t *testing.T, dir, name, markerPath string) {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"" + markerPath + "\"\n" +
		"# some stubs may be queried for subcommands; handle gsettings list-schemas\n" +
		"if [ \"$1\" = \"list-schemas\" ]; then echo \"org.gnome.desktop.background\"; exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

func tempPathEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+orig)
	return dir
}

func TestCountDesktopsIsOneOnLinux(t *testing.T) {
	n, err := CountDesktops(context.Background())
	if err != nil {
		t.Fatalf("CountDesktops: %v", err)
	}
	if n != 1 {
		t.Errorf("CountDesktops = %d, want 1", n)
	}
}

func TestSwaybgBackendAppliesAndRecordsPid(t *testing.T) {
	dir := tempPathEnv(t)
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	marker := filepath.Join(t.TempDir(), "swaybg-args.txt")
	makeStub(t, dir, "swaybg", marker)

	img := filepath.Join(t.TempDir(), "wall.jpg")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetDesktops(context.Background(), []string{img}); err != nil {
		t.Fatalf("SetDesktops: %v", err)
	}

	// swaybg is started detached; wait briefly for the stub to write its args.
	var args string
	deadline := 30
	for i := 0; i < deadline; i++ {
		if b, err := os.ReadFile(marker); err == nil {
			args = string(b)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if args == "" {
		t.Fatal("swaybg stub was never invoked")
	}
	args = strings.Join(strings.Fields(args), " ")
	if !strings.Contains(args, "-m fill") || !strings.Contains(args, "-i "+img) {
		t.Errorf("swaybg invoked with unexpected args: %q", args)
	}

	pidFile, _ := setterPidPath()
	if _, err := os.Stat(pidFile); err != nil {
		t.Errorf("setter pidfile not written at %s: %v", pidFile, err)
	}
}

func TestGsettingsBackendAppliesURI(t *testing.T) {
	dir := tempPathEnv(t)
	// Force GNOME backend by unsetting wayland env so detection skips Wayland.
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":99")
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	marker := filepath.Join(t.TempDir(), "gs-args.txt")
	// Stub gsettings records every invocation by appending so we can inspect set calls.
	path := filepath.Join(dir, "gsettings")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"list-schemas\" ]; then echo \"org.gnome.desktop.background\"; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" >> \"" + marker + "\"\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	img := filepath.Join(t.TempDir(), "wall.jpg")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetDesktops(context.Background(), []string{img}); err != nil {
		t.Fatalf("SetDesktops: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("gsettings stub not called: %v", err)
	}
	calls := strings.Join(strings.Fields(string(data)), " ")
	if !strings.Contains(calls, "set org.gnome.desktop.background picture-uri") {
		t.Errorf("gsettings did not set picture-uri: %q", calls)
	}
	if !strings.Contains(calls, "picture-uri-dark") {
		t.Errorf("gsettings did not set picture-uri-dark: %q", calls)
	}
	if !strings.Contains(calls, "file://") {
		t.Errorf("gsettings value was not a file URI: %q", calls)
	}
}

func TestFileURIEncoding(t *testing.T) {
	uri, err := fileURI("/tmp/a b/c.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "file:///tmp/") {
		t.Errorf("unexpected uri: %s", uri)
	}
	if !strings.Contains(uri, "%20") {
		t.Errorf("space not percent-encoded in %s", uri)
	}
}

func TestDetectBackendErrorsWhenNothingAvailable(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	// Strip PATH so no tool is found and make gsettings unfindable/unresponsive.
	t.Setenv("PATH", t.TempDir())
	_, err := detectBackend()
	if err == nil {
		t.Fatal("expected detectBackend to fail with no tools available")
	}
	if !strings.Contains(err.Error(), "wallpaper backend") {
		t.Errorf("unexpected error: %v", err)
	}
}
