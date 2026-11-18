package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const feastStreet = "G" + feastStreetKey

// feastRig is a hub with only the Harvest Table, a clock the test moves and fake timers.
type feastRig struct {
	t      *testing.T
	hub    *Hub
	mod    *harvestTable
	now    time.Time
	timers []*toyTimer
}

// Thanksgiving 2026 is November 26; Pot Luck Day runs November 22 to 29 on Pacific time.
var feastNoon = time.Date(2026, time.November, 26, 12, 0, 0, 0, urZone)

func newFeastRig(t *testing.T, at time.Time, path string) *feastRig {
	t.Helper()
	r := &feastRig{t: t, now: at}
	savedMods, savedPath, savedDate := modules, feastFileForTest, seasonDateForTest
	feastFileForTest, seasonDateForTest = path, ""
	modules = []moduleFactory{func(h *Hub) Module { m := newHarvestTable(h); r.mod = m.(*harvestTable); return m }}
	t.Cleanup(func() { modules, feastFileForTest, seasonDateForTest = savedMods, savedPath, savedDate })
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		ft := &toyTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	return r
}

func (r *feastRig) advance(d time.Duration) {
	end := r.now.Add(d)
	for {
		var next *toyTimer
		for _, ft := range r.timers {
			if !ft.stopped && !ft.at.After(end) && (next == nil || ft.at.Before(next.at)) {
				next = ft
			}
		}
		if next == nil {
			break
		}
		next.stopped = true
		if next.at.After(r.now) {
			r.now = next.at
		}
		next.fn()
	}
	r.now = end
}

func (r *feastRig) lay(cl *Client, c string) {
	r.now = r.now.Add(time.Second)
	send(r.hub, cl, map[string]any{"t": "feast_lay", "c": c})
	r.advance(0) // the ledger's answer comes back through h.After(0)
}

func TestHarvestTableIsSharedByEveryoneOnTheStreet(t *testing.T) {
	r := newFeastRig(t, feastNoon, "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	bo := joinAt(r.hub, 2, "Bo", feastStreet, 100, -120)
	if st := lastOf(t, ada, "feast_state"); st["on"] != true || st["total"].(float64) != 0 {
		t.Fatalf("joined state %v", st)
	}
	frames(bo)
	r.lay(ada, "pumpkin_pie")
	mine := lastOf(t, ada, "feast_laid")
	if mine["mine"] != true || mine["first"] != true || mine["total"].(float64) != 1 {
		t.Fatalf("layer's word %v", mine)
	}
	if _, ok := mine["use"]; ok {
		t.Fatalf("no ledger, no use: %v", mine)
	}
	seen := lastOf(t, bo, "feast_laid")
	row := seen["row"].(map[string]any)
	if seen["mine"] != nil || seen["first"] != nil || row["c"] != "pumpkin_pie" || row["name"] != "Ada" || row["by"].(float64) != 1 {
		t.Fatalf("the street's word %v", seen)
	}
	r.lay(bo, "onion")
	if lastOf(t, ada, "feast_laid")["total"].(float64) != 2 {
		t.Fatal("Ada sees Bo's crop")
	}
	// Someone arriving later sees both, newest last.
	cy := joinAt(r.hub, 3, "Cy", feastStreet, 0, -120)
	st := lastOf(t, cy, "feast_state")
	rows := st["rows"].([]any)
	if st["total"].(float64) != 2 || len(rows) != 2 || rows[1].(map[string]any)["c"] != "onion" {
		t.Fatalf("arrival's state %v", st)
	}
}

func TestHarvestTableRefusals(t *testing.T) {
	r := newFeastRig(t, feastNoon, "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	elsewhere := joinAt(r.hub, 2, "Bo", "GLI2V0UN7RD1T2J", 0, -120)
	r.lay(elsewhere, "pumpkin_pie")
	if no := lastOf(t, elsewhere, "feast_no"); no["code"] != "not_here" {
		t.Fatalf("another street %v", no)
	}
	r.lay(ada, "pick")
	if no := lastOf(t, ada, "feast_no"); no["code"] != "not_dish" || no["message"] != feastTextNotDish {
		t.Fatalf("a tool %v", no)
	}
	// Harvest Stuffing is the game's own dish.
	r.lay(ada, "harvest_stuffing")
	lastOf(t, ada, "feast_laid")
	// Too fast.
	send(r.hub, ada, map[string]any{"t": "feast_lay", "c": "corn"})
	if no := lastOf(t, ada, "feast_no"); no["code"] != "slow_down" {
		t.Fatalf("too fast %v", no)
	}
	// Out of season.
	off := newFeastRig(t, time.Date(2026, time.November, 17, 12, 0, 0, 0, urZone), "")
	early := joinAt(off.hub, 1, "Ada", feastStreet, 0, -120)
	if st := lastOf(t, early, "feast_state"); st["on"] != false {
		t.Fatalf("before the season %v", st)
	}
	off.lay(early, "pumpkin_pie")
	if no := lastOf(t, early, "feast_no"); no["code"] != "no_season" || no["message"] != feastTextNoSeason {
		t.Fatalf("before the season %v", no)
	}
}

func TestHarvestTableFirstOfferingOfEachGameDay(t *testing.T) {
	r := newFeastRig(t, feastNoon, "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	bo := joinAt(r.hub, 2, "Bo", feastStreet, 0, -120)
	r.lay(ada, "pumpkin_pie")
	if lastOf(t, ada, "feast_laid")["first"] != true {
		t.Fatal("first of the day")
	}
	r.lay(ada, "potcorn")
	if lastOf(t, ada, "feast_laid")["first"] != false {
		t.Fatal("the second is no first")
	}
	// Another player's first is their own.
	r.lay(bo, "corn")
	if lastOf(t, bo, "feast_laid")["first"] != true {
		t.Fatal("Bo's first")
	}
	// The next game day (four real hours) gives a new first.
	r.advance(time.Duration(gameDayMs) * time.Millisecond)
	r.lay(ada, "wavy_gravy")
	if lastOf(t, ada, "feast_laid")["first"] != true {
		t.Fatal("a new game day")
	}
}

func TestHarvestTableUsesTheLedger(t *testing.T) {
	r := newFeastRig(t, feastNoon, "")
	f := newFakeUseLedger()
	f.give(1, "pumpkin_pie", 1)
	r.mod.uses.ledger = f
	r.mod.uses.spawn = func(fn func()) { fn() }
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	r.lay(ada, "pumpkin_pie")
	mine := lastOf(t, ada, "feast_laid")
	use, ok := mine["use"].(map[string]any)
	if !ok || use["c"] != "pumpkin_pie" || use["n"].(float64) != 1 || use["spent"] != true {
		t.Fatalf("the ledger's word %v", mine)
	}
	// The one pie is gone: the next is refused and the table keeps one.
	r.lay(ada, "pumpkin_pie")
	if no := lastOf(t, ada, "feast_no"); no["code"] != "no_item" {
		t.Fatalf("none left %v", no)
	}
	if r.mod.total != 1 {
		t.Fatalf("table %d", r.mod.total)
	}
	// No answer from the site: refused, nothing laid.
	f.give(1, "corn", 1)
	f.down = true
	r.lay(ada, "corn")
	if no := lastOf(t, ada, "feast_no"); no["code"] != "no_answer" || r.mod.total != 1 {
		t.Fatalf("site down %v %d", no, r.mod.total)
	}
}

func TestHarvestTableIsPackedAwayWhenTheSeasonEnds(t *testing.T) {
	r := newFeastRig(t, time.Date(2026, time.November, 29, 23, 0, 0, 0, urZone), "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	r.lay(ada, "pumpkin_pie")
	frames(ada)
	r.advance(2 * time.Hour)
	st := lastOf(t, ada, "feast_state")
	if st["on"] != false || st["total"].(float64) != 0 || len(st["rows"].([]any)) != 0 {
		t.Fatalf("after the season %v", st)
	}
	r.lay(ada, "pumpkin_pie")
	if no := lastOf(t, ada, "feast_no"); no["code"] != "no_season" {
		t.Fatalf("after the season %v", no)
	}
}

func TestHarvestTableSurvivesARestartButNotTheSeason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feast.json")
	r := newFeastRig(t, feastNoon, path)
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	r.lay(ada, "pumpkin_pie")
	r.lay(ada, "corn")
	r.mod.save()
	flushStateWrites("")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	again := newFeastRig(t, feastNoon.Add(time.Hour), path)
	if again.mod.total != 2 || len(again.mod.rows) != 2 || again.mod.daily[1] == 0 {
		t.Fatalf("after a restart %d %v %v", again.mod.total, again.mod.rows, again.mod.daily)
	}
	// Read after the season: packed away as it is read.
	later := newFeastRig(t, time.Date(2026, time.December, 1, 12, 0, 0, 0, urZone), path)
	if later.mod.total != 0 || len(later.mod.rows) != 0 || later.mod.key != "" {
		t.Fatalf("after the season %d %v %q", later.mod.total, later.mod.rows, later.mod.key)
	}
	// Next year's table starts empty, too.
	f := feastSaved{Key: "pot_luck_day:2025", Rows: []feastRow{{C: "corn", By: 1}}, Total: 9}
	b := []byte(`{"key":"` + f.Key + `","rows":[{"c":"corn","by":1,"name":""}],"total":9,"daily":{"1":5}}`)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := newFeastRig(t, feastNoon, path)
	if fresh.mod.total != 0 || fresh.mod.key != "pot_luck_day:2026" {
		t.Fatalf("a new year %d %q", fresh.mod.total, fresh.mod.key)
	}
}

func TestHarvestTableKeepsTheNewestOfferings(t *testing.T) {
	r := newFeastRig(t, feastNoon, "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	for i := 0; i < feastKeep+5; i++ {
		r.lay(ada, "corn")
	}
	if r.mod.total != feastKeep+5 || len(r.mod.rows) != feastKeep {
		t.Fatalf("kept %d of %d", len(r.mod.rows), r.mod.total)
	}
}

func TestHarvestTableSeasonQAHold(t *testing.T) {
	seasonDateForTest = "2026-11-26"
	defer func() { seasonDateForTest = "" }()
	if feastSeasonKey(time.Date(2026, time.November, 17, 12, 0, 0, 0, urZone)) != "pot_luck_day:2026" {
		t.Fatal("a held date is in season")
	}
}
