package main

import (
	"testing"
	"time"
)

// fireLater runs the timers armed for later (the crew's idle watch among them); each checks its own time.
func (r *projRig) fireLater() {
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if tm.d > 0 && !tm.stopped {
			tm.stopped = true
			tm.fn()
		}
	}
}

// The street sees each worker's tool bubble (items/include/jobs.js startJobWork 293-307: a
// pc_overlay sent with apiSendAnnouncementX(anncx, pc), everyone on the street but the worker),
// from one message at the start and one at the end, never one a unit.
func TestProjectCrewToolBubblesReachTheStreet(t *testing.T) {
	r := newProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	r.m.ledger = nil
	r.m.trusted = true
	a := r.player(1, "Ada", projBoard)
	b := r.player(2, "Bo", projBoard)
	c := r.player(3, "Cy", "GOTHER")
	start := map[string]any{"t": "project_work_start", "job": projJobID, "req": "r138", "tool": "hatchet"}
	tick := func() { r.now = r.now.Add(1500 * time.Millisecond) }
	working := func(cl *Client) []map[string]any { return kinds(drain(cl), "project_working") }
	drain(a)
	drain(b)
	drain(c)

	// r138 is closed until bucket 1's group 1 is done: no bubble, and no error (one would stop Ada's own work).
	r.do(a, start)
	if got := drain(a); len(got) != 0 {
		t.Fatalf("a refused start is silent: %v", got)
	}
	if got := working(b); len(got) != 0 {
		t.Fatalf("no bubble for closed work: %v", got)
	}
	for _, q := range r.reqs() {
		if q.B == "1" && q.G == 1 {
			r.now = r.now.Add(time.Second)
			r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": q.ID, "n": q.Need, "currants": q.Need})
		}
	}
	drain(a)
	drain(b)

	// Not a tool word, or away from the board: nothing.
	tick()
	r.do(a, map[string]any{"t": "project_work_start", "job": projJobID, "req": "r138", "tool": "../hatchet"})
	r.do(c, start)
	if got := append(working(b), drain(c)...); len(got) != 0 {
		t.Fatalf("refused starts: %v", got)
	}

	tick()
	r.do(a, start)
	if got := working(b); len(got) != 1 || num(got[0]["id"]) != 1 || got[0]["tool"] != "hatchet" {
		t.Fatalf("Bo sees Ada's hatchet: %v", got)
	}
	if got := drain(a); len(got) != 0 {
		t.Fatalf("the worker draws its own bubble: %v", got)
	}
	if got := drain(c); len(got) != 0 {
		t.Fatalf("another street hears nothing: %v", got)
	}
	r.do(a, start)
	if got := working(b); len(got) != 0 {
		t.Fatalf("the same start again is not news: %v", got)
	}

	// Units keep the bubble up and send nothing more to the street but the board's rows.
	for i := 0; i < 4; i++ {
		tick()
		r.do(a, map[string]any{"t": "project_work", "job": projJobID, "req": "r138"})
		r.fireLater()
	}
	if got := working(b); len(got) != 0 {
		t.Fatalf("no message a unit: %v", got)
	}

	// Dee arrives and is told who is working.
	d := r.player(4, "Dee", projBoard)
	ws := kinds(drain(d), "project_workers")
	if len(ws) != 1 {
		t.Fatalf("project_workers on join: %v", ws)
	}
	list := ws[0]["list"].([]any)
	if len(list) != 1 || num(list[0].(map[string]any)["id"]) != 1 || list[0].(map[string]any)["tool"] != "hatchet" {
		t.Fatalf("Ada's hatchet in the list: %v", list)
	}
	drain(b)

	// cancelJobWork 435-438: the stop takes it down; a second stop is not news.
	tick()
	r.do(a, map[string]any{"t": "project_work_stop"})
	r.do(a, map[string]any{"t": "project_work_stop"})
	got := working(b)
	if len(got) != 1 || num(got[0]["id"]) != 1 || got[0]["tool"] != "" {
		t.Fatalf("Bo sees the bubble go: %v", got)
	}
	if got := working(d); len(got) != 1 {
		t.Fatalf("Dee too: %v", got)
	}

	// Leaving the street takes it down there.
	tick()
	r.do(a, start)
	working(b)
	r.do(a, map[string]any{"t": "join", "street": "GOTHER"})
	if got := working(b); len(got) != 1 || got[0]["tool"] != "" {
		t.Fatalf("down when Ada leaves: %v", got)
	}
	if got := kinds(drain(c), "project_workers"); len(got) != 0 {
		t.Fatalf("Ada's bubble does not follow her: %v", got)
	}

	// A game that stops sending units without saying so: down after projectCrewIdle.
	r.do(a, map[string]any{"t": "join", "street": projBoard})
	tick()
	r.do(a, start)
	working(b)
	r.now = r.now.Add(projectCrewIdle / 2)
	r.fireLater()
	if got := working(b); len(got) != 0 {
		t.Fatalf("still up at half the idle time: %v", got)
	}
	r.now = r.now.Add(projectCrewIdle)
	r.fireLater()
	if got := working(b); len(got) != 1 || got[0]["tool"] != "" {
		t.Fatalf("down once idle: %v", got)
	}

	// Going offline takes it down.
	tick()
	r.do(a, start)
	working(b)
	r.hub.Disconnect(a)
	if got := working(b); len(got) != 1 || got[0]["tool"] != "" {
		t.Fatalf("down when Ada goes offline: %v", got)
	}
	if len(r.m.crew) != 0 {
		t.Fatalf("nobody left working: %v", r.m.crew)
	}
}

// Starts and stops are paced: a client flipping its bubble cannot flood the street.
func TestProjectCrewIsPaced(t *testing.T) {
	r := newProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	r.m.ledger = nil
	r.m.trusted = true
	a := r.player(1, "Ada", projBoard)
	b := r.player(2, "Bo", projBoard)
	for _, q := range r.reqs() {
		if q.B == "1" && q.G == 1 {
			r.now = r.now.Add(time.Second)
			r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": q.ID, "n": q.Need, "currants": q.Need})
		}
	}
	r.now = r.now.Add(10 * time.Second)
	drain(a)
	drain(b)
	for i := 0; i < 20; i++ {
		tool := "hatchet"
		if i%2 == 1 {
			tool = "class_axe"
		}
		r.do(a, map[string]any{"t": "project_work_start", "job": projJobID, "req": "r138", "tool": tool})
	}
	if got := kinds(drain(b), "project_working"); len(got) != projectCrewBurst {
		t.Fatalf("%d bubbles at once, want %d", len(got), projectCrewBurst)
	}
}
