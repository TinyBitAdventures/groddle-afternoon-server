package main

import (
	"testing"
	"time"
)

const kittyTestStreet = "GA5215DAMMR2G95"

// Slice 2 of research/plans/street-animals-shared.md: Heli Kitties and Crabs are street animals the
// server shares, by position only: lag and hold, a wait while a player is at one, a verb that holds it.
func TestKittiesAndCrabsAreLaidOutAndShared(t *testing.T) {
	kitties, crabs := 0, 0
	for _, spots := range sharedWorld.streets {
		for _, s := range spots {
			switch s.kind {
			case "kitty":
				kitties++
				if s.class != "npc_kitty_chicken" {
					t.Fatalf("a Heli Kitty's class: %q", s.class)
				}
			case "crab":
				crabs++
			}
		}
	}
	// The archive: 93 Heli Kitty labels on 33 streets, three at most a street; one crab on each of eight streets.
	if kitties < 80 || kitties > 99 || crabs != 8 {
		t.Fatalf("laid out %d Heli Kitties and %d Crabs", kitties, crabs)
	}
	if ms := sharedWorld.rules.Holds["kittyPet"]; ms < 2500 || ms > 3200 {
		t.Fatalf("a pet holds a Heli Kitty for its sleep (3sleepStart and one cycle), not %d ms", ms)
	}
	r := newObjRig(t)
	k := sharedWorld.streets[kittyTestStreet]["kitty:0"]
	if k == nil {
		t.Fatalf("no kitty:0 on %s", kittyTestStreet)
	}
	// A Heli Kitty walks the whole floor: a player far along the street can still reach it.
	a := r.player(1, "Ada", kittyTestStreet, k.x+1500, k.y)
	b := r.player(2, "Bo", kittyTestStreet, k.x, k.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	snap := r.only(a, "objs")
	spots, _ := snap["spots"].(map[string]any)
	at, _ := spots["kitty:0"].([]any)
	animals, _ := snap["animals"].([]any)
	named := false
	for _, id := range animals {
		named = named || id == "kitty:0"
	}
	if !named || len(at) != 2 || num(at[0]) != k.x || num(at[1]) != k.y {
		t.Fatalf("the snapshot names the kitty and gives its spot: %v %v", animals, spots)
	}
	r.drain(b)
	// Ada pets it: it sleeps for everyone, and Bo sees the pet.
	r.act(a, 1, "kitty:0", "pet", nil)
	ok := r.only(a, "obj_ok")
	if st := objOf(ok, "kitty:0"); num(st["hold"]) != float64(sharedWorld.rules.Holds["kittyPet"]) || num(st["lag"]) != float64(sharedWorld.rules.Holds["kittyPet"]) {
		t.Fatalf("a pet holds it for its sleep: %v", ok)
	}
	ev := false
	for _, m := range r.drain(b) {
		ev = ev || (m["t"] == "obj_ev" && m["ev"] == "pet" && m["id"] == "kitty:0" && num(m["by"]) == 1)
	}
	if !ev {
		t.Fatal("Bo did not see the pet")
	}
	// Bo's menu on it: it waits for everyone; a kitty takes no squeeze.
	r.act(b, 1, "kitty:0", "wait", nil)
	r.act(b, 2, "kitty:0", "squeeze", nil)
	if no := r.only(b, "obj_no"); no["code"] != "bad_verb" {
		t.Fatalf("a squeezed Heli Kitty answered %v", no)
	}
	// After its sleep it is forgotten again, nothing kept but how long it stood.
	r.advance(10 * time.Second)
	ob := r.mod.streets[kittyTestStreet]["kitty:0"]
	if ob == nil || ob.lag < sharedWorld.rules.Holds["kittyPet"] {
		t.Fatalf("its path keeps the time it stood: %+v", ob)
	}
}

func TestACrabsVerbHoldsItForEveryoneAndItsMemoryStaysEachPlayers(t *testing.T) {
	r := newObjRig(t)
	c := sharedWorld.streets[defaultStreet]["crab:0"]
	if c == nil {
		t.Fatal("Hechey Track keeps its crab")
	}
	a := r.player(1, "Ada", defaultStreet, c.x+900, c.y)
	b := r.player(2, "Bo", defaultStreet, c.x, c.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	r.drain(b)
	for i, verb := range []string{"flatter", "hug", "insult", "pinch"} {
		r.act(a, i+1, "crab:0", verb, nil)
		ok := r.only(a, "obj_ok")
		if st := objOf(ok, "crab:0"); num(st["hold"]) != 1500 {
			t.Fatalf("a %s holds the crab 1.5 s: %v", verb, ok)
		}
		seen := false
		for _, m := range r.drain(b) {
			seen = seen || (m["t"] == "obj_ev" && m["ev"] == verb && m["id"] == "crab:0")
		}
		if !seen {
			t.Fatalf("Bo did not see the %s", verb)
		}
		r.advance(2 * time.Second)
	}
	// Its state is its path only: no tally, no memory of the tunes it heard.
	st := syncOf(r, b, "crab:0")
	for k := range st {
		if k != "id" && k != "lag" && k != "hold" {
			t.Fatalf("a Crab shares only where it is: %v", st)
		}
	}
	r.act(a, 9, "crab:0", "pet", nil)
	if no := r.only(a, "obj_no"); no["code"] != "bad_verb" {
		t.Fatalf("a petted crab answered %v", no)
	}
}

// A Fire Dragon (slice 3) is a walker like a Crab, held by a pet; none is on a shared street yet.
func TestAFireDragonIsAWalkerHeldByAPet(t *testing.T) {
	if ms := sharedWorld.rules.Holds["dragonPet"]; ms != 2000 {
		t.Fatalf("a pet holds a Fire Dragon 2 s (js/fire-dragon.js menuWaitMs), not %d", ms)
	}
	r := newObjRig(t)
	street := defaultStreet
	spot := &objSpot{id: "dragon:0", kind: "dragon", class: "npc_fire_dragon", x: 2000, y: 850}
	sharedWorld.streets[street]["dragon:0"] = spot
	defer delete(sharedWorld.streets[street], "dragon:0")
	a := r.player(1, "Ada", street, 3500, 850)
	r.send(a, map[string]any{"t": "obj_sync"})
	snap := r.only(a, "objs")
	spots, _ := snap["spots"].(map[string]any)
	if at, _ := spots["dragon:0"].([]any); len(at) != 2 {
		t.Fatalf("its spot rides the snapshot: %v", spots)
	}
	r.act(a, 1, "dragon:0", "pet", nil)
	if st := objOf(r.only(a, "obj_ok"), "dragon:0"); num(st["hold"]) != 2000 {
		t.Fatalf("a pet holds it 2 s: %v", st)
	}
	r.act(a, 2, "dragon:0", "hug", nil)
	if no := r.only(a, "obj_no"); no["code"] != "bad_verb" {
		t.Fatalf("a hugged dragon answered %v", no)
	}
}
