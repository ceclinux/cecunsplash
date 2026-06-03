//go:build darwin

package wallpaper

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type workspace struct {
	UUID string
}

func CountDesktops(ctx context.Context) (int, error) {
	workspaces, err := activeWorkspaces(ctx)
	if err == nil && len(workspaces) > 0 {
		return len(workspaces), nil
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "osascript", "-e", `tell application "System Events" to get count of desktops`).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("count desktops: %w: %s", err, strings.TrimSpace(string(out)))
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse desktop count %q: %w", strings.TrimSpace(string(out)), err)
	}
	if n < 1 {
		n = 1
	}
	return n, nil
}

func SetDesktops(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("no wallpaper paths provided")
	}

	appleScriptErr := setVisibleDesktops(ctx, paths)
	storeErr := setWallpaperStore(ctx, paths)
	dockDBErr := setDockDatabase(ctx, paths)

	if storeErr == nil {
		// Modern macOS stores per-Space wallpaper in Wallpaper Store. Restarting the
		// wallpaper agents is enough to reload inactive Spaces and does not make the
		// Dock disappear/reappear.
		_ = exec.CommandContext(ctx, "killall", "WallpaperAgent").Run()
		_ = exec.CommandContext(ctx, "killall", "WallpaperImageExtension").Run()
		return nil
	}
	if dockDBErr == nil {
		// Legacy fallback. Older macOS versions only reload desktoppicture.db after
		// Dock restarts, so this path may briefly hide/show the Dock.
		_ = exec.CommandContext(ctx, "killall", "Dock").Run()
		return nil
	}
	if appleScriptErr == nil {
		return nil
	}
	return fmt.Errorf("set visible desktops failed: %v; set wallpaper store failed: %v; set workspace database failed: %v", appleScriptErr, storeErr, dockDBErr)
}

func setVisibleDesktops(ctx context.Context, paths []string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	script := `on run argv
	tell application "System Events"
		set desktopCount to count of desktops
		repeat with i from 1 to desktopCount
			set imagePath to item ((((i - 1) mod (count of argv)) + 1)) of argv
			set picture of desktop i to POSIX file imagePath
		end repeat
	end tell
end run`
	args := append([]string{"-e", script}, paths...)
	out, err := exec.CommandContext(ctx, "osascript", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("set desktops: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func activeWorkspaces(ctx context.Context) ([]workspace, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	plist := filepath.Join(home, "Library", "Preferences", "com.apple.spaces.plist")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "plutil", "-convert", "json", "-o", "-", plist).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("read spaces plist: %w: %s", err, strings.TrimSpace(string(out)))
	}

	var decoded struct {
		SpacesDisplayConfiguration struct {
			ManagementData struct {
				Monitors []struct {
					Spaces []struct {
						UUID string `json:"uuid"`
						Type int    `json:"type"`
					} `json:"Spaces"`
				} `json:"Monitors"`
			} `json:"Management Data"`
		} `json:"SpacesDisplayConfiguration"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var workspaces []workspace
	for _, monitor := range decoded.SpacesDisplayConfiguration.ManagementData.Monitors {
		for _, sp := range monitor.Spaces {
			if sp.UUID == "" || seen[sp.UUID] {
				continue
			}
			// type 0 is a normal user Desktop/Space. Full-screen app spaces do not have desktop wallpaper.
			if sp.Type != 0 {
				continue
			}
			seen[sp.UUID] = true
			workspaces = append(workspaces, workspace{UUID: sp.UUID})
		}
	}
	return workspaces, nil
}

func setWallpaperStore(ctx context.Context, paths []string) error {
	workspaces, err := activeWorkspaces(ctx)
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		return fmt.Errorf("no active workspaces found")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	store := filepath.Join(home, "Library", "Application Support", "com.apple.wallpaper", "Store", "Index.plist")
	if _, err := os.Stat(store); err != nil {
		return err
	}

	args := []string{"-c", wallpaperStorePython, store}
	for i, ws := range workspaces {
		args = append(args, ws.UUID+"="+paths[i%len(paths)])
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("update wallpaper store: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

const wallpaperStorePython = `
import copy
import datetime
import os
import plistlib
import sys
import urllib.parse

store = sys.argv[1]
pairs = []
for arg in sys.argv[2:]:
    uuid, path = arg.split('=', 1)
    pairs.append((uuid, os.path.abspath(os.path.expanduser(path))))

with open(store, 'rb') as f:
    root = plistlib.load(f)

now = datetime.datetime.now()
spaces = root.setdefault('Spaces', {})
top_displays = root.setdefault('Displays', {})

def image_config(path):
    return plistlib.dumps({
        'type': 'imageFile',
        'url': {'relative': 'file://' + urllib.parse.quote(path)},
    }, fmt=plistlib.FMT_BINARY)

def update_entry(entry, path):
    entry.setdefault('Type', 'individual')
    desktop = entry.setdefault('Desktop', {})
    content = desktop.setdefault('Content', {})
    choices = content.setdefault('Choices', [])
    if not choices:
        choices.append({})
    choice = choices[0]
    choice['Provider'] = 'com.apple.wallpaper.choice.image'
    choice['Files'] = []
    choice['Configuration'] = image_config(path)
    content.setdefault('Shuffle', '$null')
    content.setdefault('EncodedOptionValues', '$null')
    desktop['LastSet'] = now
    desktop['LastUse'] = now

# Template for Spaces that exist in Mission Control but do not yet have modern
# wallpaper-store entries.
template_space = None
for value in spaces.values():
    if isinstance(value, dict) and 'Default' in value:
        template_space = value
        break

for uuid, path in pairs:
    if uuid not in spaces:
        spaces[uuid] = copy.deepcopy(template_space) if template_space is not None else {'Default': {'Type': 'individual'}, 'Displays': {}}
    space = spaces[uuid]
    update_entry(space.setdefault('Default', {'Type': 'individual'}), path)

    displays = space.setdefault('Displays', {})
    # Ensure each known display has a per-Space setting. On recent macOS releases,
    # inactive Spaces often read these entries instead of desktoppicture.db.
    for display_id, display_template in top_displays.items():
        displays.setdefault(display_id, copy.deepcopy(display_template) if isinstance(display_template, dict) else {'Type': 'individual'})
    if not displays:
        displays['Main'] = {'Type': 'individual'}
    for display_entry in displays.values():
        if isinstance(display_entry, dict):
            update_entry(display_entry, path)

# Keep top-level display defaults valid, but do not try to assign one image per
# Space there because these defaults are not Space-specific.
if pairs:
    for display_entry in top_displays.values():
        if isinstance(display_entry, dict):
            update_entry(display_entry, pairs[0][1])

tmp = store + '.cecunsplash.tmp'
with open(tmp, 'wb') as f:
    plistlib.dump(root, f, fmt=plistlib.FMT_BINARY)
os.replace(tmp, store)
`

func setDockDatabase(ctx context.Context, paths []string) error {
	workspaces, err := activeWorkspaces(ctx)
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		return fmt.Errorf("no active workspaces found")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	db := filepath.Join(home, "Library", "Application Support", "Dock", "desktoppicture.db")
	if _, err := os.Stat(db); err != nil {
		return err
	}

	var sql strings.Builder
	sql.WriteString("PRAGMA busy_timeout=5000;\nBEGIN IMMEDIATE;\n")
	for i, ws := range workspaces {
		path := paths[i%len(paths)]
		quotedUUID := sqlQuote(ws.UUID)

		// Some Spaces do not have rows in desktoppicture.db until their wallpaper has
		// been changed manually. Create missing rows first; otherwise only the
		// currently visible Space changes via System Events.
		sql.WriteString("INSERT INTO spaces(space_uuid) SELECT ")
		sql.WriteString(quotedUUID)
		sql.WriteString(" WHERE NOT EXISTS (SELECT 1 FROM spaces WHERE space_uuid = ")
		sql.WriteString(quotedUUID)
		sql.WriteString(");\n")

		// One generic per-Space picture plus one per known display. macOS may use
		// either depending on display/Spaces settings and OS version.
		sql.WriteString("INSERT INTO pictures(space_id, display_id) SELECT spaces.rowid, NULL FROM spaces WHERE spaces.space_uuid = ")
		sql.WriteString(quotedUUID)
		sql.WriteString(" AND NOT EXISTS (SELECT 1 FROM pictures WHERE pictures.space_id = spaces.rowid AND pictures.display_id IS NULL);\n")
		sql.WriteString("INSERT INTO pictures(space_id, display_id) SELECT spaces.rowid, displays.rowid FROM spaces CROSS JOIN displays WHERE spaces.space_uuid = ")
		sql.WriteString(quotedUUID)
		sql.WriteString(" AND NOT EXISTS (SELECT 1 FROM pictures WHERE pictures.space_id = spaces.rowid AND pictures.display_id = displays.rowid);\n")

		sql.WriteString("INSERT INTO data(value) VALUES (")
		sql.WriteString(sqlQuote(path))
		sql.WriteString(");\n")
		sql.WriteString("UPDATE preferences SET data_id = last_insert_rowid() WHERE key = 1 AND picture_id IN (SELECT pictures.rowid FROM pictures JOIN spaces ON pictures.space_id = spaces.rowid WHERE spaces.space_uuid = ")
		sql.WriteString(quotedUUID)
		sql.WriteString(");\n")
		sql.WriteString("INSERT INTO preferences(key, data_id, picture_id) SELECT 1, last_insert_rowid(), pictures.rowid FROM pictures JOIN spaces ON pictures.space_id = spaces.rowid WHERE spaces.space_uuid = ")
		sql.WriteString(quotedUUID)
		sql.WriteString(" AND NOT EXISTS (SELECT 1 FROM preferences WHERE key = 1 AND picture_id = pictures.rowid);\n")
	}
	sql.WriteString("COMMIT;\n")

	cmd := exec.CommandContext(ctx, "sqlite3", db)
	cmd.Stdin = strings.NewReader(sql.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("update desktoppicture.db: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
