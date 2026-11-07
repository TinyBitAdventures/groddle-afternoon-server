package main

import (
	"strings"
	"testing"
	"time"
)

func (r *gardenRig) pour(cl *Client, req int, c string, extra map[string]any) {
	msg := map[string]any{"t": "garden_potion", "req": req, "bed": "default", "c": c, "n": 8}
	for k, v := range extra {
		msg[k] = v
	}
	r.send(cl, msg)
	r.advance(0)
}

func TestGardenSoakAllAndSuperHoeWorkTheBedAndShowTheirTool(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	b := r.gardener(2, "Bo")
	r.pour(a, 1, "potion_garden_water", nil)
	ok := r.only(a, "garden_ok")
	if ok["verb"] != "potion" || ok["count"] != float64(8) || plotOf(t, ok, "default:4-2")["wet"] != true {
		t.Fatalf("a Soak-All answered %v", ok)
	}
	var ev map[string]any
	for _, m := range r.drain(b) {
		if m["t"] == "garden_ev" {
			ev = m
		}
	}
	if ev == nil || ev["tool"] != "watering_can" || ev["ms"] != float64(gardenPotionMs) || ev["by"] != float64(1) {
		t.Fatalf("Bo saw %v", ev)
	}
	// Every plot wet: a second Soak-All has no dry plot (canPour).
	r.pour(a, 2, "potion_garden_water", nil)
	if no := r.only(a, "garden_no"); no["message"] != "There are no dry gardens nearby." {
		t.Fatalf("a Soak-All on a wet bed answered %v", no)
	}
	r.pour(a, 3, "potion_garden_clear", nil)
	if no := r.only(a, "garden_no"); no["message"] != "There are no dirty gardens nearby." {
		t.Fatalf("a Super-Hoe on a clean bed answered %v", no)
	}
	for _, id := range []string{"default:1-1", "default:3-2"} {
		r.g.plots[gardenStreet][id].state = "dirty"
	}
	r.pour(a, 4, "potion_garden_clear", nil)
	if ok := r.only(a, "garden_ok"); ok["count"] != float64(2) || plotOf(t, ok, "default:3-2")["state"] != "clean" {
		t.Fatalf("a Super-Hoe answered %v", ok)
	}
	for _, m := range r.drain(b) {
		if m["t"] == "garden_ev" && m["tool"] != "hoe" {
			t.Fatalf("Bo saw %v", m)
		}
	}
}

func TestGardenSeedDibberTakesTheChargeAndTheSeedsInOneAct(t *testing.T) {
	r := newGardenRig(t, true)
	a := r.gardener(1, "Ada")
	r.ledger.give(1, "potion_garden_plant", 1)
	r.ledger.give(1, "seed_spinach", 3)
	r.g.plots[gardenStreet] = map[string]*gPlot{"default:2-1": {state: "seeds", seed: "seed_potato", mod: 1, planter: 9}}
	r.pour(a, 1, "potion_garden_plant", map[string]any{"seed": "seed_spinach", "n": 3, "k": 0, "mod": 0.9})
	ok := r.only(a, "garden_ok")
	if ok["count"] != float64(3) || ok["use"].(map[string]any)["c"] != "potion_garden_plant" || ok["seeds"].(map[string]any)["n"] != float64(3) {
		t.Fatalf("a Seed-Dibber answered %v", ok)
	}
	// The first three clean plots in the bed's order (2-1 was taken): 1-1, 3-1, 4-1.
	for _, id := range []string{"default:1-1", "default:3-1", "default:4-1"} {
		if p := r.g.plots[gardenStreet][id]; p.state != "seeds" || p.planter != 1 || p.mod != 0.9 {
			t.Fatalf("%s is %+v", id, p)
		}
	}
	if p := r.g.plots[gardenStreet]["default:1-2"]; p != nil && p.state != "clean" {
		t.Fatalf("1-2 is %+v", p)
	}
	calls := strings.Join(r.ledger.calls, ",")
	if !strings.Contains(calls, "potion_garden_plant") || !strings.Contains(calls, "seed_spinach x3") {
		t.Fatalf("ledger calls %s", calls)
	}
	// No seeds left: refused with the potion's own line, and the charge goes back (the fake ledger's
	// give back is the httpLedger's; here: nothing planted).
	r.ledger.give(1, "potion_garden_plant", 1)
	r.pour(a, 2, "potion_garden_plant", map[string]any{"seed": "seed_spinach", "n": 4})
	if no := r.only(a, "garden_no"); no["code"] != "no_item" || no["message"] != gardenNoCropSeeds {
		t.Fatalf("a Seed-Dibber without seeds answered %v", no)
	}
	// An herb seed for a crop bed.
	r.pour(a, 3, "potion_garden_plant", map[string]any{"seed": "herb_seed_rubeweed", "n": 4})
	if no := r.only(a, "garden_no"); no["message"] != gardenNoCropSeeds {
		t.Fatalf("an herb Seed-Dibber answered %v", no)
	}
	// No bottle.
	r2 := newGardenRig(t, true)
	c := r2.gardener(3, "Cy")
	r2.pour(c, 1, "potion_garden_water", nil)
	if no := r2.only(c, "garden_no"); no["code"] != "no_item" {
		t.Fatalf("a pour without the bottle answered %v", no)
	}
}

func TestGardenManyharvestIgnoresTheLockAndGrantsEveryCrop(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	b := r.gardener(2, "Bo")
	now := r.now.UnixMilli()
	r.g.plots[gardenStreet] = map[string]*gPlot{
		"default:1-1": {state: "crop", seed: "seed_spinach", mod: 1, planter: 1, ripeAt: now},
		"default:2-1": {state: "crop", seed: "seed_spinach", mod: 1},
		"default:3-1": {state: "crop", seed: "seed_potato", mod: 1},
		"default:4-1": {state: "seeds", seed: "seed_potato", mod: 1},
	}
	// Bo's hand pick is locked out of Ada's plot, but his Manyharvest takes it (master_garden.js 477).
	r.gact(b, 1, "default:1-1", "harvest", nil)
	if no := r.only(b, "garden_no"); no["code"] != "locked" {
		t.Fatalf("Bo's hand pick answered %v", no)
	}
	r.pour(b, 2, "potion_garden_harvest", nil)
	ok := r.only(b, "garden_ok")
	if ok["count"] != float64(3) {
		t.Fatalf("a Manyharvest answered %v", ok)
	}
	grants := ok["grants"].([]any)
	got := map[string]float64{}
	for _, g := range grants {
		m := g.(map[string]any)
		got[m["c"].(string)] = m["n"].(float64)
	}
	// Master Gardener never halves (master_garden.js 497-503): 12 spinach a plot, 18 potatoes.
	if got["spinach"] != 24 || got["potato"] != 18 || len(r.g.grants) != 2 {
		t.Fatalf("grants %v", got)
	}
	if p := r.g.plots[gardenStreet]["default:4-1"]; p.state != "seeds" {
		t.Fatalf("the seeds were picked: %+v", p)
	}
	for _, m := range r.drain(a) {
		if m["t"] == "garden_ev" && m["tool"] != nil {
			t.Fatalf("a Manyharvest showed a tool: %v", m)
		}
	}
	// The page's energy covered one plot: only the first.
	r.g.plots[gardenStreet]["default:1-1"] = &gPlot{state: "crop", seed: "seed_spinach", mod: 1}
	r.g.plots[gardenStreet]["default:2-1"] = &gPlot{state: "crop", seed: "seed_spinach", mod: 1}
	r.advance(2 * time.Second)
	r.pour(b, 3, "potion_garden_harvest", map[string]any{"n": 1})
	if ok := r.only(b, "garden_ok"); ok["count"] != float64(1) || plotOf(t, ok, "default:2-1")["state"] != "crop" {
		t.Fatalf("a Manyharvest for one plot answered %v", ok)
	}
}

func TestGardenDungKickerAndTheBedHeld(t *testing.T) {
	r := newGardenRig(t, true)
	a := r.gardener(1, "Ada")
	b := r.gardener(2, "Bo")
	now := r.now.UnixMilli()
	r.g.plots[gardenStreet] = map[string]*gPlot{
		"default:1-1": {state: "seeds", seed: "seed_potato", mod: 1, wet: true, wetAt: now, stageAt: now},
		"default:2-1": {state: "shoots", seed: "seed_potato", mod: 1, wet: true, wetAt: now, stageAt: now},
		"default:3-1": {state: "seeds", seed: "seed_potato", mod: 1},
	}
	r.ledger.give(1, "potion_garden_fertilize", 1)
	// While Ada's pour waits for the ledger, the bed is held.
	var later func()
	r.g.uses.spawn = func(fn func()) { later = fn }
	r.pour(a, 1, "potion_garden_fertilize", nil)
	r.gact(b, 1, "default:3-1", "water", nil)
	if no := r.only(b, "garden_no"); no["code"] != "busy" {
		t.Fatalf("an act on a held bed answered %v", no)
	}
	r.g.uses.spawn = func(fn func()) { fn() }
	later()
	r.advance(0)
	ok := r.only(a, "garden_ok")
	if ok["count"] != float64(2) || plotOf(t, ok, "default:1-1")["state"] != "shoots" || plotOf(t, ok, "default:2-1")["fert"] != true || plotOf(t, ok, "default:3-1")["state"] != "seeds" {
		t.Fatalf("a Dung-Kicker answered %v", ok)
	}
	r.gact(b, 2, "default:3-1", "water", nil)
	r.only(b, "garden_ok")
}

func TestGardenOverlayToolsAreTheOriginals(t *testing.T) {
	cases := []struct {
		m    gardenAct
		want string
	}{
		{gardenAct{Verb: "water"}, "watering_can"},
		{gardenAct{Verb: "water", Tool: "irrigator_9000"}, "irrigator_9000"},
		{gardenAct{Verb: "water_all", Tool: "pick"}, "watering_can"},
		{gardenAct{Verb: "clear", Tool: "high_class_hoe"}, "high_class_hoe"},
		{gardenAct{Verb: "clear"}, "hoe"},
		{gardenAct{Verb: "fertilize"}, "guano"},
		{gardenAct{Verb: "plant"}, ""},
		{gardenAct{Verb: "harvest"}, ""},
		{gardenAct{Verb: "potion", Seed: "water"}, "watering_can"},
		{gardenAct{Verb: "potion", Seed: "pick"}, ""},
	}
	for _, c := range cases {
		if got := overlayTool(c.m); got != c.want {
			t.Errorf("%+v: %q, want %q", c.m, got, c.want)
		}
	}
	if overlayMs(0) != 2000 || overlayMs(99) != gardenOverlayMin || overlayMs(1e6) != gardenOverlayMax {
		t.Fatal("overlay times")
	}
}
