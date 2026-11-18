package main

import (
	"path/filepath"
	"testing"
	"time"
)

// G982: Pot Luck Day's window is the game's (js/seasons.js potLuckWindow).
func TestPotLuckDayRunsFromTheSundayBeforeThanksgivingToTheSundayAfter(t *testing.T) {
	seasonDateForTest = ""
	pt := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, urZone) }
	for _, c := range []struct {
		at   time.Time
		want bool
	}{
		{pt(2026, 11, 21, 23), false}, {pt(2026, 11, 22, 0), true}, {pt(2026, 11, 26, 12), true}, {pt(2026, 11, 29, 23), true},
		{pt(2026, 11, 30, 0), false}, {pt(2027, 11, 21, 9), true}, {pt(2027, 11, 28, 22), true}, {pt(2027, 11, 29, 1), false},
		{pt(2026, 12, 25, 12), false}, {pt(2026, 11, 22, 0).Add(-time.Minute), false},
	} {
		if got := potLuckOn(c.at); got != c.want {
			t.Errorf("potLuckOn(%v) = %v", c.at, got)
		}
	}
	if left := potLuckLeft(pt(2026, 11, 29, 23)); left != time.Hour {
		t.Errorf("an hour before the end leaves %v", left)
	}
	seasonDateForTest = "2026-11-26"
	defer func() { seasonDateForTest = "" }()
	if !potLuckOn(pt(2026, 6, 1, 12)) || potLuckLeft(pt(2026, 6, 1, 12)) < 300*24*time.Hour {
		t.Error("a held QA date is Pot Luck Day and never ends")
	}
}

// G982: a costume on a street Chicken is everyone's; the ledger takes it; the refusals.
func TestAChickenWearsOneCostumeForEveryone(t *testing.T) {
	seasonDateForTest = "2026-11-26"
	defer func() { seasonDateForTest = "" }()
	r, l := usesRig(t)
	ch := sharedWorld.streets[defaultStreet]["chicken:1"]
	a := r.player(1, "Ada", defaultStreet, ch.x, ch.y)
	b := r.player(2, "Bo", defaultStreet, ch.x, ch.y)

	// Nothing to wear: the ledger's refusal, and the Chicken stays as it was.
	r.act(a, 1, "chicken:1", "dress", map[string]any{"c": "turkey_tailfan"})
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "no_item" || objOf(no, "chicken:1")["cos"] != nil {
		t.Fatalf("a costume Ada lacks answered %v", no)
	}
	// Not a costume: refused before the ledger is asked.
	l.give(1, "chicken_egg", 1)
	r.act(a, 2, "chicken:1", "dress", map[string]any{"c": "chicken_egg"})
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "not_costume" || no["message"] != costumeNotOne || l.held[1]["chicken_egg"] != 1 {
		t.Fatalf("an egg as a costume answered %v", no)
	}
	l.give(1, "turkey_tailfan", 1)
	l.give(2, "rooster_plume", 1)
	r.drain(b)
	r.act(a, 3, "chicken:1", "dress", map[string]any{"c": "turkey_tailfan"})
	r.advance(0)
	ok := r.only(a, "obj_ok")
	if objOf(ok, "chicken:1")["cos"] != "turkey_tailfan" || ok["use"] == nil || l.held[1]["turkey_tailfan"] != 0 {
		t.Fatalf("Ada's Turkey Tailfan answered %v (left %d)", ok, l.held[1]["turkey_tailfan"])
	}
	// Bo sees it go on: its state and the dress itself.
	var seen, ev bool
	for _, m := range r.drain(b) {
		seen = seen || (m["t"] == "objs" && objOf(m, "chicken:1")["cos"] == "turkey_tailfan")
		ev = ev || (m["t"] == "obj_ev" && m["ev"] == "dress" && m["c"] == "turkey_tailfan" && num(m["by"]) == 1)
	}
	if !seen || !ev {
		t.Fatalf("Bo saw the costume %v, the dress %v", seen, ev)
	}
	// Dressed already: it keeps its costume, and Bo keeps his plume.
	r.act(b, 1, "chicken:1", "dress", map[string]any{"c": "rooster_plume"})
	r.advance(0)
	if no := r.only(b, "obj_no"); no["code"] != "dressed" || no["message"] != costumeAlready || l.held[2]["rooster_plume"] != 1 {
		t.Fatalf("a second costume answered %v", no)
	}
	// Another Chicken takes a plume.
	ch0 := sharedWorld.streets[defaultStreet]["chicken:0"]
	d := r.player(4, "Di", defaultStreet, ch0.x, ch0.y)
	l.give(4, "rooster_plume", 1)
	r.act(d, 1, "chicken:0", "dress", map[string]any{"c": "rooster_plume"})
	r.advance(0)
	if ok := r.only(d, "obj_ok"); objOf(ok, "chicken:0")["cos"] != "rooster_plume" {
		t.Fatalf("Di's plume answered %v", ok)
	}
	// A snapshot shows both.
	if syncOf(r, a, "chicken:1")["cos"] != "turkey_tailfan" || syncOf(r, a, "chicken:0")["cos"] != "rooster_plume" {
		t.Fatal("the snapshot does not show the costumes")
	}
	// Only a Chicken: dress is no Butterfly's verb.
	bf := sharedWorld.streets[defaultStreet]["butterfly:0"]
	if bf != nil {
		c := r.player(3, "Cy", defaultStreet, bf.x, bf.y)
		r.act(c, 1, "butterfly:0", "dress", map[string]any{"c": "turkey_tailfan"})
		r.advance(0)
		if no := r.only(c, "obj_no"); no["code"] != "bad_verb" {
			t.Fatalf("a dressed Butterfly answered %v", no)
		}
	}
}

// G982: out of season nobody dresses a Chicken, and at the season's end every costume falls off for everyone.
func TestCostumesFallOffWhenPotLuckDayEnds(t *testing.T) {
	seasonDateForTest = ""
	r, l := usesRig(t)
	ch := sharedWorld.streets[defaultStreet]["chicken:1"]
	r.now = time.Date(2026, 11, 20, 12, 0, 0, 0, urZone)
	a := r.player(1, "Ada", defaultStreet, ch.x, ch.y)
	b := r.player(2, "Bo", defaultStreet, ch.x, ch.y)
	l.give(1, "turkey_tailfan", 2)
	r.act(a, 1, "chicken:1", "dress", map[string]any{"c": "turkey_tailfan"})
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "no_season" || no["message"] != costumeNoSeason || l.held[1]["turkey_tailfan"] != 2 {
		t.Fatalf("a costume before the season answered %v", no)
	}
	r.now = time.Date(2026, 11, 29, 23, 0, 0, 0, urZone)
	r.act(a, 2, "chicken:1", "dress", map[string]any{"c": "turkey_tailfan"})
	r.advance(0)
	r.only(a, "obj_ok")
	r.drain(b)
	r.advance(59 * time.Minute)
	if r.mod.streets[defaultStreet]["chicken:1"].costume != "turkey_tailfan" {
		t.Fatal("the costume fell off before the season ended")
	}
	r.advance(2 * time.Minute)
	var off bool
	for _, m := range r.frames(b, "objs") {
		o := objOf(m, "chicken:1")
		off = off || (o != nil && o["cos"] == nil)
	}
	if !off || syncOf(r, b, "chicken:1")["cos"] != nil {
		t.Fatal("the costume did not fall off for everyone at the season's end")
	}
}

// G982: a costume outlives a restart in the season, and one read after the season is dropped.
func TestACostumeOutlivesARestartButNotTheSeason(t *testing.T) {
	seasonDateForTest = ""
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Date(2026, 11, 25, 9, 0, 0, 0, urZone))
	ch := sharedWorld.streets[defaultStreet]["chicken:2"]
	a := r.player(1, "Ada", defaultStreet, ch.x, ch.y)
	r.act(a, 1, "chicken:2", "dress", map[string]any{"c": "rooster_plume"})
	r.advance(0)
	r.only(a, "obj_ok")
	r.mod.save()
	r2 := r.restart(path, r.now.Add(time.Hour))
	a2 := r2.player(1, "Ada", defaultStreet, ch.x, ch.y)
	if syncOf(r2, a2, "chicken:2")["cos"] != "rooster_plume" {
		t.Fatal("the plume was lost in a restart")
	}
	r2.mod.save()
	r3 := r2.restart(path, time.Date(2026, 12, 2, 9, 0, 0, 0, urZone))
	a3 := r3.player(1, "Ada", defaultStreet, ch.x, ch.y)
	if c := syncOf(r3, a3, "chicken:2"); c != nil && c["cos"] != nil {
		t.Fatalf("a costume read after the season reads %v", c)
	}
}
