//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/ceclinux/cecunsplash/internal/hotkey"
)

// trigger sends SIGUSR1 to the running daemon (if any) to perform an immediate
// wallpaper change. On Linux this backs the compositor-bound manual shortcut.
func trigger() error {
	pid, err := hotkey.ReadDaemonPID()
	if err != nil {
		return fmt.Errorf("no running cecunsplash daemon found: %w", err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGUSR1); err != nil {
		return fmt.Errorf("send trigger to daemon (pid %d): %w", pid, err)
	}
	fmt.Printf("sent manual trigger to daemon (pid %d)\n", pid)
	return nil
}
