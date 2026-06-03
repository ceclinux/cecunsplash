//go:build !darwin

package hotkey

import (
	"fmt"
	"strings"
)

func Register(ch chan<- struct{}, shortcut string) error {
	return fmt.Errorf("global hotkey is only supported on macOS")
}

func Stop() {}

func Normalize(shortcut string) string {
	return strings.TrimSpace(shortcut)
}
