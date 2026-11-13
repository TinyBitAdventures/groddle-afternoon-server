package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Slice 3: every street is shared, so a street the pilot never had works like Old Main Street.
func TestPatchAStreetThePilotNeverHadIsShared(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", pentalLeave, "patch:0")
	b := r.at(2, "Bo", pentalLeave, "patch:0")
	r.send(b, map[string]any{"t": "patch_sync"})
	if snap := r.only(b, "patches"); snap["none"] != nil || snap["snap"] != true {
		t.Fatalf("Pental Leave answered %v", snap)
	}
	r.ledger.give(1, "bean_fruit", 1)
	r.pact(a, 1, "patch:0", "tend", nil)
	r.ok(a)
	if fr := r.frames(b, "patches"); len(fr) != 1 || plotOf(t, fr[0], "patch:0")["stage"] != "clean" {
		t.Fatalf("Bo saw the tend as %v", fr)
	}
	// Hub 98 grows Fruit Trees, never a Spice Plant.
	r.ledger.give(1, "bean_spice", 1)
	r.pact(a, 2, "patch:0", "plant", map[string]any{"bean": "bean_spice"})
	r.no(a, "wrong_bean")
	r.pact(a, 3, "patch:0", "plant", map[string]any{"bean": "bean_fruit"})
	r.ok(a)
	r.pact(b, 1, "patch:0", "pet", nil)
	if ok := r.ok(b); ok["first"] != true || ok["petted"] != float64(1) {
		t.Fatalf("Bo's petting answered %v", ok)
	}
	r.pact(a, 4, "patch:0", "pet", nil)
	r.ok(a)
	r.pact(a, 5, "patch:0", "pet", nil)
	if ok := r.ok(a); ok["third"] != true || ok["owner"] != true {
		t.Fatalf("the planter's third petting answered %v", ok)
	}
	r.drain(b)
	r.advance(5 * time.Minute)
	fr := r.frames(b, "patches")
	if len(fr) != 1 || plotOf(t, fr[0], "patch:0")["stage"] != "tree" {
		t.Fatalf("Bo saw the Seedling grow as %v", fr)
	}
	r.pact(b, 2, "patch:0", "harvest", nil)
	r.no(b, "empty") // a young tree has no fruit (no maturity gate)
}

// writeSeed puts a seed file where the module reads it.
func writeSeed(t *testing.T, plots ...patchSeed) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "realtime-patches-seed.json")
	b, _ := json.Marshal(patchSeedFile{V: 1, Plots: plots})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func grantsOf(r *patchRig, cl *Client) map[string]int {
	out := map[string]int{}
	for _, m := range r.drain(cl) {
		if m["t"] == "patch_grant" {
			out[m["c"].(string)] += int(m["n"].(float64))
		}
	}
	return out
}

func TestPatchSeedPlantsTheWinnersOnceAndRefundsWhatCantLand(t *testing.T) {
	dir := t.TempDir()
	patchesFileForTest = filepath.Join(dir, "realtime-patches.json")
	defer func() { patchesFileForTest, patchesSeedForTest = "", "" }()
	r0 := newPatchRig(t, false)
	now := r0.now.UnixMilli()
	// Someone tended Old Main Street's patch:3 before the seed landed: it is taken.
	cy := r0.at(9, "Cy", oldMainStreet, "patch:3")
	r0.ledger.give(9, "bean_spice", 1)
	r0.pact(cy, 1, "patch:3", "tend", nil)
	r0.ok(cy)
	r0.pact(cy, 2, "patch:3", "plant", map[string]any{"bean": "bean_spice"})
	r0.ok(cy)
	r0.g.save()

	patchesSeedForTest = writeSeed(t,
		// Player 500's Fruit Tree on Pental Leave (the live count's tree), maturity 2 at health 7.
		patchSeed{ID: pentalLeave + "|patch:0", Street: pentalLeave, Key: "patch:0", User: 500, Stage: "tree", Tree: "trant_fruit", Bean: "bean_fruit",
			Maturity: 2, Health: 7, Fruit: 99, GrownAt: now - 3_600_000, CycleAt: now},
		// A Wood Tree Seedling petted three times whose 4 hours ran out: ready, it grows now.
		patchSeed{ID: "GLIF37ASJFE1AVQ|patch:0", Street: "GLIF37ASJFE1AVQ", Key: "patch:0", User: 500, Stage: "seedling", Tree: "wood_tree", Bean: "bean_wood",
			PlantedAt: now - 5*3_600_000, MinAt: now - 5*3_600_000 + 300_000, MaxAt: now - 3_600_000, Petted: 3, ThirdAt: now - 5*3_600_000 + 6000},
		// A Spice Plant Seedling petted once, out of time: a fresh hour.
		patchSeed{ID: oldMainStreet + "|patch:2", Street: oldMainStreet, Key: "patch:2", User: 7, Stage: "seedling", Tree: "trant_spice", Bean: "bean_spice",
			PlantedAt: now - 5*3_600_000, MinAt: now - 5*3_600_000 + 300_000, MaxAt: now - 3_600_000, Petted: 1},
		// Taken (Cy planted it): refunded, the bean and the fruit on it.
		patchSeed{ID: oldMainStreet + "|patch:3", Street: oldMainStreet, Key: "patch:3", User: 7, Stage: "tree", Tree: "trant_gas", Bean: "bean_gas", Maturity: 5, Health: 9, Fruit: 12},
		// An Egg Plant in a plain Patch does not fit; a Wood Tree on a dead tree's key is not a Patch.
		patchSeed{ID: oldMainStreet + "|patch:1", Street: oldMainStreet, Key: "patch:1", User: 8, Stage: "seedling", Tree: "trant_egg", Bean: "bean_egg"},
		patchSeed{ID: oldMainStreet + "|trant_spice_dead:0", Street: oldMainStreet, Key: "trant_spice_dead:0", User: 8, Stage: "tree", Tree: "wood_tree", Maturity: 4},
	)
	r := newPatchRig(t, false)
	r.now = r0.now
	if len(r.g.seeds) != 6 || r.g.resumed {
		t.Fatalf("the seeds wait for the first connect: %d, resumed %v", len(r.g.seeds), r.g.resumed)
	}
	// The first connect plants them; a refund comes as grants, resent on every connect until acked.
	sev := r.hub.Connect(Claims{Sub: 7, Name: "Sev"})
	if got := grantsOf(r, sev); got["bean_gas"] != 1 || got["general_vapour"] != 12 || len(got) != 2 {
		t.Fatalf("the taken Gas Plant refunded %v", got)
	}
	sp := r.spot(oldMainStreet, "patch:2")
	r.send(sev, map[string]any{"t": "join", "street": oldMainStreet, "x": sp.x, "y": sp.y})
	r.drain(sev)
	r.send(sev, map[string]any{"t": "patch_sync"})
	snap := r.only(sev, "patches")
	if p := plotOf(t, snap, "patch:2"); p["stage"] != "seedling" || p["seedling"].(map[string]any)["planter"] != float64(7) || p["seedling"].(map[string]any)["maxAt"] != float64(now+3_600_000) {
		t.Fatalf("the Spice Seedling is %v", p)
	}
	if p := plotOf(t, snap, "patch:3"); p["seedling"].(map[string]any)["planter"] != float64(9) {
		t.Fatalf("Cy's Seedling was overwritten: %v", p)
	}
	if p := snap["plots"].(map[string]any)["patch:1"]; p != nil {
		t.Fatalf("the Egg Plant landed in a plain Patch: %v", p)
	}
	ada := r.at(500, "Ada", pentalLeave, "patch:0")
	r.drain(ada)
	r.send(ada, map[string]any{"t": "patch_sync"})
	p := plotOf(t, r.only(ada, "patches"), "patch:0")
	tr, _ := p["tree"].(map[string]any)
	if p["stage"] != "tree" || tr["class"] != "trant_fruit" || tr["maturity"] != float64(2) || tr["health"] != float64(7) || tr["fruit"] != tr["cap"] || tr["petted"] == true {
		t.Fatalf("the winner's tree arrived as %v", p)
	}
	if lp := r.live("GLIF37ASJFE1AVQ", "patch:0"); lp == nil || lp.stage != "tree" || lp.tree.class != "wood_tree" || lp.tree.maturity != 1 {
		t.Fatalf("the ready Wood Tree Seedling is %+v", lp)
	}

	// What can't land is option A: the Egg Plant's bean; the Wood Tree's bean and its Planks at maturity 4.
	if got := grantsOf(r, r.hub.Connect(Claims{Sub: 8, Name: "Ate"})); got["bean_egg"] != 1 || got["bean_wood"] != 1 || got["plank"] != 50 || len(got) != 3 {
		t.Fatalf("player 8 was refunded %v", got)
	}

	// The same file again after a restart plants and pays nothing twice.
	r.g.save()
	raw, _ := os.ReadFile(patchesFileForTest)
	if !strings.Contains(string(raw), `"seeded"`) {
		t.Fatalf("the state file does not remember the seeds: %s", raw)
	}
	r2 := newPatchRig(t, false)
	r2.now = r.now
	if len(r2.g.seeds) != 0 || len(r2.g.seeded) != 6 {
		t.Fatalf("after a restart: %d waiting, %d remembered", len(r2.g.seeds), len(r2.g.seeded))
	}
}

func TestPatchSeedWithTheStateFileOffAndAMissingOrTornFile(t *testing.T) {
	defer func() { patchesSeedForTest = "" }()
	patchesSeedForTest = filepath.Join(t.TempDir(), "none.json")
	r := newPatchRig(t, false)
	if len(r.g.seeds) != 0 || !r.g.resumed {
		t.Fatal("a missing seed file is nothing to do")
	}
	patchesSeedForTest = writeSeed(t)
	os.WriteFile(patchesSeedForTest, []byte(`{"v":1,"plots":[`), 0o600)
	r = newPatchRig(t, false)
	if len(r.g.seeds) != 0 {
		t.Fatal("a torn seed file planted something")
	}
	if m, _ := filepath.Glob(patchesSeedForTest + ".bad-*"); len(m) != 1 {
		t.Fatalf("a torn seed file was not moved aside: %v", m)
	}
}
