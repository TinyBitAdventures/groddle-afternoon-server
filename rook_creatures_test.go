package main

import (
	"testing"
	"time"
)

// pathClock is where a frame puts a street animal's path (js/street-creatures.js pathTime: the
// server's now, or the hold's end while it holds, minus lag), in server ms.
func pathClock(frame, ob map[string]any) int64 {
	return int64(num(frame["now"]) + num(ob["hold"]) - num(ob["lag"]))
}

// lastObj is the last state of id in the "objs" frames, with its frame.
func lastObj(frames []map[string]any, id string) (map[string]any, map[string]any) {
	var frame, ob map[string]any
	for _, f := range frames {
		if f["t"] != "objs" {
			continue
		}
		if o := objOf(f, id); o != nil {
			frame, ob = f, o
		}
	}
	return frame, ob
}

// Austin 2026-11-04: "when reviving animals, they seem to spawn randomly. Can we have them spawn
// where they're currently stuck at?" A rooked Chicken or Butterfly lies where the Rook struck it
// for everyone (include/rook.js rookAttack 53-63 fullStop, onRooked) and goes on from there when it
// is revived (unRook 82-92, onRevived): its shared path stops while it is rooked.
func TestARookedAnimalLiesWhereItWasStruckAndGoesOnFromThere(t *testing.T) {
	r, objs := newRookTreeRig(t)
	for _, id := range []string{"chicken:0", "butterfly:1"} {
		if sharedWorld.streets[rookTestStreet][id] == nil {
			t.Skipf("the Rook's test street has no %s", id)
		}
	}
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{"npc_chicken:0", "npc_butterfly:1"}})
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.drain(a)
	r.drain(b)
	r.roll = 0.4 // everything rooked
	r.advance(rookWingBeat)
	struck := r.now.UnixMilli() // a fresh animal's path clock is the server's now (lag 0)
	fb := r.drain(b)
	for _, id := range []string{"chicken:0", "butterfly:1"} {
		f, ob := lastObj(fb, id)
		if ob == nil || pathClock(f, ob) != struck || num(ob["hold"]) < float64((rookedHold/2).Milliseconds()) {
			t.Fatalf("Bo is told %s stopped where it was struck: %v", id, ob)
		}
	}
	// Five minutes on, someone arriving finds it in the same place.
	r.advance(5 * time.Minute)
	c := r.player(3, "Cy")
	r.send(c, map[string]any{"t": "obj_sync"})
	if f, ob := lastObj(r.drain(c), "butterfly:1"); ob == nil || pathClock(f, ob) != struck {
		t.Fatalf("Cy's snapshot of the rooked Butterfly: %v", ob)
	}
	// A balm revives it: its path runs on from the point it stopped at, for everyone.
	r.send(a, map[string]any{"t": "rook_revive", "key": "npc_butterfly:1", "balm": true})
	f, ob := lastObj(r.drain(b), "butterfly:1")
	if ob == nil || num(ob["hold"]) != 0 || pathClock(f, ob) != struck {
		t.Fatalf("Bo is told the revived Butterfly goes on from where it lay: %v", ob)
	}
	r.advance(10 * time.Second)
	r.send(c, map[string]any{"t": "obj_sync"})
	if f, ob := lastObj(r.drain(c), "butterfly:1"); ob == nil || pathClock(f, ob) != struck+10_000 {
		t.Fatalf("ten seconds after its revival its path is 10 s on: %v", ob)
	}
	// The Chicken still lies there.
	if ob := objs.streets[rookTestStreet]["chicken:0"]; ob == nil || ob.holdEnd.UnixMilli()-ob.lag != struck {
		t.Fatalf("the rooked Chicken moved: %+v", ob)
	}
}

// A rooked animal the objects never heard of (a restart, a state file, a Rook from before this
// rule) lies still in the next snapshot, and one held for a Rook that no longer has it runs on.
func TestARookedAnimalSettlesFromTheRooksWord(t *testing.T) {
	r, objs := newRookTreeRig(t)
	if sharedWorld.streets[rookTestStreet]["chicken:1"] == nil {
		t.Skip("the Rook's test street has no chicken:1")
	}
	a := r.player(1, "Ada")
	s := r.mod.street(rookTestStreet)
	s.rooked["npc_chicken:1"] = &rookedThing{class: "npc_chicken"}
	r.send(a, map[string]any{"t": "obj_sync"})
	f, ob := lastObj(r.drain(a), "chicken:1")
	at := r.now.UnixMilli()
	if ob == nil || pathClock(f, ob) != at || num(ob["hold"]) < float64((rookedHold/2).Milliseconds()) {
		t.Fatalf("the snapshot holds the rooked Chicken: %v", ob)
	}
	// Its hold is renewed long before it runs out.
	r.now = r.now.Add(rookedHold/2 + time.Hour)
	r.send(a, map[string]any{"t": "obj_sync"})
	if f, ob := lastObj(r.drain(a), "chicken:1"); ob == nil || pathClock(f, ob) != at || num(ob["hold"]) < float64((rookedHold/2).Milliseconds()) {
		t.Fatalf("a long-rooked Chicken still lies there: %v", ob)
	}
	// The Rook forgets it (no revive message): the next read lets it walk on from there.
	delete(s.rooked, "npc_chicken:1")
	r.send(a, map[string]any{"t": "obj_sync"})
	f, ob = lastObj(r.drain(a), "chicken:1")
	if ob == nil || num(ob["hold"]) != 0 || pathClock(f, ob) != at {
		t.Fatalf("released from where it lay: %v", ob)
	}
	// A Chicken sitting on an egg when it is revived holds for the egg still.
	ch := objs.get(rookTestStreet, sharedWorld.streets[rookTestStreet]["chicken:1"])
	s.rooked["npc_chicken:1"] = &rookedThing{class: "npc_chicken"}
	objs.get(rookTestStreet, ch.spot)
	ch.incEgg, ch.incEnd = "chicken_egg", r.now.Add(20*time.Second)
	clock := ch.holdEnd.UnixMilli() - ch.lag
	delete(s.rooked, "npc_chicken:1")
	objs.get(rookTestStreet, ch.spot)
	if !ch.holdEnd.Equal(ch.incEnd) || ch.holdEnd.UnixMilli()-ch.lag != clock {
		t.Fatalf("revived on its egg: hold to %v (egg %v), clock %d want %d", ch.holdEnd, ch.incEnd, ch.holdEnd.UnixMilli()-ch.lag, clock)
	}
}

func TestTheRookAndTheAnimalsFindEachOthersHooks(t *testing.T) {
	_, _ = newRookTreeRig(t)
	h := NewHub(nil)
	if _, ok := h.ProvidedLocked("objects.rookCreature").(func(string, string) bool); !ok {
		t.Fatal("objects.rookCreature is provided")
	}
	if _, ok := h.ProvidedLocked("rook.thingRooked").(func(string, string) bool); !ok {
		t.Fatal("rook.thingRooked is provided")
	}
	for spot, want := range map[*objSpot]string{
		{id: "chicken:3", kind: "chicken"}:      "npc_chicken:3",
		{id: "butterfly:12", kind: "butterfly"}: "npc_butterfly:12",
		{id: "batterfly:0", kind: "batterfly"}:  "",
		{id: "pig:0", kind: "pig"}:              "npc_piggy:0",
	} {
		if got := rookCreatureKey(spot); got != want {
			t.Fatalf("rookCreatureKey(%s) = %q, want %q", spot.id, got, want)
		}
	}
}
