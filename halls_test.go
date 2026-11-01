package main

import (
	"encoding/json"
	"testing"
	"time"
)

const testHall = "HALL_LIF101O8CDQ1AMU"

func hallsRig(t *testing.T) (*clockRig, *halls) {
	var m *halls
	r := newClockRig(t, func(h *Hub) Module {
		var d hallsData
		if err := json.Unmarshal(hallsJSON, &d); err != nil {
			t.Fatal(err)
		}
		m = newHallsWith(h, d)
		m.rng = func() float64 { return 0.9 } // no strolls, no turning round
		return m
	})
	return r, m
}

func clerkRows(t *testing.T, st map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, c := range st["clerks"].([]any) {
		out = append(out, c.(map[string]any))
	}
	return out
}

func TestTheShippedHallsAreTheThreeStreetHalls(t *testing.T) {
	m := newHalls(NewHub(nil)).(*halls)
	for _, s := range []string{"HALL_LIF101O8CDQ1AMU", "HALL_LDOCV86VHCD245F", "HALL_LIF2E3LVGK82J7Q"} {
		if !m.known[s] {
			t.Fatalf("%s is shared", s)
		}
	}
	if len(m.d.Clerks) != 4 || m.d.Stroll != [2]float64{1606, 2166} || m.d.StrollSpeed != [2]float64{20, 40} || m.d.Reminder != 8000 {
		t.Fatalf("npc_bureaucrat.js idle_onMsg and the four clerks: %+v", m.d)
	}
}

func TestTwoPlayersShareTheHallsBureaucrats(t *testing.T) {
	r, m := hallsRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.join(ada, testHall, 300)
	r.join(bo, testHall, 320)
	st := last(t, drain(bo), "hall_state")
	if rows := clerkRows(t, st); len(rows) != 4 || rows[0]["by"].(float64) != 0 || rows[0]["x"].(float64) != 1640 {
		t.Fatalf("four free bureaucrats at their places: %v", st)
	}
	drain(ada)

	// Ada is greeted by the nearest free bureaucrat; Bo sees it busy with her.
	r.do(ada, map[string]any{"t": "hall_ask", "kind": "greet", "x": 300})
	if g := last(t, drain(ada), "hall_given"); g["i"].(float64) != 0 || g["kind"] != "greet" {
		t.Fatalf("the nearest one greets Ada: %v", g)
	}
	b := clerkRows(t, last(t, drain(bo), "hall_state"))
	if b[0]["by"].(float64) != 1 || b[0]["who"] != "Ada" || b[0]["kind"] != "greet" {
		t.Fatalf("Bo sees it busy with Ada: %v", b)
	}
	// Ada's bureaucrat walks over: Bo is told where.
	r.do(ada, map[string]any{"t": "hall_pose", "i": 0, "x": 900, "face": "left", "anim": "walk1"})
	if p := last(t, drain(bo), "hall_pose"); p["x"].(float64) != 900 || p["face"] != "left" {
		t.Fatalf("pose relayed: %v", p)
	}
	// Bo can't take Ada's bureaucrat: he gets the next nearest.
	r.do(bo, map[string]any{"t": "hall_ask", "kind": "greet", "x": 320})
	if g := last(t, drain(bo), "hall_given"); g["i"].(float64) != 1 {
		t.Fatalf("Bo gets another: %v", g)
	}
	// Nor move it.
	r.do(bo, map[string]any{"t": "hall_pose", "i": 0, "x": 100, "face": "left", "anim": "walk1"})
	if m.rooms[testHall].clerks[0].x != 900 {
		t.Fatal("only its holder moves a bureaucrat")
	}

	// Two more helpers taken (Ada's help + promise): the hall is full and Eve waits in line.
	r.do(ada, map[string]any{"t": "hall_done", "i": 0, "x": 1640})
	r.do(ada, map[string]any{"t": "hall_ask", "kind": "help", "x": 1000})
	r.do(ada, map[string]any{"t": "hall_ask", "kind": "will"})
	given := kinds(drain(ada), "hall_given")
	if len(given) != 2 || given[1]["kind"] != "will" || given[1]["i"].(float64) != 2 {
		t.Fatalf("a hand-off names the first free one (getRandomBureaucrat): %v", given)
	}
	r.do(bo, map[string]any{"t": "hall_ask", "kind": "will"})
	drain(bo)
	eve := r.hub.Connect(Claims{Sub: 3, Name: "Eve"})
	r.join(eve, testHall, 300)
	drain(eve)
	r.do(eve, map[string]any{"t": "hall_ask", "kind": "greet", "x": 300})
	if w := last(t, drain(eve), "hall_wait"); w["place"].(float64) != 1 {
		t.Fatalf("Eve waits in line: %v", w)
	}
	// A third bureaucrat for one visit is refused.
	r.do(ada, map[string]any{"t": "hall_ask", "kind": "greet"})
	if g := last(t, drain(ada), "hall_given"); g["i"].(float64) != -1 {
		t.Fatalf("a visit holds two at most: %v", g)
	}
	// Bo leaves the hall: his two go back and the line's first is served.
	r.do(bo, map[string]any{"t": "hall_pose", "i": 1, "x": 800, "face": "left", "anim": "walk1"})
	r.join(bo, "GLI32G3NUTD100I", 0)
	if g := last(t, drain(eve), "hall_given"); g["kind"] != "greet" || g["i"].(float64) != 1 {
		t.Fatalf("Eve is greeted by the nearest one freed: %v", g)
	}
	rows := clerkRows(t, last(t, drain(ada), "hall_state"))
	if rows[1]["who"] != "Eve" || rows[3]["by"].(float64) != 0 || rows[0]["who"] != "Ada" || rows[2]["kind"] != "will" {
		t.Fatalf("Eve has Bo's first, his promise is free: %v", rows)
	}
}

func TestATurnRunsOutAndAPlayerGoneFreesTheirBureaucrat(t *testing.T) {
	r, m := hallsRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.join(ada, testHall, 300)
	r.join(bo, testHall, 300)
	r.do(ada, map[string]any{"t": "hall_ask", "kind": "help", "x": 1000})
	r.do(ada, map[string]any{"t": "hall_pose", "i": 0, "x": 1100, "face": "left", "anim": "walk2"})
	drain(ada)
	r.advance(30 * time.Second)
	r.do(ada, map[string]any{"t": "hall_touch", "i": 0})
	r.advance(45 * time.Second)
	if m.rooms[testHall].clerks[0].by != 1 {
		t.Fatal("a touch keeps the turn")
	}
	r.advance(16 * time.Second)
	if l := last(t, drain(ada), "hall_lost"); l["i"].(float64) != 0 || l["why"] != "turn" {
		t.Fatalf("lost the turn: %v", l)
	}
	if c := m.rooms[testHall].clerks[0]; c.by != 0 || c.from.IsZero() || c.to != 1640 {
		t.Fatalf("free again, walking back to its place (returnToStart): %+v", c)
	}
	// Bo's bureaucrat goes when his socket does.
	r.do(bo, map[string]any{"t": "hall_ask", "kind": "help", "x": 1000})
	drain(bo)
	r.hub.Disconnect(bo)
	for _, c := range m.rooms[testHall].clerks {
		if c.by != 0 {
			t.Fatal("offline frees it")
		}
	}
	// Everyone gone: the hall closes.
	r.hub.Disconnect(ada)
	if len(m.rooms) != 0 {
		t.Fatal("an empty hall is dropped")
	}
}

func TestIdleBureaucratsStrollTheSameForEveryone(t *testing.T) {
	r, m := hallsRig(t)
	m.rng = func() float64 { return 0.1 } // stroll every interval
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.join(ada, testHall, 300)
	drain(ada)
	r.advance(time.Second)
	rows := clerkRows(t, last(t, drain(ada), "hall_state"))
	if rows[0]["to"] == nil || rows[0]["speed"].(float64) != 22 || rows[0]["to"].(float64) != 1662 || rows[0]["anim"] != "walk1" {
		t.Fatalf("idle_onMsg: a stroll at 20-40 px/s: %v", rows[0])
	}
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.join(bo, testHall, 300)
	if b := clerkRows(t, last(t, drain(bo), "hall_state")); b[0]["to"] != rows[0]["to"] {
		t.Fatalf("a newcomer sees the same walk: %v", b[0])
	}
}
