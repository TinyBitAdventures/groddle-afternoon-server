package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// G334 (lane RACES3): the dead do not race, a reload is a logout for a Game of Crowns too, and a
// crowns win the server decided outlives a restart.

const hellOne = "GA5PPFP86NF2FOS" // js/hell.js HELL_LOCATION.tsid

func TestNarakaIsHub40(t *testing.T) {
	if len(narakaStreets) != 11 || !inNaraka(hellOne) || !inNaraka("LA5PPFP86NF2FOS") || inNaraka("GA1") || inNaraka("") {
		t.Fatal("Hell One (either id) and the rest of hub 40, nothing else")
	}
}

func TestTheDeadCannotChallenge(t *testing.T) {
	r := newRaceRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.at(ada, hellOne, 0, 0)
	r.at(bo, hellOne, 50, 0)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "race_challenge", "course": "canyon_run"})
	if f := one(t, drain(ada), "race_refused"); f["why"] != "dead" || f["text"] != "You are too dead to be racing around." {
		t.Fatalf("race_ticket.js:119: %v", f)
	}
	if len(drain(bo)) != 0 || len(r.m.invites) != 0 {
		t.Fatal("no challenge from Naraka")
	}

	c := newCrownRig(t)
	var cls []*Client
	for i := 0; i < 4; i++ {
		cl := c.hub.Connect(Claims{Sub: int64(i + 1), Name: crownNames[i]})
		c.at(cl, hellOne, 0, 0)
		drain(cl)
		cls = append(cls, cl)
	}
	c.do(cls[0], map[string]any{"t": "crown_challenge"})
	if f := one(t, drain(cls[0]), "crown_refused"); f["why"] != "dead" || f["text"] != "You are too dead to be racing around." {
		t.Fatalf("race_ticket_it_game.js:119: %v", f)
	}
}

// startRaceWithCroak: Ada challenges, Bo accepts, then who croaks to Hell One during the 5 s start.
func startRaceWithCroak(t *testing.T, r *raceRig, course string, who int) (ada, bo *Client) {
	ada = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo = r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.at(ada, "GA1", 0, 0)
	r.at(bo, "GA1", 50, 0)
	r.do(ada, map[string]any{"t": "race_challenge", "course": course})
	r.do(bo, map[string]any{"t": "race_accept", "from": 1})
	one(t, drain(ada), "race_starting")
	one(t, drain(bo), "race_starting")
	dead := ada
	if who == 2 {
		dead = bo
	}
	r.at(dead, hellOne, 0, 0) // the croak: Naraka takes them while the start runs
	drain(ada)
	drain(bo)
	if r.fire(raceStartDelay) != 1 {
		t.Fatal("the start still fires")
	}
	return ada, bo
}

func TestAChallengerWhoCroaksDuringTheStartDoesNotRace(t *testing.T) {
	for _, course := range []string{"canyon_run", hogHaulCourse} {
		r := newRaceRig(t)
		if course == hogHaulCourse {
			hog, err := parseHogHaul(hogHaulJSON)
			if err != nil || hog == nil {
				t.Fatal("the shipped haul", err)
			}
			r.m.courses[hogHaulCourse] = hog
		}
		ada, bo := startRaceWithCroak(t, r, course, 1)
		a := drain(ada)
		if len(kinds(a, "race_match")) != 0 {
			t.Fatalf("%s: nobody races from Naraka", course)
		}
		if f := one(t, a, "race_failed"); f["refund"] != true || f["text"] != "You are too dead to be racing around." || f["course"] != course {
			t.Fatalf("%s: the dead challenger's game never began, so the ticket comes back: %v", course, f)
		}
		b := drain(bo)
		if len(kinds(b, "race_match")) != 0 {
			t.Fatalf("%s: no game for one", course)
		}
		if f := one(t, b, "race_failed"); f["refund"] != false {
			t.Fatalf("%s: Bo spent nothing: %v", course, f)
		}
		if len(r.m.matches) != 0 || r.m.busy(1) || r.m.busy(2) {
			t.Fatalf("%s: nothing left behind", course)
		}
		// A page that missed it asks: the server kept the refund.
		r.do(ada, map[string]any{"t": "race_pending", "course": course})
		if tk := one(t, drain(ada), "race_ticket"); tk["refund"] != true {
			t.Fatalf("%s: the fate %v", course, tk)
		}
	}
}

func TestAnAccepterWhoCroaksDuringTheStartDoesNotRace(t *testing.T) {
	r := newRaceRig(t)
	ada, bo := startRaceWithCroak(t, r, "canyon_run", 2)
	if f := one(t, drain(ada), "race_failed"); f["refund"] != true {
		t.Fatalf("Ada's challenge never became a game: %v", f)
	}
	if f := one(t, drain(bo), "race_failed"); f["refund"] != false || f["text"] != "You are too dead to be racing around." {
		t.Fatalf("Bo is told why: %v", f)
	}
}

func TestACrownsMemberWhoCroaksDuringTheStartIsLeftOut(t *testing.T) {
	r := newCrownRig(t)
	var cls []*Client
	for i := 0; i < 4; i++ {
		cl := r.hub.Connect(Claims{Sub: int64(i + 1), Name: crownNames[i]})
		r.at(cl, "GA1", float64(i*50), 0)
		cls = append(cls, cl)
	}
	r.do(cls[0], map[string]any{"t": "crown_challenge"})
	for _, cl := range cls[1:] {
		r.do(cl, map[string]any{"t": "crown_accept", "from": 1})
	}
	r.at(cls[0], hellOne, 0, 0) // Ada, the challenger, croaks
	for _, cl := range cls {
		drain(cl)
	}
	r.fire(crownsStartDelay)
	a := drain(cls[0])
	if len(kinds(a, "crown_match")) != 0 {
		t.Fatal("Ada stays in Naraka")
	}
	if f := one(t, a, "crown_failed"); f["refund"] != false || f["text"] != "You are too dead to be racing around." {
		t.Fatalf("three still play, so the ticket is spent: %v", f)
	}
	for _, cl := range cls[1:] {
		mm := one(t, drain(cl), "crown_match")
		if len(mm["players"].([]any)) != 3 {
			t.Fatalf("the three alive play: %v", mm["players"])
		}
	}
	r.do(cls[0], map[string]any{"t": "crown_pending"})
	if tk := one(t, drain(cls[0]), "crown_ticket"); tk["refund"] != false {
		t.Fatalf("spent: %v", tk)
	}
}

// A reload during the 5 s start: the page lost its in-flight state but holds the spent ticket, and asks.
func TestCrownsPendingTicketFollowsTheServer(t *testing.T) {
	r := newCrownRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.at(ada, "GA1", 0, 0)
	r.at(bo, "GA1", 0, 0)
	r.do(ada, map[string]any{"t": "crown_challenge"})
	drain(ada)
	// Still open: called off, refunded (crown_failed is the answer).
	r.do(ada, map[string]any{"t": "crown_pending"})
	if f := one(t, drain(ada), "crown_failed"); f["refund"] != true || f["text"] != crownsRefunded {
		t.Fatalf("open challenge %v", f)
	}
	if len(r.m.invites) != 0 {
		t.Fatal("called off")
	}
	// A challenge the server never knew (a restart): it never became a game.
	r.do(ada, map[string]any{"t": "crown_pending"})
	if tk := one(t, drain(ada), "crown_ticket"); tk["refund"] != true {
		t.Fatalf("unknown %v", tk)
	}
	// A match made: spent, and the asker is out of it while the other three play on.
	r2 := newCrownRig(t)
	cls, match := r2.four()
	id, street := match["match"].(string), match["street"].(string)
	_ = street
	r2.hub.Disconnect(cls[0])
	ada2 := r2.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r2.do(ada2, map[string]any{"t": "crown_pending"})
	if tk := one(t, drain(ada2), "crown_ticket"); tk["refund"] != false {
		t.Fatalf("spent %v", tk)
	}
	mt := r2.m.matches[id]
	if mt == nil || !mt.member(1).gone || mt.status == "done" {
		t.Fatal("Ada is out, the game is not")
	}
}

// A reload mid-game is a logout (instance.js player_logout -> playerLeft): the leaver is out, the holder's
// crown passes, and the other three play on (global_game.js 803-813).
func TestACrownsReloadMidGameLeavesTheRestPlaying(t *testing.T) {
	r := newCrownRig(t)
	cls, match := r.four()
	r.play(cls, match)
	mt := r.m.matches[match["match"].(string)]
	r.put(cls[0], 200, 900)
	for i := 0; i < 30; i++ {
		r.tick()
	}
	for _, cl := range cls {
		drain(cl)
	}
	r.hub.Disconnect(cls[0]) // Ada (the holder) reloads
	if mt.it == 1 {
		t.Fatal("the crown passes at once")
	}
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.at(ada, "GA1", 0, 0) // the page comes back where the challenge was made
	if l := one(t, drain(ada), "crown_left"); l["seconds"].(float64) != 2 {
		t.Fatalf("her crown time goes with her: %v", l)
	}
	if !mt.member(1).gone || mt.status != "running" {
		t.Fatalf("out, and the game goes on: %s", mt.status)
	}
	r.do(ada, map[string]any{"t": "join", "street": mt.street, "x": 0, "y": 0})
	if ada.Street() == mt.street {
		t.Fatal("no way back into the arena")
	}
}

// winCrowns plays four through to Ada's win (60 s below the play line).
func winCrowns(t *testing.T, r *crownRig) []*Client {
	cls, match := r.four()
	r.play(cls, match)
	r.put(cls[0], 200, 900)
	for i := 0; i < 601; i++ {
		r.tick()
	}
	if r.m.matches[match["match"].(string)].status != "done" {
		t.Fatal("Ada wins")
	}
	return cls
}

func TestACrownsRewardOutlivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "realtime-crowns.json")
	r := newCrownRig(t)
	r.m.useFile(path)
	cls := winCrowns(t, r)
	rw := one(t, drain(cls[0]), "crown_reward")
	// The server restarts before Ada's page acks it.
	r2 := newCrownRig(t)
	r2.m.useFile(path)
	ada := r2.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	again := one(t, drain(ada), "crown_reward")
	if again["id"] != rw["id"] || again["xp"].(float64) != 200 || again["currants"].(float64) != 50 {
		t.Fatalf("the same reward again: %v", again)
	}
	if len(r2.led.calls) != 0 {
		t.Fatal("a settled win is not credited again")
	}
	r2.do(ada, map[string]any{"t": "crown_ack", "id": again["id"]})
	r3 := newCrownRig(t)
	r3.m.useFile(path)
	ada = r3.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	if len(kinds(drain(ada), "crown_reward")) != 0 {
		t.Fatal("acked: gone for good")
	}
}

func TestACrownsWinTheLedgerNeverAnsweredIsAskedAgainAfterARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "realtime-crowns.json")
	r := newCrownRig(t)
	r.m.useFile(path)
	r.led.answer = "" // the site is down
	cls := winCrowns(t, r)
	if len(kinds(drain(cls[0]), "crown_reward")) != 0 {
		t.Fatal("not owed until the ledger answers")
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		t.Fatal("the win waiting for the ledger is on disk", err)
	}
	r2 := newCrownRig(t)
	r2.m.useFile(path)
	if len(r2.m.credits) != 1 {
		t.Fatalf("loaded %v", r2.m.credits)
	}
	ada := r2.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r2.fire(0)
	if len(r2.led.calls) != 1 {
		t.Fatalf("asked again when Ada connects: %v", r2.led.calls)
	}
	if rw := one(t, drain(ada), "crown_reward"); rw["currants"].(float64) != 50 {
		t.Fatalf("settled now: %v", rw)
	}
	if len(r2.m.credits) != 0 {
		t.Fatal("credited")
	}
}

func TestABrokenCrownsFileIsMovedAsideAndSavingGoesOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "realtime-crowns.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newCrownRig(t)
	r.m.useFile(path)
	if r.m.path != path {
		t.Fatal("the module goes on saving")
	}
	if bad, _ := filepath.Glob(path + ".bad-*"); len(bad) != 1 {
		t.Fatalf("the broken file is kept aside: %v", bad)
	}
	winCrowns(t, r)
	if b, err := os.ReadFile(path); err != nil || !json.Valid(b) {
		t.Fatal("a good file is written", err)
	}
}

// During the 5 s start of a filled challenge: a challenger whose page went away (a reload) has called it
// off (onDisconnect) and hears the refund when it asks; one still connected is answered by the start.
func TestCrownsPendingDuringAFilledStart(t *testing.T) {
	fill := func(r *crownRig) []*Client {
		var cls []*Client
		for i := 0; i < 4; i++ {
			cl := r.hub.Connect(Claims{Sub: int64(i + 1), Name: crownNames[i]})
			r.at(cl, "GA1", float64(i*50), 0)
			cls = append(cls, cl)
		}
		r.do(cls[0], map[string]any{"t": "crown_challenge"})
		for _, cl := range cls[1:] {
			r.do(cl, map[string]any{"t": "crown_accept", "from": 1})
		}
		for _, cl := range cls {
			drain(cl)
		}
		return cls
	}
	r := newCrownRig(t)
	cls := fill(r)
	r.hub.Disconnect(cls[0])
	if f := one(t, drain(cls[1]), "crown_failed"); f["refund"] != false {
		t.Fatalf("called off for the others: %v", f)
	}
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.do(ada, map[string]any{"t": "crown_pending"})
	if tk := one(t, drain(ada), "crown_ticket"); tk["refund"] != true {
		t.Fatalf("refunded: %v", tk)
	}

	r = newCrownRig(t)
	cls = fill(r)
	r.do(cls[0], map[string]any{"t": "crown_pending"})
	if got := drain(cls[0]); len(kinds(got, "crown_ticket"))+len(kinds(got, "crown_failed")) != 0 {
		t.Fatalf("not called off: %v", got)
	}
	r.fire(crownsStartDelay)
	if mm := one(t, drain(cls[0]), "crown_match"); len(mm["players"].([]any)) != 4 {
		t.Fatalf("Ada plays: %v", mm)
	}
}
