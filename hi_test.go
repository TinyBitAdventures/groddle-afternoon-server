package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

type hiRig struct {
	t      *testing.T
	hub    *Hub
	mod    *hiModule
	now    time.Time
	timers []*fakeTimer
	ignore map[[2]int64]bool
	rolls  []int
}

// hiTestIgnores stands in for buddies.go's buddies.ignoring hook.
type hiTestIgnores struct{ r *hiRig }

func (m hiTestIgnores) Register(r *Registry) {
	r.Provide("buddies.ignoring", func(a, b int64) bool { return m.r.ignore[[2]int64{a, b}] })
}

func newHiRig(t *testing.T, path string) *hiRig {
	t.Helper()
	r := &hiRig{t: t, now: time.UnixMilli(gameEpochMs + 1000*gameDayMs + 3_600_000), ignore: map[[2]int64]bool{}}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.mod = newHiWith(h, path); return r.mod },
		func(h *Hub) Module { return hiTestIgnores{r} },
	}
	t.Cleanup(func() { modules = saved })
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		ft := &fakeTimer{d: r.now.Sub(time.UnixMilli(0)) + d, fn: fn}
		r.timers = append(r.timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	// Variant rolls: the queued ones, then always "bats" (index 0).
	r.mod.roll = func(n int) int {
		if len(r.rolls) > 0 {
			v := r.rolls[0]
			r.rolls = r.rolls[1:]
			return v % n
		}
		return 0
	}
	return r
}

// advance moves the clock on and runs the timers that came due.
func (r *hiRig) advance(d time.Duration) {
	r.now = r.now.Add(d)
	for {
		ran := false
		for _, ft := range r.timers {
			if !ft.stopped && ft.d <= r.now.Sub(time.UnixMilli(0)) {
				ft.stopped = true
				ft.fn() // h.After's wrapper takes the hub lock
				ran = true
			}
		}
		if !ran {
			return
		}
	}
}

func (r *hiRig) join(id int64, name string, x float64, dir int) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	send(r.hub, cl, map[string]any{"t": "hi_sync"})
	b, _ := json.Marshal(map[string]any{"t": "join", "street": "GHITEST", "x": x, "y": 500, "dir": dir})
	r.hub.Handle(cl, b)
	return cl
}

func (r *hiRig) move(cl *Client, x float64, dir int) {
	send(r.hub, cl, map[string]any{"t": "move", "x": x, "y": 500, "dir": dir})
}

func hiVariantIndex(v string) int {
	for i, x := range hiVariants {
		if x == v {
			return i
		}
	}
	return -1
}

func TestHiStateOnConnectRollsTheButlerOnly(t *testing.T) {
	r := newHiRig(t, "")
	r.rolls = []int{hiVariantIndex("stars")}
	ada := r.join(1, "Ada", 100, 1)
	st := lastOf(t, ada, "hi_state")
	if st["variant"] != "" || st["butler"] != "stars" || st["held"].(float64) != 0 {
		t.Fatalf("state %v", st)
	}
	board := st["board"].(map[string]any)
	if len(board) != len(hiVariants) || board["bats"].(float64) != 0 {
		t.Fatalf("board %v", board)
	}
}

func TestHiFirstHiRollsASignAndTellsTheStreetsBoard(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 100, 1)
	bo := r.join(2, "Bo", 2000, 1) // too far to be a target
	frames(ada)
	frames(bo)
	r.rolls = []int{hiVariantIndex("hearts")}
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	af := frames(ada)
	said := kinds(af, "hi_said")
	if len(said) != 1 || said[0]["variant"] != "hearts" || said[0]["random"] != true || said[0]["target"] != nil {
		t.Fatalf("said %v", af)
	}
	if said[0]["activity"] != `You got the daily hi sign "Hearts"!` {
		t.Fatalf("activity %q", said[0]["activity"])
	}
	bf := frames(bo)
	if b := kinds(bf, "hi_board"); len(b) != 1 || b[0]["board"].(map[string]any)["hearts"].(float64) != 1 {
		t.Fatalf("board %v", bf)
	}
	if e := kinds(bf, "hi_emoted"); len(e) != 1 || e[0]["variant"] != "hearts" || e[0]["target"].(float64) != 0 {
		t.Fatalf("emoted %v", bf)
	}
	// Again two seconds later: the same sign, not counted again, and no "got" line.
	r.advance(2 * time.Second)
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	again := lastOf(t, ada, "hi_said")
	if again["variant"] != "hearts" || again["random"] != false || again["activity"] != "" {
		t.Fatalf("again %v", again)
	}
	if r.mod.st.Tracker.Counts["hearts"] != 1 {
		t.Fatalf("counts %v", r.mod.st.Tracker.Counts)
	}
}

func TestHiIsPacedAtOneEveryTwoSeconds(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 100, 1)
	frames(ada)
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	if n := len(kinds(frames(ada), "hi_said")); n != 1 {
		t.Fatalf("%d answers", n)
	}
}

func TestHiInfectsTheNearestInFrontWithNoSign(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 1000, 1)      // facing right
	behind := r.join(2, "Behind", 950, 1) // nearer, but behind
	front := r.join(3, "Front", 1300, 1)  // 300 px in front
	far := r.join(4, "Far", 1500, 1)      // 500 px: out of reach
	watch := r.join(5, "Watch", 3000, 1)  // on the street, out of reach
	for _, c := range []*Client{ada, behind, front, far, watch} {
		frames(c)
	}
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	said := lastOf(t, ada, "hi_said")
	target := said["target"].(map[string]any)
	if target["id"].(float64) != 3 || said["infected"] != true || said["bonus"] != false || said["mood"].(float64) != 5 {
		t.Fatalf("said %v", said)
	}
	if said["activity"] != `You got the daily hi sign "Bats", and passed it on to Front!` {
		t.Fatalf("activity %q", said["activity"])
	}
	got := lastOf(t, front, "hi_got")
	if got["variant"] != "bats" || got["infected"] != true || got["mood"].(float64) != 5 ||
		got["activity"] != `Ada just passed their daily hi sign to you: you've got "Bats"! Say "hi" back with the "5" key and get a bonus!` {
		t.Fatalf("got %v", got)
	}
	cs := got["counters"].([]any)
	if len(cs) != 1 || cs[0].([]any)[0] != "infected_by_pc:bats" {
		t.Fatalf("their counters %v", cs)
	}
	mine := said["counters"].([]any)
	if len(mine) != 2 || mine[0].([]any)[0] != "infected_pc:bats" || mine[1].([]any)[0] != "hi_variants:bats" {
		t.Fatalf("my counters %v", mine)
	}
	e := lastOf(t, watch, "hi_emoted")
	if e["id"].(float64) != 1 || e["target"].(float64) != 3 || e["variant"] != "bats" {
		t.Fatalf("watch %v", e)
	}
	noneOf(t, behind, "hi_got")
	if r.mod.st.Tracker.Counts["bats"] != 2 || r.mod.st.Tracker.Infectors["bats"][1] != 1 {
		t.Fatalf("tracker %+v", r.mod.st.Tracker)
	}
	// The next hi finds the one behind (Front was said hi to), with its own sign (none): infected too.
	r.advance(2 * time.Second)
	frames(ada)
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	said = lastOf(t, ada, "hi_said")
	if said["target"].(map[string]any)["id"].(float64) != 2 || said["activity"] != `You just passed your hi sign to Behind, now they have "Bats"!` {
		t.Fatalf("second %v", said)
	}
	// A third: nobody left in reach.
	r.advance(2 * time.Second)
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	if said = lastOf(t, ada, "hi_said"); said["target"] != nil {
		t.Fatalf("third %v", said)
	}
}

func TestHiSkipsABlockedPlayer(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 1000, 1)
	bo := r.join(2, "Bo", 1100, 1)
	r.ignore[[2]int64{2, 1}] = true // Bo blocks Ada
	frames(ada)
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	if said := lastOf(t, ada, "hi_said"); said["target"] != nil {
		t.Fatalf("said %v", said)
	}
	noneOf(t, bo, "hi_got")
}

func TestHiBonusBothWaysTwoSecondsOn(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 1000, 1)
	bo := r.join(2, "Bo", 1200, -1) // facing Ada
	frames(ada)
	frames(bo)
	send(r.hub, ada, map[string]any{"t": "hi_say"}) // Bo catches Bats
	r.advance(500 * time.Millisecond)
	frames(ada)
	frames(bo)
	send(r.hub, bo, map[string]any{"t": "hi_say"}) // the same sign back: the bonus
	said := lastOf(t, bo, "hi_said")
	if said["bonus"] != true || said["mood"].(float64) != 0 || said["infected"] != false ||
		said["activity"] != `You and Ada have the same hi sign "Bats". Bonus time!` {
		t.Fatalf("said %v", said)
	}
	got := lastOf(t, ada, "hi_got")
	if got["bonus"] != true || got["activity"] != `You and Bo have the hi sign "Bats". Bonus time!` {
		t.Fatalf("got %v", got)
	}
	noneOf(t, ada, "hi_bonus")
	r.advance(2 * time.Second)
	bb := lastOf(t, bo, "hi_bonus")
	ab := lastOf(t, ada, "hi_bonus")
	if bb["mood"].(float64) != 10 || ab["mood"].(float64) != 15 || bb["color"] != "#353535" {
		t.Fatalf("bonus bo %v ada %v", bb, ab)
	}
	if c := bb["counters"].([]any)[0].([]any); c[0] != "hi_jackpot:1" {
		t.Fatalf("bo counters %v", c)
	}
	if c := ab["counters"].([]any)[0].([]any); c[0] != "hi_jackpot:2" {
		t.Fatalf("ada counters %v", c)
	}
}

func TestHiAndMightyAndSocialButterflyCountDifferentPlayersToday(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 1000, 1)
	p := r.mod.player(1, "Ada")
	p.SaidHi = map[int64]bool{}
	for i := int64(100); i < 100+hiButterflyCount-1; i++ {
		p.SaidHi[i] = true
	}
	p.Jackpot = map[int64]bool{}
	for i := int64(100); i < 100+hiMightyCount-1; i++ {
		p.Jackpot[i] = true
	}
	bo := r.join(2, "Bo", 1100, -1)
	frames(ada)
	send(r.hub, bo, map[string]any{"t": "hi_say"}) // Ada catches Bo's
	r.advance(2 * time.Second)
	frames(ada)
	send(r.hub, ada, map[string]any{"t": "hi_say"}) // the 53rd player today, and a bonus
	said := lastOf(t, ada, "hi_said")
	if g := said["grant"].([]any); len(g) != 1 || g[0] != "social_butterfly" {
		t.Fatalf("grant %v", said)
	}
	r.advance(2 * time.Second)
	ab := lastOf(t, ada, "hi_bonus")
	if g := ab["grant"].([]any); len(g) != 1 || g[0] != "hi_and_mighty" {
		t.Fatalf("bonus %v", ab)
	}
}

func TestHiEvasionCountsARealMissileOnce(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 1000, 1)
	bo := r.join(2, "Bo", 1300, 1)
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	frames(bo)
	// A hi Ada never sent Carl, and a report from someone else, count for nothing.
	carl := r.join(3, "Carl", 5000, 1)
	r.advance(12 * time.Second)
	send(r.hub, carl, map[string]any{"t": "hi_hit", "from": 1, "secs": 12})
	noneOf(t, carl, "hi_evaded")
	// Longer than it flew: refused.
	send(r.hub, bo, map[string]any{"t": "hi_hit", "from": 1, "secs": 40})
	noneOf(t, bo, "hi_evaded")
	frames(ada)
	send(r.hub, bo, map[string]any{"t": "hi_hit", "from": 1, "secs": 12})
	ev := lastOf(t, bo, "hi_evaded")
	if g := ev["grant"].([]any); len(g) != 1 || g[0] != "hi_flyer" || ev["record"] != "set_daily" {
		t.Fatalf("evaded %v", ev)
	}
	rec := lastOf(t, ada, "hi_record")
	if rec["name"] != "Bo" || rec["msg"] != "12 seconds set today's hi sign evasion record for this location!" {
		t.Fatalf("record %v", rec)
	}
	if loc := r.mod.st.Locs["GHITEST"]; loc.Alltime == nil || loc.Alltime.Secs != 12 || loc.Daily.Label != "Bo" {
		t.Fatalf("loc %+v", loc)
	}
	if r.mod.st.Tracker.Records[0]["GHITEST"] != 2 {
		t.Fatalf("records %v", r.mod.st.Tracker.Records)
	}
	// The same missile again: nothing.
	send(r.hub, bo, map[string]any{"t": "hi_hit", "from": 1, "secs": 12})
	noneOf(t, bo, "hi_evaded")
}

func TestHiEvasionUnderTenSecondsIsJustTheLine(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 1000, 1)
	bo := r.join(2, "Bo", 1300, 1)
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	r.advance(7 * time.Second)
	frames(bo)
	send(r.hub, bo, map[string]any{"t": "hi_hit", "from": 1, "secs": 6})
	ev := lastOf(t, bo, "hi_evaded")
	if ev["activity"] != "You evaded that hi sign for 6 seconds!" || ev["record"] != "" {
		t.Fatalf("evaded %v", ev)
	}
	// Reported far later than the missile could have flown (more than 5 s short): refused.
	ada2 := r.join(3, "Cy", 1310, 1)
	_ = ada2
	r.advance(2 * time.Second)
	send(r.hub, ada, map[string]any{"t": "hi_say"}) // to Cy
	r.advance(30 * time.Second)
	frames(ada2)
	send(r.hub, ada2, map[string]any{"t": "hi_hit", "from": 1, "secs": 3})
	noneOf(t, ada2, "hi_evaded")
}

func TestHiRecordsBrokenAndTiedByTheSourcesWords(t *testing.T) {
	r := newHiRig(t, "")
	st := r.mod
	cl := r.join(9, "Zed", 0, 1)
	if s, msg := st.record(cl, "GHITEST", 20); s != "set_daily" || msg != "20 seconds set today's hi sign evasion record for this location!" {
		t.Fatalf("%s %s", s, msg)
	}
	if s, msg := st.record(cl, "GHITEST", 25); s != "broke_daily" || msg != "25 seconds broke today's hi sign evasion record of 20 seconds for this location, set by Zed!" {
		t.Fatalf("%s %s", s, msg)
	}
	if s, _ := st.record(cl, "GHITEST", 25); s != "tied_daily" {
		t.Fatalf("%s", s)
	}
	if s, _ := st.record(cl, "GHITEST", 11); s != "fail" {
		t.Fatalf("%s", s)
	}
	// Next day: the daily record is gone, the all-time one stands.
	r.advance(time.Duration(gameDayMs) * time.Millisecond)
	if s, msg := st.record(cl, "GHITEST", 11); s != "set_daily" {
		t.Fatalf("%s %s", s, msg)
	}
	if st.st.Locs["GHITEST"].Alltime.Secs != 25 {
		t.Fatalf("alltime %+v", st.st.Locs["GHITEST"].Alltime)
	}
	if s, _ := st.record(cl, "RACE_abc", 30); s != "" {
		t.Fatalf("race %s", s)
	}
}

func TestHiNewDayWinnerTopSpreaderAndHeldRecords(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 1000, 1)
	bo := r.join(2, "Bo", 1300, 1)
	cy := r.join(3, "Cy", 900, 1)
	send(r.hub, ada, map[string]any{"t": "hi_say"}) // Bats; Bo catches it
	r.advance(2 * time.Second)
	send(r.hub, ada, map[string]any{"t": "hi_say"}) // Cy catches it
	r.advance(12 * time.Second)
	send(r.hub, bo, map[string]any{"t": "hi_hit", "from": 1, "secs": 12}) // Bo holds today's record
	frames(ada)
	frames(bo)
	// The day ends while they are online.
	next := time.UnixMilli(gameEpochMs + (r.mod.today()+1)*gameDayMs)
	r.advance(next.Sub(r.now) + time.Second)
	w := lastOf(t, ada, "hi_winner")["yesterday"].(map[string]any)
	if w["variant"] != "bats" || w["count"].(float64) != 3 {
		t.Fatalf("winner %v", w)
	}
	top := w["top"].(map[string]any)
	if top["id"].(float64) != 1 || top["name"] != "Ada" || top["count"].(float64) != 2 || top["variant"] != "bats" {
		t.Fatalf("top %v", top)
	}
	bf := frames(bo)
	st := kinds(bf, "hi_state")
	if len(st) != 1 || st[0]["variant"] != "" || st[0]["held"].(float64) != 1 {
		t.Fatalf("bo state %v", bf)
	}
	if r.mod.st.Tracker.History[r.mod.today()-1].Winner.Variant != "bats" {
		t.Fatalf("history %v", r.mod.st.Tracker.History)
	}
	send(r.hub, bo, map[string]any{"t": "hi_ack", "held": 5})
	if p := r.mod.st.Players[2]; p.Held != 0 {
		t.Fatalf("held %d", p.Held)
	}
	// A new day's first hi rolls a new sign, and the old targets are free again.
	r.rolls = []int{hiVariantIndex("pigs")}
	frames(ada)
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	said := lastOf(t, ada, "hi_said")
	if said["variant"] != "pigs" || said["random"] != true || said["target"] == nil {
		t.Fatalf("new day %v", said)
	}
	_ = cy
}

func TestHiButlerAtHome(t *testing.T) {
	r := newHiRig(t, "")
	r.rolls = []int{hiVariantIndex("stars")} // the Butler's roll today
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	send(r.hub, ada, map[string]any{"t": "hi_sync"})
	if st := lastOf(t, ada, "hi_state"); st["butler"] != "stars" {
		t.Fatalf("state %v", st)
	}
	// The Butler said hi first: Ada takes its sign, the server's roll, whatever the client says.
	send(r.hub, ada, map[string]any{"t": "hi_caught", "from": "butler", "variant": "pigs"})
	if st := lastOf(t, ada, "hi_state"); st["variant"] != "stars" {
		t.Fatalf("caught %v", st)
	}
	if r.mod.st.Tracker.Counts["stars"] != 1 || len(r.mod.st.Tracker.Infectors["stars"]) != 0 {
		t.Fatalf("tracker %+v", r.mod.st.Tracker)
	}
	// Saying hi to the Butler: no player target even with one in reach; infecting it counts once a day.
	bo := r.join(2, "Bo", 0, 1)
	b, _ := json.Marshal(map[string]any{"t": "join", "street": "GHITEST", "x": 10, "y": 500})
	r.hub.Handle(ada, b)
	frames(ada)
	send(r.hub, ada, map[string]any{"t": "hi_say", "butler": "infect"})
	if said := lastOf(t, ada, "hi_said"); said["target"] != nil || said["variant"] != "stars" {
		t.Fatalf("said %v", said)
	}
	noneOf(t, bo, "hi_got")
	r.advance(2 * time.Second)
	send(r.hub, ada, map[string]any{"t": "hi_say", "butler": "infect"})
	if r.mod.st.Tracker.Counts["stars"] != 2 || r.mod.st.Tracker.Infectors["stars"][1] != 1 {
		t.Fatalf("tracker %+v", r.mod.st.Tracker)
	}
}

func TestHiStateSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hi.json")
	r := newHiRig(t, path)
	ada := r.join(1, "Ada", 1000, 1)
	r.join(2, "Bo", 1200, 1)
	send(r.hub, ada, map[string]any{"t": "hi_say"})
	r.advance(2 * time.Second) // the save
	r2 := newHiRig(t, path)
	r2.now = r.now
	if p := r2.mod.st.Players[2]; p == nil || p.Variant != "bats" {
		t.Fatalf("players %+v", r2.mod.st.Players)
	}
	if r2.mod.st.Players[1].Pcs[2] == 0 || r2.mod.st.Tracker.Counts["bats"] != 2 {
		t.Fatalf("state %+v", r2.mod.st)
	}
	// Bo's sign is still today's: a hi from Ada again finds nobody new.
	ada2 := r2.join(1, "Ada", 1000, 1)
	r2.join(2, "Bo", 1200, 1)
	frames(ada2)
	send(r2.hub, ada2, map[string]any{"t": "hi_say"})
	if said := lastOf(t, ada2, "hi_said"); said["target"] != nil || said["random"] != false {
		t.Fatalf("after restart %v", said)
	}
}

func TestPowderSneezingReachesTheStreetAndCountsSneezeORama(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 0, 1)
	var others []*Client
	for i := int64(2); i <= 6; i++ {
		others = append(others, r.join(i, "P"+string(rune('A'+i)), float64(i*500), 1))
	}
	for _, o := range others {
		frames(o)
	}
	r.rolls = []int{3} // the first sneeze 3 s in
	send(r.hub, ada, map[string]any{"t": "powder_spread", "cls": "sneezing_powder"})
	ok := lastOf(t, ada, "powder_spread")
	if ok["ok"] != true || ok["players"].(float64) != 5 || ok["counters"].([]any)[0].([]any)[0] != "powders:sneezeorama" {
		t.Fatalf("spread %v", ok)
	}
	c := lastOf(t, others[0], "powder_caught")
	if c["cls"] != "sneezing_powder" || c["from"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("caught %v", c)
	}
	r.advance(2 * time.Second)
	sn := kinds(frames(ada), "powder_sneeze")
	// Four first sneezes at 0 s (the default roll); P2's waits for 3 s.
	for _, m := range sn {
		if m["id"].(float64) == 2 || m["last"] != false {
			t.Fatalf("early sneezes %v", sn)
		}
	}
	if len(sn) != 4 {
		t.Fatalf("early sneezes %v", sn)
	}
	r.advance(time.Second)
	if sn = kinds(frames(ada), "powder_sneeze"); len(sn) != 1 || sn[0]["id"].(float64) != 2 {
		t.Fatalf("P2's first %v", sn)
	}
	r.advance(18 * time.Second)
	sn = kinds(frames(ada), "powder_sneeze")
	// The 13 s and the 21 s sneezes for all five.
	if len(sn) != 10 {
		t.Fatalf("sneezes %v", sn)
	}
	last := 0
	for _, m := range sn {
		if m["last"] == true {
			last++
		}
	}
	if last != 5 || len(r.mod.sneezing) != 0 {
		t.Fatalf("sneezes %v", sn)
	}
}

func TestPowderRefusedInACrowdAndPaced(t *testing.T) {
	r := newHiRig(t, "")
	ada := r.join(1, "Ada", 0, 1)
	for i := int64(2); i <= 11; i++ { // 11 players with Ada
		r.join(i, "P", float64(i*10), 1)
	}
	send(r.hub, ada, map[string]any{"t": "powder_spread", "cls": "sparkle_powder"})
	if got := lastOf(t, ada, "powder_spread"); got["ok"] != false || got["why"] != "crowd" {
		t.Fatalf("sparkle %v", got)
	}
	send(r.hub, ada, map[string]any{"t": "powder_spread", "cls": "sneezing_powder"})
	if got := lastOf(t, ada, "powder_spread"); got["ok"] != false || got["why"] != "slow" {
		t.Fatalf("paced %v", got)
	}
	r.advance(2 * time.Second)
	send(r.hub, ada, map[string]any{"t": "powder_spread", "cls": "sneezing_powder"})
	if got := lastOf(t, ada, "powder_spread"); got["ok"] != true || got["players"].(float64) != 10 {
		t.Fatalf("sneezing %v", got)
	}
	send(r.hub, ada, map[string]any{"t": "powder_spread", "cls": "krazy_salts"})
	noneOf(t, ada, "powder_spread")
}
