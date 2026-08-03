//go:build linux

package config

// DefaultShortcut on Linux is a signal-based manual trigger (SIGUSR1 to the
// running daemon, via `cecunsplash trigger`), because Wayland compositors do
// not expose portable global key grabs. On GNOME/Mutter/X11 the same trigger
// works too.
const DefaultShortcut = "signal+SIGUSR1"
