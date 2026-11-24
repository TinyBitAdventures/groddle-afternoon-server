package main

import (
	"testing"
)

// Merging on a house or Tower floor (drops.go header, "Merging"; #655): takeable_drop's
// apiPutItemIntoPosition merged a drop into a like stack below its stackmax (takeable.js 202-207;
// Eleven Location.addItem 475-503, within 100 px), and the Decoration Mode move merged too (main.js
// doModifyItem 4852-4890). Ours: a pile at home has no stack limit (Austin 2026-11-24, "Let's remove the
// pile limits"), only the most a stack holds anywhere (dropPileMax, 9999).

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

func TestADropOnAHouseFloorJoinsTheLikeStackThere(t *testing.T) {
	r, ada, bo := houseRig(t)
	first := r.houseDrop(ada, "gem_moonstone", 2, 1000, nil)
	a := first["stack"].(map[string]any)["id"].(string)
	drain(bo)

	// One more where it lies: it joins (the site is told which, and the pile there).
	got := r.houseDrop(ada, "gem_moonstone", 1, 1010, nil)
	c := r.site.drops[1]
	if c.Into != a || c.Max != dropPileMax {
		t.Fatalf("the drop names the like stack and the pile: %+v", c)
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

	// Austin 2026-11-24 ("Let's remove the pile limits"): a Moonstone's stackmax is 3, but a pile at home
	// takes a fourth (Glitch's full stack took none, #655).
	r.houseDrop(ada, "gem_moonstone", 1, 1000, nil)
	if c := r.site.drops[2]; c.Into != a {
		t.Fatalf("the pile of 3 is named: %+v", c)
	}
	if s := r.m.stacks[a]; s == nil || s.N != 4 || len(r.m.byStreet["HOUSE_7"]) != 1 {
		t.Fatalf("one pile of 4: %+v", r.m.byStreet["HOUSE_7"])
	}
	drain(bo)

	// Three set on it at once: all join.
	r.houseDrop(ada, "gem_moonstone", 3, 1000, map[string]any{"into": a})
	if s := r.m.stacks[a]; s == nil || s.N != 7 || len(r.m.byStreet["HOUSE_7"]) != 1 {
		t.Fatalf("one pile of 7: %+v", r.m.byStreet["HOUSE_7"])
	}
}

// A pile at home holds no more than a stack holds anywhere (9999): a drop that would pass it joins up to
// it, and the rest lies beside it as its own stack.
func TestAPileAtHomeStopsAt9999AndTheRestLiesBeside(t *testing.T) {
	r, ada, _ := houseRig(t)
	a := r.houseDrop(ada, "gem_moonstone", dropPileMax-1, 1000, nil)["stack"].(map[string]any)["id"].(string)
	r.m.rates = map[int64]*bucket{}
	got := r.houseDrop(ada, "gem_moonstone", 3, 1000, map[string]any{"into": a})
	if got["status"] != "settled" || got["into"] != a {
		t.Fatalf("it joins the pile: %v", got)
	}
	rest := r.site.drops[1].Op
	if s := r.m.stacks[a]; s == nil || s.N != dropPileMax {
		t.Fatalf("filled to 9999: %+v", s)
	}
	if s := r.m.stacks[rest]; s == nil || s.N != 2 {
		t.Fatalf("the other 2 beside it: %+v", s)
	}
	// A full pile is named no more: the next one joins the stack beside it.
	r.m.rates = map[int64]*bucket{}
	r.houseDrop(ada, "gem_moonstone", 1, 1000, map[string]any{"into": a})
	if c := r.site.drops[2]; c.Into != "" {
		t.Fatalf("a full pile is not named: %+v", c)
	}
	r.m.rates = map[int64]*bucket{}
	r.houseDrop(ada, "gem_moonstone", 1, 1000, nil)
	if c := r.site.drops[3]; c.Into != rest {
		t.Fatalf("the nearest pile with room takes it: %+v", c)
	}
	// One drop holds no more than 9999.
	r.m.rates = map[int64]*bucket{}
	if f := r.houseDrop(ada, "gem_moonstone", dropPileMax+1, 2000, nil); f["status"] != "refused" || f["why"] != "count" {
		t.Fatalf("10000 in one drop: %v", f)
	}
}

func TestOnlyANearbyLikeStackOfTheSameDropperTakesADrop(t *testing.T) {
	r, ada, bo := houseRig(t)
	r.houseDrop(ada, "apple", 2, 1000, nil)
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
	// The visitor's drop by the owner's stack lies on its own (Austin 2026-11-24: only one player's own
	// stacks join, so nobody's goods change hands).
	r.move(bo, 1000, 600)
	drain(bo)
	r.houseDrop(bo, "apple", 1, 1000, nil)
	if c := r.site.drops[3]; c.Into != "" {
		t.Fatalf("a visitor's drop never joins the owner's stack: %+v", c)
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
	if c := r.site.moves[1]; c.Into != b || c.Max != dropPileMax {
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

// Austin 2026-11-24: "Let's remove the pile limits." Any class (an Emblem of Mab, stackmax 1; a Moonstone,
// stackmax 3) joins a like stack of the same dropper on a house or Tower floor with no stack limit; a tool
// (wear) or a class whose copies carry words (a Teleportation Script, which the game keeps one to a stack)
// never joins; a street never merges.
func TestPilesAtHomeHaveNoStackLimit(t *testing.T) {
	r, ada, _ := houseRig(t)
	first := r.houseDrop(ada, "emblem_mab", 1, 1000, nil)
	a := first["stack"].(map[string]any)["id"].(string)
	for i := 0; i < 4; i++ {
		r.m.rates = map[int64]*bucket{}
		got := r.houseDrop(ada, "emblem_mab", 1, 1010, nil)
		if got["status"] != "settled" || got["into"] != a {
			t.Fatalf("emblem %d joins the pile: %v", i+2, got)
		}
	}
	if c := r.site.drops[4]; c.Into != a || c.Max != dropPileMax {
		t.Fatalf("the site is told the stack and the pile: %+v", c)
	}
	if s := r.m.stacks[a]; s == nil || s.N != 5 || len(r.m.byStreet["HOUSE_7"]) != 1 {
		t.Fatalf("one pile of 5: %+v", r.m.byStreet["HOUSE_7"])
	}
	// A pile set down whole (3 at once) is one drop there, and joins too.
	r.m.rates = map[int64]*bucket{}
	if got := r.houseDrop(ada, "emblem_mab", 3, 1000, map[string]any{"into": a}); got["status"] != "settled" || got["into"] != a {
		t.Fatalf("three at once onto the pile: %v", got)
	}
	if s := r.m.stacks[a]; s.N != 8 {
		t.Fatalf("a pile of 8: %+v", s)
	}
	// A stackmax over 1 piles past it too: 3 Moonstones and 1 more make one pile of 4.
	r.m.rates = map[int64]*bucket{}
	m := r.houseDrop(ada, "gem_moonstone", 3, 1500, nil)["stack"].(map[string]any)["id"].(string)
	r.m.rates = map[int64]*bucket{}
	if got := r.houseDrop(ada, "gem_moonstone", 1, 1500, map[string]any{"into": m}); got["into"] != m || r.m.stacks[m].N != 4 {
		t.Fatalf("a Moonstone pile of 4: %v %+v", got, r.m.stacks[m])
	}
	// A pile of 4 Moonstones set down whole at home is one drop.
	r.m.rates = map[int64]*bucket{}
	if got := r.houseDrop(ada, "gem_moonstone", 4, 3000, nil); got["status"] != "settled" || got["stack"].(map[string]any)["n"] != float64(4) {
		t.Fatalf("four Moonstones at once: %v", got)
	}
	// A Teleportation Script (stackmax 1, words of its own: the game's keepsOwnStack) never joins, even with no words.
	r.m.rates = map[int64]*bucket{}
	s := r.houseDrop(ada, "teleportation_script", 1, 2000, nil)["stack"].(map[string]any)["id"].(string)
	r.m.rates = map[int64]*bucket{}
	r.houseDrop(ada, "teleportation_script", 1, 2000, map[string]any{"into": s})
	if c := r.site.drops[len(r.site.drops)-1]; c.Into != "" {
		t.Fatalf("a script keeps its own stack: %+v", c)
	}
	// A tool (its wear) never joins.
	r.m.rates = map[int64]*bucket{}
	p := r.houseDrop(ada, "pick", 1, 2400, map[string]any{"item": map[string]any{"c": "pick", "n": 1, "r": 100}})["stack"].(map[string]any)["id"].(string)
	r.m.rates = map[int64]*bucket{}
	r.houseDrop(ada, "pick", 1, 2400, map[string]any{"item": map[string]any{"c": "pick", "n": 1, "r": 100}, "into": p})
	if c := r.site.drops[len(r.site.drops)-1]; c.Into != "" {
		t.Fatalf("a tool keeps its own stack: %+v", c)
	}
	// A pile of emblems dropped whole on a street is still one stackmax: refused there.
	r2 := newDropRig(t)
	cy := r2.connect(11, "Cy")
	r2.at(cy, dropStreet, 1000, -100)
	drain(cy)
	r2.do(cy, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "emblem_mab", "n": 2}, "x": 1000, "y": -120})
	r2.settle()
	if f := only(t, drain(cy), "ground_dropped"); f["status"] != "refused" || f["why"] != "count" {
		t.Fatalf("two emblems in one street drop: %v", f)
	}
	// A public street never merges: two Moonstones dropped side by side lie as two stacks.
	for i := 0; i < 2; i++ {
		r2.m.rates = map[int64]*bucket{}
		r2.do(cy, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "gem_moonstone", "n": 1}, "x": 1000 + float64(i), "y": -120})
		r2.settle()
	}
	if len(r2.site.drops) != 2 || r2.site.drops[1].Into != "" || len(r2.m.byStreet[dropStreet]) != 2 {
		t.Fatalf("a street never merges: %+v %v", r2.site.drops, r2.m.byStreet[dropStreet])
	}
}

// The owner's floor move of one emblem pile onto another joins it whole (the same pile rule).
func TestAnEmblemPileMovedOntoAnotherJoinsIt(t *testing.T) {
	r, ada, _ := houseRig(t)
	a := r.houseDrop(ada, "emblem_mab", 2, 1000, nil)["stack"].(map[string]any)["id"].(string)
	r.m.rates = map[int64]*bucket{}
	b := r.houseDrop(ada, "emblem_mab", 1, 1600, nil)["stack"].(map[string]any)["id"].(string)
	r.m.rates = map[int64]*bucket{}
	r.do(ada, map[string]any{"t": "ground_move", "id": b, "x": 1000, "y": 600, "ox": 750, "into": a})
	r.settle()
	if c := r.site.moves[0]; c.Into != a {
		t.Fatalf("the move names the pile: %+v", c)
	}
	if s := r.m.stacks[a]; s == nil || s.N != 3 || r.m.stacks[b] != nil {
		t.Fatalf("one pile of 3: %+v", r.m.byStreet["HOUSE_7"])
	}
}
