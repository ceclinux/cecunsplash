// Package service installs/uninstalls the platform background service. This file
// holds platform-agnostic helpers shared by the build-tag-split files.
package service

import "github.com/ceclinux/cecunsplash/internal/config"

// UnitName is the background service label. On Linux it is a systemd user unit
// (".service"); on macOS it equals the LaunchAgent Label. Keeping one name lets
// the rest of the program stay platform-agnostic.
func UnitName() string { return config.LaunchAgentID + ".service" }
