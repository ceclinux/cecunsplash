//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ceclinux/cecunsplash/internal/config"
)

// Install registers and starts the macOS LaunchAgent.
func Install(exe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	logDir := filepath.Join(home, "Library", "Logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", config.LaunchAgentID+".plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	plist := launchAgentPlist(exe, filepath.Join(logDir, "cecunsplash.log"), filepath.Join(logDir, "cecunsplash.err.log"))
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return err
	}
	uid := fmt.Sprint(os.Getuid())
	_ = runCommand("launchctl", "bootout", "gui/"+uid, plistPath)
	if err := runCommand("launchctl", "bootstrap", "gui/"+uid, plistPath); err != nil {
		return err
	}
	if err := runCommand("launchctl", "enable", "gui/"+uid+"/"+config.LaunchAgentID); err != nil {
		return err
	}
	if err := runCommand("launchctl", "kickstart", "-k", "gui/"+uid+"/"+config.LaunchAgentID); err != nil {
		return err
	}
	fmt.Println("installed LaunchAgent", plistPath)
	return nil
}

// Uninstall stops and removes the macOS LaunchAgent.
func Uninstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", config.LaunchAgentID+".plist")
	_ = runCommand("launchctl", "bootout", "gui/"+fmt.Sprint(os.Getuid()), plistPath)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("uninstalled", config.LaunchAgentID)
	return nil
}

func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func launchAgentPlist(exe, stdoutPath, stderrPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, config.LaunchAgentID, xmlEscape(exe), xmlEscape(stdoutPath), xmlEscape(stderrPath))
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")
	return replacer.Replace(s)
}
