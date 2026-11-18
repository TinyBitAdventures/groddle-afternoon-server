package main

import (
	"slices"
	"testing"
)

// G960 left (rook.go did not know built upgrades): a built street upgrade can give a street a shrine
// or another Giant's (js/street-jobs.js upgradeEcologyEntry; game.js streetShrineGiant), and the
// Rook reads the street's shrine as it stands now (rook.spot).
const sylvanGrove = "GCR10NQRPKJ16H6" // Piggy & Patch Place: options 15 (Shrine to Tii) and 16 (Zille)

func TestRookWorldHasUpgradeShrines(t *testing.T) {
	w := rookData
	if w.streets[sylvanGrove].giant != "friendly" {
		t.Fatalf("Sylvan Grove's own shrine: %+v", w.streets[sylvanGrove])
	}
	if w.upgrades[sylvanGrove]["15"].giant != "ti" || w.upgrades[sylvanGrove]["16"].giant != "zille" {
		t.Fatalf("Sylvan Grove upgrades: %+v", w.upgrades[sylvanGrove])
	}
	// Nightshade Woods has no shrine until its upgrade (options 93 and 94) is built; it is still on the schedule's list.
	if _, own := w.streets[nightshade]; own {
		t.Fatal("Nightshade Woods should have no shrine of its own")
	}
	if !w.known(nightshade) || !slices.Contains(w.order, nightshade) || !slices.Contains(w.hubs["58"], nightshade) {
		t.Fatal("Nightshade Woods is not listed for when its upgrade is built")
	}
}

func TestRookReadsTheBuiltUpgradesShrine(t *testing.T) {
	r := newRookRig(t, false)
	built := map[string]string{}
	r.hub.provided["projects.upgrade"] = func(s string) string { return built[s] }

	spot, ok := r.mod.spot(sylvanGrove)
	if !ok || spot.giant != "friendly" {
		t.Fatalf("before the upgrade: %+v %v", spot, ok)
	}
	if _, ok := r.mod.spot(nightshade); ok {
		t.Fatal("Nightshade Woods is rookable before its upgrade adds a shrine")
	}
	// A Tii emblem is only an offering on the street's own Friendly shrine...
	if _, emblem, _ := r.mod.strikeDamage("emblem_ti", 1, spot.giant, 0); emblem {
		t.Fatal("a Tii emblem counted on a Friendly shrine")
	}

	built[sylvanGrove] = "15"
	built[nightshade] = "94"
	spot, ok = r.mod.spot(sylvanGrove)
	if !ok || spot.giant != "ti" {
		t.Fatalf("after Piggy & Patch Place: %+v %v", spot, ok)
	}
	// ...and does its 250 once the upgrade has made it a Shrine to Tii (shrines.js launchAttack 1036).
	if damage, emblem, why := r.mod.strikeDamage("emblem_ti", 1, spot.giant, 0); !emblem || damage != rookEmblemDamage || why != "" {
		t.Fatalf("Tii emblem on the upgraded shrine: %d %v %q", damage, emblem, why)
	}
	n, ok := r.mod.spot(nightshade)
	if !ok || n.giant != "mab" || n.hub != "58" {
		t.Fatalf("Nightshade Woods after its upgrade: %+v %v", n, ok)
	}
	// An option whose shrine is the street's own reads the street's row.
	built[sylvanGrove] = "99"
	if spot, _ := r.mod.spot(sylvanGrove); spot.giant != "friendly" {
		t.Fatalf("an unlisted option: %+v", spot)
	}
}
