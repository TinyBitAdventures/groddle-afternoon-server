package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

type foxRig struct {
	t      *testing.T
	hub    *Hub
	m      *foxes
	now    time.Time
	timers []*rigTimer
}

func newFoxRig(t *testing.T) *foxRig {
	r := &foxRig{t: t, now: time.Unix(1_700_000_000, 0)}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.m = newFoxes(h).(*foxes); return r.m }}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

func (r *foxRig) preserve() string { return r.m.t.Preserves[0] }

func (r *foxRig) do(cl *Client, msg map[string]any) {
	r.now = r.now.Add(3 * time.Second)
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

// players puts n characters (ids 1..n) on the preserve, all at x 2000.
func (r *foxRig) players(n int) []*Client {
	out := make([]*Client, 0, n)
	for i := 1; i <= n; i++ {
		cl := r.hub.Connect(Claims{Sub: int64(i), Name: fmt.Sprintf("P%d", i)})
		r.do(cl, map[string]any{"t": "join", "street": r.preserve(), "x": 2000, "y": -100})
		out = append(out, cl)
	}
	for _, cl := range out {
		drain(cl)
	}
	return out
}

func (r *foxRig) fire(d time.Duration) int {
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

func (r *foxRig) ignite(cl *Client) {
	r.do(cl, map[string]any{"t": "fox_ignite", "baits": []float64{1700, 2000, 2300}})
}

// igniteAt moves cl to x and ignites there (baits 300 px apart around it).
func (r *foxRig) igniteAt(cl *Client, x float64) {
	r.do(cl, map[string]any{"t": "move", "x": x, "y": -100})
	r.do(cl, map[string]any{"t": "fox_ignite", "baits": []float64{x - 300, x, x + 300}})
}

func (r *foxRig) state() *preserve { return r.m.at[r.preserve()] }

const foxSpawn = 4 * time.Second

func TestFoxDataIsTheSources(t *testing.T) {
	r := newFoxRig(t)
	tb := r.m.t
	if tb.PlayersPerFox != 3 || tb.SpawnDelayMs != 4000 || tb.RespawnAfterLeaveMs != 10000 || tb.BaitExpireMs != 300000 ||
		tb.BaitDropCount != 3 || tb.MaxBrushes != 21 || tb.MaxBrushers != 3 || tb.BrushCooldownMs != 2000 ||
		tb.BaitCrowdRadius != 400 || tb.BaitCrowdMax != 4 || len(tb.Preserves) < 40 {
		t.Fatalf("data/foxes.json %+v", tb)
	}
	if tb.Text.Appears != "A fox appears!" || tb.Text.Leaves != "The fox leaves..." {
		t.Fatalf("words %+v", tb.Text)
	}
}

func TestIgniteDropsBaitsForEveryoneAndAFoxComes(t *testing.T) {
	r := newFoxRig(t)
	ps := r.players(2)
	r.ignite(ps[0])
	for _, cl := range ps {
		got := kinds(drain(cl), "fox_baits")
		if len(got) != 1 || len(got[0]["baits"].([]any)) != 3 {
			t.Fatalf("fox_baits %v", got)
		}
	}
	if r.fire(foxSpawn) != 1 {
		t.Fatal("no spawn timer")
	}
	for _, cl := range ps {
		sp := kinds(drain(cl), "fox_spawn")
		if len(sp) != 1 || sp[0]["text"] != "A fox appears!" || sp[0]["n"] != float64(1) {
			t.Fatalf("fox_spawn %v", sp)
		}
	}
	// Two players: 1 < 2/3 is false, so no second fox however much bait burns.
	r.ignite(ps[1])
	r.fire(foxSpawn)
	if n := len(r.state().foxes); n != 1 {
		t.Fatalf("foxes %d, want 1 for two players", n)
	}
}

func TestOneSpawnDueAtATime(t *testing.T) {
	r := newFoxRig(t)
	ps := r.players(10)
	r.ignite(ps[0])
	r.ignite(ps[1])
	if r.fire(foxSpawn) != 1 {
		t.Fatal("two ignites before the spawn should leave one spawn due")
	}
	if len(r.state().foxes) != 1 {
		t.Fatal("want one fox")
	}
}

func TestAFourthFoxNeedsTenPlayersAndCountsTheFlurry(t *testing.T) {
	for _, tc := range []struct {
		players int
		foxes   int
		flurry  bool
	}{{9, 3, false}, {10, 4, true}} {
		r := newFoxRig(t)
		ps := r.players(tc.players)
		for i := 0; i < 4; i++ {
			r.igniteAt(ps[i], float64(1000+1500*i))
			r.fire(foxSpawn)
		}
		if n := len(r.state().foxes); n != tc.foxes {
			t.Fatalf("%d players: %d foxes, want %d", tc.players, n, tc.foxes)
		}
		for _, cl := range ps {
			f := kinds(drain(cl), "fox_flurry")
			if (len(f) == 1) != tc.flurry {
				t.Fatalf("%d players: flurry %v", tc.players, f)
			}
		}
	}
}

func TestThreeBrushersSendTheFoxAwayAndAnotherComes(t *testing.T) {
	r := newFoxRig(t)
	ps := r.players(4)
	r.ignite(ps[0])
	r.fire(foxSpawn)
	fox := r.state().foxes[0].id
	drain(ps[0])
	for i := 0; i < 2; i++ {
		r.do(ps[i], map[string]any{"t": "fox_brush", "fox": fox})
	}
	if r.state().foxes[0].leaving {
		t.Fatal("two brushers should not send it away")
	}
	r.do(ps[2], map[string]any{"t": "fox_brush", "fox": fox})
	lv := kinds(drain(ps[0]), "fox_leave")
	if len(lv) != 1 || lv[0]["text"] != "The fox leaves..." {
		t.Fatalf("fox_leave %v", lv)
	}
	// Still counted until it is gone; bait left, so another is due in 10 s.
	if len(r.state().foxes) != 1 || r.state().spawn == nil {
		t.Fatal("leaving fox should count and a respawn be due")
	}
	r.fire(time.Duration(r.m.t.LeaveMs) * time.Millisecond)
	if len(kinds(drain(ps[0]), "fox_gone")) != 1 || len(r.state().foxes) != 0 {
		t.Fatal("fox_gone")
	}
	if r.fire(10*time.Second) != 1 || len(r.state().foxes) != 1 {
		t.Fatal("respawn after 10 s")
	}
}

func TestTwentyOneBrushesAndTheCooldown(t *testing.T) {
	r := newFoxRig(t)
	ps := r.players(1)
	r.ignite(ps[0])
	r.fire(foxSpawn)
	f := r.state().foxes[0]
	r.now = r.now.Add(time.Second)
	b, _ := json.Marshal(map[string]any{"t": "fox_brush", "fox": f.id})
	r.hub.Handle(ps[0], b)
	r.hub.Handle(ps[0], b) // same instant: within 2 s, nothing
	if f.brushes != 1 {
		t.Fatalf("brushes %d, want 1 (npc_fox.js:212)", f.brushes)
	}
	for i := 0; i < 20; i++ {
		r.do(ps[0], map[string]any{"t": "fox_brush", "fox": f.id})
	}
	if !f.leaving || f.brushes != 21 {
		t.Fatalf("after 21 brushes leaving=%v brushes=%d", f.leaving, f.brushes)
	}
}

func TestEatingTheLastBaitSendsEveryFoxAway(t *testing.T) {
	r := newFoxRig(t)
	ps := r.players(4)
	r.ignite(ps[0])
	r.fire(foxSpawn)
	p := r.state()
	fox := p.foxes[0].id
	ids := []string{p.baits[0].id, p.baits[1].id, p.baits[2].id}
	drain(ps[1])
	for _, id := range ids {
		r.do(ps[1], map[string]any{"t": "fox_eat", "fox": fox, "bait": id})
		r.do(ps[2], map[string]any{"t": "fox_eat", "fox": fox, "bait": id}) // a second report: already eaten
	}
	got := drain(ps[1])
	if len(kinds(got, "fox_bait_gone")) != 3 {
		t.Fatalf("bait gone %v", kinds(got, "fox_bait_gone"))
	}
	lv := kinds(got, "fox_leave")
	if len(lv) != 1 || lv[0]["text"] != "No more bait remains, so the Fox leaves..." {
		t.Fatalf("fox_leave %v", lv)
	}
}

func TestBaitGoesStaleAfterFiveMinutes(t *testing.T) {
	r := newFoxRig(t)
	ps := r.players(1)
	r.ignite(ps[0])
	if r.fire(5*time.Minute) != 3 || len(r.state().baits) != 0 {
		t.Fatal("baits should expire")
	}
}

func TestCrowdedBaitAndPlacesThatAreNotPreserves(t *testing.T) {
	r := newFoxRig(t)
	ps := r.players(1)
	r.ignite(ps[0])
	r.ignite(ps[0]) // 3 near: fine (3 is not > 4)
	r.ignite(ps[0]) // 6 near: refused
	got := drain(ps[0])
	errs := kinds(got, "error")
	if len(kinds(got, "fox_baits")) != 2 || len(errs) != 1 || errs[0]["message"] != "There is already too much bait around here. It will confuse the foxes." {
		t.Fatalf("crowded: baits %d errors %v", len(kinds(got, "fox_baits")), errs)
	}
	other := r.hub.Connect(Claims{Sub: 99, Name: "Out"})
	r.do(other, map[string]any{"t": "join", "street": "LNOTAPRESERVE", "x": 0, "y": 0})
	drain(other)
	r.ignite(other)
	if e := kinds(drain(other), "error"); len(e) != 1 || e[0]["message"] != "Bait can only be ignited in Preserves" {
		t.Fatalf("not a preserve: %v", e)
	}
	// Bait spots far from where the server saw the igniter are ignored.
	r.do(ps[0], map[string]any{"t": "fox_ignite", "baits": []float64{9000, 9300, 9600}})
	if len(kinds(drain(ps[0]), "fox_baits")) != 0 {
		t.Fatal("far baits accepted")
	}
}

func TestArrivalsSeeTheFoxesAndAnEmptyPreserveForgets(t *testing.T) {
	r := newFoxRig(t)
	ps := r.players(1)
	r.ignite(ps[0])
	r.fire(foxSpawn)
	late := r.hub.Connect(Claims{Sub: 50, Name: "Late"})
	r.do(late, map[string]any{"t": "join", "street": r.preserve(), "x": 0, "y": 0})
	st := kinds(drain(late), "fox_state")
	if len(st) != 1 || len(st[0]["foxes"].([]any)) != 1 || len(st[0]["baits"].([]any)) != 3 {
		t.Fatalf("fox_state %v", st)
	}
	r.do(late, map[string]any{"t": "leave"})
	r.do(ps[0], map[string]any{"t": "leave"})
	if r.state() != nil {
		t.Fatal("an empty preserve should forget its foxes")
	}
	if f, ok := r.hub.ProvidedLocked("foxes.count").(func(string) int); !ok || f(r.preserve()) != 0 {
		t.Fatal("foxes.count")
	}
}
