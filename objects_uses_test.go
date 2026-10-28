package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeUseLedger is the site's ledger/use: each player's jars and bottles, five charges each.
type fakeUseLedger struct {
	held  map[int64]map[string]int // jars or bottles held
	left  map[int64]map[string]int // charges left in the open one (0: a full one)
	calls []string                 // op id c
	ops   map[string]ledgerAnswer
	down  bool
}

func newFakeUseLedger() *fakeUseLedger {
	return &fakeUseLedger{held: map[int64]map[string]int{}, left: map[int64]map[string]int{}, ops: map[string]ledgerAnswer{}}
}

func (f *fakeUseLedger) give(id int64, class string, n int) {
	if f.held[id] == nil {
		f.held[id], f.left[id] = map[string]int{}, map[string]int{}
	}
	f.held[id][class] += n
}

func (f *fakeUseLedger) use(op string, id int64, class string) ledgerAnswer {
	f.calls = append(f.calls, fmt.Sprintf("%s %d %s", op, id, class))
	if f.down {
		return ledgerAnswer{}
	}
	if a, ok := f.ops[op]; ok {
		return a
	}
	if f.held[id][class] < 1 {
		a := ledgerAnswer{Status: "refused", Short: &ledgerShort{ID: id, C: class}}
		f.ops[op] = a
		return a
	}
	left := f.left[id][class]
	if left == 0 {
		left = 5
	}
	left--
	spent := left == 0
	if spent {
		f.held[id][class]--
	}
	f.left[id][class] = left
	a := ledgerAnswer{Status: "settled", Left: &left, Spent: spent}
	f.ops[op] = a
	return a
}

// usesRig is an objects rig whose bottles and jars are the fake ledger's; ledger calls run in place.
func usesRig(t *testing.T) (*objRig, *fakeUseLedger) {
	r := newObjRig(t)
	l := newFakeUseLedger()
	r.mod.uses.ledger = l
	r.mod.uses.spawn = func(fn func()) { fn() }
	n := 0
	r.mod.uses.newOp = func() string { n++; return fmt.Sprintf("use-%016x", n) }
	return r, l
}

func TestTreePoisonIsConfirmedAndUsedThroughTheLedgerFirst(t *testing.T) {
	r, l := usesRig(t)
	spot := sharedWorld.streets[defaultStreet]["tree:0"]
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	b := r.player(2, "Bo", defaultStreet, spot.x+50, spot.y)
	l.give(1, usePoison, 1)

	// Bo holds no Tree Poison: refused with the ledger's words, and the tree stays as it was for everyone.
	r.act(b, 1, "tree:0", "poison", nil)
	r.advance(0)
	if no := r.only(b, "obj_no"); no["code"] != "no_item" || no["message"] != useTextLacks || no["verb"] != "poison" {
		t.Fatalf("a pour without the bottle answered %v", no)
	}
	if f := r.frames(a, "objs"); len(f) != 0 || r.mod.streets[defaultStreet]["tree:0"] != nil {
		t.Fatalf("the tree changed: %v", f)
	}
	// Ada holds one: one charge used (4 left in the bottle), then the poison, for everyone.
	r.now = r.now.Add(time.Second)
	r.act(a, 2, "tree:0", "poison", nil)
	if len(r.drain(a)) != 0 {
		t.Fatal("nothing is answered before the ledger")
	}
	r.advance(0)
	ok := r.only(a, "obj_ok")
	use, _ := ok["use"].(map[string]any)
	if num(ok["poison"]) != 180000 || use == nil || use["c"] != usePoison || num(use["left"]) != 4 || use["spent"] != false || !strings.HasPrefix(use["op"].(string), "use-") {
		t.Fatalf("the pour answered %v", ok)
	}
	if got := objOf(r.only(b, "objs"), "tree:0"); got == nil || num(got["poison"]) != 180000 || num(got["by"]) != 1 {
		t.Fatalf("Bo heard %v", got)
	}
	if len(l.calls) != 2 || !strings.HasSuffix(l.calls[1], " 1 "+usePoison) {
		t.Fatalf("ledger calls %v", l.calls)
	}

	// The antidote: none held is the source's own line; one held cures it for everyone.
	r.now = r.now.Add(time.Second)
	r.act(b, 3, "tree:0", "antidote", nil)
	r.advance(0)
	if no := r.only(b, "obj_no"); no["code"] != "no_item" || no["message"] != "You don't have any antidote!" {
		t.Fatalf("an antidote without one answered %v", no)
	}
	l.give(2, useAntidote, 1)
	r.now = r.now.Add(time.Second)
	r.act(b, 4, "tree:0", "antidote", nil)
	r.advance(0)
	if ok := r.only(b, "obj_ok"); ok["use"].(map[string]any)["c"] != useAntidote {
		t.Fatalf("the antidote answered %v", ok)
	}
	if got := objOf(r.only(a, "objs"), "tree:0"); got == nil || got["poison"] != nil {
		t.Fatalf("Ada heard the cure as %v", got)
	}
}

func TestAPourIsRefusedBeforeTheLedgerWhenTheTreeSaysNo(t *testing.T) {
	r, l := usesRig(t)
	spot := sharedWorld.streets[defaultStreet]["tree:0"]
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	l.give(1, useAntidote, 1)
	r.act(a, 1, "tree:0", "antidote", nil)
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "not_poisoned" {
		t.Fatalf("an antidote on a healthy tree answered %v", no)
	}
	r.mod.get(defaultStreet, spot).dead = true
	r.now = r.now.Add(time.Second)
	r.act(a, 2, "tree:0", "poison", nil)
	if no := r.only(a, "obj_no"); no["code"] != "dead" {
		t.Fatalf("a poison on a dead tree answered %v", no)
	}
	if len(l.calls) != 0 {
		t.Fatalf("no charge is asked for a pour that cannot land: %v", l.calls)
	}
}

func TestATreeIsHeldWhileAPourWaitsForTheLedger(t *testing.T) {
	r, l := usesRig(t)
	spot := sharedWorld.streets[defaultStreet]["tree:0"]
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	b := r.player(2, "Bo", defaultStreet, spot.x+50, spot.y)
	l.give(1, usePoison, 1)
	l.give(2, usePoison, 1)
	// The site has not answered Ada yet (the call is made, its answer waits for the hub).
	r.act(a, 1, "tree:0", "poison", nil)
	r.act(b, 1, "tree:0", "poison", nil)
	if no := r.only(b, "obj_no"); no["code"] != "busy" || no["message"] != useTextBusy {
		t.Fatalf("a second pour while the first waits answered %v", no)
	}
	// Ada's second use while her first waits: one at a time.
	r.now = r.now.Add(time.Second)
	r.send(a, map[string]any{"t": "obj_dust", "req": 2})
	if no := r.only(a, "obj_no"); no["code"] != "slow_down" {
		t.Fatalf("a second use while one waits answered %v", no)
	}
	r.advance(0)
	r.only(a, "obj_ok")
	if len(l.calls) != 1 {
		t.Fatalf("only Ada's charge was used: %v", l.calls)
	}
}

func TestNoAnswerFromTheLedgerChangesNothing(t *testing.T) {
	r, l := usesRig(t)
	spot := sharedWorld.streets[defaultStreet]["tree:0"]
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	l.give(1, usePoison, 1)
	l.down = true
	r.act(a, 1, "tree:0", "poison", nil)
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "no_answer" || no["message"] != useTextNoAnswer {
		t.Fatalf("no answer gave %v", no)
	}
	if ob := r.mod.streets[defaultStreet]["tree:0"]; ob != nil && !ob.poisonEnd.IsZero() {
		t.Fatal("the tree was poisoned without the ledger's word")
	}
}

func TestFertilidustIsUsedThroughTheLedgerAndTheTreesGrowAsItLands(t *testing.T) {
	r, l := usesRig(t)
	spots := sharedWorld.streets[defaultStreet]
	t3 := spots["tree:3"]
	a := r.player(1, "Ada", defaultStreet, t3.x, t3.y)
	b := r.player(2, "Bo", defaultStreet, 0, 0)
	r.drain(a)
	r.drain(b)

	// No jar in Bo's ledger: refused, and nothing grows, now or once the dust would have landed.
	r.send(b, map[string]any{"t": "obj_dust", "req": 1})
	r.advance(0)
	if no := r.only(b, "obj_no"); no["code"] != "no_item" || no["message"] != useTextLacks || no["verb"] != "dust" {
		t.Fatalf("a dust without the jar answered %v", no)
	}
	r.advance(dustLand)
	if f := r.frames(a, "objs"); len(f) != 0 {
		t.Fatalf("trees grew for a refused dust: %v", f)
	}

	// Ada's jar: one charge of five, then the street's trees grow dustLand later, for everyone.
	l.give(1, useFertilidust, 1)
	r.now = r.now.Add(time.Second)
	r.send(a, map[string]any{"t": "obj_dust", "req": 2})
	r.advance(0)
	used := r.only(a, "obj_dust_used")
	use, _ := used["use"].(map[string]any)
	if num(used["req"]) != 2 || used["c"] != useFertilidust || use == nil || num(use["left"]) != 4 || use["spent"] != false {
		t.Fatalf("the jar answered %v", used)
	}
	if f := r.frames(b, "objs"); len(f) != 0 {
		t.Fatal("the trees grew before the dust landed")
	}
	r.advance(dustLand)
	if done := r.only(a, "obj_dusted"); len(done["ids"].([]any)) < 2 {
		t.Fatalf("the dust grew %v", done)
	}
	if got := objOf(r.only(b, "objs"), "tree:3"); num(got["maturity"]) != 8 {
		t.Fatalf("Bo sees the dusted tree as %v", got)
	}

	// Four more empty the jar: the last says spent, and a sixth is refused.
	for i := 0; i < 4; i++ {
		r.now = r.now.Add(5 * time.Second)
		r.send(a, map[string]any{"t": "obj_dust", "req": 3 + i})
		r.advance(0)
		u := r.only(a, "obj_dust_used")["use"].(map[string]any)
		if num(u["left"]) != float64(3-i) || u["spent"] != (i == 3) {
			t.Fatalf("use %d: %v", i+2, u)
		}
		r.advance(dustLand)
		r.drain(a)
		r.drain(b)
	}
	r.now = r.now.Add(5 * time.Second)
	r.send(a, map[string]any{"t": "obj_dust", "req": 9})
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "no_item" {
		t.Fatalf("a sixth dust from one jar answered %v", no)
	}

	// The Lite is its own class: the one tree near you.
	l.give(1, useFertilidustLite, 1)
	r.now = r.now.Add(5 * time.Second)
	r.send(a, map[string]any{"t": "obj_dust", "req": 10, "id": "tree:3"})
	r.advance(0)
	if used := r.only(a, "obj_dust_used"); used["c"] != useFertilidustLite {
		t.Fatalf("the Lite answered %v", used)
	}
	if last := l.calls[len(l.calls)-1]; !strings.HasSuffix(last, " 1 "+useFertilidustLite) {
		t.Fatalf("the Lite asked %q", last)
	}
	// A Lite on a tree across the street is refused before anything is used.
	calls := len(l.calls)
	r.now = r.now.Add(5 * time.Second)
	r.send(b, map[string]any{"t": "obj_dust", "req": 11, "id": "tree:3"})
	if no := r.only(b, "obj_no"); no["code"] != "far" || len(l.calls) != calls {
		t.Fatalf("a far Lite answered %v (%d calls)", no, len(l.calls)-calls)
	}
}

// —— Connected time (credits.go) ——

// onlineRig is a hub with the credits module only, its outbox caught, on a clock the test moves.
type onlineRig struct {
	hub    *Hub
	mod    *credits
	now    time.Time
	timers []*objTimer
}

func newOnlineRig(t *testing.T) *onlineRig {
	r := &onlineRig{now: time.Unix(1_800_000_000, 0)}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module {
		r.mod = newCredits(h).(*credits)
		r.mod.out = newCreditOutbox(&fakeCreditSite{answer: allDone})
		r.mod.boot = "0a1b2c3d"
		return r.mod
	}}
	defer func() { modules = saved }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	return r
}

func (r *onlineRig) advance(d time.Duration) {
	end := r.now.Add(d)
	for {
		var next *objTimer
		for _, tm := range r.timers {
			if !tm.done && !tm.at.After(end) && (next == nil || tm.at.Before(next.at)) {
				next = tm
			}
		}
		if next == nil {
			r.now = end
			return
		}
		r.now = next.at
		next.done = true
		next.fn()
	}
}

// spans is the outbox's online events as user:from-to, seconds after the rig's start.
func (r *onlineRig) spans() []string {
	var out []string
	for _, ev := range r.mod.out.queue {
		if ev.Why == "online" {
			out = append(out, fmt.Sprintf("%d:%d-%d", ev.User, ev.From-1_800_000_000, ev.To-1_800_000_000))
		}
	}
	return out
}

func TestConnectedTimeGoesToTheSiteEachMinuteAndOnLeaving(t *testing.T) {
	r := newOnlineRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.advance(30 * time.Second)
	r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.advance(30 * time.Second) // the first minute's tick
	if got := strings.Join(r.spans(), " "); got != "1:0-60 2:30-60" && got != "2:30-60 1:0-60" {
		t.Fatalf("after a minute: %s", got)
	}
	// A new socket for Ada is the same session; going offline sends what is left.
	ada = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.advance(20 * time.Second)
	r.hub.Disconnect(ada)
	spans := r.spans()
	if last := spans[len(spans)-1]; last != "1:60-80" || len(spans) != 3 {
		t.Fatalf("Ada's leaving sent %v", spans)
	}
	// Nobody connected, nothing sent: an hour of nobody playing earns nothing.
	r.hub.Disconnect(r.hub.ClientLocked(2))
	n := len(r.spans())
	r.advance(time.Hour)
	if len(r.spans()) != n {
		t.Fatalf("time went to the site while nobody was connected: %v", r.spans()[n:])
	}
	ids := map[string]bool{}
	for _, ev := range r.mod.out.queue {
		if ev.Why != "online" || !strings.HasPrefix(ev.ID, "cr-o-0a1b2c3d-") || len(ev.ID) > 40 || ids[ev.ID] || ev.Got.Items == nil || ev.At != ev.To {
			t.Fatalf("event %+v", ev)
		}
		ids[ev.ID] = true
	}
}

func TestConnectedTimeIsSentOnShutdownAndKeptInTheJournal(t *testing.T) {
	r := newOnlineRig(t)
	path := filepath.Join(t.TempDir(), "credits.jsonl")
	if err := r.mod.out.journalled(path); err != nil {
		t.Fatal(err)
	}
	r.hub.Connect(Claims{Sub: 7, Name: "Cy"})
	r.advance(45 * time.Second)
	// A blink (under onlineMin) is not sent.
	blink := r.hub.Connect(Claims{Sub: 8, Name: "Di"})
	r.now = r.now.Add(500 * time.Millisecond)
	r.hub.Disconnect(blink)
	r.hub.Shutdown()
	if got := strings.Join(r.spans(), " "); got != "7:0-45" {
		t.Fatalf("on shutdown: %s", got)
	}
	// A restart replays it, interval and all.
	o := journalledOutbox(t, &fakeCreditSite{answer: down}, path)
	if o.pending() != 1 || o.queue[0].Why != "online" || o.queue[0].To-o.queue[0].From != 45 || o.queue[0].User != 7 {
		t.Fatalf("after a restart: %+v", o.queue)
	}
	// The module's add never sends an online event of its own making.
	if r.mod.add(7, "online", tradeGoods{Currants: 1}) != "" {
		t.Fatal("online went through add")
	}
}
