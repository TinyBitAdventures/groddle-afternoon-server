package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// placedStacks records what the objects module asks drops.go to place (a Plop, a Piglet).
type placedStack struct {
	street string
	by     int64
	x      float64
	item   tradeItem
}

func recordPlaced(r *objRig) *[]placedStack {
	var got []placedStack
	r.hub.provided["drops.place"] = func(street string, by int64, name string, x, y float64, it tradeItem) bool {
		got = append(got, placedStack{street: street, by: by, x: x, item: it})
		return true
	}
	return &got
}

// nextGameDay moves the rig's clock past the next game-day turn, running what fell due.
func nextGameDay(r *objRig) {
	now := r.now.UnixMilli()
	day := (now - gameEpochMs) / gameDayMs
	r.advance(time.Duration(gameEpochMs+(day+1)*gameDayMs-now+100) * time.Millisecond)
}

// G605: capture (npc_piggy.js verbs.capture 361-541) takes a street Piggy off the street for
// everyone; it comes back at the next game day (ours).
func TestACapturedStreetPiggyLeavesForEveryoneUntilTheNextGameDay(t *testing.T) {
	r, l := usesRig(t)
	got := r.credited()
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	b := r.player(2, "Bo", defaultStreet, pig.x+100, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	herd := map[string]any{"perks": []string{"herd"}}
	r.act(a, 1, "pig:2", "capture", nil)
	if no := r.only(a, "obj_no"); no["code"] != "skill" || no["message"] != pigTextHerd {
		t.Fatalf("a capture without Herdkeeping answered %v", no)
	}
	r.act(a, 2, "pig:2", "capture", herd)
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "no_bait" || no["message"] != pigTextBaitless {
		t.Fatalf("a capture without bait answered %v", no)
	}
	l.give(1, "pig_bait", 2)
	// 439: a 75% roll; 0.9 misses, the bait spent all the same.
	r.mod.roll = func() float64 { return 0.9 }
	r.now = r.now.Add(time.Second)
	r.act(a, 3, "pig:2", "capture", herd)
	r.advance(0)
	ok := r.only(a, "obj_ok")
	if ok["success"] != false || objOf(ok, "pig:2")["gone"] != nil || l.held[1]["pig_bait"] != 1 {
		t.Fatalf("a missed capture answered %v (bait %d)", ok, l.held[1]["pig_bait"])
	}
	r.drain(b)
	r.mod.roll = func() float64 { return 0.5 }
	r.now = r.now.Add(time.Second)
	r.act(a, 4, "pig:2", "capture", herd)
	r.advance(0)
	ok = r.only(a, "obj_ok")
	if ok["success"] != true || objOf(ok, "pig:2")["gone"] != true || l.held[1]["pig_bait"] != 0 {
		t.Fatalf("a capture answered %v", ok)
	}
	if len(*got) != 1 || (*got)[0] != "1:take:hogtied_piggyx1" {
		t.Fatalf("credited %v", *got)
	}
	var sawGone, sawEv bool
	for _, f := range r.drain(b) {
		if f["t"] == "objs" && objOf(f, "pig:2")["gone"] == true {
			sawGone = true
		}
		if f["t"] == "obj_ev" && f["ev"] == "capture" && num(f["by"]) == 1 {
			sawEv = true
		}
	}
	if !sawGone || !sawEv {
		t.Fatalf("Bo saw gone %v, the capture %v", sawGone, sawEv)
	}
	// Gone for Bo too: his verbs on it are refused.
	r.act(b, 1, "pig:2", "pet", map[string]any{"skill": "no_skill"})
	if no := r.only(b, "obj_no"); no["code"] != "gone" {
		t.Fatalf("Bo's pet on a captured Piggy answered %v", no)
	}
	// 383-386: one capture per player per street per 42 "game days" (Ada walks over to pig:3 first, so
	// the answer is the limit and never the reach, wherever the planner lays the Piggies).
	other := sharedWorld.streets[defaultStreet]["pig:3"]
	r.send(a, map[string]any{"t": "move", "x": other.x, "y": other.y})
	r.drain(a)
	l.give(1, "pig_bait", 1)
	r.now = r.now.Add(time.Second)
	r.act(a, 5, "pig:3", "capture", herd)
	if no := r.only(a, "obj_no"); no["code"] != "once_here" || no["message"] != pigTextOncePlace {
		t.Fatalf("a second capture here answered %v", no)
	}
	// Ours: back at its spot at the next game day, a new Piggy.
	r.drain(b)
	nextGameDay(r)
	back := false
	for _, f := range r.drain(b) {
		if p := objOf(f, "pig:2"); f["t"] == "objs" && p != nil && p["gone"] == nil && num(p["mood"]) == 1 && num(p["hunger"]) == 0 {
			back = true
		}
	}
	if !back {
		t.Fatal("the captured Piggy did not come back at the next game day")
	}
	r.act(b, 2, "pig:2", "pet", map[string]any{"skill": "no_skill"})
	if ok := r.only(b, "obj_ok"); ok["success"] != true {
		t.Fatalf("Bo's pet on the Piggy back again answered %v", ok)
	}
}

// A sad Piggy is always caught (439-444), and a sad street keeps no capture limit (383-386).
func TestASadStreetsPiggyIsAlwaysCaught(t *testing.T) {
	r, l := usesRig(t)
	street := ""
	for tsid := range sharedWorld.sad {
		if sharedWorld.streets[tsid]["pig:0"] != nil && (street == "" || tsid < street) {
			street = tsid
		}
	}
	if street == "" {
		t.Fatal("no sad street lays out a Piggy")
	}
	pig := sharedWorld.streets[street]["pig:0"]
	a := r.player(1, "Ada", street, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	l.give(1, "pig_bait", 1)
	r.mod.setCaptured(street, 1) // caught one here a minute ago: no limit on a sad street
	r.mod.roll = func() float64 { return 0.99 }
	r.act(a, 1, "pig:0", "capture", map[string]any{"perks": []string{"herd"}})
	r.advance(0)
	if ok := r.only(a, "obj_ok"); ok["success"] != true || objOf(ok, "pig:0")["gone"] != true {
		t.Fatalf("a sad Piggy at a 0.99 roll answered %v", ok)
	}
}

// G605: a Potion of Animal Youth on a street Piggy (potion_animal_youth.js onPour 547-569): the
// pour through the ledger, the Piggy gone for everyone, a Piglet on the ground for everyone.
func TestAYouthPotionLeavesAPigletForEveryone(t *testing.T) {
	r, l := usesRig(t)
	placed := recordPlaced(r)
	pig := sharedWorld.streets[defaultStreet]["pig:1"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	b := r.player(2, "Bo", defaultStreet, pig.x+100, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	r.act(a, 1, "pig:1", "youth", map[string]any{"k": 0})
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "no_item" {
		t.Fatalf("a pour with no bottle answered %v", no)
	}
	l.give(1, useYouth, 1)
	r.now = r.now.Add(time.Second)
	r.act(a, 2, "pig:1", "youth", map[string]any{"k": 0})
	r.advance(0)
	ok := r.only(a, "obj_ok")
	use, _ := ok["use"].(map[string]any)
	if objOf(ok, "pig:1")["gone"] != true || use == nil || len(l.copies) != 2 || l.copies[1] != useYouth+" #0" {
		t.Fatalf("the pour answered %v (copies %v)", ok, l.copies)
	}
	if len(*placed) != 1 || (*placed)[0].item.C != "piglet" || (*placed)[0].by != 1 || (*placed)[0].street != defaultStreet {
		t.Fatalf("placed %v", *placed)
	}
	var ev map[string]any
	for _, f := range r.drain(b) {
		if f["t"] == "obj_ev" {
			ev = f
		}
	}
	if ev == nil || ev["ev"] != "youth" || num(ev["by"]) != 1 {
		t.Fatalf("Bo heard %v", ev)
	}
	r.act(a, 3, "pig:1", "youth", nil)
	if no := r.only(a, "obj_no"); no["code"] != "gone" {
		t.Fatalf("a second pour on the gone Piggy answered %v", no)
	}
}

// G605: crops fed to a street Piggy are counted on the Piggy, whoever feeds it, and its Plops lie
// on the ground for everyone with their seeds as their words (npc_piggy.js feed 685-729, doPlop
// 1315-1350).
func TestAFedStreetPiggyPlopsForEveryone(t *testing.T) {
	r, l := usesRig(t)
	placed := recordPlaced(r)
	pig := sharedWorld.streets[defaultStreet]["pig:0"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	b := r.player(2, "Bo", defaultStreet, pig.x+50, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	l.give(1, "potato", 9)
	l.give(2, "potato", 9)
	r.mod.roll = func() float64 { return 0.99 } // randInt(2,7) rolls 7
	// Not hungry: a crop still goes through the ledger now, its count toward a Plop.
	r.act(a, 1, "pig:0", "feed", map[string]any{"c": "potato", "n": 2})
	r.advance(0)
	r.only(a, "obj_ok")
	if l.held[1]["potato"] != 7 || len(*placed) != 0 {
		t.Fatalf("two Potatoes: held %d, placed %v", l.held[1]["potato"], *placed)
	}
	// Bo's one makes three on the Piggy: a Plop of round(1/3) = 0 places nothing (ours).
	r.now = r.now.Add(time.Second)
	r.act(b, 1, "pig:0", "feed", map[string]any{"c": "potato", "n": 1})
	r.advance(0)
	r.only(b, "obj_ok")
	if len(*placed) != 0 || r.mod.streets[defaultStreet]["pig:0"].crops["seed_potato"] != 0 {
		t.Fatalf("the third Potato: placed %v, crops %v", *placed, r.mod.streets[defaultStreet]["pig:0"].crops)
	}
	// Nine at once: three Plops' worth of seeds (round(9/3) = 3), one Plop of 3 seeds (randInt capped).
	r.now = r.now.Add(time.Second)
	l.give(1, "potato", 2)
	r.act(a, 2, "pig:0", "feed", map[string]any{"c": "potato", "n": 9})
	r.advance(0)
	r.only(a, "obj_ok")
	if len(*placed) != 1 {
		t.Fatalf("nine Potatoes placed %v", *placed)
	}
	p := (*placed)[0]
	var tag plopTag
	if json.Unmarshal(p.item.T, &tag) != nil || p.item.C != "plop" || p.by != 1 || tag.Seed != "seed_potato" || tag.N != 3 {
		t.Fatalf("the Plop: %+v %s", p, p.item.T)
	}
	var ev map[string]any
	for _, f := range r.drain(b) {
		if f["t"] == "obj_ev" && f["ev"] == "plop" {
			ev = f
		}
	}
	if ev == nil || num(ev["by"]) != 1 || ev["seed"] != "seed_potato" || num(ev["n"]) != 3 {
		t.Fatalf("Bo heard %v", ev)
	}
	// A pumpkin queues count x 2 seeds: two Plops of up to 7, 5 s apart (1327).
	l.give(1, "pumpkin", 1)
	r.mod.roll = func() float64 { return 0 } // randInt(2,7) rolls 2
	r.now = r.now.Add(time.Second)
	r.act(a, 3, "pig:0", "feed", map[string]any{"c": "pumpkin", "n": 1})
	r.advance(0)
	r.only(a, "obj_ok")
	if len(*placed) != 2 {
		t.Fatalf("a pumpkin placed %d Plops at once", len(*placed)-1)
	}
	r.advance(plopEvery)
	if len(*placed) != 2 {
		t.Fatalf("a pumpkin's two seeds made %d Plops", len(*placed)-1)
	}
}

// The captures, a gone Piggy and the Plops still to come outlive a restart.
func TestAGonePiggyAndItsPlopsOutliveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	ob := r.mod.get(defaultStreet, pig)
	r.mod.pigLeaves(defaultStreet, ob, "capture")
	r.mod.setCaptured(defaultStreet, 1)
	other := r.mod.get(defaultStreet, sharedWorld.streets[defaultStreet]["pig:3"])
	other.crops = map[string]int{"seed_carrot": 2}
	other.plops = []plopEntry{{Seed: "seed_carrot", Num: 4, By: 1, Name: "Ada"}}
	r.mod.saveSoon()
	r2 := r.restart(path, r.now.Add(time.Minute))
	a := r2.player(1, "Ada", defaultStreet, pig.x, pig.y)
	if p := syncOf(r2, a, "pig:2"); p["gone"] != true {
		t.Fatalf("after a restart the captured Piggy reads %v", p)
	}
	if !r2.mod.capturedHereLately(defaultStreet, 1) {
		t.Fatal("the capture's place-time was lost")
	}
	o := r2.mod.streets[defaultStreet]["pig:3"]
	if o.crops["seed_carrot"] != 2 || len(o.plops) != 1 || o.plops[0].Num != 4 {
		t.Fatalf("after a restart pig:3 reads crops %v plops %v", o.crops, o.plops)
	}
	nextGameDay(r2)
	if p := syncOf(r2, a, "pig:2"); p["gone"] != nil {
		t.Fatalf("the next game day after a restart: %v", p)
	}
}
