package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// crownRig is a hub with only the crowns module, a manual clock and manual timers.
type crownRig struct {
	t      *testing.T
	hub    *Hub
	m      *crowns
	now    time.Time
	timers []*rigTimer
	ids    int
	led    *fakeCrownsLedger
}

type fakeCrownsLedger struct {
	mu     sync.Mutex
	calls  []string
	answer string
}

func (f *fakeCrownsLedger) crowns(id string, user int64, got tradeGoods) ledgerAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%s %d %d", id, user, got.Currants))
	return ledgerAnswer{Status: f.answer}
}

func newCrownRig(t *testing.T) *crownRig {
	r := &crownRig{t: t, now: time.Unix(1_700_000_000, 0), led: &fakeCrownsLedger{answer: "settled"}}
	a, err := parseCrownsArena(crownsJSON)
	if err != nil {
		t.Fatal(err)
	}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module {
		r.m = newCrownsWith(h, a)
		r.m.newID = func() string { r.ids++; return fmt.Sprintf("%016x", r.ids) }
		r.m.pick = func(n int) int { return 0 }
		r.m.spawn = func(fn func()) { fn() }
		r.m.ledger = r.led
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

func (r *crownRig) fire(d time.Duration) int {
	n := 0
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if tm.d == d && !tm.stopped {
			tm.stopped = true
			tm.fn()
			n++
		}
	}
	return n
}

// tick moves the clock on by one server tick and runs it.
func (r *crownRig) tick() {
	r.now = r.now.Add(crownsTickEvery)
	r.fire(crownsTickEvery)
	r.fire(0) // ledger answers
}

func (r *crownRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *crownRig) at(cl *Client, street string, x, y float64) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": x, "y": y})
}

func (r *crownRig) put(cl *Client, x, y float64) {
	// A fresh move window each time (the hub allows 20 a second).
	cl.moveWindow, cl.moveCount = time.Time{}, 0
	r.do(cl, map[string]any{"t": "move", "x": x, "y": y})
}

var crownNames = []string{"Ada", "Bo", "Cy", "Di", "Ed"}

// four brings Ada, Bo, Cy and Di onto GA1 and through a filled challenge into one match.
func (r *crownRig) four() ([]*Client, map[string]any) {
	var cls []*Client
	for i := 0; i < 4; i++ {
		cl := r.hub.Connect(Claims{Sub: int64(i + 1), Name: crownNames[i]})
		r.at(cl, "GA1", float64(i*50), 0)
		cls = append(cls, cl)
	}
	for _, cl := range cls {
		drain(cl)
	}
	r.do(cls[0], map[string]any{"t": "crown_challenge"})
	w := one(r.t, drain(cls[0]), "crown_waiting")
	if w["need"].(float64) != 3 || w["text"] != "Waiting for other players..." {
		r.t.Fatalf("waiting %v", w)
	}
	ch := one(r.t, drain(cls[1]), "crown_challenged")
	if ch["text"] != "Ada is looking for a challenger for Game of Crowns." {
		r.t.Fatalf("challenge text %v", ch["text"])
	}
	drain(cls[2])
	drain(cls[3])
	r.do(cls[1], map[string]any{"t": "crown_accept", "from": 1})
	if w := one(r.t, drain(cls[1]), "crown_waiting"); w["need"].(float64) != 2 {
		r.t.Fatalf("bo waits for 2 more: %v", w)
	}
	r.do(cls[2], map[string]any{"t": "crown_accept", "from": 1})
	r.do(cls[3], map[string]any{"t": "crown_accept", "from": 1})
	st := one(r.t, drain(cls[0]), "crown_starting")
	if st["text"] != "Starting Game of Crowns between Ada, Bo, Cy and Di in 5 seconds!" {
		r.t.Fatalf("starting %v", st["text"])
	}
	for _, cl := range cls[1:] {
		drain(cl)
	}
	if r.fire(crownsStartDelay) != 1 {
		r.t.Fatal("the start waits 5 s")
	}
	var match map[string]any
	for i, cl := range cls {
		mm := one(r.t, drain(cl), "crown_match")
		if int(mm["slot"].(float64)) != i {
			r.t.Fatalf("slot %v", mm["slot"])
		}
		if mm["playLine"].(float64) != 610 {
			r.t.Fatalf("play line %v", mm["playLine"])
		}
		match = mm
	}
	return cls, match
}

// play takes the four through Click to Start and the countdown to GO; Ada (pick 0) holds the crown.
func (r *crownRig) play(cls []*Client, match map[string]any) (string, string) {
	id, street := match["match"].(string), match["street"].(string)
	starts := r.m.a.Starts
	for i, cl := range cls {
		r.at(cl, street, starts[i][0], starts[i][1])
	}
	for _, cl := range cls {
		drain(cl)
	}
	r.do(cls[0], map[string]any{"t": "crown_ready", "match": id})
	rs := one(r.t, drain(cls[1]), "crown_ready_state")
	if rs["text"] != "Ada is ready to play. Need 2 more." {
		r.t.Fatalf("ready text %v", rs["text"])
	}
	r.do(cls[1], map[string]any{"t": "crown_ready", "match": id})
	r.do(cls[2], map[string]any{"t": "crown_ready", "match": id})
	rs = kinds(drain(cls[1]), "crown_ready_state")[1]
	if rs["text"] != "Cy is ready to play. Just waiting on a few more slackers to HIT THE START BUTTON." {
		r.t.Fatalf("slackers %v", rs["text"])
	}
	r.do(cls[3], map[string]any{"t": "crown_ready", "match": id})
	soon := one(r.t, drain(cls[0]), "crown_soon")
	if soon["text"] != "YAY! THE GAME IS STARTING SOON." {
		r.t.Fatalf("soon %v", soon)
	}
	r.fire(crownsSoon)
	if c := one(r.t, drain(cls[0]), "crown_countdown"); c["in"].(float64) != 4000 {
		r.t.Fatalf("countdown %v", c)
	}
	r.fire(4000 * time.Millisecond)
	g := one(r.t, drain(cls[0]), "crown_go")
	if g["it"].(float64) != 1 || g["clock"] != false {
		r.t.Fatalf("go %v", g)
	}
	for _, cl := range cls[1:] {
		one(r.t, drain(cl), "crown_go")
	}
	return id, street
}

func TestCrownsChallengeNeedsThreeOthers(t *testing.T) {
	r := newCrownRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.at(ada, "GA1", 0, 0)
	drain(ada)
	r.do(ada, map[string]any{"t": "crown_challenge"})
	if f := one(t, drain(ada), "crown_refused"); f["why"] != "alone" || f["text"] != "You need at least one other player here." {
		t.Fatalf("alone %v", f)
	}
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.at(bo, "GA1", 0, 0)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "crown_challenge"})
	r.do(bo, map[string]any{"t": "crown_accept", "from": 1})
	drain(bo)
	// Nobody else came: the challenge runs out and the ticket comes back (games_invite_timeout).
	r.now = r.now.Add(crownsInviteTimeout)
	r.fire(crownsInviteTimeout)
	f := one(t, drain(ada), "crown_failed")
	if f["refund"] != true || f["text"] != "Not enough players accepted your challenge. Try again later?" {
		t.Fatalf("expired %v", f)
	}
	if f := one(t, drain(bo), "crown_failed"); f["refund"] != false {
		t.Fatalf("an accepter gets nothing back %v", f)
	}
}

func TestCrownsLateAccepterAndPrivateArena(t *testing.T) {
	r := newCrownRig(t)
	cls, match := r.four()
	ed := r.hub.Connect(Claims{Sub: 5, Name: "Ed"})
	r.at(ed, match["street"].(string), 0, 0)
	if e := one(t, drain(ed), "error"); e["code"] != "private_street" {
		t.Fatalf("an outsider can't join %v", e)
	}
	// A fifth on the street after it filled is too late.
	r2 := newCrownRig(t)
	var c2 []*Client
	for i := 0; i < 5; i++ {
		cl := r2.hub.Connect(Claims{Sub: int64(i + 1), Name: crownNames[i]})
		r2.at(cl, "GA1", 0, 0)
		c2 = append(c2, cl)
	}
	r2.do(c2[0], map[string]any{"t": "crown_challenge"})
	for i := 1; i < 4; i++ {
		r2.do(c2[i], map[string]any{"t": "crown_accept", "from": 1})
	}
	drain(c2[4])
	r2.do(c2[4], map[string]any{"t": "crown_accept", "from": 1})
	if f := one(t, drain(c2[4]), "crown_refused"); f["why"] != "full" || f["text"] != "Sorry, you were not quite fast enough to join Ada for Game of Crowns." {
		t.Fatalf("late %v", f)
	}
	_ = cls
}

func TestCrownsClockRunsOnlyBelowThePlayLineAndTheHolderWins(t *testing.T) {
	r := newCrownRig(t)
	cls, match := r.four()
	_, _ = r.play(cls, match)
	// Above the line nothing counts.
	for i := 0; i < 10; i++ {
		r.tick()
	}
	if r.m.matches[match["match"].(string)].member(1).ms != 0 {
		t.Fatal("above the play line the clock waits")
	}
	r.put(cls[0], 200, 900) // Ada goes below (610)
	r.tick()
	mt := r.m.matches[match["match"].(string)]
	if !mt.clock {
		t.Fatal("below the line the holder's clock runs")
	}
	for i := 0; i < 599; i++ {
		r.tick()
	}
	if mt.status != "running" {
		t.Fatalf("not yet: %d ms", mt.member(1).ms)
	}
	r.tick()
	if mt.status != "done" {
		t.Fatalf("60 s below the line wins: %d ms", mt.member(1).ms)
	}
	for i, cl := range cls {
		msgs := drain(cl)
		e := one(t, msgs, "crown_end")
		w := e["winner"].(map[string]any)
		if w["name"] != "Ada" || e["why"] != "won" {
			t.Fatalf("end %v", e)
		}
		if i == 0 {
			if e["seconds"].(float64) != 60 {
				t.Fatalf("Ada's seconds %v", e["seconds"])
			}
			rw := one(t, msgs, "crown_reward")
			if rw["xp"].(float64) != 200 || rw["currants"].(float64) != 50 || !crownsIDRe.MatchString(rw["id"].(string)) {
				t.Fatalf("reward %v", rw)
			}
		} else if len(kinds(msgs, "crown_reward")) != 0 {
			t.Fatal("only the winner is paid")
		}
	}
	if len(r.led.calls) != 1 || !strings.HasSuffix(r.led.calls[0], " 1 50") {
		t.Fatalf("the ledger pays Ada 50 currants once: %v", r.led.calls)
	}
	// Not acked: sent again on the next connect; acked: gone.
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	rw := one(t, drain(ada), "crown_reward")
	r.do(ada, map[string]any{"t": "crown_ack", "id": rw["id"]})
	ada = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	if len(kinds(drain(ada), "crown_reward")) != 0 {
		t.Fatal("an acked reward is not sent again")
	}
}

func TestCrownsTagLockAndCrownGameLocks(t *testing.T) {
	r := newCrownRig(t)
	cls, match := r.four()
	r.play(cls, match)
	mt := r.m.matches[match["match"].(string)]
	r.put(cls[0], 1000, 900)
	r.put(cls[1], 1030, 900)
	// The new holder's 1 s lock (games_is_it lock(1)): no tag yet.
	r.tick()
	if mt.it != 1 {
		t.Fatal("a just-crowned holder is locked")
	}
	for i := 0; i < 10; i++ {
		r.tick()
	}
	if mt.it != 2 {
		t.Fatalf("Bo touching Ada takes the crown: it %d", mt.it)
	}
	tag := one(t, kinds(drain(cls[2]), "crown_tag"), "crown_tag")
	if tag["from"].(float64) != 1 || tag["to"].(float64) != 2 || tag["toName"] != "Bo" {
		t.Fatalf("tag %v", tag)
	}
	// No tag back inside the lock.
	for i := 0; i < 5; i++ {
		r.tick()
	}
	if mt.it != 2 {
		t.Fatal("no tag backs inside the second")
	}
	// Bo runs to a lock: the crown locks for 4 s.
	lock := r.m.a.Locks[1]
	r.put(cls[1], lock[0], lock[1])
	r.put(cls[0], lock[0]+50, lock[1])
	r.tick()
	ev := kinds(drain(cls[3]), "crown_lock")
	if len(ev) != 1 || ev[0]["locked"] != true || ev[0]["i"].(float64) != 1 {
		t.Fatalf("lock %v", ev)
	}
	for i := 0; i < 35; i++ {
		r.tick()
	}
	if mt.it != 2 {
		t.Fatal("a locked crown can't be stolen")
	}
	for i := 0; i < 10; i++ {
		r.tick()
	}
	if mt.it != 1 {
		t.Fatalf("after 4 s the crown can be stolen again: it %d", mt.it)
	}
	// The lock respawns after 12 s, and a non-holder taking it only uses it up.
	if !r.now.Before(mt.locks[1]) {
		t.Fatal("the lock is down")
	}
	r.now = mt.locks[1]
	r.put(cls[3], lock[0], lock[1])
	r.put(cls[0], 2400, 1700)
	r.put(cls[1], 100, 300)
	r.tick()
	ev = kinds(drain(cls[3]), "crown_lock")
	if len(ev) != 1 || ev[0]["locked"] != false || ev[0]["name"] != "Di" {
		t.Fatalf("respawned lock %v", ev)
	}
}

func TestCrownsHolderLeavingPassesTheCrown(t *testing.T) {
	r := newCrownRig(t)
	cls, match := r.four()
	r.play(cls, match)
	mt := r.m.matches[match["match"].(string)]
	r.put(cls[0], 200, 900)
	for i := 0; i < 50; i++ {
		r.tick()
	}
	drain(cls[0])
	r.do(cls[0], map[string]any{"t": "crown_leave", "match": mt.id})
	l := one(t, drain(cls[0]), "crown_left")
	if l["seconds"].(float64) != 4 {
		t.Fatalf("Ada's crown time goes with her: %v", l)
	}
	if mt.it == 1 || mt.it == 0 || mt.status != "running" {
		t.Fatalf("someone else holds it and the game goes on: it %d %s", mt.it, mt.status)
	}
	// Off to another street mid-game: out.
	r.at(cls[1], "GA1", 0, 0)
	if mt.member(2) == nil || !mt.member(2).gone {
		t.Fatal("joining another street leaves the game")
	}
	// A drop has 15 s to come back.
	r.hub.Disconnect(cls[2])
	if mt.member(3).gone || mt.member(3).grace == nil {
		t.Fatal("a dropped member gets a grace")
	}
	cy := r.hub.Connect(Claims{Sub: 3, Name: "Cy"})
	r.at(cy, mt.street, 600, 300)
	if mt.member(3).grace != nil || mt.member(3).gone {
		t.Fatal("coming back within the grace keeps you in")
	}
}

func TestCrownsCalledOffUnderThreeBeforeGo(t *testing.T) {
	r := newCrownRig(t)
	cls, match := r.four()
	street := match["street"].(string)
	for _, cl := range cls {
		r.at(cl, street, 100, 200)
		drain(cl)
	}
	r.do(cls[0], map[string]any{"t": "crown_leave", "match": match["match"]})
	if len(kinds(drain(cls[1]), "crown_end")) != 0 {
		t.Fatal("three can still play")
	}
	r.do(cls[1], map[string]any{"t": "crown_leave", "match": match["match"]})
	e := one(t, drain(cls[2]), "crown_end")
	if e["why"] != "short" || e["winner"] != nil {
		t.Fatalf("under three it is off %v", e)
	}
}

func TestCrownsPlayAgainNeedsThreeAndDropsTheUndecided(t *testing.T) {
	r := newCrownRig(t)
	cls, match := r.four()
	r.play(cls, match)
	mt := r.m.matches[match["match"].(string)]
	r.put(cls[0], 200, 900)
	for i := 0; i < 601; i++ {
		r.tick()
	}
	if mt.status != "done" {
		t.Fatal("round one is over")
	}
	for _, cl := range cls {
		drain(cl)
	}
	r.do(cls[0], map[string]any{"t": "crown_again", "match": mt.id})
	if rs := one(t, drain(cls[1]), "crown_ready_state"); rs["text"] != "Ada is ready to play. Need 2 more." {
		t.Fatalf("again %v", rs["text"])
	}
	r.do(cls[1], map[string]any{"t": "crown_again", "match": mt.id})
	r.do(cls[2], map[string]any{"t": "crown_again", "match": mt.id})
	one(t, drain(cls[0]), "crown_soon")
	r.fire(crownsSoon)
	if rm := one(t, drain(cls[3]), "crown_removed"); rm["text"] != "You were removed from the game due to inactivity." {
		t.Fatalf("removed %v", rm)
	}
	msgs := drain(cls[0])
	if rd := one(t, msgs, "crown_round"); rd["round"].(float64) != 2 || len(rd["players"].([]any)) != 3 {
		t.Fatalf("round %v", rd)
	}
	one(t, msgs, "crown_countdown")
	r.fire(4000 * time.Millisecond)
	g := one(t, drain(cls[1]), "crown_go")
	if g["round"].(float64) != 2 {
		t.Fatalf("round two %v", g)
	}
	for _, s := range g["scores"].([]any) {
		if s.(map[string]any)["ms"].(float64) != 0 {
			t.Fatal("a new round starts every clock at 0")
		}
	}
}

func TestCrownsLedgerRefusalPaysNoCurrants(t *testing.T) {
	r := newCrownRig(t)
	r.led.answer = "refused"
	cls, match := r.four()
	r.play(cls, match)
	r.put(cls[0], 200, 900)
	for i := 0; i < 601; i++ {
		r.tick()
	}
	rw := one(t, drain(cls[0]), "crown_reward")
	if rw["currants"].(float64) != 0 || rw["xp"].(float64) != 200 {
		t.Fatalf("refused: no currants %v", rw)
	}
}

func TestCrownsArenaData(t *testing.T) {
	a, err := parseCrownsArena(crownsJSON)
	if err != nil {
		t.Fatal(err)
	}
	// config_prod.js:1281 and games_end_it_game 671-674.
	if a.Players.Min != 4 || a.Players.Max != 5 || a.Players.Retry != 3 || a.WinMs != 60000 || a.Reward.XP != 200 || a.Reward.Currants != 50 {
		t.Fatalf("arena %+v", a)
	}
	if a.TagLockMs != 1000 || a.PickupLockMs != 4000 || a.LockRespawnMs != 12000 || len(a.Locks) != 4 || len(a.Starts) != 4 {
		t.Fatalf("locks %+v", a)
	}
	for _, s := range a.Starts {
		if s[1] >= a.PlayLineY {
			t.Fatal("every start is above the play line")
		}
	}
}
