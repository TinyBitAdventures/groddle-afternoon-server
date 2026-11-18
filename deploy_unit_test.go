package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

// Every state file flag must have its path in the live unit (deploy/groddle-realtime.service). A flag
// left out falls back to a file beside -secret-file, which systemd keeps read-only: the Harvest
// Table's saves failed that way from v0.33.0 until v0.34.1 (2026-11-18).
func TestUnitNamesEveryStateFile(t *testing.T) {
	unit, err := os.ReadFile("deploy/groddle-realtime.service")
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	flag.VisitAll(func(f *flag.Flag) {
		if !strings.HasSuffix(f.Name, "-file") || f.Name == "secret-file" {
			return
		}
		if !strings.Contains(text, "-"+f.Name+" /var/lib/groddle-realtime/") {
			t.Errorf("deploy/groddle-realtime.service has no -%s under /var/lib/groddle-realtime", f.Name)
		}
	})
}
