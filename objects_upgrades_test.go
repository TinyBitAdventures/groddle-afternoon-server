package main

import (
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"
)

// Nightshade Woods: its upgrade (job 35, options 93 and 94) adds a shrine and more animals.
const nightshade = "GCR11JA67NJ15N3"

// builtUpgrade stands in for the projects module: the street's built upgrade option.
func (r *objRig) builtUpgrade(street, option string) {
	r.hub.provided["projects.upgrade"] = func(s string) string {
		if s == street {
			return option
		}
		return ""
	}
}

func TestUpgradedStreetsHaveTheirLayouts(t *testing.T) {
	ups := sharedWorld.upgrades
	// tools/build-realtime-objects.mjs upgradeLayouts: the options whose layout moves anything.
	for street, opts := range map[string][]string{nightshade: {"93", "94"}, "GCR10NQRPKJ16H6": {"15", "16"}, "GLI1BQA9PUH16A0": {"13"}} {
		if len(ups[street]) != len(opts) {
			t.Fatalf("%s upgrades %v, want %v", street, ups[street], opts)
		}
		for _, o := range opts {
			if ups[street][o] == nil {
				t.Fatalf("%s has no layout for option %s", street, o)
			}
		}
	}
	// Verdant Green and Middle Arbor's upgrades only change a shrine they already have: no new layout.
	if ups["GCR10S7GVKJ1ABI"] != nil || ups["GCR101UN0NJ1QPQ"] != nil || len(ups) != 3 {
		t.Fatalf("upgraded streets: %v", ups)
	}
	base, up := sharedWorld.streets[nightshade], ups[nightshade]["93"].spots
	if base["chicken:1"].x != 2531.5 || up["chicken:1"].x != 4251.5 || up["pig:2"].x != 1730.5 || up["butterfly:2"] == nil || base["butterfly:2"] != nil {
		t.Fatalf("Nightshade Woods: chicken:1 %v -> %v, pig:2 %v, butterfly:2 %v", base["chicken:1"], up["chicken:1"], up["pig:2"], up["butterfly:2"])
	}
}

// Player report #222/#232: by an upgraded street's Chicken, the server said "You need to be closer"
// since it still had the Chicken where the street stood before its upgrade.
func TestAnUpgradedStreetsAnimalIsInReachWhereThePageDrawsIt(t *testing.T) {
	r := newObjRig(t)
	r.credited()
	up := sharedWorld.upgrades[nightshade]["94"].spots
	ch := up["chicken:1"]
	a := r.player(1, "Ada", nightshade, 4274, ch.y)
	b := r.player(2, "Bo", nightshade, sharedWorld.streets[nightshade]["chicken:0"].x, ch.y)
	// Before the upgrade is built the street's own layout stands: chicken:1 is 1,700 px off.
	r.act(a, 1, "chicken:1", "squeeze", nil)
	if no := r.only(a, "obj_no"); no["code"] != "far" {
		t.Fatalf("before the upgrade: %v", no)
	}
	// Bo squeezes chicken:0 at its old spot: its uses are kept across the switch.
	r.act(b, 1, "chicken:0", "squeeze", nil)
	if ok := r.only(b, "obj_ok"); ok["success"] != true {
		t.Fatalf("Bo's squeeze: %v", ok)
	}
	r.builtUpgrade(nightshade, "94")
	r.now = r.now.Add(3 * time.Second)
	r.act(a, 2, "chicken:1", "squeeze", nil)
	if ok := r.only(a, "obj_ok"); ok["success"] != true {
		t.Fatalf("after the upgrade, by chicken:1 at %v: %v", ch.x, ok)
	}
	ob := r.mod.streets[nightshade]["chicken:0"]
	if ob == nil || ob.spot != up["chicken:0"] || ob.uses[2]["squeeze"] != 1 {
		t.Fatalf("chicken:0 after the switch: %+v", ob)
	}
	// The snapshot walks the Piggies from the upgraded spots and shares the added Butterfly.
	r.send(a, map[string]any{"t": "obj_sync"})
	snap := r.only(a, "objs")
	homes, _ := snap["homes"].(map[string]any)
	h2, _ := homes["pig:2"].([]any)
	animals, _ := json.Marshal(snap["animals"])
	if len(h2) != 2 || num(h2[0]) != up["pig:2"].x || string(animals) != `["butterfly:0","butterfly:1","butterfly:2","chicken:0","chicken:1"]` {
		t.Fatalf("snapshot homes %v animals %s", homes, animals)
	}
	// A Piggy far off in the old layout but beside her now answers too (pig:2: 3,468 -> 1,730).
	c := r.player(3, "Cy", nightshade, up["pig:2"].x, up["pig:2"].y)
	if math.Abs(sharedWorld.streets[nightshade]["pig:2"].x-up["pig:2"].x) < pigReachX/2 {
		t.Fatal("pig:2 barely moved")
	}
	r.act(c, 1, "pig:2", "wait", nil)
	for _, m := range r.drain(c) {
		if m["t"] == "obj_no" {
			t.Fatalf("pig:2 refused: %v", m)
		}
	}
}

// A saved object of an id only the upgraded layout has comes back after a restart.
func TestAnUpgradedStreetsObjectsOutliveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	r.credited()
	r.builtUpgrade(nightshade, "93")
	up := sharedWorld.upgrades[nightshade]["93"].spots
	ch := up["chicken:1"]
	a := r.player(1, "Ada", nightshade, ch.x, ch.y)
	r.act(a, 1, "chicken:1", "squeeze", nil)
	if ok := r.only(a, "obj_ok"); ok["success"] != true {
		t.Fatalf("squeeze: %v", ok)
	}
	r2 := r.restart(path, r.now.Add(2*time.Second))
	r2.credited()
	r2.builtUpgrade(nightshade, "93")
	b := r2.player(1, "Ada", nightshade, ch.x, ch.y)
	if got := syncOf(r2, b, "chicken:1"); got == nil || num(got["sq"]) != 1 {
		t.Fatalf("chicken:1 after a restart: %v", got)
	}
	if ob := r2.mod.streets[nightshade]["chicken:1"]; ob == nil || ob.spot != ch {
		t.Fatalf("chicken:1's spot after a restart: %+v", ob)
	}
}

// The projects module names the built upgrade's option (the game's appliedUpgrade).
func TestProjectsNameABuiltUpgrade(t *testing.T) {
	r := newProjRig(t, "", nil, time.UnixMilli(1_700_000_000_000))
	if got := r.m.upgradeOn(nightshade); got != "" {
		t.Fatalf("unbuilt: %q", got)
	}
	r.m.st.Jobs["35"] = &projRow{Done: 1, Option: "94"}
	if got := r.m.upgradeOn("LCR11JA67NJ15N3"); got != "94" {
		t.Fatalf("built with 94: %q", got)
	}
	// No option kept: the first, as upgradeOption falls back to options[0].
	r.m.st.Jobs["35"].Option = ""
	if got := r.m.upgradeOn(nightshade); got != "93" {
		t.Fatalf("built with none: %q", got)
	}
	if fn, _ := r.hub.ProvidedLocked("projects.upgrade").(func(string) string); fn == nil || fn(nightshade) != "93" {
		t.Fatal("projects.upgrade is not provided")
	}
}
