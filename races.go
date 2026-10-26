package main

// Races and games between real players (multiplayer phase 4): a Race Ticket
// challenges the street, the players who accept race each other in one
// private instance, and the server referees. First game: Grab 'Em Good
// (shared_instances.quoin_grab); the seven finish-line races (race) run on
// the same machinery.
//
// The rules are GameServerJS's ticket games:
//   - items/race_ticket.js verbs.activate 116-158: activating needs at least
//     one other player on the street ("You need at least one other player
//     here.", 121); the ticket is spent (150) and games_invite_create runs.
//   - players/inc_games.js games_invite_create 2387-2419: the street hears
//     "<name> is looking for a challenger for <game>." (2392), the
//     challenger waits ("Waiting for other players...", 60 s, 2396-2409).
//     games_invite_timeout 2421-2454: nobody came, "Not enough players
//     accepted your challenge. Try again later?" and the ticket back
//     (2448-2450). games_accept 2456-2477 and games_add_opponent 2496-2580:
//     min_players-1 opponents fill it ("Sorry, you were not quite fast
//     enough to join <name> for <game>.", 2467); full, everyone gets
//     "Starting <game> between <a> and <b> in 5 seconds!" (2530, pretty_list
//     common.js:765) and 5 s later (2559) games_invite_start 2597-2617 puts
//     them all in one new instance (shared_instance_manager.js
//     playerJoinInstance, force_uid).
//   - config_prod.js:1279-1287 shared_instances: quoin_grab and race are
//     min_players 2, max_players 2, can_retry false.
//   - groups/shared_instance_manager.js: on entering, the splash with Click
//     to Start, and games_auto_leave after 2 min unclicked (210, inc_games.js
//     2301-2306 "You were removed from the game due to inactivity.");
//     dismissOverlaysAndStart 231-342: "You must wait for more players
//     before this game can begin." (261), "is ready to play. Need N more."
//     (336) or "... Just waiting on a few more slackers to HIT THE START
//     BUTTON." (339); once can_start_game (instance.js 579-602: at least
//     min ready, nobody unready) "is ready to play. LET'S GO!" (278),
//     "YAY! THE GAME IS STARTING SOON." for 5 s (276, 308), then
//     startCountdown 646-689 (the 4000 ms 321_countdown) and startGame.
//   - groups/global_game.js quoin_grab_get_quoin 447-476: a grab scores one;
//     remaining = the instance's quoins less every score; the grabber wins
//     when their lead over everyone beats the quoins left, or the last one
//     is gone. race_set_winner 381-386: the first across the line wins.
//     playerLeft 797-828: a player leaving a running race or quoin grab
//     (logout too: instance.js player_logout 380-394) cancels it for
//     everyone, with no winner (inc_games.js games_end_quoin_grab 1587-1610:
//     the logo splash, no reward).
//   - items/quoin.js onPlayerCollision 156-310: in a game a quoin skips the
//     daily limit (159), pays its benefit times the grabber's multiplier
//     (161-174), still shards the Glitches within 300 px (165, 83-154: the
//     same rule as quoins.go) and never respawns (305).
//   - inc_games.js games_end_quoin_grab 1529-1600 / games_end_race 1330-1390:
//     the winner gets +50 iMG and +100 currants (achievements won), everyone
//     else nothing (achievements lost). Each client applies its own (the
//     save is the client's in this phase); the server says who won.
//
// Ours: an open challenge belongs to the street it was made on (leaving that
// street or going offline withdraws it, and the ticket comes back);
// accepting needs you on that street too; a member who drops has raceGrace
// to come back before it counts as leaving; before GO, anyone leaving calls
// the game off for the rest (in the source the others waited on, but nobody
// else can ever join a ticket instance); the countdown runs the full 4 s
// overlay (startCountdown starts the game at 3 s, 688, under a 4 s overlay;
// the remake's countdown shows READY? 3 2 1 GO over 4 s); a match ends by
// itself after raceMaxLife.
//
// Client to server:
//
//	{"t":"race_challenge","course":"grab_em_good"}  activated a ticket for that course
//	{"t":"race_accept","from":7}                    accept player 7's challenge
//	{"t":"race_ready","match":"…"}                  Click to Start
//	{"t":"race_grab","match":"…","i":3}             touched game quoin 3 (Grab 'Em Good)
//	{"t":"race_finish","match":"…"}                 touched the finish (a finish-line race)
//	{"t":"race_leave","match":"…"}                  Leave race / Leave the Game
//
// Server to client:
//
//	{"t":"race_challenged","from":{"id":7,"name":"Ada"},"course":"…","title":"…","text":"…","timeout":60}
//	{"t":"race_unchallenged","from":7,"text":"…"}   that challenge is gone (filled, expired, withdrawn)
//	{"t":"race_waiting","from":7,"course":"…","title":"…","text":"Waiting for other players...","timeout":60,"have":0,"need":1}
//	{"t":"race_failed","course":"…","text":"…","refund":true}   your challenge expired or was withdrawn
//	{"t":"race_refused","why":"alone|busy|full|gone|street|course|slow","text":"…"}
//	{"t":"race_starting","course":"…","title":"…","text":"Starting … in 5 seconds!","in":5000,"players":[…]}
//	{"t":"race_match","match":"…","street":"RACE_…","course":"…","title":"…","game":"quoin_grab","kind":"quoins",
//	    "slot":0,"spawn":[x,y],"players":[{"id","name","slot"}],"total":71,"quoins":[[x,y]…] | "finish":[x,y],"autoLeave":120}
//	{"t":"race_ready_state","match":"…","ready":[7],"need":1,"text":"Ada is ready to play. Need 1 more."}
//	{"t":"race_soon","match":"…","in":5000,"text":"YAY! THE GAME IS STARTING SOON."}
//	{"t":"race_countdown","match":"…","in":4000}
//	{"t":"race_go","match":"…"}
//	{"t":"race_grabbed","match":"…","i":3,"id":7,"name":"Ada","x":0,"y":0,"base":10,"crowd":2,
//	    "scores":[{"id":7,"name":"Ada","score":1}],"remaining":70,"shards":[{"id":8,"x":0,"y":0,"pct":0.31}]}
//	{"t":"race_grab_refused","match":"…","i":3,"why":"gone|far|street|unknown|slow|state"}
//	{"t":"race_end","match":"…","winner":{"id":7,"name":"Ada"}|null,"why":"won|left|idle|timeout|short",
//	    "left":{"id":8,"name":"Bo"},"scores":[…],"remaining":0,"total":71}
//
// A shard's amount is max(round(base * the receiver's own multiplier * pct), 1)
// (quoin.js 107-118), worked out by the receiver as the rest of its rewards.

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed data/races.json
var racesJSON []byte

const (
	raceInviteTimeout = 60 * time.Second        // inc_games.js 2398 prompt timeout 60, 2409 events_add 60
	raceStartDelay    = 5 * time.Second         // inc_games.js 2559 games_invite_start after 5*1000
	raceAutoLeave     = 2 * time.Minute         // shared_instance_manager.js:210 games_auto_leave
	raceSoon          = 5 * time.Second         // shared_instance_manager.js:276 startWithMinimumPlayers in 5
	raceCountdown     = 4000 * time.Millisecond // shared_instance_manager.js:674 the 4000 ms 321_countdown
	raceGrace         = 15 * time.Second        // ours: a dropped racer's time to come back
	raceMaxLife       = 30 * time.Minute        // ours
	raceLinger        = 5 * time.Minute         // ours: a finished match is forgotten after this
	raceQuoinBenefit  = 10                      // js/races.js QUOIN_GRAB_QUOIN: mood, benefit 10

	// races-feature.js quoinTick: a racer touches a game quoin when it is
	// within 40 px across and between 118 px above and 18 px below the
	// feet. Moves arrive at most every 100 ms, so some travel on top.
	raceQuoinReachX = 40.0
	raceQuoinAbove  = 118.0
	raceQuoinBelow  = 18.0
	raceSlack       = 120.0
	// races-feature.js raceTick: the finish is within 52 px across, from
	// 150 px above to 8 px below it.
	raceFinishReachX = 52.0
	raceFinishAbove  = 150.0
	raceFinishBelow  = 8.0

	raceGrabsPerSec = 8
	raceAsksBurst   = 6
	raceAsksEvery   = time.Second
)

// raceGame is one config_prod.js shared_instances row (1279-1287).
type raceGame struct {
	min, max int
	name     string
}

var raceGames = map[string]raceGame{
	"quoin_grab": {min: 2, max: 2, name: "Grab 'Em Good"},
	"race":       {min: 2, max: 2, name: "Race"},
}

var (
	courseRe = regexp.MustCompile(`^[a-z_]{1,32}$`)
	matchRe  = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

type raceCourse struct {
	Title  string       `json:"title"`
	Game   string       `json:"game"`
	Kind   string       `json:"kind"`
	Spawns [][2]float64 `json:"spawns"`
	Quoins [][2]float64 `json:"quoins"`
	Finish *[2]float64  `json:"finish"`
}

func parseRaceData(b []byte) (map[string]*raceCourse, error) {
	var raw struct {
		Courses map[string]*raceCourse `json:"courses"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := map[string]*raceCourse{}
	for id, c := range raw.Courses {
		if c == nil || len(c.Spawns) < 2 {
			continue
		}
		if _, ok := raceGames[c.Game]; !ok {
			continue
		}
		if (c.Kind == "quoins" && len(c.Quoins) > 0) || (c.Kind == "finish" && c.Finish != nil) {
			out[id] = c
		}
	}
	return out, nil
}

type raceRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// raceInvite is one open challenge (inc_games.js this.games_invite).
type raceInvite struct {
	from      raceRef
	course    string
	street    string
	opponents []raceRef
	full      bool
	at        time.Time
	stop      func() bool
}

type raceMember struct {
	raceRef
	slot      int
	ready     bool
	gone      bool
	autoLeave func() bool
	grace     func() bool
}

type raceMatch struct {
	id, street, courseID string
	course               *raceCourse
	game                 raceGame
	members              []*raceMember
	status               string // waiting, soon, countdown, running, done
	taken                map[int]bool
	scores               map[int64]int
	timers               []func() bool
	life                 func() bool
}

type raceAsk struct {
	tokens float64
	at     time.Time
	window time.Time
	grabs  int
}

type races struct {
	h       *Hub
	courses map[string]*raceCourse
	invites map[int64]*raceInvite // by challenger
	joined  map[int64]int64       // opponent -> the challenger whose invite they accepted
	matches map[string]*raceMatch
	matchOf map[int64]*raceMatch
	asks    map[int64]*raceAsk
	newID   func() string
}

func init() { modules = append(modules, newRaces) }

func newRaces(h *Hub) Module {
	c, err := parseRaceData(racesJSON)
	if err != nil {
		panic("realtime: data/races.json: " + err.Error())
	}
	return newRacesWith(h, c)
}

func newRacesWith(h *Hub, courses map[string]*raceCourse) *races {
	return &races{
		h:       h,
		courses: courses,
		invites: map[int64]*raceInvite{},
		joined:  map[int64]int64{},
		matches: map[string]*raceMatch{},
		matchOf: map[int64]*raceMatch{},
		asks:    map[int64]*raceAsk{},
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return hex.EncodeToString(b)
		},
	}
}

func (m *races) Register(r *Registry) {
	r.Handle("race_challenge", m.challenge)
	r.Handle("race_accept", m.accept)
	r.Handle("race_ready", m.ready)
	r.Handle("race_grab", m.grab)
	r.Handle("race_finish", m.finish)
	r.Handle("race_leave", m.leave)
	r.OnJoin(m.onJoin)
	r.OnLeave(m.onLeave)
	r.OnDisconnect(m.onDisconnect)
}

// RaceStreet is the hub street a match's racers share: unguessable, so
// nobody outside the match stumbles in.
func raceStreet(id string) string { return "RACE_" + strings.ToUpper(id) }

func refOf(cl *Client) raceRef { return raceRef{ID: cl.ID(), Name: cl.Name()} }

func (m *races) allow(cl *Client) bool {
	a := m.asks[cl.ID()]
	now := m.h.Now()
	if a == nil {
		a = &raceAsk{tokens: raceAsksBurst, at: now}
		m.asks[cl.ID()] = a
	}
	a.tokens = math.Min(raceAsksBurst, a.tokens+now.Sub(a.at).Seconds()/raceAsksEvery.Seconds())
	a.at = now
	if a.tokens < 1 {
		return false
	}
	a.tokens--
	return true
}

func (m *races) refuse(cl *Client, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "race_refused", "why": why, "text": text})
}

func (m *races) busy(id int64) bool {
	if _, ok := m.invites[id]; ok {
		return true
	}
	if _, ok := m.joined[id]; ok {
		return true
	}
	return m.matchOf[id] != nil
}

func (m *races) title(courseID string) string {
	if c := m.courses[courseID]; c != nil {
		return c.Title
	}
	return "the race"
}

func (m *races) secondsLeft(inv *raceInvite) int {
	left := raceInviteTimeout - m.h.Now().Sub(inv.at)
	return int(math.Max(1, math.Ceil(left.Seconds())))
}

func (m *races) challengedMsg(inv *raceInvite) map[string]any {
	return map[string]any{
		"t": "race_challenged", "from": inv.from, "course": inv.course, "title": m.title(inv.course),
		"text":    inv.from.Name + " is looking for a challenger for " + m.title(inv.course) + ".",
		"timeout": m.secondsLeft(inv),
	}
}

// —— Challenges ——

func (m *races) challenge(cl *Client, frame json.RawMessage) {
	var msg struct {
		Course string `json:"course"`
	}
	if json.Unmarshal(frame, &msg) != nil || !courseRe.MatchString(msg.Course) || m.courses[msg.Course] == nil {
		m.refuse(cl, "course", "That race is not in this remake yet.")
		return
	}
	if !m.allow(cl) {
		m.refuse(cl, "slow", "Slow down a little.")
		return
	}
	if m.busy(cl.ID()) {
		m.refuse(cl, "busy", "You are already racing!")
		return
	}
	street := cl.Street()
	if street == "" || strings.HasPrefix(street, "RACE_") {
		m.refuse(cl, "street", "You cannot start a race from here.")
		return
	}
	// race_ticket.js:121: num_keys(getActivePlayers()) <= 1.
	if len(m.h.StreetLocked(street)) <= 1 {
		m.refuse(cl, "alone", "You need at least one other player here.")
		return
	}
	inv := &raceInvite{from: refOf(cl), course: msg.Course, street: street, at: m.h.Now()}
	m.invites[cl.ID()] = inv
	from := cl.ID()
	inv.stop = m.h.After(raceInviteTimeout, func() { m.expire(from, inv) })
	game := raceGames[m.courses[msg.Course].Game]
	m.h.SendLocked(cl, map[string]any{"t": "race_waiting", "from": from, "course": inv.course, "title": m.title(inv.course),
		"text": "Waiting for other players...", "timeout": m.secondsLeft(inv), "have": 0, "need": game.min - 1})
	m.h.BroadcastStreetLocked(street, m.challengedMsg(inv), cl.ID())
}

// expire is games_invite_timeout (inc_games.js 2421-2454).
func (m *races) expire(from int64, inv *raceInvite) {
	if m.invites[from] != inv || inv.full {
		return
	}
	m.withdraw(from, "Not enough players accepted your challenge. Try again later?")
}

// withdraw closes an open challenge and gives the challenger the ticket back.
func (m *races) withdraw(from int64, text string) {
	inv := m.invites[from]
	if inv == nil {
		return
	}
	delete(m.invites, from)
	if inv.stop != nil {
		inv.stop()
	}
	for _, o := range inv.opponents {
		delete(m.joined, o.ID)
		if oc := m.h.ClientLocked(o.ID); oc != nil {
			m.h.SendLocked(oc, map[string]any{"t": "race_failed", "course": inv.course, "text": inv.from.Name + " was looking for a challenger for " + m.title(inv.course) + ".", "refund": false})
		}
	}
	if cl := m.h.ClientLocked(from); cl != nil {
		m.h.SendLocked(cl, map[string]any{"t": "race_failed", "course": inv.course, "text": text, "refund": true})
	}
	m.h.BroadcastStreetLocked(inv.street, map[string]any{"t": "race_unchallenged", "from": from,
		"text": inv.from.Name + " was looking for a challenger for " + m.title(inv.course) + "."}, from)
}

func (m *races) accept(cl *Client, frame json.RawMessage) {
	var msg struct {
		From int64 `json:"from"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.From <= 0 || msg.From == cl.ID() {
		return
	}
	if !m.allow(cl) {
		m.refuse(cl, "slow", "Slow down a little.")
		return
	}
	inv := m.invites[msg.From]
	if inv == nil {
		m.refuse(cl, "gone", "That challenge is over.")
		return
	}
	title := m.title(inv.course)
	game := raceGames[m.courses[inv.course].Game]
	// inc_games.js 2462-2471, games_invite_is_full 2479-2494.
	if inv.full || len(inv.opponents) >= game.min-1 {
		m.refuse(cl, "full", "Sorry, you were not quite fast enough to join "+inv.from.Name+" for "+title+".")
		return
	}
	if m.busy(cl.ID()) {
		m.refuse(cl, "busy", "You are already racing!")
		return
	}
	if cl.Street() != inv.street {
		m.refuse(cl, "street", "You need to be on the same street as "+inv.from.Name+".")
		return
	}
	inv.opponents = append(inv.opponents, refOf(cl))
	m.joined[cl.ID()] = msg.From
	if len(inv.opponents) < game.min-1 {
		// games_add_opponent 2569-2579: the accepter waits too.
		need := game.min - 1 - len(inv.opponents)
		for _, id := range append([]int64{inv.from.ID}, refIDs(inv.opponents)...) {
			if oc := m.h.ClientLocked(id); oc != nil {
				m.h.SendLocked(oc, map[string]any{"t": "race_waiting", "from": inv.from.ID, "course": inv.course, "title": title,
					"text": "Waiting for other players...", "timeout": m.secondsLeft(inv), "have": len(inv.opponents), "need": need})
			}
		}
		return
	}
	// Full (2507-2559): the street's request closes and everyone starts in 5 s.
	inv.full = true
	if inv.stop != nil {
		inv.stop()
	}
	names := []string{inv.from.Name}
	for _, o := range inv.opponents {
		names = append(names, o.Name)
	}
	players := append([]raceRef{inv.from}, inv.opponents...)
	text := "Starting " + title + " between " + prettyList(names, " and ") /* common.js:765 pretty_list, party.go */ + " in 5 seconds!"
	m.h.BroadcastStreetLocked(inv.street, map[string]any{"t": "race_unchallenged", "from": inv.from.ID,
		"text": inv.from.Name + " was looking for a challenger for " + title + ". " + cl.Name() + " accepted."}, 0)
	for _, p := range players {
		if oc := m.h.ClientLocked(p.ID); oc != nil {
			m.h.SendLocked(oc, map[string]any{"t": "race_starting", "course": inv.course, "title": title, "text": text,
				"in": raceStartDelay.Milliseconds(), "players": players})
		}
	}
	from := inv.from.ID
	inv.stop = m.h.After(raceStartDelay, func() { m.start(from, inv) })
}

func refIDs(refs []raceRef) []int64 {
	out := make([]int64, len(refs))
	for i, r := range refs {
		out[i] = r.ID
	}
	return out
}

// —— Matches ——

// start is games_invite_start (inc_games.js 2597-2617): one new instance for them all.
func (m *races) start(from int64, inv *raceInvite) {
	if m.invites[from] != inv {
		return
	}
	delete(m.invites, from)
	for _, o := range inv.opponents {
		delete(m.joined, o.ID)
	}
	c := m.courses[inv.course]
	game := raceGames[c.Game]
	var here []raceRef
	for _, p := range append([]raceRef{inv.from}, inv.opponents...) {
		if m.h.ClientLocked(p.ID) != nil && m.matchOf[p.ID] == nil && len(here) < game.max {
			here = append(here, p)
		}
	}
	if len(here) < game.min {
		for _, p := range here {
			if oc := m.h.ClientLocked(p.ID); oc != nil {
				m.h.SendLocked(oc, map[string]any{"t": "race_failed", "course": inv.course,
					"text": "Not enough players accepted your challenge. Try again later?", "refund": p.ID == from})
			}
		}
		return
	}
	id := m.newID()
	for m.matches[id] != nil || !matchRe.MatchString(id) {
		id = m.newID()
	}
	mt := &raceMatch{id: id, street: raceStreet(id), courseID: inv.course, course: c, game: game, status: "waiting",
		taken: map[int]bool{}, scores: map[int64]int{}}
	m.matches[id] = mt
	for i, p := range here {
		mem := &raceMember{raceRef: p, slot: i}
		mt.members = append(mt.members, mem)
		mt.scores[p.ID] = 0
		m.matchOf[p.ID] = mt
		pid := p.ID
		mem.autoLeave = m.h.After(raceAutoLeave, func() { m.idle(mt, pid) })
	}
	mt.life = m.h.After(raceMaxLife, func() {
		if mt.status != "done" {
			m.end(mt, nil, "timeout", nil)
		}
	})
	players := make([]map[string]any, len(mt.members))
	for i, mem := range mt.members {
		players[i] = map[string]any{"id": mem.ID, "name": mem.Name, "slot": mem.slot}
	}
	for _, mem := range mt.members {
		oc := m.h.ClientLocked(mem.ID)
		if oc == nil {
			continue
		}
		msg := map[string]any{"t": "race_match", "match": id, "street": mt.street, "course": mt.courseID, "title": c.Title,
			"game": c.Game, "kind": c.Kind, "slot": mem.slot, "spawn": c.Spawns[mem.slot%len(c.Spawns)], "players": players,
			"autoLeave": int(raceAutoLeave.Seconds())}
		if c.Kind == "quoins" {
			msg["total"] = len(c.Quoins)
			msg["quoins"] = c.Quoins
		} else {
			msg["finish"] = c.Finish
		}
		m.h.SendLocked(oc, msg)
	}
}

func (m *races) member(mt *raceMatch, id int64) *raceMember {
	for _, mem := range mt.members {
		if mem.ID == id {
			return mem
		}
	}
	return nil
}

// matchFor finds the caller's match from a frame's "match", or nil.
func (m *races) matchFor(cl *Client, id string) (*raceMatch, *raceMember) {
	mt := m.matchOf[cl.ID()]
	if mt == nil || mt.id != id {
		return nil, nil
	}
	mem := m.member(mt, cl.ID())
	if mem == nil || mem.gone {
		return nil, nil
	}
	return mt, mem
}

func (m *races) sendMatch(mt *raceMatch, msg map[string]any) {
	for _, mem := range mt.members {
		if mem.gone {
			continue
		}
		if oc := m.h.ClientLocked(mem.ID); oc != nil {
			m.h.SendLocked(oc, msg)
		}
	}
}

func (m *races) after(mt *raceMatch, d time.Duration, fn func()) {
	mt.timers = append(mt.timers, m.h.After(d, fn))
}

func (m *races) ready(cl *Client, frame json.RawMessage) {
	var msg struct {
		Match string `json:"match"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	mt, mem := m.matchFor(cl, msg.Match)
	if mt == nil || mt.status != "waiting" || mem.ready || cl.Street() != mt.street {
		return
	}
	mem.ready = true
	if mem.autoLeave != nil {
		mem.autoLeave()
		mem.autoLeave = nil
	}
	ready, unready := []int64{}, 0
	for _, o := range mt.members {
		if o.gone {
			continue
		}
		if o.ready {
			ready = append(ready, o.ID)
		} else {
			unready++
		}
	}
	need := mt.game.min - len(ready)
	// instance.js can_start_game 579-602 and dismissOverlaysAndStart 273-341.
	if need <= 0 && unready == 0 {
		m.sendMatch(mt, map[string]any{"t": "race_ready_state", "match": mt.id, "ready": ready, "need": 0,
			"text": cl.Name() + " is ready to play. LET'S GO!"})
		mt.status = "soon"
		m.sendMatch(mt, map[string]any{"t": "race_soon", "match": mt.id, "in": raceSoon.Milliseconds(), "text": "YAY! THE GAME IS STARTING SOON."})
		m.after(mt, raceSoon, func() { m.countdown(mt) })
		return
	}
	text := cl.Name() + " is ready to play. Need " + itoa(need) + " more."
	if need <= 0 {
		text = cl.Name() + " is ready to play. Just waiting on a few more slackers to HIT THE START BUTTON."
	}
	m.sendMatch(mt, map[string]any{"t": "race_ready_state", "match": mt.id, "ready": ready, "need": need, "text": text})
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// countdown is startWithMinimumPlayers + startCountdown (shared_instance_manager.js 345-358, 646-689).
func (m *races) countdown(mt *raceMatch) {
	if mt.status != "soon" {
		return
	}
	live := 0
	for _, o := range mt.members {
		if !o.gone && o.ready {
			live++
		}
	}
	if live < mt.game.min {
		return
	}
	mt.status = "countdown"
	m.sendMatch(mt, map[string]any{"t": "race_countdown", "match": mt.id, "in": raceCountdown.Milliseconds()})
	m.after(mt, raceCountdown, func() {
		if mt.status != "countdown" {
			return
		}
		mt.status = "running"
		m.sendMatch(mt, map[string]any{"t": "race_go", "match": mt.id})
	})
}

func (m *races) allowGrab(cl *Client) bool {
	a := m.asks[cl.ID()]
	now := m.h.Now()
	if a == nil {
		a = &raceAsk{tokens: raceAsksBurst, at: now}
		m.asks[cl.ID()] = a
	}
	if now.Sub(a.window) >= time.Second {
		a.window, a.grabs = now, 0
	}
	a.grabs++
	return a.grabs <= raceGrabsPerSec
}

func (m *races) scoreRows(mt *raceMatch) []map[string]any {
	out := make([]map[string]any, 0, len(mt.members))
	for _, mem := range mt.members {
		out = append(out, map[string]any{"id": mem.ID, "name": mem.Name, "score": mt.scores[mem.ID]})
	}
	return out
}

func (m *races) remaining(mt *raceMatch) int {
	n := len(mt.course.Quoins)
	for _, s := range mt.scores {
		n -= s
	}
	return n
}

func (m *races) grab(cl *Client, frame json.RawMessage) {
	var msg struct {
		Match string `json:"match"`
		I     *int   `json:"i"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.I == nil {
		return
	}
	i := *msg.I
	refuse := func(why string) {
		m.h.SendLocked(cl, map[string]any{"t": "race_grab_refused", "match": msg.Match, "i": i, "why": why})
	}
	mt, _ := m.matchFor(cl, msg.Match)
	if mt == nil || mt.course.Kind != "quoins" || mt.status != "running" {
		refuse("state")
		return
	}
	if !m.allowGrab(cl) {
		refuse("slow")
		return
	}
	if cl.Street() != mt.street {
		refuse("street")
		return
	}
	if i < 0 || i >= len(mt.course.Quoins) {
		refuse("unknown")
		return
	}
	// quoin.js 157: if (!this.spawned) return; and 305: game quoins never come back.
	if mt.taken[i] {
		refuse("gone")
		return
	}
	q := mt.course.Quoins[i]
	px, py := cl.Pos()
	if math.Abs(px-q[0]) > raceQuoinReachX+raceSlack || q[1] < py-raceQuoinAbove-raceSlack || q[1] > py+raceQuoinBelow+raceSlack {
		refuse("far")
		return
	}
	mt.taken[i] = true
	mt.scores[cl.ID()]++
	shards, crowd := m.shards(mt, cl)
	remaining := m.remaining(mt)
	m.sendMatch(mt, map[string]any{"t": "race_grabbed", "match": mt.id, "i": i, "id": cl.ID(), "name": cl.Name(),
		"x": q[0], "y": q[1], "base": raceQuoinBenefit, "crowd": crowd, "scores": m.scoreRows(mt), "remaining": remaining, "shards": shards})
	// global_game.js quoin_grab_get_quoin 453-468.
	mine := mt.scores[cl.ID()]
	lead := 0
	for id, s := range mt.scores {
		if id != cl.ID() && mine-s > lead {
			lead = mine - s
		}
	}
	if lead > remaining || remaining == 0 {
		m.end(mt, &raceRef{ID: cl.ID(), Name: cl.Name()}, "won", nil)
	}
}

// shards: distributeQuoinShards (quoin.js 83-154) over the other racers,
// nearest first. Racers cannot follow anyone in a game; AFK is not tracked
// here (quoins.go has it for streets).
func (m *races) shards(mt *raceMatch, cl *Client) ([]map[string]any, int) {
	gx, gy := cl.Pos()
	type near struct {
		o    *Client
		dist float64
	}
	var in []near
	for _, mem := range mt.members {
		if mem.gone || mem.ID == cl.ID() {
			continue
		}
		o := m.h.ClientLocked(mem.ID)
		if o == nil || o.Street() != mt.street {
			continue
		}
		ox, oy := o.Pos()
		if d := math.Hypot(ox-gx, oy-gy); d <= shardRadius {
			in = append(in, near{o, d})
		}
	}
	sort.Slice(in, func(a, b int) bool {
		if in[a].dist != in[b].dist {
			return in[a].dist < in[b].dist
		}
		return in[a].o.ID() < in[b].o.ID()
	})
	out := []map[string]any{}
	for _, n := range in {
		ox, oy := n.o.Pos()
		out = append(out, map[string]any{"id": n.o.ID(), "x": ox, "y": oy, "pct": math.Round(shardPct(n.dist)*1000) / 1000})
	}
	return out, len(in) + 1
}

func (m *races) finish(cl *Client, frame json.RawMessage) {
	var msg struct {
		Match string `json:"match"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	mt, _ := m.matchFor(cl, msg.Match)
	if mt == nil || mt.course.Kind != "finish" || mt.status != "running" || cl.Street() != mt.street {
		return
	}
	f := *mt.course.Finish
	px, py := cl.Pos()
	if math.Abs(px-f[0]) > raceFinishReachX+raceSlack || py < f[1]-raceFinishAbove-raceSlack || py > f[1]+raceFinishBelow+raceSlack {
		m.h.SendLocked(cl, map[string]any{"t": "race_grab_refused", "match": mt.id, "i": -1, "why": "far"})
		return
	}
	// global_game.js race_set_winner 381-386: the first across the line.
	m.end(mt, &raceRef{ID: cl.ID(), Name: cl.Name()}, "won", nil)
}

// end settles a match for everyone (global_game.js cancel 735-763 after a
// winner or a leaver; inc_games.js games_end_* pay the winner).
func (m *races) end(mt *raceMatch, winner *raceRef, why string, left *raceRef) {
	if mt.status == "done" {
		return
	}
	mt.status = "done"
	for _, stop := range mt.timers {
		stop()
	}
	mt.timers = nil
	if mt.life != nil {
		mt.life()
	}
	for _, mem := range mt.members {
		if mem.autoLeave != nil {
			mem.autoLeave()
			mem.autoLeave = nil
		}
	}
	msg := map[string]any{"t": "race_end", "match": mt.id, "winner": nil, "why": why, "scores": m.scoreRows(mt)}
	if winner != nil {
		msg["winner"] = *winner
	}
	if left != nil {
		msg["left"] = *left
	}
	if mt.course.Kind == "quoins" {
		msg["remaining"] = m.remaining(mt)
		msg["total"] = len(mt.course.Quoins)
	}
	m.sendMatch(mt, msg)
	if left != nil {
		if oc := m.h.ClientLocked(left.ID); oc != nil {
			if mem := m.member(mt, left.ID); mem != nil && mem.gone {
				m.h.SendLocked(oc, msg)
			}
		}
	}
	mt.timers = append(mt.timers, m.h.After(raceLinger, func() { m.forget(mt) }))
}

func (m *races) forget(mt *raceMatch) {
	if m.matches[mt.id] != mt {
		return
	}
	for _, stop := range mt.timers {
		stop()
	}
	for _, mem := range mt.members {
		if mem.grace != nil {
			mem.grace()
		}
		if m.matchOf[mem.ID] == mt {
			delete(m.matchOf, mem.ID)
		}
	}
	delete(m.matches, mt.id)
}

// drop takes a racer out: before the end it calls the game off for the rest
// (global_game.js playerLeft 814-815: race, quoin_grab, cancel()).
func (m *races) drop(mt *raceMatch, id int64, why string) {
	mem := m.member(mt, id)
	if mem == nil || mem.gone {
		return
	}
	mem.gone = true
	if mem.grace != nil {
		mem.grace()
		mem.grace = nil
	}
	if mem.autoLeave != nil {
		mem.autoLeave()
		mem.autoLeave = nil
	}
	if m.matchOf[id] == mt {
		delete(m.matchOf, id)
	}
	if mt.status != "done" {
		m.end(mt, nil, why, &mem.raceRef)
	}
	for _, o := range mt.members {
		if !o.gone {
			return
		}
	}
	m.forget(mt)
}

// idle is games_auto_leave (inc_games.js 2301-2306).
func (m *races) idle(mt *raceMatch, id int64) {
	mem := m.member(mt, id)
	if mem == nil || mem.gone || mem.ready || mt.status != "waiting" {
		return
	}
	if oc := m.h.ClientLocked(id); oc != nil {
		m.h.SendLocked(oc, map[string]any{"t": "race_removed", "match": mt.id, "text": "You were removed from the game due to inactivity."})
	}
	m.drop(mt, id, "idle")
}

func (m *races) leave(cl *Client, frame json.RawMessage) {
	var msg struct {
		Match string `json:"match"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	mt, _ := m.matchFor(cl, msg.Match)
	if mt == nil {
		return
	}
	m.drop(mt, cl.ID(), "left")
}

// —— Hooks ——

func (m *races) onJoin(cl *Client) {
	id := cl.ID()
	if mt := m.matchOf[id]; mt != nil {
		mem := m.member(mt, id)
		if cl.Street() == mt.street {
			if mem != nil && mem.grace != nil {
				mem.grace()
				mem.grace = nil
			}
		} else {
			// Off to another street: out of the game (instance.js player_exited_location 343-366).
			m.drop(mt, id, "left")
		}
	}
	// Open challenges on this street reach the newcomer too.
	for _, inv := range m.invites {
		if inv.street == cl.Street() && !inv.full && inv.from.ID != id {
			m.h.SendLocked(cl, m.challengedMsg(inv))
		}
	}
}

func (m *races) onLeave(cl *Client, street string) {
	id := cl.ID()
	if inv := m.invites[id]; inv != nil && !inv.full && inv.street == street {
		m.withdraw(id, "You left the street, so your challenge was called off.")
	}
	if from, ok := m.joined[id]; ok {
		if inv := m.invites[from]; inv != nil && !inv.full && inv.street == street {
			// games_remove_opponent 2582-2595.
			delete(m.joined, id)
			for i, o := range inv.opponents {
				if o.ID == id {
					inv.opponents = append(inv.opponents[:i], inv.opponents[i+1:]...)
					break
				}
			}
		}
	}
	if mt := m.matchOf[id]; mt != nil && street == mt.street {
		m.startGrace(mt, id)
	}
}

func (m *races) startGrace(mt *raceMatch, id int64) {
	mem := m.member(mt, id)
	if mem == nil || mem.gone || mem.grace != nil {
		return
	}
	if mt.status == "done" {
		// After the end, leaving the instance is just leaving.
		m.drop(mt, id, "left")
		return
	}
	mem.grace = m.h.After(raceGrace, func() {
		mem.grace = nil
		m.drop(mt, id, "left")
	})
}

func (m *races) onDisconnect(cl *Client) {
	id := cl.ID()
	if _, ok := m.invites[id]; ok {
		m.withdraw(id, "")
	}
	delete(m.asks, id)
	if mt := m.matchOf[id]; mt != nil {
		m.startGrace(mt, id)
	}
}
