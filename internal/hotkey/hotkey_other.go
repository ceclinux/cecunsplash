//go:build !darwin && !linux

package hotkey

import (
	"fmt"
	"strings"
)

func Register(ch chan<- struct{}, shortcut string) error {
	return fmt.Errorf("global hotkey is only supported on macOS and Linux")
}

func Stop() {}

func Normalize(shortcut string) string {
	return strings.TrimSpace(shortcut)
}
