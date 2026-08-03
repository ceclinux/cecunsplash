package service

import (
	"strings"
	"testing"
)

func TestSystemdUnitContent(t *testing.T) {
	exe := "/usr/local/bin/cecunsplash"
	unit := systemdUnit(exe)
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/cecunsplash run") {
		t.Errorf("unit missing ExecStart: %s", unit)
	}
	if !strings.Contains(unit, "[Service]") || !strings.Contains(unit, "[Install]") || !strings.Contains(unit, "[Unit]") {
		t.Errorf("unit missing sections:\n%s", unit)
	}
	if !strings.Contains(unit, "WantedBy=default.target") {
		t.Errorf("unit missing WantedBy: %s", unit)
	}
	if !strings.Contains(unit, "Restart=on-failure") {
		t.Errorf("unit missing Restart: %s", unit)
	}
	if !strings.Contains(unit, "After=network-online.target") {
		t.Errorf("unit missing network dependency: %s", unit)
	}
}

func TestUnitNameAndPath(t *testing.T) {
	if got := UnitName(); !strings.HasSuffix(got, ".service") {
		t.Errorf("unitName = %q, want *.service", got)
	}
	p, err := unitPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, UnitName()) {
		t.Errorf("unitPath %q doesn't end with %q", p, UnitName())
	}
	if !strings.Contains(p, "systemd/user") {
		t.Errorf("unit path %q should be under systemd/user", p)
	}
}
