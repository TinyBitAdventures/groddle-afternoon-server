package main

import "testing"

// G1327 (R50): homes_unexpand (GameServerJS locations/inc_homes.js 154-183) moved every item in the last
// 300 px of the house 150 px left, ground stacks too (166-172). The shared stacks (G1190) stayed where they
// were, so one by the old right wall lay past the new one. The owner's game now sends the shift, and every
// floor's stacks move with it, all or none, once.

const (
	shiftA = "drop-00000000000000a1" // Ada's, by the old right wall of the ground floor (room x)
	shiftB = "drop-00000000000000b2" // Bo's, mid-room: stays
	shiftC = "drop-00000000000000c3" // Ada's, upstairs by the right wall
	shiftD = "drop-00000000000000d4" // on a Tower floor
)

func shiftRig(t *testing.T) (*dropRig, *Client, *Client) {
	r := newDropRig(t)
	rows := []dropRow{
		{ID: shiftA, By: 7, Name: "Ada", Street: "HOUSE-7", X: 1900, Y: 620, C: "apple", N: 3, At: 1},
		{ID: shiftB, By: 9, Name: "Bo", Street: "HOUSE-7", X: 1000, Y: 620, C: "cherry", N: 2, At: 2},
		{ID: shiftC, By: 7, Name: "Ada", Street: "HOUSE-7-1", X: 1800, Y: 620, C: "apple", N: 1, At: 3},
		{ID: shiftD, By: 7, Name: "Ada", Street: "TOWER-7-2", X: 1900, Y: 620, C: "apple", N: 1, At: 4},
	}
	for _, row := range rows {
		r.m.addRow(row)
		r.site.ground = append(r.site.ground, row)
	}
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.at(ada, "HOUSE_7", 2600, 600)
	r.at(bo, "HOUSE_7", 1700, 600)
	r.settle()
	drain(ada)
	drain(bo)
	return r, ada, bo
}

func shiftX(r *dropRig, id string) float64 { return r.m.stacks[id].X }

func TestTheOwnersUnexpandMovesTheSharedStacksInTheLastStretchOfEveryFloor(t *testing.T) {
	r, ada, bo := shiftRig(t)
	// A visitor cannot move the owner's floor.
	r.do(bo, map[string]any{"t": "ground_shift", "from": 1750, "dx": -150})
	if f := oneOf(t, drain(bo), "ground_shifted"); f["status"] != "refused" || f["why"] != "place" {
		t.Fatalf("a visitor's shift: %v", f)
	}
	// Only homes_unexpand's one shift.
	r.do(ada, map[string]any{"t": "ground_shift", "from": 1750, "dx": -300})
	if f := oneOf(t, drain(ada), "ground_shifted"); f["why"] != "bad" {
		t.Fatalf("a shift of 300: %v", f)
	}
	r.settle()
	if len(r.site.shifts) != 0 {
		t.Fatalf("refused shifts reached the site: %v", r.site.shifts)
	}

	r.do(ada, map[string]any{"t": "ground_shift", "from": 1750, "dx": -150})
	r.settle()
	if len(r.site.shifts) != 1 {
		t.Fatalf("one ask of the site: %v", r.site.shifts)
	}
	c := r.site.shifts[0]
	if c.ID != 7 || c.From != 1750 || c.Dx != -150 || len(c.Streets) != 3 || c.Streets[0] != "HOUSE-7" || c.Streets[1] != "HOUSE-7-1" || c.Streets[2] != "HOUSE-7-2" {
		t.Fatalf("the shift asked: %+v", c)
	}
	if x := shiftX(r, shiftA); x != 1750 {
		t.Fatalf("the stack by the old wall: x %v, want 1750", x)
	}
	if x := shiftX(r, shiftB); x != 1000 {
		t.Fatalf("the stack mid-room moved: x %v", x)
	}
	if x := shiftX(r, shiftC); x != 1650 {
		t.Fatalf("upstairs: x %v, want 1650", x)
	}
	if x := shiftX(r, shiftD); x != 1900 {
		t.Fatalf("the Tower is not the house: x %v", x)
	}
	if f := oneOf(t, drain(ada), "ground_shifted"); f["status"] != "settled" || f["n"] != float64(2) {
		t.Fatalf("the owner's answer: %v", f)
	}
	// Everyone on the floor sees it move.
	st := oneOf(t, drain(bo), "ground_state")
	for _, s := range st["stacks"].([]any) {
		s := s.(map[string]any)
		if s["id"] == shiftA && s["x"] != float64(1750) {
			t.Fatalf("Bo sees it at %v", s["x"])
		}
	}

	// On her Tower floor, that floor alone.
	r.at(ada, "TOWER_7_2", 1900, 600)
	r.settle()
	drain(ada)
	r.do(ada, map[string]any{"t": "ground_shift", "from": 1750, "dx": -150})
	r.settle()
	if c := r.site.shifts[1]; len(c.Streets) != 1 || c.Streets[0] != "TOWER-7-2" {
		t.Fatalf("the Tower's shift: %+v", c)
	}
	if shiftX(r, shiftD) != 1750 || shiftX(r, shiftA) != 1750 {
		t.Fatalf("after the Tower's shift: tower %v, house %v", shiftX(r, shiftD), shiftX(r, shiftA))
	}

	// Nowhere else: a public street.
	r.at(ada, "LA5101HF7F429V", 100, 600)
	r.settle()
	drain(ada)
	r.do(ada, map[string]any{"t": "ground_shift", "from": 0, "dx": -150})
	if f := oneOf(t, drain(ada), "ground_shifted"); f["why"] != "place" {
		t.Fatalf("a public street: %v", f)
	}
}

// A lost answer leaves this server's stacks as they were (none half moved) and asks again with the same op;
// the site answers as recorded, moving nothing twice. Meanwhile nothing is dropped or moved there.
func TestAShiftWithALostAnswerIsAskedAgainAndMovesEachStackOnce(t *testing.T) {
	r, ada, _ := shiftRig(t)
	r.site.lost = 1 // the site moved them; its answer (each of httpLedger's tries) never came
	r.do(ada, map[string]any{"t": "ground_shift", "from": 1750, "dx": -150})
	r.settle()
	if shiftX(r, shiftA) != 1900 || shiftX(r, shiftC) != 1800 {
		t.Fatalf("no answer yet, nothing moved here: %v %v", shiftX(r, shiftA), shiftX(r, shiftC))
	}
	for _, f := range drain(ada) {
		if f["t"] == "ground_shifted" {
			t.Fatalf("answered before the site did: %v", f)
		}
	}
	// While it waits: no drop and no move on the floor.
	r.do(ada, map[string]any{"t": "ground_drop", "ref": "d1", "item": map[string]any{"c": "apple", "n": 1}, "x": 2600, "y": 600, "ox": 750})
	if f := only(t, drain(ada), "ground_dropped"); f["why"] != "busy" {
		t.Fatalf("a drop while the shift waits: %v", f)
	}
	r.do(ada, map[string]any{"t": "ground_move", "id": shiftB, "x": 900, "y": 620, "ox": 750})
	if f := only(t, drain(ada), "ground_moved"); f["why"] != "busy" {
		t.Fatalf("a move while the shift waits: %v", f)
	}
	// A second shift waits its turn too.
	r.do(ada, map[string]any{"t": "ground_shift", "from": 1600, "dx": -150})
	if f := oneOf(t, drain(ada), "ground_shifted"); f["why"] != "busy" {
		t.Fatalf("a second shift: %v", f)
	}
	if n := r.fire(dropLoadRetry); n != 1 {
		t.Fatalf("asked again once: %d", n)
	}
	r.settle()
	ops := map[string]bool{}
	for _, c := range r.site.shifts {
		ops[c.Op] = true
	}
	if len(r.site.shifts) != 2 || len(ops) != 1 {
		t.Fatalf("the same op asked again: %+v", r.site.shifts)
	}
	if shiftX(r, shiftA) != 1750 || shiftX(r, shiftC) != 1650 || shiftX(r, shiftB) != 1000 {
		t.Fatalf("moved once: %v %v %v", shiftX(r, shiftA), shiftX(r, shiftC), shiftX(r, shiftB))
	}
	for _, row := range r.site.ground {
		if row.ID == shiftA && row.X != 1750 {
			t.Fatalf("the site moved it twice: %v", row.X)
		}
	}
	if f := oneOf(t, drain(ada), "ground_shifted"); f["status"] != "settled" {
		t.Fatalf("the answer: %v", f)
	}
	// Free again.
	r.site.held = map[string]int{shiftB: 2}
	r.do(ada, map[string]any{"t": "ground_move", "id": shiftB, "x": 1650, "y": 620, "ox": 750})
	r.settle()
	if f := oneOf(t, drain(ada), "ground_moved"); f["status"] != "settled" {
		t.Fatalf("a move after: %v", f)
	}
}

// A drop on its way to the site when the owner un-expands was set in the old house: the shift waits for it
// to land, and it moves with the rest.
func TestAShiftWaitsForADropOnItsWayAndMovesItToo(t *testing.T) {
	r, ada, bo := shiftRig(t)
	r.move(bo, 2600, 600)
	r.do(bo, map[string]any{"t": "ground_drop", "ref": "d1", "item": map[string]any{"c": "cherry", "n": 1}, "x": 2600, "y": 600, "ox": 750})
	r.do(ada, map[string]any{"t": "ground_shift", "from": 1750, "dx": -150})
	if len(r.site.shifts) != 0 {
		t.Fatal("the shift went before the drop landed")
	}
	r.settle() // the drop lands
	if len(r.site.drops) != 1 {
		t.Fatalf("the drop: %v", r.site.drops)
	}
	op := r.site.drops[0].Op
	if r.m.stacks[op] == nil || r.m.stacks[op].X != 1850 {
		t.Fatalf("the drop landed at %+v", r.m.stacks[op])
	}
	r.site.ground = append(r.site.ground, dropRow{ID: op, By: 9, Street: "HOUSE-7", X: 1850, Y: 580, C: "cherry", N: 1})
	if len(r.site.shifts) != 0 {
		t.Fatal("asked before its wait")
	}
	r.fire(shiftWait)
	r.settle()
	if len(r.site.shifts) != 1 || shiftX(r, shiftA) != 1750 {
		t.Fatalf("after the drop landed: %+v, %v", r.site.shifts, shiftX(r, shiftA))
	}
	if r.m.stacks[op].X != 1700 {
		t.Fatalf("Bo's drop by the old wall: x %v, want 1700", r.m.stacks[op].X)
	}
}

// oneOf is the one frame of a kind among those sent.
func oneOf(t *testing.T, frames []map[string]any, kind string) map[string]any {
	t.Helper()
	if got := kinds(frames, kind); len(got) == 1 {
		return got[0]
	}
	t.Fatalf("want one %s, got %v", kind, frames)
	return nil
}
