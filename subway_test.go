package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"
)

// clockRig is a hub with a clock the test moves and the timers that clock makes due.
type clockRig struct {
	t      *testing.T
	hub    *Hub
	now    time.Time
	timers []*clockTimer
}

type clockTimer struct {
	at      time.Time
	fn      func()
	stopped bool
}

func newClockRig(t *testing.T, mod moduleFactory) *clockRig {
	r := &clockRig{t: t, now: time.Unix(1_700_000_000, 0)}
	saved := modules
	modules = []moduleFactory{mod}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &clockTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

// advance moves the clock on, firing every timer that falls due on the way, in order.
func (r *clockRig) advance(d time.Duration) {
	end := r.now.Add(d)
	for {
		var next *clockTimer
		for _, tm := range r.timers {
			if !tm.stopped && !tm.at.After(end) && (next == nil || tm.at.Before(next.at)) {
				next = tm
			}
		}
		if next == nil {
			break
		}
		next.stopped = true
		if next.at.After(r.now) {
			r.now = next.at
		}
		next.fn()
	}
	r.now = end
}

func (r *clockRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *clockRig) join(cl *Client, street string, x float64) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": x, "y": 0})
}

func last(t *testing.T, msgs []map[string]any, kind string) map[string]any {
	t.Helper()
	k := kinds(msgs, kind)
	if len(k) == 0 {
		t.Fatalf("no %s in %v", kind, msgs)
	}
	return k[len(k)-1]
}

func testSubway() subwayData {
	return subwayData{MinCapacity: 3, MaxCapacity: 10, Initial: 60, Dwell: 10, Lonely: 10, Doors: 3, Travel: 20,
		Stations: []subwayStation{{ID: 1, Name: "One", Street: "G1", Next: 2, Prev: 3}, {ID: 2, Name: "Two", Street: "G2", Next: 3, Prev: 1}, {ID: 3, Name: "Three", Street: "G3", Next: 1, Prev: 2}}}
}

func subwayRig(t *testing.T) (*clockRig, *subway) {
	var m *subway
	ids := 0
	r := newClockRig(t, func(h *Hub) Module {
		m = newSubwayWith(h, testSubway())
		m.newID = func() string { ids++; return fmt.Sprintf("%016X", ids) }
		return m
	})
	return r, m
}

func TestTheShippedSubwayMatchesTheSource(t *testing.T) {
	d, err := parseSubway(subwayJSON)
	if err != nil || len(d.Stations) != 12 {
		t.Fatalf("data/subway.json: %v %d", err, len(d.Stations))
	}
	// config_prod.js transit_instances.subway_1.
	if d.MinCapacity != 3 || d.MaxCapacity != 10 || d.Initial != 60 || d.Dwell != 10 {
		t.Fatalf("capacities and timeouts %+v", d)
	}
	m := newSubwayWith(NewHub(nil), d)
	for _, s := range d.Stations {
		if m.byID[s.Next] == nil || m.byID[s.Prev] == nil || m.byID[s.Next].Prev != s.ID {
			t.Fatalf("the line is a loop: %+v", s)
		}
	}
}

func TestRidersShareATrainAndGetOffAtTheirOwnStops(t *testing.T) {
	r, m := subwayRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.join(ada, "G1", 0)
	r.join(bo, "G1", 100)
	drain(ada)
	drain(bo)

	// Ada boards with Bo still on the platform: the train waits for him (60 s, transit_schedule_departure).
	r.do(ada, map[string]any{"t": "subway_board", "station": 1, "line": "forwards"})
	tr := last(t, drain(ada), "subway_train")
	street := tr["street"].(string)
	if tr["phase"] != "boarding" || tr["initial"] != true || tr["need"].(float64) != 2 || tr["depart_in"].(float64) != 60000 {
		t.Fatalf("a new train waits for riders: %v", tr)
	}
	// Bo can't walk onto it without boarding.
	r.join(bo, street, 0)
	if e := last(t, drain(bo), "error"); e["code"] != "private_street" {
		t.Fatalf("the train is its riders': %v", e)
	}
	r.join(ada, street, 0)
	drain(ada)
	r.advance(5 * time.Second)
	r.do(bo, map[string]any{"t": "subway_board", "station": 1, "line": "forwards"})
	tb := last(t, drain(bo), "subway_train")
	if tb["train"] != tr["train"] || len(tb["riders"].([]any)) != 2 || tb["need"].(float64) != 1 {
		t.Fatalf("Bo boards Ada's train: %v", tb)
	}
	// Nobody else on the platform now: the lone-rider wait (10 s from the first boarding) applies.
	if tb["depart_in"].(float64) != 5000 {
		t.Fatalf("with the platform empty the train goes 10 s after its first boarding: %v", tb)
	}
	r.join(bo, street, 50)
	got := drain(ada)
	if j := last(t, got, "joined"); j["player"].(map[string]any)["name"] != "Bo" {
		t.Fatalf("Ada sees Bo in the carriage: %v", got)
	}
	r.do(bo, map[string]any{"t": "chat", "text": "all aboard"})
	if s := last(t, drain(ada), "said"); s["text"] != "all aboard" {
		t.Fatal("and hears him")
	}

	// Departure, travel, arrival at Two.
	r.advance(5 * time.Second)
	if mv := last(t, drain(ada), "subway_train"); mv["phase"] != "moving" || mv["arrive_in"].(float64) != 20000 {
		t.Fatalf("departed: %v", mv)
	}
	r.advance(20 * time.Second)
	at := last(t, drain(bo), "subway_train")
	if at["phase"] != "stopped" || at["stop"].(float64) != 2 || at["depart_in"].(float64) != 10000 {
		t.Fatalf("stopped at Two: %v", at)
	}
	// Bo gets off at Two; Ada rides on.
	r.join(bo, "G2", 0)
	left := drain(ada)
	if l := last(t, left, "subway_train"); len(l["riders"].([]any)) != 1 {
		t.Fatalf("Bo is off: %v", left)
	}
	if len(kinds(left, "left")) != 1 {
		t.Fatal("Ada saw him go")
	}
	r.advance(10 * time.Second)
	r.advance(20 * time.Second)
	if a := last(t, drain(ada), "subway_train"); a["stop"].(float64) != 3 || a["phase"] != "stopped" {
		t.Fatalf("Ada rides on to Three: %v", a)
	}
	// Ada gets off; the empty train goes out of service.
	r.join(ada, "G3", 0)
	if len(m.trains) != 0 || len(m.riderOf) != 0 {
		t.Fatalf("an empty train goes: %v", m.trains)
	}
}

func TestThreeRidersCloseTheDoorsAndLateBoardersJoinAStandingTrain(t *testing.T) {
	r, m := subwayRig(t)
	var cls []*Client
	for i := 1; i <= 4; i++ {
		cl := r.hub.Connect(Claims{Sub: int64(i), Name: fmt.Sprint("P", i)})
		r.join(cl, "G1", 0)
		cls = append(cls, cl)
	}
	for _, cl := range cls[:2] {
		r.do(cl, map[string]any{"t": "subway_board", "station": 1, "line": "forwards"})
	}
	r.advance(2 * time.Second)
	r.do(cls[2], map[string]any{"t": "subway_board", "station": 1, "line": "forwards"})
	tr := last(t, drain(cls[2]), "subway_train")
	if tr["need"].(float64) != 0 || tr["depart_in"].(float64) != 3000 {
		t.Fatalf("min_capacity aboard: the doors close and it goes in 3 s: %v", tr)
	}
	// A Blue Line boarder gets a train of their own.
	r.do(cls[3], map[string]any{"t": "subway_board", "station": 1, "line": "backwards"})
	if b := last(t, drain(cls[3]), "subway_train"); b["train"] == tr["train"] {
		t.Fatal("each line has its own trains")
	}
	for _, cl := range cls[:3] {
		r.join(cl, tr["street"].(string), 0)
	}
	r.advance(3 * time.Second)
	r.advance(20 * time.Second)
	// Standing at Two: someone there boards it (transit_add_instance at arrival).
	eve := r.hub.Connect(Claims{Sub: 9, Name: "Eve"})
	r.join(eve, "G2", 0)
	r.advance(8 * time.Second)
	r.do(eve, map[string]any{"t": "subway_board", "station": 2, "line": "forwards"})
	e := last(t, drain(eve), "subway_train")
	if e["train"] != tr["train"] || e["depart_in"].(float64) != 3000 || e["initial"] != false {
		t.Fatalf("a late boarder joins the standing train and still gets the doors' 3 s: %v", e)
	}
	ids := []string{}
	for id := range m.trains {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) != 2 {
		t.Fatalf("two trains: %v", ids)
	}
}

func TestATrainIsFullAtTenAndADroppedRiderKeepsTheirPlace(t *testing.T) {
	r, m := subwayRig(t)
	var first string
	for i := 1; i <= 11; i++ {
		cl := r.hub.Connect(Claims{Sub: int64(i), Name: fmt.Sprint("P", i)})
		r.join(cl, "G1", 0)
		r.do(cl, map[string]any{"t": "subway_board", "station": 1, "line": "forwards"})
		tr := last(t, drain(cl), "subway_train")
		if i == 1 {
			first = tr["train"].(string)
		}
		if (i <= 10) != (tr["train"] == first) {
			t.Fatalf("rider %d: max_capacity 10: %v", i, tr)
		}
	}
	// A rider's socket drops: they keep the place 15 s, then lose it.
	cl := r.hub.ClientLocked(2)
	r.hub.Disconnect(cl)
	r.advance(10 * time.Second)
	if m.riderOf[2] == nil {
		t.Fatal("still aboard in the grace")
	}
	back := r.hub.Connect(Claims{Sub: 2, Name: "P2"})
	r.join(back, "TRAIN_"+first, 0)
	if e := kinds(drain(back), "error"); len(e) != 0 {
		t.Fatalf("back aboard after a reconnect: %v", e)
	}
	r.hub.Disconnect(r.hub.ClientLocked(3))
	r.advance(16 * time.Second)
	if m.riderOf[3] != nil {
		t.Fatal("gone after the grace")
	}
}

func TestBoardingNeedsThePlatform(t *testing.T) {
	r, _ := subwayRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.join(ada, "G2", 0)
	drain(ada)
	r.do(ada, map[string]any{"t": "subway_board", "station": 1, "line": "forwards"})
	if got := last(t, drain(ada), "subway_refused"); got["why"] != "street" {
		t.Fatalf("not on that platform: %v", got)
	}
	r.do(ada, map[string]any{"t": "subway_board", "station": 99, "line": "forwards"})
	if got := last(t, drain(ada), "subway_refused"); got["why"] != "station" {
		t.Fatalf("no such station: %v", got)
	}
}
