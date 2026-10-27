package main

import (
	"encoding/json"
	"testing"
	"time"
)

type requestRig struct {
	t      *testing.T
	hub    *Hub
	now    time.Time
	timers []*rigTimer
	ignore map[[2]int64]bool
}

const requestStreet = "LA5101HF7F429V"

// ignoreModule stands in for buddies.go's buddies.ignoring.
type ignoreModule struct{ r *requestRig }

func (m ignoreModule) Register(r *Registry) {
	r.Provide("buddies.ignoring", func(a, b int64) bool { return m.r.ignore[[2]int64{a, b}] })
}

func newRequestRig(t *testing.T) *requestRig {
	r := &requestRig{t: t, now: time.Unix(1_700_000_000, 0), ignore: map[[2]int64]bool{}}
	saved := modules
	modules = []moduleFactory{newTrades, func(h *Hub) Module { return ignoreModule{r} }}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

func (r *requestRig) do(cl *Client, msg map[string]any) {
	r.now = r.now.Add(2 * time.Second)
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *requestRig) on(id int64, name, street string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.do(cl, map[string]any{"t": "join", "street": street, "x": 0, "y": 0})
	return cl
}

func (r *requestRig) fire(d time.Duration) int {
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

func TestATradeRequestIsHeardAndAcceptingStartsATrade(t *testing.T) {
	r := newRequestRig(t)
	ada := r.on(7, "Ada", requestStreet)
	bo := r.on(9, "Bo", requestStreet)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "trade_request"})
	mine := kinds(drain(ada), "trade_requested")
	heard := kinds(drain(bo), "trade_requested")
	if len(mine) != 1 || mine[0]["mine"] != true || mine[0]["timeout"] != float64(300) {
		t.Fatalf("requester %v", mine)
	}
	if len(heard) != 1 || heard[0]["text"] != "Ada is looking for someone to trade with." || heard[0]["mine"] != false {
		t.Fatalf("street %v", heard)
	}
	r.do(bo, map[string]any{"t": "trade_request_accept", "from": 7})
	a, b := drain(ada), drain(bo)
	for _, got := range [][]map[string]any{a, b} {
		un := kinds(got, "trade_unrequested")
		if len(un) != 1 || un[0]["text"] != "Ada was looking for someone to trade with. Bo accepted." {
			t.Fatalf("unrequested %v", un)
		}
		op := kinds(got, "trade_open")
		if len(op) != 1 || op[0]["by"] != float64(9) {
			t.Fatalf("the accepter starts the trade: %v", op)
		}
	}
	// Gone once accepted.
	cy := r.on(11, "Cy", requestStreet)
	if len(kinds(drain(cy), "trade_requested")) != 0 {
		t.Fatal("an accepted request is still open")
	}
}

func TestATradeRequestNeedsSomeoneToHearIt(t *testing.T) {
	r := newRequestRig(t)
	ada := r.on(7, "Ada", requestStreet)
	drain(ada)
	r.do(ada, map[string]any{"t": "trade_request"})
	e := kinds(drain(ada), "error")
	if len(e) != 1 || e[0]["message"] != "Wait until there's someone around to hear your offer before you make it!" {
		t.Fatalf("alone: %v", e)
	}
}

func TestArrivalsHearOpenRequestsWithTheTimeLeft(t *testing.T) {
	r := newRequestRig(t)
	ada := r.on(7, "Ada", requestStreet)
	r.on(9, "Bo", requestStreet)
	r.do(ada, map[string]any{"t": "trade_request"})
	r.now = r.now.Add(100 * time.Second)
	cy := r.on(11, "Cy", requestStreet) // do() adds 2 s more
	got := kinds(drain(cy), "trade_requested")
	if len(got) != 1 || got[0]["timeout"] != float64(198) {
		t.Fatalf("arrival %v", got)
	}
	// Elsewhere: nothing.
	dee := r.on(13, "Dee", "LOTHERSTREET")
	if len(kinds(drain(dee), "trade_requested")) != 0 {
		t.Fatal("heard on another street")
	}
}

func TestARequestEndsOnTimeoutCancelAndLeaving(t *testing.T) {
	r := newRequestRig(t)
	ada := r.on(7, "Ada", requestStreet)
	bo := r.on(9, "Bo", requestStreet)
	r.do(ada, map[string]any{"t": "trade_request"})
	drain(bo)
	if r.fire(5*time.Minute) != 1 {
		t.Fatal("no 5 minute timer")
	}
	un := kinds(drain(bo), "trade_unrequested")
	if len(un) != 1 || un[0]["text"] != "Ada was looking for someone to trade with." {
		t.Fatalf("timeout %v", un)
	}

	r.do(ada, map[string]any{"t": "trade_request"})
	r.do(ada, map[string]any{"t": "trade_request_cancel"})
	un = kinds(drain(bo), "trade_unrequested")
	if len(un) != 1 || un[0]["text"] != "Ada was looking for someone to trade with." {
		t.Fatalf("cancel %v", un)
	}

	r.do(ada, map[string]any{"t": "trade_request"})
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "join", "street": "LOTHERSTREET", "x": 0, "y": 0})
	un = kinds(drain(bo), "trade_unrequested")
	if len(un) != 1 || un[0]["text"] != nil {
		t.Fatalf("leaving %v", un)
	}
	if len(kinds(drain(ada), "trade_unrequested")) != 1 {
		t.Fatal("the requester should hear their request went")
	}
	// Too late to accept.
	r.do(bo, map[string]any{"t": "trade_request_accept", "from": 7})
	if e := kinds(drain(bo), "error"); len(e) != 1 || len(kinds(drain(ada), "trade_open")) != 0 {
		t.Fatalf("accepting a gone request: %v", e)
	}
}

func TestANewRequestReplacesTheOldAndBlockedPlayersNeverHear(t *testing.T) {
	r := newRequestRig(t)
	ada := r.on(7, "Ada", requestStreet)
	bo := r.on(9, "Bo", requestStreet)
	cy := r.on(11, "Cy", requestStreet)
	r.ignore[[2]int64{11, 7}] = true // Cy blocks Ada
	drain(bo)
	drain(cy)
	r.do(ada, map[string]any{"t": "trade_request"})
	r.do(ada, map[string]any{"t": "trade_request"})
	got := drain(bo)
	if len(kinds(got, "trade_requested")) != 2 || len(kinds(got, "trade_unrequested")) != 1 {
		t.Fatalf("replace: %v", got)
	}
	if len(drain(cy)) != 0 {
		t.Fatal("a blocked player heard the request")
	}
	r.do(cy, map[string]any{"t": "trade_request_accept", "from": 7})
	if len(kinds(drain(cy), "error")) != 1 {
		t.Fatal("a blocked player accepted")
	}
	// Going offline ends it too.
	r.hub.Disconnect(ada)
	if len(kinds(drain(bo), "trade_unrequested")) != 1 {
		t.Fatal("offline should end the request")
	}
}

func TestAcceptingWhileTheRequesterIsTradingHearsTheTradeRefusal(t *testing.T) {
	r := newRequestRig(t)
	ada := r.on(7, "Ada", requestStreet)
	bo := r.on(9, "Bo", requestStreet)
	cy := r.on(11, "Cy", requestStreet)
	r.do(ada, map[string]any{"t": "trade_request"})
	r.do(cy, map[string]any{"t": "trade_start", "id": 7})
	drain(bo)
	r.do(bo, map[string]any{"t": "trade_request_accept", "from": 7})
	e := kinds(drain(bo), "error")
	if len(e) != 1 || e[0]["code"] != "trade" {
		t.Fatalf("busy requester: %v", e)
	}
}
