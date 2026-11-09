package main

import (
	"testing"
	"time"
)

// G593: a shared Heli Kitty has a Rook key (npc_kitty_chicken:<i>, the game's rookKeyFor from
// heli-kitties-feature's kittyKey), so the Rook stops it where it was struck for everyone and it goes
// on from there when it is revived, as a Chicken, Butterfly or Piggy does. npc_kitty_chicken includes
// items/include/rook.js (npc_kitty_chicken.js 1): fullStop (include/rook.js rookAttack 53-63), onRooked
// 1239-1248, and a rooked kitty is not startled (onPlayerCollision 1207-1210).
func TestARookedHeliKittyLiesWhereItWasStruckForEveryone(t *testing.T) {
	r, objs := newRookTreeRig(t)
	street := kittyTestStreet
	if sharedWorld.streets[street]["kitty:0"] == nil {
		t.Skipf("no kitty:0 on %s", street)
	}
	sp, ok := r.mod.w.streets[street]
	if !ok {
		t.Skipf("%s has no shrine for the Rook", street)
	}
	join := func(id int64, name string) *Client {
		cl := r.hub.Connect(Claims{Sub: id, Name: name})
		r.send(cl, map[string]any{"t": "join", "street": street, "x": sp.x, "y": sp.y})
		r.drain(cl)
		return cl
	}
	a := join(1, "Ada")
	b := join(2, "Bo")
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{"npc_kitty_chicken:0"}})
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.drain(a)
	r.drain(b)
	r.roll = 0.4 // everything rooked
	r.advance(rookWingBeat)
	if !r.mod.thingRooked(street, "npc_kitty_chicken:0") {
		t.Fatal("the wing beat rooked the Heli Kitty")
	}
	struck := r.now.UnixMilli()
	f, ob := lastObj(r.drain(b), "kitty:0")
	if ob == nil || pathClock(f, ob) != struck || num(ob["hold"]) < float64((rookedHold/2).Milliseconds()) {
		t.Fatalf("Bo is told the Heli Kitty stopped where it was struck: %v", ob)
	}
	// Someone arriving later finds it in the same place.
	r.advance(5 * time.Minute)
	c := join(3, "Cy")
	r.send(c, map[string]any{"t": "obj_sync"})
	if f, ob := lastObj(r.drain(c), "kitty:0"); ob == nil || pathClock(f, ob) != struck {
		t.Fatalf("Cy's snapshot of the rooked Heli Kitty: %v", ob)
	}
	// Revived, it flies on from where it lay, for everyone.
	r.send(a, map[string]any{"t": "rook_revive", "key": "npc_kitty_chicken:0", "balm": true})
	f, ob = lastObj(r.drain(b), "kitty:0")
	if ob == nil || num(ob["hold"]) != 0 || pathClock(f, ob) != struck {
		t.Fatalf("Bo is told the revived Heli Kitty goes on from where it lay: %v", ob)
	}
	if live := objs.streets[street]["kitty:0"]; live != nil && live.holdEnd.After(r.now) {
		t.Fatalf("the revived Heli Kitty still holds: %+v", live)
	}
}

func TestAHeliKittyHasARookKey(t *testing.T) {
	if got := rookCreatureKey(&objSpot{id: "kitty:2", kind: "kitty"}); got != "npc_kitty_chicken:2" {
		t.Fatalf("rookCreatureKey(kitty:2) = %q", got)
	}
	for _, kind := range []string{"crab", "dragon", "batterfly"} {
		if got := rookCreatureKey(&objSpot{id: kind + ":0", kind: kind}); got != "" {
			t.Fatalf("the Rook never rooks a %s: %q", kind, got)
		}
	}
	if !rookableClasses["npc_kitty_chicken"] || rookCreatureKinds["npc_kitty_chicken"] != "kitty" {
		t.Fatal("npc_kitty_chicken is rookable and names the street objects' kitty")
	}
}
