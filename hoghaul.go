package main

// The Great Hog Haul between real players (G334): a Great Hog Haul Ticket
// challenges the street like the other ticket races (races.go: the invite,
// the accept, Click to Start, the countdown, leaving), and the server
// referees the haul itself from the layout it owns (data/hoghaul.json, written
// by the game's tools/build-realtime-hoghaul.mjs).
//
// The rules are GameServerJS's:
//   - config_prod.js:1286 shared_instances.hogtie_piggy: min_players 2,
//     max_players 2, 'The Great Hog Haul'. Its instance LHVLC8CI3Q62P8T is not
//     archived; the game's stand-in is the quest's cave (js/hog-haul.js
//     HOG_HAUL_STREET_ID), laid out as solo lays it out.
//   - global_game.js initHogtiePiggyInInstance 634-682: one location for both
//     racers, every Piggy and sack on it shared (hit boxes 100 x 50, 652-655).
//   - items/pig_bait.js onPlayerCollision 116-122: a sack is picked up by a
//     racer carrying nothing (no has_pig_bait, no has_hogtied_piggy), and is
//     gone for everyone (apiDelete).
//   - items/npc_piggy.js onPlayerCollision 1063-1068 and inc_games.js
//     games_hogtie_piggy_pickup_pig 1957-2021: a loose Piggy is hogtied by a
//     racer with bait and no Piggy, and is gone (apiDelete); without bait,
//     "You need bait first!" (2017).
//   - location.js 1428-1429: the pig_pen box takes a hogtied Piggy;
//     inc_games.js games_hogtie_piggy_add_pig 2023-2076: the score goes up for
//     everyone (global_game.js hogtie_piggy_add_pig 696-705), three wins
//     (2070-2071, hogtie_piggy_set_winner 685-690).
//   - inc_games.js games_end_hogtie_piggy 2088-2183: the winner +50 iMG and
//     +100 currants (2134-2135), the others nothing; credits.go sends the
//     winner's currants to the site's ledger as for a race.
//   - global_game.js playerLeft 814-815: leaving calls it off (races.go drop).
//
// Ours: a racer touches a sack or a Piggy within the source's 100 x 50 box
// (plus a Piggy's pacing range and the same slack as races.go, since moves
// arrive at most every 100 ms), and the pen within solo's 310 x 145 box.
//
// Client to server (besides races.go's):
//
//	{"t":"race_bait","match":"…","i":3}   touched sack 3
//	{"t":"race_pig","match":"…","i":2}    touched Piggy 2
//	{"t":"race_pen","match":"…"}          at the Pig Pen
//
// Server to client:
//
//	race_match for the haul also has "home":"GHVJQH26ERF3VMJ","goal":3,"pen":[x,y],"baits":[[x,y]…],"pigs":[[x,y,range]…]
//	{"t":"race_hog","match":"…","what":"bait|pig|pen","i":3,"id":7,"name":"Ada","score":1,
//	    "scores":[{"id","name","score"}],"carry":{"7":"pig"}}
//	{"t":"race_hog_refused","match":"…","what":"pig","i":2,"why":"needs_bait|gone|far|hands|state|street|unknown|slow","text":"You need bait first!"}

import (
	_ "embed"
	"encoding/json"
	"math"
	"strconv"
)

//go:embed data/hoghaul.json
var hogHaulJSON []byte

const (
	hogHaulCourse = "hogtie_piggy"
	hogHaulGoal   = 3 // inc_games.js:2070 score >= 3

	// global_game.js:652-655 apiSetHitBox(100, 50) on every Piggy and sack.
	hogBoxHalfW = 50.0
	hogBoxH     = 50.0
	// game.js spawnHogHaulPen: solo's pen box, 310 x 145 above the ground.
	hogPenHalfW = 155.0
	hogPenH     = 145.0
)

func parseHogHaul(b []byte) (*raceCourse, error) {
	var raw struct {
		Street string       `json:"street"`
		Title  string       `json:"title"`
		Goal   int          `json:"goal"`
		Spawns [][2]float64 `json:"spawns"`
		Pen    *[2]float64  `json:"pen"`
		Baits  [][2]float64 `json:"baits"`
		Pigs   [][3]float64 `json:"pigs"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if raw.Pen == nil || len(raw.Spawns) < 2 || len(raw.Baits) == 0 || len(raw.Pigs) == 0 || raw.Street == "" {
		return nil, nil
	}
	goal := raw.Goal
	if goal <= 0 {
		goal = hogHaulGoal
	}
	return &raceCourse{Title: raw.Title, Game: "hogtie_piggy", Kind: "pigs", Spawns: raw.Spawns,
		Home: raw.Street, Goal: goal, Pen: raw.Pen, Baits: raw.Baits, Pigs: raw.Pigs}, nil
}

func hogMatchFields(c *raceCourse, msg map[string]any) {
	msg["home"] = c.Home
	msg["goal"] = c.Goal
	msg["pen"] = c.Pen
	msg["baits"] = c.Baits
	msg["pigs"] = c.Pigs
}

func (m *races) carryRows(mt *raceMatch) map[string]string {
	out := map[string]string{}
	for id, what := range mt.carry {
		if what != "" {
			out[strconv.FormatInt(id, 10)] = what
		}
	}
	return out
}

// hogClaim checks a haul claim's common ground and answers a refusal; it returns the match when the claim may go on.
func (m *races) hogClaim(cl *Client, frame json.RawMessage, what string) (*raceMatch, int, bool) {
	var msg struct {
		Match string `json:"match"`
		I     *int   `json:"i"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return nil, 0, false
	}
	i := -1
	if msg.I != nil {
		i = *msg.I
	}
	refuse := func(why string) {
		m.h.SendLocked(cl, map[string]any{"t": "race_hog_refused", "match": msg.Match, "what": what, "i": i, "why": why})
	}
	mt, _ := m.matchFor(cl, msg.Match)
	if mt == nil || mt.course.Kind != "pigs" || mt.status != "running" {
		refuse("state")
		return nil, 0, false
	}
	if !m.allowGrab(cl) {
		refuse("slow")
		return nil, 0, false
	}
	if cl.Street() != mt.street {
		refuse("street")
		return nil, 0, false
	}
	return mt, i, true
}

// near: within a source hit box (half width, height above the feet) of a spot on the ground, give or take range and the slack.
func near(px, py, x, y, halfW, h, rangeX float64) bool {
	return math.Abs(px-x) <= halfW+rangeX+raceSlack && py >= y-h-raceSlack && py <= y+h+raceSlack
}

func (m *races) hogTold(mt *raceMatch, cl *Client, what string, i int) {
	m.sendMatch(mt, map[string]any{"t": "race_hog", "match": mt.id, "what": what, "i": i, "id": cl.ID(), "name": cl.Name(),
		"score": mt.scores[cl.ID()], "scores": m.scoreRows(mt), "carry": m.carryRows(mt)})
}

// hogBait is pig_bait.js onPlayerCollision 116-122.
func (m *races) hogBait(cl *Client, frame json.RawMessage) {
	mt, i, ok := m.hogClaim(cl, frame, "bait")
	if !ok {
		return
	}
	refuse := func(why string) {
		m.h.SendLocked(cl, map[string]any{"t": "race_hog_refused", "match": mt.id, "what": "bait", "i": i, "why": why})
	}
	if i < 0 || i >= len(mt.course.Baits) {
		refuse("unknown")
		return
	}
	if mt.baitsTaken[i] {
		refuse("gone")
		return
	}
	if mt.carry[cl.ID()] != "" {
		refuse("hands")
		return
	}
	b := mt.course.Baits[i]
	px, py := cl.Pos()
	if !near(px, py, b[0], b[1], hogBoxHalfW, hogBoxH, 0) {
		refuse("far")
		return
	}
	mt.baitsTaken[i] = true
	mt.carry[cl.ID()] = "bait"
	m.hogTold(mt, cl, "bait", i)
}

// hogPig is npc_piggy.js onPlayerCollision 1063-1068 + games_hogtie_piggy_pickup_pig.
func (m *races) hogPig(cl *Client, frame json.RawMessage) {
	mt, i, ok := m.hogClaim(cl, frame, "pig")
	if !ok {
		return
	}
	refuse := func(why, text string) {
		out := map[string]any{"t": "race_hog_refused", "match": mt.id, "what": "pig", "i": i, "why": why}
		if text != "" {
			out["text"] = text
		}
		m.h.SendLocked(cl, out)
	}
	if i < 0 || i >= len(mt.course.Pigs) {
		refuse("unknown", "")
		return
	}
	if mt.pigsTaken[i] {
		refuse("gone", "")
		return
	}
	p := mt.course.Pigs[i]
	px, py := cl.Pos()
	if !near(px, py, p[0], p[1], hogBoxHalfW, hogBoxH, p[2]) {
		refuse("far", "")
		return
	}
	switch mt.carry[cl.ID()] {
	case "pig":
		refuse("hands", "")
		return
	case "":
		refuse("needs_bait", "You need bait first!") // inc_games.js:2017
		return
	}
	mt.pigsTaken[i] = true
	mt.carry[cl.ID()] = "pig"
	m.hogTold(mt, cl, "pig", i)
}

// hogPen is location.js 1428-1429 + games_hogtie_piggy_add_pig 2023-2076.
func (m *races) hogPen(cl *Client, frame json.RawMessage) {
	mt, _, ok := m.hogClaim(cl, frame, "pen")
	if !ok {
		return
	}
	if mt.carry[cl.ID()] != "pig" {
		m.h.SendLocked(cl, map[string]any{"t": "race_hog_refused", "match": mt.id, "what": "pen", "i": -1, "why": "hands"})
		return
	}
	pen := *mt.course.Pen
	px, py := cl.Pos()
	if !near(px, py, pen[0], pen[1], hogPenHalfW, hogPenH, 0) {
		m.h.SendLocked(cl, map[string]any{"t": "race_hog_refused", "match": mt.id, "what": "pen", "i": -1, "why": "far"})
		return
	}
	delete(mt.carry, cl.ID())
	mt.scores[cl.ID()]++
	m.hogTold(mt, cl, "pen", -1)
	if mt.scores[cl.ID()] >= mt.course.Goal {
		m.end(mt, &raceRef{ID: cl.ID(), Name: cl.Name()}, "won", nil)
	}
}
