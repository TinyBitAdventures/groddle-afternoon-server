package main

import (
	"encoding/json"
	"testing"
	"time"
)

type fakeBoxSite struct {
	buys   []sdbCall
	moves  []cabinetCall
	answer boxAnswer
}

func (f *fakeBoxSite) sdbBuy(c sdbCall) boxAnswer {
	f.buys = append(f.buys, c)
	return f.answer
}

func (f *fakeBoxSite) cabinetMove(c cabinetCall) boxAnswer {
	f.moves = append(f.moves, c)
	return f.answer
}

type homeBoxesRig struct {
	hub     *Hub
	m       *homeBoxes
	ign     *fakeIgnores
	site    *fakeBoxSite
	pending []func()
	now     time.Time
	timers  []*rigTimer
}

func newHomeBoxesRig(t *testing.T) *homeBoxesRig {
	r := &homeBoxesRig{now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}, site: &fakeBoxSite{}}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module {
			r.m = newHomeBoxes(h).(*homeBoxes)
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

func (r *homeBoxesRig) run() {
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

func (r *homeBoxesRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *homeBoxesRig) at(id int64, name, street string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.do(cl, map[string]any{"t": "join", "street": street})
	drain(cl)
	return cl
}

const sdbOp = "sdb-0123456789abcdef"
const cabOp = "cab-0123456789abcdef"

func TestSDBBuyerMustStandInTheHouseAndAskProperly(t *testing.T) {
	r := newHomeBoxesRig(t)
	owner := r.at(7, "Ada", "HOUSE_7")
	out := r.at(9, "Bo", "HOME_7")
	in := r.at(10, "Cy", "HOUSE_7")
	buy := func(op string, count, price int64) map[string]any {
		return map[string]any{"t": "sdb_buy", "owner": 7, "uid": 4, "op": op, "count": count, "price": price}
	}
	cases := []struct {
		cl  *Client
		msg map[string]any
		why string
	}{
		{out, buy(sdbOp, 1, 12), "away"},
		{in, buy("sdb-xyz", 1, 12), "bad"},
		{in, buy(sdbOp, 1, -1), "bad"},
		{in, buy(sdbOp, 1, 10_000_000), "bad"},
		{in, buy(sdbOp, 0, 12), "zero"},
		{in, buy(sdbOp, -3, 12), "zero"},
		{owner, buy(sdbOp, 1, 12), "own"},
	}
	for i, c := range cases {
		r.now = r.now.Add(5 * time.Second)
		r.do(c.cl, c.msg)
		got := kinds(drain(c.cl), "sdb_bought")
		if len(got) != 1 || got[0]["status"] != "refused" || got[0]["why"] != c.why {
			t.Fatalf("case %d: %v", i, got)
		}
	}
	r.ign.ign[[2]int64{7, 10}] = true
	r.now = r.now.Add(5 * time.Second)
	r.do(in, buy(sdbOp, 1, 12))
	if got := kinds(drain(in), "sdb_bought"); len(got) != 1 || got[0]["why"] != "blocked" || got[0]["text"] != homeTextBlocked {
		t.Fatalf("blocked: %v", got)
	}
	if len(r.site.buys) != 0 || len(r.pending) != 0 {
		t.Fatalf("the site was asked: %v", r.site.buys)
	}
}

func TestSDBPurchaseSettlesThroughTheSiteAndTellsTheHouse(t *testing.T) {
	r := newHomeBoxesRig(t)
	owner := r.at(7, "Ada", "HOUSE_7")
	bo := r.at(9, "Bo", "HOUSE_7")
	cy := r.at(10, "Cy", "HOUSE_7")
	street := r.at(11, "Di", "HOME_7")
	r.site.answer = boxAnswer{Status: "settled", Class: "apple", UID: 4, N: 3, Price: 12, Total: 36, Stock: 7}
	// The count is capped at what one purchase asks for (StoreBuyUI.as); the site cuts it to the stock.
	r.do(bo, map[string]any{"t": "sdb_buy", "owner": 7, "uid": 4, "op": sdbOp, "count": 5000, "price": 12})
	// The same op again while it is weighed is refused here (it settles once at the site anyway).
	r.do(bo, map[string]any{"t": "sdb_buy", "owner": 7, "uid": 4, "op": sdbOp, "count": 1, "price": 12})
	if got := kinds(drain(bo), "sdb_bought"); len(got) != 1 || got[0]["why"] != "busy" {
		t.Fatalf("again: %v", got)
	}
	r.run()
	if len(r.site.buys) != 1 || r.site.buys[0] != (sdbCall{Op: sdbOp, Buyer: 9, Owner: 7, UID: 4, Count: sdbBuyMax, Price: 12}) {
		t.Fatalf("site calls: %+v", r.site.buys)
	}
	got := kinds(drain(bo), "sdb_bought")
	if len(got) != 1 || got[0]["status"] != "settled" || got[0]["n"].(float64) != 3 || got[0]["total"].(float64) != 36 || got[0]["stock"].(float64) != 7 {
		t.Fatalf("buyer: %v", got)
	}
	sold := kinds(drain(owner), "sdb_sold")
	if len(sold) != 1 || sold[0]["buyer"].(map[string]any)["name"] != "Bo" || sold[0]["n"].(float64) != 3 || sold[0]["total"].(float64) != 36 {
		t.Fatalf("owner: %v", sold)
	}
	if ch := kinds(drain(cy), "box_changed"); len(ch) != 1 || ch[0]["uid"].(float64) != 4 || ch[0]["stock"].(float64) != 7 {
		t.Fatalf("others: %v", ch)
	}
	if ch := kinds(drain(street), "box_changed"); len(ch) != 0 {
		t.Fatalf("the street heard it: %v", ch)
	}
	if len(r.m.busy) != 0 {
		t.Fatalf("busy left: %v", r.m.busy)
	}
}

func TestSDBRefusalsSilenceAndRate(t *testing.T) {
	r := newHomeBoxesRig(t)
	r.at(7, "Ada", "HOUSE_7")
	bo := r.at(9, "Bo", "HOUSE_7")
	r.site.answer = boxAnswer{Status: "refused", Why: "short", Text: "Sorry, you can't afford that."}
	r.do(bo, map[string]any{"t": "sdb_buy", "owner": 7, "uid": 4, "op": sdbOp, "count": 2, "price": 12})
	r.run()
	if got := kinds(drain(bo), "sdb_bought"); len(got) != 1 || got[0]["why"] != "short" || got[0]["n"] != nil {
		t.Fatalf("short: %v", got)
	}
	r.site.answer = boxAnswer{}
	r.now = r.now.Add(5 * time.Second)
	r.do(bo, map[string]any{"t": "sdb_buy", "owner": 7, "uid": 4, "op": sdbOp, "count": 2, "price": 12})
	r.run()
	if got := kinds(drain(bo), "sdb_bought"); len(got) != 1 || got[0]["why"] != "off" || got[0]["text"] != boxTextOff {
		t.Fatalf("no answer: %v", got)
	}
	// Four quick asks pass, the fifth is told to slow down.
	r.now = r.now.Add(time.Minute)
	for i := 0; i < sdbBuyBurst; i++ {
		r.do(bo, map[string]any{"t": "sdb_buy", "owner": 7, "uid": 4, "op": sdbOp[:4] + "00000000000000" + string(rune('a'+i)) + "0", "count": 1, "price": 12})
	}
	r.do(bo, map[string]any{"t": "sdb_buy", "owner": 7, "uid": 4, "op": "sdb-ffffffffffffffff", "count": 1, "price": 12})
	frames := drain(bo)
	if errs := kinds(frames, "error"); len(errs) != 1 || errs[0]["code"] != "slow_down" {
		t.Fatalf("rate: %v", frames)
	}
	r.m.site = nil
	r.now = r.now.Add(time.Minute)
	r.do(bo, map[string]any{"t": "sdb_buy", "owner": 7, "uid": 4, "op": sdbOp, "count": 1, "price": 12})
	if got := kinds(drain(bo), "sdb_bought"); len(got) != 1 || got[0]["why"] != "off" {
		t.Fatalf("no site: %v", got)
	}
}

func TestCabinetMoveForKeyHoldersInTheHouse(t *testing.T) {
	r := newHomeBoxesRig(t)
	owner := r.at(7, "Ada", "HOUSE_7")
	bo := r.at(9, "Bo", "HOUSE_7")
	cy := r.at(10, "Cy", "HOUSE_7")
	out := r.at(11, "Di", "HOME_7")
	move := func(op, kind, class string, n int64) map[string]any {
		return map[string]any{"t": "cabinet_move", "owner": 7, "uid": 5, "op": op, "kind": kind, "slot": 2, "class": class, "n": n}
	}
	for i, c := range []struct {
		cl  *Client
		msg map[string]any
		why string
	}{
		{out, move(cabOp, "take", "apple", 1), "away"},
		{bo, move("cab-1", "take", "apple", 1), "bad"},
		{bo, move(cabOp, "steal", "apple", 1), "bad"},
		{bo, move(cabOp, "take", "Apple!", 1), "bad"},
		{bo, move(cabOp, "take", "apple", 0), "bad"},
		{owner, move(cabOp, "take", "apple", 1), "own"},
	} {
		r.do(c.cl, c.msg)
		if got := kinds(drain(c.cl), "cabinet_moved"); len(got) != 1 || got[0]["why"] != c.why {
			t.Fatalf("case %d: %v", i, got)
		}
	}
	slot := int64(2)
	r.site.answer = boxAnswer{Status: "settled", Kind: "take", Class: "apple", UID: 5, N: 3, Slot: &slot, Contents: json.RawMessage(`[null,null,{"classTsid":"apple","count":2}]`)}
	r.do(bo, move(cabOp, "take", "apple", 3))
	r.run()
	if len(r.site.moves) != 1 || r.site.moves[0].Actor != 9 || r.site.moves[0].Owner != 7 || *r.site.moves[0].Slot != 2 || r.site.moves[0].Kind != "take" {
		t.Fatalf("site: %+v", r.site.moves)
	}
	got := kinds(drain(bo), "cabinet_moved")
	if len(got) != 1 || got[0]["status"] != "settled" || got[0]["n"].(float64) != 3 || len(got[0]["contents"].([]any)) != 3 {
		t.Fatalf("actor: %v", got)
	}
	for _, cl := range []*Client{owner, cy} {
		ch := kinds(drain(cl), "cabinet_changed")
		if len(ch) != 1 || ch[0]["op"] != cabOp || ch[0]["by"].(map[string]any)["name"] != "Bo" || ch[0]["slot"].(float64) != 2 {
			t.Fatalf("%d: %v", cl.ID(), ch)
		}
	}
	r.ign.ign[[2]int64{7, 9}] = true
	r.do(bo, move("cab-1111111111111111", "put", "apple", 1))
	if got := kinds(drain(bo), "cabinet_moved"); len(got) != 1 || got[0]["why"] != "blocked" {
		t.Fatalf("blocked: %v", got)
	}
}

func TestLeavingFreesBoxOps(t *testing.T) {
	r := newHomeBoxesRig(t)
	r.at(7, "Ada", "HOUSE_7")
	bo := r.at(9, "Bo", "HOUSE_7")
	r.do(bo, map[string]any{"t": "sdb_buy", "owner": 7, "uid": 4, "op": sdbOp, "count": 1, "price": 12})
	r.hub.Disconnect(bo)
	if len(r.m.busy) != 0 {
		t.Fatalf("busy after leaving: %v", r.m.busy)
	}
}
