//go:build !darwin

package wallpaper

import (
	"context"
	"fmt"
)

func CountDesktops(ctx context.Context) (int, error) {
	return 0, fmt.Errorf("desktop wallpaper control is only supported on macOS")
}

func SetDesktops(ctx context.Context, paths []string) error {
	return fmt.Errorf("desktop wallpaper control is only supported on macOS")
}
