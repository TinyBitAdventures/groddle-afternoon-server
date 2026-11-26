package main

import (
	"path/filepath"
	"testing"
	"time"
)

// G946, report #700: a Dust Trap is everyone's on its street, as items/dust_trap.js was one item
// there: the first player in springs it, it stays down for all for 2 x the time it stood ready
// (3 min to 1 h; the minimum with no ready_time), and comes back for all.
func TestADustTrapIsSprungForEveryoneOnTheStreet(t *testing.T) {
	r := newObjRig(t)
	const street, id = "GA5101HF7F429V5", "node:dust_trap:0"
	trap := sharedWorld.streets[street][id]
	if trap == nil || trap.class != dustTrapClass {
		t.Fatalf("the layout has no shared Dust Trap at %s %s", street, id)
	}
	a := r.player(1, "Ada", street, trap.x, trap.y)
	b := r.player(2, "Bo", street, trap.x+40, trap.y)
	r.drain(a)

	// Ada walks in first: down for everyone, the minimum wait (onCreate: no ready_time).
	r.act(a, 1, id, "trigger", nil)
	ok := r.only(a, "obj_ok")
	got := objOf(ok, id)
	if num(got["units"]) != 0 || num(got["in"]) != 181250 || num(ok["reset"]) != 180000 {
		t.Fatalf("the first spring reads %v (reset %v)", got, ok["reset"])
	}
	if before := ok["before"].(map[string]any); num(before["units"]) != 1 {
		t.Fatalf("the spring started from %v", before)
	}
	if seen := objOf(r.only(b, "objs"), id); seen == nil || num(seen["units"]) != 0 || num(seen["sprung"]) < 1 {
		t.Fatalf("Bo sees %v", seen)
	}
	// Bo walks into it while it is down: nothing (the source's collision did nothing).
	r.act(b, 1, id, "trigger", nil)
	if no := r.only(b, "obj_no"); no["code"] != "not_ready" {
		t.Fatalf("a down trap answered %v", no)
	}
	r.advance(10 * time.Second)
	if s := syncOf(r, b, id); num(s["sprung"]) != 10000 {
		t.Fatalf("down 10 s reads %v", s)
	}

	// Back for everyone: reset + 250 ms, makeReady 1 s on.
	r.advance(180*time.Second + 1250*time.Millisecond - 10*time.Second)
	if up := objOf(r.only(b, "objs"), id); up == nil || num(up["units"]) != 1 || up["sprung"] != nil {
		t.Fatalf("the reset reads %v", up)
	}
	r.drain(a)

	// Ready 10 minutes, then Bo's spring: 2 x 600 s.
	r.advance(10 * time.Minute)
	r.act(b, 2, id, "trigger", nil)
	if got := objOf(r.only(b, "obj_ok"), id); num(got["in"]) != 1_201_250 {
		t.Fatalf("a trap ready 10 min stays down %v", got)
	}
	r.advance(1_201_250 * time.Millisecond)
	// Ready 2 hours: the 1 hour cap.
	r.advance(2 * time.Hour)
	r.drain(a)
	r.act(a, 3, id, "trigger", nil)
	if got := objOf(r.only(a, "obj_ok"), id); num(got["in"]) != 3_601_250 {
		t.Fatalf("a trap ready 2 h stays down %v", got)
	}
	r.advance(3_601_250 * time.Millisecond)
	// Ready over a day (reset_time/2 > full_reset_time): the minimum again.
	r.advance(25 * time.Hour)
	r.drain(b)
	r.act(b, 3, id, "trigger", nil)
	if got := objOf(r.only(b, "obj_ok"), id); num(got["in"]) != 181250 {
		t.Fatalf("a trap ready a day stays down %v", got)
	}
}

func TestADustTrapsWaitOutlivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	const street, id = "GA5101HF7F429V5", "node:dust_trap:1"
	trap := sharedWorld.streets[street][id]
	a := r.player(1, "Ada", street, trap.x, trap.y)
	r.act(a, 1, id, "trigger", nil)
	r.only(a, "obj_ok")
	// Down 60 s when the server stops; a new one starts at once.
	r.advance(59 * time.Second)
	r2 := r.restart(path, r.now.Add(time.Second))
	b := r2.player(2, "Bo", street, trap.x, trap.y)
	s := syncOf(r2, b, id)
	if num(s["units"]) != 0 || num(s["in"]) != 121250 || num(s["sprung"]) != 60000 {
		t.Fatalf("after a restart the trap reads %v", s)
	}
	r2.advance(121250 * time.Millisecond)
	if up := objOf(r2.only(b, "objs"), id); num(up["units"]) != 1 {
		t.Fatalf("the trap never came back: %v", up)
	}
	// Its ready_time came through too: ready 5 min 1 s, then down twice that.
	r2.advance(5 * time.Minute)
	r3 := r2.restart(path, r2.now.Add(time.Second))
	c := r3.player(3, "Cy", street, trap.x, trap.y)
	r3.drain(c)
	r3.act(c, 1, id, "trigger", nil)
	if got := objOf(r3.only(c, "obj_ok"), id); num(got["in"]) != 603250 {
		t.Fatalf("a trap ready 5 min across a restart stays down %v", got)
	}
}
