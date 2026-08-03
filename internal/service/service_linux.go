//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func unitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", UnitName()), nil
}

// Install writes a systemd user unit, reloads the user manager, then enables and
// starts it. Standard output/error go to the user journal (journalctl --user).
func Install(exe string) error {
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	unit := systemdUnit(exe)
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "enable", "--now", UnitName()); err != nil {
		return err
	}
	fmt.Println("installed systemd user unit", path)
	fmt.Println("view logs with: journalctl --user -u " + UnitName())
	return nil
}

// Uninstall stops, disables and removes the systemd user unit.
func Uninstall() error {
	_ = run("systemctl", "--user", "stop", UnitName())
	_ = run("systemctl", "--user", "disable", UnitName())
	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = run("systemctl", "--user", "daemon-reload")
	fmt.Println("uninstalled", UnitName())
	return nil
}

func systemdUnit(exe string) string {
	var b strings.Builder
	fmt.Fprintln(&b, "[Unit]")
	fmt.Fprintln(&b, "Description=cecunsplash - daily Unsplash wallpaper")
	fmt.Fprintln(&b, "Documentation=https://github.com/ceclinux/cecunsplash")
	fmt.Fprintln(&b, "After=network-online.target")
	fmt.Fprintln(&b, "Wants=network-online.target")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Service]")
	fmt.Fprintln(&b, "Type=simple")
	fmt.Fprintf(&b, "ExecStart=%s run\n", exe)
	fmt.Fprintln(&b, "Restart=on-failure")
	fmt.Fprintln(&b, "RestartSec=30")
	fmt.Fprintln(&b, "StandardOutput=journal")
	fmt.Fprintln(&b, "StandardError=journal")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "[Install]")
	fmt.Fprintln(&b, "WantedBy=default.target")
	return b.String()
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
