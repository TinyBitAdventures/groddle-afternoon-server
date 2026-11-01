package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// mayhemRig is a hub with the mayhem module (and optionally crowns), a manual clock and manual timers.
type mayhemRig struct {
	t      *testing.T
	hub    *Hub
	m      *mayhem
	c      *crowns
	now    time.Time
	timers []*rigTimer
	ids    int
	led    *fakeMayhemLedger
	target int64
}

type fakeMayhemLedger struct {
	mu     sync.Mutex
	calls  []string
	answer string
}

func (f *fakeMayhemLedger) mayhem(id string, user int64, got tradeGoods) ledgerAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("%s %d %d", id, user, got.Currants))
	return ledgerAnswer{Status: f.answer}
}

func newMayhemRig(t *testing.T, withCrowns bool) *mayhemRig {
	r := &mayhemRig{t: t, now: time.Unix(1_700_000_000, 0), led: &fakeMayhemLedger{answer: "settled"}, target: 7}
	a, err := parseMayhemRules(mayhemJSON)
	if err != nil {
		t.Fatal(err)
	}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module {
		r.m = newMayhemWith(h, a)
		r.m.newID = func() string { r.ids++; return fmt.Sprintf("%016x", r.ids) }
		r.m.pick = func(lo, hi int64) int64 { return r.target }
		r.m.spawn = func(fn func()) { fn() }
		r.m.ledger = r.led
		return r.m
	}}
	if withCrowns {
		ca, _ := parseCrownsArena(crownsJSON)
		modules = append(modules, func(h *Hub) Module { r.c = newCrownsWith(h, ca); return r.c })
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

func (r *mayhemRig) fire(d time.Duration) int {
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

func (r *mayhemRig) tick() {
	r.now = r.now.Add(mayhemTickEvery)
	r.fire(mayhemTickEvery)
	r.fire(0)
}

func (r *mayhemRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *mayhemRig) at(cl *Client, street string, x, y float64) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": x, "y": y})
}

func (r *mayhemRig) put(cl *Client, x, y float64) {
	cl.moveWindow, cl.moveCount = time.Time{}, 0
	r.do(cl, map[string]any{"t": "move", "x": x, "y": y})
}

var mayhemNames = []string{"Ada", "Bo", "Cy", "Di", "Ed", "Flo"}

// enter brings n players onto GA1, has each join from a Game Pig and walk into the arena at their seat.
func (r *mayhemRig) enter(n int) ([]*Client, string, string) {
	var cls []*Client
	for i := 0; i < n; i++ {
		cl := r.hub.Connect(Claims{Sub: int64(i + 1), Name: mayhemNames[i]})
		r.at(cl, "GA1", float64(i*300), 0)
		cls = append(cls, cl)
	}
	for _, cl := range cls {
		drain(cl)
	}
	var room, street string
	for i, cl := range cls {
		r.do(cl, map[string]any{"t": "mayhem_join"})
		rm := one(r.t, drain(cl), "mayhem_room")
		if room == "" {
			room, street = rm["room"].(string), rm["street"].(string)
		} else if rm["room"] != room {
			r.t.Fatalf("one open game takes everyone: %v vs %v", rm["room"], room)
		}
		if int(rm["slot"].(float64)) != i || rm["min"].(float64) != 4 || rm["max"].(float64) != 20 {
			r.t.Fatalf("room %v", rm)
		}
		sp := rm["spawn"].([]any)
		r.at(cl, street, sp[0].(float64), sp[1].(float64))
	}
	for _, cl := range cls {
		drain(cl)
	}
	return cls, room, street
}

// start has everyone click and runs the soon and the countdown to GO.
func (r *mayhemRig) start(cls []*Client, room string) map[string]any {
	for _, cl := range cls {
		r.do(cl, map[string]any{"t": "mayhem_ready", "room": room})
	}
	soon := one(r.t, drain(cls[0]), "mayhem_soon")
	if soon["text"] != "YAY! THE GAME IS STARTING SOON." {
		r.t.Fatalf("soon %v", soon)
	}
	r.fire(mayhemSoon)
	one(r.t, drain(cls[0]), "mayhem_countdown")
	r.fire(4000 * time.Millisecond)
	g := one(r.t, drain(cls[0]), "mayhem_go")
	for _, cl := range cls[1:] {
		one(r.t, drain(cl), "mayhem_go")
	}
	return g
}

func rowsByID(msg map[string]any) map[int64]map[string]any {
	out := map[int64]map[string]any{}
	for _, p := range msg["players"].([]any) {
		row := p.(map[string]any)
		out[int64(row["id"].(float64))] = row
	}
	return out
}

func TestMayhemRulesData(t *testing.T) {
	a, err := parseMayhemRules(mayhemJSON)
	if err != nil {
		t.Fatal(err)
	}
	// config_prod.js:1282; global_game.js:298; inc_games.js 1008, 1077-1078.
	if a.Players.Min != 4 || a.Players.Max != 20 || a.Target.Min != 10 || a.Target.Max != 100 || a.LockMs != 1000 ||
		a.Reward.XP != 200 || a.Reward.Currants != 50 || a.Teams[0] != "blue" || a.Teams[1] != "red" || a.Street != "GHFFV931FRF3TMS" {
		t.Fatalf("rules %+v", a)
	}
	if _, err := parseMayhemRules([]byte(`{"title":"x"}`)); err == nil {
		t.Fatal("a broken rules file is refused")
	}
}

func TestMayhemJoinFillsOneGameAndNeedsFour(t *testing.T) {
	r := newMayhemRig(t, false)
	cls, room, street := r.enter(3)
	// The street the third joined from heard (ours, as a crowns challenge).
	ed := r.hub.Connect(Claims{Sub: 5, Name: "Ed"})
	r.at(ed, "GA1", 0, 0)
	drain(ed)
	// Three click: not enough (min_players 4).
	for _, cl := range cls {
		r.do(cl, map[string]any{"t": "mayhem_ready", "room": room})
	}
	msgs := drain(cls[0])
	if len(kinds(msgs, "mayhem_soon")) != 0 {
		t.Fatal("three never start")
	}
	last := kinds(msgs, "mayhem_players")
	if txt := last[len(last)-1]["text"]; txt != "Cy is ready to play. Need 1 more." {
		t.Fatalf("need text %v", txt)
	}
	// An outsider cannot walk into the arena.
	r.at(ed, street, 0, 0)
	if e := one(t, drain(ed), "error"); e["code"] != "private_street" {
		t.Fatalf("private %v", e)
	}
	r.at(ed, "GA1", 0, 0)
	drain(ed)
	// Di joins from the street: she is in the same game and must click before it starts.
	di := r.hub.Connect(Claims{Sub: 4, Name: "Di"})
	r.at(di, "GA1", 0, 0)
	drain(di)
	r.do(di, map[string]any{"t": "mayhem_join"})
	if rm := one(t, drain(di), "mayhem_room"); rm["room"] != room || rm["slot"].(float64) != 3 {
		t.Fatalf("di %v", rm)
	}
	look := one(t, drain(ed), "mayhem_looking")
	if look["text"] != "Di wants to play Math Mayhem. Need 1 more." || look["need"].(float64) != 1 {
		t.Fatalf("looking %v", look)
	}
	if p := one(t, drain(cls[0]), "mayhem_players"); p["text"] != "Di joined Math Mayhem." {
		t.Fatalf("joined %v", p)
	}
	r.at(di, street, 660, 275)
	drain(cls[0])
	r.do(di, map[string]any{"t": "mayhem_ready", "room": room})
	if s := one(t, drain(cls[0]), "mayhem_soon"); s["in"].(float64) != 5000 {
		t.Fatalf("soon %v", s)
	}
	// A game about to start takes nobody new: Ed gets a new one.
	r.do(ed, map[string]any{"t": "mayhem_join"})
	if rm := one(t, drain(ed), "mayhem_room"); rm["room"] == room {
		t.Fatal("a starting game is closed")
	}
	// Twice in is busy.
	r.do(ed, map[string]any{"t": "mayhem_join"})
	if f := one(t, drain(ed), "mayhem_refused"); f["why"] != "busy" {
		t.Fatalf("busy %v", f)
	}
}

func TestMayhemTeamsTargetAndMath(t *testing.T) {
	r := newMayhemRig(t, false)
	r.target = 3
	cls, room, street := r.enter(4)
	g := r.start(cls, room)
	if g["target"].(float64) != 3 || g["ms"].(float64) != 300000 {
		t.Fatalf("go %v", g)
	}
	// initMathMayhemInInstance: blue 1, red 1, blue 2, red 2.
	rows := rowsByID(g)
	want := map[int64][2]any{1: {"blue", 1.0}, 2: {"red", 1.0}, 3: {"blue", 2.0}, 4: {"red", 2.0}}
	for id, w := range want {
		if rows[id]["team"] != w[0] || rows[id]["score"] != w[1] {
			t.Fatalf("player %d: %v", id, rows[id])
		}
	}
	_ = street
	// Ada (blue 1) walks to Di (red 2): two teams take the smaller from the larger, 1.
	r.put(cls[0], 2300, 259)
	r.tick()
	r.put(cls[0], 2320, 259)
	r.tick()
	b := one(t, drain(cls[1]), "mayhem_bump")
	if b["answer"].(float64) != 1 {
		t.Fatalf("2 - 1 %v", b)
	}
	rows = rowsByID(b)
	if rows[1]["score"].(float64) != 1 || rows[4]["score"].(float64) != 1 || rows[1]["locked"].(float64) != 1000 {
		t.Fatalf("both get it and are locked %v", rows)
	}
	// Still touching: no second sum until they part.
	for i := 0; i < 15; i++ {
		r.tick()
	}
	if len(kinds(drain(cls[1]), "mayhem_bump")) != 0 {
		t.Fatal("a touch counts once")
	}
	// Ada goes to Cy (blue 2): the same team adds, 1 + 2 = 3 = the target. Ada moved, Cy was touched: Cy wins.
	r.put(cls[0], 660, 275)
	r.tick()
	r.put(cls[0], 670, 275)
	r.tick()
	end := one(t, drain(cls[1]), "mayhem_end")
	w := end["winner"].(map[string]any)
	if w["name"] != "Cy" || end["why"] != "won" || end["target"].(float64) != 3 {
		t.Fatalf("end %v", end)
	}
	cyMsgs := drain(cls[2])
	for _, msgs := range [][]map[string]any{drain(cls[0]), cyMsgs, drain(cls[3])} {
		if e := one(t, msgs, "mayhem_end"); e["played"] != true {
			t.Fatalf("everyone's game ends %v", e)
		}
	}
	// Cy's 50 currants went into the ledger once, then the reward.
	if len(r.led.calls) != 1 || r.led.calls[0] != "mayhem-0000000000000001-1-3 3 50" {
		t.Fatalf("ledger %v", r.led.calls)
	}
	rew := one(t, cyMsgs, "mayhem_reward")
	if rew["xp"].(float64) != 200 || rew["currants"].(float64) != 50 || rew["id"] != "mayhem-0000000000000001-1-3" {
		t.Fatalf("reward %v", rew)
	}
	// The reward comes again on a reconnect until acked.
	cy := r.hub.Connect(Claims{Sub: 3, Name: "Cy"})
	one(t, drain(cy), "mayhem_reward")
	r.do(cy, map[string]any{"t": "mayhem_ack", "id": rew["id"]})
	cy2 := r.hub.Connect(Claims{Sub: 3, Name: "Cy"})
	if len(kinds(drain(cy2), "mayhem_reward")) != 0 {
		t.Fatal("acked once is done")
	}
}

func TestMayhemLockHoldsATouchUntilItEnds(t *testing.T) {
	r := newMayhemRig(t, false)
	r.target = 99
	cls, room, _ := r.enter(4)
	r.start(cls, room)
	// Ada and Bo touch (1 and 1, two teams: 0); both locked a second.
	r.put(cls[0], 1790, 245)
	r.tick()
	one(t, drain(cls[0]), "mayhem_bump")
	// Bo, still locked, runs into Cy at once: the touch waits for the lock.
	r.put(cls[1], 670, 275)
	r.tick()
	if len(kinds(drain(cls[0]), "mayhem_bump")) != 0 {
		t.Fatal("a locked player does no math")
	}
	for i := 0; i < 10; i++ {
		r.tick()
	}
	b := kinds(drain(cls[0]), "mayhem_bump")
	if len(b) != 1 || b[0]["answer"].(float64) != 2 {
		t.Fatalf("after the lock Bo (0, red) and Cy (2, blue) make 2: %v", b)
	}
}

func TestMayhemLeaversTimeoutAndPlayAgain(t *testing.T) {
	r := newMayhemRig(t, false)
	cls, room, street := r.enter(4)
	r.start(cls, room)
	// Di leaves: her number goes with her and the game goes on (playerLeft 816-818).
	r.do(cls[3], map[string]any{"t": "mayhem_leave", "room": room})
	st := one(t, drain(cls[0]), "mayhem_state")
	if len(st["players"].([]any)) != 3 {
		t.Fatalf("three left %v", st)
	}
	// Nobody makes it: the round ends with no winner (ours).
	r.now = r.now.Add(5 * time.Minute)
	r.fire(5 * time.Minute)
	end := one(t, drain(cls[0]), "mayhem_end")
	if end["winner"] != nil || end["why"] != "timeout" {
		t.Fatalf("timeout %v", end)
	}
	if len(r.led.calls) != 0 {
		t.Fatal("no winner, no currants")
	}
	// Play Again: three choose it and a new player joins the open game; four start round 2.
	for _, cl := range cls[:3] {
		r.do(cl, map[string]any{"t": "mayhem_again", "room": room})
	}
	ed := r.hub.Connect(Claims{Sub: 5, Name: "Ed"})
	r.at(ed, "GA1", 0, 0)
	r.do(ed, map[string]any{"t": "mayhem_join"})
	if rm := one(t, drain(ed), "mayhem_room"); rm["room"] != room {
		t.Fatalf("a finished game takes players again %v", rm)
	}
	r.at(ed, street, 660, 275)
	drain(cls[0])
	r.do(ed, map[string]any{"t": "mayhem_ready", "room": room})
	one(t, drain(cls[0]), "mayhem_soon")
	r.fire(mayhemSoon)
	if c := one(t, drain(cls[0]), "mayhem_countdown"); c["round"].(float64) != 2 {
		t.Fatalf("round 2 %v", c)
	}
}

func TestMayhemTooFewEndsTheRound(t *testing.T) {
	r := newMayhemRig(t, false)
	cls, room, _ := r.enter(4)
	r.start(cls, room)
	for _, cl := range cls[1:3] {
		r.do(cl, map[string]any{"t": "mayhem_leave", "room": room})
	}
	drain(cls[0])
	// Bo drops off the socket: 15 s of grace, then out; one left cannot do math.
	r.hub.Disconnect(cls[3])
	r.fire(mayhemGrace)
	end := one(t, drain(cls[0]), "mayhem_end")
	if end["why"] != "short" || end["winner"] != nil {
		t.Fatalf("short %v", end)
	}
}

func TestMayhemIdleIsRemoved(t *testing.T) {
	r := newMayhemRig(t, false)
	cls, room, _ := r.enter(2)
	r.do(cls[0], map[string]any{"t": "mayhem_ready", "room": room})
	drain(cls[0])
	r.fire(mayhemAutoLeave)
	if rm := one(t, drain(cls[1]), "mayhem_removed"); rm["text"] != "You were removed from the game due to inactivity." {
		t.Fatalf("idle %v", rm)
	}
	if r.m.roomOf[2] != nil || r.m.roomOf[1] == nil {
		t.Fatal("only the one who never clicked goes")
	}
}

func TestMayhemRefusals(t *testing.T) {
	r := newMayhemRig(t, true)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.at(ada, "GA524M0OMOE2OGT", 0, 0) // Naraka
	drain(ada)
	r.do(ada, map[string]any{"t": "mayhem_join"})
	if f := one(t, drain(ada), "mayhem_refused"); f["why"] != "dead" {
		t.Fatalf("dead %v", f)
	}
	// In a Game of Crowns challenge: busy both ways.
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.at(ada, "GA1", 0, 0)
	r.at(bo, "GA1", 0, 0)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "crown_challenge"})
	drain(ada)
	r.do(ada, map[string]any{"t": "mayhem_join"})
	if f := one(t, drain(ada), "mayhem_refused"); f["why"] != "busy" {
		t.Fatalf("crowns busy %v", f)
	}
	r.do(bo, map[string]any{"t": "mayhem_join"})
	drain(bo)
	r.do(bo, map[string]any{"t": "crown_accept", "from": 1})
	if f := one(t, drain(bo), "crown_refused"); f["why"] != "busy" {
		t.Fatalf("mayhem busy %v", f)
	}
}

func TestMayhemRewardSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	mayhemFileForTest = filepath.Join(dir, "mayhem.json")
	defer func() { mayhemFileForTest = "" }()
	r := newMayhemRig(t, false)
	r.m.useFile(mayhemFileForTest)
	r.target = 3
	cls, room, _ := r.enter(4)
	r.start(cls, room)
	r.put(cls[0], 2300, 259)
	r.tick()
	r.put(cls[0], 2320, 259)
	r.tick()
	for i := 0; i < 10; i++ {
		r.tick() // the 1 s lock
	}
	r.put(cls[0], 660, 275)
	r.tick()
	r.put(cls[0], 670, 275)
	r.tick()
	if len(r.led.calls) != 1 {
		t.Fatalf("won? %v", r.led.calls)
	}
	if b, err := os.ReadFile(mayhemFileForTest); err != nil || len(b) < 10 {
		t.Fatalf("owed is on disk: %v %s", err, b)
	}
	r2 := newMayhemRig(t, false)
	r2.m.useFile(mayhemFileForTest)
	cy := r2.hub.Connect(Claims{Sub: 3, Name: "Cy"})
	if rew := one(t, drain(cy), "mayhem_reward"); rew["currants"].(float64) != 50 {
		t.Fatalf("after a restart %v", rew)
	}
}
