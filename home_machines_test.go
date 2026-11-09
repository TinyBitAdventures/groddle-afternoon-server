package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeMachines stands in for the site's groddle/v1/homes/machines.
type fakeMachines struct {
	calls  []machineAct
	answer machineAnswer
}

func (f *fakeMachines) homeMachine(a machineAct) machineAnswer {
	f.calls = append(f.calls, a)
	return f.answer
}

type machineRig struct {
	*homesRig
	mach    *homeMachines
	site    *fakeMachines
	pending []func()
}

func newMachineRig(t *testing.T) *machineRig {
	machine := json.RawMessage(`{"id":"hm-00000000000000aa","class":"fuelmaker","rx":120,"fuel":50,"music":true,"job":null,"waiting":[]}`)
	r := &machineRig{homesRig: &homesRig{t: t, now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}}, site: &fakeMachines{answer: machineAnswer{Status: "settled", Machine: machine, Now: 1}}}
	savedSecret := homesSecretForTest
	homesSecretForTest = homesTestSecret
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newHomes(h).(*homes); return r.m },
		func(h *Hub) Module { r.mach = newHomeMachines(h).(*homeMachines); return r.mach },
		func(h *Hub) Module { return r.ign },
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	homesSecretForTest = savedSecret
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	r.mach.site = r.site
	r.mach.spawn = func(fn func()) { r.pending = append(r.pending, fn) }
	n := 0
	r.mach.newID = func() string { n++; return strings.Repeat("0", 15) + string(rune('0'+n)) }
	return r
}

func (r *machineRig) settle() {
	for len(r.pending) > 0 {
		fn := r.pending[0]
		r.pending = r.pending[1:]
		fn()
	}
	r.fire(0)
}

// onHerStreet puts Ada (7) on her own home street, Bo (9) and Cy (11) visiting it.
func (r *machineRig) onHerStreet() (ada, bo, cy *Client) {
	ada, bo, cy = r.connect(7, "Ada"), r.connect(9, "Bo"), r.connect(11, "Cy")
	r.join(ada, home(7))
	r.pass(bo, 7, "Ada", false)
	r.join(bo, home(7))
	r.pass(cy, 7, "Ada", false)
	r.join(cy, home(7))
	drain(ada)
	drain(bo)
	drain(cy)
	return ada, bo, cy
}

func TestTheOwnerSetsAMachineDownAndEveryoneSeesIt(t *testing.T) {
	r := newMachineRig(t)
	ada, bo, cy := r.onHerStreet()
	r.do(ada, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "place", "class": "fuelmaker", "rx": 120, "fuel": 50})
	if got := drain(ada); len(got) != 0 {
		t.Fatalf("nothing until the site answers: %v", got)
	}
	r.settle()
	if len(r.site.calls) != 1 {
		t.Fatalf("one call: %v", r.site.calls)
	}
	c := r.site.calls[0]
	if c.Op != "hm-0000000000000001" || c.Owner != 7 || c.Actor != 7 || c.Verb != "place" || c.Class != "fuelmaker" || c.Rx == nil || *c.Rx != 120 || c.Fuel == nil || *c.Fuel != 50 {
		t.Fatalf("the call names the place: %+v", c)
	}
	f := only(t, drain(ada), "home_machine_done")
	if f["status"] != "settled" || f["req"] != float64(1) || f["op"] != "hm-0000000000000001" || f["machine"].(map[string]any)["class"] != "fuelmaker" {
		t.Fatalf("Ada is answered: %v", f)
	}
	for _, who := range []*Client{bo, cy} {
		a := only(t, drain(who), "home_machine_changed")
		if a["owner"] != float64(7) || a["verb"] != "place" || a["by"].(map[string]any)["name"] != "Ada" || a["machine"].(map[string]any)["rx"] != float64(120) {
			t.Fatalf("the visitors see it: %v", a)
		}
	}
}

func TestOnlyTheOwnerSetsOneDownAndOnlyOnHerStreet(t *testing.T) {
	r := newMachineRig(t)
	ada, bo, _ := r.onHerStreet()
	r.do(bo, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "place", "class": "fuelmaker", "rx": 120})
	if f := only(t, drain(bo), "home_machine_done"); f["why"] != "own" {
		t.Fatalf("a visitor can't set one down on her street: %v", f)
	}
	r.do(bo, map[string]any{"t": "home_machine", "req": 1, "owner": 9, "verb": "place", "class": "fuelmaker", "rx": 120})
	if f := only(t, drain(bo), "home_machine_done"); f["why"] != "away" {
		t.Fatalf("nor on his own while he stands on hers: %v", f)
	}
	r.join(ada, "LA5101HF7F429V")
	drain(ada)
	r.do(ada, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "place", "class": "fuelmaker", "rx": 120})
	if f := only(t, drain(ada), "home_machine_done"); f["why"] != "away" {
		t.Fatalf("never on a public street: %v", f)
	}
	if len(r.site.calls) != 0 {
		t.Fatalf("the site is never asked: %v", r.site.calls)
	}
}

// HOMES2: in the house and on a Tower floor too; the site hears the place, and only those standing there see it.
func TestMachinesStandInTheHouseAndTheTower(t *testing.T) {
	r := newMachineRig(t)
	ada, bo, cy := r.onHerStreet()
	if p, _ := machinePlace("TOWER_7_3", 7); p != "tower:3" {
		t.Fatalf("a Tower floor: %q", p)
	}
	for _, s := range []string{"TOWER_9_3", "HOUSE_9", "HOME_9", "TOWER_7_x", "LA5101HF7F429V", ""} {
		if p, _ := machinePlace(s, 7); p != "" {
			t.Fatalf("%s is not Ada's home: %q", s, p)
		}
	}
	r.join(ada, house(7))
	r.pass(bo, 7, "Ada", true)
	r.join(bo, house(7))
	drain(ada)
	drain(bo)
	drain(cy)
	r.do(ada, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "place", "class": "woodworker", "rx": 300})
	r.settle()
	if c := r.site.calls[0]; c.Place != "house" || c.Verb != "place" {
		t.Fatalf("the site hears it is the house: %+v", c)
	}
	only(t, drain(ada), "home_machine_done")
	if a := only(t, drain(bo), "home_machine_changed"); a["verb"] != "place" {
		t.Fatalf("a key-holder in the house sees it: %v", a)
	}
	if got := drain(cy); len(got) != 0 {
		t.Fatalf("Cy on the street is not told of the house's machine: %v", got)
	}
	r.join(cy, "TOWER_7_2")
	drain(cy)
	drain(ada)
	drain(bo)
	r.do(cy, map[string]any{"t": "home_machine", "req": 2, "owner": 7, "verb": "refuel", "id": "hm-00000000000000aa"})
	r.settle()
	if c := r.site.calls[1]; c.Place != "tower:2" || c.Actor != 11 {
		t.Fatalf("a visitor on her Tower's third floor: %+v", c)
	}
	only(t, drain(cy), "home_machine_done")
	if a := only(t, drain(ada), "home_machine_changed"); a["verb"] != "refuel" {
		t.Fatalf("the owner is told wherever she is: %v", a)
	}
	if got := drain(bo); len(got) != 0 {
		t.Fatalf("Bo in the house is not: %v", got)
	}
}

func TestMachineActsMustBeWellFormed(t *testing.T) {
	r := newMachineRig(t)
	ada, bo, _ := r.onHerStreet()
	cases := []struct {
		who *Client
		msg map[string]any
	}{
		{ada, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "place", "class": "still", "rx": 120}},
		{ada, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "place", "class": "fuelmaker"}},
		{ada, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "place", "class": "fuelmaker", "rx": 999999}},
		{ada, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "place", "class": "fuelmaker", "rx": 0, "fuel": 500}},
		{bo, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "use", "id": "hm-00000000000000aa", "key": "fuel_cell", "qty": 0}},
		{bo, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "use", "id": "nope", "key": "fuel_cell", "qty": 1}},
		{bo, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "dance", "id": "hm-00000000000000aa"}},
		{bo, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "collect"}},
	}
	for _, c := range cases {
		r.do(c.who, c.msg)
		if f := only(t, drain(c.who), "home_machine_done"); f["status"] != "refused" || f["why"] != "bad" {
			t.Fatalf("%v: refused, got %v", c.msg, f)
		}
	}
	if len(r.site.calls) != 0 {
		t.Fatalf("the site is never asked: %v", r.site.calls)
	}
}

func TestAVisitorUsesTheMachineAndTheOwnerAnywhereIsTold(t *testing.T) {
	r := newMachineRig(t)
	ada, bo, cy := r.onHerStreet()
	r.join(ada, "LA5101HF7F429V")
	drain(ada)
	drain(bo)
	drain(cy)
	r.do(bo, map[string]any{"t": "home_machine", "req": 4, "owner": 7, "verb": "use", "id": "hm-00000000000000aa", "key": "fuel_cell", "qty": 2, "rx": 5, "class": "woodworker"})
	r.settle()
	c := r.site.calls[0]
	if c.Verb != "use" || c.Actor != 9 || c.Key != "fuel_cell" || c.Qty != 2 || c.Class != "" || c.Rx != nil {
		t.Fatalf("a use names the recipe and the count, nothing else: %+v", c)
	}
	only(t, drain(bo), "home_machine_done")
	for _, who := range []*Client{ada, cy} {
		only(t, drain(who), "home_machine_changed")
	}
}

func TestAPayAnsweredWithARefusalStillTellsTheStreet(t *testing.T) {
	r := newMachineRig(t)
	ada, bo, _ := r.onHerStreet()
	r.site.answer = machineAnswer{Status: "refused", Why: "busy", Text: "Someone else is using this. 4 seconds remaining.", Paid: json.RawMessage(`[{"user":11,"amount":7}]`)}
	r.do(bo, map[string]any{"t": "home_machine", "req": 2, "owner": 7, "verb": "collect", "id": "hm-00000000000000aa"})
	r.settle()
	if f := only(t, drain(bo), "home_machine_done"); f["status"] != "refused" || f["why"] != "busy" {
		t.Fatalf("Bo hears the site's refusal: %v", f)
	}
	if a := only(t, drain(ada), "home_machine_changed"); a["verb"] != "check" || a["paid"] == nil {
		t.Fatalf("the owner hears of the job the site settled on the way: %v", a)
	}
	r.site.answer = machineAnswer{Status: "refused", Why: "nothing", Text: "There is nothing in it for you.", Paid: json.RawMessage(`[]`)}
	r.do(bo, map[string]any{"t": "home_machine", "req": 3, "owner": 7, "verb": "collect", "id": "hm-00000000000000aa"})
	r.settle()
	only(t, drain(bo), "home_machine_done")
	if got := drain(ada); len(got) != 0 {
		t.Fatalf("a plain refusal tells nobody else: %v", got)
	}
}

func TestABlockedVisitorIsRefusedBeforeTheSite(t *testing.T) {
	r := newMachineRig(t)
	_, bo, _ := r.onHerStreet()
	r.ign.ign[[2]int64{7, 9}] = true
	r.do(bo, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "refuel", "id": "hm-00000000000000aa"})
	if f := only(t, drain(bo), "home_machine_done"); f["why"] != "blocked" {
		t.Fatalf("blocked: %v", f)
	}
	if len(r.site.calls) != 0 {
		t.Fatalf("the site is never asked: %v", r.site.calls)
	}
}

func TestNoAnswerFromTheSiteIsARefusal(t *testing.T) {
	r := newMachineRig(t)
	_, bo, _ := r.onHerStreet()
	r.site.answer = machineAnswer{}
	r.do(bo, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "refuel", "id": "hm-00000000000000aa", "all": true})
	r.settle()
	if f := only(t, drain(bo), "home_machine_done"); f["status"] != "refused" || f["why"] != "off" {
		t.Fatalf("no answer: %v", f)
	}
	if c := r.site.calls[0]; !c.All {
		t.Fatalf("refuel all reaches the site: %+v", c)
	}
}

func TestNoSiteMeansNoMachines(t *testing.T) {
	r := newMachineRig(t)
	ada, _, _ := r.onHerStreet()
	r.mach.site = nil
	r.do(ada, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "place", "class": "fuelmaker", "rx": 120})
	if f := only(t, drain(ada), "home_machine_done"); f["why"] != "off" {
		t.Fatalf("off: %v", f)
	}
}

func TestMachineActsAreRateLimited(t *testing.T) {
	r := newMachineRig(t)
	_, bo, _ := r.onHerStreet()
	slowed := false
	for i := 0; i < machineBurst+3; i++ {
		r.do(bo, map[string]any{"t": "home_machine", "req": i, "owner": 7, "verb": "check", "id": "hm-00000000000000aa"})
		for _, f := range drain(bo) {
			if f["t"] == "error" && f["code"] == "slow_down" {
				slowed = true
			}
		}
	}
	if !slowed {
		t.Fatal("a burst past the limit is slowed")
	}
}

// Lane machines (#152): in a Community Machine Room anyone sets their own machine down and acts
// on anyone's; everyone in the room and the owner anywhere are told; nowhere else but a home.
func TestMachinesStandInTheCommunityMachineRooms(t *testing.T) {
	r := newMachineRig(t)
	for street, place := range map[string]string{"GCR101Q98A12EHH": "mr:meadow", "GA9MU59GB792T80": "mr:besara", "GIF16EM56A12FSB": "mr:forest"} {
		for _, owner := range []int64{7, 9, 12345} {
			if p, s := machinePlace(street, owner); p != place || s != street {
				t.Fatalf("%s for %d: %q %q", street, owner, p, s)
			}
		}
	}
	if p, _ := machinePlace("LCR101Q98A12EHH", 7); p != "" {
		t.Fatalf("the room's L tsid is not a street anyone stands on: %q", p)
	}
	ada, bo, cy := r.connect(7, "Ada"), r.connect(9, "Bo"), r.connect(11, "Cy")
	r.join(ada, "GIF16EM56A12FSB")
	r.join(bo, "GIF16EM56A12FSB")
	r.join(cy, "LA5101HF7F429V")
	drain(ada)
	drain(bo)
	drain(cy)
	r.do(bo, map[string]any{"t": "home_machine", "req": 1, "owner": 7, "verb": "place", "class": "fuelmaker", "rx": 200})
	if f := only(t, drain(bo), "home_machine_done"); f["why"] != "own" {
		t.Fatalf("only your own machine goes down, in a room too: %v", f)
	}
	r.do(ada, map[string]any{"t": "home_machine", "req": 2, "owner": 7, "verb": "place", "class": "fuelmaker", "rx": 200, "fuel": 80})
	r.settle()
	if c := r.site.calls[0]; c.Place != "mr:forest" || c.Owner != 7 || c.Actor != 7 || c.Verb != "place" {
		t.Fatalf("the site hears the room: %+v", c)
	}
	only(t, drain(ada), "home_machine_done")
	if a := only(t, drain(bo), "home_machine_changed"); a["verb"] != "place" || a["owner"] != float64(7) {
		t.Fatalf("Bo in the room sees it go down: %v", a)
	}
	// Cy walks in and uses Ada's machine; Ada is told, so is Bo.
	r.join(cy, "GIF16EM56A12FSB")
	drain(ada)
	drain(bo)
	drain(cy)
	r.do(cy, map[string]any{"t": "home_machine", "req": 3, "owner": 7, "verb": "use", "id": "hm-00000000000000aa", "key": "fuel_cell", "qty": 1})
	r.settle()
	if c := r.site.calls[1]; c.Place != "mr:forest" || c.Owner != 7 || c.Actor != 11 || c.Key != "fuel_cell" {
		t.Fatalf("a stranger uses it: %+v", c)
	}
	only(t, drain(cy), "home_machine_done")
	only(t, drain(ada), "home_machine_changed")
	only(t, drain(bo), "home_machine_changed")
	// Ada leaves for a public street: she is still told; acts from there are refused.
	r.join(ada, "LA5101HF7F429V")
	drain(ada)
	r.do(cy, map[string]any{"t": "home_machine", "req": 4, "owner": 7, "verb": "collect", "id": "hm-00000000000000aa"})
	r.settle()
	if a := only(t, drain(ada), "home_machine_changed"); a["verb"] != "collect" {
		t.Fatalf("the owner is told wherever she is: %v", a)
	}
	r.do(ada, map[string]any{"t": "home_machine", "req": 5, "owner": 7, "verb": "take", "id": "hm-00000000000000aa"})
	if f := only(t, drain(ada), "home_machine_done"); f["why"] != "away" {
		t.Fatalf("picked up only from in the room: %v", f)
	}
	if len(r.site.calls) != 3 {
		t.Fatalf("the refusal never reaches the site: %d", len(r.site.calls))
	}
}
