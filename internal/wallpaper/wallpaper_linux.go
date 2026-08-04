//go:build linux

package wallpaper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// CountDesktops reports the niri workspace count when running under niri with
// a persistent wallpaper daemon (swww or its awww fork) available: those can
// swap images atomically via a persistent daemon, so per-workspace wallpaper is
// flash-free. Without one we fall back to one shared wallpaper, since swaybg can
// only change images by restarting and that flashes on every switch.
func CountDesktops(ctx context.Context) (int, error) {
	if isNiriDesktop() && hasPersistentDaemon() {
		workspaces, err := niriWorkspaces(ctx)
		if err == nil && len(workspaces) > 0 {
			return len(workspaces), nil
		}
	}
	return 1, nil
}

// SetDesktops applies wallpapers using the best available backend. On niri with
// a flash-free persistent daemon and more than one workspace, it gives each
// workspace its own wallpaper (swapped instantly via the daemon's IPC, no
// black flash). Otherwise it applies a single shared image.
func SetDesktops(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("no wallpaper paths provided")
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("empty wallpaper path in list")
		}
	}
	// Per-workspace wallpaper is only worth doing on niri with a flash-free
	// persistent daemon (swww/awww); swaybg would flash on every switch, so with
	// only swaybg we keep a single shared wallpaper instead.
	if isNiriDesktop() && len(paths) > 1 && hasPersistentDaemon() {
		if err := applyNiriWorkspaceWallpapers(ctx, paths); err == nil {
			return nil
		}
		// On failure fall back to a single shared image below.
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

	// Wayland first: a persistent, IPC-driven daemon (swww / awww) is
	// preferred on niri because it can swap images without flashing. swaybg,
	// swww, awww, wbg follow for single-image wallpaper.
	if wayland {
		if _, ok := detectPersistentDaemon(); ok {
			return swwwBackend{}, nil
		}
		if _, err := exec.LookPath("swaybg"); err == nil {
			return swaybgBackend{}, nil
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

// niriEventType identifies a kind of event in the niri event-stream.
// Only the events cecunsplash cares about are modelled here.
type niriEventType string

const (
	evWorkspacesChanged  niriEventType = "WorkspacesChanged"
	evWorkspaceActivated niriEventType = "WorkspaceActivated"
)

// niriEvent is a thin decoder for the niri event-stream JSON. Unknown event
// types are simply ignored.
type niriEvent struct {
	WorkspacesChanged *struct {
		Workspaces []niriWorkspace `json:"workspaces"`
	} `json:"WorkspacesChanged,omitempty"`

	WorkspaceActivated *struct {
		ID      int  `json:"id"`
		Focused bool `json:"focused"`
	} `json:"WorkspaceActivated,omitempty"`
}

// stdbufArgs makes `niri msg --json event-stream` line-buffered so individual
// events are delivered immediately instead of being held in a 4 KiB block
// buffer until it fills. stdbuf (coreutils) is present on every Linux distro.
var stdbufArgs = []string{"stdbuf", "-oL"}

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

func hasSwaybg() bool {
	_, err := exec.LookPath("swaybg")
	return err == nil
}

// persistentDaemon is a flash-free, IPC-driven wallpaper daemon: either swww
// or its drop-in fork awww. Both keep a long-lived daemon process and swap the
// displayed image atomically (no kill, no black flash).
type persistentDaemon struct {
	client string // "swww" or "awww"
	daemon string // daemon binary, "" if the client starts it (swww init)
}

// detectPersistentDaemon returns the first available flash-free daemon.
func detectPersistentDaemon() (persistentDaemon, bool) {
	if _, err := exec.LookPath("swww"); err == nil {
		return persistentDaemon{client: "swww"}, true
	}
	if _, err := exec.LookPath("awww"); err == nil {
		return persistentDaemon{client: "awww", daemon: "awww-daemon"}, true
	}
	return persistentDaemon{}, false
}

func hasPersistentDaemon() bool {
	_, ok := detectPersistentDaemon()
	return ok
}

// ensureDaemon makes sure the daemon is running. `swww init` is idempotent and
// starts the daemon; awww has no equivalent, so welaunch awww-daemon ourselves
// (and wait for it to be reachable).
func (d persistentDaemon) ensureDaemon(ctx context.Context) error {
	if d.daemonReady(ctx) {
		return nil
	}
	if d.daemon == "" {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(c, d.client, "init").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s init: %w: %s", d.client, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if _, err := exec.LookPath(d.daemon); err != nil {
		return fmt.Errorf("%s not installed: %w", d.daemon, err)
	}
	cmd := exec.Command(d.daemon)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", d.daemon, err)
	}
	go func() { _ = cmd.Wait() }() // reap when it exits
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if d.daemonReady(ctx) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("%s did not become ready", d.daemon)
}

func (d persistentDaemon) daemonReady(ctx context.Context) bool {
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return exec.CommandContext(c, d.client, "query").Run() == nil
}

// paintInstant swaps the wallpaper instantly with no transition animation and
// no daemon restart, so there is no black flash on workspace switches.
func (d persistentDaemon) paintInstant(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, d.client, "img", path, "--transition-type", "none").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s img %s: %w: %s", d.client, path, err, strings.TrimSpace(string(out)))
	}
	return nil
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
	d, ok := detectPersistentDaemon()
	if !ok {
		return fmt.Errorf("no persistent wallpaper daemon (swww/awww) found")
	}
	if err := d.ensureDaemon(ctx); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	out, err := exec.CommandContext(ctx, d.client, "img", path, "--transition-type", "grow").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s img failed: %w: %s", d.client, err, strings.TrimSpace(string(out)))
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

// imagePainter paints a single wallpaper image onto the output. The niri
// per-workspace watcher holds one of these and calls it whenever the focused
// workspace changes.
type imagePainter func(path string) error

// niriWatcher gives each niri workspace its own wallpaper and swaps the
// displayed wallpaper whenever the focused workspace changes. It is driven by
// niri's JSON event-stream (line-buffered via stdbuf), so updates are
// event-driven rather than polled. The image for a workspace is chosen by its
// position (idx): workspace at index N gets paths[(N-1)%len(paths)]. The
// mapping is cached by niri's stable workspace id, so a workspace keeps its
// wallpaper even if it is later reordered. The painter is a persistent IPC
// daemon (swww/awww) with `--transition-type none` when available (instant
// atomic swap via the persistent daemon: no kill, no black flash), falling back
// to swaybg.
type niriWatcher struct {
	mu       sync.Mutex
	paint    imagePainter
	idxToImg map[int]string // workspace idx -> image path (by position)
	idToImg  map[int]string // workspace id -> image path (cached, stable)
	stopCh   chan struct{}
	cancel   context.CancelFunc
}

var niriWatch niriWatcher

// start (re)initialises the position->image map from the current workspace
// list and launches the event-stream watcher using the given painter. It returns
// the image for the currently focused workspace so the caller can paint it
// immediately.
func (w *niriWatcher) start(ctx context.Context, paths []string, paint imagePainter) (string, error) {
	workspaces, err := niriWorkspaces(ctx)
	if err != nil {
		return "", err
	}

	// Stop any previous watcher before starting a fresh one.
	w.stop()

	watchCtx, cancel := context.WithCancel(context.Background())
	// Build the position->image map: workspace at index N gets image
	// paths[(N-1)%len(paths)]. Then cache id->image for the workspaces that
	// currently exist so the first paint is correct.
	idxToImg := make(map[int]string, len(paths))
	for i := range paths {
		idxToImg[i+1] = paths[i%len(paths)] // niri workspace idx is 1-based
	}
	idToImg := make(map[int]string, len(workspaces))
	for _, ws := range workspaces {
		idToImg[ws.ID] = w.imgForIdxLocked(ws.Idx, idxToImg)
	}

	w.mu.Lock()
	w.paint = paint
	w.idxToImg = idxToImg
	w.idToImg = idToImg
	w.stopCh = make(chan struct{})
	w.cancel = cancel
	stopCh := w.stopCh
	w.mu.Unlock()

	focusedID := focusedWorkspaceID(workspaces)
	img := w.imgForID(focusedID)

	go w.watchLoop(watchCtx, stopCh)
	return img, nil
}

// stop tears down the watcher: cancels the event-stream context and closes the
// stop channel so the goroutine exits. Safe to call multiple times.
func (w *niriWatcher) stop() {
	w.mu.Lock()
	stopCh := w.stopCh
	cancel := w.cancel
	w.stopCh = nil
	w.cancel = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if stopCh != nil {
		<-stopCh // wait for the goroutine to finish
	}
}

// imgForIdxLocked returns the image for a workspace position (idx). The caller
// must hold w.mu.
func (w *niriWatcher) imgForIdxLocked(idx int, idxToImg map[int]string) string {
	if img, ok := idxToImg[idx]; ok {
		return img
	}
	// idx beyond the image list: cycle by position for a stable result.
	if len(idxToImg) == 0 {
		return ""
	}
	return idxToImg[((idx-1)%len(idxToImg))+1]
}

// imgForID returns the cached image for a workspace id, assigning one (by the
// workspace's current position) if the workspace is new. New workspaces get the
// image for their idx, so the mapping stays deterministic.
func (w *niriWatcher) imgForID(id int) string {
	w.mu.Lock()
	if img, ok := w.idToImg[id]; ok {
		w.mu.Unlock()
		return img
	}
	idxToImg := w.idxToImg
	w.mu.Unlock()

	// Unknown workspace: look up the daemon's workspace idx for this id and use
	// the image for that position. The niri query runs outside the lock.
	idx := 1
	if workspaces, err := niriWorkspaces(context.Background()); err == nil {
		for _, ws := range workspaces {
			if ws.ID == id {
				idx = ws.Idx
				break
			}
		}
	}
	img := w.imgForIdxLocked(idx, idxToImg)
	if img != "" {
		w.mu.Lock()
		w.idToImg[id] = img
		w.mu.Unlock()
	}
	return img
}

// watchLoop runs `niri msg --json event-stream` (line-buffered) and reacts to
// workspace focus changes. On EOF or error it reconnects after a short backoff.
func (w *niriWatcher) watchLoop(ctx context.Context, stopCh chan struct{}) {
	defer func() {
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
	}()
	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}
		if err := w.streamEvents(ctx, stopCh); err != nil {
			fmt.Fprintf(os.Stderr, "cecunsplash: niri event-stream ended: %v\n", err)
		}
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (w *niriWatcher) streamEvents(ctx context.Context, stopCh <-chan struct{}) error {
	args := append(append([]string{}, stdbufArgs...), "niri", "msg", "--json", "event-stream")
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start niri event-stream: %w", err)
	}
	// Ensure the child is reaped, even if it exits before us.
	go func() { _, _ = cmd.Process.Wait() }()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		select {
		case <-stopCh:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return scanner.Err()
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
			return scanner.Err()
		default:
		}
		var ev niriEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		switch {
		case ev.WorkspaceActivated != nil && ev.WorkspaceActivated.Focused:
			w.onFocus(ev.WorkspaceActivated.ID)
		case ev.WorkspacesChanged != nil:
			w.onWorkspacesChanged(ev.WorkspacesChanged.Workspaces)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	return nil
}

// onFocus paints the image mapped to the newly focused workspace.
func (w *niriWatcher) onFocus(id int) {
	img := w.imgForID(id)
	if img == "" {
		return
	}
	w.mu.Lock()
	paint := w.paint
	w.mu.Unlock()
	if paint == nil {
		return
	}
	if err := paint(img); err != nil {
		fmt.Fprintf(os.Stderr, "cecunsplash: paint on focus (ws id %d): %v\n", id, err)
	}
}

// onWorkspacesChanged learns about newly created workspaces so they get an
// image assigned (by their position) before they are first focused.
func (w *niriWatcher) onWorkspacesChanged(workspaces []niriWorkspace) {
	for _, ws := range workspaces {
		_ = w.imgForID(ws.ID) // assign by idx if new
	}
}

func focusedWorkspaceID(workspaces []niriWorkspace) int {
	for _, ws := range workspaces {
		if ws.IsFocused {
			return ws.ID
		}
	}
	if len(workspaces) > 0 {
		return workspaces[0].ID
	}
	return 0
}

// applyNiriWorkspaceWallpapers maps one wallpaper per niri workspace (by stable
// id) and starts a watcher that swaps the displayed wallpaper as the focused
// workspace changes. The currently focused workspace is painted immediately.
// It prefers a persistent IPC daemon (swww/awww, instant `--transition-type
// none` swap: no black flash) and falls back to swaybg.
func applyNiriWorkspaceWallpapers(ctx context.Context, paths []string) error {
	paint, err := chooseNiriPainter(ctx)
	if err != nil {
		return err
	}
	img, err := niriWatch.start(ctx, paths, paint)
	if err != nil {
		return err
	}
	if img == "" {
		return fmt.Errorf("no wallpaper mapped for focused workspace")
	}
	return paint(img)
}

// chooseNiriPainter returns a flash-free painter when possible. A persistent
// IPC daemon (swww or its awww fork) swaps the image atomically via
// `<client> img --transition-type none` (instant, no daemon restart), so
// switching workspaces shows the new wallpaper instantly with no black flash.
// swaybg can only change images by restarting, which flashes, so it is only
// used as a last resort.
func chooseNiriPainter(ctx context.Context) (imagePainter, error) {
	if d, ok := detectPersistentDaemon(); ok {
		if err := d.ensureDaemon(ctx); err != nil {
			return nil, fmt.Errorf("start daemon: %w", err)
		}
		// Kill any swaybg cecunsplash previously managed so it does not fight the
		// persistent daemon.
		_ = killManagedSetter(false)
		return d.paintInstant, nil
	}
	if hasSwaybg() {
		fmt.Fprintln(os.Stderr, "cecunsplash: hint: install 'swww' for per-workspace wallpaper without a flash on switch (swaybg must restart to change images)")
		return startSwaybg, nil
	}
	return nil, fmt.Errorf("no Wayland wallpaper backend found; install 'swww' (recommended) for niri")
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
