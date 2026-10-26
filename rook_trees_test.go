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
