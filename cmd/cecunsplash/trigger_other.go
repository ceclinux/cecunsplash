//go:build !linux

package main

import "errors"

// trigger is only used on Linux, where compositors do not expose a portable
// global hotkey and the shortcut invokes `cecunsplash trigger`. macOS registers
// its hotkey in-process and has no daemon pidfile.
func trigger() error {
	return errors.New("cecunsplash trigger is only supported on Linux; use the configured hotkey")
}
