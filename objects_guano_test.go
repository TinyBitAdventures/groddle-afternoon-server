package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// guanoStreet is the first street (by tsid) with exactly n Batterflies, and the first of them.
func guanoStreet(t *testing.T, n int) (string, *objSpot) {
	t.Helper()
	street := ""
	for tsid, spots := range sharedWorld.streets {
		count := 0
		for _, s := range spots {
			if s.kind == "batterfly" {
				count++
			}
		}
		if count == n && (street == "" || tsid < street) {
			street = tsid
		}
	}
	if street == "" {
		t.Fatalf("no street lays out %d Batterflies", n)
	}
	return street, sharedWorld.streets[street]["batterfly:0"]
}

// guanoOf is the Guano under each Batterfly of a snapshot or objs frame, and their sum.
func guanoOf(msg map[string]any) (map[string]int, int) {
	piles, total := map[string]int{}, 0
	list, _ := msg["objs"].([]any)
	for _, raw := range list {
		o, _ := raw.(map[string]any)
		id, _ := o["id"].(string)
		if strings.HasPrefix(id, "batterfly:") && num(o["guano"]) > 0 {
			piles[id] = int(num(o["guano"]))
			total += piles[id]
		}
	}
	return piles, total
}

func guanoSync(r *objRig, cl *Client) map[string]any {
	r.t.Helper()
	r.drain(cl)
	r.now = r.now.Add(2 * time.Second) // past the sync limit's burst
	r.send(cl, map[string]any{"t": "obj_sync"})
	return r.only(cl, "objs")
}

func TestEachBatterflyTicksAtItsOwnMinuteOfTheHour(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 6; i++ {
		p := guanoPhase("GA9114MS8B32FB2", fmt.Sprintf("batterfly:%d", i))
		if p < time.Second || p > guanoEvery || p%time.Second != 0 {
			t.Fatalf("batterfly:%d ticks %v into the hour", i, p)
		}
		seen[p] = true
	}
	if len(seen) < 5 {
		t.Fatalf("the six clocks tick together: %v", seen)
	}
	if guanoPhase("GA9114MS8B32FB2", "batterfly:0") != guanoPhase("GA9114MS8B32FB2", "batterfly:0") {
		t.Fatal("a Batterfly's minute moved")
	}
}

func TestGuanoGrowsByTheClockOnAnEmptyStreetAndEveryoneSeesTheSamePile(t *testing.T) {
	r := newObjRig(t)
	got := r.credited()
	street, spot := guanoStreet(t, 2)
	start := r.now
	// Nobody is on the street for three hours: no timer runs for it.
	r.advance(3 * time.Hour)
	if len(r.timers) != 0 || len(r.mod.streets[street]) != 0 {
		t.Fatalf("an empty street cost %d timers and %d objects", len(r.timers), len(r.mod.streets[street]))
	}
	a := r.player(1, "Ada", street, spot.x, spot.y)
	snap := guanoSync(r, a)
	piles, total := guanoOf(snap)
	if num(snap["guano"]) != float64(start.UnixMilli()) || total != 6 || piles["batterfly:0"] != 3 || piles["batterfly:1"] != 3 {
		t.Fatalf("Ada arrives to %v (since %v)", piles, snap["guano"])
	}
	b := r.player(2, "Bo", street, spot.x+60, spot.y)
	if p, n := guanoOf(guanoSync(r, b)); n != 6 || p["batterfly:0"] != 3 {
		t.Fatalf("Bo sees %v", p)
	}
	r.drain(a)
	// Ada picks up the pile under the first Batterfly: hers, credited, and gone for Bo too.
	r.act(a, 1, "batterfly:0", "pickup", nil)
	ok := r.only(a, "obj_ok")
	if num(ok["take"]) != 3 || num(ok["before"].(map[string]any)["guano"]) != 3 || objOf(ok, "batterfly:0")["guano"] != nil || strings.Join(*got, ",") != "1:take:guanox3" {
		t.Fatalf("Ada's pickup answered %v, credited %v", ok, *got)
	}
	heard := r.only(b, "objs")
	if o := objOf(heard, "batterfly:0"); o == nil || o["guano"] != nil {
		t.Fatalf("Bo heard %v", heard)
	}
	// Bo was a moment late for that pile, and takes the other.
	r.act(b, 1, "batterfly:0", "pickup", nil)
	if no := r.only(b, "obj_no"); no["code"] != "gone" || no["message"] != guanoGone {
		t.Fatalf("Bo's late pickup answered %v", no)
	}
	// Bo walks to the second Batterfly (wherever the planner lays it) for its pile.
	second := sharedWorld.streets[street]["batterfly:1"]
	r.send(b, map[string]any{"t": "move", "x": second.x, "y": second.y})
	r.drain(b)
	r.act(b, 2, "batterfly:1", "pickup", nil)
	if ok := r.only(b, "obj_ok"); num(ok["take"]) != 3 || strings.Join(*got, ",") != "1:take:guanox3,2:take:guanox3" {
		t.Fatalf("Bo's pickup answered %v, credited %v", ok, *got)
	}
	// With both standing there the next hour's two Guano arrive in front of them, each at its minute.
	r.drain(a)
	r.drain(b)
	r.advance(time.Hour)
	for _, cl := range []*Client{a, b} {
		last := map[string]int{}
		for _, m := range r.drain(cl) {
			if m["t"] == "objs" {
				p, _ := guanoOf(m)
				for id, n := range p {
					last[id] = n
				}
			}
		}
		if last["batterfly:0"] != 1 || last["batterfly:1"] != 1 {
			t.Fatalf("after an hour a player standing there saw %v", last)
		}
	}
	// A pile that is not there, something that is no Batterfly, and a player too far away.
	rock := ""
	for id, s := range sharedWorld.streets[street] {
		if s.kind == "rock" && (rock == "" || id < rock) {
			rock = id
		}
	}
	if rock != "" {
		rs := sharedWorld.streets[street][rock]
		c := r.player(3, "Cy", street, rs.x, rs.y)
		r.act(c, 1, rock, "pickup", nil)
		if no := r.only(c, "obj_no"); no["code"] != "bad_verb" {
			t.Fatalf("picking up a rock answered %v", no)
		}
	}
	far := r.player(4, "Di", street, spot.x+pigReachX+500, spot.y)
	r.act(far, 1, "batterfly:0", "pickup", nil)
	if no := r.only(far, "obj_no"); no["code"] != "far" {
		t.Fatalf("a far pickup answered %v", no)
	}
	if len(*got) != 2 {
		t.Fatalf("credited %v", *got)
	}
}

func TestAStreetHoldsTenGuanoAndATickWhileFullIsLost(t *testing.T) {
	r := newObjRig(t)
	got := r.credited()
	street, spot := guanoStreet(t, 4)
	a := r.player(1, "Ada", street, spot.x, spot.y)
	if _, n := guanoOf(guanoSync(r, a)); n != 0 {
		t.Fatalf("a new world starts with %d Guano", n)
	}
	// Four Batterflies: 4, 8, then the street is full at 10 and stays there.
	for _, want := range []int{4, 8, 10, 10, 10} {
		r.advance(time.Hour)
		if _, n := guanoOf(guanoSync(r, a)); n != want {
			t.Fatalf("the street holds %d, want %d", n, want)
		}
	}
	full := 0
	for _, ob := range r.mod.streets[street] {
		full += ob.guano
	}
	if full != guanoCap || r.mod.guanoStop[street] != nil {
		t.Fatalf("a full street holds %d and its timer is %v", full, r.mod.guanoStop[street] != nil)
	}
	// Ada takes one pile. The hours the street stood full made nothing: the next Guano comes at a
	// Batterfly's next tick, not at once.
	piles, _ := guanoOf(guanoSync(r, a))
	took := piles["batterfly:0"]
	r.act(a, 1, "batterfly:0", "pickup", nil)
	if ok := r.only(a, "obj_ok"); int(num(ok["take"])) != took || took < 1 {
		t.Fatalf("Ada's pickup answered %v", ok)
	}
	if _, n := guanoOf(guanoSync(r, a)); n != guanoCap-took {
		t.Fatalf("after the pickup the street holds %d", n)
	}
	if r.mod.guanoStop[street] == nil {
		t.Fatal("no tick is armed once the street has room")
	}
	r.advance(time.Hour)
	if _, n := guanoOf(guanoSync(r, a)); n != min(guanoCap, guanoCap-took+4) {
		t.Fatalf("an hour on the street holds %d", n)
	}
	// A day with nobody there: full, never more.
	r.send(a, map[string]any{"t": "leave"})
	r.advance(24 * time.Hour)
	b := r.player(2, "Bo", street, spot.x, spot.y)
	if _, n := guanoOf(guanoSync(r, b)); n != guanoCap {
		t.Fatalf("after a day the street holds %d", n)
	}
	if strings.Join(*got, ",") != fmt.Sprintf("1:take:guanox%d", took) {
		t.Fatalf("credited %v", *got)
	}
}

func TestFedGuanoGoesToTheFeederNotThePile(t *testing.T) {
	r, l := usesRig(t)
	got := r.credited()
	street, spot := guanoStreet(t, 2)
	a := r.player(1, "Ada", street, spot.x, spot.y)
	b := r.player(2, "Bo", street, spot.x, spot.y)
	l.give(1, "grain", 15)
	r.advance(time.Hour)
	before, n := guanoOf(guanoSync(r, b))
	if n != 2 {
		t.Fatalf("an hour made %v", before)
	}
	r.act(a, 1, "batterfly:0", "feed", map[string]any{"c": "grain", "n": 15})
	r.advance(0)
	ok := r.only(a, "obj_ok")
	if num(ok["guano"]) != 1 || ok["take"] != nil || num(objOf(ok, "batterfly:0")["guano"]) != float64(before["batterfly:0"]) || strings.Join(*got, ",") != "1:take:guanox1" {
		t.Fatalf("Ada's feed answered %v, credited %v", ok, *got)
	}
	if after, n := guanoOf(guanoSync(r, b)); n != 2 || after["batterfly:0"] != before["batterfly:0"] {
		t.Fatalf("the pile after a feed: %v", after)
	}
}

func TestAStateFileFromBeforeStartsEveryPileEmptyAndARestartKeepsThem(t *testing.T) {
	street, spot := guanoStreet(t, 2)
	path := filepath.Join(t.TempDir(), "objects.json")
	// A file as the server before this change wrote it: a Batterfly with food in it, no Guano words.
	old := fmt.Sprintf(`{"v":1,"streets":{%q:{"batterfly:0":{"k":"batterfly","food":7}}}}`, street)
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newObjRigAt(t, path, time.Time{})
	start := r.now
	var f objFile
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &f) != nil || f.GuanoStart != start.UnixMilli() || f.Streets[street]["batterfly:0"].Food != 7 {
		t.Fatalf("the file after the first start: %s (%v)", raw, err)
	}
	a := r.player(1, "Ada", street, spot.x, spot.y)
	snap := guanoSync(r, a)
	if _, n := guanoOf(snap); n != 0 || num(objOf(snap, "batterfly:0")["food"]) != 7 || num(snap["guano"]) != float64(start.UnixMilli()) {
		t.Fatalf("the old file reads as %v", snap)
	}
	r.advance(2 * time.Hour)
	if _, n := guanoOf(guanoSync(r, a)); n != 4 {
		t.Fatalf("two hours made %d", n)
	}
	r.act(a, 1, "batterfly:0", "pickup", nil)
	if ok := r.only(a, "obj_ok"); num(ok["take"]) != 2 {
		t.Fatalf("the pickup answered %v", ok)
	}
	// The server restarts and is down three hours: the pile left is kept, the clocks' start too,
	// and the hours it was down count.
	stopped := r.now
	r = r.restart(path, stopped.Add(3*time.Hour))
	got := r.credited()
	if !r.mod.guanoStart.Equal(start) {
		t.Fatalf("the clocks start at %v, want %v", r.mod.guanoStart, start)
	}
	b := r.player(2, "Bo", street, spot.x, spot.y)
	snap = guanoSync(r, b)
	piles, n := guanoOf(snap)
	if n != 2+6 || piles["batterfly:0"] != 3 || piles["batterfly:1"] != 5 || num(objOf(snap, "batterfly:0")["food"]) != 7 || num(snap["guano"]) != float64(start.UnixMilli()) {
		t.Fatalf("after the restart Bo sees %v", snap)
	}
	if len(*got) != 0 {
		t.Fatalf("a restart credited %v", *got)
	}
	// A file that says more than the street may hold is held to the cap per pile.
	bad := fmt.Sprintf(`{"v":1,"guanoStart":%d,"guanoAt":{%q:%d,"NOSUCHSTREET":5},"streets":{%q:{"batterfly:0":{"k":"batterfly","guano":999}}}}`,
		start.UnixMilli(), street, r.now.UnixMilli(), street)
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	r2 := newObjRigAt(t, path, r.now)
	if ob := r2.mod.streets[street]["batterfly:0"]; ob == nil || ob.guano != guanoCap || len(r2.mod.guanoAt) != 1 {
		t.Fatalf("a bad file read as %+v, settled %v", ob, r2.mod.guanoAt)
	}
}

func TestTheGuanoClockSkipIsForQAOnly(t *testing.T) {
	r := newObjRig(t)
	street, spot := guanoStreet(t, 2)
	a := r.player(1, "Ada", street, spot.x, spot.y)
	guanoSync(r, a)
	r.send(a, map[string]any{"t": "obj_guano_dev", "skip": 2 * 3600_000})
	if _, n := guanoOf(guanoSync(r, a)); n != 0 {
		t.Fatalf("without the flag the skip made %d Guano", n)
	}
	r.mod.dev = true
	r.drain(a)
	r.send(a, map[string]any{"t": "obj_guano_dev", "skip": 2 * 3600_000})
	if _, n := guanoOf(r.only(a, "objs")); n != 4 {
		t.Fatalf("a two hour skip told the street of %d Guano", n)
	}
	if _, n := guanoOf(guanoSync(r, a)); n != 4 {
		t.Fatalf("after the skip the street holds %d", n)
	}
}
