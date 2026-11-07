package main

import (
	"testing"
	"time"
)

// G451: quests/bogspecialization_harvest_full_peat_bog_in_time_period.js 67-92, catalog_buffs.js 149-169.
func TestPeterOutPeat(t *testing.T) {
	r := newObjRig(t)
	w := *sharedWorld
	const bog = "node:peat_1:0"
	w.streets = map[string]map[string]*objSpot{"BOGS": {
		bog:                      {id: bog, kind: "node", class: "peat_1", x: 100, y: 500},
		"node:mortar_barnacle:0": {id: "node:mortar_barnacle:0", kind: "node", class: "mortar_barnacle", x: 150, y: 500},
	}}
	r.mod.w = &w
	a := r.player(1, "Ada", "BOGS", 150, 500)
	b := r.player(2, "Bo", "BOGS", 180, 500)
	far := r.player(3, "Cy", "BOGS", 250, 500)
	r.drain(a)
	r.drain(b)
	r.drain(far)
	start := func(cl *Client, id string) map[string]any {
		r.send(cl, map[string]any{"t": "bog_start", "req": 1, "id": id})
		return r.only(cl, "bog_started")
	}

	// "You need to be near a full Peat Bog.": too far, or not a bog.
	if got := start(far, bog); got["ok"] != false || got["message"] != bogNotClose {
		t.Fatalf("far: %v", got)
	}
	if got := start(a, "node:mortar_barnacle:0"); got["ok"] != false {
		t.Fatalf("a barnacle: %v", got)
	}
	if got := start(a, "tree:9"); got["ok"] != false {
		t.Fatalf("nothing there: %v", got)
	}

	// A bog with a harvest gone is no full bog.
	r.act(b, 1, bog, "harvest", nil)
	r.only(b, "obj_ok")
	r.drain(a)
	r.drain(far)
	if got := start(a, bog); got["ok"] != false || got["message"] != bogNotClose {
		t.Fatalf("not full: %v", got)
	}
	grow := func() { // it grows back, one harvest at a time
		for i := 0; i < 6; i++ {
			r.advance(6 * time.Minute)
		}
	}
	grow()
	r.drain(a)
	r.drain(b)
	r.drain(far)

	// Full: the bog calls to the whole street, and the player alone hears the timer.
	if got := start(a, bog); got["ok"] != true || num(got["ms"]) != 9000 {
		t.Fatalf("start: %v", got)
	}
	for _, cl := range []*Client{b, far} {
		if got := r.only(cl, "bog_call"); got["id"] != bog {
			t.Fatalf("the call: %v", got)
		}
	}
	if got := start(a, bog); got["ok"] != false {
		t.Fatalf("twice at once: %v", got)
	}
	r.drain(a)

	// Three of four gone when the 9 s end: it fails.
	for i := 0; i < 3; i++ {
		r.act(b, 10+i, bog, "harvest", nil)
	}
	r.drain(b)
	r.advance(bogFor - time.Second)
	if got := kinds(r.drain(a), "bog_test"); len(got) != 0 {
		t.Fatalf("early: %v", got)
	}
	r.advance(time.Second)
	if got := r.only(a, "bog_test"); got["emptied"] != false {
		t.Fatalf("not emptied: %v", got)
	}
	if got := kinds(r.drain(b), "bog_test"); len(got) != 0 {
		t.Fatalf("the helper is told nothing: %v", got)
	}
	grow()
	r.drain(a)
	r.drain(b)
	r.drain(far)

	// All four gone in time: the flag.
	if got := start(a, bog); got["ok"] != true {
		t.Fatalf("again: %v", got)
	}
	for i := 0; i < 2; i++ {
		r.act(a, 20+i, bog, "harvest", nil)
		r.act(b, 30+i, bog, "harvest", nil)
	}
	r.drain(b)
	r.drain(a)
	r.advance(bogFor)
	if got := r.only(a, "bog_test"); got["emptied"] != true {
		t.Fatalf("emptied: %v", got)
	}

	// Gone from the street when the 9 s end: the bog is still looked at (apiFindObject), offline nothing is sent.
	grow()
	r.drain(a)
	if got := start(a, bog); got["ok"] != true {
		t.Fatalf("third: %v", got)
	}
	r.drain(a)
	r.send(a, map[string]any{"t": "leave"})
	r.advance(bogFor)
	if got := r.only(a, "bog_test"); got["emptied"] != false {
		t.Fatalf("off the street: %v", got)
	}
}
