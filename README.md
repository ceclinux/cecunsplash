# cecunsplash

A **macOS and Linux** terminal app written in Go that runs as a background service and changes your wallpaper from [Unsplash](https://unsplash.com).

- On **macOS** it runs as a LaunchAgent and sets a separate image for each Space/workspace.
- On **Linux** it runs as a **systemd user service** and set the wallpaper using the best available backend for your desktop:
  - **Wayland** (niri, sway, Hyprland, …) → [`swaybg`](https://github.com/swaywm/swaybg) (preferred), also `swww` and `wbg`.
  - **GNOME/Unity/Cinnamon** → `gsettings` (`org.gnome.desktop.background`).
  - **X11** → `feh`, `xwallpaper`, or `nitrogen`.

## Features

- Daily wallpaper change at **02:00** by default.
- If the machine is offline at the scheduled time, it waits until network access is available.
- Downloads only Unsplash photos whose original metadata has width at least **3840** and height at least **2160**.
- Avoids recently used Unsplash photo IDs as much as possible to reduce repeated wallpapers.
- Uses a cache directory and removes old wallpaper images after each successful change, keeping only the currently applied files.
- Manual change while the service is running:
  - **macOS:** global hotkey, default **Shift + Control + Command + D** (configurable with `--hotkey`).
  - **Linux:** default manual shortcut **Ctrl + Alt + D**; bind it in your desktop environment/compositor to run `cecunsplash trigger` (Wayland compositors do not expose portable global key grabs). On **niri**, each workspace gets its own wallpaper.
- No third-party Go dependencies.

## Build

```sh
go build -o cecunsplash ./cmd/cecunsplash
```

On macOS the hotkey module uses cgo/Carbon, so build with `CGO_ENABLED=1` (the default on macOS).

## GitHub release builds

GitHub Actions workflows are included in `.github/workflows/`:

- `release.yml` — builds and publishes **macOS** binaries (`cecunsplash-darwin-amd64`, `cecunsplash-darwin-arm64`).
- `release-linux.yml` — builds and publishes **Linux** binaries (`cecunsplash-linux-amd64`, `cecunsplash-linux-arm64`).
- `release-archlinux.yml` — builds and publishes an **Arch Linux pacman package** (`cecunsplash-<version>-1-x86_64.pkg.tar.zst`).

Create a release by pushing a version tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The Linux workflow also runs `go test -race ./...` on Ubuntu before building.

### Arch Linux package

Each tagged GitHub release includes a pacman package for Arch Linux x86_64 users:

```sh
curl -LO https://github.com/ceclinux/cecunsplash/releases/download/v0.1.0/cecunsplash-0.1.0-1-x86_64.pkg.tar.zst
sudo pacman -U ./cecunsplash-0.1.0-1-x86_64.pkg.tar.zst
```

Replace `v0.1.0`/`0.1.0` with the release version you want to install.

## Configure

Create an Unsplash developer app and use its Access Key:

```sh
./cecunsplash configure --access-key YOUR_UNSPLASH_ACCESS_KEY
```

Optional settings:

```sh
./cecunsplash configure \
  --access-key YOUR_UNSPLASH_ACCESS_KEY \
  --query "mountains ocean" \
  --time 02:00 \
  --dir ~/.cache/cecunsplash/wallpapers
```

On macOS you can also set the hotkey:

```sh
./cecunsplash configure --hotkey shift+control+command+d
```

On Linux the default manual shortcut is `ctrl+alt+d`. Bind that shortcut in
your desktop environment/compositor to run `cecunsplash trigger`, which sends
`SIGUSR1` to the daemon.

The default wallpaper cache directories are:

- macOS: `~/Library/Caches/cecunsplash/wallpapers`
- Linux: `~/.cache/cecunsplash/wallpapers`

Configuration is stored at:

- macOS: `~/Library/Application Support/cecunsplash/config.json` (via `os.UserConfigDir()`)
- Linux: `~/.config/cecunsplash/config.json`

## Run once

```sh
./cecunsplash now
```

This downloads a wallpaper and applies it immediately. On Linux/Wayland make
sure `swaybg` is installed (recommended for niri/sway/Hyprland), otherwise the
tool will fall back to `gsettings` on GNOME or report that no supported backend
was found.

## Install background service

### Linux (systemd user service)

```sh
./cecunsplash install --access-key YOUR_UNSPLASH_ACCESS_KEY
```

This writes `~/.config/systemd/user/cecunsplash.service` (label
`com.ceclinux.cecunsplash.service`), enables and starts it, and writes logs to
the **user journal**:

```sh
journalctl --user -u com.ceclinux.cecunsplash.service -f
```

### macOS (LaunchAgent)

```sh
./cecunsplash install --access-key YOUR_UNSPLASH_ACCESS_KEY
```

This installs `~/Library/LaunchAgents/com.ceclinux.cecunsplash.plist`, starts the
service immediately, and writes logs to:

- `~/Library/Logs/cecunsplash.log`
- `~/Library/Logs/cecunsplash.err.log`

## Uninstall

```sh
./cecunsplash uninstall
```

By default, uninstall also deletes the stored Unsplash access key from the config
file. To keep it:

```sh
./cecunsplash uninstall --keep-key
```

## Triggering a manual change (Linux)

While the daemon is running:

```sh
./cecunsplash trigger
```

This sends `SIGUSR1` to the daemon, which performs an immediate wallpaper change
(subject to the same "already changed today" rule as the scheduled change).

## Commands

```text
cecunsplash configure --access-key KEY [--query "mountains"] [--time 02:00] [--hotkey ...] [--dir DIR]
cecunsplash now
cecunsplash run [--no-shortcut]
cecunsplash install [--access-key KEY] [--hotkey ...]
cecunsplash uninstall [--keep-key]
cecunsplash trigger        (Linux: send SIGUSR1 to the running daemon)
cecunsplash config
```

## Linux wallpaper backends

The backend is auto-detected from the environment in this priority order:

1. `WAYLAND_DISPLAY` is set:
   - `swaybg` (managed, restarted per change with `-m fill -i <image>`)
   - `swww` (`swww init` then `swww img`)
   - `wbg` (managed)
2. `gsettings` schema `org.gnome.desktop.background` exists → `gsettings set … picture-uri` / `picture-uri-dark` / `picture-options zoom`.
3. `DISPLAY` is set (X11): `feh --bg-fill`, `xwallpaper --zoom`, or `nitrogen --set-zoom-fill`.

If none is available the command fails with a message listing what to install.

> On Wayland compositors such as **niri**, install `swaybg` for wallpaper changes
> to take effect (e.g. `pacman -S swaybg` on Arch, `apt install swaybg` on Debian).
> The daemon manages its own `swaybg` process via a pidfile under
> `~/.local/state/cecunsplash/` (or `$XDG_STATE_HOME/cecunsplash/`).

## Run the test suite

```sh
go test -race ./...
```