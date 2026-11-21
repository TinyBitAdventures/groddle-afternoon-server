package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
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
	// G1015a and lane potluckk: the game's spots (js/feast-day.js FEAST_SPOTS), none on the spice
	// tree's trunk, the last clear of the shrine's base.
	want := [feastDayPiles]int{230, 249, 268, 287, 306, 325, 344, 515, 535, 556, 576, 597, 617, 637, 658, 678}
	if feastDaySpots != want {
		t.Fatalf("spots %v", feastDaySpots)
	}
	for i, x := range feastDaySpots {
		if (x > 344 && x < 515) || (i > 0 && x < feastDaySpots[i-1]+19) {
			t.Fatalf("spot %d at %d: %v", i, x, feastDaySpots)
		}
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
	// 7 laid: Pot adds 13 of his own dishes, so 20 dishes: 16 piles out, 4 to come.
	pot := 0
	for _, n := range r.day.pot {
		pot += n
	}
	if pot != 13 || r.day.pot["pumpkin_pie"] != 3 || st["left"].(float64) != feastDayPotDishes*feastDayBites {
		t.Fatalf("pot %v left %v", r.day.pot, st["left"])
	}
	out := 0
	for _, n := range r.day.placed {
		out += n
	}
	if out != feastDayPiles || r.day.placed["corn"] > 6 || r.day.placed["carrot"] > 1 {
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
	// Nothing laid: Pot's 20, 16 out and 4 to come. Spot 0 is emptied five times (Bo, then Ada, under
	// the 11-bite cap): the last time nothing is left to lay there.
	for round := 0; round <= feastDayPotDishes-feastDayPiles; round++ {
		eater := bo
		if round >= 3 {
			eater = ada
		}
		for i := 0; i < feastDayBites; i++ {
			r.bite(eater, 0)
		}
		r.advance(feastDayRefill)
	}
	if r.day.piles[0].B != 0 || r.day.refill[0] != nil {
		t.Fatalf("an empty pool lays nothing %v", r.day.piles[0])
	}
	frames(bo)
	r.lay(ada, "corn")
	if left := lastOf(t, bo, "feastday_left"); left["left"].(float64) != float64((feastDayPiles-1)*feastDayBites+feastDayBites) {
		t.Fatalf("the street hears the pool grow %v", left)
	}
	r.advance(feastDayRefill)
	if r.day.piles[0].B != feastDayBites || r.day.piles[0].C != "corn" {
		t.Fatalf("the new dish is laid %v", r.day.piles[0])
	}
}

// The feast is laid out at Thanksgiving Day's first second for a player already standing on the
// street, with nobody joining or eating (it was laid out only on a join or a bite).
func TestFeastDayIsLaidOutAtMidnightForWhoeverIsThere(t *testing.T) {
	r := newFeastDayRig(t, time.Date(2026, time.November, 25, 22, 0, 0, 0, urZone), "")
	ada := joinAt(r.hub, 1, "Ada", feastStreet, 0, -120)
	if st := lastOf(t, ada, "feastday_state"); st["on"] != false {
		t.Fatalf("the day before: %v", st)
	}
	r.advance(time.Date(2026, time.November, 25, 23, 59, 59, 0, urZone).Sub(r.now))
	noneOf(t, ada, "feastday_state")
	r.advance(2 * time.Second)
	st := lastOf(t, ada, "feastday_state")
	if st["on"] != true || len(st["piles"].([]any)) != feastDayPiles {
		t.Fatalf("at midnight: %v", st)
	}
	r.advance(24 * time.Hour)
	if st := lastOf(t, ada, "feastday_state"); st["on"] != false {
		t.Fatalf("the next day: %v", st)
	}
}

// Every feast pile, at the widest pile's width (89 px), keeps clear of what stands on Groddle Forest
// Junction's floor where this server holds it: the shrine (data/rook.json, its base 118 px wide), the
// street's trees on that floor (data/objects.json; the spice tree's trunk 26 px left of its foot to 42
// right, as the game draws it mirrored) and the subway door (~1099). The last pile at 700 overlapped
// the shrine's base, the first right of the tree at 490 its trunk.
func TestFeastDayPilesClearTheStreetsFixtures(t *testing.T) {
	const (
		left     = -3000.0 // Groddle Forest Junction's bounds: source x = street px + left
		pileHalf = 45.0
		margin   = 6.0
	)
	type span struct {
		name     string
		from, to float64
	}
	var fixtures []span
	var rook struct {
		Streets map[string][]any `json:"streets"`
	}
	if b, err := os.ReadFile("data/rook.json"); err != nil || json.Unmarshal(b, &rook) != nil {
		t.Fatalf("data/rook.json: %v", err)
	}
	sh := rook.Streets["G"+feastStreetKey]
	if len(sh) < 2 {
		t.Fatalf("no shrine on the street: %v", sh)
	}
	shrineX := sh[1].(float64) + left
	fixtures = append(fixtures, span{"shrine", shrineX - 59, shrineX + 59})
	var objs struct {
		Streets map[string]struct {
			Trees [][]any `json:"trees"`
		} `json:"streets"`
	}
	if json.Unmarshal(objectsJSON, &objs) != nil {
		t.Fatal("data/objects.json")
	}
	for _, tr := range objs.Streets["G"+feastStreetKey].Trees {
		i, x, y := int(tr[0].(float64)), tr[2].(float64)+left, tr[3].(float64)
		if y < 1000 { // up on a ledge, not the piles' floor (~1100)
			continue
		}
		l, r := -50*0.84, 31*0.84
		if i%2 == 1 {
			l, r = -31*0.84, 50*0.84
		}
		fixtures = append(fixtures, span{fmt.Sprintf("tree %d (%s)", i, tr[1]), x + l, x + r})
	}
	fixtures = append(fixtures, span{"subway door", 1099 - 50, 1099 + 50})
	if math.Abs(shrineX-789) > 2 {
		t.Fatalf("shrine at %.1f", shrineX)
	}
	for _, s := range feastDaySpots {
		x := float64(s)
		for _, f := range fixtures {
			if x+pileHalf > f.from-margin && x-pileHalf < f.to+margin {
				t.Fatalf("pile at %d within %v px of %s (%.0f-%.0f)", s, margin, f.name, f.from, f.to)
			}
		}
	}
}
