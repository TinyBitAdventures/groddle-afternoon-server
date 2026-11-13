package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	lothaStreet   = "GHV88RQU60B3Q0G"
	danglesStreet = "GHF70EIUS0D3RDM"
	koulatoStreet = "GIF10RP1DBU11NU"
)

// toyRig is a hub with only the street toys module, a clock the test moves and fake timers.
type toyRig struct {
	t      *testing.T
	hub    *Hub
	mod    *streetToys
	now    time.Time
	timers []*toyTimer
	placed []placedStack
	place  bool
}

type toyTimer struct {
	at      time.Time
	fn      func()
	stopped bool
}

func newToyRig(t *testing.T) *toyRig {
	t.Helper()
	r := &toyRig{t: t, now: time.Unix(1_800_000_000, 0), place: true}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { m := newStreetToys(h); r.mod = m.(*streetToys); return m }}
	t.Cleanup(func() { modules = saved })
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		ft := &toyTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	r.hub.provided["drops.place"] = func(street string, by int64, name string, x, y float64, it tradeItem) bool {
		if !r.place {
			return false
		}
		r.placed = append(r.placed, placedStack{street: street, by: by, x: x, item: it})
		return true
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

func (r *toyRig) move(cl *Client, x, y float64) {
	r.now = r.now.Add(60 * time.Millisecond) // past the hub's move rate
	send(r.hub, cl, map[string]any{"t": "move", "x": x, "y": y, "dir": 1, "anim": "stand"})
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

// onJar is a player's feet on jar i (10 px above the box's foot, as the game's tests stand them).
func onJar(i int) (float64, float64) { return koulatoJars[i].X, koulatoJars[i].Y - 10 }

// Koulato Cluster: every arrival counts; the 30th brings the jars out for everyone.
func TestKoulatoCountsEveryArrivalAndTheThirtiethBringsTheJars(t *testing.T) {
	r := newToyRig(t)
	ada := joinAt(r.hub, 1, "Ada", koulatoStreet, 1000, 1100)
	if st := lastOf(t, ada, "toy_state"); st["arrivals"] != 1.0 || st["out"] != false {
		t.Fatalf("first arrival %v", st)
	}
	// Bo walks on and off 28 times: each is an arrival (a join from elsewhere).
	bo := joinAt(r.hub, 2, "Bo", "GA5101HF7F429V5", 0, 0)
	for i := 0; i < 28; i++ {
		send(r.hub, bo, map[string]any{"t": "join", "street": koulatoStreet, "x": 900, "y": 1100})
		send(r.hub, bo, map[string]any{"t": "join", "street": "GA5101HF7F429V5", "x": 0, "y": 0})
		frames(bo) // a full send queue drops a client as too slow
		frames(ada)
	}
	frames(ada)
	frames(bo)
	if n, out := r.hub.ProvidedLocked("toys.koulato").(func(string) (int, bool))(koulatoStreet); n != 29 || out {
		t.Fatalf("after 29 arrivals: %d %v", n, out)
	}
	cy := joinAt(r.hub, 3, "Cy", koulatoStreet, 900, 1100)
	if got := playedOf(ada); len(got) != 1 || !sameStrings(eventsOf(got[0]), []string{"place_jars"}) || got[0]["out"] != true {
		t.Fatalf("the street saw %v", got)
	}
	if st := lastOf(t, cy, "toy_state"); st["out"] != true || st["arrivals"] != 0.0 {
		t.Fatalf("the 30th arrival's state %v", st)
	}
	// A rejoin after a dropped socket is no arrival; a fresh visit after the grace is.
	r.hub.Disconnect(cy)
	r.now = r.now.Add(10 * time.Second)
	cy = joinAt(r.hub, 3, "Cy", koulatoStreet, 900, 1100)
	if st := lastOf(t, cy, "toy_state"); st["arrivals"] != 0.0 || st["out"] != true {
		t.Fatalf("a rejoin counted: %v", st)
	}
	// A new socket replacing the old one (no disconnect first) is a rejoin too.
	cy = joinAt(r.hub, 3, "Cy", koulatoStreet, 900, 1100)
	if st := lastOf(t, cy, "toy_state"); st["arrivals"] != 0.0 {
		t.Fatalf("a replaced socket's rejoin counted: %v", st)
	}
	r.hub.Disconnect(cy)
	r.now = r.now.Add(3 * time.Minute)
	cy = joinAt(r.hub, 3, "Cy", koulatoStreet, 900, 1100)
	if st := lastOf(t, cy, "toy_state"); st["arrivals"] != 1.0 {
		t.Fatalf("a visit after the grace: %v", st)
	}
}

// putOut sets a street's jars out without 30 joins.
func (r *toyRig) putOut(street string) {
	r.hub.mu.Lock()
	st := r.mod.street(street)
	st.jarsOut = true
	r.hub.mu.Unlock()
}

// Three players on the three jars complete it once; each gets the jar at their own jar.
func TestKoulatoThreePlayersOnTheJarsGetOneJarEachOnce(t *testing.T) {
	r := newToyRig(t)
	r.putOut(koulatoStreet)
	ada := joinAt(r.hub, 1, "Ada", koulatoStreet, 1000, 1100)
	bo := joinAt(r.hub, 2, "Bo", koulatoStreet, 1100, 1100)
	cy := joinAt(r.hub, 3, "Cy", koulatoStreet, 1200, 1100)
	if st := lastOf(t, cy, "toy_state"); st["out"] != true {
		t.Fatalf("jars out on arrival %v", st)
	}
	frames(ada)
	// Ada alone on jar 2: it lights for everyone, and goes out when she steps off.
	r.move(ada, koulatoJars[1].X, koulatoJars[1].Y-10)
	r.advance(250 * time.Millisecond)
	if got := playedOf(bo); len(got) != 1 || !sameStrings(eventsOf(got[0]), []string{"jar2_on"}) {
		t.Fatalf("jar 2 %v", got)
	}
	r.move(ada, 1000, 1100)
	r.advance(250 * time.Millisecond)
	if got := playedOf(cy); len(got) != 2 || !sameStrings(eventsOf(got[1]), []string{"jar2_off"}) {
		t.Fatalf("off again %v", got)
	}
	// One Glitch can stand on one jar only: Ada's jump from jar to jar never lights all three.
	for i := 0; i < 3; i++ {
		x, y := onJar(i)
		r.move(ada, x, y)
		r.advance(250 * time.Millisecond)
	}
	if len(r.placed) != 0 {
		t.Fatal("one player completed it")
	}
	// Ada on 3, Bo on 1, Cy on 2: all three at once.
	x, y := onJar(0)
	r.move(bo, x, y)
	r.advance(250 * time.Millisecond)
	frames(ada)
	x, y = onJar(1)
	r.move(cy, x, y)
	r.advance(250 * time.Millisecond)
	got := playedOf(ada)
	if len(got) != 1 || !sameStrings(eventsOf(got[0]), []string{"jar2_on", "complete", "turn_off"}) || got[0]["out"] != false {
		t.Fatalf("the completion %v", got)
	}
	jars := got[0]["jars"].([]any)
	want := []float64{2, 3, 1}
	for i, j := range jars {
		m := j.(map[string]any)
		if m["n"] != float64(i+1) || m["id"] != want[i] || m["own"] != false {
			t.Fatalf("jar %d %v", i+1, m)
		}
	}
	if len(r.placed) != 3 {
		t.Fatalf("placed %v", r.placed)
	}
	for i, p := range r.placed {
		if p.street != koulatoStreet || p.by != int64(want[i]) || p.x != koulatoJars[i].DropX || p.item.C != "firefly_jar" || p.item.N != 1 || p.item.R == nil || *p.item.R != 7 {
			t.Fatalf("placed %d %+v", i, p)
		}
	}
	// Standing on, moving, resending, reconnecting: never again until the next 30 arrivals.
	r.advance(2 * time.Second)
	r.click(ada, "lotha_note_1")
	r.hub.Disconnect(bo)
	bo = joinAt(r.hub, 2, "Bo", koulatoStreet, x, y)
	r.move(bo, koulatoJars[0].X, koulatoJars[0].Y-10)
	r.advance(2 * time.Second)
	if len(r.placed) != 3 {
		t.Fatalf("paid again: %v", r.placed)
	}
	if st := lastOf(t, bo, "toy_state"); st["out"] != false {
		t.Fatalf("after completion %v", st)
	}
}

// No shared drop (no ledger, a full street): each player puts their own jar down.
func TestKoulatoWithoutSharedDropsTellsEachPlayerToPutTheirOwnDown(t *testing.T) {
	r := newToyRig(t)
	r.place = false
	r.putOut(koulatoStreet)
	var cls []*Client
	for i := 0; i < 3; i++ {
		x, y := onJar(i)
		cls = append(cls, joinAt(r.hub, int64(i+1), "P", koulatoStreet, x, y))
	}
	r.advance(250 * time.Millisecond)
	got := playedOf(cls[0])
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	for _, j := range got[0]["jars"].([]any) {
		if j.(map[string]any)["own"] != true {
			t.Fatalf("jar %v", j)
		}
	}
}

// The count and the jars out survive a restart.
func TestKoulatoCountSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toys.json")
	toysFileForTest = path
	t.Cleanup(func() { toysFileForTest = "" })
	r := newToyRig(t)
	for i := 0; i < 4; i++ {
		joinAt(r.hub, int64(i+1), "P", koulatoStreet, 0, 0)
	}
	r.advance(2 * time.Second)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f toysFile
	if json.Unmarshal(b, &f) != nil || f.Koulato[koulatoStreet].Arrivals != 4 {
		t.Fatalf("file %s", b)
	}
	r2 := newToyRig(t)
	cl := joinAt(r2.hub, 9, "Z", koulatoStreet, 0, 0)
	if st := lastOf(t, cl, "toy_state"); st["arrivals"] != 5.0 {
		t.Fatalf("after a restart %v", st)
	}
}
