package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ceclinux/cecunsplash/internal/app"
	"github.com/ceclinux/cecunsplash/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	logger := log.New(os.Stdout, "cecunsplash: ", log.LstdFlags)
	cmd := os.Args[1]
	var err error
	switch cmd {
	case "run":
		err = runDaemon(logger, os.Args[2:])
	case "now":
		err = changeNow(logger, os.Args[2:])
	case "configure":
		err = configure(os.Args[2:])
	case "config":
		err = printConfig()
	case "install":
		err = install(os.Args[2:])
	case "uninstall":
		err = uninstall(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runDaemon(logger *log.Logger, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	noShortcut := fs.Bool("no-shortcut", false, "disable Shift+Control+Command+D for this run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *noShortcut {
		cfg.ShortcutEnabled = false
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Printf("starting background service")
	return app.New(cfg, logger).RunDaemon(ctx)
}

func changeNow(logger *log.Logger, args []string) error {
	fs := flag.NewFlagSet("now", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.New(cfg, logger).ChangeAll(ctx)
}

func configure(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("configure", flag.ExitOnError)
	accessKey := fs.String("access-key", "", "Unsplash API access key")
	query := fs.String("query", "", "Unsplash search query, e.g. 'mountains ocean' ")
	changeTime := fs.String("time", "", "daily change time in HH:MM, default 02:00")
	wallpaperDir := fs.String("dir", "", "directory for downloaded wallpapers")
	shortcut := fs.Bool("shortcut", cfg.ShortcutEnabled, "enable Shift+Control+Command+D")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *accessKey != "" {
		cfg.UnsplashAccessKey = strings.TrimSpace(*accessKey)
	}
	if *query != "" {
		cfg.Query = strings.TrimSpace(*query)
	}
	if *changeTime != "" {
		cfg.ChangeTime = strings.TrimSpace(*changeTime)
	}
	if *wallpaperDir != "" {
		cfg.WallpaperDir = expandHome(*wallpaperDir)
	}
	cfg.ShortcutEnabled = *shortcut
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	path, _ := config.Path()
	fmt.Println("saved", path)
	return nil
}

func printConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	bin := fs.String("bin", "", "path to cecunsplash binary; defaults to current executable")
	accessKey := fs.String("access-key", "", "Unsplash API access key to store for the background service")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *accessKey != "" {
		cfg.UnsplashAccessKey = strings.TrimSpace(*accessKey)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	// Persist the access key as launchd does not inherit the shell environment.
	if err := config.Save(cfg); err != nil {
		return err
	}
	exe := *bin
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return err
		}
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	if _, err := os.Stat(exe); err != nil {
		return err
	}

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
	_ = runCommand("launchctl", "bootout", "gui/"+fmt.Sprint(os.Getuid()), plistPath)
	if err := runCommand("launchctl", "bootstrap", "gui/"+fmt.Sprint(os.Getuid()), plistPath); err != nil {
		return err
	}
	if err := runCommand("launchctl", "enable", "gui/"+fmt.Sprint(os.Getuid())+"/"+config.LaunchAgentID); err != nil {
		return err
	}
	if err := runCommand("launchctl", "kickstart", "-k", "gui/"+fmt.Sprint(os.Getuid())+"/"+config.LaunchAgentID); err != nil {
		return err
	}
	fmt.Println("installed LaunchAgent", plistPath)
	return nil
}

func uninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	keepKey := fs.Bool("keep-key", false, "keep Unsplash access key in config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", config.LaunchAgentID+".plist")
	_ = runCommand("launchctl", "bootout", "gui/"+fmt.Sprint(os.Getuid()), plistPath)
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if !*keepKey {
		if err := deleteAccessKey(); err != nil {
			return err
		}
	}
	fmt.Println("uninstalled", config.LaunchAgentID)
	return nil
}

func deleteAccessKey() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.UnsplashAccessKey == "" {
		return nil
	}
	cfg.UnsplashAccessKey = ""
	if err := config.Save(cfg); err != nil {
		return err
	}
	path, _ := config.Path()
	fmt.Println("deleted Unsplash access key from", path)
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

func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func expandHome(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")
	return replacer.Replace(s)
}

func usage() {
	fmt.Println(`cecunsplash - Unsplash daily workspace wallpapers for macOS

Usage:
  cecunsplash configure --access-key KEY [--query "mountains"] [--time 02:00]
  cecunsplash now
  cecunsplash run
  cecunsplash install --access-key KEY
  cecunsplash uninstall [--keep-key]
  cecunsplash config

Defaults: 3840x2160 minimum images, daily change at 02:00, and manual
Shift+Control+Command+D while the background service is running.`)
}
