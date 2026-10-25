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

// makeScript writes an executable script named name into dir.
func makeScript(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
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

func TestCountDesktopsFallsBackToOneWithoutNoctalia(t *testing.T) {
	// Keep the real session's binaries and sockets out of the detection path:
	// without Noctalia there is no per-monitor backend, so one image is enough.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("NIRI_SOCKET", "")
	t.Setenv("XDG_CURRENT_DESKTOP", "niri")

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
	// Keep the real session's sockets out of the detection path.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_CURRENT_DESKTOP", "niri")

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
	// Force GNOME backend by unsetting wayland env and setting a GNOME desktop.
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":99")
	t.Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	// No compositor sockets: force the GNOME fallback path.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

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
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
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

func TestDetectBackendDoesNotUseGsettingsOnNiri(t *testing.T) {
	dir := t.TempDir()
	// Only the gsettings stub is visible, so swaybg/swww/wbg cannot be found.
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	t.Setenv("DISPLAY", ":99")
	t.Setenv("XDG_CURRENT_DESKTOP", "niri")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	makeScript(t, dir, "gsettings", "#!/bin/sh\n"+
		"if [ \"$1\" = \"list-schemas\" ]; then echo \"org.gnome.desktop.background\"; exit 0; fi\n"+
		"exit 0\n")

	_, err := detectBackend()
	if err == nil {
		t.Fatal("expected detectBackend to fail without a real Wayland backend")
	}
	if !strings.Contains(err.Error(), "Install 'swaybg' for niri/Wayland") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNoctaliaRuntimeEnvDerivesDisplayFromSocket(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	// The dmenu socket must be ignored; the shell socket is the one that works.
	for _, name := range []string{"noctalia-dmenu-wayland-1.sock", "noctalia-wayland-3.sock"} {
		if err := os.WriteFile(filepath.Join(runtimeDir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	env := noctaliaRuntimeEnv(nil)
	if len(env) != 1 || env[0] != "WAYLAND_DISPLAY=wayland-3" {
		t.Errorf("noctaliaRuntimeEnv = %v, want [WAYLAND_DISPLAY=wayland-3]", env)
	}
}

func TestNoctaliaRuntimeEnvKeepsExistingDisplay(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-7")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	if env := noctaliaRuntimeEnv(nil); env != nil {
		t.Errorf("noctaliaRuntimeEnv = %v, want nil when WAYLAND_DISPLAY is already set", env)
	}
}

func TestNoctaliaBackendAppliesViaIPC(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("XDG_CURRENT_DESKTOP", "niri")

	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	if err := os.WriteFile(filepath.Join(runtimeDir, "noctalia-wayland-9.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(t.TempDir(), "noctalia-args.txt")
	// Fail unless the derived WAYLAND_DISPLAY reached the CLI.
	makeScript(t, dir, "noctalia", "#!/bin/sh\n"+
		"if [ \"$WAYLAND_DISPLAY\" != \"wayland-9\" ]; then echo \"bad WAYLAND_DISPLAY=$WAYLAND_DISPLAY\" >&2; exit 1; fi\n"+
		"if [ \"$1\" = \"msg\" ] && [ \"$2\" = \"wallpaper-get\" ]; then exit 0; fi\n"+
		"printf '%s\\n' \"$@\" > \""+marker+"\"\n"+
		"exit 0\n")

	swayMarker := filepath.Join(t.TempDir(), "swaybg-args.txt")
	makeScript(t, dir, "swaybg", "#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+swayMarker+"\"\nexit 0\n")

	img := filepath.Join(t.TempDir(), "wall.jpg")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetDesktops(context.Background(), []string{img}); err != nil {
		t.Fatalf("SetDesktops: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("noctalia stub was not called: %v", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 || fields[0] != "msg" || fields[1] != "wallpaper-set" {
		t.Fatalf("unexpected noctalia args: %q", fields)
	}
	if fields[2] != img {
		t.Errorf("noctalia got path %q, want %q", fields[2], img)
	}
	if _, err := os.Stat(swayMarker); err == nil {
		t.Error("swaybg was used even though Noctalia answered IPC")
	}
}

// perMonitorStubs installs a noctalia stub that accepts IPC and a niri stub that
// reports two outputs, then returns the noctalia call marker path.
func perMonitorStubs(t *testing.T) (dir, marker string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	t.Setenv("DISPLAY", "")
	t.Setenv("XDG_CURRENT_DESKTOP", "niri")
	t.Setenv("NIRI_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	marker = filepath.Join(t.TempDir(), "noctalia-args.txt")
	makeScript(t, dir, "noctalia", "#!/bin/sh\n"+
		"if [ \"$1\" = \"msg\" ] && [ \"$2\" = \"wallpaper-get\" ]; then exit 0; fi\n"+
		"printf '%s\\n' \"$@\" >> \""+marker+"\"\n"+
		"exit 0\n")
	makeScript(t, dir, "niri", "#!/bin/sh\n"+
		"echo '{\"eDP-1\":{\"logical\":{\"width\":1920,\"height\":1080}},\"DP-2\":{\"logical\":{\"width\":2560,\"height\":1440}}}'\n"+
		"exit 0\n")
	return dir, marker
}

func TestCountDesktopsCountsNoctaliaOutputs(t *testing.T) {
	perMonitorStubs(t)

	n, err := CountDesktops(context.Background())
	if err != nil {
		t.Fatalf("CountDesktops: %v", err)
	}
	if n != 2 {
		t.Errorf("CountDesktops = %d, want 2 (one per connected output)", n)
	}
}

func TestNiriConnectorsSkipDisabledOutputs(t *testing.T) {
	// niri lists disabled outputs too, with a null logical size. Noctalia does
	// not manage them, so they must not be counted or receive an image.
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("NIRI_SOCKET", "")
	makeScript(t, dir, "noctalia", "#!/bin/sh\nexit 0\n")
	makeScript(t, dir, "niri", "#!/bin/sh\n"+
		"echo '{\"eDP-1\":{\"logical\":null},\"DP-1\":{\"logical\":{\"width\":2560,\"height\":1440}},\"DP-2\":{\"logical\":{\"width\":1920,\"height\":1080}}}'\n"+
		"exit 0\n")

	connectors, err := niriConnectors(context.Background())
	if err != nil {
		t.Fatalf("niriConnectors: %v", err)
	}
	if got := strings.Join(connectors, ","); got != "DP-1,DP-2" {
		t.Errorf("connectors = %q, want %q", got, "DP-1,DP-2")
	}

	n, err := CountDesktops(context.Background())
	if err != nil {
		t.Fatalf("CountDesktops: %v", err)
	}
	if n != 2 {
		t.Errorf("CountDesktops = %d, want 2", n)
	}
}

func TestNoctaliaAppliesOneImagePerMonitor(t *testing.T) {
	_, marker := perMonitorStubs(t)

	first := filepath.Join(t.TempDir(), "a.jpg")
	second := filepath.Join(t.TempDir(), "b.jpg")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := SetDesktops(context.Background(), []string{first, second}); err != nil {
		t.Fatalf("SetDesktops: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("noctalia stub was not called: %v", err)
	}
	// Connectors are sorted (DP-2, eDP-1). The first image is applied to all
	// outputs (and becomes the default), then eDP-1 keeps the second image.
	want := []string{
		"msg wallpaper-set " + first,
		"msg wallpaper-set eDP-1 " + second,
	}
	var calls []string
	var current []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "msg" {
			if len(current) > 0 {
				calls = append(calls, strings.Join(current, " "))
			}
			current = []string{"msg"}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		calls = append(calls, strings.Join(current, " "))
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("noctalia calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestNoctaliaFallsBackToSwaybgWhenIPCUnavailable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-test")
	t.Setenv("DISPLAY", "")
	t.Setenv("XDG_CURRENT_DESKTOP", "niri")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	// The shell binary exists but the shell is not running.
	makeScript(t, dir, "noctalia", "#!/bin/sh\nexit 1\n")

	marker := filepath.Join(t.TempDir(), "swaybg-args.txt")
	makeScript(t, dir, "swaybg", "#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+marker+"\"\nexit 0\n")

	img := filepath.Join(t.TempDir(), "wall.jpg")
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetDesktops(context.Background(), []string{img}); err != nil {
		t.Fatalf("SetDesktops: %v", err)
	}

	var data []byte
	for i := 0; i < 50; i++ {
		if b, err := os.ReadFile(marker); err == nil {
			data = b
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if data == nil {
		t.Fatal("swaybg stub was never invoked as a fallback")
	}
	if !strings.Contains(strings.Join(strings.Fields(string(data)), " "), "-i "+img) {
		t.Errorf("swaybg invoked with unexpected args: %q", data)
	}
}
