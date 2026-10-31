package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// A player who has met a Rook rests rookPlayerRest, in any hub (rook.go header, Austin 2026-10-31).

// rookTestHub is the test street's hub and its other streets, sorted.
func rookTestHub(r *rookRig) (string, []string) {
	hub := r.mod.w.streets[rookTestStreet].hub
	var others []string
	for _, tsid := range r.mod.w.hubs[hub] {
		if tsid != rookTestStreet {
			others = append(others, tsid)
		}
	}
	return hub, others
}

// rookOtherHub is another hub than the test street's, and its streets.
func rookOtherHub(r *rookRig) (string, []string) {
	mine := r.mod.w.streets[rookTestStreet].hub
	var hubs []string
	for hub := range r.mod.w.hubs {
		if hub != mine {
			hubs = append(hubs, hub)
		}
	}
	sort.Strings(hubs)
	return hubs[0], r.mod.w.hubs[hubs[0]]
}

func (r *rookRig) walk(cl *Client, tsid string) {
	r.send(cl, map[string]any{"t": "join", "street": tsid, "x": 100.0, "y": 100.0})
	r.drain(cl)
}

// rookAttacks lists the streets with an attack on.
func rookAttacks(r *rookRig) []string {
	var out []string
	for tsid, s := range r.mod.streets {
		if s.attack != nil {
			out = append(out, tsid)
		}
	}
	sort.Strings(out)
	return out
}

func TestRookLonePlayerRestsSixHoursAfterMeetingOne(t *testing.T) {
	r := newRookRig(t, false)
	a := r.player(1, "Ada")
	r.advance(rookScheduleMin)
	if got := rookAttacks(r); len(got) != 1 || got[0] != rookTestStreet {
		t.Fatalf("Ada alone meets the hour's Rook: %v", got)
	}
	met := r.now
	if until := r.mod.met[1]; !until.Equal(met.Add(rookPlayerRest)) {
		t.Fatalf("Ada rests from %v: until %v", met, until)
	}
	// The Rook flies off unfought; Ada walks the streets of this hub and another one for six hours.
	r.advance(rookHerald + rookMaxWingBeats*rookWingBeat + rookDepartClear)
	if got := rookAttacks(r); len(got) != 0 {
		t.Fatalf("the Rook left: %v", got)
	}
	_, here := rookTestHub(r)
	_, there := rookOtherHub(r)
	streets := []string{here[0], there[0], here[1], there[1], rookTestStreet, there[2], here[2]}
	for i := 0; r.now.Before(met.Add(rookPlayerRest - 30*time.Minute)); i++ {
		r.walk(a, streets[i%len(streets)])
		r.advance(30 * time.Minute)
		if got := rookAttacks(r); len(got) != 0 {
			t.Fatalf("%v after meeting a Rook, Ada on %s met another: %v", r.now.Sub(met), a.Street(), got)
		}
	}
	r.walk(a, here[3])
	r.advance(met.Add(rookPlayerRest - time.Second).Sub(r.now))
	if got := rookAttacks(r); len(got) != 0 {
		t.Fatalf("still resting a second before six hours: %v", got)
	}
	// Rested, the hub's next turn finds her again.
	r.advance(rookScheduleMax + time.Second)
	if got := rookAttacks(r); len(got) != 1 || got[0] != here[3] {
		t.Fatalf("after six hours the Rook comes to Ada's street %s: %v", here[3], got)
	}
}

func TestRookFreshPlayerIsPickedWhileAnotherRests(t *testing.T) {
	r := newRookRig(t, false)
	hub, others := rookTestHub(r)
	rested, fresh := others[0], others[len(others)-1]
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.walk(a, rested)
	r.walk(b, fresh)
	r.mod.met[1] = r.now.Add(rookPlayerRest)
	r.roll = 0 // the first candidate: Ada's street, were it one
	r.mod.hubs[hub].timer.stop()
	r.mod.hubs[hub].timer = nil
	r.mod.hubFires(hub)
	if got := rookAttacks(r); len(got) != 1 || got[0] != fresh {
		t.Fatalf("Bo's street %s, not resting Ada's, gets the Rook: %v", fresh, got)
	}
	if until := r.mod.met[2]; !until.Equal(r.now.Add(rookPlayerRest)) {
		t.Fatalf("Bo met it: %v", until)
	}
}

func TestRookAllRestingHubWaitsItsTurn(t *testing.T) {
	r := newRookRig(t, false)
	hub, _ := rookTestHub(r)
	r.player(1, "Ada")
	r.player(2, "Bo")
	r.mod.met[1] = r.now.Add(rookPlayerRest)
	r.mod.met[2] = r.now.Add(rookPlayerRest)
	r.advance(rookScheduleMin)
	if got := rookAttacks(r); len(got) != 0 {
		t.Fatalf("a street of resting players is not picked: %v", got)
	}
	if r.mod.hubs[hub].timer == nil {
		t.Fatal("the hub waits its next turn")
	}
}

func TestRookMixedStreetIsPickedAndBothMeetIt(t *testing.T) {
	r := newRookRig(t, false)
	r.player(1, "Ada")
	r.player(2, "Bo")
	r.advance(rookScheduleMin - time.Second)
	r.mod.met[1] = r.now.Add(time.Hour) // Ada is still resting when the hour comes; Bo is not
	r.advance(time.Second)
	if got := rookAttacks(r); len(got) != 1 || got[0] != rookTestStreet {
		t.Fatalf("one fresh player is enough: %v", got)
	}
	for _, id := range []int64{1, 2} {
		if until := r.mod.met[id]; !until.Equal(r.now.Add(rookPlayerRest)) {
			t.Fatalf("player %d met it and rests six hours from now: %v", id, until)
		}
	}
}

func TestRookArrivingWhileItIsOutIsMeetingIt(t *testing.T) {
	r := newRookRig(t, false)
	r.player(1, "Ada")
	r.advance(rookScheduleMin)
	if got := rookAttacks(r); len(got) != 1 {
		t.Fatalf("Ada's Rook: %v", got)
	}
	r.advance(rookHerald + time.Minute) // the Rook is over the street
	r.player(3, "Cy")
	if until := r.mod.met[3]; !until.Equal(r.now.Add(rookPlayerRest)) {
		t.Fatalf("Cy saw the Rook: %v", until)
	}
}

func TestRookDevStartIgnoresTheRest(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	r.mod.met[1] = r.now.Add(rookPlayerRest)
	r.send(a, map[string]any{"t": "rook_dev", "do": "start"})
	if got := rookAttacks(r); len(got) != 1 {
		t.Fatalf("a dev start on a resting player's street: %v", got)
	}
	r.player(2, "Bo")
	if _, ok := r.mod.met[2]; ok {
		t.Fatal("a dev Rook gives no rest")
	}
	if !r.mod.met[1].Equal(r.now.Add(rookPlayerRest)) {
		t.Fatalf("nor changes one: %v", r.mod.met[1])
	}
}

func TestRookPlayerRestSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	r, _ := newRookTreeRigAt(t, dir, time.Time{})
	r.player(1, "Ada")
	r.mod.meet(r.hub.ClientLocked(1))
	until := r.mod.met[1]
	// One set without a save is written when the server stops.
	r.mod.met[9] = r.now.Add(time.Hour)
	r.hub.Shutdown()
	r2, _ := newRookTreeRigAt(t, dir, r.now.Add(time.Hour-time.Minute))
	if !r2.mod.met[1].Equal(until) || !r2.mod.met[9].Equal(r.now.Add(time.Hour)) {
		t.Fatalf("after a restart: %v", r2.mod.met)
	}
	// Reconnected, Ada is still resting: alone in the hub, the hour brings no Rook.
	r2.mod.dev = false
	r2.player(1, "Ada")
	r2.advance(rookScheduleMin)
	if got := rookAttacks(r2); len(got) != 0 {
		t.Fatalf("Ada rests across the restart: %v", got)
	}
}

func TestRookExpiredRestsArePruned(t *testing.T) {
	dir := t.TempDir()
	r, _ := newRookTreeRigAt(t, dir, time.Time{})
	now := r.now
	f := rookFile{V: 1, Streets: map[string]*rookRec{}, Met: map[int64]int64{
		1: msOf(now.Add(time.Hour)), 2: msOf(now.Add(-time.Minute)), -3: msOf(now.Add(time.Hour)),
	}}
	b, _ := json.Marshal(f)
	if err := os.WriteFile(filepath.Join(dir, "rook.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	r2, _ := newRookTreeRigAt(t, dir, now)
	if len(r2.mod.met) != 1 || !r2.mod.met[1].Equal(now.Add(time.Hour)) {
		t.Fatalf("on load only Ada's rest is left: %v", r2.mod.met)
	}
	// Past Ada's rest, a save drops it from memory and the file.
	r2.now = now.Add(time.Hour)
	r2.mod.save()
	if len(r2.mod.met) != 0 {
		t.Fatalf("memory after the save: %v", r2.mod.met)
	}
	var got rookFile
	raw, _ := os.ReadFile(filepath.Join(dir, "rook.json"))
	if err := json.Unmarshal(raw, &got); err != nil || len(got.Met) != 0 {
		t.Fatalf("file after the save: %s (%v)", raw, err)
	}
}
