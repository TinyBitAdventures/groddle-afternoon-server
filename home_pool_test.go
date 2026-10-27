package main

import (
	"testing"
	"time"
)

// fakeHomeVisits stands in for the site's groddle/v1/homes/pool/visit.
type fakeHomeVisits struct {
	calls  [][2]int64
	answer ledgerAnswer
}

func (f *fakeHomeVisits) homeVisit(owner, visitor int64) ledgerAnswer {
	f.calls = append(f.calls, [2]int64{owner, visitor})
	return f.answer
}

// poolRig is the homes rig with the pool module too, the site faked and its calls run in place.
type poolRig struct {
	*homesRig
	pool    *homePool
	site    *fakeHomeVisits
	pending []func()
}

func newPoolRig(t *testing.T) *poolRig {
	r := &poolRig{homesRig: &homesRig{t: t, now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}}, site: &fakeHomeVisits{answer: ledgerAnswer{Status: "settled"}}}
	savedSecret := homesSecretForTest
	homesSecretForTest = homesTestSecret
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newHomes(h).(*homes); return r.m },
		func(h *Hub) Module { r.pool = newHomePool(h).(*homePool); return r.pool },
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
	r.pool.site = r.site
	r.pool.spawn = func(fn func()) { r.pending = append(r.pending, fn) }
	return r
}

func (r *poolRig) settle() {
	for len(r.pending) > 0 {
		fn := r.pending[0]
		r.pending = r.pending[1:]
		fn()
	}
	r.fire(0)
}

func TestAVisitorOnAHomeStreetIsToldToTheSiteOnceADay(t *testing.T) {
	r := newPoolRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	// The owner on their own street is not a visitor (home.js:93 this.owner.tsid != pc.tsid).
	r.join(ada, home(7))
	r.settle()
	if len(r.site.calls) != 0 {
		t.Fatalf("the owner counts nothing: %v", r.site.calls)
	}
	r.pass(bo, 7, "Ada", true)
	r.join(bo, home(7))
	r.settle()
	if len(r.site.calls) != 1 || r.site.calls[0] != [2]int64{7, 9} {
		t.Fatalf("Bo on Ada's street is told: %v", r.site.calls)
	}
	// In and out of the house and back the same game day: once (home_log_visitor's daily_visitors).
	r.join(bo, house(7))
	r.join(bo, home(7))
	r.settle()
	if len(r.site.calls) != 1 {
		t.Fatalf("the house is not the street, and a return the same day is not new: %v", r.site.calls)
	}
	// The next game day (four hours on) Bo counts again.
	r.now = r.now.Add(4 * time.Hour)
	r.pass(bo, 7, "Ada", true) // the site's pass lasts minutes
	r.join(bo, house(7))
	r.join(bo, home(7))
	r.settle()
	if len(r.site.calls) != 2 {
		t.Fatalf("a new game day counts again: %v", r.site.calls)
	}
}

func TestAHouseOrAPublicStreetFillsNoPool(t *testing.T) {
	r := newPoolRig(t)
	bo := r.connect(9, "Bo")
	r.join(bo, "LA5101HF7F429V")
	r.pass(bo, 7, "Ada", true)
	r.join(bo, house(7))
	r.settle()
	if len(r.site.calls) != 0 {
		t.Fatalf("only the home street (is_public): %v", r.site.calls)
	}
}

func TestAVisitTheSiteDidNotAnswerIsAskedAgainOnTheNextJoin(t *testing.T) {
	r := newPoolRig(t)
	bo := r.connect(9, "Bo")
	r.pass(bo, 7, "Ada", true)
	r.site.answer = ledgerAnswer{}
	r.join(bo, home(7))
	r.settle()
	r.site.answer = ledgerAnswer{Status: "settled"}
	r.join(bo, house(7))
	r.join(bo, home(7))
	r.settle()
	if len(r.site.calls) != 2 {
		t.Fatalf("no answer: asked again: %v", r.site.calls)
	}
	r.join(bo, house(7))
	r.join(bo, home(7))
	r.settle()
	if len(r.site.calls) != 2 {
		t.Fatalf("answered: not again: %v", r.site.calls)
	}
}

func TestNoSiteNoPoolCalls(t *testing.T) {
	r := newPoolRig(t)
	r.pool.site = nil
	bo := r.connect(9, "Bo")
	r.pass(bo, 7, "Ada", true)
	r.join(bo, home(7))
	r.settle()
	if len(r.pending) != 0 || len(r.site.calls) != 0 {
		t.Fatalf("without a ledger nothing is told")
	}
}
