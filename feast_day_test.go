package main

import (
	"path/filepath"
	"testing"
	"time"
)

// feastDayRig is a hub with the Harvest Table and the feast, a clock the test moves and fake timers.
type feastDayRig struct {
	*feastRig
	day *feastDay
}

func newFeastDayRig(t *testing.T, at time.Time, path string) *feastDayRig {
	t.Helper()
	r := &feastDayRig{feastRig: &feastRig{t: t, now: at}}
	savedMods, savedPath, savedDay, savedDate := modules, feastFileForTest, feastDayFileForTest, seasonDateForTest
	feastFileForTest, feastDayFileForTest, seasonDateForTest = "", path, ""
	if path != "" {
		feastFileForTest = path + ".table"
	}
	modules = []moduleFactory{
		func(h *Hub) Module { m := newHarvestTable(h); r.mod = m.(*harvestTable); return m },
		func(h *Hub) Module { m := newFeastDay(h); r.day = m.(*feastDay); return m },
	}
	t.Cleanup(func() {
		modules, feastFileForTest, feastDayFileForTest, seasonDateForTest = savedMods, savedPath, savedDay, savedDate
	})
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		ft := &toyTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	return r
}

func (r *feastDayRig) bite(cl *Client, s int) {
	r.now = r.now.Add(time.Second)
	send(r.hub, cl, map[string]any{"t": "feastday_eat", "s": s, "n": r.day.piles[s].N})
}

// Pot Luck Day's Sunday, a week of lays, then Thanksgiving.
var feastDayMorning = time.Date(2026, time.November, 26, 9, 0, 0, 0, urZone)

func TestFeastDayIsOnlyThanksgiving(t *testing.T) {
	for _, c := range []struct {
		at time.Time
		on bool
	}{
		{time.Date(2026, time.November, 26, 0, 0, 0, 0, urZone), true},
		{time.Date(2026, time.November, 26, 23, 59, 0, 0, urZone), true},
		{time.Date(2026, time.November, 25, 23, 59, 0, 0, urZone), false},
		{time.Date(2026, time.November, 27, 0, 0, 0, 0, urZone), false},
		{time.Date(2027, time.November, 25, 12, 0, 0, 0, urZone), true},
	} {
		if (feastDayKey(c.at) != "") != c.on {
			t.Fatalf("%v: on %v", c.at, !c.on)
		}
	}
	if feastDaySpots[0] != 230 || feastDaySpots[feastDayPiles-1] != 750 {
		t.Fatalf("spots %v", feastDaySpots)
	}
	seasonDateForTest = "2026-11-26"
	defer func() { seasonDateForTest = "" }()
	if feastDayKey(time.Date(2026, time.November, 17, 12, 0, 0, 0, urZone)) != "feast_day:2026" {
		t.Fatal("a held date is the day")
	}
}

func TestFeastDayLaysTheWeeksFoodAndPotTopsItUp(t *testing.T) {
	r := newFeastDayRig(t, time.Date(2026, time.November, 23, 12, 0, 0, 0, urZone), "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	if st := lastOf(t, ada, "feastday_state"); st["on"] != false {
		t.Fatalf("before the day %v", st)
	}
	for i := 0; i < 6; i++ {
		r.lay(ada, "corn")
	}
	r.lay(ada, "carrot")
	r.now = feastDayMorning
	bo := joinAt(r.hub, 2, "Bo", feastStreet, 0, -120)
	st := lastOf(t, bo, "feastday_state")
	piles := st["piles"].([]any)
	if st["on"] != true || len(piles) != feastDayPiles || st["cap"].(float64) != feastDayCap || st["ate"].(float64) != 0 {
		t.Fatalf("the day's state %v", st)
	}
	// 7 laid: Pot adds 13 of his own dishes, so 20 piles and nothing more to come.
	pot := 0
	for _, n := range r.day.pot {
		pot += n
	}
	if pot != 13 || r.day.pot["pumpkin_pie"] != 3 || st["left"].(float64) != feastDayPiles*feastDayBites {
		t.Fatalf("pot %v left %v", r.day.pot, st["left"])
	}
	if r.day.placed["corn"] != 6 || r.day.placed["carrot"] != 1 {
		t.Fatalf("placed %v", r.day.placed)
	}
	p := piles[0].(map[string]any)
	if p["b"].(float64) != feastDayBites || p["x"].(float64) != 230 || p["s"].(float64) != 0 {
		t.Fatalf("pile %v", p)
	}
}

func TestFeastDayFirstEaterWinsAndTheSpotRefills(t *testing.T) {
	r := newFeastDayRig(t, time.Date(2026, time.November, 24, 12, 0, 0, 0, urZone), "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	for i := 0; i < 30; i++ {
		r.lay(ada, "potato")
	}
	r.now = feastDayMorning
	send(r.hub, ada, map[string]any{"t": "feastday_sync"})
	bo := joinAt(r.hub, 2, "Bo", "L"+feastStreetKey, 0, -120)
	frames(ada)
	frames(bo)
	left := r.day.left()
	if left != 30*feastDayBites {
		t.Fatalf("left %d", left)
	}
	r.bite(ada, 4)
	ate := lastOf(t, ada, "feastday_ate")
	if ate["ate"].(float64) != 1 || ate["c"] != "potato" || ate["left"].(float64) != float64(left-1) || ate["pile"].(map[string]any)["b"].(float64) != 2 {
		t.Fatalf("Ada's bite %v", ate)
	}
	seen := lastOf(t, bo, "feastday_pile")
	if seen["street"] != "L"+feastStreetKey || seen["pile"].(map[string]any)["b"].(float64) != 2 {
		t.Fatalf("Bo sees it %v", seen)
	}
	r.bite(bo, 4)
	r.bite(ada, 4)
	if r.day.piles[4].B != 0 {
		t.Fatalf("emptied %v", r.day.piles[4])
	}
	// A bite of a pile already gone (the eater's page had not heard): too slow.
	serial := r.day.piles[4].N
	frames(bo)
	send(r.hub, bo, map[string]any{"t": "feastday_eat", "s": 4, "n": serial})
	if no := lastOf(t, bo, "feastday_no"); no["code"] != "gone" || no["message"] != feastDayTextGone {
		t.Fatalf("too slow %v", no)
	}
	// 20 s later the spot is laid again, a new pile, for everyone.
	r.advance(feastDayRefill)
	if r.day.piles[4].B != feastDayBites || r.day.piles[4].N == serial {
		t.Fatalf("refilled %v", r.day.piles[4])
	}
	if p := lastOf(t, ada, "feastday_pile")["pile"].(map[string]any); p["b"].(float64) != feastDayBites {
		t.Fatalf("Ada sees the refill %v", p)
	}
	if r.day.placed["potato"] != feastDayPiles+1 {
		t.Fatalf("placed %v", r.day.placed)
	}
}

func TestFeastDayElevenBitesAPlayer(t *testing.T) {
	r := newFeastDayRig(t, feastDayMorning, "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	for i := 0; i < feastDayCap; i++ {
		r.bite(ada, i)
	}
	if lastOf(t, ada, "feastday_ate")["ate"].(float64) != feastDayCap {
		t.Fatal("eleven")
	}
	r.bite(ada, 15)
	if no := lastOf(t, ada, "feastday_no"); no["code"] != "full" || no["message"] != feastDayTextFull {
		t.Fatalf("the twelfth %v", no)
	}
	if r.day.piles[15].B != feastDayBites {
		t.Fatal("a refused bite is not taken")
	}
	// Too fast, another street, out of the day.
	bo := joinAt(r.hub, 2, "Bo", feastStreet, 0, -120)
	r.bite(bo, 15)
	send(r.hub, bo, map[string]any{"t": "feastday_eat", "s": 15, "n": r.day.piles[15].N})
	if no := lastOf(t, bo, "feastday_no"); no["code"] != "slow_down" {
		t.Fatalf("too fast %v", no)
	}
	away := joinAt(r.hub, 3, "Cy", "GLI2V0UN7RD1T2J", 0, -120)
	r.bite(away, 15)
	if no := lastOf(t, away, "feastday_no"); no["code"] != "not_here" {
		t.Fatalf("elsewhere %v", no)
	}
}

func TestFeastDayRunsOutWhenThePoolIsEaten(t *testing.T) {
	r := newFeastDayRig(t, feastDayMorning, "")
	// Nothing laid: Pot's 20 dishes, 60 bites, then the spots stay empty.
	for id := int64(1); id <= 6; id++ {
		cl := joinAt(r.hub, id, "P", feastStreet, 0, -120)
		for i := 0; i < feastDayCap; i++ {
			s := -1
			for k, p := range r.day.piles {
				if p.B > 0 {
					s = k
					break
				}
			}
			if s < 0 {
				break
			}
			r.bite(cl, s)
		}
		r.advance(feastDayRefill)
	}
	if r.day.left() != 0 || len(r.day.shown()) != 0 {
		t.Fatalf("left %d piles %v", r.day.left(), r.day.shown())
	}
	if r.day.placed["pumpkin_pie"] != 4 {
		t.Fatalf("placed %v", r.day.placed)
	}
}

func TestFeastDayLeftoversArePackedAwayAtMidnight(t *testing.T) {
	r := newFeastDayRig(t, time.Date(2026, time.November, 26, 23, 0, 0, 0, urZone), "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	r.bite(ada, 0)
	frames(ada)
	r.advance(2 * time.Hour)
	st := lastOf(t, ada, "feastday_state")
	if st["on"] != false || len(st["piles"].([]any)) != 0 || r.day.key != "" {
		t.Fatalf("after midnight %v", st)
	}
	// The table stays: Pot Luck Day runs to the 29th.
	if r.mod.key == "" {
		t.Fatal("the Harvest Table is still out")
	}
	r.bite(ada, 0)
	if no := lastOf(t, ada, "feastday_no"); no["code"] != "no_season" {
		t.Fatalf("the next day %v", no)
	}
}

func TestFeastDaySurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feast-day.json")
	r := newFeastDayRig(t, time.Date(2026, time.November, 25, 12, 0, 0, 0, urZone), path)
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	for i := 0; i < 25; i++ {
		r.lay(ada, "onion")
	}
	r.mod.save()
	r.now = feastDayMorning
	send(r.hub, ada, map[string]any{"t": "feastday_sync"})
	for i := 0; i < 3; i++ {
		r.bite(ada, 2)
	}
	r.bite(ada, 3)
	r.day.saveSoon()
	flushStateWrites("")
	again := newFeastDayRig(t, feastDayMorning.Add(time.Hour), path)
	if again.day.key != "feast_day:2026" || again.day.ate[1] != 4 || again.day.piles[2].B != 0 || again.day.piles[3].B != 2 {
		t.Fatalf("after a restart %q %v %v", again.day.key, again.day.ate, again.day.piles[:4])
	}
	// The spot left empty is laid again 20 s after the first word.
	bo := joinAt(again.hub, 2, "Bo", feastStreet, 0, -120)
	frames(bo)
	again.advance(feastDayRefill)
	if again.day.piles[2].B != feastDayBites || again.day.piles[2].N <= r.day.serial {
		t.Fatalf("refilled after a restart %v", again.day.piles[2])
	}
	// Read on another day: packed away as it is read.
	later := newFeastDayRig(t, time.Date(2026, time.November, 27, 9, 0, 0, 0, urZone), path)
	cy := joinAt(later.hub, 3, "Cy", feastStreet, 0, -120)
	if st := lastOf(t, cy, "feastday_state"); st["on"] != false || later.day.key != "" {
		t.Fatalf("the next day %v", st)
	}
}

func TestFeastDayALayOnTheDayRefillsSpotsThePoolLeftEmpty(t *testing.T) {
	r := newFeastDayRig(t, feastDayMorning, "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	bo := joinAt(r.hub, 2, "Bo", feastStreet, 0, -120)
	// Nothing laid: Pot's 20, all out. Bo empties spot 0; nothing is left to lay there.
	for i := 0; i < feastDayBites; i++ {
		r.bite(bo, 0)
	}
	r.advance(feastDayRefill)
	if r.day.piles[0].B != 0 || r.day.refill[0] != nil {
		t.Fatalf("an empty pool lays nothing %v", r.day.piles[0])
	}
	frames(bo)
	r.lay(ada, "corn")
	if left := lastOf(t, bo, "feastday_left"); left["left"].(float64) != float64(19*feastDayBites+feastDayBites) {
		t.Fatalf("the street hears the pool grow %v", left)
	}
	r.advance(feastDayRefill)
	if r.day.piles[0].B != feastDayBites || r.day.piles[0].C != "corn" {
		t.Fatalf("the new dish is laid %v", r.day.piles[0])
	}
}
