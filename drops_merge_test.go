package main

import (
	"testing"
)

// Merging on a house or Tower floor (drops.go header, "Merging"; #655): takeable_drop's
// apiPutItemIntoPosition merged a drop into a like stack below its stackmax (takeable.js 202-207;
// Eleven Location.addItem 475-503, within 100 px), and the Decoration Mode move merged too (main.js
// doModifyItem 4852-4890).

// houseRig: Ada (7) at home in HOUSE_7 (its left end drawn at x 750), Bo (9) visiting; the site merges.
func houseRig(t *testing.T) (r *dropRig, ada, bo *Client) {
	r = newDropRig(t)
	r.site.merges = true
	ada, bo = r.connect(7, "Ada"), r.connect(9, "Bo")
	r.at(ada, "HOUSE_7", 900, 600)
	r.at(bo, "HOUSE_7", 1400, 600)
	r.settle()
	drain(ada)
	drain(bo)
	return
}

func (r *dropRig) houseDrop(cl *Client, c string, n int, x float64, extra map[string]any) map[string]any {
	msg := map[string]any{"t": "ground_drop", "ref": "d1", "item": map[string]any{"c": c, "n": n}, "x": x, "y": 600, "ox": 750}
	for k, v := range extra {
		msg[k] = v
	}
	r.do(cl, msg)
	r.settle()
	for _, f := range drain(cl) {
		if f["t"] == "ground_dropped" {
			return f
		}
	}
	return nil
}

func TestADropOnAHouseFloorJoinsTheLikeStackThereUpToItsStackmax(t *testing.T) {
	r, ada, bo := houseRig(t)
	first := r.houseDrop(ada, "gem_moonstone", 2, 1000, nil)
	a := first["stack"].(map[string]any)["id"].(string)
	drain(bo)

	// One more where it lies: it joins (the site is told which, and the stackmax).
	got := r.houseDrop(ada, "gem_moonstone", 1, 1010, nil)
	c := r.site.drops[1]
	if c.Into != a || c.Max != 3 {
		t.Fatalf("the drop names the like stack and the stackmax: %+v", c)
	}
	if got["status"] != "settled" || got["stack"] != nil || got["into"] != a || got["item"].(map[string]any)["n"] != float64(1) {
		t.Fatalf("the dropper is told what left the pack and where it went: %v", got)
	}
	if s := r.m.stacks[a]; s == nil || s.N != 3 || len(r.m.byStreet["HOUSE_7"]) != 1 {
		t.Fatalf("one stack of 3: %+v", r.m.byStreet["HOUSE_7"])
	}
	if s := only(t, drain(bo), "ground_added")["stack"].(map[string]any); s["id"] != a || s["n"] != float64(3) {
		t.Fatalf("the visitor sees the stack grow: %v", s)
	}

	// #655: a full stack (a Moonstone's stackmax is 3) takes no fourth; it lies as its own stack.
	r.houseDrop(ada, "gem_moonstone", 1, 1000, nil)
	if c := r.site.drops[2]; c.Into != "" {
		t.Fatalf("a full stack is not named: %+v", c)
	}
	if len(r.m.byStreet["HOUSE_7"]) != 2 {
		t.Fatalf("two stacks: %v", r.m.byStreet["HOUSE_7"])
	}
	drain(bo)

	// Two onto a stack of 1: one joins, the other lies on its own.
	d := r.site.drops[2].Op
	r.houseDrop(ada, "gem_moonstone", 3, 1000, map[string]any{"into": d})
	if s := r.m.stacks[d]; s == nil || s.N != 3 {
		t.Fatalf("filled to 3: %+v", s)
	}
	rest := r.site.drops[3].Op
	if s := r.m.stacks[rest]; s == nil || s.N != 1 {
		t.Fatalf("the rest on its own: %+v", s)
	}
	_ = ada
}

func TestOnlyANearbyLikeStackOfTheSameDropperOrTheOwnerTakesADrop(t *testing.T) {
	r, ada, bo := houseRig(t)
	own := r.houseDrop(ada, "apple", 2, 1000, nil)["stack"].(map[string]any)["id"].(string)
	// Far off (over 100 px): on its own.
	r.houseDrop(ada, "apple", 1, 1200, nil)
	if r.site.drops[1].Into != "" {
		t.Fatalf("too far: %+v", r.site.drops[1])
	}
	// merge false (Decoration Mode set it beside a stack, not on it): on its own.
	r.houseDrop(ada, "apple", 1, 1005, map[string]any{"merge": false})
	if r.site.drops[2].Into != "" {
		t.Fatalf("merge false: %+v", r.site.drops[2])
	}
	// The visitor's drop by the owner's stack joins it (the owner's stacks there never go home).
	r.move(bo, 1000, 600)
	drain(bo)
	r.houseDrop(bo, "apple", 1, 1000, nil)
	if c := r.site.drops[3]; c.Into != own {
		t.Fatalf("a visitor's drop joins the owner's stack: %+v", c)
	}
	// The owner's drop by only the visitor's stack does not (it would go home with the visitor's).
	r.houseDrop(bo, "cherry", 1, 1000, nil)
	if c := r.site.drops[4]; c.Into != "" || c.ID != 9 {
		t.Fatalf("the visitor's own cherry: %+v", c)
	}
	r.houseDrop(ada, "cherry", 1, 1000, nil)
	if c := r.site.drops[5]; c.Into != "" {
		t.Fatalf("the owner's drop never joins a visitor's stack: %+v", c)
	}
	// A public street never merges.
	r2 := newDropRig(t)
	r2.site.merges = true
	cy := r2.connect(11, "Cy")
	r2.at(cy, dropStreet, 1000, -100)
	drain(cy)
	r2.do(cy, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "apple", "n": 1}, "x": 1000, "y": -120})
	r2.settle()
	r2.do(cy, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "apple", "n": 1}, "x": 1000, "y": -120})
	r2.settle()
	if c := r2.site.drops[1]; c.Into != "" {
		t.Fatalf("a public street: %+v", c)
	}
}

func TestTheOwnerSetsADropAnywhereInTheirHouseAndAVisitorOnlyNearby(t *testing.T) {
	r, ada, bo := houseRig(t)
	r.houseDrop(ada, "apple", 1, 2400, nil)
	if c := r.site.drops[0]; c.X != 2400-750 {
		t.Fatalf("Decoration Mode's spot across the room: %+v", c)
	}
	r.houseDrop(bo, "apple", 1, 2900, nil)
	if c := r.site.drops[1]; c.X != 1400-750 {
		t.Fatalf("a visitor's far drop lands at their feet: %+v", c)
	}
}

func TestTheOwnerMovesAFloorStackAndItStaysShared(t *testing.T) {
	r, ada, bo := houseRig(t)
	a := r.houseDrop(ada, "apple", 2, 1000, nil)["stack"].(map[string]any)["id"].(string)
	r.move(ada, 2000, 600)
	b := r.houseDrop(ada, "apple", 3, 2000, nil)["stack"].(map[string]any)["id"].(string)
	drain(bo)

	// Along the floor: the site moves it (x from the house), everyone sees it where it went.
	r.do(ada, map[string]any{"t": "ground_move", "id": a, "x": 1600, "y": 600, "ox": 750})
	if s := r.m.stacks[a]; s.picking == 0 {
		t.Fatal("nobody takes it while the site weighs the move")
	}
	r.settle()
	if len(r.site.moves) != 1 || r.site.moves[0].Drop != a || r.site.moves[0].X != 850 || r.site.moves[0].Street != "HOUSE-7" || r.site.moves[0].ID != 7 {
		t.Fatalf("the site is asked: %+v", r.site.moves)
	}
	if f := pick(t, drain(ada), "ground_moved"); f["status"] != "settled" {
		t.Fatalf("Ada is told: %v", f)
	}
	if s := only(t, drain(bo), "ground_added")["stack"].(map[string]any); s["id"] != a || s["x"] != float64(850) || s["n"] != float64(2) {
		t.Fatalf("the visitor sees it moved, still shared: %v", s)
	}

	// Onto the like stack: it joins it and its own record goes.
	r.do(ada, map[string]any{"t": "ground_move", "id": a, "x": 2000, "y": 600, "ox": 750, "into": b})
	r.settle()
	if c := r.site.moves[1]; c.Into != b || c.Max != 100 {
		t.Fatalf("the move names the like stack: %+v", c)
	}
	if r.m.stacks[a] != nil || r.m.stacks[b].N != 5 {
		t.Fatalf("one stack of 5: %+v %+v", r.m.stacks[a], r.m.stacks[b])
	}
	frames := drain(bo)
	if g := pick(t, frames, "ground_gone"); g["id"] != a || g["why"] != "merged" {
		t.Fatalf("the moved stack is gone for everyone: %v", g)
	}
	if s := pick(t, frames, "ground_added")["stack"].(map[string]any); s["id"] != b || s["n"] != float64(5) {
		t.Fatalf("and the one it joined grew: %v", s)
	}

	// A visitor moves nothing.
	r.do(bo, map[string]any{"t": "ground_move", "id": b, "x": 1, "y": 600, "ox": 750})
	if f := only(t, drain(bo), "ground_moved"); f["why"] != "place" || f["text"] != moveTextPlace || len(r.site.moves) != 2 {
		t.Fatalf("a visitor is refused: %v", f)
	}
}

// A pickup (or a Piggy) never takes from a stack while a merge into it is being weighed: the site's part
// ids rely on the count the server holds, and a merge raises it (LedgerDrop partId).
func TestAStackBeingMergedIntoIsTakenByNobody(t *testing.T) {
	r, ada, bo := houseRig(t)
	a := r.houseDrop(ada, "apple", 2, 1000, nil)["stack"].(map[string]any)["id"].(string)
	r.move(ada, 1500, 600)
	r.move(bo, 1010, 600)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "ground_drop", "ref": "d2", "item": map[string]any{"c": "apple", "n": 1}, "x": 1010, "y": 600, "ox": 750, "into": a})
	r.do(bo, map[string]any{"t": "ground_pickup", "id": a, "n": 1, "ox": 750})
	if f := only(t, drain(bo), "ground_taken"); f["why"] != "busy" {
		t.Fatalf("busy while the merge is weighed: %v", f)
	}
	r.settle()
	if r.m.stacks[a].picking != 0 || r.m.stacks[a].N != 3 {
		t.Fatalf("free again, with 3: %+v", r.m.stacks[a])
	}
}
