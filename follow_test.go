package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// followRig is a hub with only the follow module (and a block list), a manual clock and timers.
type followRig struct {
	t      *testing.T
	hub    *Hub
	m      *follows
	ign    *fakeIgnores
	now    time.Time
	timers []*rigTimer
}

func newFollowRig(t *testing.T) *followRig {
	r := &followRig{t: t, now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newFollows(h).(*follows); return r.m },
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

func (r *followRig) fire(d time.Duration) int {
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

func (r *followRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

func (r *followRig) do(cl *Client, msg map[string]any) {
	r.now = r.now.Add(2 * time.Second)
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *followRig) join(cl *Client, street string, x float64) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": x, "y": -100})
}

// follow makes f follow l (both on l's street), draining both.
func (r *followRig) follow(f, l *Client) {
	r.t.Helper()
	r.do(f, map[string]any{"t": "follow_start", "id": l.ID()})
	r.do(l, map[string]any{"t": "follow_answer", "id": f.ID(), "accept": true})
	if r.m.leaderOf[f.ID()] != l.ID() {
		r.t.Fatalf("%s is not following %s: %v", f.Name(), l.Name(), r.m.leaderOf)
	}
	drain(f)
	drain(l)
}

func followOne(t *testing.T, msgs []map[string]any, kind string) map[string]any {
	t.Helper()
	got := kinds(msgs, kind)
	if len(got) != 1 {
		t.Fatalf("want one %s, got %d in %v", kind, len(got), msgs)
	}
	return got[0]
}

func TestFollowAskAcceptAndTheLeadersList(t *testing.T) {
	r := newFollowRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.join(ada, "GA5A", 100)
	r.join(bo, "GA5A", 300)
	drain(ada)
	drain(bo)
	r.do(bo, map[string]any{"t": "follow_start", "id": 1})
	if m := followOne(t, drain(ada), "follow_asked"); m["id"] != float64(2) || m["name"] != "Bo" {
		t.Fatalf("asked %v", m)
	}
	if m := followOne(t, drain(bo), "follow_waiting"); m["name"] != "Ada" {
		t.Fatalf("waiting %v", m)
	}
	r.do(ada, map[string]any{"t": "follow_answer", "id": 2, "accept": true})
	if m := followOne(t, drain(bo), "follow_started"); m["id"] != float64(1) || m["streets"] != float64(1) {
		t.Fatalf("started %v", m)
	}
	list := followOne(t, drain(ada), "follow_list")
	if fl := list["followers"].([]any); len(fl) != 1 || fl[0].(map[string]any)["name"] != "Bo" || list["added"] != float64(2) {
		t.Fatalf("list %v", list)
	}
	// The answer timer was stopped: firing 30 s does nothing more.
	r.fire(followAskWait)
	if len(kinds(drain(bo), "follow_refused")) != 0 {
		t.Fatal("a stale timeout refused after the accept")
	}
}

func TestFollowRefusals(t *testing.T) {
	r := newFollowRig(t)
	ada, bo, cy := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	r.join(ada, "GA5A", 0)
	r.join(bo, "GA5A", 0)
	r.join(cy, "GA5B", 0)
	drain(ada)
	drain(bo)
	drain(cy)
	why := func(cl *Client) string {
		t.Helper()
		return followOne(t, drain(cl), "follow_refused")["why"].(string)
	}
	r.do(cy, map[string]any{"t": "follow_start", "id": 1})
	if w := why(cy); w != "street" {
		t.Fatalf("another street: %s", w)
	}
	r.do(cy, map[string]any{"t": "follow_start", "id": 99})
	if w := why(cy); w != "gone" {
		t.Fatalf("offline: %s", w)
	}
	// Bo follows Ada; Ada asking to follow Bo is following herself (main.js:1511).
	r.follow(bo, ada)
	r.do(ada, map[string]any{"t": "follow_start", "id": 2})
	if w := why(ada); w != "self" {
		t.Fatalf("self: %s", w)
	}
	// Bo already follows Ada.
	r.join(cy, "GA5A", 0)
	drain(cy)
	r.do(bo, map[string]any{"t": "follow_start", "id": 3})
	if m := followOne(t, drain(bo), "follow_refused"); m["why"] != "already" || m["name"] != "Ada" {
		t.Fatalf("already %v", m)
	}
	// Blocked either way.
	r.ign.ign[[2]int64{1, 3}] = true
	r.do(cy, map[string]any{"t": "follow_start", "id": 1})
	if w := why(cy); w != "blocked" {
		t.Fatalf("blocked: %s", w)
	}
	if len(kinds(drain(ada), "follow_asked")) != 0 {
		t.Fatal("a blocked player's ask reached the leader")
	}
}

func TestFollowAskingAFollowerAsksTheirLeader(t *testing.T) {
	r := newFollowRig(t)
	ada, bo, cy := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	for _, c := range []*Client{ada, bo, cy} {
		r.join(c, "GA5A", 0)
	}
	drain(ada)
	drain(bo)
	drain(cy)
	r.follow(bo, ada)
	r.do(cy, map[string]any{"t": "follow_start", "id": 2})
	if len(kinds(drain(bo), "follow_asked")) != 0 {
		t.Fatal("the follower was asked")
	}
	followOne(t, drain(ada), "follow_asked")
	if m := followOne(t, drain(cy), "follow_waiting"); m["name"] != "Ada" {
		t.Fatalf("waiting for %v", m)
	}
}

func TestFollowDeclineTimeoutAndTooFar(t *testing.T) {
	r := newFollowRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.join(ada, "GA5A", 0)
	r.join(bo, "GA5A", 500)
	drain(ada)
	drain(bo)
	r.do(bo, map[string]any{"t": "follow_start", "id": 1})
	r.do(ada, map[string]any{"t": "follow_answer", "id": 2, "accept": false})
	if m := followOne(t, drain(bo), "follow_refused"); m["why"] != "declined" || m["name"] != "Ada" {
		t.Fatalf("decline %v", m)
	}
	// No answer in 30 s reads as No thanks (timeout_value decline).
	r.do(bo, map[string]any{"t": "follow_start", "id": 1})
	drain(ada)
	drain(bo)
	if n := r.fire(followAskWait); n != 1 {
		t.Fatalf("timers %d", n)
	}
	if m := followOne(t, drain(bo), "follow_refused"); m["why"] != "declined" {
		t.Fatalf("timeout %v", m)
	}
	followOne(t, drain(ada), "follow_ask_gone")
	r.do(ada, map[string]any{"t": "follow_answer", "id": 2, "accept": true})
	if r.m.leaderOf[2] != 0 {
		t.Fatal("a timed-out ask was accepted")
	}
	// Bo walks 1001 px away before Ada says OK (player.js:1487).
	r.do(bo, map[string]any{"t": "follow_start", "id": 1})
	r.do(bo, map[string]any{"t": "move", "x": 1001, "y": -100})
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "follow_answer", "id": 2, "accept": true})
	if m := followOne(t, drain(bo), "follow_refused"); m["why"] != "far" {
		t.Fatalf("far %v", m)
	}
	if m := followOne(t, drain(ada), "follow_too_far"); m["name"] != "Bo" {
		t.Fatalf("too far %v", m)
	}
	if r.m.leaderOf[2] != 0 {
		t.Fatal("followed from too far")
	}
}

func TestFollowGoesWhereTheLeaderWentAndCountsStreets(t *testing.T) {
	r := newFollowRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.join(ada, "GA5A", 0)
	r.join(bo, "GA5A", 80)
	drain(ada)
	drain(bo)
	r.follow(bo, ada)
	r.join(ada, "GA5B", 420)
	g := followOne(t, drain(bo), "follow_go")
	if g["street"] != "GA5B" || g["x"] != float64(420) || g["via"] != "walk" || g["name"] != "Ada" {
		t.Fatalf("go %v", g)
	}
	r.join(bo, "GA5B", 400)
	if a := followOne(t, drain(bo), "follow_arrived"); a["streets"] != float64(2) {
		t.Fatalf("arrived %v", a)
	}
	// A lagging follower still arrives where it was sent, and is sent on.
	r.join(ada, "GA5C", 0)
	r.join(ada, "GA5D", 0)
	drain(bo)
	r.join(bo, "GA5C", 0)
	if a := followOne(t, drain(bo), "follow_arrived"); a["streets"] != float64(3) {
		t.Fatalf("arrived on the way %v", a)
	}
	r.join(bo, "GA5D", 0)
	if a := followOne(t, drain(bo), "follow_arrived"); a["streets"] != float64(4) {
		t.Fatalf("caught up %v", a)
	}
	if r.m.leaderOf[2] != 1 {
		t.Fatal("stopped following on the way")
	}
	// Joining the leader's street again (a new outfit) counts nothing.
	r.join(bo, "GA5D", 10)
	if len(kinds(drain(bo), "follow_arrived")) != 0 {
		t.Fatal("a rejoin counted a street")
	}
}

func TestFollowEndsOnInputAnotherStreetAndOffline(t *testing.T) {
	r := newFollowRig(t)
	ada, bo, cy := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	for _, c := range []*Client{ada, bo, cy} {
		r.join(c, "GA5A", 0)
	}
	r.follow(bo, ada)
	r.follow(cy, ada)
	r.do(bo, map[string]any{"t": "follow_stop"})
	if m := followOne(t, drain(bo), "follow_ended"); m["why"] != "stopped" || m["name"] != "Ada" {
		t.Fatalf("stop %v", m)
	}
	if l := followOne(t, drain(ada), "follow_list"); l["removed"] != float64(2) || len(l["followers"].([]any)) != 1 {
		t.Fatalf("list %v", l)
	}
	// Cy goes somewhere else on their own (a teleport, a signpost): it ends.
	r.join(cy, "GA5Q", 0)
	if m := followOne(t, drain(cy), "follow_ended"); m["why"] != "left" {
		t.Fatalf("left %v", m)
	}
	r.follow(bo, ada)
	r.hub.Disconnect(ada)
	if m := followOne(t, drain(bo), "follow_ended"); m["why"] != "offline" {
		t.Fatalf("leader offline %v", m)
	}
	if len(r.m.of) != 0 || len(r.m.leaderOf) != 0 {
		t.Fatalf("left over %v %v", r.m.of, r.m.leaderOf)
	}
	// A reload (a new socket for the same character) starts unfollowed, and so do its followers.
	ada = r.connect(1, "Ada")
	r.join(ada, "GA5A", 0)
	r.join(bo, "GA5A", 0)
	r.follow(bo, ada)
	r.connect(2, "Bo")
	if len(r.m.of) != 0 || len(r.m.leaderOf) != 0 {
		t.Fatalf("a reload kept the follow %v", r.m.leaderOf)
	}
	if l := followOne(t, drain(ada), "follow_list"); l["why"] != "reset" {
		t.Fatalf("reset %v", l)
	}
}

func TestFollowPrivatePlacesLetFollowersGo(t *testing.T) {
	r := newFollowRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.join(ada, "GA5A", 0)
	r.join(bo, "GA5A", 0)
	r.follow(bo, ada)
	r.join(ada, "HOME_1", 0)
	if m := followOne(t, drain(bo), "follow_ended"); m["why"] != "private" {
		t.Fatalf("home %v", m)
	}
	if len(kinds(drain(bo), "follow_go")) != 0 {
		t.Fatal("sent into a home")
	}
	// Off every street (a quest room) for followAwayWait.
	r.join(ada, "GA5A", 0)
	r.follow(bo, ada)
	r.do(ada, map[string]any{"t": "leave"})
	if r.fire(followAwayWait) != 1 {
		t.Fatal("no away timer")
	}
	if m := followOne(t, drain(bo), "follow_ended"); m["why"] != "private" {
		t.Fatalf("away %v", m)
	}
	// A street change in between (leave, then a join) keeps them.
	r.join(ada, "GA5A", 0)
	r.follow(bo, ada)
	r.do(ada, map[string]any{"t": "leave"})
	r.join(ada, "GA5B", 0)
	if r.fire(followAwayWait) != 0 {
		t.Fatal("the away timer outlived the join")
	}
	followOne(t, drain(bo), "follow_go")
	if r.m.leaderOf[2] != 1 {
		t.Fatal("let go on a street change")
	}
}

func TestFollowTeleportWithThreeFollowers(t *testing.T) {
	r := newFollowRig(t)
	ada := r.connect(1, "Ada")
	r.join(ada, "GA5A", 0)
	fs := []*Client{}
	for i := int64(2); i <= 4; i++ {
		f := r.connect(i, fmt.Sprintf("F%d", i))
		r.join(f, "GA5A", 0)
		r.follow(f, ada)
		fs = append(fs, f)
	}
	r.do(ada, map[string]any{"t": "follow_teleport", "street": "GA5T", "kind": "map"})
	r.join(ada, "GA5T", 50)
	for _, f := range fs {
		if g := followOne(t, drain(f), "follow_go"); g["via"] != "teleport" || g["street"] != "GA5T" {
			t.Fatalf("go %v", g)
		}
		r.join(f, "GA5T", 40)
	}
	drain(ada)
	if r.fire(followTeleportDone) != 1 {
		t.Fatal("no teleport_complete")
	}
	if m := followOne(t, drain(ada), "follow_teleported"); m["followers"] != float64(3) || m["complete"] != float64(3) || m["kind"] != "map" {
		t.Fatalf("teleported %v", m)
	}
	// Said on arrival (the game tells once it has landed): counted the same.
	r.join(ada, "GA5U", 0)
	r.do(ada, map[string]any{"t": "follow_teleport", "street": "GA5U", "kind": "plain"})
	// One follower stops before teleport_complete: counted 3 at the teleport, 2 at the end.
	r.do(fs[0], map[string]any{"t": "follow_stop"})
	drain(ada)
	r.fire(followTeleportDone)
	if m := followOne(t, drain(ada), "follow_teleported"); m["followers"] != float64(3) || m["complete"] != float64(2) || m["kind"] != "plain" {
		t.Fatalf("on arrival %v", m)
	}
	// A teleport said for one street and a walk to another is a walk.
	drain(fs[1])
	r.do(ada, map[string]any{"t": "follow_teleport", "street": "GA5Z", "kind": "plain"})
	r.join(ada, "GA5Y", 0)
	if g := followOne(t, drain(fs[1]), "follow_go"); g["via"] != "walk" {
		t.Fatalf("walk %v", g)
	}
	if r.fire(followTeleportDone) != 0 {
		t.Fatal("a walk counted as a teleport")
	}
}

func TestFollowGlitchTrain(t *testing.T) {
	r := newFollowRig(t)
	lead := r.connect(1, "Lead")
	r.join(lead, "GA5A", 0)
	var fs []*Client
	for i := int64(2); i <= int64(glitchTrainLength); i++ {
		f := r.connect(i, fmt.Sprintf("F%d", i))
		r.join(f, "GA5A", 0)
		r.do(f, map[string]any{"t": "follow_start", "id": 1})
		r.do(lead, map[string]any{"t": "follow_answer", "id": i, "accept": true})
		fs = append(fs, f)
		if i < int64(glitchTrainLength) {
			drain(lead) // the send buffer holds 64
			for _, o := range fs {
				drain(o)
			}
		}
	}
	last := func(cl *Client) map[string]any {
		got := kinds(drain(cl), "follow_train")
		if len(got) == 0 {
			t.Fatalf("no train for %s", cl.Name())
		}
		return got[len(got)-1]
	}
	if m := last(lead); m["chain"] != float64(20) || m["on"] != true {
		t.Fatalf("leader %v", m)
	}
	if m := last(fs[0]); m["chain"] != float64(20) || m["on"] != true {
		t.Fatalf("follower %v", m)
	}
	// Each street entered while on the train is said again (location.js:123).
	r.join(lead, "GA5B", 0)
	if m := last(lead); m["street"] != "GA5B" || m["on"] != true {
		t.Fatalf("new street %v", m)
	}
	r.do(fs[0], map[string]any{"t": "follow_stop"})
	if m := last(lead); m["chain"] != float64(19) || m["on"] != false {
		t.Fatalf("off %v", m)
	}
	if m := last(fs[0]); m["chain"] != float64(1) || m["on"] != false {
		t.Fatalf("the one who stopped %v", m)
	}
}

func TestFollowBusyLeaderAndOneAskAtATime(t *testing.T) {
	r := newFollowRig(t)
	lead := r.connect(1, "Lead")
	r.join(lead, "GA5A", 0)
	for i := int64(2); i < 2+followMaxAsks; i++ {
		f := r.connect(i, fmt.Sprintf("F%d", i))
		r.join(f, "GA5A", 0)
		r.do(f, map[string]any{"t": "follow_start", "id": 1})
	}
	late := r.connect(99, "Late")
	r.join(late, "GA5A", 0)
	drain(late)
	r.do(late, map[string]any{"t": "follow_start", "id": 1})
	if m := followOne(t, drain(late), "follow_refused"); m["why"] != "busy" {
		t.Fatalf("busy %v", m)
	}
	// Asking someone else takes back the first ask.
	other := r.connect(50, "Other")
	r.join(other, "GA5A", 0)
	f2 := r.hub.ClientLocked(2)
	drain(lead)
	r.do(f2, map[string]any{"t": "follow_start", "id": 50})
	if m := followOne(t, drain(lead), "follow_ask_gone"); m["id"] != float64(2) {
		t.Fatalf("ask gone %v", m)
	}
	if len(r.m.asks[1]) != followMaxAsks-1 || r.m.asking[2] != 50 {
		t.Fatalf("asks %v asking %v", len(r.m.asks[1]), r.m.asking[2])
	}
}
