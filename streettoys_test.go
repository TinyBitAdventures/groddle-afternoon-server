package main

import (
	"testing"
	"time"
)

const (
	lothaStreet   = "GHV88RQU60B3Q0G"
	danglesStreet = "GHF70EIUS0D3RDM"
)

// toyRig is a hub with only the street toys module, a clock the test moves and fake timers.
type toyRig struct {
	t      *testing.T
	hub    *Hub
	mod    *streetToys
	now    time.Time
	timers []*toyTimer
}

type toyTimer struct {
	at      time.Time
	fn      func()
	stopped bool
}

func newToyRig(t *testing.T) *toyRig {
	t.Helper()
	r := &toyRig{t: t, now: time.Unix(1_800_000_000, 0)}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { m := newStreetToys(h); r.mod = m.(*streetToys); return m }}
	t.Cleanup(func() { modules = saved })
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		ft := &toyTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	return r
}

// advance moves the clock by d, running each timer that falls due (in time order; h.After's own
// wrapper takes the hub lock).
func (r *toyRig) advance(d time.Duration) {
	end := r.now.Add(d)
	for {
		var next *toyTimer
		for _, ft := range r.timers {
			if !ft.stopped && !ft.at.After(end) && (next == nil || ft.at.Before(next.at)) {
				next = ft
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

func (r *toyRig) click(cl *Client, toy string) {
	send(r.hub, cl, map[string]any{"t": "toy_click", "toy": toy})
}

func playedOf(cl *Client) []map[string]any {
	var out []map[string]any
	for _, m := range frames(cl) {
		if m["t"] == "toy_played" {
			out = append(out, m)
		}
	}
	return out
}

func eventsOf(m map[string]any) []string {
	var out []string
	for _, e := range m["events"].([]any) {
		out = append(out, e.(string))
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Lotha Harte: everyone hears each note, the street keeps one order, the heart lights for everyone.
func TestLothaNotesAreHeardByTheStreetAndTheHeartLightsForEveryone(t *testing.T) {
	r := newToyRig(t)
	ada := joinAt(r.hub, 1, "Ada", lothaStreet, 5400, 900)
	bo := joinAt(r.hub, 2, "Bo", lothaStreet, 5500, 900)
	if st := lastOf(t, ada, "toy_state"); st["toys"] != "lotha" || st["lit"] != false {
		t.Fatalf("Ada's state on arrival %v", st)
	}
	frames(bo)
	// Two players play the order between them: Ada the first note, Bo the second and third.
	r.click(ada, "lotha_note_1")
	r.now = r.now.Add(time.Second)
	r.click(bo, "lotha_note_2")
	r.now = r.now.Add(time.Second)
	r.click(bo, "lotha_note_3")
	got := playedOf(ada)
	if len(got) != 3 || !sameStrings(eventsOf(got[0]), []string{"first_note"}) || got[0]["by"] != 1.0 ||
		!sameStrings(eventsOf(got[2]), []string{"third_note", "correct_order"}) || got[2]["by"] != 2.0 {
		t.Fatalf("Ada heard %v", got)
	}
	if b := playedOf(bo); len(b) != 3 {
		t.Fatalf("the clicker hears their own notes through the server too: %v", b)
	}
	// A player arriving while it is lit sees the heart lit.
	cy := joinAt(r.hub, 3, "Cy", lothaStreet, 5000, 900)
	if st := lastOf(t, cy, "toy_state"); st["lit"] != true {
		t.Fatalf("a mid-state arrival %v", st)
	}
	// While lit (the lock), notes still sound but play no order.
	r.now = r.now.Add(time.Second)
	r.click(cy, "lotha_note_1")
	r.now = r.now.Add(time.Second)
	r.click(cy, "lotha_note_2")
	r.now = r.now.Add(time.Second)
	r.click(cy, "lotha_note_3")
	for _, m := range playedOf(ada) {
		if len(eventsOf(m)) != 1 {
			t.Fatalf("a note during the lock ran %v", m)
		}
	}
	// light_fade 22 s after correct_order, to everyone there.
	r.advance(19 * time.Second)
	fade := playedOf(bo)
	if len(fade) != 4 || !sameStrings(eventsOf(fade[3]), []string{"light_fade"}) || fade[3]["by"] != 0.0 {
		t.Fatalf("the fade %v", fade)
	}
	if st := lastOf(t, joinAt(r.hub, 4, "Di", lothaStreet, 5000, 900), "toy_state"); st["lit"] != false {
		t.Fatalf("after the fade %v", st)
	}
	// The lock holds until reset, 3.5 s after light_fade; then a new order lights it again.
	r.now = r.now.Add(4 * time.Second)
	frames(ada)
	r.click(ada, "lotha_note_1")
	r.now = r.now.Add(time.Second)
	r.click(ada, "lotha_note_2")
	r.now = r.now.Add(time.Second)
	r.click(ada, "lotha_note_3")
	if last := playedOf(ada); !sameStrings(eventsOf(last[2]), []string{"third_note", "correct_order"}) {
		t.Fatalf("after the reset %v", last)
	}
}

func TestLothaWrongOrderClearsAndClicksAreChecked(t *testing.T) {
	r := newToyRig(t)
	ada := joinAt(r.hub, 1, "Ada", lothaStreet, 5400, 900)
	frames(ada)
	for _, toy := range []string{"lotha_note_2", "lotha_note_1", "lotha_note_3"} {
		r.click(ada, toy)
		r.now = r.now.Add(time.Second)
	}
	// second_note with no first clears; first_note then third_note: note2 is missing.
	for _, m := range playedOf(ada) {
		if len(eventsOf(m)) != 1 {
			t.Fatalf("a wrong order lit the heart: %v", m)
		}
	}
	// Too soon after the last click, an unknown toy, a toy of another street: nothing.
	r.click(ada, "lotha_note_1")
	r.click(ada, "lotha_note_2")
	r.now = r.now.Add(time.Second)
	r.click(ada, "icicle_light_1")
	r.click(ada, "nope")
	if got := playedOf(ada); len(got) != 1 {
		t.Fatalf("checked clicks played %v", got)
	}
	// Off the street: nothing.
	bo := joinAt(r.hub, 2, "Bo", "GA5101HF7F429V5", 0, 0)
	r.now = r.now.Add(time.Second)
	r.click(bo, "lotha_note_1")
	if got := playedOf(ada); len(got) != 0 {
		t.Fatalf("a click from another street was heard: %v", got)
	}
	noneOf(t, bo, "toy_state")
}

// Icicle Dangles: a light goes out and on for everyone, with its 700 ms lock.
func TestDanglesLightsAreTheStreets(t *testing.T) {
	r := newToyRig(t)
	ada := joinAt(r.hub, 1, "Ada", danglesStreet, 1000, 900)
	bo := joinAt(r.hub, 2, "Bo", danglesStreet, 1200, 900)
	frames(ada)
	frames(bo)
	r.click(ada, "icicle_light_2")
	if got := playedOf(bo); len(got) != 1 || !sameStrings(eventsOf(got[0]), []string{"shroom_light_2_off"}) {
		t.Fatalf("Bo saw %v", got)
	}
	// Bo clicks the same light within 700 ms: locked, nothing.
	r.now = r.now.Add(300 * time.Millisecond)
	r.click(bo, "icicle_light_2")
	if got := playedOf(ada); len(got) != 1 {
		t.Fatalf("a locked light answered %v", got)
	}
	cy := joinAt(r.hub, 3, "Cy", danglesStreet, 1000, 900)
	if st := lastOf(t, cy, "toy_state"); st["toys"] != "dangles" || st["lights"].([]any)[1] != false || st["lights"].([]any)[0] != true {
		t.Fatalf("a mid-state arrival %v", st)
	}
	r.now = r.now.Add(time.Second)
	r.click(bo, "icicle_light_2")
	if got := playedOf(ada); len(got) != 1 || !sameStrings(eventsOf(got[0]), []string{"shroom_light_2_on"}) {
		t.Fatalf("on again %v", got)
	}
}
