package main

// Whole-state files written off the hub lock (G735). A module's save marshals its state under the
// hub lock, as before, and hands the bytes to writeStateSoon; one goroutine per file writes them
// through writeFileSynced (a temp file, fsync, rename), so no handler waits on the disk. A newer
// state for a file replaces one still waiting (each is the whole state), so the file always ends at
// the newest. Reading a state file (readStateFile) first waits for its write, and the server's
// shutdown waits for every write after the modules' shutdown hooks (main.go), so a restart reads what
// was saved last.
//
// Journals that must be on disk before the act they record (trade.go's trade journal, proofs.go's
// spent tickets) keep their own synced writes.

import (
	"log"
	"sync"
	"testing"
)

type stateWriter struct {
	mu      sync.Mutex
	idle    *sync.Cond
	pending map[string][]byte // newest state per file, not yet written
	busy    map[string]bool   // a goroutine is writing this file
	inPlace bool              // write at once in the caller (go test, unless a test asks otherwise)
}

func newStateWriter(inPlace bool) *stateWriter {
	w := &stateWriter{pending: map[string][]byte{}, busy: map[string]bool{}, inPlace: inPlace}
	w.idle = sync.NewCond(&w.mu)
	return w
}

var stateWrites = newStateWriter(testing.Testing())

// writeStateSoon queues a whole state for path and returns at once (errors are logged by the writer).
func writeStateSoon(path string, b []byte) error {
	return stateWrites.put(path, b)
}

// flushStateWrites waits until path's queued state is on disk ("" waits for every file).
func flushStateWrites(path string) {
	stateWrites.flush(path)
}

func (w *stateWriter) put(path string, b []byte) error {
	if w.inPlace {
		return writeFileSynced(path, b)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending[path] = b
	if !w.busy[path] {
		w.busy[path] = true
		go w.run(path)
	}
	return nil
}

func (w *stateWriter) run(path string) {
	w.mu.Lock()
	for {
		b, ok := w.pending[path]
		if !ok {
			delete(w.busy, path)
			w.idle.Broadcast()
			w.mu.Unlock()
			return
		}
		delete(w.pending, path)
		w.mu.Unlock()
		if err := writeFileSynced(path, b); err != nil {
			log.Printf("state %s: %v", path, err)
		}
		w.mu.Lock()
	}
}

func (w *stateWriter) flush(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		if path == "" {
			if len(w.busy) == 0 {
				return
			}
		} else if !w.busy[path] {
			return
		}
		w.idle.Wait()
	}
}
