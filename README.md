# cecunsplash

A macOS terminal app written in Go that runs as a background LaunchAgent and changes each Desktop/Space wallpaper from Unsplash.

## Features

- Daily wallpaper change at **02:00** by default.
- If the Mac is offline at the scheduled time, it waits until network access is available.
- Downloads only Unsplash photos whose original metadata is at least **3840x2160**.
- Sets a separate image for each macOS Space/workspace detected from Mission Control preferences, with System Events plus Dock wallpaper database support.
- Manual change shortcut while the service is running: **Shift + Control + Command + D**.
- No third-party Go dependencies.

## Build

```sh
go build -o cecunsplash ./cmd/cecunsplash
```

## GitHub release builds

A GitHub Actions workflow is included at `.github/workflows/release.yml`.

It builds and publishes release binaries for:

- `cecunsplash-darwin-amd64` — Intel Macs
- `cecunsplash-darwin-arm64` — Apple Silicon / M-series Macs

Create a release by pushing a version tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The workflow also supports manual runs from the GitHub Actions tab.

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
  --dir ~/Pictures/cecunsplash
```

Configuration is stored at `~/.config/cecunsplash/config.json`.

## Run once

```sh
./cecunsplash now
```

macOS may ask for Automation permission so the terminal/app can control **System Events**.

## Install background service

```sh
./cecunsplash install
```

This installs `~/Library/LaunchAgents/com.ceclinux.cecunsplash.plist`, starts the service immediately, and writes logs to:

- `~/Library/Logs/cecunsplash.log`
- `~/Library/Logs/cecunsplash.err.log`

Uninstall:

```sh
./cecunsplash uninstall
```

## Commands

```text
cecunsplash configure --access-key KEY [--query "mountains"] [--time 02:00]
cecunsplash now
cecunsplash run
cecunsplash install
cecunsplash uninstall
cecunsplash config
```
