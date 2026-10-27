package main

import "testing"

// Hub.Shutdown runs every module's shutdown hook once, holding the hub lock.
func TestShutdownRunsModuleHooksUnderTheLock(t *testing.T) {
	h := NewHub(nil)
	ran := 0
	(&Registry{h: h}).OnShutdown(func() {
		if h.mu.TryLock() {
			t.Fatal("shutdown hook ran without the hub lock")
		}
		ran++
	})
	h.Shutdown()
	if ran != 1 {
		t.Fatalf("hook ran %d times, want 1", ran)
	}
}
