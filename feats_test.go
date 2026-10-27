package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeFeatLedger is the site's ledger for Feat rewards: currants per player, each id answered once.
type fakeFeatLedger struct {
	mu       sync.Mutex
	currants map[int64]int64
	seen     map[string]string
	calls    []string
	silent   bool
}

func newFakeFeatLedger() *fakeFeatLedger {
	return &fakeFeatLedger{currants: map[int64]int64{}, seen: map[string]string{}}
}

func (f *fakeFeatLedger) feat(id string, user int64, got tradeGoods) ledgerAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	if f.silent {
		return ledgerAnswer{}
	}
	if s, ok := f.seen[id]; ok {
		return ledgerAnswer{Status: s}
	}
	f.currants[user] += got.Currants
	f.seen[id] = "settled"
	return ledgerAnswer{Status: "settled"}
}

type featRig struct {
	t      *testing.T
	hub    *Hub
	m      *feats
	led    *fakeFeatLedger
	now    time.Time
	timers []*rigTimer
}

func featTableForTest(t *testing.T) featTable {
	var table featTable
	if err := json.Unmarshal(featsJSON, &table); err != nil {
		t.Fatal(err)
	}
	return table
}

// featRunStart is the start of a run whose Feat is feat (the schedule's own arithmetic).
func featRunStart(t *testing.T, table featTable, feat string) time.Time {
	k := int64(len(table.Order))
	at := int64(slices.Index(table.Order, feat))
	if at < 0 {
		t.Fatalf("%s is not in the order", feat)
	}
	n := int64(5340)
	for n%k != at {
		n++
	}
	return time.UnixMilli(table.EpochMs + n*table.RunMs)
}

func newFeatRig(t *testing.T, path string, led *fakeFeatLedger, now time.Time, edit func(*featTable)) *featRig {
	r := &featRig{t: t, led: led, now: now}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module {
		table := featTableForTest(t)
		if edit != nil {
			edit(&table)
		}
		r.m = newFeatsWith(h, table, path)
		if led != nil {
			r.m.ledger = led
		}
		r.m.spawn = func(fn func()) { fn() }
		r.m.dev = true
		return r.m
	}}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

func (r *featRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *featRig) player(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.do(cl, map[string]any{"t": "join", "street": "GA5101HF7F429V5"})
	r.do(cl, map[string]any{"t": "feat_sync"})
	return cl
}

// fire runs every live timer up to d long (once each), as time passes.
func (r *featRig) fire(d time.Duration) {
	for i := 0; i < 10; i++ {
		fired := false
		for _, tm := range append([]*rigTimer{}, r.timers...) {
			if tm.d <= d && !tm.stopped {
				tm.stopped = true
				tm.fn()
				fired = true
			}
		}
		if !fired {
			return
		}
	}
}

func (r *featRig) add(cl *Client, feat string, n int64, extra ...any) map[string]any {
	msg := map[string]any{"t": "feat_add", "feat": feat, "n": n}
	for i := 0; i+1 < len(extra); i += 2 {
		msg[extra[i].(string)] = extra[i+1]
	}
	r.do(cl, msg)
	got := kinds(drain(cl), "feat_counted")
	if len(got) != 1 {
		r.t.Fatalf("one feat_counted for %v: %v", msg, got)
	}
	return got[0]
}

func TestFeatTableComesFromTheGame(t *testing.T) {
	table := featTableForTest(t)
	// The wired Feats in game_day order, then the multiplayer-only one (G172, The Good Neighbor) last,
	// so every earlier Feat keeps its index (first and reward ids are built from it).
	if len(table.Order) != 11 || table.Order[0] != "meditation_of_groddlocritus" || table.Order[9] != "makeup_feat_redux" || table.Order[10] != "visiting_stones" {
		t.Fatalf("the wired Feats in game_day order, then visiting_stones: %v", table.Order)
	}
	// js/feats-mp.js GOALS: the minimum is 3 x the personal goal (animal_love 50), bonus x2, supermegabonus x3.
	a := table.Feats["animal_love"]
	if a.Goal != 150 || a.Bonus != 300 || a.Super != 450 || a.Pace.Burst != 10 {
		t.Fatalf("animal_love: %+v", a)
	}
	if table.RunMs != 86_400_000 || table.QuickMs != 1_800_000 || table.Epics["2"] != 6 || table.Epics["4"] != 3 {
		t.Fatalf("a day a Feat, 30 minutes quick, epics over every production Feat: %+v", table)
	}
	if table.Reward.XP != 272 || table.Reward.Currants != 170 || table.Reward.Energy != 102 || table.Reward.TopPlaces != 26 || table.Reward.CigarPlaces != 30 {
		t.Fatalf("doBreakIt's reward and inc_feats.js's places: %+v", table.Reward)
	}
	if !table.Feats["makeup_feat_redux"].Elsewhere || table.Feats["streaking"].Acts["wheel"].N != 5 {
		t.Fatal("the Last Feat chain counts its own first contribution; a Cubimal box is 5")
	}
}

func TestFeatsTakeTurnsADayEach(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "animal_love")
	r := newFeatRig(t, "", nil, start.Add(time.Hour), nil)
	a := r.player(1, "Ada")
	st := kinds(drain(a), "feat_state")
	if len(st) != 1 || st[0]["feat"] != "animal_love" || int64(num(st[0]["start"])) != start.UnixMilli() || int64(num(st[0]["end"])) != start.Add(24*time.Hour).UnixMilli() {
		t.Fatalf("today's Feat, 24 hours from a game day boundary: %v", st)
	}
	if (start.UnixMilli()-table.EpochMs)%table.GameDayMs != 0 {
		t.Fatal("a run starts on a game day")
	}
	// The next day, the next Feat (the one after animal_love in game_day order).
	r.now = start.Add(24*time.Hour + time.Minute)
	r.fire(48 * time.Hour)
	msgs := drain(a)
	if e := kinds(msgs, "feat_ended"); len(e) != 1 || e[0]["feat"] != "animal_love" || num(e[0]["tier"]) != 0 {
		t.Fatalf("the day is over and nobody contributed: failed: %v", msgs)
	}
	if s := kinds(msgs, "feat_state"); len(s) != 1 || s[0]["feat"] != "makeup_feat_redux" {
		t.Fatalf("everyone hears the new Feat: %v", msgs)
	}
	if len(kinds(msgs, "feat_reward")) != 0 {
		t.Fatal("a failed Feat pays nothing")
	}
}

func TestFeatContributionsCountForEveryoneAtTheSourcePace(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "animal_love")
	r := newFeatRig(t, "", newFakeFeatLedger(), start.Add(10*time.Minute), nil)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	drain(a)
	drain(b)

	got := r.add(a, "animal_love", 3)
	if num(got["n"]) != 3 || num(got["total"]) != 3 || num(got["mine"]) != 3 || num(got["place"]) != 1 {
		t.Fatalf("Ada's three count: %v", got)
	}
	// The first contribution ever: feats:contributed (inc_feats.js:54), quick_on_your_feat (72), the vog's remaining (78).
	first := kinds(drain(a), "feat_first")
	if len(first) != 0 {
		t.Fatalf("feat_first went with feat_counted, drained above: %v", first)
	}
	r.do(a, map[string]any{"t": "feat_sync"})
	first = kinds(drain(a), "feat_first")
	if len(first) != 1 || fmt.Sprint(first[0]["counters"]) != "[map[counter:feats:contributed n:1]]" || fmt.Sprint(first[0]["grants"]) != "[quick_on_your_feat]" || num(first[0]["remaining"]) != 147 {
		t.Fatalf("Ada's first contribution, sent until acked: %v", first)
	}
	r.do(a, map[string]any{"t": "feat_ack", "id": first[0]["id"]})
	r.do(a, map[string]any{"t": "feat_sync"})
	if len(kinds(drain(a), "feat_first")) != 0 {
		t.Fatal("acked: not sent again")
	}

	// Pace: a bucket of 10, one more every 2 s. A forged burst counts 10 at most, then nothing.
	got = r.add(b, "animal_love", 500)
	if num(got["n"]) != 10 {
		t.Fatalf("a report counts at most the burst: %v", got)
	}
	got = r.add(b, "animal_love", 1)
	if num(got["n"]) != 0 || got["reason"] != "pace" {
		t.Fatalf("then the pace: %v", got)
	}
	r.now = r.now.Add(4 * time.Second)
	if got = r.add(b, "animal_love", 5); num(got["n"]) != 2 {
		t.Fatalf("two more after 4 s: %v", got)
	}
	// Not today's Feat.
	if got = r.add(a, "tottlys_toys", 1); num(got["n"]) != 0 || got["reason"] != "not_today" {
		t.Fatalf("only today's Feat counts (feats_get): %v", got)
	}
	// Everyone hears the total, at most once a second.
	r.fire(time.Second)
	p := kinds(drain(a), "feat_progress")
	if len(p) != 1 || num(p[0]["total"]) != 15 || num(p[0]["count"]) != 2 {
		t.Fatalf("one progress for everyone: %v", p)
	}
	top := p[0]["top"].([]any)
	if top[0].(map[string]any)["name"] != "Bo" || num(top[0].(map[string]any)["n"]) != 12 {
		t.Fatalf("Bo leads: %v", top)
	}
}

func TestFeatActsFixTheAmountAndCountAStreetOnce(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "streaking")
	r := newFeatRig(t, "", nil, start.Add(time.Hour), nil)
	a := r.player(1, "Ada")
	drain(a)
	if got := r.add(a, "streaking", 40); num(got["n"]) != 0 || got["reason"] != "act" {
		t.Fatalf("a streak report names its act: %v", got)
	}
	if got := r.add(a, "streaking", 40, "act", "streak", "street", "GA5101HF7F429V5"); num(got["n"]) != 1 {
		t.Fatalf("a streak is 1 whatever the client says: %v", got)
	}
	r.now = r.now.Add(time.Minute)
	if got := r.add(a, "streaking", 1, "act", "streak", "street", "LA5101HF7F429V5"); got["reason"] != "already" {
		t.Fatalf("once per street per game day (location.js 1046-1050), its L twin too: %v", got)
	}
	if got := r.add(a, "streaking", 1, "act", "wheel"); num(got["n"]) != 5 {
		t.Fatalf("a Cubimal box is 5 (inc_skill_packages.js:54): %v", got)
	}
	r.now = r.now.Add(time.Duration(table.GameDayMs) * time.Millisecond)
	if got := r.add(a, "streaking", 1, "act", "streak", "street", "GA5101HF7F429V5"); num(got["n"]) != 1 {
		t.Fatalf("the next game day the street counts again: %v", got)
	}
}

// G172: bag_butler.js 3802-3805, +2 for the first package left with a Butler; each neighbour's
// HOME_<id> counts once a run.
func TestFeatGoodNeighborCountsEachButlerOnce(t *testing.T) {
	table := featTableForTest(t)
	if f := table.Feats["visiting_stones"]; f.Epic != 4 || f.Goal != 12 || f.Acts["package"].N != 2 || f.Acts["package"].Dedupe != "street" {
		t.Fatalf("The Good Neighbor: %+v", f)
	}
	start := featRunStart(t, table, "visiting_stones")
	r := newFeatRig(t, "", nil, start.Add(time.Hour), nil)
	a := r.player(1, "Ada")
	drain(a)
	if got := r.add(a, "visiting_stones", 9, "act", "package", "street", "HOME_7"); num(got["n"]) != 2 {
		t.Fatalf("a package is 2 whatever the client says: %v", got)
	}
	r.now = r.now.Add(time.Minute)
	if got := r.add(a, "visiting_stones", 2, "act", "package", "street", "HOME_7"); got["reason"] != "already" {
		t.Fatalf("the same Butler once a run: %v", got)
	}
	if got := r.add(a, "visiting_stones", 2, "act", "package", "street", "HOME_8"); num(got["n"]) != 2 {
		t.Fatalf("another neighbour's Butler counts: %v", got)
	}
}

func TestFeatGoalsRanksAndRewardsThroughTheLedger(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "tottlys_toys")
	led := newFakeFeatLedger()
	path := filepath.Join(t.TempDir(), "feats.json")
	r := newFeatRig(t, path, led, start.Add(2*time.Hour), nil)
	// 31 players: places 1-13 Large, 14-26 Medium and feats:top_26, 27-30 The Cigar, 31 only the Small rock.
	var cls []*Client
	for i := int64(1); i <= 31; i++ {
		cls = append(cls, r.player(i, fmt.Sprintf("P%d", i)))
	}
	for _, cl := range cls {
		drain(cl)
	}
	for i, cl := range cls {
		// Player 1 puts in the most; ties keep who got there first.
		n := int64(10)
		if i > 0 {
			n = int64(2)
		}
		if got := r.add(cl, "tottlys_toys", n); num(got["n"]) != float64(n) {
			t.Fatalf("player %d: %v", i+1, got)
		}
		r.now = r.now.Add(time.Second)
	}
	// 10 + 30 x 2 = 70: past the minimum (30) and the bonus (60), short of the supermegabonus (90).
	goals := kinds(drain(cls[0]), "feat_goal")
	if len(goals) != 2 || num(goals[0]["tier"]) != 1 || num(goals[1]["tier"]) != 2 {
		t.Fatalf("everyone hears each goal met: %v", goals)
	}
	for _, cl := range cls {
		drain(cl)
	}
	// The day ends.
	r.now = start.Add(24*time.Hour + time.Second)
	r.do(cls[0], map[string]any{"t": "feat_sync"})
	ended := kinds(drain(cls[0]), "feat_ended")
	if len(ended) != 1 || num(ended[0]["tier"]) != 2 || num(ended[0]["total"]) != 70 || num(ended[0]["count"]) != 31 {
		t.Fatalf("the Feat ended at its bonus goal: %v", ended)
	}
	r.fire(0)
	for _, cl := range cls {
		drain(cl)
	}
	if len(led.calls) != 31 {
		t.Fatalf("one ledger credit a contributor: %v", led.calls)
	}
	// Scale 70 / 30: 170 currants -> 397 (Math.round).
	if led.currants[1] != 397 || led.currants[31] != 397 {
		t.Fatalf("every contributor's currants in the ledger: %v", led.currants)
	}
	rewardOf := func(cl *Client) map[string]any {
		r.do(cl, map[string]any{"t": "feat_sync"})
		rw := kinds(drain(cl), "feat_reward")
		if len(rw) != 1 {
			t.Fatalf("one reward for %d: %v", cl.ID(), rw)
		}
		return rw[0]
	}
	first := rewardOf(cls[0])
	if num(first["place"]) != 1 || fmt.Sprint(first["items"]) != "[map[c:petrified_rock_small n:1] map[c:petrified_rock_large n:1]]" ||
		fmt.Sprint(first["counters"]) != "[map[counter:feats:contributed_to n:1] map[counter:feats:top_26 n:1]]" {
		t.Fatalf("first place: %v", first)
	}
	rw := first["rewards"].(map[string]any)
	if num(rw["xp"]) != 635 || num(rw["energy"]) != 238 || num(rw["currants"]) != 397 || fmt.Sprint(rw["favor"]) != "[map[giant:all points:79]]" {
		t.Fatalf("doBreakIt's reward times 70/30: %v", rw)
	}
	// petrified_rock_large.js:8: the Large rock goes to "the 13 who contributed the most awe".
	if p := rewardOf(cls[12]); num(p["place"]) != 13 || fmt.Sprint(p["items"]) != "[map[c:petrified_rock_small n:1] map[c:petrified_rock_large n:1]]" {
		t.Fatalf("place 13: %v", p)
	}
	if p := rewardOf(cls[13]); num(p["place"]) != 14 || fmt.Sprint(p["items"]) != "[map[c:petrified_rock_small n:1] map[c:petrified_rock_medium n:1]]" {
		t.Fatalf("place 14: %v", p)
	}
	if p := rewardOf(cls[27]); num(p["place"]) != 28 || fmt.Sprint(p["grants"]) != "[the_cigar]" || fmt.Sprint(p["counters"]) != "[map[counter:feats:contributed_to n:1]]" {
		t.Fatalf("place 28 gets The Cigar (inc_feats.js 142-143): %v", p)
	}
	if p := rewardOf(cls[30]); num(p["place"]) != 31 || len(p["grants"].([]any)) != 0 || len(p["items"].([]any)) != 1 {
		t.Fatalf("place 31: a Small rock only: %v", p)
	}
	// A restart keeps what is owed, and the ledger is never asked twice for a reward it paid.
	r2 := newFeatRig(t, path, led, r.now.Add(time.Minute), nil)
	a := r2.player(31, "P31")
	if rw := kinds(drain(a), "feat_reward"); len(rw) != 1 || num(rw[0]["place"]) != 31 {
		t.Fatalf("after a restart the reward is still owed: %v", rw)
	}
	if len(led.calls) != 31 {
		t.Fatalf("no second credit: %v", led.calls)
	}
}

func TestFeatRewardsWaitForTheLedgerAcrossARestart(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "tottlys_toys")
	led := newFakeFeatLedger()
	led.silent = true
	path := filepath.Join(t.TempDir(), "feats.json")
	r := newFeatRig(t, path, led, start.Add(time.Hour), nil)
	a := r.player(1, "Ada")
	for i := 0; i < 3; i++ {
		r.add(a, "tottlys_toys", 10)
		r.now = r.now.Add(30 * time.Second)
	}
	r.now = start.Add(25 * time.Hour)
	r.do(a, map[string]any{"t": "feat_sync"})
	r.fire(0)
	if len(kinds(drain(a), "feat_reward")) != 0 {
		t.Fatal("no reward before the ledger answers")
	}
	// Restart: the credit is asked again when Ada connects, and then she hears the reward.
	led.silent = false
	r2 := newFeatRig(t, path, led, r.now.Add(time.Minute), nil)
	a = r2.player(1, "Ada")
	r2.fire(0)
	if rw := kinds(drain(a), "feat_reward"); len(rw) != 1 || led.currants[1] != 170 {
		t.Fatalf("credited once the site answers (scale 1): %v %v", rw, led.currants)
	}
}

func TestFeatFirstCountsEpicsAndTheChainCountsItsOwn(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "makeup_feat_redux")
	r := newFeatRig(t, "", nil, start.Add(time.Hour), func(t *featTable) {
		// An epic of two: hi_signs_1 rewarded before, pet_only now completes it.
		t.Epics["4"] = 2
	})
	a := r.player(1, "Ada")
	drain(a)
	r.add(a, "makeup_feat_redux", 2)
	r.do(a, map[string]any{"t": "feat_sync"})
	f := kinds(drain(a), "feat_first")
	if len(f) != 1 || len(f[0]["counters"].([]any)) != 0 || len(f[0]["grants"].([]any)) != 0 {
		t.Fatalf("the chain counts feats:contributed itself (game.js), and an hour in is not quick: %v", f)
	}
	r.m.st.Rewarded[1] = []string{"hi_signs_1"}
	r.do(a, map[string]any{"t": "feat_dev", "feat": "pet_only"})
	drain(a)
	if got := r.add(a, "pet_only", 4); num(got["n"]) != 4 {
		t.Fatalf("pet_only: %v", got)
	}
	r.do(a, map[string]any{"t": "feat_sync"})
	var pet map[string]any
	for _, m := range kinds(drain(a), "feat_first") {
		if m["feat"] == "pet_only" {
			pet = m
		}
	}
	if pet == nil || fmt.Sprint(pet["counters"]) != "[map[counter:feats:contributed n:1] map[counter:epics_contributed:4 n:1] map[counter:epics_completed:4 n:1]]" || fmt.Sprint(pet["grants"]) != "[quick_on_your_feat]" {
		t.Fatalf("epics_contributed and epics_completed (inc_feats.js 55-69), quick after a dev start: %v", pet)
	}
}

func TestFeatOtherModulesCountThroughProvide(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "hi_signs_1")
	r := newFeatRig(t, "", nil, start.Add(time.Hour), nil)
	a := r.player(1, "Ada")
	drain(a)
	count, ok := r.hub.ProvidedLocked("feats.count").(func(int64, string, string, int64) int64)
	if !ok {
		t.Fatal("feats.count is provided")
	}
	r.hub.mu.Lock()
	n, other := count(1, "Ada", "hi_signs_1", 1), count(1, "Ada", "animal_love", 1)
	r.hub.mu.Unlock()
	if n != 1 || other != 0 || r.m.st.Run.Total != 1 {
		t.Fatalf("a hi sign seen by the server counts; another Feat's does not: %d %d", n, other)
	}
}

func TestFeatDevBringsTheEndNear(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "animal_love")
	r := newFeatRig(t, "", nil, start.Add(time.Hour), nil)
	a := r.player(1, "Ada")
	drain(a)
	r.do(a, map[string]any{"t": "feat_dev", "end_in": 30})
	st := kinds(drain(a), "feat_state")
	if len(st) != 1 || int64(num(st[0]["end"])) != r.now.Add(30*time.Second).UnixMilli() || st[0]["feat"] != "animal_love" {
		t.Fatalf("ends in 30 s: %v", st)
	}
	r.now = r.now.Add(31 * time.Second)
	r.fire(time.Minute)
	msgs := drain(a)
	if len(kinds(msgs, "feat_ended")) != 1 || kinds(msgs, "feat_state")[0]["feat"] != "makeup_feat_redux" {
		t.Fatalf("then the next Feat: %v", msgs)
	}
	r.m.dev = false
	r.do(a, map[string]any{"t": "feat_dev", "end_in": 30})
	if len(drain(a)) != 0 {
		t.Fatal("feat_dev is QA only")
	}
}
