package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// One scheduled Rook a day in the whole world (rook.go header, Austin 2026-11-04).

func TestRookOneScheduledAttackADayAcrossHubs(t *testing.T) {
	r := newRookRig(t, false)
	_, there := rookOtherHub(r)
	r.roll = 0 // the shortest wait, and the first street in order of the two
	r.due()
	a := r.player(1, "Ada") // this hub
	b := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.walk(b, there[0]) // another hub
	r.advance(r.delay())
	first := rookAttacks(r)
	if len(first) != 1 {
		t.Fatalf("the day's one Rook: %v", first)
	}
	started := r.now
	// Neither the other hub nor this one gets another for a day, however Ada and Bo walk about.
	_, here := rookTestHub(r)
	for i := 0; r.now.Before(started.Add(rookDaily - time.Hour)); i++ {
		r.advance(time.Hour)
		r.walk(a, here[i%len(here)])
		r.walk(b, there[(i+1)%len(there)])
		for _, tsid := range rookAttacks(r) {
			if tsid != first[0] {
				t.Fatalf("%v after the day's Rook started, another on %s", r.now.Sub(started), tsid)
			}
		}
	}
	r.advance(started.Add(rookDaily).Sub(r.now))
	if got := rookAttacks(r); len(got) != 0 {
		t.Fatalf("not the moment the day is up: %v", got)
	}
	r.advance(rookDelayMax)
	if got := rookAttacks(r); len(got) != 1 || (got[0] != a.Street() && got[0] != b.Street()) {
		t.Fatalf("a day on, one Rook on Ada's %s or Bo's %s: %v", a.Street(), b.Street(), got)
	}
	if !r.mod.next.After(started.Add(rookDaily)) {
		t.Fatalf("and the next day counts from that one: %v", r.mod.next.Sub(started))
	}
}

func TestRookComesAFewMinutesAfterSomeoneSettlesOnAStreet(t *testing.T) {
	r := newRookRig(t, false)
	r.due()
	if r.mod.timer != nil {
		t.Fatal("with nobody out, the world waits for an arrival")
	}
	a := r.player(1, "Ada")
	if r.mod.timer == nil {
		t.Fatal("an arrival arms the few minutes")
	}
	// A minute before the Rook would come, Ada walks onto another street of the hub: it is too
	// soon there, so it waits its next try.
	_, here := rookTestHub(r)
	r.advance(r.delay() - time.Minute)
	r.walk(a, here[0])
	r.advance(time.Minute)
	if got := rookAttacks(r); len(got) != 0 {
		t.Fatalf("not within %v of an arrival: %v", rookSettle, got)
	}
	if r.mod.timer == nil {
		t.Fatal("the world tries again")
	}
	r.advance(r.delay())
	if got := rookAttacks(r); len(got) != 1 || got[0] != here[0] {
		t.Fatalf("the next try finds Ada on %s: %v", here[0], got)
	}
	// Nobody out when a try comes: no Rook and nothing armed until somebody arrives.
	r2 := newRookRig(t, false)
	r2.due()
	b := r2.player(2, "Bo")
	r2.hub.Disconnect(b)
	r2.advance(rookDelayMax)
	if got := rookAttacks(r2); len(got) != 0 || r2.mod.timer != nil {
		t.Fatalf("nobody out: %v (timer %v)", got, r2.mod.timer != nil)
	}
	r2.player(3, "Cy")
	r2.advance(r2.delay())
	if got := rookAttacks(r2); len(got) != 1 {
		t.Fatalf("Cy's arrival brings it a few minutes on: %v", got)
	}
}

func TestRookDevAndAdminLeaveTheDailyClockAlone(t *testing.T) {
	r := newRookRig(t, true)
	next := r.mod.next
	if !next.Equal(r.now.Add(rookDaily)) {
		t.Fatalf("a new server waits a day: %v", next.Sub(r.now))
	}
	// A dev start needs no day and moves none.
	a := r.player(1, "Ada")
	r.send(a, map[string]any{"t": "rook_dev", "do": "start"})
	if got := rookAttacks(r); len(got) != 1 || !r.mod.next.Equal(next) {
		t.Fatalf("a dev start before the day is up: %v (next %v)", got, r.mod.next.Sub(next))
	}
	r.advance(rookHerald)
	if code, _ := rookCall(t, r, "rook_end", `{"street":"`+rookTestStreet+`"}`); code != 200 {
		t.Fatalf("end: %d", code)
	}
	r.advance(rookDepartClear)
	if got := rookAttacks(r); len(got) != 0 || !r.mod.next.Equal(next) {
		t.Fatalf("ended: %v (next %v)", got, r.mod.next.Sub(next))
	}
	// With the day up, a dev start does not use it: the day's Rook still comes, to Bo's street.
	_, there := rookOtherHub(r)
	b := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.walk(b, there[0])
	r.walk(a, there[1])
	r.due()
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	if !r.mod.next.Equal(r.now) {
		t.Fatal("a dev attack moves no clock")
	}
	r.advance(r.delay())
	s := r.mod.streets[there[0]]
	if s == nil || s.attack == nil || !s.attack.rests {
		t.Fatalf("the day's Rook on Bo's street: %v", rookAttacks(r))
	}
	// The console's End attack on the day's Rook does not give the day back.
	day := r.mod.next
	r.advance(rookHerald)
	rookCall(t, r, "rook_end", `{"street":"`+there[0]+`"}`)
	r.advance(rookDepartClear)
	if !r.mod.next.Equal(day) {
		t.Fatalf("an ended Rook keeps its day: %v", r.mod.next.Sub(day))
	}
}

func TestRookDailyClockSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rook.json")
	readNext := func() int64 {
		var f rookFile
		raw, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(raw, &f) != nil {
			t.Fatalf("state file: %s (%v)", raw, err)
		}
		return f.Next
	}
	// First run without a file: the day counts from now and is written at once.
	r, _ := newRookTreeRigAt(t, dir, time.Time{})
	r.mod.dev = false
	if !r.mod.next.Equal(r.now.Add(rookDaily)) || readNext() != msOf(r.now.Add(rookDaily)) {
		t.Fatalf("first run: next %v, file %d", r.mod.next.Sub(r.now), readNext())
	}
	// The day's Rook starts; its next day is in the file.
	r.due()
	r.player(1, "Ada")
	r.advance(r.delay())
	if got := rookAttacks(r); len(got) != 1 {
		t.Fatalf("the day's Rook: %v", got)
	}
	day := r.mod.next
	if !day.Equal(r.now.Add(rookDaily)) || readNext() != msOf(day) {
		t.Fatalf("after the start: next %v, file %d", day.Sub(r.now), readNext())
	}
	r.hub.Shutdown()
	// A restart (a deploy) two hours on keeps the day; Bo, fresh, gets no Rook before it is up.
	r2, _ := newRookTreeRigAt(t, dir, r.now.Add(2*time.Hour))
	r2.mod.dev = false
	if !r2.mod.next.Equal(day) {
		t.Fatalf("after a restart: %v", r2.mod.next.Sub(day))
	}
	r2.player(2, "Bo")
	r2.advance(day.Add(-time.Second).Sub(r2.now))
	if got := rookAttacks(r2); len(got) != 0 {
		t.Fatalf("restarted, no Rook before the day is up: %v", got)
	}
	r2.advance(time.Second + rookDelayMax)
	if got := rookAttacks(r2); len(got) != 1 {
		t.Fatalf("a day on, Bo's Rook: %v", got)
	}
}

func TestRookOldStateFileWaitsADayFromItsFirstLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rook.json")
	now := time.UnixMilli(gameEpochMs + 1000*gameDayMs + 3_600_000)
	// A file from before the daily rule: no "next".
	raw := `{"v":1,"streets":{},"met":{"7":` + jsonInt(msOf(now.Add(time.Hour))) + `}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ := newRookTreeRigAt(t, dir, now)
	if !r.mod.next.Equal(now.Add(rookDaily)) || !r.mod.met[7].Equal(now.Add(time.Hour)) {
		t.Fatalf("an old file loads, the day from now: next %v, met %v", r.mod.next.Sub(now), r.mod.met)
	}
	// A restart later that day does not push it on: the first load wrote it.
	r2, _ := newRookTreeRigAt(t, dir, now.Add(23*time.Hour))
	if !r2.mod.next.Equal(now.Add(rookDaily)) {
		t.Fatalf("the second load keeps the first's day: %v", r2.mod.next.Sub(now))
	}
	// A time more than a day away is a bad clock: a day from the load.
	f := rookFile{V: 1, Streets: map[string]*rookRec{}, Next: msOf(now.Add(10 * rookDaily))}
	b, _ := json.Marshal(f)
	os.WriteFile(path, b, 0o600)
	r3, _ := newRookTreeRigAt(t, dir, now)
	if !r3.mod.next.Equal(now.Add(rookDaily)) {
		t.Fatalf("a far-off day: %v", r3.mod.next.Sub(now))
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }
