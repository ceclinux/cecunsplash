//go:build linux

package wallpaper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// On most Linux compositors / window managers every workspace (Space) shares the
// same wallpaper, so cecunsplash applies a single image. swaybg can paint a
// different image per output, but per-workspace wallpaper is not generally
// supported by Wayland compositors, so we keep the macOS behaviour of one image
// per desktop but collapse it to a single shared wallpaper here.

const setterPidName = "setter.pid"

// CountDesktops reports how many images a change should fetch. On niri with a
// running Noctalia shell every connected output can get its own wallpaper, so
// that is one image per monitor. Otherwise Linux workspaces share one image.
func CountDesktops(ctx context.Context) (int, error) {
	if noctaliaAvailable(noctaliaRuntimeEnv(waylandDisplayEnv())) {
		if connectors, err := niriConnectors(ctx); err == nil && len(connectors) > 1 {
			return len(connectors), nil
		}
	}
	return 1, nil
}

// SetDesktops applies the first wallpaper path using the best available backend.
func SetDesktops(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("no wallpaper paths provided")
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("empty wallpaper path in list")
		}
	}
	backend, err := detectBackend()
	if err != nil {
		return err
	}
	return backend.apply(ctx, paths)
}

// setterDir returns the directory used to store the pidfile of the managed
// setter process (e.g. swaybg). It prefers XDG_STATE_HOME, then ~/.local/state.
func setterDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "cecunsplash"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "cecunsplash"), nil
}

func setterPidPath() (string, error) {
	dir, err := setterDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, setterPidName), nil
}

// backend is a wallpaper application strategy.
type backend interface {
	apply(ctx context.Context, paths []string) error
}

func detectBackend() (backend, error) {
	desktop := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP"))
	waylandEnv := waylandDisplayEnv()

	// Noctalia first: on niri setups the shell owns the background layer, and a
	// second background surface (swaybg) would fight it for the same slot.
	noctaliaEnv := noctaliaRuntimeEnv(waylandEnv)
	if noctaliaAvailable(noctaliaEnv) {
		return noctaliaBackend{env: noctaliaEnv}, nil
	}

	// Wayland next: swaybg is the de-facto standard for niri, sway, Hyprland, ...
	if os.Getenv("WAYLAND_DISPLAY") != "" || waylandEnv != nil {
		if _, err := exec.LookPath("swaybg"); err == nil {
			return swaybgBackend{env: waylandEnv}, nil
		}
		if _, err := exec.LookPath("swww"); err == nil {
			return swwwBackend{env: waylandEnv}, nil
		}
		if _, err := exec.LookPath("wbg"); err == nil {
			return wbgBackend{env: waylandEnv}, nil
		}
	}

	// GNOME / Unity / Cinnamon expose the background via gsettings. Do not use
	// this fallback on other Wayland compositors (niri/sway/Hyprland/etc.); the
	// schema may exist because GNOME components are installed, but changing it has
	// no visible effect there.
	if isGsettingsDesktop(desktop) && hasGnomeBackgroundSchema() {
		return gsettingsBackend{}, nil
	}

	// X11 fallback tools.
	if os.Getenv("DISPLAY") != "" {
		if _, err := exec.LookPath("feh"); err == nil {
			return fehBackend{}, nil
		}
		if _, err := exec.LookPath("xwallpaper"); err == nil {
			return xwallpaperBackend{}, nil
		}
		if _, err := exec.LookPath("nitrogen"); err == nil {
			return nitrogenBackend{}, nil
		}
	}

	var avail []string
	avail = append(avail, "WAYLAND_DISPLAY="+boolStr(os.Getenv("WAYLAND_DISPLAY") != ""))
	avail = append(avail, "XDG_CURRENT_DESKTOP="+desktop)
	avail = append(avail, "DISPLAY="+boolStr(os.Getenv("DISPLAY") != ""))
	return nil, fmt.Errorf("no supported Linux wallpaper backend found (tried Noctalia IPC and swaybg/swww/wbg on Wayland, gsettings on GNOME, feh/xwallpaper/nitrogen on X11). Install 'swaybg' for niri/Wayland or make sure the Noctalia shell is running. Environment: %s", strings.Join(avail, ", "))
}

func boolStr(b bool) string {
	if b {
		return "set"
	}
	return "unset"
}

// waylandDisplayEnv derives WAYLAND_DISPLAY from the compositor socket. A
// systemd user service started at boot does not inherit the graphical session
// environment, so without this the daemon cannot reach any Wayland surface.
func waylandDisplayEnv() []string {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return nil
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(runtimeDir, "wayland-*"))
	if err != nil {
		return nil
	}
	for _, m := range matches {
		name := filepath.Base(m)
		if strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".pid") {
			continue
		}
		return []string{"WAYLAND_DISPLAY=" + name}
	}
	return nil
}

// noctaliaRuntimeEnv makes sure the noctalia CLI can find the running shell.
// Noctalia's IPC socket is $XDG_RUNTIME_DIR/noctalia-<WAYLAND_DISPLAY>.sock and
// the CLI refuses an absolute WAYLAND_DISPLAY, so derive the relative name from
// the socket when the environment does not provide one.
func noctaliaRuntimeEnv(base []string) []string {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return base
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return base
	}
	matches, err := filepath.Glob(filepath.Join(runtimeDir, "noctalia-*.sock"))
	if err != nil {
		return base
	}
	for _, m := range matches {
		name := filepath.Base(m)
		if strings.HasPrefix(name, "noctalia-dmenu-") {
			continue
		}
		display := strings.TrimSuffix(strings.TrimPrefix(name, "noctalia-"), ".sock")
		if display == "" {
			continue
		}
		env := make([]string, 0, len(base)+1)
		for _, e := range base {
			if strings.HasPrefix(e, "WAYLAND_DISPLAY=") {
				continue
			}
			env = append(env, e)
		}
		return append(env, "WAYLAND_DISPLAY="+display)
	}
	return base
}

// noctaliaAvailable reports whether a Noctalia shell answers IPC. It is the
// probe used to decide whether the Noctalia backend can be used at all.
func noctaliaAvailable(env []string) bool {
	if _, err := exec.LookPath("noctalia"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "noctalia", "msg", "wallpaper-get")
	cmd.Env = append(os.Environ(), env...)
	return cmd.Run() == nil
}

// niriEnv derives NIRI_SOCKET from the compositor socket when a systemd user
// service did not inherit it. niri's socket is
// $XDG_RUNTIME_DIR/niri.<WAYLAND_DISPLAY>.<pid>.sock.
func niriEnv() []string {
	if os.Getenv("NIRI_SOCKET") != "" {
		return nil
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		return nil
	}
	patterns := []string{}
	if display := os.Getenv("WAYLAND_DISPLAY"); display != "" {
		patterns = append(patterns, filepath.Join(runtimeDir, "niri."+display+".*.sock"))
	}
	patterns = append(patterns, filepath.Join(runtimeDir, "niri.*.sock"))
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			continue
		}
		return []string{"NIRI_SOCKET=" + matches[0]}
	}
	return nil
}

// niriOutput is the part of `niri msg --json outputs` cecunsplash needs.
// Disabled outputs have a null logical size but are still listed, while
// Noctalia only manages enabled ones, so they must be filtered out.
type niriOutput struct {
	Logical *json.RawMessage `json:"logical"`
}

// niriConnectors lists enabled outputs in a stable (sorted) order so the
// image-to-monitor mapping is the same when fetching and applying.
func niriConnectors(ctx context.Context) ([]string, error) {
	if _, err := exec.LookPath("niri"); err != nil {
		return nil, err
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "niri", "msg", "--json", "outputs")
	cmd.Env = append(os.Environ(), niriEnv()...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("niri outputs: %w", err)
	}
	var outputs map[string]niriOutput
	if err := json.Unmarshal(out, &outputs); err != nil {
		return nil, fmt.Errorf("parse niri outputs: %w", err)
	}
	names := make([]string, 0, len(outputs))
	for name, output := range outputs {
		if name != "" && output.Logical != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func isGsettingsDesktop(desktop string) bool {
	return strings.Contains(desktop, "gnome") || strings.Contains(desktop, "unity") || strings.Contains(desktop, "cinnamon")
}

func hasGnomeBackgroundSchema() bool {
	out, err := exec.Command("gsettings", "list-schemas").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "org.gnome.desktop.background")
}

func absHome(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p, err
	}
	return abs, nil
}

// fileURI returns a file:// URI for gsettings.
func fileURI(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.ToSlash(abs)
	// minimal percent-encoding of the spaces and other reserved chars for paths
	var b strings.Builder
	b.WriteString("file://")
	for _, r := range abs {
		if r == '/' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '~' {
			b.WriteRune(r)
		} else {
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String(), nil
}

// ---------- Noctalia shell (Wayland, IPC) ----------

// noctaliaBackend hands the image to the running Noctalia shell. Noctalia owns
// the background layer, so this avoids competing layer-shell surfaces and gets
// its transition effects and palette extraction for free.
type noctaliaBackend struct {
	env []string
}

func (b noctaliaBackend) apply(ctx context.Context, paths []string) error {
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		path, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("resolve wallpaper path: %w", err)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("wallpaper file: %w", err)
		}
		abs = append(abs, path)
	}

	connectors, err := niriConnectors(ctx)
	if err != nil || len(connectors) < 2 || len(abs) < 2 {
		return b.set(ctx, "", abs[0])
	}

	// Apply the default first so outputs connected later start from a valid
	// image, then give each connected output its own wallpaper.
	if err := b.set(ctx, "", abs[0]); err != nil {
		return err
	}
	for i, connector := range connectors {
		if i == 0 {
			// Already applied as the default above.
			continue
		}
		// Best effort: the default above already covers every output, so an
		// output Noctalia does not know about yet (hotplug race) must not fail
		// the whole change and trigger a retry.
		_ = b.set(ctx, connector, abs[i%len(abs)])
	}
	return nil
}

// set applies one image through Noctalia IPC. An empty connector targets every
// output and updates the default path.
func (b noctaliaBackend) set(ctx context.Context, connector, path string) error {
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"msg", "wallpaper-set"}
	if connector != "" {
		args = append(args, connector)
	}
	args = append(args, path)
	cmd := exec.CommandContext(c, "noctalia", args...)
	cmd.Env = append(os.Environ(), b.env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("noctalia wallpaper-set %s failed: %w: %s", connector, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------- swaybg (Wayland, managed) ----------

type swaybgBackend struct {
	env []string
}

func (b swaybgBackend) apply(ctx context.Context, paths []string) error {
	path := paths[0]
	if _, err := exec.LookPath("swaybg"); err != nil {
		return fmt.Errorf("swaybg not installed: %w", err)
	}

	// Kill any previously managed swaybg so we can start a fresh one.
	if err := killManagedSetter(false); err != nil {
		// non-fatal: races with the desktop can happen
	}

	args := []string{"-m", "fill", "-i", path}
	cmd := exec.Command("swaybg", args...)
	cmd.Env = append(os.Environ(), b.env...)
	// Detach from the controlling terminal and create a new session so the
	// process survives after cecunsplash exits.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start swaybg: %w", err)
	}
	if err := writeSetterPid(cmd.Process.Pid, "swaybg", strings.Join(args, " ")); err != nil {
		// Logically we still want swaybg running; record failure but keep going.
		_ = err
	}
	return nil
}

// ---------- swww (Wayland, daemon) ----------

type swwwBackend struct {
	env []string
}

func (b swwwBackend) apply(ctx context.Context, paths []string) error {
	path := paths[0]
	if _, err := exec.LookPath("swww"); err != nil {
		return fmt.Errorf("swww not installed: %w", err)
	}
	// Ensure the daemon is running (no-op if already running).
	initCmd := exec.Command("swww", "init")
	initCmd.Env = append(os.Environ(), b.env...)
	_ = initCmd.Run()
	cmd := exec.Command("swww", "img", path, "--transition-type", "grow")
	cmd.Env = append(os.Environ(), b.env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("swww img failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------- wbg (Wayland, managed) ----------

type wbgBackend struct {
	env []string
}

func (b wbgBackend) apply(ctx context.Context, paths []string) error {
	path := paths[0]
	if _, err := exec.LookPath("wbg"); err != nil {
		return fmt.Errorf("wbg not installed: %w", err)
	}
	if err := killManagedSetter(false); err != nil {
		_ = err
	}
	cmd := exec.Command("wbg", "-i", path)
	cmd.Env = append(os.Environ(), b.env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start wbg: %w", err)
	}
	_ = writeSetterPid(cmd.Process.Pid, "wbg", "-i "+path)
	return nil
}

// ---------- GNOME gsettings ----------

type gsettingsBackend struct{}

func (gsettingsBackend) apply(ctx context.Context, paths []string) error {
	path := paths[0]
	uri, err := fileURI(path)
	if err != nil {
		return err
	}
	for _, key := range []string{"picture-uri", "picture-uri-dark"} {
		if err := gsettingsSet(ctx, "org.gnome.desktop.background", key, uri); err != nil {
			return fmt.Errorf("gsettings set %s: %w", key, err)
		}
	}
	_ = gsettingsSet(ctx, "org.gnome.desktop.background", "picture-options", "zoom")
	return nil
}

func gsettingsSet(ctx context.Context, schema, key, value string) error {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "gsettings", "set", schema, key, value).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gsettings set %s %s: %w: %s", schema, key, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------- feh (X11) ----------

type fehBackend struct{}

func (fehBackend) apply(ctx context.Context, paths []string) error {
	path := paths[0]
	if _, err := exec.LookPath("feh"); err != nil {
		return fmt.Errorf("feh not installed: %w", err)
	}
	out, err := exec.CommandContext(ctx, "feh", "--bg-fill", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("feh failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------- xwallpaper (X11) ----------

type xwallpaperBackend struct{}

func (xwallpaperBackend) apply(ctx context.Context, paths []string) error {
	path := paths[0]
	if _, err := exec.LookPath("xwallpaper"); err != nil {
		return fmt.Errorf("xwallpaper not installed: %w", err)
	}
	out, err := exec.CommandContext(ctx, "xwallpaper", "--zoom", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("xwallpaper failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------- nitrogen (X11) ----------

type nitrogenBackend struct{}

func (nitrogenBackend) apply(ctx context.Context, paths []string) error {
	path := paths[0]
	if _, err := exec.LookPath("nitrogen"); err != nil {
		return fmt.Errorf("nitrogen not installed: %w", err)
	}
	out, err := exec.CommandContext(ctx, "nitrogen", "--set-zoom-fill", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nitrogen failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------- pidfile management for managed setters ----------

// writeSetterPid records the PID/command line of a setter process started by us.
func writeSetterPid(pid int, name, argsline string) error {
	dir, err := setterDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path, err := setterPidPath()
	if err != nil {
		return err
	}
	content := fmt.Sprintf("%d\t%s\t%s\n", pid, name, argsline)
	return os.WriteFile(path, []byte(content), 0o600)
}

// killManagedSetter terminates a setter process previously started by cecunsplash.
// When forceAll is true, all running instances of the managed setter binary are
// killed (used only at uninstall/cleanup).
func killManagedSetter(forceAll bool) error {
	path, err := setterPidPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	_ = os.Remove(path)
	fields := strings.Split(strings.TrimSpace(string(data)), "\t")
	if len(fields) == 0 {
		return nil
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return fmt.Errorf("bad pidfile %q: %s", string(data), err)
	}
	sendTerm(pid)
	if forceAll && len(fields) > 1 {
		// kill any lingering instances of the same binary
		_ = exec.Command("pkill", "-TERM", "-x", fields[1]).Run()
	}
	return nil
}

func sendTerm(pid int) {
	if pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Signal(syscall.SIGTERM)
	}
}

// CleanupSetter stops a setter previously started by cecunsplash (kept for future use).
func CleanupSetter() {
	_ = killManagedSetter(false)
}

func init() {
	_ = runtime.NumCPU // keep runtime import referenced on all builds
}
