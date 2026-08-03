package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ceclinux/cecunsplash/internal/app"
	"github.com/ceclinux/cecunsplash/internal/config"
	"github.com/ceclinux/cecunsplash/internal/hotkey"
	"github.com/ceclinux/cecunsplash/internal/service"
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
	case "trigger":
		err = trigger()
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
	noShortcut := fs.Bool("no-shortcut", false, "disable the manual trigger shortcut for this run")
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
	hotkeyValue := fs.String("hotkey", "", "global shortcut, e.g. shift+control+command+d (macOS) or signal+SIGUSR1 (Linux)")
	shortcut := fs.Bool("shortcut", cfg.ShortcutEnabled, "enable the manual trigger")
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
	if *hotkeyValue != "" {
		cfg.Shortcut = strings.TrimSpace(*hotkeyValue)
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
	hotkeyValue := fs.String("hotkey", "", "global shortcut, e.g. shift+control+command+d (macOS) or signal+SIGUSR1 (Linux)")
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
	if *hotkeyValue != "" {
		cfg.Shortcut = strings.TrimSpace(*hotkeyValue)
	}
	if err := cfg.Validate(); err != nil {
		if config.IsMissingAccessKey(err) {
			fmt.Fprintln(os.Stderr, "warning: no Unsplash access key configured; install the unit now and add one later with: cecunsplash configure --access-key YOUR_KEY")
			fmt.Fprintln(os.Stderr, "         then restart the service:        systemctl --user restart "+service.UnitName())
		} else {
			return err
		}
	}
	// Persist the access key as background services do not inherit the shell env.
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
	return service.Install(exe)
}

func uninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	keepKey := fs.Bool("keep-key", false, "keep Unsplash access key in config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := service.Uninstall(); err != nil {
		// report but continue so the key can still be cleared
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	if !*keepKey {
		if err := deleteAccessKey(); err != nil {
			return err
		}
	}
	return nil
}

// trigger sends SIGUSR1 to the running daemon (if any) to perform an immediate
// wallpaper change. On Linux this is the manual "shortcut"; on platforms without
// the signal-based hotkey it reports an error.
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

func usage() {
	fmt.Println(`cecunsplash - daily Unsplash wallpapers (macOS and Linux)

Usage:
  cecunsplash configure --access-key KEY [--query "mountains"] [--time 02:00] [--hotkey ...]
  cecunsplash now
  cecunsplash run
  cecunsplash install [--access-key KEY] [--hotkey ...]
  cecunsplash uninstall [--keep-key]
  cecunsplash trigger     (Linux manual shortcut: sends SIGUSR1 to the daemon)
  cecunsplash config

Defaults: minimum width 3840 and minimum height 2160, daily change at 02:00.
macOS manual shortcut: shift+control+command+d.
Linux manual shortcut: signal+SIGUSR1, invoked via ` + "`cecunsplash trigger`" + `.
On Linux/Wayland (niri, sway, Hyprland) the wallpaper is set with swaybg;
GNOME uses gsettings; X11 uses feh/xwallpaper/nitrogen.`)
}
