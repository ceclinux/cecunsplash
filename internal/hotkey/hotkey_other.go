//go:build !darwin

package hotkey

import "fmt"

func Register(ch chan<- struct{}) error {
	return fmt.Errorf("global hotkey is only supported on macOS")
}

func Stop() {}
