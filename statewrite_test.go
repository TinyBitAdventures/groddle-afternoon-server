package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStateWritesLeaveTheCallerAndEndAtTheNewestState(t *testing.T) {
	w := newStateWriter(false)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				w.put(b, []byte(fmt.Sprintf(`{"from":%d}`, i)))
			}
		}(i)
	}
	for n := 0; n <= 200; n++ {
		w.put(a, []byte(fmt.Sprintf(`{"n":%d}`, n)))
	}
	w.flush(a)
	if got, _ := os.ReadFile(a); string(got) != `{"n":200}` {
		t.Fatalf("a ends at %s", got)
	}
	wg.Wait()
	w.flush("")
	if got, err := os.ReadFile(b); err != nil || len(got) == 0 {
		t.Fatalf("b: %s %v", got, err)
	}
	w.mu.Lock()
	idle := len(w.busy) == 0 && len(w.pending) == 0
	w.mu.Unlock()
	if !idle {
		t.Fatal("the writer is idle after a flush")
	}
}

func TestAStateSavedOffTheLockIsReadBackAfterARestart(t *testing.T) {
	saved := stateWrites
	stateWrites = newStateWriter(false)
	defer func() { stateWrites = saved }()
	path := filepath.Join(t.TempDir(), "quoins.json")
	start := time.UnixMilli(gameEpochMs + 2000*gameDayMs + 60_000)
	r := newQuoinRigAt(t, path, start)
	ada := joinAt(r.hub, 1, "Ada", "GQTEST", 1000, touchY)
	send(r.hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0})
	lastOf(t, ada, "quoin_got")
	r.advance(time.Second) // the save, handed to the writer
	r = newQuoinRigAt(t, path, r.now.Add(10*time.Second))
	if g := r.mod.grabs[1]; g.n != 1 {
		t.Fatalf("Ada's grab after the restart: %+v", g)
	}
}
