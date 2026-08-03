//go:build linux

package config

// DefaultShortcut is the suggested Linux keybinding for manually changing the
// wallpaper. cecunsplash still receives the trigger via SIGUSR1, so bind this
// shortcut in your desktop environment/compositor to run `cecunsplash trigger`.
const DefaultShortcut = "ctrl+alt+d"
