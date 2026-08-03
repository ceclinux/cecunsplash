//go:build linux

package wallpaper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// CountDesktops reports 1: Linux desktops share one wallpaper across workspaces.
func CountDesktops(ctx context.Context) (int, error) {
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
	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	desktop := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP"))

	// Wayland first: swaybg is the de-facto standard for niri, sway, Hyprland, ...
	if wayland {
		if _, err := exec.LookPath("swaybg"); err == nil {
			return swaybgBackend{}, nil
		}
		if _, err := exec.LookPath("swww"); err == nil {
			return swwwBackend{}, nil
		}
		if _, err := exec.LookPath("wbg"); err == nil {
			return wbgBackend{}, nil
		}
	}

	// GNOME / Unity / Cinnamon expose the background via gsettings.
	if hasGnomeBackgroundSchema() {
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
	return nil, fmt.Errorf("no supported Linux wallpaper backend found (tried swaybg/swww/wbg on Wayland, gsettings on GNOME, feh/xwallpaper/nitrogen on X11). Install 'swaybg' for niri/Wayland. Environment: %s", strings.Join(avail, ", "))
}

func boolStr(b bool) string {
	if b {
		return "set"
	}
	return "unset"
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

// ---------- swaybg (Wayland, managed) ----------

type swaybgBackend struct{}

func (swaybgBackend) apply(ctx context.Context, paths []string) error {
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

type swwwBackend struct{}

func (swwwBackend) apply(ctx context.Context, paths []string) error {
	path := paths[0]
	if _, err := exec.LookPath("swww"); err != nil {
		return fmt.Errorf("swww not installed: %w", err)
	}
	// Ensure the daemon is running (no-op if already running).
	_ = exec.Command("swww", "init").Run()
	out, err := exec.Command("swww", "img", path, "--transition-type", "grow").CombinedOutput()
	if err != nil {
		return fmt.Errorf("swww img failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ---------- wbg (Wayland, managed) ----------

type wbgBackend struct{}

func (wbgBackend) apply(ctx context.Context, paths []string) error {
	path := paths[0]
	if _, err := exec.LookPath("wbg"); err != nil {
		return fmt.Errorf("wbg not installed: %w", err)
	}
	if err := killManagedSetter(false); err != nil {
		_ = err
	}
	cmd := exec.Command("wbg", "-i", path)
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
