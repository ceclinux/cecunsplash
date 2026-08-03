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
	"sync"
	"syscall"
	"time"
)

// On Linux a distinct wallpaper can be applied per niri workspace: cecunsplash
// queries niri for the workspace count, downloads one image per workspace, and
// runs a background watcher that swaps swaybg whenever the focused workspace
// changes. On other compositors / window managers every workspace shares one
// wallpaper, so we collapse to a single shared image there.

const setterPidName = "setter.pid"

// CountDesktops reports the niri workspace count when running under niri;
// otherwise Linux desktops share one wallpaper across workspaces.
func CountDesktops(ctx context.Context) (int, error) {
	if isNiriDesktop() {
		workspaces, err := niriWorkspaces(ctx)
		if err == nil && len(workspaces) > 0 {
			return len(workspaces), nil
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
	return nil, fmt.Errorf("no supported Linux wallpaper backend found (tried swaybg/swww/wbg on Wayland, gsettings on GNOME, feh/xwallpaper/nitrogen on X11). Install 'swaybg' for niri/Wayland. Environment: %s", strings.Join(avail, ", "))
}

func boolStr(b bool) string {
	if b {
		return "set"
	}
	return "unset"
}

type niriWorkspace struct {
	ID        int    `json:"id"`
	Idx       int    `json:"idx"`
	Name      string `json:"name"`
	IsFocused bool   `json:"is_focused"`
}

func isNiriDesktop() bool {
	return strings.Contains(strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP")), "niri") || os.Getenv("NIRI_SOCKET") != ""
}

func niriWorkspaces(ctx context.Context) ([]niriWorkspace, error) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "niri", "msg", "--json", "workspaces").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("niri workspaces: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var workspaces []niriWorkspace
	if err := json.Unmarshal(out, &workspaces); err != nil {
		return nil, err
	}
	return workspaces, nil
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

// ---------- swaybg (Wayland, managed) ----------

type swaybgBackend struct{}

func (swaybgBackend) apply(ctx context.Context, paths []string) error {
	if _, err := exec.LookPath("swaybg"); err != nil {
		return fmt.Errorf("swaybg not installed: %w", err)
	}
	if isNiriDesktop() && len(paths) > 1 {
		if err := applyNiriWorkspaceWallpapers(ctx, paths); err == nil {
			return nil
		}
		// If niri workspace introspection fails, fall back to one shared wallpaper.
	}
	return startSwaybg(paths[0])
}

func startSwaybg(path string) error {
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
	go func() { _ = cmd.Wait() }() // reap the detached setter when it exits
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
	go func() { _ = cmd.Wait() }() // reap the detached setter when it exits
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

// ---------- niri per-workspace wallpapers ----------

// niriWatcher holds the state for the background goroutine that swaps swaybg
// when the focused niri workspace changes, giving each workspace its own
// wallpaper.
type niriWatcher struct {
	mu       sync.Mutex
	idxToImg map[int]string // workspace idx -> image path
	stopCh   chan struct{}
	cancel   context.CancelFunc
	started  bool
}

var niriWatch niriWatcher

// start launches the watcher goroutine (idempotent). It returns the image
// path for the currently focused workspace so the caller can paint it right
// away.
func (w *niriWatcher) start(ctx context.Context, paths []string) (string, error) {
	w.mu.Lock()
	if w.stopCh != nil {
		close(w.stopCh)
	}
	if w.cancel != nil {
		w.cancel()
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	w.stopCh = make(chan struct{})
	w.cancel = cancel
	w.idxToImg = buildIdxToImg(paths)
	stopCh := w.stopCh
	w.started = true
	w.mu.Unlock()

	workspaces, err := niriWorkspaces(ctx)
	if err != nil {
		cancel()
		return "", err
	}
	focusedIdx := focusedWorkspaceIdx(workspaces)
	img := w.imgForIdx(focusedIdx)

	go w.watchLoop(watchCtx, stopCh)
	return img, nil
}

func (w *niriWatcher) stop() {
	w.mu.Lock()
	if w.stopCh != nil {
		close(w.stopCh)
		w.stopCh = nil
	}
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.started = false
	w.mu.Unlock()
}

func (w *niriWatcher) imgForIdx(idx int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if idx, ok := w.idxToImg[idx]; ok {
		return idx
	}
	// unknown / new workspace: cycle through the available images
	if len(w.idxToImg) == 0 {
		return ""
	}
	keys := make([]int, 0, len(w.idxToImg))
	for k := range w.idxToImg {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return w.idxToImg[keys[idx%len(keys)]]
}

// watchLoop polls niri for the focused workspace and restarts swaybg with the
// appropriate image when it changes. Polling is used instead of `event-stream`
// because the latter is block-buffered when its stdout is a pipe, so individual
// events may not be delivered until the buffer fills.
func (w *niriWatcher) watchLoop(ctx context.Context, stopCh chan struct{}) {
	var lastIdx int = -1
	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-time.After(750 * time.Millisecond):
		}

		workspaces, err := niriWorkspaces(ctx)
		if err != nil {
			continue
		}
		idx := focusedWorkspaceIdx(workspaces)
		if idx == lastIdx {
			continue
		}
		lastIdx = idx
		if img := w.imgForIdx(idx); img != "" {
			if err := startSwaybg(img); err != nil {
				fmt.Fprintf(os.Stderr, "cecunsplash: startSwaybg on focus ws %d: %v\n", idx, err)
			}
		}
	}
}

func buildIdxToImg(paths []string) map[int]string {
	m := make(map[int]string, len(paths))
	for i, p := range paths {
		m[i+1] = p // niri workspace idx is 1-based
	}
	return m
}

func focusedWorkspaceIdx(workspaces []niriWorkspace) int {
	for _, ws := range workspaces {
		if ws.IsFocused {
			return ws.Idx
		}
	}
	if len(workspaces) > 0 {
		return workspaces[0].Idx
	}
	return 1
}

// applyNiriWorkspaceWallpapers maps one wallpaper per niri workspace index and
// starts a watcher that swaps swaybg as the focused workspace changes. The
// currently focused workspace is painted immediately.
func applyNiriWorkspaceWallpapers(ctx context.Context, paths []string) error {
	img, err := niriWatch.start(ctx, paths)
	if err != nil {
		return err
	}
	if img == "" {
		return fmt.Errorf("no wallpaper mapped for focused workspace")
	}
	return startSwaybg(img)
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
	niriWatch.stop()
	_ = killManagedSetter(false)
}

func init() {
	_ = runtime.NumCPU // keep runtime import referenced on all builds
}
