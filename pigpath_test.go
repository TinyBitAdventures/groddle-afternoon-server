package main

import "testing"

// Values the game computed (js/street-creatures.js piggyWalkAt, chickenSeed, piggyPlan) for three
// homes: the server must put a Piggy exactly where every page draws it.
func TestAPiggysPathMatchesTheGame(t *testing.T) {
	homes := []pigHome{
		{baseX: 1234.5, rng: 90, dir: 1, trunks: []float64{1300, 900}, trunkPx: 120},
		{baseX: 402.3, rng: 61, dir: -1, trunkPx: 120},
		{baseX: 2999.9, rng: 90, dir: -1, trunks: []float64{3050.2}, trunkPx: 120},
	}
	want := []struct {
		home int
		seed uint32
		t    float64
		x    float64
		dir  int
		pose string
		ms   float64
	}{
		{0, 2027807255, 0, 1234.5, 1, "walk", 186666.66666666666},
		{0, 2027807255, 12345, 1191.615, 1, "walk", 186666.66666666666},
		{0, 2027807255, 560000000777, 1255.4790007858035, 1, "walk", 186666.66666666666},
		{0, 2027807255, 560000098765, 1275.0449992141973, -1, "walk", 186666.66666666666},
		{0, 2027807255, 561000000003, 1169.7, -1, "look", 186666.66666666666},
		{1, 387277287, 0, 402.29999999999995, -1, "walk", 136962.96296296295},
		{1, 387277287, 12345, 403.29999999999995, -1, "look", 136962.96296296295},
		{1, 387277287, 560000000777, 426.2790015469573, 1, "walk", 136962.96296296295},
		{1, 387277287, 560000098765, 443.29999999999995, -1, "sniff", 136962.96296296295},
		{1, 387277287, 561000000003, 456.2189984502803, -1, "walk", 136962.96296296295},
		{2, 3041710494, 0, 2999.9, -1, "walk", 204933.3333333333},
		{2, 3041710494, 12345, 3026.585, -1, "walk", 204933.3333333333},
		{2, 3041710494, 560000000777, 3013.679001431522, 1, "walk", 204933.3333333333},
		{2, 3041710494, 560000098765, 3009.844998568478, -1, "walk", 204933.3333333333},
		{2, 3041710494, 561000000003, 2980.0189985659217, -1, "walk", 204933.3333333333},
	}
	for _, w := range want {
		h := homes[w.home]
		if seed := creatureSeed(w.home+3, h.baseX); seed != w.seed {
			t.Fatalf("home %d: seed %d, the game's %d", w.home, seed, w.seed)
		}
		if ms := pigPlan(h, w.seed).ms; ms != w.ms {
			t.Fatalf("home %d: round %v ms, the game's %v", w.home, ms, w.ms)
		}
		x, dir, pose := pigAt(h, w.seed, w.t)
		if x != w.x || dir != w.dir || pose != w.pose {
			t.Fatalf("home %d at %v: %v %d %s, the game's %v %d %s", w.home, w.t, x, dir, pose, w.x, w.dir, w.pose)
		}
	}
}
