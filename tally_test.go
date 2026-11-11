package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tallyAt adds a tally under the hub lock, as the modules do.
func tallyAt(h *Hub, key string, n int64) {
	h.mu.Lock()
	h.tallyLocked(key, n)
	h.mu.Unlock()
}

func TestTalliesRideThePlayTimeBatchesOnceByID(t *testing.T) {
	site := &fakePlaySite{}
	r := newPlayRig(t, "", site, false)
	day := r.now.UTC().Format("2006-01-02")
	cl := r.game(7)
	tallyAt(r.hub, "pig:nibble", 1)
	tallyAt(r.hub, "pig:nibble", 2)
	tallyAt(r.hub, "rock:mine", 1)
	tallyAt(r.hub, "Not A Key", 1)
	tallyAt(r.hub, "rock:mine", 0)
	r.advance(time.Minute)
	site.lose = true // taken, and the answer lost: the same id again carries the same counts
	r.pt.flush()
	r.pt.flush()
	if len(site.posts) != 2 || site.posts[0].ID != site.posts[1].ID {
		t.Fatalf("posts %+v", site.posts)
	}
	want := map[string]int64{"pig:nibble": 3, "rock:mine": 1}
	if got := site.posts[1].Counts[day]; len(got) != 2 || got["pig:nibble"] != 3 || got["rock:mine"] != 1 {
		t.Fatalf("counts %v, want %v", site.posts[1].Counts, want)
	}
	if len(r.pt.tallied()) != 0 {
		t.Fatalf("taken, still waiting: %v", r.pt.tallied())
	}

	// A tally with nobody's seconds to send still goes, with an empty players list (never null).
	r.hub.Disconnect(cl)
	r.pt.flush()
	tallyAt(r.hub, "quoin:xp", 1)
	n := len(site.posts)
	r.pt.flush()
	if len(site.posts) != n+1 || site.posts[n].Counts[day]["quoin:xp"] != 1 {
		t.Fatalf("a counts-only batch: %+v", site.posts[n:])
	}
	b, _ := json.Marshal(site.posts[n])
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil || string(raw["players"]) != "[]" {
		t.Fatalf("players in a counts-only batch: %s", b)
	}
	// A batch without tallies says nothing about them.
	b, _ = json.Marshal(playBatch{ID: "pt-x-1", Players: []playEntry{}})
	if string(b) != `{"id":"pt-x-1","players":[]}` {
		t.Fatalf("no counts: %s", b)
	}
}

func TestTalliesKeepADaysKeysBounded(t *testing.T) {
	c := playCounts{}
	for i := 0; i < tallyDayKeys+10; i++ {
		c.addOne("2026-11-11", "k:"+string(rune('a'+i%26))+string(rune('a'+i/26)), 1)
	}
	c.addOne("2026-11-11", "k:aa", 5)
	if len(c["2026-11-11"]) != tallyDayKeys || c["2026-11-11"]["k:aa"] != 6 {
		t.Fatalf("%d keys, k:aa %d", len(c["2026-11-11"]), c["2026-11-11"]["k:aa"])
	}
}

func TestTalliesWaitAcrossARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "realtime-play-time.json")
	site := &fakePlaySite{down: true}
	r := newPlayRig(t, file, site, false)
	day := r.now.UTC().Format("2006-01-02")
	r.game(7)
	tallyAt(r.hub, "tree:harvest", 4)
	r.advance(time.Minute)
	r.pt.flush() // in flight, the site down
	tallyAt(r.hub, "tree:harvest", 1)
	r.hub.Shutdown()
	var f playFile
	b, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(b, &f) != nil || f.Sent == nil || f.Sent.Counts[day]["tree:harvest"] != 4 || f.Counts[day]["tree:harvest"] != 1 {
		t.Fatalf("state file %s", b)
	}
	site.down = false
	r2 := newPlayRig(t, file, site, false)
	if got := r2.pt.tallied()[day]["tree:harvest"]; got != 5 {
		t.Fatalf("after the restart %d, want 5", got)
	}
	if !r2.pt.flush() {
		t.Fatal("flush failed")
	}
	sum := int64(0)
	ids := map[string]bool{}
	for _, p := range site.posts {
		if !ids[p.ID] {
			sum += p.Counts[day]["tree:harvest"]
		}
		ids[p.ID] = true
	}
	if sum != 5 || site.posts[0].ID != f.Sent.ID {
		t.Fatalf("the site took %d (want 5), first batch %s want %s", sum, site.posts[0].ID, f.Sent.ID)
	}
}

func TestSharedActsAreTalliedWhenTheyLand(t *testing.T) {
	r := newObjRig(t)
	got := map[string]int64{}
	r.hub.provided["playtime.count"] = func(k string, n int64) { got[k] += n }
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	r.drain(a)
	r.mod.roll = func() float64 { return 0.99 } // a pet that misses: no tally
	r.act(a, 1, "pig:2", "pet", nil)
	if ok := r.only(a, "obj_ok"); ok["success"] != false {
		t.Fatalf("a missed pet answered %v", ok)
	}
	r.mod.roll = func() float64 { return 0.5 }
	r.act(a, 2, "pig:2", "pet", nil)
	r.only(a, "obj_ok")
	r.act(a, 3, "pig:2", "nibble", map[string]any{"skill": "animalkinship_2"})
	r.only(a, "obj_ok")
	r.now = r.now.Add(time.Second)
	r.act(a, 4, "pig:2", "nibble", map[string]any{"skill": "animalkinship_2"}) // the daily limit: refused, no tally
	r.only(a, "obj_no")
	if len(got) != 2 || got["pig:pet"] != 1 || got["pig:nibble"] != 1 {
		t.Fatalf("tallies %v", got)
	}
	if k := objTallyKey(&objSpot{kind: "node", class: "peat_1"}, "harvest"); k != "peat_1:harvest" || !tallyKeyRe.MatchString(k) {
		t.Fatalf("a node's key %q", k)
	}
	if k := objTallyKey(&objSpot{kind: "rock", class: "rock_beryl_1"}, "mine"); k != "rock:mine" {
		t.Fatalf("a rock's key %q", k)
	}
}
