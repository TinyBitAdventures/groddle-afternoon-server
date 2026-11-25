package main

import "testing"

// The game's js/street-creatures.js flierSeed and flierAt computed these (lane flyk, 2026-11-24):
// the server's port gives the same seed and the same x to the bit, so the Guano it drops lies under
// the Batterfly every page draws.
func TestTheFlierPathIsTheGames(t *testing.T) {
	for _, c := range []struct {
		kind   string
		index  int
		spotX  float64
		worldW float64
		seed   uint32
		t, x   float64
	}{
		{"batterfly", 0, 5929.3, 6403, 3320219405, 0, 618.70215230670453},
		{"batterfly", 0, 5929.3, 6403, 3320219405, 61234.5, 3038.5210226665886},
		{"batterfly", 0, 5929.3, 6403, 3320219405, 3600000, 5430.5211993833127},
		{"batterfly", 0, 5929.3, 6403, 3320219405, 553312345678, 1933.2908161937473},
		{"batterfly", 1, 1521, 6403, 1740578566, 0, 2447.0851482117068},
		{"batterfly", 1, 1521, 6403, 1740578566, 61234.5, 4710.8342720243609},
		{"batterfly", 1, 1521, 6403, 1740578566, 3600000, 4261.8940340292147},
		{"batterfly", 1, 1521, 6403, 1740578566, 553312345678, 3135.8557077181490},
		{"batterfly", 3, 812.5, 1500, 587992188, 0, 935.83526432156987},
		{"batterfly", 3, 812.5, 1500, 587992188, 61234.5, 909.26641276107875},
		{"batterfly", 3, 812.5, 1500, 587992188, 3600000, 635.73079512210688},
		{"batterfly", 3, 812.5, 1500, 587992188, 553312345678, 1054.8816364706493},
		{"butterfly", 2, 4202.5, 4800, 4252145239, 0, 3740.9083453499011},
		{"butterfly", 2, 4202.5, 4800, 4252145239, 61234.5, 1307.2582526779352},
		{"butterfly", 2, 4202.5, 4800, 4252145239, 3600000, 498.80613465372971},
		{"butterfly", 2, 4202.5, 4800, 4252145239, 553312345678, 545.52956313595359},
		{"batterfly", 0, 300, 100, 3320221960, 0, 50},
		{"batterfly", 0, 300, 100, 3320221960, 553312345678, 50},
	} {
		if s := flierSeed(c.kind, c.index, c.spotX); s != c.seed {
			t.Fatalf("%s:%d at %v: seed %d, the game's %d", c.kind, c.index, c.spotX, s, c.seed)
		}
		if x := flierX(c.kind, c.seed, c.worldW, c.t); x != c.x {
			t.Fatalf("%s:%d at %v ms: x %.17g, the game's %.17g", c.kind, c.index, c.t, x, c.x)
		}
	}
}

// Every Batterfly on every street keeps inside its street, and every street with one knows its width.
func TestEveryBatterflyFliesInsideItsStreet(t *testing.T) {
	n := 0
	for tsid, spots := range sharedWorld.streets {
		for id, spot := range spots {
			if spot.kind != "batterfly" {
				continue
			}
			if spot.worldW <= 0 {
				t.Fatalf("%s %s: no street width", tsid, id)
			}
			seed := flierSeed("batterfly", spot.index, spot.x)
			for ms := 0.0; ms < 1_800_000; ms += 5000 {
				if x := flierX("batterfly", seed, spot.worldW, ms); x < 0 || x > spot.worldW {
					t.Fatalf("%s %s at %v ms: x %v of %v", tsid, id, ms, x, spot.worldW)
				}
			}
			n++
		}
	}
	if n < 100 {
		t.Fatalf("only %d Batterflies", n)
	}
}
