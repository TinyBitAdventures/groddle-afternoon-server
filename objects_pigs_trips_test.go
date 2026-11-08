package main

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// hungryPigsNear makes every Piggy of the street sated but the ones named, at hunger 3.
func hungryPigsOnly(r *objRig, street string, ids ...string) {
	r.mod.wakePigs(street)
	for id, ob := range r.mod.streets[street] {
		if ob.spot.kind == "pig" {
			ob.hunger = 0
		}
		for _, want := range ids {
			if id == want {
				ob.hunger = 3
			}
		}
	}
}

// tripOf is the trip in a frame's Piggy state.
func tripOf(ob map[string]any) map[string]any {
	t, _ := ob["trip"].(map[string]any)
	return t
}

// Slice 1 of research/plans/street-animals-shared.md: a Piggy's walk to a tree's fruit is the
// server's, on its path's clock, so two pages see the same Piggy reach the same tree at the same moment.
func TestAPiggysWalkToATreeIsTheSameForEveryone(t *testing.T) {
	r := newObjRig(t)
	street := defaultStreet
	pig := sharedWorld.streets[street]["pig:2"]
	treeID := fruitTreeFor(t, r, street, pig)
	tree := sharedWorld.streets[street][treeID]
	a := r.player(1, "Ada", street, tree.x, tree.y)
	b := r.player(2, "Bo", street, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	snap := r.only(a, "objs")
	homes, _ := snap["homes"].(map[string]any)
	home, _ := homes["pig:2"].([]any)
	if snap["pigtrips"] != true || len(home) != 2 || num(home[0]) != pig.x || num(home[1]) != pig.rng {
		t.Fatalf("the snapshot gives each Piggy's walk: pigtrips %v, home %v", snap["pigtrips"], home)
	}
	if _, ok := snap["trunks"].([]any); !ok {
		t.Fatalf("the snapshot gives the street's trunks: %v", snap["trunks"])
	}
	hungryPigsOnly(r, street, "pig:2")
	ob := r.mod.streets[street]["pig:2"]
	from := r.mod.pigX(street, ob)
	r.drain(b)
	// Ada harvests the tree: the server sends the hungry Piggy, and Bo sees the same trip.
	r.act(a, 1, treeID, "food", nil)
	if f := r.drain(a); len(f) != 1 || f[0]["t"] != "objs" {
		t.Fatalf("Ada hears only the Piggy setting off: %v", f)
	}
	_, seen := lastObj(r.drain(b), "pig:2")
	trip := tripOf(seen)
	if trip == nil || num(trip["x0"]) != math.Round(from*10)/10 || num(trip["c0"]) != float64(r.mod.pathClock(ob)) || num(trip["eat"]) != 3000 {
		t.Fatalf("Bo is told the trip from %v: %v", from, seen)
	}
	leg := time.Duration(num(trip["leg"])) * time.Millisecond
	if want := math.Abs(num(trip["x1"])-num(trip["x0"])) / 42 * 1000; math.Abs(float64(leg.Milliseconds())-want) > 1 {
		t.Fatalf("the walk takes %v at 42 px/s, not %v ms", leg, want)
	}
	// Halfway, Bo opens its menu for 5 s: the walk stands still for everyone (wait).
	r.advance(leg / 2)
	mid := r.mod.pigX(street, ob)
	for i := 0; i < 5; i++ {
		r.act(b, 10+i, "pig:2", "wait", nil)
		r.advance(time.Second)
	}
	if got := r.mod.pigX(street, ob); math.Abs(got-mid) > 30 {
		t.Fatalf("held, it walked on from %v to %v", mid, got)
	}
	fruit := r.mod.get(street, tree).fruit
	r.advance(leg/2 - 100*time.Millisecond)
	if r.mod.get(street, tree).fruit != fruit || ob.hunger == 0 {
		t.Fatal("it ate before it got there")
	}
	r.advance(2 * time.Second)
	ate := false
	for _, m := range r.drain(b) {
		if m["t"] == "obj_ev" && m["ev"] == "ate" && m["id"] == "pig:2" && m["on"] == treeID {
			ate = true
		}
	}
	if !ate || ob.hunger != 0 || r.mod.get(street, tree).fruit != fruit-1 {
		t.Fatalf("on arrival it eats one fruit for everyone: ate %v, hunger %d", ate, ob.hunger)
	}
	// Back where it set off, its path takes it on from there: the trip goes on its lag.
	lag := ob.lag
	for i := 0; i < 1000 && ob.trip != nil; i++ {
		r.advance(50 * time.Millisecond)
	}
	_, back := lastObj(r.drain(b), "pig:2")
	if ob.trip != nil || back == nil || tripOf(back) != nil || ob.lag != lag+2*leg.Milliseconds()+3000 {
		t.Fatalf("back on its path: trip %+v, state %v, lag %d -> %d", ob.trip, back, lag, ob.lag)
	}
	if got := r.mod.pigX(street, ob); math.Abs(got-from) > 3 {
		t.Fatalf("it went on from %v, not where it left its path (%v)", got, from)
	}
}

func TestOnlyHungryPiggiesGoForATreeWithFruit(t *testing.T) {
	r := newObjRig(t)
	street := defaultStreet
	pig := sharedWorld.streets[street]["pig:2"]
	treeID := fruitTreeFor(t, r, street, pig)
	tree := sharedWorld.streets[street][treeID]
	a := r.player(1, "Ada", street, tree.x, tree.y)
	hungryPigsOnly(r, street)
	r.act(a, 1, treeID, "food", nil)
	if f := r.drain(a); len(f) != 0 {
		t.Fatalf("no hungry Piggy, nobody goes: %v", f)
	}
	hungryPigsOnly(r, street, "pig:2")
	r.mod.get(street, tree).fruit = 0
	r.act(a, 2, treeID, "food", nil)
	if f := r.drain(a); len(f) != 0 || r.mod.streets[street]["pig:2"].trip != nil {
		t.Fatalf("a tree with no fruit sends nobody: %v", f)
	}
	r.act(a, 3, "pig:2", "food", nil)
	if no := r.only(a, "obj_no"); no["code"] != "bad_verb" {
		t.Fatalf("food is a tree's word: %v", no)
	}
}

// A Piggy the Rook strikes on its way to food stops there for everyone and walks on when revived.
func TestARookedPiggyStopsOnItsWayToFood(t *testing.T) {
	r := newObjRig(t)
	street := defaultStreet
	rooked := false
	r.hub.provided["rook.thingRooked"] = func(s, key string) bool { return rooked && s == street && key == "npc_piggy:2" }
	pig := sharedWorld.streets[street]["pig:2"]
	treeID := fruitTreeFor(t, r, street, pig)
	tree := sharedWorld.streets[street][treeID]
	a := r.player(1, "Ada", street, tree.x, tree.y)
	hungryPigsOnly(r, street, "pig:2")
	r.act(a, 1, treeID, "food", nil)
	ob := r.mod.streets[street]["pig:2"]
	leg := time.Duration(ob.trip.Leg) * time.Millisecond
	r.advance(leg / 3)
	rooked = true
	r.mod.rookCreature(street, "pig:2")
	at := r.mod.pigX(street, ob)
	r.advance(time.Hour)
	if got := r.mod.pigX(street, ob); got != at || ob.hunger == 0 || !r.mod.pigRooked(street, ob) {
		t.Fatalf("rooked, it stays at %v (now %v, hunger %d)", at, got, ob.hunger)
	}
	rooked = false
	r.mod.rookCreature(street, "pig:2")
	r.advance(leg)
	if ob.hunger != 0 {
		t.Fatalf("revived, it walks on and eats: hunger %d, trip %+v", ob.hunger, ob.trip)
	}
}

// Two hungry Piggies sent to one tree stop on their own spots, not in each other.
func TestPiggiesStopOnTheirOwnSpots(t *testing.T) {
	r := newObjRig(t)
	street := defaultStreet
	var ids []string
	for id, s := range sharedWorld.streets[street] {
		if s.kind == "pig" {
			ids = append(ids, id)
		}
	}
	pig := sharedWorld.streets[street]["pig:2"]
	treeID := fruitTreeFor(t, r, street, pig)
	tree := sharedWorld.streets[street][treeID]
	a := r.player(1, "Ada", street, tree.x, tree.y)
	hungryPigsOnly(r, street, ids...)
	r.act(a, 1, treeID, "food", nil)
	var stops []float64
	for _, id := range ids {
		if t := r.mod.streets[street][id].trip; t != nil {
			stops = append(stops, t.X1)
		}
	}
	if len(stops) < 2 {
		t.Skipf("only %d Piggies in reach of %s", len(stops), treeID)
	}
	for i := range stops {
		if d := math.Abs(stops[i] - tree.x); d < 119 {
			t.Fatalf("a Piggy stops in the trunk: %v", stops)
		}
		for j := i + 1; j < len(stops); j++ {
			if math.Abs(stops[i]-stops[j]) < 60 {
				t.Fatalf("two Piggies on one spot: %v", stops)
			}
		}
	}
}

func TestAPiggysWalkToFoodOutlivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	street := defaultStreet
	pig := sharedWorld.streets[street]["pig:2"]
	treeID := fruitTreeFor(t, r, street, pig)
	tree := sharedWorld.streets[street][treeID]
	a := r.player(1, "Ada", street, tree.x, tree.y)
	hungryPigsOnly(r, street, "pig:2")
	r.act(a, 1, treeID, "food", nil)
	trip := *r.mod.streets[street]["pig:2"].trip
	r.mod.save()
	r2 := r.restart(path, r.now.Add(500*time.Millisecond))
	a2 := r2.player(1, "Ada", street, tree.x, tree.y)
	got := tripOf(syncOf(r2, a2, "pig:2"))
	if got == nil || num(got["x1"]) != trip.X1 || num(got["c0"]) != float64(trip.C0) {
		t.Fatalf("after a restart its trip reads %v (was %+v)", got, trip)
	}
	r2.advance(time.Duration(trip.Leg+trip.Eat+trip.Leg) * time.Millisecond)
	if ob := r2.mod.streets[street]["pig:2"]; ob.trip != nil || ob.hunger != 0 {
		t.Fatalf("after a restart it still eats and comes back: %+v, hunger %d", ob.trip, ob.hunger)
	}
}
