package main

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProjectLedger is the site's ledger: items and currants per player, each id answered once
// and the same again after (src/Ledger.php project). silent: no answer at all.
type fakeProjectLedger struct {
	mu       sync.Mutex
	items    map[int64]map[string]int64
	currants map[int64]int64
	seen     map[string]string
	calls    []string
	silent   bool
}

func newFakeProjectLedger() *fakeProjectLedger {
	return &fakeProjectLedger{items: map[int64]map[string]int64{}, currants: map[int64]int64{}, seen: map[string]string{}}
}

func (f *fakeProjectLedger) project(id string, user int64, gave, got tradeGoods) ledgerAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	if f.silent {
		return ledgerAnswer{}
	}
	if s, ok := f.seen[id]; ok {
		return ledgerAnswer{Status: s}
	}
	if f.items[user] == nil {
		f.items[user] = map[string]int64{}
	}
	for _, it := range gave.Items {
		if f.items[user][it.C] < int64(it.N) {
			f.seen[id] = "void"
			return ledgerAnswer{Status: "refused", Short: &ledgerShort{ID: user, C: it.C}}
		}
	}
	if f.currants[user] < gave.Currants {
		f.seen[id] = "void"
		return ledgerAnswer{Status: "refused", Short: &ledgerShort{ID: user, Currants: true}}
	}
	for _, it := range gave.Items {
		f.items[user][it.C] -= int64(it.N)
	}
	f.currants[user] -= gave.Currants
	for _, it := range got.Items {
		f.items[user][it.C] += int64(it.N)
	}
	f.currants[user] += got.Currants
	f.seen[id] = "settled"
	return ledgerAnswer{Status: "settled"}
}

type projRig struct {
	t      *testing.T
	hub    *Hub
	m      *projects
	led    *fakeProjectLedger
	now    time.Time
	timers []*rigTimer
}

const (
	projJobID  = "62"              // Build Lowland Slough: one phase, job_standard4_for_new_streets
	projBoard  = "GLI57DI72HI153U" // its board's street
	projBuilds = "GLI4HJ9GRLI1C4E"
)

func newProjRig(t *testing.T, path string, led *fakeProjectLedger, now time.Time) *projRig {
	r := &projRig{t: t, led: led, now: now}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module {
		var table projTable
		if err := json.Unmarshal(projectsJSON, &table); err != nil {
			t.Fatal(err)
		}
		r.m = newProjectsWith(h, table, path)
		r.m.ledger = led
		r.m.spawn = func(fn func()) { fn() }
		r.m.rng = func() float64 { return 0.01 } // the first line of every drop table
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

// settle runs the ledger answers waiting (h.After(0)).
func (r *projRig) settle() {
	for i := 0; i < 20; i++ {
		fired := false
		for _, tm := range append([]*rigTimer{}, r.timers...) {
			if tm.d == 0 && !tm.stopped {
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

func (r *projRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *projRig) player(id int64, name, street string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.do(cl, map[string]any{"t": "join", "street": street})
	r.do(cl, map[string]any{"t": "project_sync"})
	return cl
}

func lastRow(t *testing.T, msgs []map[string]any, job string) map[string]any {
	t.Helper()
	var row map[string]any
	for _, m := range kinds(msgs, "project_state") {
		if m["job"] == job {
			row = m["row"].(map[string]any)
		}
	}
	if row == nil {
		t.Fatalf("no project_state for %s in %v", job, msgs)
	}
	return row
}

func (r *projRig) reqs() []projReq {
	job := r.m.t.Jobs[projJobID]
	cls, _ := r.m.t.class(job, 0)
	return cls.Reqs
}

func TestProjectTableComesFromTheGame(t *testing.T) {
	var table projTable
	if err := json.Unmarshal(projectsJSON, &table); err != nil {
		t.Fatal(err)
	}
	job := table.Jobs[projJobID]
	if job.Kind != "street" || job.Street != projBuilds || len(job.Offer) != 1 || job.Offer[0] != projBoard || len(job.Phases) != 1 {
		t.Fatalf("job 62 builds Lowland Slough from its board: %+v", job)
	}
	cls, ok := table.class(job, 0)
	if !ok || cls.Cut != 10 || cls.Pct != 0 || cls.Rewards.XP != 30000 || len(cls.Perf.Drops) != 2 {
		t.Fatalf("job_standard4_for_new_streets: %+v", cls)
	}
	need := map[string]int64{}
	for _, q := range cls.Reqs {
		need[q.ID] = q.Need
	}
	// soloNeed: ceil(num / 50), at least 1 (js/street-jobs.js): ginger 500 -> 10, currants 20,000 -> 400.
	if need["r133"] != 10 || need["r136"] != 400 || need["r138"] != 6 {
		t.Fatalf("remake amounts: %v", need)
	}
	if table.WorkUnitMs != 1500 || table.CooldownMs != 1_800_000 || table.Divisor != 50 {
		t.Fatalf("pace, rest and divisor: %d %d %d", table.WorkUnitMs, table.CooldownMs, table.Divisor)
	}
}

func TestProjectGivesCountOnlyWhatTheLedgerTook(t *testing.T) {
	led := newFakeProjectLedger()
	led.items[1] = map[string]int64{"ginger": 30}
	led.currants[1] = 100
	r := newProjRig(t, "", led, time.Unix(1_700_000_000, 0))
	a := r.player(1, "Ada", projBoard)
	b := r.player(2, "Bo", projBoard)
	c := r.player(3, "Cy", "GOTHER")
	street := kinds(drain(a), "project_street")
	if len(street) != 1 || street[0]["jobs"].(map[string]any)[projJobID] == nil {
		t.Fatalf("a join tells the street's projects: %v", street)
	}
	drain(b)
	drain(c)

	// Ada gives 25 ginger: the requirement needs 10, so 10 are asked of the ledger and held meanwhile.
	led.silent = true
	r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": "r133", "n": 25})
	row := lastRow(t, drain(b), projJobID)
	if num(row["held"].(map[string]any)["r133"]) != 10 || len(row["got"].(map[string]any)) != 0 {
		t.Fatalf("held while the ledger is asked: %v", row)
	}
	r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": "r134", "n": 1})
	if got := errorsOf(drain(a)); len(got) != 1 || got[0] != projectTextCounting {
		t.Fatalf("one contribution at a time: %v", got)
	}
	r.settle() // no answer: asked again later
	led.silent = false
	r.now = r.now.Add(2 * time.Second)
	for _, tm := range r.timers {
		if tm.d > 0 && !tm.stopped {
			tm.stopped = true
			tm.fn()
		}
	}
	r.settle()
	ga := drain(a)
	given := kinds(ga, "project_given")
	if len(given) != 1 || num(given[0]["items"].(map[string]any)["ginger"]) != 10 || num(given[0]["cost"]) != 40 {
		t.Fatalf("Ada hears what was taken: %v", ga)
	}
	if led.items[1]["ginger"] != 20 {
		t.Fatalf("the ledger took 10, has %d", led.items[1]["ginger"])
	}
	row = lastRow(t, drain(b), projJobID)
	if num(row["got"].(map[string]any)["r133"]) != 10 || len(row["held"].(map[string]any)) != 0 || num(row["count"]) != 1 {
		t.Fatalf("Bo sees the board fill: %v", row)
	}
	if len(kinds(drain(c), "project_state")) != 0 {
		t.Fatal("a player on another street hears nothing")
	}

	// A forged give: Bo's ledger holds no cheese.
	r.do(b, map[string]any{"t": "project_give", "job": projJobID, "req": "r134", "n": 5})
	r.settle()
	if got := errorsOf(drain(b)); len(got) != 1 || got[0] != projectTextNoItem {
		t.Fatalf("refused with the source's words (main.js:6157): %v", got)
	}
	row = lastRow(t, drain(a), projJobID)
	if row["got"].(map[string]any)["r134"] != nil || len(row["held"].(map[string]any)) != 0 {
		t.Fatalf("nothing counted for a refusal: %v", row)
	}
	// Currants Ada lacks, then what she has.
	r.now = r.now.Add(time.Second)
	r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": "r136", "currants": 400})
	r.settle()
	if got := errorsOf(drain(a)); len(got) != 1 || got[0] != projectTextNotTaken {
		t.Fatalf("currants refused (main.js:6179): %v", got)
	}
	r.now = r.now.Add(time.Second)
	r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": "r136", "currants": 60})
	r.settle()
	if g := kinds(drain(a), "project_given"); len(g) != 1 || num(g[0]["currants"]) != 60 || led.currants[1] != 40 {
		t.Fatalf("60 currants given: %v (ledger %d)", g, led.currants[1])
	}
	// Elsewhere, a full requirement, an unknown one.
	r.now = r.now.Add(time.Second)
	r.do(c, map[string]any{"t": "project_give", "job": projJobID, "req": "r133", "n": 1})
	r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": "r133", "n": 1})
	r.now = r.now.Add(time.Second)
	r.do(a, map[string]any{"t": "project_give", "job": "nope", "req": "r133", "n": 1})
	if got := errorsOf(append(drain(c), drain(a)...)); strings.Join(got, "|") != projectTextNotHere+"|"+projectTextNoNeed+"|"+projectTextNoJob {
		t.Fatalf("refusals: %q", got)
	}
}

func TestProjectWorkKeepsTheSourcePace(t *testing.T) {
	r := newProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	r.m.ledger = nil
	r.m.trusted = true
	a := r.player(1, "Ada", projBoard)
	drain(a)
	// r138 (group 2) opens once bucket 1's group 1 is done.
	r.do(a, map[string]any{"t": "project_work", "job": projJobID, "req": "r138"})
	if got := errorsOf(drain(a)); len(got) != 1 || got[0] != projectTextEarlier {
		t.Fatalf("the group gate: %v", got)
	}
	for _, q := range r.reqs() {
		if q.B == "1" && q.G == 1 {
			r.now = r.now.Add(time.Second)
			r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": q.ID, "n": q.Need, "currants": q.Need})
		}
	}
	drain(a)
	r.do(a, map[string]any{"t": "project_work", "job": projJobID, "req": "r138"})
	r.do(a, map[string]any{"t": "project_work", "job": projJobID, "req": "r138"})
	msgs := drain(a)
	if len(kinds(msgs, "project_worked")) != 1 || strings.Join(errorsOf(msgs), "") != projectTextPace {
		t.Fatalf("one unit, then the pace: %v", msgs)
	}
	for i := 0; i < 5; i++ {
		r.now = r.now.Add(1400 * time.Millisecond) // a little early each time: within the slack, once
		r.do(a, map[string]any{"t": "project_work", "job": projJobID, "req": "r138"})
	}
	msgs = drain(a)
	if n := len(kinds(msgs, "project_worked")); n != 4 {
		t.Fatalf("the pace holds over time (4 of 5 early units count): %d %v", n, errorsOf(msgs))
	}
}

// fillPhase puts everything job 62 needs in: Ada most of the items, Bo the currants and the work.
func fillPhase(r *projRig, a, b *Client) {
	for _, q := range r.reqs() {
		who := a
		if q.Type == "currants" || q.ID == "r143" {
			who = b
		}
		if q.Type == "work" {
			continue
		}
		r.now = r.now.Add(time.Second)
		r.do(who, map[string]any{"t": "project_give", "job": projJobID, "req": q.ID, "n": q.Need, "currants": q.Need})
		r.settle()
	}
	for _, q := range r.reqs() {
		if q.Type != "work" {
			continue
		}
		for i := int64(0); i < q.Need; i++ {
			r.now = r.now.Add(1500 * time.Millisecond)
			r.do(b, map[string]any{"t": "project_work", "job": projJobID, "req": q.ID})
			r.settle()
		}
	}
}

func seedLedger(led *fakeProjectLedger, cls projClass) {
	for _, id := range []int64{1, 2} {
		led.items[id] = map[string]int64{}
		led.currants[id] = 10_000
		for _, q := range cls.Reqs {
			if q.Type == "item" {
				led.items[id][q.C] = q.Need
			}
		}
	}
}

func TestProjectPhaseSharesAndTheRoad(t *testing.T) {
	led := newFakeProjectLedger()
	r := newProjRig(t, filepath.Join(t.TempDir(), "projects.json"), led, time.Unix(1_700_000_000, 0))
	cls, _ := r.m.t.class(r.m.t.Jobs[projJobID], 0)
	seedLedger(led, cls)
	a := r.player(1, "Ada", projBoard)
	b := r.player(2, "Bo", projBoard)
	c := r.player(3, "Cy", "GOTHER")
	drain(a)
	drain(b)
	drain(c)
	fillPhase(r, a, b)

	// Base cost each (quest.js addPlayerContribution) and the phase total (getTotalBasecost).
	var total, bo int64
	for _, q := range cls.Reqs {
		total += q.Need * q.Cost
		if q.Type != "item" || q.ID == "r143" {
			bo += q.Need * q.Cost
		}
	}
	ada := total - bo
	ga, gb := drain(a), drain(b)
	ra, rb := kinds(ga, "project_reward"), kinds(gb, "project_reward")
	if len(ra) != 1 || len(rb) != 1 {
		t.Fatalf("one reward each: %v / %v", ra, rb)
	}
	for _, x := range []struct {
		msg  map[string]any
		mine int64
	}{{ra[0], ada}, {rb[0], bo}} {
		share := float64(x.mine) / float64(total)
		// players/inc_jobs.js 239: my_contribution / getTotalBasecost; quest.js getRewards: Math.round(reward * multiplier); the remake's /50.
		wantXP := math.Floor(30000*share/50 + 0.5)
		rw := x.msg["rewards"].(map[string]any)
		if math.Abs(num(x.msg["share"])-share) > 1e-9 || num(rw["xp"]) != wantXP || num(rw["mood"]) != math.Floor(10000*share/50+0.5) {
			t.Fatalf("share %v xp %v, want %v %v: %v", x.msg["share"], rw["xp"], share, wantXP, x.msg)
		}
		// performance_cutoff 10: both are winners and in the top five; the first line of each drop table (rng 0.01).
		perf := x.msg["performance"].(map[string]any)
		if x.msg["winner"] != true || x.msg["top5"] != true || num(perf["xp"]) != 200 || len(perf["items"].([]any)) != 2 || x.msg["final"] != true {
			t.Fatalf("winner rewards: %v", x.msg)
		}
	}
	// The items went into each ledger once (gem_diamond and a trophy piece), under ids naming the phase and player.
	if led.items[1]["gem_diamond"] != 1 || led.items[2]["gem_diamond"] != 1 {
		t.Fatalf("credited: %v %v", led.items[1], led.items[2])
	}
	id := ra[0]["id"].(string)
	if !strings.HasPrefix(id, "job-r-62-0-") || !strings.HasSuffix(id, "-1") || !projIDOK(id) {
		t.Fatalf("reward id %q", id)
	}
	// Everyone online hears the road is open; the board's street rests 30 minutes.
	built := kinds(drain(c), "project_built")
	if len(built) != 1 || built[0]["job"] != projJobID || len(kinds(ga, "project_built")) != 1 {
		t.Fatalf("project_built to everyone: %v", built)
	}
	if r.m.st.Rest[projBoard] == 0 || r.m.st.Jobs[projJobID].Done == 0 {
		t.Fatal("done and resting")
	}
	r.now = r.now.Add(time.Second)
	r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": "r133", "n": 1})
	if got := errorsOf(drain(a)); len(got) != 1 || got[0] != projectTextDone {
		t.Fatalf("a finished project: %v", got)
	}
	// A reconnect hears the world and the reward until acked.
	a2 := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.do(a2, map[string]any{"t": "project_sync"})
	msgs := drain(a2)
	world := kinds(msgs, "project_world")
	if len(world) != 1 || world[0]["built"].(map[string]any)[projJobID] == nil || len(kinds(msgs, "project_reward")) != 1 {
		t.Fatalf("on connect: %v", msgs)
	}
	r.do(a2, map[string]any{"t": "project_ack", "id": id})
	a3 := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.do(a3, map[string]any{"t": "project_sync"})
	if len(kinds(drain(a3), "project_reward")) != 0 {
		t.Fatal("an acked reward is not sent again")
	}
}

func TestProjectRestartLosesAndDoublesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	led := newFakeProjectLedger()
	start := time.Unix(1_700_000_000, 0)
	r := newProjRig(t, path, led, start)
	cls, _ := r.m.t.class(r.m.t.Jobs[projJobID], 0)
	seedLedger(led, cls)
	a := r.player(1, "Ada", projBoard)
	drain(a)
	r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": "r133", "n": 10})
	r.settle()
	// The site takes the next one but its answer is lost (the server goes down before it lands).
	r.now = r.now.Add(time.Second)
	led.silent = true
	r.do(a, map[string]any{"t": "project_give", "job": projJobID, "req": "r134", "n": 5})
	r.settle()
	led.silent = false
	pending := ""
	for id := range r.m.st.Gives {
		pending = id
	}
	led.project(pending, 1, tradeGoods{Items: []tradeItem{{C: "cheese_very_very_stinky", N: 5}}}, tradeGoods{})
	drain(a)

	// Restart from the file.
	r2 := newProjRig(t, path, led, start.Add(time.Minute))
	if g := r2.m.st.Gives[pending]; g == nil || r2.m.st.Jobs[projJobID].held["r134"] != 5 || r2.m.st.Jobs[projJobID].Got["r133"] != 10 {
		t.Fatalf("the give and the board survive: %+v", r2.m.st)
	}
	a = r2.player(1, "Ada", projBoard)
	r2.settle()
	msgs := drain(a)
	given := kinds(msgs, "project_given")
	// The first give was never acked (sent again), the second is asked again with its id and counts once.
	if len(given) != 2 || r2.m.st.Jobs[projJobID].Got["r134"] != 5 || len(r2.m.st.Gives) != 0 {
		t.Fatalf("after the restart: given %v got %v", given, r2.m.st.Jobs[projJobID].Got)
	}
	if led.items[1]["cheese_very_very_stinky"] != 0 {
		t.Fatalf("taken once: %d left", led.items[1]["cheese_very_very_stinky"])
	}
	row := r2.m.st.Jobs[projJobID]
	if len(row.Per) != 1 || row.Per[0].Mine[0].Cost != 10*4+5*33 {
		t.Fatalf("Ada's base cost, once each: %+v", row.Per)
	}
	for _, g := range given {
		r2.do(a, map[string]any{"t": "project_ack", "id": g["id"]})
	}
	r3 := newProjRig(t, path, led, start.Add(2*time.Minute))
	a = r3.player(1, "Ada", projBoard)
	if len(kinds(drain(a), "project_given")) != 0 {
		t.Fatal("acks are kept across a restart")
	}
}

func TestProjectUpgradeTakesTheMostVotedOption(t *testing.T) {
	job := projJob{Kind: "upgrade", Options: []string{"13", "14"}}
	if winningOption(job, map[string]int64{"13": 5, "14": 9}) != "14" || winningOption(job, map[string]int64{"13": 5, "14": 5}) != "13" || winningOption(job, nil) != "13" {
		t.Fatal("quest.js getWinningOption: most base cost, the first of a tie")
	}
	r := newProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	if r.m.option(r.m.t.Jobs["16"], "nope") != r.m.t.Jobs["16"].Options[0] || r.m.option(r.m.t.Jobs[projJobID], "13") != "" {
		t.Fatal("an unknown option backs the first; a street job has none")
	}
}

func TestProjectCooldownAfterAStreetIsBuilt(t *testing.T) {
	r := newProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	r.m.ledger = nil
	r.m.trusted = true
	// A board street offering two street-building jobs.
	var street, first, second string
	for s, ids := range r.m.offer {
		var streets []string
		for _, id := range ids {
			if r.m.t.Jobs[id].Kind == "street" {
				streets = append(streets, id)
			}
		}
		if len(streets) >= 2 {
			street, first, second = s, streets[0], streets[1]
			break
		}
	}
	if street == "" {
		t.Skip("no street offers two street jobs")
	}
	r.m.row(first).Done = r.m.ms()
	r.m.st.Rest[street] = r.m.ms()
	a := r.player(1, "Ada", street)
	drain(a)
	// An item or currants requirement that is open now (a work one would answer "Nothing here needs that").
	var q reqView
	for _, v := range r.m.reqs(r.m.t.Jobs[second], nil) {
		if v.open && (v.Type == "item" || v.Type == "currants") {
			q = v
			break
		}
	}
	if q.ID == "" {
		t.Skip("no open item or currants requirement")
	}
	r.do(a, map[string]any{"t": "project_give", "job": second, "req": q.ID, "n": 1, "currants": 1})
	if got := errorsOf(drain(a)); len(got) != 1 || got[0] != projectTextCooldown {
		t.Fatalf("locations/inc_jobs.js: 30 minutes: %v", got)
	}
	r.now = r.now.Add(31 * time.Minute)
	r.do(a, map[string]any{"t": "project_give", "job": second, "req": q.ID, "n": 1, "currants": 1})
	if got := errorsOf(drain(a)); len(got) != 0 {
		t.Fatalf("open again after the rest: %v", got)
	}
}
