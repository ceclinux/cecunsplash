//go:build !darwin && !linux

package service

import "fmt"

func Install(exe string) error {
	return fmt.Errorf("background service install is only supported on macOS and Linux")
}

func Uninstall() error {
	return fmt.Errorf("background service install is only supported on macOS and Linux")
}
