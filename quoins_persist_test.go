package main

import (
	"path/filepath"
	"testing"
	"time"
)

type quoinRig struct {
	hub    *Hub
	mod    *quoinsModule
	now    time.Time
	timers []*objTimer
}

// newQuoinRigAt is a hub with only the quoins module on test data and a state file, its clock at now.
func newQuoinRigAt(t *testing.T, path string, now time.Time) *quoinRig {
	t.Helper()
	saved, savedPath := modules, quoinsFileForTest
	quoinsFileForTest = path
	r := &quoinRig{now: now}
	modules = []moduleFactory{func(h *Hub) Module { r.mod = newQuoinsWith(h, testQuoinData()); return r.mod }}
	defer func() { modules, quoinsFileForTest = saved, savedPath }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	r.mod.randInt = func(lo, hi int) int { return hi }
	return r
}

func (r *quoinRig) advance(d time.Duration) {
	r.now = r.now.Add(d)
	for {
		var next *objTimer
		for _, tm := range r.timers {
			if !tm.done && !tm.at.After(r.now) && (next == nil || tm.at.Before(next.at)) {
				next = tm
			}
		}
		if next == nil {
			return
		}
		next.done = true
		next.fn()
	}
}

func TestGrabbedQuoinsStayGoneAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quoins.json")
	start := time.UnixMilli(gameEpochMs + 2000*gameDayMs + 60_000)
	r := newQuoinRigAt(t, path, start)
	ada := joinAt(r.hub, 1, "Ada", "GQTEST", 1000, touchY)
	send(r.hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0}) // 1800 s
	lastOf(t, ada, "quoin_got")
	send(r.hub, ada, map[string]any{"t": "move", "x": 4000, "y": touchY})
	send(r.hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 3}) // 40 s
	lastOf(t, ada, "quoin_got")
	r.advance(time.Second) // the save
	// Restarted ten seconds on: both are still gone, and Ada's two grabs still count today.
	r = newQuoinRigAt(t, path, r.now.Add(10*time.Second))
	if g := r.mod.grabs[1]; g.n != 2 {
		t.Fatalf("Ada's grabs after the restart: %+v", g)
	}
	bo := joinAt(r.hub, 2, "Bo", "GQTEST", 1000, touchY)
	frames(bo)
	send(r.hub, bo, map[string]any{"t": "quoin_sync", "street": "GQTEST"})
	q := lastOf(t, bo, "quoins")
	if gone, _ := q["gone"].([]any); len(gone) != 2 {
		t.Fatalf("after the restart the street reads %v", q)
	}
	send(r.hub, bo, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0})
	if no := lastOf(t, bo, "quoin_refused"); no["why"] != "gone" {
		t.Fatalf("a grab of a gone quoin answered %v", no)
	}
	// The fast one comes back on its old clock: 40 s from the grab.
	r.advance(28 * time.Second)
	noneOf(t, bo, "quoin_back")
	r.advance(2 * time.Second)
	if back := lastOf(t, bo, "quoin_back"); back["i"].(float64) != 3 {
		t.Fatalf("back %v", back)
	}
	// Down past the slow one's half hour: it is back on the next start, and a new game day starts the count again.
	r.advance(time.Second)
	r = newQuoinRigAt(t, path, r.now.Add(5*time.Hour))
	cy := joinAt(r.hub, 3, "Cy", "GQTEST", 1000, touchY)
	frames(cy)
	send(r.hub, cy, map[string]any{"t": "quoin_sync", "street": "GQTEST"})
	if gone, _ := lastOf(t, cy, "quoins")["gone"].([]any); len(gone) != 0 || len(r.mod.grabs) != 0 {
		t.Fatalf("five hours on: gone %v, grabs %v", gone, r.mod.grabs)
	}
}
