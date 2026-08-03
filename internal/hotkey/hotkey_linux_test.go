//go:build linux

package hotkey

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRegisterSignalTriggerAndReadPID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	ch := make(chan struct{}, 2)
	if err := Register(ch, Normalize("signal+SIGUSR1")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer Stop()

	pid, err := ReadDaemonPID()
	if err != nil {
		t.Fatalf("ReadDaemonPID: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("ReadDaemonPID = %d, want %d", pid, os.Getpid())
	}

	// Send SIGUSR1 to ourselves; the handler should forward to ch.
	proc, _ := os.FindProcess(os.Getpid())
	_ = proc.Signal(syscall.SIGUSR1)

	select {
	case <-ch:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive manual-trigger notification after SIGUSR1")
	}
}

func TestStopRemovesPidfile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	ch := make(chan struct{}, 1)
	if err := Register(ch, Normalize("signal+SIGUSR1")); err != nil {
		t.Fatal(err)
	}
	pidPath, _ := pidPath()
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("pidfile not created: %v", err)
	}
	Stop()
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("pidfile still exists after Stop: %v", err)
	}
}

func TestReadDaemonPIDFailsWhenAbsent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state-missing"))
	if _, err := ReadDaemonPID(); err == nil {
		t.Fatal("expected error when no daemon pidfile exists")
	}
}
