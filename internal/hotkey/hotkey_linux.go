//go:build linux

package hotkey

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// sink receives the manual-trigger notifications forwarded from SIGUSR1.
var sink chan<- struct{}

var (
	hkMu   sync.Mutex
	sigCh  chan os.Signal
	stopCh chan struct{}
)

// pidPath is where the daemon records its PID so `cecunsplash trigger` can send
// SIGUSR1.
func pidPath() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "cecunsplash", "daemon.pid"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "cecunsplash", "daemon.pid"), nil
}

// Register enables the Linux manual trigger: cecunsplash writes its PID to a
// pidfile and listens for SIGUSR1, forwarding each signal to ch so the daemon
// performs an immediate wallpaper change (just like the macOS hotkey). The
// shortcut string is informational on Linux; the real trigger is SIGUSR1 /
// `cecunsplash trigger`.
func Register(ch chan<- struct{}, shortcut string) error {
	hkMu.Lock()
	sink = ch
	sigCh = make(chan os.Signal, 4)
	stopCh = make(chan struct{})
	sigC := sigCh
	stopC := stopCh
	hkMu.Unlock()

	signal.Notify(sigC, syscall.SIGUSR1, syscall.SIGUSR2)

	path, err := pidPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return fmt.Errorf("write daemon pidfile: %w", err)
	}

	go func() {
		for {
			select {
			case <-stopC:
				return
			case _, ok := <-sigC:
				if !ok {
					return
				}
				hkMu.Lock()
				target := sink
				hkMu.Unlock()
				if target == nil {
					continue
				}
				select {
				case target <- struct{}{}:
				default:
				}
			}
		}
	}()
	return nil
}

// Stop unregisters the signal listener and removes the pidfile. It is safe to
// call multiple times.
func Stop() {
	hkMu.Lock()
	if stopCh != nil {
		close(stopCh)
		stopCh = nil
	}
	if sigCh != nil {
		signal.Stop(sigCh)
		sigCh = nil
	}
	sink = nil
	hkMu.Unlock()

	if path, err := pidPath(); err == nil {
		_ = os.Remove(path)
	}
}

// Normalize returns the shortcut string in a tidy form. On Linux the shortcut
// is symbolic, so we only trim whitespace.
func Normalize(shortcut string) string {
	return strings.TrimSpace(shortcut)
}

// ReadDaemonPID returns the PID of the running daemon recorded by Register, or
// an error if no daemon is running. Exposed for the `cecunsplash trigger`
// command (in package main).
func ReadDaemonPID() (int, error) {
	path, err := pidPath()
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse daemon pidfile: %s", strings.TrimSpace(string(data)))
	}
	// Validate that the process exists.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, err
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return 0, fmt.Errorf("daemon (pid %d) is not running: %w", pid, err)
	}
	return pid, nil
}
