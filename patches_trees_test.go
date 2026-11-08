package main

import (
	"slices"
	"testing"
	"time"
)

// G564 slice 2: the Rook, Fertilidust and Trantsformation Fluid on trees grown in shared Patches.

// A rookable street (a Mab shrine) with a Patch, a fruit tree (tree:0) and a Wood Tree (tree:1).
const patchRookStreet = "GCR29V8S4Q12IF4"

// setTree stands a grown tree in a plot, as a Seedling would have left it.
func (r *patchRig) setTree(street, key, class string, maturity, health, fruit int) *pTree {
	r.t.Helper()
	spot := r.g.spotOf(street, key)
	if spot == nil {
		r.t.Fatalf("no plot %s on %s", key, street)
	}
	now := r.now.UnixMilli()
	p := r.g.plot(street, spot, now)
	t := &pTree{class: class, grownAt: now, cycleAt: now}
	t.day = r.g.dayOf(now)
	t.maturity, t.health = maturity, health
	if class != "wood_tree" {
		t.cap = r.g.ow.capAt(class, maturity, health)
		t.fruit = min(fruit, t.cap)
	}
	p.stage, p.since, p.tree, p.seed, p.dead = "tree", now, t, nil, ""
	r.g.arm()
	return t
}

func (r *patchRig) plotFrame(cl *Client, key string) map[string]any {
	var got map[string]any
	for _, f := range r.frames(cl, "patches") {
		if p, ok := f["plots"].(map[string]any)[key].(map[string]any); ok {
			got = p
		}
	}
	return got
}

func TestPatchTrantsformationFluid(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	b := r.at(2, "Bo", oldMainStreet, "patch:1")
	r.drain(b)
	// Nothing grows here yet: not a tree.
	r.pact(a, 1, "patch:0", "trantsform", nil)
	if no := r.no(a, "not_tree"); no["message"] != patchNoTrantsform {
		t.Fatal(no)
	}
	tr := r.setTree(oldMainStreet, "patch:0", "trant_spice", 3, 7, 5)
	r.pact(a, 2, "patch:0", "trantsform", nil)
	if no := r.no(a, "small"); no["message"] != patchTrantSmall {
		t.Fatal(no)
	}
	tr.maturity, tr.health = 7, 3
	r.pact(a, 3, "patch:0", "trantsform", nil)
	if no := r.no(a, "weak"); no["message"] != patchTrantWeak {
		t.Fatal(no)
	}
	tr.health = 8
	tr.poisonEnd, tr.poisonBy = r.now.UnixMilli()+60_000, 2
	r.pact(a, 4, "patch:0", "trantsform", nil)
	r.no(a, "poisoned")
	tr.poisonEnd, tr.poisonBy = 0, 0
	// No bottle: the ledger's line, nothing changes.
	r.pact(a, 5, "patch:0", "trantsform", nil)
	r.no(a, "no_item")
	if r.live(oldMainStreet, "patch:0").tree.class != "trant_spice" {
		t.Fatal("a refused pour changed the tree")
	}
	r.ledger.give(1, useTrantsform, 1)
	r.pact(a, 6, "patch:0", "trantsform", map[string]any{"k": 0})
	ok := r.ok(a)
	// The roll 0.5 picks the third of bean, bubble, fruit, gas, wood (never the spice it was, never an Egg Plant).
	if ok["from"] != "trant_spice" || ok["to"] != "trant_fruit" || ok["use"] == nil {
		t.Fatalf("the pour answered %v", ok)
	}
	if got := r.ledger.copies; len(got) != 1 || got[0] != useTrantsform+" #0" {
		t.Fatalf("the ledger used %v", got)
	}
	nt := r.live(oldMainStreet, "patch:0").tree
	if nt.class != "trant_fruit" || nt.maturity != 4 || nt.health != 4 || nt.fruit != 0 || nt.petted {
		t.Fatalf("the new tree is %+v (want maturity max(4, 7-3), health 4, no fruit)", nt)
	}
	if p := r.plotFrame(b, "patch:0"); p == nil || p["tree"].(map[string]any)["class"] != "trant_fruit" {
		t.Fatalf("Bo saw %v", p)
	}
	// A Wood Tree has no health: only its maturity counts; it becomes at most maturity 6 as a Wood Tree.
	wood := r.setTree(oldMainStreet, "patch:1", "wood_tree", 5, 0, 0)
	_ = wood
	r.g.roll = func() float64 { return 0.99 } // the last of bean, bubble, fruit, gas, spice
	r.ledger.give(1, useTrantsform, 1)
	r.walk(a, "patch:1")
	r.pact(a, 7, "patch:1", "trantsform", nil)
	if ok := r.ok(a); ok["from"] != "wood_tree" || ok["to"] != "trant_spice" {
		t.Fatalf("the Wood Tree's pour answered %v", ok)
	}
	if nt := r.live(oldMainStreet, "patch:1").tree; nt.maturity != 4 || nt.health != 4 {
		t.Fatalf("the trantsformed Wood Tree is %+v", nt)
	}
	// Never into an Egg Plant on a street, and never from one.
	r.g.roll = func() float64 { return 0 }
	for i := 0; i < 3; i++ {
		r.setTree(oldMainStreet, "patch:1", "trant_bubble", 8, 8, 0)
		r.ledger.give(1, useTrantsform, 1)
		r.pact(a, 10+i, "patch:1", "trantsform", nil)
		if ok := r.ok(a); ok["to"] == "trant_egg" || ok["to"] == "trant_bubble" {
			t.Fatal(ok)
		}
	}
	r.setTree(oldMainStreet, "patch:1", "trant_egg", 8, 8, 0)
	r.pact(a, 20, "patch:1", "trantsform", nil)
	r.no(a, "not_tree")
}

func TestPatchTrantsformTreeChangedWhileTheLedgerAnswered(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	tr := r.setTree(oldMainStreet, "patch:0", "trant_spice", 8, 8, 0)
	r.ledger.give(1, useTrantsform, 1)
	r.deferLedger()
	r.pact(a, 1, "patch:0", "trantsform", nil)
	// The plot is held: nobody else pours on it meanwhile.
	b := r.at(2, "Bo", oldMainStreet, "patch:0")
	r.pact(b, 2, "patch:0", "poison", nil)
	r.no(b, "busy")
	tr.maturity = 2 // as if it had changed under the pour
	r.flush()
	r.no(a, "small")
	if r.live(oldMainStreet, "patch:0").tree.class != "trant_spice" {
		t.Fatal("the tree changed")
	}
}

func TestPatchFertilidustRevivesGrowsAndLite(t *testing.T) {
	r := newPatchRig(t, true)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	b := r.at(2, "Bo", oldMainStreet, "patch:3")
	r.drain(b)
	spice := r.setTree(oldMainStreet, "patch:0", "trant_spice", 3, 7, 0)
	wood := r.setTree(oldMainStreet, "patch:1", "wood_tree", 2, 0, 0)
	r.send(a, map[string]any{"t": "obj_dust", "req": 1})
	r.advance(0)
	used := r.only(a, "obj_dust_used")
	if rev, _ := used["revived"].([]any); len(rev) != 1 || rev[0] != "trant_spice_dead:0" {
		t.Fatalf("the jar answered %v", used)
	}
	// The dead Spice Plant came back at once, for everyone: health 4 + 1, maturity 3 + 1, no fruit.
	dead := r.live(oldMainStreet, "trant_spice_dead:0")
	if dead.stage != "tree" || dead.tree.class != "trant_spice" || dead.tree.maturity != 4 || dead.tree.health != 5 || dead.tree.fruit != 0 {
		t.Fatalf("the revived tree is %+v / %+v", dead, dead.tree)
	}
	if p := r.plotFrame(b, "trant_spice_dead:0"); p == nil || p["stage"] != "tree" {
		t.Fatalf("Bo saw %v", p)
	}
	if spice.maturity != 3 {
		t.Fatal("the living trees grow only as the dust lands")
	}
	r.advance(dustLand)
	dusted := r.only(a, "obj_dusted")
	ids, _ := dusted["ids"].([]any)
	if len(ids) != 2 || !slices.Contains(ids, any("patch:0")) || !slices.Contains(ids, any("patch:1")) {
		t.Fatalf("the dust grew %v", dusted)
	}
	cap := r.g.ow.capAt("trant_spice", 4, 10)
	want := min(cap, r.g.ow.prodAt("trant_spice", 4, 10))
	if spice.maturity != 4 || spice.health != 10 || spice.fruit != want {
		t.Fatalf("the Spice Plant is %+v, want maturity 4, health 10, fruit %d", spice, want)
	}
	if wood.maturity != 3 {
		t.Fatalf("the Wood Tree is %+v", wood)
	}
	if rt := r.live(oldMainStreet, "trant_spice_dead:0").tree; rt.maturity != 4 {
		t.Fatal("the tree the jar revived also grew as it landed")
	}
	// Lite names one Patch tree by its plot key: only it grows.
	r.drain(a)
	r.send(a, map[string]any{"t": "obj_dust", "req": 2, "id": "patch:0", "k": 1})
	r.advance(dustLand)
	if d := r.only(a, "obj_dusted"); len(d["ids"].([]any)) != 1 || spice.maturity != 5 || wood.maturity != 3 {
		t.Fatalf("Lite grew %v (spice %d, wood %d)", d, spice.maturity, wood.maturity)
	}
	// Too far, or a plot with no tree.
	r.send(a, map[string]any{"t": "obj_dust", "req": 3, "id": "patch:1"})
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "far" {
		t.Fatal(no)
	}
	r.send(b, map[string]any{"t": "obj_dust", "req": 4, "id": "patch:3"})
	r.advance(0)
	if no := r.only(b, "obj_no"); no["code"] != "unknown" {
		t.Fatal(no)
	}
}

// A hub with the patches module, the street objects and the Rook, the way the server runs them.
func newPatchRookRig(t *testing.T) (*patchRig, *rook) {
	t.Helper()
	var rk *rook
	r := newPatchRigWith(t, true, func(h *Hub) Module { rk = newRook(h).(*rook); return rk })
	r.shareAll()
	rk.dev = true
	rk.roll = func() float64 { return 0.4 } // every rookable thing is rooked
	return r, rk
}

func TestPatchRookStripsKillsAndWaitsForRevival(t *testing.T) {
	r, rk := newPatchRookRig(t)
	if _, ok := rk.w.streets[patchRookStreet]; !ok {
		t.Skip("the test street is no longer rookable")
	}
	a := r.at(1, "Ada", patchRookStreet, "patch:0")
	b := r.at(2, "Bo", patchRookStreet, "patch:0")
	tr := r.setTree(patchRookStreet, "patch:0", "trant_fruit", 5, 8, 10)
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.drain(a)
	r.drain(b)
	r.advance(rookWingBeat)
	key := "trant_fruit:ppatch:0"
	if rk.streets[patchRookStreet].rooked[key] == nil {
		t.Fatalf("the Patch tree was not rooked: %v", rk.streets[patchRookStreet].rooked)
	}
	if tr.fruit != 0 || tr.health != 6 {
		t.Fatalf("the rooked tree is %+v, want no fruit and health 8-2", tr)
	}
	if p := r.plotFrame(b, "patch:0"); p == nil || p["tree"].(map[string]any)["fruit"] != float64(0) {
		t.Fatalf("Bo saw %v", p)
	}
	// While rooked: nobody pets, waters, harvests or trantsforms it.
	for i, verb := range []string{"pet", "water", "harvest", "trantsform"} {
		r.pact(a, 10+i, "patch:0", verb, nil)
		if no := r.no(a, "rooked"); no["message"] != patchRooked {
			t.Fatal(no)
		}
	}
	// Its fruit grows back from step 6 while it waits.
	r.advance(rookFruitFirst)
	if want := min(r.g.ow.capAt("trant_fruit", 5, 6), r.g.ow.prodAt("trant_fruit", 5, 6)); tr.fruit != want || want == 0 {
		t.Fatalf("the rooked tree's fruit is %d, want %d", tr.fruit, want)
	}
	// Three doses revive it; then it is a tree like any other.
	c := r.at(3, "Cy", patchRookStreet, "patch:0")
	for _, cl := range []*Client{a, b, c} {
		r.send(cl, map[string]any{"t": "rook_revive", "key": key})
		r.advance(0)
	}
	if rk.streets[patchRookStreet].rooked[key] != nil {
		t.Fatal("three doses did not revive it")
	}
	r.pact(a, 20, "patch:0", "pet", nil)
	r.ok(a)
}

func TestPatchRookKillsAHealthOneTreeAndAWoodTreeIsOnlyRooked(t *testing.T) {
	r, rk := newPatchRookRig(t)
	if _, ok := rk.w.streets[patchRookStreet]; !ok {
		t.Skip("the test street is no longer rookable")
	}
	a := r.at(1, "Ada", patchRookStreet, "patch:0")
	r.setTree(patchRookStreet, "patch:0", "trant_bean", 6, 1, 3)
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.advance(rookWingBeat)
	p := r.live(patchRookStreet, "patch:0")
	if p.stage != "dead" || p.dead != "trant_bean_dead" {
		t.Fatalf("a health-1 Patch tree the Rook struck is %+v", p)
	}
	if rk.streets[patchRookStreet].rooked["trant_bean:ppatch:0"] != nil {
		t.Fatal("a dead Patch tree stays rooked")
	}
	// Cleared and grown again, the new tree is not rooked.
	r.pact(a, 1, "patch:0", "clear", nil)
	r.ok(a)
	wood := r.setTree(patchRookStreet, "patch:0", "wood_tree", 4, 0, 0)
	if r.g.rooked(patchRookStreet, r.live(patchRookStreet, "patch:0")) {
		t.Fatal("the new tree starts rooked")
	}
	r.drain(a)
	r.send(a, map[string]any{"t": "rook_dev", "do": "wingbeat"})
	r.advance(0)
	if rk.streets[patchRookStreet].rooked["wood_tree:ppatch:0"] == nil {
		t.Fatalf("the Wood Tree was not rooked: %v", rk.streets[patchRookStreet].rooked)
	}
	if wood.maturity != 4 {
		t.Fatalf("a rooked Wood Tree is stripped of nothing: %+v", wood)
	}
	r.pact(a, 2, "patch:0", "harvest", nil)
	r.no(a, "rooked")
}

func TestPatchRookedPatchKeysOutliveARestart(t *testing.T) {
	dir := t.TempDir()
	saved := rookFileForTest
	rookFileForTest = dir + "/rook.json"
	defer func() { rookFileForTest = saved }()
	_, rk := newPatchRookRig(t)
	s := rk.street(patchRookStreet)
	s.rooked["trant_fruit:ppatch:0"] = &rookedThing{class: "trant_fruit"}
	s.rooked["trant_egg:ppatch:0"] = &rookedThing{class: "trant_fruit"} // a class that disagrees: dropped
	if !rk.save() {
		t.Fatal("not saved")
	}
	_, rk2 := newPatchRookRig(t)
	got := rk2.streets[patchRookStreet]
	if got == nil || got.rooked["trant_fruit:ppatch:0"] == nil || got.rooked["trant_egg:ppatch:0"] != nil {
		t.Fatalf("after a restart: %+v", got)
	}
	if _, ok := got.keys["trant_fruit:ppatch:0"]; ok {
		t.Fatal("a Patch tree's key is the patches module's, not a client's")
	}
}

func TestPatchADeadStreetTreesPlotTakesABeanAgain(t *testing.T) {
	r := newPatchRig(t, true)
	r.shareAll()
	tree0 := sharedWorld.streets[patchRookStreet]["tree:0"]
	a := r.player(1, "Ada", patchRookStreet, tree0.x, tree0.y)
	r.mod.killTree(patchRookStreet, r.mod.get(patchRookStreet, tree0), 0)
	r.pact(a, 1, "tree:0", "clear", nil)
	r.ok(a)
	bean := ""
	hub := r.g.w.streets[patchRookStreet].hub
	for _, b := range []string{"bean_fruit", "bean_bean", "bean_bubble", "bean_spice", "bean_gas", "bean_wood"} {
		if r.g.w.canPlant(map[string]string{"bean_fruit": "trant_fruit", "bean_bean": "trant_bean", "bean_bubble": "trant_bubble",
			"bean_spice": "trant_spice", "bean_gas": "trant_gas", "bean_wood": "wood_tree"}[b], "patch", hub) {
			bean = b
			break
		}
	}
	if bean == "" {
		t.Skip("nothing grows in this hub")
	}
	r.ledger.give(1, bean, 1)
	r.pact(a, 2, "tree:0", "tend", nil)
	r.ok(a)
	r.pact(a, 3, "tree:0", "plant", map[string]any{"bean": bean})
	if ok := r.ok(a); ok["plot"].(map[string]any)["stage"] != "seedling" {
		t.Fatal(ok)
	}
	for i := 0; i < 3; i++ {
		r.pact(a, 4+i, "tree:0", "pet", nil)
		r.ok(a)
	}
	r.advance(5 * time.Minute)
	if p := r.live(patchRookStreet, "tree:0"); p.stage != "tree" || !p.adopted {
		t.Fatalf("the dead street tree's plot grew %+v", p)
	}
}
