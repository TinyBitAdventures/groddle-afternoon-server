package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDropSite stands in for the site's groddle/v1/ledger/drop routes.
type fakeDropSite struct {
	drops  []dropCall
	placed []dropCall // G605: stacks the street made
	takes  []string
	eats   []string
	ground []dropRow
	err    error
	answer dropAnswer       // for drops
	took   map[string]bool  // stacks the site has given to someone
	silent bool             // no answer at all
	lose   bool             // a part settles at the site but its answer never comes back
	count  map[string]int   // a stack's count at the site, once a part was asked of it
	parts  map[string]int64 // <op>-t<from> -> who took that part
}

func (f *fakeDropSite) drop(c dropCall) dropAnswer {
	f.drops = append(f.drops, c)
	if f.silent {
		return dropAnswer{}
	}
	return f.answer
}

func (f *fakeDropSite) dropTake(op string, id int64, why string, n, from int) dropAnswer {
	if n > 0 {
		return f.dropTakePart(op, id, n, from)
	}
	f.takes = append(f.takes, fmt.Sprintf("%s:%d:%s", op, id, why))
	if f.silent {
		return dropAnswer{}
	}
	if f.took[op] {
		return dropAnswer{Status: "refused", Why: "gone"}
	}
	f.took[op] = true
	return dropAnswer{Status: "settled", Op: op + "-t"}
}

// dropTakePart is LedgerDrop::takePart: each part recorded once as <op>-t<from>.
func (f *fakeDropSite) dropTakePart(op string, id int64, n, from int) dropAnswer {
	f.takes = append(f.takes, fmt.Sprintf("%s:%d:pickup:%d/%d", op, id, n, from))
	if f.silent {
		return dropAnswer{}
	}
	if f.count == nil {
		f.count, f.parts = map[string]int{}, map[string]int64{}
	}
	if _, ok := f.count[op]; !ok {
		f.count[op] = from
	}
	left := f.count[op]
	credit := fmt.Sprintf("%s-t%d", op, from)
	if who, ok := f.parts[credit]; ok {
		if who == id {
			return dropAnswer{Status: "settled", Op: credit, N: &left}
		}
		return dropAnswer{Status: "refused", Why: "stale", N: &left}
	}
	if f.took[op] {
		return dropAnswer{Status: "refused", Why: "gone"}
	}
	if left != from {
		return dropAnswer{Status: "refused", Why: "stale", N: &left}
	}
	left -= n
	f.count[op], f.parts[credit] = left, id
	if f.lose {
		return dropAnswer{}
	}
	return dropAnswer{Status: "settled", Op: credit, N: &left}
}

func (f *fakeDropSite) dropsOnGround() ([]dropRow, error) { return f.ground, f.err }

func (f *fakeDropSite) dropPlace(c dropCall) dropAnswer {
	f.placed = append(f.placed, c)
	if f.silent {
		return dropAnswer{}
	}
	return dropAnswer{Status: "settled"}
}

func (f *fakeDropSite) dropEat(op string, from int) dropAnswer {
	f.eats = append(f.eats, fmt.Sprintf("%s:%d", op, from))
	if f.silent {
		return dropAnswer{}
	}
	if f.took[op] {
		return dropAnswer{Status: "refused", Why: "gone"}
	}
	n := from - 1
	return dropAnswer{Status: "settled", N: &n}
}

type dropRig struct {
	t       *testing.T
	hub     *Hub
	m       *drops
	site    *fakeDropSite
	now     time.Time
	timers  []*rigTimer
	pending []func()
}

const dropStreet = "LA5101HF7F429V"

func newDropRig(t *testing.T) *dropRig {
	r := &dropRig{t: t, now: time.Unix(1_700_000_000, 0), site: &fakeDropSite{answer: dropAnswer{Status: "settled"}, took: map[string]bool{}}}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.m = newDrops(h).(*drops); return r.m }}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	r.m.site = r.site
	r.m.loaded = true
	r.m.spawn = func(fn func()) { r.pending = append(r.pending, fn) }
	n := 0
	r.m.newID = func() string { n++; return fmt.Sprintf("%016x", n) }
	return r
}

func (r *dropRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

func (r *dropRig) do(cl *Client, msg map[string]any) {
	r.now = r.now.Add(time.Second)
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *dropRig) at(cl *Client, street string, x, y float64) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": x, "y": y})
}

func (r *dropRig) move(cl *Client, x, y float64) {
	r.do(cl, map[string]any{"t": "move", "x": x, "y": y})
}

func (r *dropRig) settle() {
	for len(r.pending) > 0 {
		fn := r.pending[0]
		r.pending = r.pending[1:]
		fn()
	}
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if tm.d == 0 && !tm.stopped {
			tm.stopped = true
			tm.fn()
		}
	}
}

// fire runs every live timer of duration d once.
func (r *dropRig) fire(d time.Duration) int {
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

// trio puts Ada (7), Bo (9) and Cy (11) on one street, Ada at x 1000.
func (r *dropRig) trio() (ada, bo, cy *Client) {
	ada, bo, cy = r.connect(7, "Ada"), r.connect(9, "Bo"), r.connect(11, "Cy")
	r.at(ada, dropStreet, 1000, -100)
	r.at(bo, dropStreet, 1300, -100)
	r.at(cy, dropStreet, 1400, -100)
	drain(ada)
	drain(bo)
	drain(cy)
	return
}

// dropApples: Ada drops 3 apples at her feet and the site agrees.
func (r *dropRig) dropApples(ada *Client) string {
	r.do(ada, map[string]any{"t": "ground_drop", "ref": "d1", "item": map[string]any{"c": "apple", "n": 3}, "x": 1050, "y": -120})
	r.settle()
	return r.site.drops[len(r.site.drops)-1].Op
}

func TestADropSettlesAtTheSiteAndEveryoneOnTheStreetSeesIt(t *testing.T) {
	r := newDropRig(t)
	ada, bo, cy := r.trio()
	r.do(ada, map[string]any{"t": "ground_drop", "ref": "d1", "item": map[string]any{"c": "apple", "n": 3}, "x": 1050, "y": -120})
	if got := append(drain(ada), drain(bo)...); len(got) != 0 {
		t.Fatalf("nothing until the site answers: %v", got)
	}
	r.settle()
	if len(r.site.drops) != 1 {
		t.Fatalf("one call: %v", r.site.drops)
	}
	c := r.site.drops[0]
	if c.Op != "drop-0000000000000001" || c.ID != 7 || c.Street != dropStreet || c.X != 1050 || c.Y != -120 || c.Item.C != "apple" || c.Item.N != 3 {
		t.Fatalf("the call: %+v", c)
	}
	var mine map[string]any
	for _, f := range drain(ada) {
		if f["t"] == "ground_dropped" {
			mine = f
		}
	}
	if mine == nil || mine["status"] != "settled" || mine["op"] != c.Op || mine["ref"] != "d1" || mine["stack"].(map[string]any)["id"] != c.Op {
		t.Fatalf("Ada is told: %v", mine)
	}
	for _, cl := range []*Client{bo, cy} {
		f := only(t, drain(cl), "ground_added")
		s := f["stack"].(map[string]any)
		if s["id"] != c.Op || s["c"] != "apple" || s["n"] != float64(3) || s["by"].(map[string]any)["name"] != "Ada" {
			t.Fatalf("the street sees: %v", f)
		}
	}
	// Someone arriving later sees it lying there.
	dee := r.connect(13, "Dee")
	r.at(dee, dropStreet, 0, 0)
	st := pick(t, drain(dee), "ground_state")
	if list := st["stacks"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != c.Op {
		t.Fatalf("Dee sees: %v", st)
	}
	if n := r.hub.ProvidedLocked("drops.count").(func(string) int)(dropStreet); n != 1 {
		t.Fatalf("drops.count: %d", n)
	}
}

func TestADropFarFromTheDropperLandsAtTheirFeet(t *testing.T) {
	r := newDropRig(t)
	ada, _, _ := r.trio()
	r.do(ada, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "apple", "n": 1}, "x": 5000, "y": -120})
	r.settle()
	if c := r.site.drops[0]; c.X != 1000 || c.Y != -120 {
		t.Fatalf("takeable.js 166-167: x %d y %d", c.X, c.Y)
	}
}

func TestDropsFollowTheRulesForWhatIsShared(t *testing.T) {
	r := newDropRig(t)
	ada, _, _ := r.trio()
	cases := []struct {
		item map[string]any
		why  string
		text string
	}{
		{map[string]any{"c": "greeter_twig", "n": 1}, "soulbound", "This item is locked to you and can't be dropped."},
		{map[string]any{"c": "admin_widget", "n": 1}, "type", dropTextCant},
		{map[string]any{"c": "apple", "n": 0}, "count", dropTextShort},
		{map[string]any{"c": "apple", "n": 101}, "count", dropTextShort},
	}
	for _, c := range cases {
		r.do(ada, map[string]any{"t": "ground_drop", "item": c.item})
		f := only(t, drain(ada), "ground_dropped")
		if f["status"] != "refused" || f["why"] != c.why || f["text"] != c.text {
			t.Fatalf("%v: %v", c.item, f)
		}
	}
	// A private place keeps each player's drops their own.
	r.at(ada, "HOME_7", 0, 0)
	st := pick(t, drain(ada), "ground_state")
	if len(st["stacks"].([]any)) != 0 {
		t.Fatalf("a home shows no shared stacks: %v", st)
	}
	r.do(ada, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "apple", "n": 1}})
	if f := only(t, drain(ada), "ground_dropped"); f["why"] != "private" {
		t.Fatalf("in a home: %v", f)
	}
	if len(r.site.drops) != 0 {
		t.Fatalf("the site was never asked: %v", r.site.drops)
	}
}

func TestOneDropAtATimeAndAFullStreet(t *testing.T) {
	r := newDropRig(t)
	ada, _, _ := r.trio()
	r.do(ada, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "apple", "n": 1}})
	r.do(ada, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "apple", "n": 1}})
	if f := only(t, drain(ada), "ground_dropped"); f["why"] != "busy" {
		t.Fatalf("a second drop while the first is weighed: %v", f)
	}
	r.settle()
	drain(ada)
	for i := 1; i < dropStreetMax; i++ {
		r.do(ada, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "apple", "n": 1}})
		r.settle()
		drain(ada)
	}
	drain(ada)
	r.do(ada, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "apple", "n": 1}})
	if f := only(t, drain(ada), "ground_dropped"); f["why"] != "full" {
		t.Fatalf("a full street: %v", f)
	}
}

func TestTheDropperGuardsTheirStackUntilTheyWalkAway(t *testing.T) {
	r := newDropRig(t)
	ada, bo, cy := r.trio()
	op := r.dropApples(ada)
	drain(ada)
	drain(bo)
	drain(cy)
	// Ada stands 50 px from it: Bo may not (takeable.js 36-39).
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op})
	if f := only(t, drain(bo), "ground_taken"); f["status"] != "refused" || f["why"] != "locked" || f["text"] != "You are not allowed to pick this up." {
		t.Fatalf("Bo near Ada's stack: %v", f)
	}
	// She walks away; then she comes back, but the mark is gone for good (41-42).
	r.move(ada, 2000, -100)
	drain(bo)
	drain(cy)
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op})
	if len(r.pending) != 1 {
		t.Fatalf("Bo's pickup goes to the site")
	}
	// Cy asks while the site weighs Bo's.
	r.do(cy, map[string]any{"t": "ground_pickup", "id": op})
	if f := only(t, drain(cy), "ground_taken"); f["why"] != "busy" {
		t.Fatalf("Cy while Bo's is weighed: %v", f)
	}
	r.settle()
	if r.site.takes[0] != op+":9:pickup" {
		t.Fatalf("the take: %v", r.site.takes)
	}
	var got map[string]any
	for _, f := range drain(bo) {
		if f["t"] == "ground_taken" {
			got = f
		}
	}
	if got["status"] != "settled" || got["op"] != op+"-t" || got["item"].(map[string]any)["c"] != "apple" || got["item"].(map[string]any)["n"] != float64(3) {
		t.Fatalf("Bo is told: %v", got)
	}
	g := only(t, drain(cy), "ground_gone")
	if g["id"] != op || g["by"].(map[string]any)["name"] != "Bo" || g["why"] != "picked" {
		t.Fatalf("the street hears: %v", g)
	}
	r.do(cy, map[string]any{"t": "ground_pickup", "id": op})
	if f := only(t, drain(cy), "ground_taken"); f["why"] != "gone" || f["text"] != pickTextGone {
		t.Fatalf("too late: %v", f)
	}
}

func TestTheDropperMayAlwaysPickTheirOwnBackUp(t *testing.T) {
	r := newDropRig(t)
	ada, _, _ := r.trio()
	op := r.dropApples(ada)
	drain(ada)
	r.do(ada, map[string]any{"t": "ground_pickup", "id": op})
	r.settle()
	if r.site.takes[0] != op+":7:pickup" {
		t.Fatalf("the take: %v", r.site.takes)
	}
}

func TestAPickerFarAwayIsToldToGetCloser(t *testing.T) {
	r := newDropRig(t)
	ada, bo, _ := r.trio()
	op := r.dropApples(ada)
	r.move(ada, 3000, -100)
	r.move(bo, 2000, -100)
	drain(bo)
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op})
	f := only(t, drain(bo), "ground_taken")
	if f["why"] != "far" || f["text"] != "Oops. The Apple got too far away for you to pick up. Get closer and then try again." {
		t.Fatalf("main.js:2978: %v", f)
	}
	// Another street cannot pick it up at all.
	cy := r.connect(11, "Cy")
	r.at(cy, "LIF102FDNU11314", 1000, -100)
	drain(cy)
	r.do(cy, map[string]any{"t": "ground_pickup", "id": op})
	if f := only(t, drain(cy), "ground_taken"); f["why"] != "gone" {
		t.Fatalf("from another street: %v", f)
	}
}

func TestTheSiteSaysTheStackIsGoneAndTheStreetForgetsIt(t *testing.T) {
	r := newDropRig(t)
	ada, bo, _ := r.trio()
	op := r.dropApples(ada)
	r.move(ada, 3000, -100)
	r.site.took[op] = true // taken at the site already (a server that crashed mid-pickup)
	drain(bo)
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op})
	r.settle()
	frames := drain(bo)
	var taken, gone map[string]any
	for _, f := range frames {
		switch f["t"] {
		case "ground_taken":
			taken = f
		case "ground_gone":
			gone = f
		}
	}
	if taken["status"] != "refused" || taken["text"] != pickTextGone || gone == nil || gone["by"] != nil {
		t.Fatalf("Bo hears: %v", frames)
	}
	if len(r.m.stacks) != 0 {
		t.Fatalf("forgotten: %v", r.m.stacks)
	}
}

func TestNoAnswerFromTheSiteMovesNothing(t *testing.T) {
	r := newDropRig(t)
	ada, bo, _ := r.trio()
	r.site.silent = true
	r.do(ada, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "apple", "n": 1}})
	r.settle()
	if f := only(t, drain(ada), "ground_dropped"); f["status"] != "refused" || f["why"] != "off" {
		t.Fatalf("no answer: %v", f)
	}
	if got := drain(bo); len(got) != 0 || len(r.m.stacks) != 0 {
		t.Fatalf("nothing on the ground: %v %v", got, r.m.stacks)
	}
}

func TestTheStacksOnTheGroundComeFromTheSiteAfterARestart(t *testing.T) {
	r := newDropRig(t)
	ada := r.connect(7, "Ada")
	r.at(ada, dropStreet, 0, 0)
	drain(ada)
	r.m.loaded = false
	r.site.err = fmt.Errorf("down")
	r.do(ada, map[string]any{"t": "ground_drop", "item": map[string]any{"c": "apple", "n": 1}})
	if f := only(t, drain(ada), "ground_dropped"); f["why"] != "off" {
		t.Fatalf("not loaded yet: %v", f)
	}
	r.settle()
	if r.m.loaded {
		t.Fatalf("still not loaded")
	}
	r.site.err = nil
	r.site.ground = []dropRow{
		{ID: "drop-00000000000000aa", By: 9, Name: "Bo", Street: dropStreet, X: 10, Y: -20, C: "cherry", N: 5, At: r.now.Unix() - 60},
		{ID: "drop-00000000000000bb", By: 9, Name: "Bo", Street: "HOME_9", X: 10, Y: -20, C: "cherry", N: 5, At: r.now.Unix()},
		{ID: "not-a-drop", By: 9, Street: dropStreet, C: "cherry", N: 5},
	}
	if r.fire(dropLoadRetry) != 1 {
		t.Fatalf("the load is tried again")
	}
	r.settle()
	if !r.m.loaded || len(r.m.stacks) != 1 {
		t.Fatalf("loaded: %v", r.m.stacks)
	}
	st := pick(t, drain(ada), "ground_state")
	if list := st["stacks"].([]any); len(list) != 1 || list[0].(map[string]any)["by"].(map[string]any)["name"] != "Bo" {
		t.Fatalf("Ada sees what lay there: %v", st)
	}
}

func TestAStackNobodyPicksUpGoesHomeToItsDropper(t *testing.T) {
	r := newDropRig(t)
	ada, bo, _ := r.trio()
	op := r.dropApples(ada)
	drain(ada)
	drain(bo)
	if r.fire(dropSweepEvery) != 1 || len(r.site.takes) != 0 {
		t.Fatalf("a fresh stack stays: %v", r.site.takes)
	}
	r.now = r.now.Add(dropKeep)
	if r.fire(dropSweepEvery) != 1 {
		t.Fatalf("the sweep runs again")
	}
	r.settle()
	if len(r.site.takes) != 1 || r.site.takes[0] != op+":7:return" {
		t.Fatalf("returned: %v", r.site.takes)
	}
	var back map[string]any
	for _, f := range drain(ada) {
		if f["t"] == "ground_back" {
			back = f
		}
	}
	if back == nil || back["op"] != op+"-t" || back["item"].(map[string]any)["n"] != float64(3) {
		t.Fatalf("Ada hears: %v", back)
	}
	if g := only(t, drain(bo), "ground_gone"); g["why"] != "returned" {
		t.Fatalf("the street hears: %v", g)
	}
	if strings.Contains(fmt.Sprint(r.m.stacks), op) {
		t.Fatalf("gone from the ground")
	}
}

// dropTen: Ada drops 10 apples and walks off, so the others may pick them up.
func (r *dropRig) dropTen(ada *Client) string {
	r.do(ada, map[string]any{"t": "ground_drop", "ref": "d1", "item": map[string]any{"c": "apple", "n": 10}, "x": 1050, "y": -120})
	r.settle()
	r.move(ada, 3000, -100)
	return r.site.drops[len(r.site.drops)-1].Op
}

func TestPickUpHowManyTakesAPartAndTheRestStaysForEveryone(t *testing.T) {
	r := newDropRig(t)
	ada, bo, cy := r.trio()
	op := r.dropTen(ada)
	drain(ada)
	drain(bo)
	drain(cy)
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op, "n": 0})
	if f := only(t, drain(bo), "ground_taken"); f["why"] != "bad" || len(r.site.takes) != 0 {
		t.Fatalf("none is not a count: %v", f)
	}
	// Bo asks for 4 of the 10 (main.js 3227-3240: the stack splits, the rest stays down).
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op, "n": 4})
	// Cy asks while the site weighs Bo's part: one at a time on a stack.
	r.do(cy, map[string]any{"t": "ground_pickup", "id": op, "n": 2})
	if f := only(t, drain(cy), "ground_taken"); f["why"] != "busy" {
		t.Fatalf("Cy while Bo's part is weighed: %v", f)
	}
	r.settle()
	if len(r.site.takes) != 1 || r.site.takes[0] != op+":9:pickup:4/10" {
		t.Fatalf("the part asked of the site: %v", r.site.takes)
	}
	var got, left map[string]any
	for _, f := range drain(bo) {
		switch f["t"] {
		case "ground_taken":
			got = f
		case "ground_left":
			left = f
		}
	}
	if got["status"] != "settled" || got["op"] != op+"-t10" || got["item"].(map[string]any)["c"] != "apple" || got["item"].(map[string]any)["n"] != float64(4) {
		t.Fatalf("Bo is told: %v", got)
	}
	if left == nil || left["n"] != float64(6) {
		t.Fatalf("Bo sees what is left too: %v", left)
	}
	for _, cl := range []*Client{ada, cy} {
		f := only(t, drain(cl), "ground_left")
		if f["id"] != op || f["n"] != float64(6) || f["street"] != dropStreet || f["by"].(map[string]any)["name"] != "Bo" {
			t.Fatalf("the street hears the new count: %v", f)
		}
	}
	if s := r.m.stacks[op]; s == nil || s.N != 6 {
		t.Fatalf("the stack holds 6: %+v", s)
	}
	// Someone arriving sees 6 lying there.
	dee := r.connect(13, "Dee")
	r.at(dee, dropStreet, 0, 0)
	if st := pick(t, drain(dee), "ground_state"); st["stacks"].([]any)[0].(map[string]any)["n"] != float64(6) {
		t.Fatalf("Dee sees: %v", st)
	}
	// Cy asks for more than lies there: the whole stack, as before (<id>-t).
	r.do(cy, map[string]any{"t": "ground_pickup", "id": op, "n": 9})
	r.settle()
	if r.site.takes[1] != op+":11:pickup" {
		t.Fatalf("the rest goes whole: %v", r.site.takes)
	}
	if g := pick(t, drain(bo), "ground_gone"); g["by"].(map[string]any)["name"] != "Cy" {
		t.Fatalf("the street hears it is gone: %v", g)
	}
}

func TestAPartWaitsForAPiggysBite(t *testing.T) {
	r := newDropRig(t)
	ada, bo, _ := r.trio()
	op := r.dropTen(ada)
	drain(bo)
	r.m.stacks[op].eating = true
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op, "n": 3})
	if f := only(t, drain(bo), "ground_taken"); f["why"] != "busy" || len(r.site.takes) != 0 {
		t.Fatalf("a part names its count, so it waits: %v", f)
	}
}

func TestAPartOfACountNoLongerThereIsRefusedAndTheStreetIsCorrected(t *testing.T) {
	r := newDropRig(t)
	ada, bo, cy := r.trio()
	op := r.dropTen(ada)
	// At the site the stack already holds 5 (a part this server never heard the answer to).
	r.site.count = map[string]int{op: 5}
	r.site.parts = map[string]int64{op + "-t10": 11}
	drain(bo)
	drain(cy)
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op, "n": 4})
	r.settle()
	var got, left map[string]any
	for _, f := range drain(bo) {
		switch f["t"] {
		case "ground_taken":
			got = f
		case "ground_left":
			left = f
		}
	}
	if got["status"] != "refused" || got["why"] != "stale" || got["text"] != pickTextStale || got["item"] != nil {
		t.Fatalf("Bo is told to look again: %v", got)
	}
	if left == nil || left["n"] != float64(5) || left["by"] != nil {
		t.Fatalf("the count is corrected: %v", left)
	}
	if f := only(t, drain(cy), "ground_left"); f["n"] != float64(5) {
		t.Fatalf("for the whole street: %v", f)
	}
	// Asked again, from the count there is: 4 of 5, one left.
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op, "n": 4})
	r.settle()
	if last := r.site.takes[len(r.site.takes)-1]; last != op+":9:pickup:4/5" || r.site.count[op] != 1 || r.m.stacks[op].N != 1 {
		t.Fatalf("the second ask: %v, site %d", r.site.takes, r.site.count[op])
	}
}

func TestALostAnswerAskedAgainTakesNothingMore(t *testing.T) {
	r := newDropRig(t)
	ada, bo, _ := r.trio()
	op := r.dropTen(ada)
	drain(bo)
	// The site settles Bo's 4 but the answer is lost: Bo hears it did not work, the stack still shows 10.
	r.site.lose = true
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op, "n": 4})
	r.settle()
	if f := pick(t, drain(bo), "ground_taken"); f["status"] != "refused" || f["why"] != "off" {
		t.Fatalf("no answer: %v", f)
	}
	if r.m.stacks[op].N != 10 || r.site.count[op] != 6 {
		t.Fatalf("server %d, site %d", r.m.stacks[op].N, r.site.count[op])
	}
	// Bo asks again for 4: the same part (<op>-t10) answers as recorded; nothing more moves.
	r.site.lose = false
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op, "n": 4})
	r.settle()
	got := pick(t, drain(bo), "ground_taken")
	if got["status"] != "settled" || got["op"] != op+"-t10" {
		t.Fatalf("as recorded: %v", got)
	}
	if r.site.count[op] != 6 || r.m.stacks[op].N != 6 {
		t.Fatalf("no double take: site %d, server %d", r.site.count[op], r.m.stacks[op].N)
	}
}

func TestHTTPLedgerSendsAPartTheSameOnEveryTry(t *testing.T) {
	secret := []byte(strings.Repeat("s", 40))
	var mu sync.Mutex
	var bodies []string
	seen := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		seen++
		n := seen
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(502) // a hiccup: tried again with the same ask
			return
		}
		w.Write([]byte(`{"status":"settled","op":"drop-00000000000000aa-t10","item":{"c":"apple","n":4},"n":6}`))
	}))
	defer srv.Close()
	l := &httpLedger{base: srv.URL, secret: secret, client: srv.Client(), now: time.Now, pause: func(time.Duration) {}}
	a := l.dropTake("drop-00000000000000aa", 9, "pickup", 4, 10)
	if a.Status != "settled" || a.Op != "drop-00000000000000aa-t10" || a.N == nil || *a.N != 6 || a.Item.N != 4 {
		t.Fatalf("the answer: %+v", a)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || bodies[0] != `{"from":10,"id":9,"n":4,"op":"drop-00000000000000aa","why":"pickup"}` {
		t.Fatalf("the same body each try: %v", bodies)
	}
	// The whole stack names no count, as before.
	bodies = nil
	l.dropTake("drop-00000000000000aa", 9, "pickup", 0, 0)
	if len(bodies) != 1 || bodies[0] != `{"id":9,"op":"drop-00000000000000aa","why":"pickup"}` {
		t.Fatalf("a whole take: %v", bodies)
	}
}

func TestAnOlderSiteThatTookTheWholeStackEmptiesTheStreet(t *testing.T) {
	r := newDropRig(t)
	ada, bo, cy := r.trio()
	op := r.dropTen(ada)
	drain(bo)
	drain(cy)
	old := &oldDropSite{r.site}
	r.m.site = old
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op, "n": 4})
	r.settle()
	if g := only(t, drain(cy), "ground_gone"); g["by"].(map[string]any)["name"] != "Bo" || r.m.stacks[op] != nil {
		t.Fatalf("the stack went whole: %v", g)
	}
}

// oldDropSite answers a part as a site from before G560 did: the whole stack under <op>-t.
type oldDropSite struct{ *fakeDropSite }

func (o *oldDropSite) dropTake(op string, id int64, why string, n, from int) dropAnswer {
	return o.fakeDropSite.dropTake(op, id, why, 0, 0)
}

// pick is the one frame of a kind among others (a join's street list comes first).
func pick(t *testing.T, frames []map[string]any, kind string) map[string]any {
	t.Helper()
	var out []map[string]any
	for _, f := range frames {
		if f["t"] == kind {
			out = append(out, f)
		}
	}
	if len(out) != 1 {
		t.Fatalf("want one %s, got %v", kind, frames)
	}
	return out[0]
}

// G605: a stack the street makes (a Plop, a Piglet) is placed under the player whose act made it:
// the site records it, everyone on the street sees it, that player's 100 px guard holds it first,
// and its words (a Plop's seeds) are checked and kept.
func TestTheStreetPlacesAStackUnderThePlayerWhoseActMadeIt(t *testing.T) {
	r := newDropRig(t)
	ada, bo, _ := r.trio()
	place := r.m.place
	if !place(dropStreet, 7, "Ada", 1020, -100, tradeItem{C: "plop", N: 1, T: json.RawMessage(`{"seed":"seed_potato","n":40,"x":1}`)}) {
		t.Fatal("the place was not asked")
	}
	r.settle()
	if len(r.site.placed) != 1 || r.site.placed[0].ID != 7 || r.site.placed[0].Item.C != "plop" || string(r.site.placed[0].Item.T) != `{"seed":"seed_potato","n":7}` {
		t.Fatalf("the site was asked %+v", r.site.placed)
	}
	if len(r.site.drops) != 0 {
		t.Fatal("a placed stack is no drop from anyone's ledger")
	}
	added := only(t, drain(bo), "ground_added")
	stack := added["stack"].(map[string]any)
	if stack["c"] != "plop" || stack["by"].(map[string]any)["id"] != float64(7) {
		t.Fatalf("Bo sees %v", stack)
	}
	drain(ada)
	r.do(bo, map[string]any{"t": "ground_pickup", "id": stack["id"]})
	if f := only(t, drain(bo), "ground_taken"); f["why"] != "locked" {
		t.Fatalf("Bo near Ada's Plop: %v", f)
	}
	// Not shared: a private place, or a class the ledger does not carry.
	if place("HOME_7", 7, "Ada", 0, 0, tradeItem{C: "plop", N: 1}) || place(dropStreet, 7, "Ada", 0, 0, tradeItem{C: "nonsense", N: 1}) {
		t.Fatal("placed where nothing is shared")
	}
}
