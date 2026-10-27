package main

import (
	"testing"
	"time"
)

// A hub with the Rook and the shared street objects together, the way the server runs them.
func newRookTreeRig(t *testing.T) (*rookRig, *objects) {
	t.Helper()
	r := &rookRig{t: t, now: time.UnixMilli(gameEpochMs + 1000*gameDayMs + 3_600_000), roll: 0.5}
	var objs *objects
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { objs = newObjects(h).(*objects); return objs },
		func(h *Hub) Module { r.mod = newRook(h).(*rook); return r.mod },
	}
	defer func() { modules = saved }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	r.mod.roll = func() float64 { return r.roll }
	r.mod.dev = true
	objs.roll = func() float64 { return 0.5 }
	return r, objs
}

func TestARookedTreeLosesItsFruitForEveryoneAndWaitsForReviving(t *testing.T) {
	r, objs := newRookTreeRig(t)
	spot := sharedWorld.streets[rookTestStreet]["tree:0"]
	if spot == nil {
		t.Skip("the Rook's test street has no shared tree")
	}
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	key := spot.class + ":0"
	// A pairs the tree with its key; a pairing of another class, or another tree for the same key, is ignored.
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{key, "npc_piggy:0"}, "objs": map[string]string{key: "tree:0", "npc_piggy:0": "tree:0"}})
	r.send(b, map[string]any{"t": "rook_sync", "keys": []string{key}, "objs": map[string]string{key: "tree:1"}})
	if got := r.mod.streets[rookTestStreet].objs; len(got) != 1 || got[key] != "tree:0" {
		t.Fatalf("pairings %v", got)
	}
	health := spot.health
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.drain(a)
	r.drain(b)
	r.roll = 0.4 // everything rooked
	r.advance(rookWingBeat)
	var tree map[string]any
	for _, f := range r.drain(b) {
		if f["t"] == "objs" {
			tree = objOf(f, "tree:0")
		}
	}
	want := max(1, health-2)
	if health == 2 {
		want = 1
	}
	if tree == nil || num(tree["fruit"]) != 0 || int(num(tree["health"])) != want {
		t.Fatalf("Bo is told the tree lost its fruit and health (onRooked): %v, want health %d", tree, want)
	}
	// While rooked, nobody harvests, pets or waters it.
	r.send(a, map[string]any{"t": "move", "x": spot.x, "y": spot.y})
	r.drain(a)
	r.send(a, map[string]any{"t": "obj_act", "req": 1, "id": "tree:0", "verb": "water"})
	var no map[string]any
	for _, f := range r.drain(a) {
		if f["t"] == "obj_no" {
			no = f
		}
	}
	if no == nil || no["code"] != "rooked" {
		t.Fatalf("watering a rooked tree answered %v", no)
	}
	if !objs.rooked(rookTestStreet, "tree:0") || objs.rooked(rookTestStreet, "tree:1") {
		t.Fatal("only the paired tree reads as rooked")
	}
}

func TestTheModulesFindEachOthersHooks(t *testing.T) {
	_, _ = newRookTreeRig(t)
	h := NewHub(nil)
	if _, ok := h.ProvidedLocked("objects.rookTree").(func(string, string) bool); !ok {
		t.Fatal("objects.rookTree is provided")
	}
	if _, ok := h.ProvidedLocked("rook.treeRooked").(func(string, string) bool); !ok {
		t.Fatal("rook.treeRooked is provided")
	}
	if h.ProvidedLocked("nobody.nothing") != nil {
		t.Fatal("an unknown hook is nil")
	}
}

// G361: a rooked trant grows its fruit back while it waits (include/trants.js 1170-1232: the
// sequence's plusHealth at step 6, then the complaint's drop and lift every 4 s), and keeps it
// when it is revived; harvest still waits for the revival.
func TestARookedTreeGrowsItsFruitBackWhileItWaits(t *testing.T) {
	r, objs := newRookTreeRig(t)
	spot := sharedWorld.streets[rookTestStreet]["tree:0"]
	if spot == nil || spot.kind != "tree" {
		t.Skip("the Rook's test street has no shared trant")
	}
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	key := spot.class + ":0"
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{key}, "objs": map[string]string{key: "tree:0"}})
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.roll = 0.4
	r.advance(rookWingBeat)
	r.drain(a)
	r.drain(b)
	tree := objs.streets[rookTestStreet]["tree:0"]
	if tree == nil || tree.fruit != 0 {
		t.Fatalf("the struck tree reads %+v", tree)
	}
	h, m := tree.health, tree.maturity
	treeFrames := func(cl *Client) []map[string]any {
		var out []map[string]any
		for _, f := range r.drain(cl) {
			if f["t"] == "objs" {
				if o := objOf(f, "tree:0"); o != nil {
					out = append(out, o)
				}
			}
		}
		return out
	}
	r.advance(rookFruitFirst - time.Millisecond)
	if got := treeFrames(b); len(got) != 0 {
		t.Fatalf("fruit came back before step 6: %v", got)
	}
	r.advance(time.Millisecond)
	cap := sharedWorld.capAt(spot.class, m, h)
	want := min(cap, sharedWorld.prodAt(spot.class, m, h))
	got := treeFrames(b)
	if len(got) != 1 || num(got[0]["fruit"]) != float64(want) || int(num(got[0]["health"])) != h {
		t.Fatalf("Bo sees the rooked tree at step 6 as %v, want fruit %d", got, want)
	}
	// The complaint's drop trims to the lower health's capacity, the lift pushes again: it settles.
	settled := min(cap, min(want, sharedWorld.capAt(spot.class, m, h-1))+sharedWorld.prodAt(spot.class, m, h))
	r.advance(rookFruitEvery * 3)
	if tree.fruit != settled || tree.rookStop != nil || !tree.rookSteady {
		t.Fatalf("the rooked tree settles at %d (stop %v, steady %v), want %d", tree.fruit, tree.rookStop != nil, tree.rookSteady, settled)
	}
	// Still no harvest while rooked; a balm revives it and the fruit is there to pick.
	r.send(a, map[string]any{"t": "move", "x": spot.x, "y": spot.y})
	r.drain(a)
	r.send(a, map[string]any{"t": "obj_act", "req": 1, "id": "tree:0", "verb": "harvest"})
	if no := frameOf(r.drain(a), "obj_no"); no == nil || no["code"] != "rooked" {
		t.Fatalf("harvesting a rooked tree answered %v", no)
	}
	r.send(a, map[string]any{"t": "rook_revive", "key": key, "balm": true})
	r.drain(a)
	if objs.rooked(rookTestStreet, "tree:0") {
		t.Fatal("the balm did not revive the tree")
	}
	r.now = r.now.Add(time.Second)
	r.send(a, map[string]any{"t": "obj_act", "req": 2, "id": "tree:0", "verb": "harvest"})
	ok := frameOf(r.drain(a), "obj_ok")
	if ok == nil || num(ok["before"].(map[string]any)["fruit"]) != float64(settled) {
		t.Fatalf("the revived tree's harvest answered %v, want it to start from %d", ok, settled)
	}
	// Revived: no more pushes.
	r.advance(time.Minute)
	if tree.rookStop != nil {
		t.Fatal("a revived tree still has a fruit push armed")
	}
}

func frameOf(frames []map[string]any, kind string) map[string]any {
	for _, f := range frames {
		if f["t"] == kind {
			return f
		}
	}
	return nil
}
