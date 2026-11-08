package main

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

// cubiRig is a hub with only the battle module and a feats.count stand-in, a manual clock and
// timers that run in time order.
type cubiRig struct {
	t      *testing.T
	hub    *Hub
	m      *cubiBattles
	now    time.Time
	timers []*cubiTimer
	feats  []string
	rolls  []float64
}

type cubiTimer struct {
	due     time.Time
	seq     int
	fn      func()
	stopped bool
}

type cubiFeats struct{ r *cubiRig }

func (f cubiFeats) Register(r *Registry) {
	r.Provide("feats.count", func(id int64, name, feat string, n int64) int64 {
		f.r.feats = append(f.r.feats, name+":"+feat+":"+strings.Repeat("+", int(n)))
		return n
	})
}

func newCubiRig(t *testing.T) *cubiRig {
	r := &cubiRig{t: t, now: time.Unix(1_700_000_000, 0)}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newCubiBattles(h).(*cubiBattles); return r.m },
		func(h *Hub) Module { return cubiFeats{r} },
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	seq := 0
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		seq++
		tm := &cubiTimer{due: r.now.Add(d), seq: seq, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	// Rolls come from r.rolls in order, then 0.5 (a middling roll that never chokes: floor(0.5*10) = 5).
	r.m.rng = func() float64 {
		if len(r.rolls) == 0 {
			return 0.5
		}
		v := r.rolls[0]
		r.rolls = r.rolls[1:]
		return v
	}
	return r
}

// advance moves the clock d on, running each timer due on the way in time order.
func (r *cubiRig) advance(d time.Duration) {
	end := r.now.Add(d)
	for {
		var next *cubiTimer
		for _, tm := range r.timers {
			if tm.stopped || tm.due.After(end) {
				continue
			}
			if next == nil || tm.due.Before(next.due) || (tm.due.Equal(next.due) && tm.seq < next.seq) {
				next = tm
			}
		}
		if next == nil {
			break
		}
		next.stopped = true
		if next.due.After(r.now) {
			r.now = next.due
		}
		next.fn()
	}
	r.now = end
}

func (r *cubiRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

func (r *cubiRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *cubiRig) join(cl *Client, street string, x, y float64) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": x, "y": y})
}

func (r *cubiRig) battle(cl *Client, class, style string, level int) {
	r.do(cl, map[string]any{"t": "cubi_battle", "c": class, "style": style, "level": level, "xp": 0})
}

func cubiOne(t *testing.T, msgs []map[string]any, kind string) map[string]any {
	t.Helper()
	got := kinds(msgs, kind)
	if len(got) != 1 {
		t.Fatalf("want one %s, got %d in %v", kind, len(got), msgs)
	}
	return got[0]
}

func TestCubiAloneSaysComeAtMeAndWaits(t *testing.T) {
	r := newCubiRig(t)
	ada := r.connect(1, "Ada")
	r.join(ada, "GA5A", 100, -60)
	drain(ada)
	r.battle(ada, "npc_cubimal_chick", "r", 1)
	f := cubiOne(t, drain(ada), "cubi_fighter")["f"].(map[string]any)
	if f["label"] != "Chick Cubimal Lvl.1" || f["state"] != "waiting" || f["ego"] != float64(10) || f["x"] != float64(100) {
		t.Fatalf("fighter %v", f)
	}
	r.advance(999 * time.Millisecond)
	if got := drain(ada); len(got) != 0 {
		t.Fatalf("nothing before the second's search: %v", got)
	}
	r.advance(time.Millisecond)
	say := cubiOne(t, drain(ada), "cubi_say")
	if say["text"] != "Come at me, Bro!" || say["ms"] != float64(3000) {
		t.Fatalf("say %v", say)
	}
	r.advance(time.Minute)
	if got := drain(ada); len(got) != 0 || r.m.fighters[1] == nil {
		t.Fatalf("it waits on the ground: %v", got)
	}
	// A second figure while one is out: busy.
	r.battle(ada, "npc_cubimal_rook", "p", 1)
	if e := cubiOne(t, drain(ada), "error"); e["message"] != "busy" {
		t.Fatalf("error %v", e)
	}
	// Its owner picks it up.
	r.do(ada, map[string]any{"t": "cubi_pickup"})
	home := cubiOne(t, drain(ada), "cubi_home")
	if home["why"] != "picked" || home["id"] != float64(1) || r.m.fighters[1] != nil {
		t.Fatalf("home %v", home)
	}
}

func TestCubiRefusals(t *testing.T) {
	r := newCubiRig(t)
	ada := r.connect(1, "Ada")
	r.battle(ada, "npc_cubimal_chick", "r", 1)
	if e := cubiOne(t, drain(ada), "error"); e["message"] != "not on a street" {
		t.Fatalf("%v", e)
	}
	r.join(ada, "GA5A", 100, -60)
	drain(ada)
	r.battle(ada, "npc_cubimal_factorydefect_chick", "r", 1)
	if e := cubiOne(t, drain(ada), "error"); e["message"] != "not a figure that battles" {
		t.Fatalf("%v", e)
	}
	r.battle(ada, "npc_cubimal_chick", "x", 1)
	if e := cubiOne(t, drain(ada), "error"); e["message"] != "no such style" {
		t.Fatalf("%v", e)
	}
	r.battle(ada, "npc_cubimal_chick", "s", 1)
	drain(ada)
	r.do(ada, map[string]any{"t": "cubi_pickup"})
	drain(ada)
	r.battle(ada, "npc_cubimal_chick", "s", 1)
	if e := cubiOne(t, drain(ada), "error"); e["message"] != "slow_down" {
		t.Fatalf("%v", e)
	}
	r.advance(time.Second)
	r.battle(ada, "npc_cubimal_chick", "s", 500)
	if f := cubiOne(t, drain(ada), "cubi_fighter")["f"].(map[string]any); f["level"] != float64(99) || f["ego"] != float64(10+2*98) {
		t.Fatalf("a level is held to the ceiling and the Ego follows it: %v", f)
	}
}

// A full fight between two level 1 figures with middling rolls (0.5, never a choke): Gumption
// against Panache takes 2 a turn, Panache against Gumption 3, so Bo's Rook wins.
func TestCubiFightTakesTurnsAndTheWinnerLevels(t *testing.T) {
	r := newCubiRig(t)
	ada, bo, cy := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	r.join(ada, "GA5A", 100, -60)
	r.join(bo, "GA5A", 400, -60)
	r.join(cy, "GA5A", 900, -60)
	drain(ada)
	drain(bo)
	drain(cy)
	r.battle(bo, "npc_cubimal_rook", "p", 1)
	r.advance(time.Second) // Bo's waits: Come at me, Bro!
	drain(ada)
	drain(bo)
	drain(cy)
	r.do(ada, map[string]any{"t": "cubi_battle", "c": "npc_cubimal_chick", "style": "r", "name": "Sprocket", "level": 1, "xp": 0})
	r.advance(time.Second)
	got := drain(cy)
	vs := cubiOne(t, got, "cubi_vs")
	if vs["text"] != "Ada's Sprocket Lvl.1 VS. Bo's Rook Cubimal Lvl.1" || vs["a"] != float64(1) || vs["b"] != float64(2) {
		t.Fatalf("vs %v", vs)
	}
	r.advance(10 * time.Millisecond)
	got = append(got, drain(cy)...)
	strike := cubiOne(t, got, "cubi_strike")
	// Gumption against Panache: -1 on Ada's top, randInt(1, 3) at 0.5 = 2 for both rolls.
	if strike["id"] != float64(1) || strike["vs"] != float64(2) || strike["text"] != "You're more like a spherimal!" || strike["ego"] != float64(8) || strike["to"] != float64(360) {
		t.Fatalf("strike %v", strike)
	}
	// The style shouts go to each owner only, on the first strike.
	if s := cubiOne(t, drain(ada), "cubi_say"); s["text"] != "Gumption!" || s["id"] != float64(1) {
		t.Fatalf("Ada's shout %v", s)
	}
	if s := cubiOne(t, drain(bo), "cubi_say"); s["text"] != "Panache!" || s["id"] != float64(2) {
		t.Fatalf("Bo's shout %v", s)
	}
	if len(kinds(got, "cubi_say")) != 0 {
		t.Fatalf("a bystander hears no shout: %v", got)
	}
	r.advance(1400 * time.Millisecond)
	back := cubiOne(t, drain(cy), "cubi_back")
	// The middle of 100 and 400 is 250; Ada's side, 50 from it: 200.
	if back["id"] != float64(1) || back["to"] != float64(200) {
		t.Fatalf("back %v", back)
	}
	ego := cubiOne(t, drain(ada), "cubi_line")
	if ego["text"] != "Rook Cubimal Lvl.1's Ego remaining: 8" {
		t.Fatalf("ego %v", ego)
	}
	cubiOne(t, drain(bo), "cubi_line")
	r.advance(1500 * time.Millisecond)
	strike = cubiOne(t, drain(cy), "cubi_strike")
	// Panache against Gumption: +1 (randInt(1, 5) at 0.5 = 3, "you're a Noobimal.").
	if strike["id"] != float64(2) || strike["text"] != "you're a Noobimal." || strike["ego"] != float64(7) || strike["to"] != float64(240) {
		t.Fatalf("Bo strikes back %v", strike)
	}
	// Run it out: someone wins.
	r.advance(time.Minute)
	all := drain(cy)
	won := cubiOne(t, all, "cubi_won")
	homes := kinds(all, "cubi_home")
	if len(homes) != 2 {
		t.Fatalf("both go home: %v", all)
	}
	sort.Slice(homes, func(i, j int) bool { return homes[i]["why"].(string) < homes[j]["why"].(string) })
	lost, win := homes[0], homes[1]
	if lost["why"] != "lost" || win["why"] != "won" || win["id"] != won["id"] || lost["id"] != won["vs"] {
		t.Fatalf("homes %v won %v", homes, won)
	}
	// A level 1 figure beats a level 1: +10, next_level_amount 10, so level 2 with 0 left over.
	if win["level"] != float64(2) || win["xp"] != float64(0) || win["earned"] != float64(10) || win["level_up"] != true {
		t.Fatalf("the winner's gain %v", win)
	}
	say := kinds(all, "cubi_say")
	if len(say) == 0 || say[len(say)-1]["text"] != "LEVEL 2!" {
		t.Fatalf("LEVEL 2! %v", say)
	}
	winner := "Ada"
	if won["id"] == float64(2) {
		winner = "Bo"
	}
	if len(r.feats) != 1 || r.feats[0] != winner+":tottlys_toys:++" {
		t.Fatalf("Tottly's Toys: 1 for the win, 1 for the level: %v", r.feats)
	}
	if !strings.HasSuffix(won["text"].(string), "Lvl.1") || !strings.Contains(won["text"].(string), " defeated ") {
		t.Fatalf("won %v", won)
	}
	if len(r.m.fighters) != 0 {
		t.Fatalf("nothing left out: %v", r.m.fighters)
	}
}

func TestCubiOutOfReachOrOwnFiguresNeverFight(t *testing.T) {
	r := newCubiRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.join(ada, "GA5A", 100, -60)
	r.join(bo, "GA5A", 551, -60) // 451 px away
	r.battle(bo, "npc_cubimal_rook", "p", 1)
	r.battle(ada, "npc_cubimal_chick", "r", 1)
	r.advance(2 * time.Second)
	if got := kinds(drain(ada), "cubi_vs"); len(got) != 0 {
		t.Fatalf("too far: %v", got)
	}
	// Ground far above or below is not reachable either.
	r.do(bo, map[string]any{"t": "cubi_pickup"})
	r.join(bo, "GA5A", 300, -200)
	r.advance(time.Second)
	r.battle(bo, "npc_cubimal_rook", "p", 1)
	r.advance(2 * time.Second)
	if got := kinds(drain(ada), "cubi_vs"); len(got) != 0 {
		t.Fatalf("not the same ground: %v", got)
	}
}

func TestCubiLeavingCallsTheFightOff(t *testing.T) {
	r := newCubiRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.join(ada, "GA5A", 100, -60)
	r.join(bo, "GA5A", 400, -60)
	r.battle(bo, "npc_cubimal_rook", "p", 1)
	r.advance(time.Second)
	r.battle(ada, "npc_cubimal_chick", "r", 1)
	r.advance(1100 * time.Millisecond)
	drain(ada)
	drain(bo)
	// Mid-fight, no pick up.
	r.do(ada, map[string]any{"t": "cubi_pickup"})
	if e := cubiOne(t, drain(ada), "error"); e["message"] != "nothing to pick up" {
		t.Fatalf("%v", e)
	}
	r.join(ada, "GB5B", 0, 0)
	homes := kinds(drain(bo), "cubi_home")
	if len(homes) != 2 || homes[0]["why"] != "left" || homes[1]["why"] != "left" {
		t.Fatalf("both home, no winner: %v", homes)
	}
	// Ada hears her own figure go home though she has left the street.
	if got := kinds(drain(ada), "cubi_home"); len(got) != 1 || got[0]["id"] != float64(1) {
		t.Fatalf("Ada's own home: %v", got)
	}
	r.advance(time.Minute)
	if len(r.feats) != 0 || len(r.m.fighters) != 0 {
		t.Fatalf("no winner, nothing out: %v %v", r.feats, r.m.fighters)
	}
}

func TestCubiJoinSeesTheFiguresOut(t *testing.T) {
	r := newCubiRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.join(ada, "GA5A", 100, -60)
	r.battle(ada, "npc_cubimal_chick", "s", 3)
	r.join(bo, "GA5A", 300, -60)
	list := cubiOne(t, drain(bo), "cubi_fighters")
	fs := list["fighters"].([]any)
	if len(fs) != 1 || fs[0].(map[string]any)["label"] != "Chick Cubimal Lvl.3" || fs[0].(map[string]any)["ego"] != float64(14) {
		t.Fatalf("fighters %v", list)
	}
}

// The same insult twice running halves the second one's damage (rounded up), and the bubble says "No, ".
func TestCubiSameInsultHalves(t *testing.T) {
	r := newCubiRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.join(ada, "GA5A", 100, -60)
	r.join(bo, "GA5A", 400, -60)
	r.battle(bo, "npc_cubimal_rook", "r", 1)
	r.advance(time.Second)
	r.battle(ada, "npc_cubimal_chick", "r", 1)
	// Same style: no mod. Ada: insult randInt(1,4) at 0.99 = 4, damage at 0.0 = 1, no choke (0.5).
	// Bo: insult 4 again (the same), damage roll 0.99 (unused), no choke: takes ceil(4/2) = 2.
	r.rolls = []float64{0.99, 0.0, 0.5, 0.99, 0.99, 0.5}
	r.advance(1010 * time.Millisecond)
	s1 := cubiOne(t, drain(bo), "cubi_strike")
	if s1["text"] != "Any squarer and you'd be round." || s1["ego"] != float64(9) {
		t.Fatalf("first %v", s1)
	}
	r.advance(2900 * time.Millisecond)
	s2 := kinds(drain(bo), "cubi_strike")
	if len(s2) != 1 || s2[0]["text"] != "No, Any squarer and you'd be round." || s2[0]["ego"] != float64(8) {
		t.Fatalf("second %v", s2)
	}
	// A choke does nothing and stays put.
	r.rolls = []float64{0.5, 0.5, 0.0}
	r.advance(2200 * time.Millisecond)
	s3 := kinds(drain(bo), "cubi_strike")
	if len(s3) != 1 || s3[0]["text"] != "*choke*" || s3[0]["choke"] != true || s3[0]["ego"] != float64(9) || s3[0]["to"] != float64(200) {
		t.Fatalf("choke %v", s3)
	}
}

func TestCubiRules(t *testing.T) {
	r := newCubiRig(t)
	for l, want := range map[int]int{1: 10, 2: 22, 3: 48, 4: 105, 5: 231} {
		if got := r.m.nextLevel(l); got != want {
			t.Fatalf("next_level_amount at %d: %d, want %d", l, got, want)
		}
	}
	for pair, want := range map[string]int{"rs": 1, "rp": -1, "sr": -1, "pr": 1, "sp": 1, "ps": 0, "rr": 0, "pp": 0, "ss": 0} {
		if got := styleMod(pair[:1], pair[1:]); got != want {
			t.Fatalf("%s: %d, want %d", pair, got, want)
		}
	}
	if len(r.m.t.Labels) != 43 || len(r.m.t.Insults) != 22 || r.m.t.Labels["npc_cubimal_rook"] != "Rook Cubimal" {
		t.Fatalf("data %d labels, %d insults", len(r.m.t.Labels), len(r.m.t.Insults))
	}
}
