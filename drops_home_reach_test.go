package main

import "testing"

// #719 (G1224 R50): the owner at home picks up any stack in their own house, Tower floor or on
// their home street from anywhere there (GameServerJS main.js 2955-2979: "if (at_home &&
// msg.verb == 'pickup') check_for_proximity = false;" and "else if (range > config.verb_radius
// && !at_home)"; at_home is pc.houses_is_at_home, inc_houses.js 1167-1169). Decoration Mode's
// move of a floor stack onto a table is such a pickup: the avatar is hidden, so where it stood
// must not matter. A visitor far away is still told to get closer.
func TestTheOwnerAtHomePicksUpFromAnywhereThere(t *testing.T) {
	r := newDropRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.at(ada, "HOUSE_7", 900, 600)
	r.at(bo, "HOUSE_7", 960, 600)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "ground_drop", "ref": "d1", "item": map[string]any{"c": "apple", "n": 3}, "x": 920, "y": 600, "ox": 750})
	r.settle()
	op := r.site.drops[0].Op
	r.do(bo, map[string]any{"t": "ground_drop", "ref": "d2", "item": map[string]any{"c": "cherry", "n": 2}, "x": 960, "y": 600, "ox": 750})
	r.settle()
	op2 := r.site.drops[1].Op
	// Both walk 2000 px down the long house, past the 800 px verb radius.
	r.move(ada, 2900, 600)
	r.move(bo, 2960, 600)
	drain(ada)
	drain(bo)
	// A visitor far away is told to get closer (main.js:2978).
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op, "ox": 750})
	if f := only(t, drain(bo), "ground_taken"); f["why"] != "far" {
		t.Fatalf("a visitor far away: %v", f)
	}
	// The owner is not, whoever dropped it.
	r.do(ada, map[string]any{"t": "ground_pickup", "id": op2, "ox": 750})
	for _, f := range drain(ada) {
		if f["t"] == "ground_taken" && f["status"] == "refused" {
			t.Fatalf("the owner at home is refused: %v", f)
		}
	}
	r.settle()
	if len(r.site.takes) != 1 || r.site.takes[0] != op2+":7:pickup" {
		t.Fatalf("the owner took it: %v", r.site.takes)
	}

	// Her Tower floor too.
	r.at(ada, "TOWER_7_2", 500, 600)
	r.at(bo, "TOWER_7_2", 520, 600)
	drain(ada)
	drain(bo)
	r.do(bo, map[string]any{"t": "ground_drop", "ref": "d3", "item": map[string]any{"c": "apple", "n": 1}, "x": 520, "y": 600})
	r.settle()
	op3 := r.site.drops[2].Op
	r.move(ada, 2500, 600)
	r.move(bo, 2500, 600)
	drain(ada)
	r.do(ada, map[string]any{"t": "ground_pickup", "id": op3})
	for _, f := range drain(ada) {
		if f["t"] == "ground_taken" && f["status"] == "refused" {
			t.Fatalf("the owner on her Tower floor is refused: %v", f)
		}
	}
	r.settle()
	if len(r.site.takes) != 2 || r.site.takes[1] != op3+":7:pickup" {
		t.Fatalf("the owner took it on her Tower floor: %v", r.site.takes)
	}
}
