package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeYard stands in for the site's groddle/v1/homes/yard.
type fakeYard struct {
	calls  []yardAct
	answer yardAnswer
}

func (f *fakeYard) homeYard(a yardAct) yardAnswer {
	f.calls = append(f.calls, a)
	return f.answer
}

// yardRig is the homes rig with the yard module too, the site faked and its calls run in place.
type yardRig struct {
	*homesRig
	yard    *homeYard
	site    *fakeYard
	pending []func()
}

func newYardRig(t *testing.T) *yardRig {
	act := json.RawMessage(`{"id":"yd-0000000000000001","kind":"animal","target":"a1","verb":"pet","took":0,"at":1}`)
	r := &yardRig{homesRig: &homesRig{t: t, now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}}, site: &fakeYard{answer: yardAnswer{Status: "settled", Added: 4, Act: act}}}
	savedSecret := homesSecretForTest
	homesSecretForTest = homesTestSecret
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newHomes(h).(*homes); return r.m },
		func(h *Hub) Module { r.yard = newHomeYard(h).(*homeYard); return r.yard },
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
	r.yard.site = r.site
	r.yard.spawn = func(fn func()) { r.pending = append(r.pending, fn) }
	n := 0
	r.yard.newID = func() string { n++; return strings.Repeat("0", 15) + string(rune('0'+n)) }
	return r
}

func (r *yardRig) settle() {
	for len(r.pending) > 0 {
		fn := r.pending[0]
		r.pending = r.pending[1:]
		fn()
	}
	r.fire(0)
}

// visiting puts Bo (9) on Ada's (7) home street, Ada in her house, Cy (11) on the street too.
func (r *yardRig) visiting() (ada, bo, cy *Client) {
	ada, bo, cy = r.connect(7, "Ada"), r.connect(9, "Bo"), r.connect(11, "Cy")
	r.join(ada, house(7))
	r.pass(bo, 7, "Ada", false)
	r.join(bo, home(7))
	r.pass(cy, 7, "Ada", false)
	r.join(cy, home(7))
	drain(ada)
	drain(bo)
	drain(cy)
	return ada, bo, cy
}

func TestAVisitorPetsTheOwnersPiggyAndEveryoneThereIsTold(t *testing.T) {
	r := newYardRig(t)
	ada, bo, cy := r.visiting()
	r.do(bo, map[string]any{"t": "home_yard", "req": 3, "owner": 7, "kind": "animal", "id": "a1", "verb": "pet"})
	if got := drain(bo); len(got) != 0 {
		t.Fatalf("nothing until the site answers: %v", got)
	}
	r.settle()
	if len(r.site.calls) != 1 {
		t.Fatalf("one call: %v", r.site.calls)
	}
	c := r.site.calls[0]
	if c.Op != "yd-0000000000000001" || c.Owner != 7 || c.Visitor != 9 || c.Kind != "animal" || c.ID != "a1" || c.Verb != "pet" {
		t.Fatalf("the call names the act, never an amount: %+v", c)
	}
	f := only(t, drain(bo), "home_yard_done")
	if f["status"] != "settled" || f["req"] != float64(3) || f["added"] != float64(4) || f["act"].(map[string]any)["target"] != "a1" {
		t.Fatalf("Bo is answered: %v", f)
	}
	for _, who := range []*Client{ada, cy} {
		a := only(t, drain(who), "home_yard_acted")
		if a["owner"] != float64(7) || a["from"].(map[string]any)["name"] != "Bo" || a["act"].(map[string]any)["verb"] != "pet" {
			t.Fatalf("the owner in her house and the other visitor are told: %v", a)
		}
	}
}

func TestTheOwnerIsToldAnywhereOnline(t *testing.T) {
	r := newYardRig(t)
	ada, bo, _ := r.visiting()
	r.join(ada, "LA5101HF7F429V")
	drain(ada)
	r.do(bo, map[string]any{"t": "home_yard", "req": 1, "owner": 7, "kind": "tree", "id": "patch:1", "verb": "water"})
	r.settle()
	only(t, drain(ada), "home_yard_acted")
}

func TestYardVerbsNeedTheHomeStreetAndAGoodAct(t *testing.T) {
	r := newYardRig(t)
	ada, bo, _ := r.visiting()
	cases := []map[string]any{
		{"t": "home_yard", "req": 1, "owner": 7, "kind": "animal", "id": "patch:0", "verb": "pet"},
		{"t": "home_yard", "req": 1, "owner": 7, "kind": "plot", "id": "a1", "verb": "dig"},
		{"t": "home_yard", "req": 1, "owner": 7, "kind": "tree", "id": "patch:0", "verb": "poison"},
		{"t": "home_yard", "req": 1, "owner": 7, "kind": "rock", "id": "patch:0", "verb": "pet"},
		{"t": "home_yard", "req": 1, "owner": 9, "kind": "animal", "id": "a1", "verb": "pet"},
	}
	for _, c := range cases {
		r.do(bo, c)
		f := only(t, drain(bo), "home_yard_done")
		if f["status"] != "refused" {
			t.Fatalf("%v: refused, got %v", c, f)
		}
	}
	// The owner does her own verbs in her own game; a visitor must stand on the home street.
	r.do(ada, map[string]any{"t": "home_yard", "req": 1, "owner": 7, "kind": "animal", "id": "a1", "verb": "pet"})
	if f := only(t, drain(ada), "home_yard_done"); f["why"] != "bad" {
		t.Fatalf("not for the owner: %v", f)
	}
	r.join(bo, "LA5101HF7F429V")
	drain(bo)
	r.do(bo, map[string]any{"t": "home_yard", "req": 1, "owner": 7, "kind": "animal", "id": "a1", "verb": "pet"})
	if f := only(t, drain(bo), "home_yard_done"); f["why"] != "away" {
		t.Fatalf("from another street: %v", f)
	}
	if len(r.pending) != 0 || len(r.site.calls) != 0 {
		t.Fatalf("no site calls for refused acts")
	}
}

func TestABlockedVisitorAndTheSiteRefusingSayNo(t *testing.T) {
	r := newYardRig(t)
	ada, bo, _ := r.visiting()
	r.site.answer = yardAnswer{Status: "refused", Why: "limit", Text: "You can only pet this Piggy 1 time per game day."}
	r.do(bo, map[string]any{"t": "home_yard", "req": 2, "owner": 7, "kind": "animal", "id": "a1", "verb": "pet"})
	r.settle()
	if f := only(t, drain(bo), "home_yard_done"); f["status"] != "refused" || f["text"] != "You can only pet this Piggy 1 time per game day." {
		t.Fatalf("the site's words: %v", f)
	}
	if got := drain(ada); len(got) != 0 {
		t.Fatalf("a refusal tells the owner nothing: %v", got)
	}
	// No answer: refused, nothing told.
	r.site.answer = yardAnswer{}
	r.do(bo, map[string]any{"t": "home_yard", "req": 3, "owner": 7, "kind": "animal", "id": "a1", "verb": "pet"})
	r.settle()
	if f := only(t, drain(bo), "home_yard_done"); f["why"] != "off" {
		t.Fatalf("no answer: %v", f)
	}
	r.ign.ign[[2]int64{7, 9}] = true
	r.do(bo, map[string]any{"t": "home_yard", "req": 4, "owner": 7, "kind": "animal", "id": "a1", "verb": "pet"})
	if f := only(t, drain(bo), "home_yard_done"); f["why"] != "blocked" {
		t.Fatalf("blocked: %v", f)
	}
}

func TestYardVerbsAreRateLimited(t *testing.T) {
	r := newYardRig(t)
	_, bo, _ := r.visiting()
	for i := 0; i < yardBurst+3; i++ {
		r.do(bo, map[string]any{"t": "home_yard", "req": i, "owner": 7, "kind": "plot", "id": "patch:0", "verb": "tend"})
	}
	if len(r.pending) != yardBurst {
		t.Fatalf("a burst of %d, got %d", yardBurst, len(r.pending))
	}
}

func TestNoSiteNoYardVerbs(t *testing.T) {
	r := newYardRig(t)
	r.yard.site = nil
	_, bo, _ := r.visiting()
	r.do(bo, map[string]any{"t": "home_yard", "req": 1, "owner": 7, "kind": "animal", "id": "a1", "verb": "pet"})
	if f := only(t, drain(bo), "home_yard_done"); f["why"] != "off" {
		t.Fatalf("off: %v", f)
	}
}
