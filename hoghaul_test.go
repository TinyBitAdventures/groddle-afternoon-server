package main

import (
	"testing"
	"time"
)

// hogRig is a race rig whose courses include a small Great Hog Haul: two sacks, two Piggies, the pen at 500.
func hogRig(t *testing.T) *raceRig {
	r := newRaceRig(t)
	pen := [2]float64{500, 500}
	r.m.courses[hogHaulCourse] = &raceCourse{Title: "The Great Hog Haul", Game: "hogtie_piggy", Kind: "pigs",
		Spawns: [][2]float64{{350, 500}, {650, 500}}, Home: "GHVJQH26ERF3VMJ", Goal: 3, Pen: &pen,
		Baits: [][2]float64{{1000, 500}, {1400, 500}, {1800, 500}, {2200, 500}},
		Pigs:  [][3]float64{{3000, 500, 90}, {3400, 500, 90}, {3800, 500, 90}, {4200, 500, 90}}}
	return r
}

func TestTheShippedHogHaulLayoutParses(t *testing.T) {
	c, err := parseHogHaul(hogHaulJSON)
	if err != nil || c == nil {
		t.Fatalf("data/hoghaul.json: %v %v", c, err)
	}
	if c.Kind != "pigs" || c.Game != "hogtie_piggy" || c.Goal != 3 || len(c.Baits) != 11 || len(c.Pigs) != 8 || c.Pen == nil || c.Home != "GHVJQH26ERF3VMJ" {
		t.Fatalf("layout %+v", c)
	}
	if raceGames["hogtie_piggy"].min != 2 || raceGames["hogtie_piggy"].max != 2 || raceGames["hogtie_piggy"].name != "The Great Hog Haul" {
		t.Fatal("config_prod.js:1286")
	}
	if newRaces(NewHub(nil)).(*races).courses[hogHaulCourse] == nil {
		t.Fatal("the module serves the haul")
	}
}

func TestTheGreatHogHaulIsRefereedByTheServer(t *testing.T) {
	r := hogRig(t)
	var credited []string
	r.hub.provided["credits.add"] = func(user int64, why string, got tradeGoods) string {
		credited = append(credited, why)
		if user != 1 || why != "race" || got.Currants != 100 {
			t.Fatalf("credit %d %s %+v", user, why, got)
		}
		return ""
	}
	ada, bo, match := r.pair(hogHaulCourse)
	if match["home"] != "GHVJQH26ERF3VMJ" || match["goal"].(float64) != 3 || len(match["baits"].([]any)) != 4 || len(match["pigs"].([]any)) != 4 || match["pen"] == nil {
		t.Fatalf("the match carries the layout: %v", match)
	}
	id, _ := r.run(ada, bo, match)

	// pig_bait.js 116-122 / npc_piggy.js 1063-1068: no bait, no Piggy ("You need bait first!").
	r.move(ada, 3000, 500)
	r.do(ada, map[string]any{"t": "race_pig", "match": id, "i": 0})
	if got := one(t, drain(ada), "race_hog_refused"); got["why"] != "needs_bait" || got["text"] != "You need bait first!" {
		t.Fatalf("needs bait: %v", got)
	}
	// A sack out of reach is refused; one in reach is Ada's and gone for everyone.
	r.do(ada, map[string]any{"t": "race_bait", "match": id, "i": 0})
	if got := one(t, drain(ada), "race_hog_refused"); got["why"] != "far" {
		t.Fatalf("far: %v", got)
	}
	r.move(ada, 1000, 500)
	r.do(ada, map[string]any{"t": "race_bait", "match": id, "i": 0})
	b := one(t, drain(bo), "race_hog")
	if b["what"] != "bait" || b["id"].(float64) != 1 || b["carry"].(map[string]any)["1"] != "bait" {
		t.Fatalf("bait %v", b)
	}
	drain(ada)
	r.move(bo, 1000, 500)
	r.do(bo, map[string]any{"t": "race_bait", "match": id, "i": 0})
	if got := one(t, drain(bo), "race_hog_refused"); got["why"] != "gone" {
		t.Fatalf("gone: %v", got)
	}
	// One sack at a time.
	r.move(ada, 1400, 500)
	r.do(ada, map[string]any{"t": "race_bait", "match": id, "i": 1})
	if got := one(t, drain(ada), "race_hog_refused"); got["why"] != "hands" {
		t.Fatalf("hands full: %v", got)
	}
	// The pen takes only a hogtied Piggy.
	r.move(ada, 500, 500)
	r.do(ada, map[string]any{"t": "race_pen", "match": id})
	if got := one(t, drain(ada), "race_hog_refused"); got["why"] != "hands" {
		t.Fatalf("no piggy: %v", got)
	}
	// A pacing Piggy is reachable round its spot.
	hogtie := func(cl *Client, bait, pig int) {
		t.Helper()
		r.now = r.now.Add(time.Second) // under races.go's eight claims a second
		bx := r.m.courses[hogHaulCourse].Baits[bait][0]
		px := r.m.courses[hogHaulCourse].Pigs[pig][0]
		r.move(cl, bx, 500)
		r.do(cl, map[string]any{"t": "race_bait", "match": id, "i": bait})
		r.move(cl, px+80, 500)
		r.do(cl, map[string]any{"t": "race_pig", "match": id, "i": pig})
		r.move(cl, 500, 480)
		r.do(cl, map[string]any{"t": "race_pen", "match": id})
	}
	r.move(ada, 3080, 500)
	r.do(ada, map[string]any{"t": "race_pig", "match": id, "i": 0})
	if p := one(t, drain(bo), "race_hog"); p["what"] != "pig" || p["carry"].(map[string]any)["1"] != "pig" {
		t.Fatalf("pig %v", p)
	}
	r.move(ada, 520, 470)
	r.do(ada, map[string]any{"t": "race_pen", "match": id})
	pen := one(t, drain(bo), "race_hog")
	if pen["what"] != "pen" || pen["score"].(float64) != 1 || len(pen["carry"].(map[string]any)) != 0 {
		t.Fatalf("pen %v", pen)
	}
	drain(ada)
	// Bo scores once, Ada twice more: three wins, and only the winner is credited.
	hogtie(bo, 2, 1)
	hogtie(ada, 1, 2)
	drain(ada)
	drain(bo)
	hogtie(ada, 3, 3)
	end := one(t, drain(bo), "race_end")
	if w, _ := end["winner"].(map[string]any); w == nil || w["name"] != "Ada" || end["why"] != "won" || end["title"] != "The Great Hog Haul" {
		t.Fatalf("end %v", end)
	}
	if len(credited) != 1 {
		t.Fatalf("credited %v", credited)
	}
}

// G349: what a page that held a spent ticket hears when it asks.
func TestAPendingTicketIsSpentOnceAMatchIsMadeAndRefundedOtherwise(t *testing.T) {
	r := newRaceRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.at(ada, "GA1", 0, 0)
	r.at(bo, "GA1", 50, 0)
	drain(ada)
	drain(bo)

	// Never heard of: never became a game, so the ticket comes back.
	r.do(ada, map[string]any{"t": "race_pending", "course": "canyon_run"})
	if got := one(t, drain(ada), "race_ticket"); got["refund"] != true || got["text"] != raceRefunded {
		t.Fatalf("unknown: %v", got)
	}
	// An open challenge whose page went: called off and refunded (race_failed is the answer).
	r.do(ada, map[string]any{"t": "race_challenge", "course": "canyon_run"})
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "race_pending", "course": "canyon_run"})
	if got := one(t, drain(ada), "race_failed"); got["refund"] != true {
		t.Fatalf("open: %v", got)
	}
	if len(r.m.invites) != 0 || len(r.m.fates) != 0 {
		t.Fatal("the challenge is gone and its fate told")
	}
	one(t, drain(bo), "race_unchallenged")
	// A challenge withdrawn while the page was away: the fate waits for it.
	r.do(ada, map[string]any{"t": "race_challenge", "course": "canyon_run"})
	r.hub.Disconnect(ada)
	ada = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.at(ada, "GA1", 0, 0)
	drain(ada)
	r.do(ada, map[string]any{"t": "race_pending", "course": "canyon_run"})
	if got := one(t, drain(ada), "race_ticket"); got["refund"] != true {
		t.Fatalf("withdrawn while away: %v", got)
	}
	// A match made: spent, even after the race.
	ada2, bo2, match := r.pair("canyon_run")
	id, _ := r.run(ada2, bo2, match)
	r.do(ada2, map[string]any{"t": "race_leave", "match": id})
	drain(ada2)
	drain(bo2)
	r.do(ada2, map[string]any{"t": "race_pending", "course": "canyon_run"})
	if got := one(t, drain(ada2), "race_ticket"); got["refund"] != false {
		t.Fatalf("spent: %v", got)
	}
	// Told once: asked again, the fate is gone and a new challenge starts clean.
	if len(r.m.fates) != 0 {
		t.Fatalf("fates %v", r.m.fates)
	}
}

// G334: a reload mid-race is a logout (instance.js player_logout 380-394): the race is called off for everyone at once.
func TestAReloadedRacerAskingCallsTheRaceOffAtOnce(t *testing.T) {
	r := newRaceRig(t)
	ada, bo, match := r.pair("canyon_run")
	id, _ := r.run(ada, bo, match)
	r.hub.Disconnect(ada)
	ada = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.do(ada, map[string]any{"t": "race_pending", "course": "canyon_run"})
	got := drain(ada)
	if end := one(t, got, "race_end"); end["why"] != "left" || end["match"] != id || end["title"] != "Canyon Run" {
		t.Fatalf("the leaver hears it ended: %v", end)
	}
	if tk := one(t, got, "race_ticket"); tk["refund"] != false {
		t.Fatalf("spent: %v", tk)
	}
	if end := one(t, drain(bo), "race_end"); end["why"] != "left" || end["winner"] != nil {
		t.Fatalf("called off for Bo without waiting out the grace: %v", end)
	}
}
