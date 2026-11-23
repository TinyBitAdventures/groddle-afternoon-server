package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// newUrRig is a hub with only Happy New Ur, a clock the test moves, fake timers and a fake Global.
type newUrRig struct {
	feastRig
	mod    *newUr
	global []string
}

func newNewUrRig(t *testing.T, at time.Time) *newUrRig {
	t.Helper()
	r := &newUrRig{feastRig: feastRig{t: t, now: at}}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { m := newNewUr(h); r.mod = m.(*newUr); return m }}
	t.Cleanup(func() { modules = saved })
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		ft := &toyTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	r.hub.provided["channels.notice"] = func(room, text string) {
		if room == "global" {
			r.global = append(r.global, text)
		}
	}
	return r
}

func TestNewUrMidnightsComeFromTheGameClock(t *testing.T) {
	for _, y := range []int{2026, 2027, 2031, 2040} {
		ats, big := newUrMidnights(y)
		if len(ats) != 6 {
			t.Fatalf("%d: %d midnights", y, len(ats))
		}
		for i, at := range ats {
			// Each is the start of a game day: whole days from Glitch's epoch.
			if (at.Unix()-newUrEpoch)%int64(newUrDay/time.Second) != 0 {
				t.Fatalf("%d: %v is not a game day's start", y, at)
			}
			if p := at.In(urZone); p.Month() != time.December || p.Day() != 31 {
				t.Fatalf("%d: %v is not December 31 on Ur's clock", y, p)
			}
			if i > 0 && at.Sub(ats[i-1]) != newUrDay {
				t.Fatalf("%d: midnights not four hours apart", y)
			}
		}
		if h := ats[big].In(urZone).Hour(); h != newUrBigHour {
			t.Fatalf("%d: the big midnight at %d:00 Pacific", y, h)
		}
	}
	// 2026: 1 PM Pacific on December 31 is 21:00 UTC, a new game day (Austin's pick).
	ats, big := newUrMidnights(2026)
	if got := ats[big].UTC().Format(time.RFC3339); got != "2026-12-31T21:00:00Z" {
		t.Fatalf("the big midnight in 2026 is %s", got)
	}
}

func TestNewUrSendsTheClockWhenAskedAndTheMidnightToEveryone(t *testing.T) {
	ats, big := newUrMidnights(2026)
	r := newNewUrRig(t, ats[big].Add(-5*time.Minute))
	ada := joinAt(r.hub, 1, "Ada", "GTEST1", 0, 0)
	bo := joinAt(r.hub, 2, "Bo", "GTEST2", 0, 0)
	noneOf(t, ada, "new_ur") // a connect sends nothing new: the page asks
	send(r.hub, ada, map[string]any{"t": "new_ur_sync"})
	st := lastOf(t, ada, "new_ur")
	if int64(st["now"].(float64)) != r.now.UnixMilli() || int64(st["big"].(float64)) != ats[big].UnixMilli() || len(st["midnights"].([]any)) != 6 {
		t.Fatalf("state %v", st)
	}
	frames(bo)
	send(r.hub, bo, map[string]any{"t": "new_ur_sync"})
	if lastOf(t, bo, "new_ur")["midnights"] == nil {
		t.Fatal("sync answers with the state")
	}
	r.advance(5 * time.Minute)
	for _, cl := range []*Client{ada, bo} {
		m := lastOf(t, cl, "new_ur")
		if m["ev"] != "midnight" || m["index"].(float64) != 4 || m["big"] != true || int64(m["at"].(float64)) != ats[big].UnixMilli() || int64(m["now"].(float64)) != ats[big].UnixMilli() {
			t.Fatalf("midnight %v", m)
		}
	}
	// Once only, and the next one four hours on is not the big one.
	r.advance(time.Minute)
	noneOf(t, ada, "new_ur")
	r.advance(4 * time.Hour)
	if m := lastOf(t, ada, "new_ur"); m["index"].(float64) != 5 || m["big"] != false {
		t.Fatalf("the fifth %v", m)
	}
}

func TestNewUrTellsGlobalBeforeTheBigMidnightEachOnce(t *testing.T) {
	ats, big := newUrMidnights(2026)
	r := newNewUrRig(t, ats[big].Add(-25*time.Hour))
	joinAt(r.hub, 1, "Ada", "GTEST1", 0, 0)
	r.advance(25 * time.Hour)
	if len(r.global) != len(newUrNotices) {
		t.Fatalf("Global heard %q", r.global)
	}
	for i, n := range newUrNotices {
		if r.global[i] != n.text {
			t.Fatalf("line %d: %q", i, r.global[i])
		}
	}
	// Another connect (a reconnect, a new player) never repeats a line.
	joinAt(r.hub, 2, "Bo", "GTEST2", 0, 0)
	r.advance(time.Hour)
	if len(r.global) != len(newUrNotices) {
		t.Fatalf("repeated: %q", r.global)
	}
}

func TestNewUrDoesNotPostAStaleLineAfterARestart(t *testing.T) {
	ats, big := newUrMidnights(2026)
	// Up 30 minutes before the big midnight: the 24-hour and one-hour lines are long past.
	r := newNewUrRig(t, ats[big].Add(-30*time.Minute))
	joinAt(r.hub, 1, "Ada", "GTEST1", 0, 0)
	r.advance(30 * time.Minute)
	if len(r.global) != 1 || r.global[0] != newUrNotices[2].text {
		t.Fatalf("Global heard %q", r.global)
	}
}

func TestNewUrKeepsWakingThroughTheYear(t *testing.T) {
	// A connect in June: the timer wakes at least every seasonWakeMax and finds December's midnights.
	r := newNewUrRig(t, time.Date(2026, time.June, 1, 12, 0, 0, 0, urZone))
	ada := joinAt(r.hub, 1, "Ada", "GTEST1", 0, 0)
	frames(ada)
	ats, _ := newUrMidnights(2026)
	r.advance(ats[0].Sub(r.now))
	if m := lastOf(t, ada, "new_ur"); m["ev"] != "midnight" || m["index"].(float64) != 1 {
		t.Fatalf("first midnight %v", m)
	}
	// Into January and on to the next eve.
	next, _ := newUrMidnights(2027)
	r.advance(next[0].Sub(r.now))
	if m := lastOf(t, ada, "new_ur"); m["ev"] != "midnight" || int64(m["at"].(float64)) != next[0].UnixMilli() {
		t.Fatalf("next year's first %v", m)
	}
}

// The game's js/new-ur.js says the same lines (its tests read this file too).
func TestNewUrNoticesMatchTheGame(t *testing.T) {
	game := os.Getenv("GRODDLE_GAME_DIR")
	if game == "" {
		game = filepath.Join("..", "groddle-afternoon", "spike")
	}
	b, err := os.ReadFile(filepath.Join(game, "public", "js", "new-ur.js"))
	if err != nil {
		t.Skipf("no game checkout: %v", err)
	}
	src := string(b)
	for _, n := range newUrNotices {
		if !strings.Contains(src, "'"+n.text+"'") {
			t.Fatalf("js/new-ur.js lacks %q", n.text)
		}
	}
	if !regexp.MustCompile(`BIG_HOUR = 13;`).MatchString(src) {
		t.Fatal("js/new-ur.js BIG_HOUR differs")
	}
}
