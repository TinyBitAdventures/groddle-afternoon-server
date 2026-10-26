package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// raceRig is a hub with only the races module, a manual clock and manual timers.
type raceRig struct {
	t      *testing.T
	hub    *Hub
	m      *races
	now    time.Time
	timers []*rigTimer
	ids    int
}

func testCourses() map[string]*raceCourse {
	quoins := make([][2]float64, 5)
	for i := range quoins {
		quoins[i] = [2]float64{float64(100 + 100*i), 450}
	}
	fin := [2]float64{900, 400}
	return map[string]*raceCourse{
		"grab_em_good": {Title: "Grab 'Em Good", Game: "quoin_grab", Kind: "quoins", Spawns: [][2]float64{{100, 500}, {600, 500}}, Quoins: quoins},
		"canyon_run":   {Title: "Canyon Run", Game: "race", Kind: "finish", Spawns: [][2]float64{{0, 500}, {0, 400}}, Finish: &fin},
	}
}

func newRaceRig(t *testing.T) *raceRig {
	r := &raceRig{t: t, now: time.Unix(1_700_000_000, 0)}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module {
		r.m = newRacesWith(h, testCourses())
		r.m.newID = func() string { r.ids++; return fmt.Sprintf("%016x", r.ids) }
		return r.m
	}}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

func (r *raceRig) fire(d time.Duration) int {
	n := 0
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if tm.d == d && !tm.stopped {
			tm.stopped = true
			tm.fn() // h.After's func takes the hub lock itself
			n++
		}
	}
	return n
}

func (r *raceRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *raceRig) at(cl *Client, street string, x, y float64) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": x, "y": y})
}

func (r *raceRig) move(cl *Client, x, y float64) {
	r.now = r.now.Add(60 * time.Millisecond)
	r.do(cl, map[string]any{"t": "move", "x": x, "y": y})
}

func one(t *testing.T, msgs []map[string]any, kind string) map[string]any {
	t.Helper()
	k := kinds(msgs, kind)
	if len(k) != 1 {
		t.Fatalf("want one %s, got %d in %v", kind, len(k), msgs)
	}
	return k[0]
}

// pair brings Ada and Bo onto GA1 and into one started Grab 'Em Good match.
func (r *raceRig) pair(course string) (ada, bo *Client, match map[string]any) {
	ada = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo = r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.at(ada, "GA1", 0, 0)
	r.at(bo, "GA1", 50, 0)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "race_challenge", "course": course})
	ch := one(r.t, drain(bo), "race_challenged")
	if ch["text"] != "Ada is looking for a challenger for "+r.m.courses[course].Title+"." {
		r.t.Fatalf("challenge text %v", ch["text"])
	}
	r.do(bo, map[string]any{"t": "race_accept", "from": 1})
	st := one(r.t, drain(ada), "race_starting")
	if st["text"] != "Starting "+r.m.courses[course].Title+" between Ada and Bo in 5 seconds!" {
		r.t.Fatalf("starting text %v", st["text"])
	}
	one(r.t, drain(bo), "race_starting")
	if r.fire(raceStartDelay) != 1 {
		r.t.Fatal("the start waits 5 s")
	}
	match = one(r.t, drain(ada), "race_match")
	mb := one(r.t, drain(bo), "race_match")
	if match["street"] != mb["street"] || match["match"] != mb["match"] {
		r.t.Fatal("both racers get one instance")
	}
	if match["slot"].(float64) != 0 || mb["slot"].(float64) != 1 {
		r.t.Fatalf("slots %v %v", match["slot"], mb["slot"])
	}
	return ada, bo, match
}

// run takes a paired match through Click to Start and the countdown to GO.
func (r *raceRig) run(ada, bo *Client, match map[string]any) (string, string) {
	id, street := match["match"].(string), match["street"].(string)
	r.at(ada, street, 100, 500)
	r.at(bo, street, 600, 500)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "race_ready", "match": id})
	rs := one(r.t, drain(bo), "race_ready_state")
	if rs["text"] != "Ada is ready to play. Need 1 more." {
		r.t.Fatalf("ready text %v", rs["text"])
	}
	r.do(bo, map[string]any{"t": "race_ready", "match": id})
	soon := one(r.t, drain(ada), "race_soon")
	if soon["text"] != "YAY! THE GAME IS STARTING SOON." || soon["in"].(float64) != 5000 {
		r.t.Fatalf("soon %v", soon)
	}
	r.fire(raceSoon)
	if c := one(r.t, drain(ada), "race_countdown"); c["in"].(float64) != 4000 {
		r.t.Fatalf("countdown %v", c)
	}
	r.fire(raceCountdown)
	one(r.t, drain(ada), "race_go")
	one(r.t, drain(bo), "race_go")
	return id, street
}

func TestAChallengeNeedsSomeoneElseOnTheStreet(t *testing.T) {
	r := newRaceRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.at(ada, "GA1", 0, 0)
	drain(ada)
	r.do(ada, map[string]any{"t": "race_challenge", "course": "grab_em_good"})
	if got := one(t, drain(ada), "race_refused"); got["why"] != "alone" || got["text"] != "You need at least one other player here." {
		t.Fatalf("alone: %v", got)
	}
	r.do(ada, map[string]any{"t": "race_challenge", "course": "nope"})
	if got := one(t, drain(ada), "race_refused"); got["why"] != "course" {
		t.Fatalf("an unknown course: %v", got)
	}
}

func TestAChallengeNobodyTakesExpiresAndGivesTheTicketBack(t *testing.T) {
	r := newRaceRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.at(ada, "GA1", 0, 0)
	r.at(bo, "GA1", 0, 0)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "race_challenge", "course": "grab_em_good"})
	w := one(t, drain(ada), "race_waiting")
	if w["text"] != "Waiting for other players..." || w["timeout"].(float64) != 60 || w["need"].(float64) != 1 {
		t.Fatalf("waiting %v", w)
	}
	r.do(ada, map[string]any{"t": "race_challenge", "course": "grab_em_good"})
	if got := one(t, drain(ada), "race_refused"); got["why"] != "busy" {
		t.Fatalf("a second challenge: %v", got)
	}
	// Cy arrives and hears the open challenge with the time left.
	cy := r.hub.Connect(Claims{Sub: 3, Name: "Cy"})
	r.now = r.now.Add(20 * time.Second)
	r.at(cy, "GA1", 0, 0)
	if ch := one(t, drain(cy), "race_challenged"); ch["timeout"].(float64) != 40 {
		t.Fatalf("a newcomer hears it with 40 s left: %v", ch)
	}
	r.fire(raceInviteTimeout)
	f := one(t, drain(ada), "race_failed")
	if f["text"] != "Not enough players accepted your challenge. Try again later?" || f["refund"] != true {
		t.Fatalf("failed %v", f)
	}
	if u := one(t, drain(bo), "race_unchallenged"); u["from"].(float64) != 1 {
		t.Fatalf("unchallenged %v", u)
	}
	r.do(bo, map[string]any{"t": "race_accept", "from": 1})
	if got := one(t, drain(bo), "race_refused"); got["why"] != "gone" {
		t.Fatalf("a late accept: %v", got)
	}
}

func TestLeavingTheStreetWithdrawsAChallenge(t *testing.T) {
	r := newRaceRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.at(ada, "GA1", 0, 0)
	r.at(bo, "GA1", 0, 0)
	r.do(ada, map[string]any{"t": "race_challenge", "course": "grab_em_good"})
	drain(ada)
	drain(bo)
	r.at(ada, "GA2", 0, 0)
	if f := one(t, drain(ada), "race_failed"); f["refund"] != true {
		t.Fatalf("withdrawn %v", f)
	}
	one(t, drain(bo), "race_unchallenged")
	if len(r.m.invites) != 0 {
		t.Fatal("the invite is gone")
	}
}

func TestAThirdAccepterIsTooLate(t *testing.T) {
	r := newRaceRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	cy := r.hub.Connect(Claims{Sub: 3, Name: "Cy"})
	for _, c := range []*Client{ada, bo, cy} {
		r.at(c, "GA1", 0, 0)
	}
	r.do(ada, map[string]any{"t": "race_challenge", "course": "grab_em_good"})
	r.do(bo, map[string]any{"t": "race_accept", "from": 1})
	drain(cy)
	r.do(cy, map[string]any{"t": "race_accept", "from": 1})
	got := one(t, drain(cy), "race_refused")
	if got["why"] != "full" || got["text"] != "Sorry, you were not quite fast enough to join Ada for Grab 'Em Good." {
		t.Fatalf("too late: %v", got)
	}
}

func TestGrabEmGoodIsRefereedByTheServer(t *testing.T) {
	r := newRaceRig(t)
	ada, bo, match := r.pair("grab_em_good")
	if match["total"].(float64) != 5 || len(match["quoins"].([]any)) != 5 {
		t.Fatalf("the match carries the quoins: %v", match)
	}
	id, _ := r.run(ada, bo, match)

	// A grab from too far is refused; one in reach scores.
	r.do(ada, map[string]any{"t": "race_grab", "match": id, "i": 4})
	if got := one(t, drain(ada), "race_grab_refused"); got["why"] != "far" {
		t.Fatalf("far: %v", got)
	}
	r.do(ada, map[string]any{"t": "race_grab", "match": id, "i": 0})
	g := one(t, drain(bo), "race_grabbed")
	if g["id"].(float64) != 1 || g["remaining"].(float64) != 4 || g["base"].(float64) != 10 {
		t.Fatalf("grabbed %v", g)
	}
	one(t, drain(ada), "race_grabbed")
	// First touch wins: Bo's touch of the same quoin is refused.
	r.move(bo, 100, 500)
	r.do(bo, map[string]any{"t": "race_grab", "match": id, "i": 0})
	if got := one(t, drain(bo), "race_grab_refused"); got["why"] != "gone" {
		t.Fatalf("gone: %v", got)
	}
	// Bo stands beside Ada, so her next grab shards him (quoin.js 83-154).
	r.move(ada, 200, 500)
	r.move(bo, 210, 500)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "race_grab", "match": id, "i": 1})
	g = one(t, drain(bo), "race_grabbed")
	sh := g["shards"].([]any)
	if len(sh) != 1 || sh[0].(map[string]any)["id"].(float64) != 2 || sh[0].(map[string]any)["pct"].(float64) != 0.5 {
		t.Fatalf("shards %v", g["shards"])
	}
	drain(ada)
	// 2-0 with 3 left: not yet. Bo takes one: 2-1, 2 left. Ada takes one: 3-1, 1 left: a lead of 2 beats 1.
	r.move(bo, 300, 500)
	r.do(bo, map[string]any{"t": "race_grab", "match": id, "i": 2})
	if len(kinds(drain(ada), "race_end")) != 0 {
		t.Fatal("2-1 with 2 left does not settle it")
	}
	r.move(ada, 400, 500)
	r.do(ada, map[string]any{"t": "race_grab", "match": id, "i": 3})
	end := one(t, drain(bo), "race_end")
	w, _ := end["winner"].(map[string]any)
	if w == nil || w["name"] != "Ada" || end["why"] != "won" || end["remaining"].(float64) != 1 {
		t.Fatalf("end %v", end)
	}
	one(t, drain(ada), "race_end")
	r.do(bo, map[string]any{"t": "race_grab", "match": id, "i": 4})
	if got := one(t, drain(bo), "race_grab_refused"); got["why"] != "state" {
		t.Fatalf("after the end: %v", got)
	}
	// Leaving after the end is quiet; once both go the match is forgotten.
	r.do(ada, map[string]any{"t": "race_leave", "match": id})
	r.at(bo, "GA1", 0, 0)
	if len(kinds(drain(bo), "race_end")) != 0 || len(r.m.matches) != 0 || len(r.m.matchOf) != 0 {
		t.Fatalf("forgotten: %v %v", r.m.matches, r.m.matchOf)
	}
}

func TestTheLastQuoinSettlesATie(t *testing.T) {
	// global_game.js 467: remaining == 0 names the grabber even on a level score.
	r := newRaceRig(t)
	r.m.courses["grab_em_good"].Quoins = [][2]float64{{100, 450}, {600, 450}}
	ada, bo, match := r.pair("grab_em_good")
	id, _ := r.run(ada, bo, match)
	r.do(ada, map[string]any{"t": "race_grab", "match": id, "i": 0})
	drain(ada)
	drain(bo)
	r.do(bo, map[string]any{"t": "race_grab", "match": id, "i": 1})
	end := one(t, drain(ada), "race_end")
	if w, _ := end["winner"].(map[string]any); w == nil || w["name"] != "Bo" {
		t.Fatalf("end %v", end)
	}
}

func TestLeavingARunningGameCallsItOffForEveryone(t *testing.T) {
	r := newRaceRig(t)
	ada, bo, match := r.pair("grab_em_good")
	id, _ := r.run(ada, bo, match)
	r.do(bo, map[string]any{"t": "race_leave", "match": id})
	end := one(t, drain(ada), "race_end")
	if end["winner"] != nil || end["why"] != "left" || end["left"].(map[string]any)["name"] != "Bo" {
		t.Fatalf("called off: %v", end)
	}
	one(t, drain(bo), "race_end")
}

func TestADroppedRacerHasAGraceToComeBack(t *testing.T) {
	r := newRaceRig(t)
	ada, bo, match := r.pair("grab_em_good")
	id, street := r.run(ada, bo, match)
	r.hub.Disconnect(bo)
	bo2 := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.at(bo2, street, 600, 500)
	if r.fire(raceGrace) != 0 {
		t.Fatal("coming back stops the grace timer")
	}
	r.do(bo2, map[string]any{"t": "race_grab", "match": id, "i": 0})
	if got := kinds(drain(bo2), "race_grab_refused"); len(got) != 1 || got[0]["why"] != "far" {
		t.Fatalf("still racing (just far): %v", got)
	}
	r.hub.Disconnect(bo2)
	drain(ada)
	r.fire(raceGrace)
	if end := one(t, drain(ada), "race_end"); end["why"] != "left" {
		t.Fatalf("gone for good: %v", end)
	}
}

func TestGoingToAnotherStreetLeavesTheGame(t *testing.T) {
	r := newRaceRig(t)
	ada, bo, match := r.pair("grab_em_good")
	r.run(ada, bo, match)
	r.at(bo, "GA1", 0, 0)
	if end := one(t, drain(ada), "race_end"); end["why"] != "left" {
		t.Fatalf("left: %v", end)
	}
}

func TestAnUnclickedRacerIsRemovedForInactivity(t *testing.T) {
	r := newRaceRig(t)
	ada, bo, match := r.pair("grab_em_good")
	id, street := match["match"].(string), match["street"].(string)
	r.at(ada, street, 100, 500)
	r.at(bo, street, 600, 500)
	r.do(ada, map[string]any{"t": "race_ready", "match": id})
	drain(ada)
	drain(bo)
	r.fire(raceAutoLeave)
	if got := one(t, drain(bo), "race_removed"); got["text"] != "You were removed from the game due to inactivity." {
		t.Fatalf("removed %v", got)
	}
	if end := one(t, drain(ada), "race_end"); end["why"] != "idle" {
		t.Fatalf("idle end %v", end)
	}
}

func TestReadyNeedsTheRacerInTheInstance(t *testing.T) {
	r := newRaceRig(t)
	ada, _, match := r.pair("grab_em_good")
	id := match["match"].(string)
	r.do(ada, map[string]any{"t": "race_ready", "match": id})
	if r.m.matches[id].members[0].ready {
		t.Fatal("ready from the old street does not count")
	}
	r.do(ada, map[string]any{"t": "race_ready", "match": "0000000000000099"})
}

func TestAFinishLineRaceGoesToTheFirstAcross(t *testing.T) {
	r := newRaceRig(t)
	ada, bo, match := r.pair("canyon_run")
	if f := match["finish"].([]any); f[0].(float64) != 900 {
		t.Fatalf("finish %v", match["finish"])
	}
	id, _ := r.run(ada, bo, match)
	r.do(bo, map[string]any{"t": "race_finish", "match": id})
	if got := one(t, drain(bo), "race_grab_refused"); got["why"] != "far" {
		t.Fatalf("far from the line: %v", got)
	}
	r.move(bo, 905, 400)
	r.do(bo, map[string]any{"t": "race_finish", "match": id})
	end := one(t, drain(ada), "race_end")
	if w, _ := end["winner"].(map[string]any); w == nil || w["name"] != "Bo" {
		t.Fatalf("end %v", end)
	}
}

func TestRaceGrabsAreRateLimited(t *testing.T) {
	r := newRaceRig(t)
	ada, bo, match := r.pair("grab_em_good")
	id, _ := r.run(ada, bo, match)
	drain(ada)
	for i := 0; i < raceGrabsPerSec+3; i++ {
		r.do(ada, map[string]any{"t": "race_grab", "match": id, "i": 4})
	}
	slow := 0
	for _, m := range kinds(drain(ada), "race_grab_refused") {
		if m["why"] == "slow" {
			slow++
		}
	}
	if slow != 3 {
		t.Fatalf("slow refusals %d", slow)
	}
}

func TestTheRaceDataFileParses(t *testing.T) {
	c, err := parseRaceData(racesJSON)
	if err != nil {
		t.Fatal(err)
	}
	g := c["grab_em_good"]
	if g == nil || g.Game != "quoin_grab" || len(g.Quoins) != 71 || len(g.Spawns) != 2 {
		t.Fatalf("grab_em_good %+v", g)
	}
	n := 0
	for _, row := range c {
		if row.Kind == "finish" {
			n++
		}
	}
	if n != 7 || !strings.Contains(c["canyon_run"].Title, "Canyon") {
		t.Fatalf("finish races %d", n)
	}
	if prettyList([]string{"A", "B", "C"}, " and ") != "A, B and C" {
		t.Fatal("pretty_list")
	}
}
