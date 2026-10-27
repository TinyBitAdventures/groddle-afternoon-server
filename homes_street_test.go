package main

import (
	"encoding/json"
	"testing"
	"time"
)

type furnCall struct {
	op                  string
	buyer, owner, price int64
}

type fakeFurnSite struct {
	calls  []furnCall
	answer furnAnswer
}

func (f *fakeFurnSite) furniture(op string, buyer, owner, price int64) furnAnswer {
	f.calls = append(f.calls, furnCall{op, buyer, owner, price})
	return f.answer
}

type homeStreetRig struct {
	hub     *Hub
	m       *homeStreet
	ign     *fakeIgnores
	site    *fakeFurnSite
	pending []func()
	now     time.Time
	timers  []*rigTimer
}

func newHomeStreetRig(t *testing.T) *homeStreetRig {
	r := &homeStreetRig{now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}, site: &fakeFurnSite{}}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module {
			r.m = newHomeStreet(h).(*homeStreet)
			r.m.site = r.site
			r.m.spawn = func(fn func()) { r.pending = append(r.pending, fn) }
			return r.m
		},
		func(h *Hub) Module { return r.ign },
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

// run lets the site answer the purchases waiting, then the hub's After(0).
func (r *homeStreetRig) run() {
	for _, fn := range r.pending {
		fn()
	}
	r.pending = nil
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if tm.d == 0 && !tm.stopped {
			tm.stopped = true
			tm.fn()
		}
	}
}

func (r *homeStreetRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *homeStreetRig) at(id int64, name, street string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.do(cl, map[string]any{"t": "join", "street": street})
	drain(cl)
	return cl
}

const furnOp = "furn-0123456789abcdef"

func TestFurnitureBuyerMustBeInTheHouse(t *testing.T) {
	r := newHomeStreetRig(t)
	r.at(7, "Ada", "HOUSE_7")
	out := r.at(9, "Bo", "HOME_7")
	in := r.at(10, "Cy", "HOUSE_7")
	own := r.hub.ClientLocked(7)
	cases := []struct {
		cl   *Client
		msg  map[string]any
		why  string
		text string
	}{
		{out, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 100}, "away", furnTextAway},
		{in, map[string]any{"t": "furn_buy", "owner": 7, "op": "furn-xyz", "price": 100}, "bad", furnTextBad},
		{in, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 0}, "bad", furnTextBad},
		{in, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 1_000_000}, "bad", furnTextBad},
		{own, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 100}, "own", furnTextOwn},
	}
	for i, c := range cases {
		r.now = r.now.Add(5 * time.Second)
		r.do(c.cl, c.msg)
		got := kinds(drain(c.cl), "furn_bought")
		if len(got) != 1 || got[0]["status"] != "refused" || got[0]["why"] != c.why || got[0]["text"] != c.text {
			t.Fatalf("case %d: %v", i, got)
		}
	}
	r.ign.ign[[2]int64{7, 10}] = true
	r.now = r.now.Add(5 * time.Second)
	r.do(in, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 100})
	if got := kinds(drain(in), "furn_bought"); len(got) != 1 || got[0]["why"] != "blocked" {
		t.Fatalf("blocked: %v", got)
	}
	if len(r.site.calls) != 0 || len(r.pending) != 0 {
		t.Fatalf("the site was asked: %v", r.site.calls)
	}
}

func TestFurnitureSaleSettlesThroughTheSite(t *testing.T) {
	r := newHomeStreetRig(t)
	owner := r.at(7, "Ada", "HOUSE_7")
	bo := r.at(9, "Bo", "HOUSE_7")
	cy := r.at(10, "Cy", "HOUSE_7")
	r.site.answer = furnAnswer{Status: "settled", Class: "furniture_bed", UID: 4, Price: 1200, X: 610}
	r.do(bo, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 1200})
	// While the site weighs it, nobody else can buy that piece.
	r.do(cy, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 1200})
	if got := kinds(drain(cy), "furn_bought"); len(got) != 1 || got[0]["why"] != "busy" {
		t.Fatalf("second buyer: %v", got)
	}
	r.run()
	if len(r.site.calls) != 1 || r.site.calls[0] != (furnCall{furnOp, 9, 7, 1200}) {
		t.Fatalf("site calls: %v", r.site.calls)
	}
	got := kinds(drain(bo), "furn_bought")
	if len(got) != 1 || got[0]["status"] != "settled" || got[0]["class"] != "furniture_bed" || got[0]["uid"].(float64) != 4 || got[0]["price"].(float64) != 1200 {
		t.Fatalf("buyer: %v", got)
	}
	sold := kinds(drain(owner), "furn_sold")
	if len(sold) != 1 || sold[0]["buyer"].(map[string]any)["name"] != "Bo" || sold[0]["x"].(float64) != 610 {
		t.Fatalf("owner: %v", sold)
	}
	if gone := kinds(drain(cy), "furn_gone"); len(gone) != 1 || gone[0]["uid"].(float64) != 4 {
		t.Fatalf("others: %v", gone)
	}
	// The piece is free again for the site to refuse.
	if len(r.m.busy) != 0 {
		t.Fatalf("busy left: %v", r.m.busy)
	}
}

func TestFurnitureRefusalsAndSilence(t *testing.T) {
	r := newHomeStreetRig(t)
	r.at(7, "Ada", "HOUSE_7")
	bo := r.at(9, "Bo", "HOUSE_7")
	r.site.answer = furnAnswer{Status: "refused", Why: "price", Text: "Oops, the price appears to have changed underneath you. Please try your purchase again."}
	r.do(bo, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 1100})
	r.run()
	if got := kinds(drain(bo), "furn_bought"); len(got) != 1 || got[0]["why"] != "price" || got[0]["class"] != nil {
		t.Fatalf("price: %v", got)
	}
	r.site.answer = furnAnswer{}
	r.now = r.now.Add(5 * time.Second)
	r.do(bo, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 1200})
	r.run()
	if got := kinds(drain(bo), "furn_bought"); len(got) != 1 || got[0]["why"] != "off" || got[0]["text"] != furnTextOff {
		t.Fatalf("no answer: %v", got)
	}
	r.m.site = nil
	r.now = r.now.Add(5 * time.Second)
	r.do(bo, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 1200})
	if got := kinds(drain(bo), "furn_bought"); len(got) != 1 || got[0]["why"] != "off" {
		t.Fatalf("no site: %v", got)
	}
}

func TestBuyerLeavingFreesThePiece(t *testing.T) {
	r := newHomeStreetRig(t)
	r.at(7, "Ada", "HOUSE_7")
	bo := r.at(9, "Bo", "HOUSE_7")
	r.do(bo, map[string]any{"t": "furn_buy", "owner": 7, "op": furnOp, "price": 1200})
	r.hub.Disconnect(bo)
	if len(r.m.busy) != 0 {
		t.Fatalf("busy after leaving: %v", r.m.busy)
	}
}

func TestHomeRefreshReachesEveryoneAtTheOwnersHome(t *testing.T) {
	r := newHomeStreetRig(t)
	owner := r.at(7, "Ada", "HOME_7")
	street := r.at(9, "Bo", "HOME_7")
	house := r.at(10, "Cy", "HOUSE_7")
	elsewhere := r.at(11, "Di", "HOME_9")
	r.do(owner, map[string]any{"t": "home_refresh", "what": "poles"})
	r.do(owner, map[string]any{"t": "home_refresh", "what": "everything"})
	for _, cl := range []*Client{street, house} {
		got := kinds(drain(cl), "home_refresh")
		if len(got) != 1 || got[0]["owner"].(float64) != 7 || got[0]["what"] != "poles" {
			t.Fatalf("%d: %v", cl.ID(), got)
		}
	}
	if got := kinds(drain(owner), "home_refresh"); len(got) != 0 {
		t.Fatalf("the sender heard it: %v", got)
	}
	if got := kinds(drain(elsewhere), "home_refresh"); len(got) != 0 {
		t.Fatalf("another home heard it: %v", got)
	}
	// A visitor can only refresh their own home, not someone else's.
	r.do(street, map[string]any{"t": "home_refresh", "what": "tags"})
	if got := kinds(drain(owner), "home_refresh"); len(got) != 0 {
		t.Fatalf("a visitor refreshed the owner's home: %v", got)
	}
}
