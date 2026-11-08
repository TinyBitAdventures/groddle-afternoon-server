package main

import (
	"math"
	"testing"
	"time"
)

// step fires the fox's current leg (the newest timer shorter than a bait's five minutes, not a spawn), with the
// clock moved on by its length; false when none is due.
func (r *foxRig) step() bool {
	for i := len(r.timers) - 1; i >= 0; i-- {
		tm := r.timers[i]
		if tm.stopped || tm.d >= time.Minute || tm.d == foxSpawn || tm.d == time.Duration(r.m.t.RespawnAfterLeaveMs)*time.Millisecond {
			continue
		}
		tm.stopped = true
		r.now = r.now.Add(tm.d)
		tm.fn()
		return true
	}
	return false
}

// Slice 3 of research/plans/street-animals-shared.md: the server runs a preserve's foxes with the
// game's rules (js/foxes.js foxDestination, foxArrival) on the layout every page draws, so a fox is
// in the same place for everyone and the server eats a bait when it gets there.
func TestTheServerRunsAPreservesFoxForEveryone(t *testing.T) {
	r := newFoxRig(t)
	r.m.roll = func() float64 { return 0.9 } // no turns at baits or markers
	lay, ok := r.m.t.Layouts[r.preserve()]
	if !ok || lay.W <= 0 || len(lay.Markers) == 0 {
		t.Fatalf("data/foxes.json lays out %s: %+v", r.preserve(), lay)
	}
	ps := r.players(2)
	r.ignite(ps[0])
	r.fire(foxSpawn)
	f := r.state().foxes[0]
	walks := kinds(drain(ps[1]), "fox_walk")
	if len(walks) != 1 || num(walks[0]["x0"]) != lay.Spawner || walks[0]["pose"] != "run" || walks[0]["fox"] != f.id {
		t.Fatalf("it sets off from the spawner: %v", walks)
	}
	if want := math.Abs(num(walks[0]["x1"])-lay.Spawner) / foxRunPxMs; math.Abs(num(walks[0]["ms"])-want) > 1 {
		t.Fatalf("at 500 px/s: %v ms for %v px", walks[0]["ms"], num(walks[0]["x1"])-lay.Spawner)
	}
	// Leg after leg, each from where the last ended, until it reaches a bait and eats it for everyone.
	last := num(walks[0]["x1"])
	eaten := 0
	for i := 0; i < 40 && eaten == 0; i++ {
		if !r.step() {
			t.Fatal("the fox stopped running")
		}
		for _, m := range drain(ps[1]) {
			switch m["t"] {
			case "fox_walk":
				if num(m["x0"]) != last {
					t.Fatalf("a leg from %v after one that ended at %v", m["x0"], last)
				}
				if x := num(m["x1"]); x < 0 || x > lay.W+foxExitBeyond {
					t.Fatalf("a leg off the street: %v", m)
				}
				last = num(m["x1"])
			case "fox_bait_gone":
				eaten++
			}
		}
	}
	if eaten != 1 || len(r.state().baits) != 2 {
		t.Fatalf("it ate %d baits, %d left", eaten, len(r.state().baits))
	}
	// Someone arriving gets its current leg.
	c := r.hub.Connect(Claims{Sub: 9, Name: "Cy"})
	r.do(c, map[string]any{"t": "join", "street": r.preserve(), "x": 2000, "y": -100})
	st := kinds(drain(c), "fox_state")
	foxes, _ := st[0]["foxes"].([]any)
	row, _ := foxes[0].(map[string]any)
	if w, _ := row["walk"].(map[string]any); w == nil || num(w["x1"]) != last {
		t.Fatalf("a newcomer's fox_state carries its leg: %v", st)
	}
	// A page's report of an eat is not believed any more: the server eats.
	b := r.state().baits[0].id
	r.do(ps[0], map[string]any{"t": "fox_eat", "fox": f.id, "bait": b})
	if len(r.state().baits) != 2 {
		t.Fatal("a page ate a bait the server's fox never reached")
	}
}

func TestABrushedFoxStandsAndALeavingOneRunsForTheExit(t *testing.T) {
	r := newFoxRig(t)
	ps := r.players(4)
	r.ignite(ps[0])
	r.fire(foxSpawn)
	f := r.state().foxes[0]
	lay := r.m.t.Layouts[r.preserve()]
	drain(ps[3])
	for i := 0; i < 3; i++ {
		r.do(ps[i], map[string]any{"t": "fox_brush", "fox": f.id})
	}
	got := drain(ps[3])
	stands := 0
	for _, m := range kinds(got, "fox_walk") {
		if m["pose"] == "brushed" && num(m["ms"]) == foxBrushedMs {
			stands++
		}
	}
	if stands < 1 || !f.leaving {
		t.Fatalf("brushed, it stands 600 ms; three brushers send it away: stands %d leaving %v", stands, f.leaving)
	}
	// It runs to the exit (500 px past the spawner) and is gone there, for everyone.
	gone := false
	for i := 0; i < 10 && !gone; i++ {
		if !r.step() {
			break
		}
		for _, m := range drain(ps[3]) {
			if m["t"] == "fox_walk" && m["pose"] == "run" {
				if x := num(m["x1"]); math.Abs(math.Abs(x-lay.Spawner)-foxExitBeyond) > 0.5 {
					t.Fatalf("leaving, it runs for %v, not the exit", x)
				}
			}
			gone = gone || m["t"] == "fox_gone"
		}
	}
	if !gone || len(r.state().foxes) != 0 {
		t.Fatal("it never left at the exit")
	}
}
