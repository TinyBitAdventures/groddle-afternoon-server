package main

// Game of Crowns between real players (G334): a Game of Crowns Ticket
// challenges the street, the players who accept play in one private copy of
// the arena, and the server referees the crown: who holds it, the tags, the
// locks, the clock below the play line and the winner, all from the
// positions players report and its own clock, never from a client saying what
// happened.
//
// The rules are GameServerJS's it_game:
//   - config_prod.js:1281 shared_instances.it_game: min_players 4,
//     max_players 5, can_retry, min_retry_players 3; and @glitchlog
//     2012-08-10 "GoC tickets are now 4 player". A ticket fills with
//     min_players-1 opponents (inc_games.js games_invite_is_full 2479-2494),
//     so a ticket game is four players.
//   - items/race_ticket.js verbs.activate 116-158 and players/inc_games.js
//     games_invite_create 2387-2419, games_invite_timeout 2421-2454,
//     games_accept 2456-2477, games_add_opponent 2496-2580 and
//     games_invite_start 2597-2617: the street hears "<name> is looking for a
//     challenger for Game of Crowns.", the challenger waits 60 s ("Waiting
//     for other players..."), a late accepter gets "Sorry, you were not quite
//     fast enough to join <name> for Game of Crowns.", a full challenge says
//     "Starting Game of Crowns between <a>, <b>, <c> and <d> in 5 seconds!"
//     and 5 s later puts them all in one new instance; nobody came: "Not
//     enough players accepted your challenge. Try again later?" and the
//     ticket back.
//   - groups/shared_instance_manager.js dismissOverlaysAndStart 231-342 and
//     groups/instance.js can_start_game 579-602: for it_game the start needs
//     min_retry_players (3) ready and nobody unready (271, 588); then "YAY!
//     THE GAME IS STARTING SOON." for 5 s and startCountdown's 4 s;
//     games_auto_leave after 2 min unclicked (210; inc_games.js 2301-2306).
//   - groups/global_game.js initITGameInInstance 140-170 and chooseIt
//     172-199: one player at random holds the crown; setIt 209-226; whosIt
//     203-206 (no tag backs); it_game_tick 229-251 every second, the end at
//     60. playerLeft 803-813: the holder leaving passes the crown to someone
//     chosen at random, everyone else plays on.
//   - players/inc_games.js games_tag_player 362-380 (a touch steals an
//     unlocked crown), games_is_it 391-449 (the new holder is locked 1 s,
//     games_it_game_lock(1)), games_it_game_is_started 843-859 and
//     games_it_game_gate 549-611 (only time below the play line, y >= -1390,
//     counts; going back up pauses it), games_end_it_game 613-737: the holder
//     reaching 60 s total wins +200 iMG +50 currants (671-674),
//     achievements it_game:won, it_game:lost and it_game:seconds_with_crown.
//   - items/crown_game_lock.js onPlayerCollision: a Crown Game Lock is taken
//     by whoever touches it, respawns after 12 s, and locks the crown for 4 s
//     when the toucher holds it (games_it_game_lock(), duration 4; a new
//     pickup restarts the lock, 772-783).
//
// The arena (data/crowns.json, the game's tools/build-realtime-crowns.mjs):
// the play line in the arena's screen coords, the four starts, the four
// locks, the reaches of a tag and a lock touch and the clocks above.
//
// Who decides what. Positions are what players report in "move" (the hub
// keeps the latest); every crownsTickEvery the server reads them: a player
// within the tag reach of an unlocked holder takes the crown; a player
// within reach of a ready lock takes it; the holder's clock runs while they
// stand below the play line. The client sends no tags, no locks and no
// times. The winner's currants go into the site's ledger through a signed
// call (POST groddle/v1/ledger/crowns, idempotent per id, LedgerCrowns.php);
// the iMG and the badges are the client's to apply from crown_reward and
// crown_end, as every other reward in this phase.
//
// Ours: an open challenge belongs to its street; a member who drops has
// crownsGrace to come back (a holder who drops passes the crown at once, as
// playerLeft does on logout); a game that falls under three before GO is
// called off (nobody else can join a ticket instance); Play Again counts as
// clicking start for the next round (the source showed the start button
// again), and members who have not chosen when the next countdown begins
// are taken out of the game; a match ends by itself after crownsMaxLife; a
// reward not acked is kept in memory only (a restart loses its iMG, while
// the site's ledger still pays its currants on the winner's next load,
// Ledger::catchUp). Each tag and lock reach has crownsSlack more across,
// because positions arrive every 100 ms.
//
// Client to server:
//
//	{"t":"crown_challenge"}                  activated a Game of Crowns Ticket
//	{"t":"crown_accept","from":7}            accept player 7's challenge
//	{"t":"crown_ready","match":"…"}          Click to Start
//	{"t":"crown_again","match":"…"}          Play Again (after an end)
//	{"t":"crown_leave","match":"…"}          Leave the Game
//	{"t":"crown_ack","id":"crowns-…"}        applied a crown_reward and saved
//
// Server to client:
//
//	{"t":"crown_challenged","from":{"id","name"},"title":"Game of Crowns","text":"…","timeout":60}
//	{"t":"crown_unchallenged","from":7,"text":"…"}
//	{"t":"crown_waiting","from":7,"text":"Waiting for other players...","timeout":60,"have":1,"need":3}
//	{"t":"crown_failed","text":"…","refund":true|false}
//	{"t":"crown_refused","why":"alone|busy|full|gone|street|slow","text":"…"}
//	{"t":"crown_starting","text":"Starting … in 5 seconds!","in":5000,"players":[{"id","name"}]}
//	{"t":"crown_match","match":"…","street":"CROWNS_…","slot":0,"spawn":[x,y],"players":[{"id","name","slot"}],
//	    "playLine":610,"locks":[[x,y]…],"winMs":60000,"autoLeave":120}
//	{"t":"crown_ready_state","match":"…","ready":[7],"need":2,"text":"Ada is ready to play. Need 2 more."}
//	{"t":"crown_soon","match":"…","in":5000,"text":"YAY! THE GAME IS STARTING SOON."}
//	{"t":"crown_round","match":"…","round":2,"players":[…]}         Play Again: a new round, everyone back at the starts
//	{"t":"crown_countdown","match":"…","round":1,"in":4000}
//	{"t":"crown_go","match":"…", …State}
//	{"t":"crown_state","match":"…","round":1,"it":7,"locked":800,"clock":true,"scores":[{"id","name","ms"}],"locks":[true,…]}
//	{"t":"crown_tag","match":"…","from":7,"to":8,"fromName":"Ada","toName":"Bo", …State}
//	{"t":"crown_lock","match":"…","i":2,"id":8,"name":"Bo","locked":true,"x":0,"y":0, …State}
//	{"t":"crown_end","match":"…","round":1,"winner":{"id","name"}|null,"why":"won|short|empty|timeout","scores":[…],"seconds":12}
//	{"t":"crown_left","match":"…","round":1,"seconds":12}              you left a running game: your crown time
//	{"t":"crown_removed","match":"…","text":"You were removed from the game due to inactivity."}
//	{"t":"crown_reward","id":"crowns-<match>-<round>-<player>","match":"…","round":1,"xp":200,"currants":50}
//
// A crown_reward is sent again on every connect until acked.

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	mrand "math/rand/v2"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

//go:embed data/crowns.json
var crownsJSON []byte

const (
	ledgerRouteCrowns = "/groddle/v1/ledger/crowns"

	crownsInviteTimeout = 60 * time.Second // inc_games.js 2398 / 2409
	crownsStartDelay    = 5 * time.Second  // inc_games.js 2559
	crownsAutoLeave     = 2 * time.Minute  // shared_instance_manager.js:210
	crownsSoon          = 5 * time.Second  // shared_instance_manager.js:276
	crownsGrace         = 15 * time.Second // ours
	crownsMaxLife       = 30 * time.Minute // ours
	crownsLinger        = 5 * time.Minute  // ours: a finished, empty match is forgotten
	crownsTickEvery     = 100 * time.Millisecond
	crownsStateEvery    = time.Second // global_game.js it_game_tick every 1000 ms
	crownsSlack         = 12.0        // px more across for a tag or a lock: moves arrive every 100 ms
	crownsMaxOwed       = 20

	crownsAsksBurst = 6
	crownsAsksEvery = time.Second
)

var (
	crownsMatchRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
	crownsIDRe    = regexp.MustCompile(`^crowns-[0-9a-f]{16}-[0-9]{1,3}-[0-9]{1,12}$`)
	// Streets a challenge can't start from: instances and homes (race_ticket.js activate is a street verb).
	crownsPrivatePrefix = []string{"CROWNS_", "RACE_", "PARTY_", "HOME_", "HOUSE_"}
)

type crownsArena struct {
	Street    string       `json:"street"`
	Title     string       `json:"title"`
	PlayLineY float64      `json:"playLineY"`
	Starts    [][2]float64 `json:"starts"`
	Locks     [][2]float64 `json:"locks"`
	Tag       struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"tag"`
	LockReach struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"lockReach"`
	WinMs         int64 `json:"winMs"`
	TagLockMs     int64 `json:"tagLockMs"`
	PickupLockMs  int64 `json:"pickupLockMs"`
	LockRespawnMs int64 `json:"lockRespawnMs"`
	CountdownMs   int64 `json:"countdownMs"`
	Players       struct {
		Min   int `json:"min"`
		Max   int `json:"max"`
		Retry int `json:"retry"`
	} `json:"players"`
	Reward struct {
		XP       int64 `json:"xp"`
		Currants int64 `json:"currants"`
	} `json:"reward"`
}

func parseCrownsArena(b []byte) (*crownsArena, error) {
	var a crownsArena
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	if a.Title == "" || len(a.Starts) == 0 || a.WinMs <= 0 || a.Players.Min < 2 || a.Players.Retry < 1 || a.Players.Max < a.Players.Min {
		return nil, fmt.Errorf("not an arena")
	}
	return &a, nil
}

type crownRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type crownInvite struct {
	from      crownRef
	street    string
	opponents []crownRef
	full      bool
	at        time.Time
	stop      func() bool
}

type crownMember struct {
	crownRef
	slot      int
	ready     bool // clicked start (or Play Again) for the coming round
	playing   bool // in the round under way
	gone      bool
	ms        int64 // crown time this round, below the play line
	below     bool
	lockUntil time.Time
	autoLeave func() bool
	grace     func() bool
}

type crownMatch struct {
	id, street string
	members    []*crownMember
	status     string // waiting, soon, countdown, running, done
	round      int
	it         int64
	itAt       time.Time
	clock      bool // the holder's clock is running
	locks      []time.Time
	lastTick   time.Time
	lastState  time.Time
	timers     []func() bool
	ticker     func() bool
	life       func() bool
}

// crownsLedger is the site's ledger for a winner's currants: put them in, once per id.
type crownsLedger interface {
	crowns(id string, user int64, got tradeGoods) ledgerAnswer
}

func (l *httpLedger) crowns(id string, user int64, got tradeGoods) ledgerAnswer {
	return l.call(ledgerRouteCrowns, map[string]any{"id": id, "user": user, "got": goodsJSON(got)})
}

type crownsAsk struct {
	tokens float64
	at     time.Time
}

type crownOwed struct {
	id  string
	msg []byte
}

type crowns struct {
	h       *Hub
	a       *crownsArena
	invites map[int64]*crownInvite
	joined  map[int64]int64
	matches map[string]*crownMatch
	matchOf map[int64]*crownMatch
	asks    map[int64]*crownsAsk
	owed    map[int64][]crownOwed
	ledger  crownsLedger // nil: no ledger (see trusted)
	trusted bool         // -trade-ledger off: the winner's currants are the client's (development only)
	spawn   func(func())
	newID   func() string
	pick    func(n int) int
}

func init() { modules = append(modules, newCrowns) }

func newCrowns(h *Hub) Module {
	a, err := parseCrownsArena(crownsJSON)
	if err != nil {
		panic("realtime: data/crowns.json: " + err.Error())
	}
	m := newCrownsWith(h, a)
	if !testing.Testing() {
		site, missing := newLedgerFromFlags()
		if hl, ok := site.(*httpLedger); ok && hl != nil {
			m.ledger = hl
		}
		m.trusted = site == nil && !missing
	}
	return m
}

func newCrownsWith(h *Hub, a *crownsArena) *crowns {
	return &crowns{
		h: h, a: a,
		invites: map[int64]*crownInvite{}, joined: map[int64]int64{},
		matches: map[string]*crownMatch{}, matchOf: map[int64]*crownMatch{},
		asks: map[int64]*crownsAsk{}, owed: map[int64][]crownOwed{},
		spawn: func(fn func()) { go fn() },
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return hex.EncodeToString(b)
		},
		pick: func(n int) int { return mrand.IntN(n) },
	}
}

func (m *crowns) Register(r *Registry) {
	r.Handle("crown_challenge", m.challenge)
	r.Handle("crown_accept", m.accept)
	r.Handle("crown_ready", m.ready)
	r.Handle("crown_again", m.again)
	r.Handle("crown_leave", m.leave)
	r.Handle("crown_ack", m.ack)
	r.OnConnect(m.onConnect)
	r.OnJoin(m.onJoin)
	r.OnLeave(m.onLeave)
	r.OnDisconnect(m.onDisconnect)
	r.GateJoin(m.gate)
}

// crownsStreet is the hub street a match's players share: unguessable.
func crownsStreet(id string) string { return "CROWNS_" + strings.ToUpper(id) }

// gate keeps a match's arena to its members (an instance is entered only through its game's invite).
func (m *crowns) gate(cl *Client, street string) (bool, string, string) {
	if !strings.HasPrefix(street, "CROWNS_") {
		return true, "", ""
	}
	if mt := m.matchOf[cl.ID()]; mt != nil && mt.street == street {
		if mem := mt.member(cl.ID()); mem != nil && !mem.gone {
			return true, "", ""
		}
	}
	return false, "private_street", "That game is not yours."
}

func (m *crowns) allow(cl *Client) bool {
	a := m.asks[cl.ID()]
	now := m.h.Now()
	if a == nil {
		a = &crownsAsk{tokens: crownsAsksBurst, at: now}
		m.asks[cl.ID()] = a
	}
	a.tokens = math.Min(crownsAsksBurst, a.tokens+now.Sub(a.at).Seconds()/crownsAsksEvery.Seconds())
	a.at = now
	if a.tokens < 1 {
		return false
	}
	a.tokens--
	return true
}

func (m *crowns) refuse(cl *Client, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "crown_refused", "why": why, "text": text})
}

func (m *crowns) busy(id int64) bool {
	if _, ok := m.invites[id]; ok {
		return true
	}
	if _, ok := m.joined[id]; ok {
		return true
	}
	return m.matchOf[id] != nil
}

func (m *crowns) secondsLeft(inv *crownInvite) int {
	left := crownsInviteTimeout - m.h.Now().Sub(inv.at)
	return int(math.Max(1, math.Ceil(left.Seconds())))
}

func (m *crowns) need() int { return m.a.Players.Min - 1 }

func (m *crowns) challengedMsg(inv *crownInvite) map[string]any {
	return map[string]any{"t": "crown_challenged", "from": inv.from, "title": m.a.Title,
		"text": inv.from.Name + " is looking for a challenger for " + m.a.Title + ".", "timeout": m.secondsLeft(inv)}
}

func crownsPrivate(street string) bool {
	for _, p := range crownsPrivatePrefix {
		if strings.HasPrefix(street, p) {
			return true
		}
	}
	return false
}

// —— Challenges ——

func (m *crowns) challenge(cl *Client, _ json.RawMessage) {
	if !m.allow(cl) {
		m.refuse(cl, "slow", "Slow down a little.")
		return
	}
	if m.busy(cl.ID()) {
		m.refuse(cl, "busy", "You are already racing!")
		return
	}
	street := cl.Street()
	if street == "" || crownsPrivate(street) {
		m.refuse(cl, "street", "You cannot start a race from here.")
		return
	}
	// race_ticket.js:121: num_keys(getActivePlayers()) <= 1.
	if len(m.h.StreetLocked(street)) <= 1 {
		m.refuse(cl, "alone", "You need at least one other player here.")
		return
	}
	inv := &crownInvite{from: crownRef{cl.ID(), cl.Name()}, street: street, at: m.h.Now()}
	m.invites[cl.ID()] = inv
	from := cl.ID()
	inv.stop = m.h.After(crownsInviteTimeout, func() { m.expire(from, inv) })
	m.h.SendLocked(cl, map[string]any{"t": "crown_waiting", "from": from, "title": m.a.Title,
		"text": "Waiting for other players...", "timeout": m.secondsLeft(inv), "have": 0, "need": m.need()})
	m.h.BroadcastStreetLocked(street, m.challengedMsg(inv), from)
	log.Printf("crowns: %s challenged %s", cl.Name(), street)
}

// expire is games_invite_timeout (inc_games.js 2421-2454).
func (m *crowns) expire(from int64, inv *crownInvite) {
	if m.invites[from] != inv || inv.full {
		return
	}
	m.withdraw(from, "Not enough players accepted your challenge. Try again later?")
}

// withdraw closes an open challenge and gives the challenger the ticket back.
func (m *crowns) withdraw(from int64, text string) {
	inv := m.invites[from]
	if inv == nil {
		return
	}
	delete(m.invites, from)
	if inv.stop != nil {
		inv.stop()
	}
	was := inv.from.Name + " was looking for a challenger for " + m.a.Title + "."
	for _, o := range inv.opponents {
		delete(m.joined, o.ID)
		if oc := m.h.ClientLocked(o.ID); oc != nil {
			m.h.SendLocked(oc, map[string]any{"t": "crown_failed", "text": was, "refund": false})
		}
	}
	if cl := m.h.ClientLocked(from); cl != nil {
		m.h.SendLocked(cl, map[string]any{"t": "crown_failed", "text": text, "refund": true})
	}
	m.h.BroadcastStreetLocked(inv.street, map[string]any{"t": "crown_unchallenged", "from": from, "text": was}, from)
}

func (m *crowns) accept(cl *Client, frame json.RawMessage) {
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
	// inc_games.js 2462-2471.
	if inv.full || len(inv.opponents) >= m.need() {
		m.refuse(cl, "full", "Sorry, you were not quite fast enough to join "+inv.from.Name+" for "+m.a.Title+".")
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
	inv.opponents = append(inv.opponents, crownRef{cl.ID(), cl.Name()})
	m.joined[cl.ID()] = msg.From
	if len(inv.opponents) < m.need() {
		// games_add_opponent 2569-2579: the accepter waits too.
		need := m.need() - len(inv.opponents)
		for _, id := range append([]int64{inv.from.ID}, crownIDs(inv.opponents)...) {
			if oc := m.h.ClientLocked(id); oc != nil {
				m.h.SendLocked(oc, map[string]any{"t": "crown_waiting", "from": inv.from.ID, "title": m.a.Title,
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
	players := append([]crownRef{inv.from}, inv.opponents...)
	names := make([]string, len(players))
	for i, p := range players {
		names[i] = p.Name
	}
	text := "Starting " + m.a.Title + " between " + prettyList(names, " and ") + " in 5 seconds!"
	m.h.BroadcastStreetLocked(inv.street, map[string]any{"t": "crown_unchallenged", "from": inv.from.ID,
		"text": inv.from.Name + " was looking for a challenger for " + m.a.Title + ". " + cl.Name() + " accepted."}, 0)
	for _, p := range players {
		if oc := m.h.ClientLocked(p.ID); oc != nil {
			m.h.SendLocked(oc, map[string]any{"t": "crown_starting", "title": m.a.Title, "text": text,
				"in": crownsStartDelay.Milliseconds(), "players": players})
		}
	}
	from := inv.from.ID
	inv.stop = m.h.After(crownsStartDelay, func() { m.start(from, inv) })
}

func crownIDs(refs []crownRef) []int64 {
	out := make([]int64, len(refs))
	for i, r := range refs {
		out[i] = r.ID
	}
	return out
}

// —— Matches ——

func (mt *crownMatch) member(id int64) *crownMember {
	for _, mem := range mt.members {
		if mem.ID == id {
			return mem
		}
	}
	return nil
}

// start is games_invite_start (inc_games.js 2597-2617): one new arena for them all.
func (m *crowns) start(from int64, inv *crownInvite) {
	if m.invites[from] != inv {
		return
	}
	delete(m.invites, from)
	for _, o := range inv.opponents {
		delete(m.joined, o.ID)
	}
	var here []crownRef
	for _, p := range append([]crownRef{inv.from}, inv.opponents...) {
		if m.h.ClientLocked(p.ID) != nil && m.matchOf[p.ID] == nil && len(here) < m.a.Players.Max {
			here = append(here, p)
		}
	}
	// The arena needs min_retry_players to start at all (instance.js 588).
	if len(here) < m.a.Players.Retry {
		for _, p := range here {
			if oc := m.h.ClientLocked(p.ID); oc != nil {
				m.h.SendLocked(oc, map[string]any{"t": "crown_failed", "text": "Not enough players accepted your challenge. Try again later?", "refund": p.ID == from})
			}
		}
		return
	}
	id := m.newID()
	for m.matches[id] != nil || !crownsMatchRe.MatchString(id) {
		id = m.newID()
	}
	mt := &crownMatch{id: id, street: crownsStreet(id), status: "waiting", round: 1, locks: make([]time.Time, len(m.a.Locks))}
	m.matches[id] = mt
	for i, p := range here {
		mem := &crownMember{crownRef: p, slot: i}
		mt.members = append(mt.members, mem)
		m.matchOf[p.ID] = mt
		pid := p.ID
		mem.autoLeave = m.h.After(crownsAutoLeave, func() { m.idle(mt, pid) })
	}
	mt.life = m.h.After(crownsMaxLife, func() {
		if mt.status != "done" {
			m.end(mt, nil, "timeout")
		}
	})
	players := m.playersJSON(mt)
	for _, mem := range mt.members {
		if oc := m.h.ClientLocked(mem.ID); oc != nil {
			m.h.SendLocked(oc, map[string]any{"t": "crown_match", "match": id, "street": mt.street, "title": m.a.Title,
				"slot": mem.slot, "spawn": m.a.Starts[mem.slot%len(m.a.Starts)], "players": players,
				"playLine": m.a.PlayLineY, "locks": m.a.Locks, "winMs": m.a.WinMs, "autoLeave": int(crownsAutoLeave.Seconds())})
		}
	}
	log.Printf("crowns %s: %s", id, strings.Join(func() []string {
		var n []string
		for _, p := range here {
			n = append(n, p.Name)
		}
		return n
	}(), ", "))
}

func (m *crowns) playersJSON(mt *crownMatch) []map[string]any {
	out := []map[string]any{}
	for _, mem := range mt.members {
		if !mem.gone {
			out = append(out, map[string]any{"id": mem.ID, "name": mem.Name, "slot": mem.slot})
		}
	}
	return out
}

// matchFor finds the caller's match from a frame's "match", or nil.
func (m *crowns) matchFor(cl *Client, id string) (*crownMatch, *crownMember) {
	mt := m.matchOf[cl.ID()]
	if mt == nil || mt.id != id {
		return nil, nil
	}
	mem := mt.member(cl.ID())
	if mem == nil || mem.gone {
		return nil, nil
	}
	return mt, mem
}

func (m *crowns) sendMatch(mt *crownMatch, msg map[string]any) {
	b, _ := json.Marshal(msg)
	for _, mem := range mt.members {
		if mem.gone {
			continue
		}
		if oc := m.h.ClientLocked(mem.ID); oc != nil {
			m.h.pushLocked(oc, b)
		}
	}
}

func (m *crowns) after(mt *crownMatch, d time.Duration, fn func()) {
	mt.timers = append(mt.timers, m.h.After(d, fn))
}

// present: a member who is in the match and standing in its arena now.
func (m *crowns) present(mt *crownMatch, mem *crownMember) *Client {
	if mem == nil || mem.gone {
		return nil
	}
	oc := m.h.ClientLocked(mem.ID)
	if oc == nil || !oc.Online() || oc.Street() != mt.street {
		return nil
	}
	return oc
}

func (m *crowns) ready(cl *Client, frame json.RawMessage) {
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
	m.markReady(mt, mem, cl.Name())
}

// again is Play Again (games_leave_instance 'again', inc_games.js 2212-2233): ready for the next round.
func (m *crowns) again(cl *Client, frame json.RawMessage) {
	var msg struct {
		Match string `json:"match"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	mt, mem := m.matchFor(cl, msg.Match)
	if mt == nil || mem.ready || cl.Street() != mt.street {
		return
	}
	if played := mt.status == "done" || (mt.status == "waiting" && !mt.lastTick.IsZero()); !played && mt.status != "soon" {
		return
	}
	if mt.status == "done" {
		mt.status = "waiting"
		for _, o := range mt.members {
			o.playing = false
		}
	}
	if mt.status == "soon" {
		// Joining a round that is about to start.
		mem.ready = true
		if mem.autoLeave != nil {
			mem.autoLeave()
			mem.autoLeave = nil
		}
		m.sendMatch(mt, map[string]any{"t": "crown_ready_state", "match": mt.id, "ready": m.readyIDs(mt), "need": 0,
			"text": cl.Name() + " is ready to play. LET'S GO!"})
		return
	}
	m.markReady(mt, mem, cl.Name())
}

func (m *crowns) readyIDs(mt *crownMatch) []int64 {
	ids := []int64{}
	for _, o := range mt.members {
		if !o.gone && o.ready {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

// markReady is dismissOverlaysAndStart 231-342 with can_start_game 579-602.
func (m *crowns) markReady(mt *crownMatch, mem *crownMember, name string) {
	mem.ready = true
	if mem.autoLeave != nil {
		mem.autoLeave()
		mem.autoLeave = nil
	}
	ready := m.readyIDs(mt)
	// Before the first round everyone in the arena must click; after one, Play Again is a choice (ours).
	unready := 0
	if mt.round == 1 && mt.lastTick.IsZero() {
		for _, o := range mt.members {
			if !o.gone && !o.ready {
				unready++
			}
		}
	}
	need := m.a.Players.Retry - len(ready)
	if need <= 0 && unready == 0 {
		m.sendMatch(mt, map[string]any{"t": "crown_ready_state", "match": mt.id, "ready": ready, "need": 0,
			"text": name + " is ready to play. LET'S GO!"})
		mt.status = "soon"
		m.sendMatch(mt, map[string]any{"t": "crown_soon", "match": mt.id, "in": crownsSoon.Milliseconds(), "text": "YAY! THE GAME IS STARTING SOON."})
		m.after(mt, crownsSoon, func() { m.countdown(mt) })
		return
	}
	text := name + " is ready to play. Need " + itoa(need) + " more."
	if need <= 0 {
		text = name + " is ready to play. Just waiting on a few more slackers to HIT THE START BUTTON."
	}
	m.sendMatch(mt, map[string]any{"t": "crown_ready_state", "match": mt.id, "ready": ready, "need": max(need, 0), "text": text})
}

// countdown is startWithMinimumPlayers + startCountdown (shared_instance_manager.js 345-358, 646-689).
func (m *crowns) countdown(mt *crownMatch) {
	if mt.status != "soon" {
		return
	}
	if len(m.readyIDs(mt)) < m.a.Players.Retry {
		mt.status = "waiting"
		m.sendMatch(mt, map[string]any{"t": "crown_ready_state", "match": mt.id, "ready": m.readyIDs(mt), "need": m.a.Players.Retry - len(m.readyIDs(mt)),
			"text": "You must wait for more players before this game can begin."})
		return
	}
	// The round's players are the ready ones; anyone still choosing is out of the game (ours).
	for _, o := range mt.members {
		if o.gone || o.ready {
			continue
		}
		if oc := m.h.ClientLocked(o.ID); oc != nil {
			m.h.SendLocked(oc, map[string]any{"t": "crown_removed", "match": mt.id, "text": "You were removed from the game due to inactivity."})
		}
		m.drop(mt, o.ID)
	}
	if !mt.lastTick.IsZero() {
		mt.round++
	}
	for _, o := range mt.members {
		o.playing = !o.gone && o.ready
		o.ready = false
		o.ms, o.below, o.lockUntil = 0, false, time.Time{}
	}
	for i := range mt.locks {
		mt.locks[i] = time.Time{}
	}
	mt.it, mt.clock = 0, false
	if mt.round > 1 {
		m.sendMatch(mt, map[string]any{"t": "crown_round", "match": mt.id, "round": mt.round, "players": m.playersJSON(mt)})
	}
	mt.status = "countdown"
	m.sendMatch(mt, map[string]any{"t": "crown_countdown", "match": mt.id, "round": mt.round, "in": m.a.CountdownMs})
	m.after(mt, time.Duration(m.a.CountdownMs)*time.Millisecond, func() { m.goNow(mt) })
}

// goNow starts the round: initITGameInInstance and chooseIt (global_game.js 140-199).
func (m *crowns) goNow(mt *crownMatch) {
	if mt.status != "countdown" {
		return
	}
	mt.status = "running"
	now := m.h.Now()
	mt.lastTick, mt.lastState = now, now
	m.chooseIt(mt, 0)
	m.sense(mt, now)
	msg := m.stateJSON(mt, "crown_go")
	m.sendMatch(mt, msg)
	if mt.it != 0 {
		log.Printf("crowns %s round %d: GO, %s has the crown", mt.id, mt.round, mt.member(mt.it).Name)
	}
	m.arm(mt)
}

func (m *crowns) arm(mt *crownMatch) {
	if mt.ticker != nil {
		mt.ticker()
	}
	mt.ticker = m.h.After(crownsTickEvery, func() {
		mt.ticker = nil
		if mt.status != "running" {
			return
		}
		m.tick(mt)
		if mt.status == "running" {
			m.arm(mt)
		}
	})
}

// chooseIt gives the crown to a playing member in the arena, chosen at random (not except).
func (m *crowns) chooseIt(mt *crownMatch, except int64) {
	var pool []*crownMember
	for _, o := range mt.members {
		if o.playing && o.ID != except && m.present(mt, o) != nil {
			pool = append(pool, o)
		}
	}
	mt.clock = false
	if len(pool) == 0 {
		mt.it = 0
		return
	}
	m.setIt(mt, pool[m.pick(len(pool))])
}

// setIt is games_is_it + setIt: the new holder is locked for a second (games_it_game_lock(1)).
func (m *crowns) setIt(mt *crownMatch, mem *crownMember) {
	now := m.h.Now()
	mt.it, mt.itAt = mem.ID, now
	mem.lockUntil = now.Add(time.Duration(m.a.TagLockMs) * time.Millisecond)
	mt.clock = mem.below
}

// sense reads where everyone stands: below the play line or not (games_it_game_is_started 843-859).
func (m *crowns) sense(mt *crownMatch, now time.Time) {
	for _, o := range mt.members {
		oc := m.present(mt, o)
		if oc == nil || !o.playing {
			o.below = false
			continue
		}
		_, y := oc.Pos()
		o.below = y >= m.a.PlayLineY
	}
	if it := mt.member(mt.it); it != nil {
		mt.clock = it.below
	} else {
		mt.clock = false
	}
}

// tick is it_game_tick plus the collisions the source's client reported: the clock, tags and locks.
func (m *crowns) tick(mt *crownMatch) {
	now := m.h.Now()
	dt := now.Sub(mt.lastTick).Milliseconds()
	mt.lastTick = now
	it := mt.member(mt.it)
	// The interval counts with the holder where they stood at its start (as solo's tickCrowns).
	if it != nil && mt.clock && dt > 0 {
		it.ms = min(m.a.WinMs, it.ms+dt)
		if it.ms >= m.a.WinMs {
			m.end(mt, &it.crownRef, "won")
			return
		}
	}
	clockWas, itWas := mt.clock, mt.it
	if it == nil {
		// Nobody holds it (nobody was in the arena at GO): playerEntered 838-849 chooses one.
		m.chooseIt(mt, 0)
	}
	m.sense(mt, now)
	changed := clockWas != mt.clock || itWas != mt.it
	if m.tags(mt, now) || m.lockTouches(mt, now) {
		return // each sent the state
	}
	if changed || now.Sub(mt.lastState) >= crownsStateEvery {
		m.sendMatch(mt, m.stateJSON(mt, "crown_state"))
	}
}

// tags: someone touching an unlocked holder takes the crown (games_tag_player 362-380).
func (m *crowns) tags(mt *crownMatch, now time.Time) bool {
	it := mt.member(mt.it)
	ic := m.present(mt, it)
	if ic == nil || now.Before(it.lockUntil) {
		return false
	}
	hx, hy := ic.Pos()
	var best *crownMember
	bestD := math.Inf(1)
	for _, o := range mt.members {
		if o == it || !o.playing {
			continue
		}
		oc := m.present(mt, o)
		if oc == nil {
			continue
		}
		x, y := oc.Pos()
		dx, dy := math.Abs(x-hx), math.Abs(y-hy)
		if dx <= m.a.Tag.X+crownsSlack && dy <= m.a.Tag.Y && dx+dy < bestD {
			best, bestD = o, dx+dy
		}
	}
	if best == nil {
		return false
	}
	from := it
	m.setIt(mt, best)
	m.sense(mt, now)
	msg := m.stateJSON(mt, "crown_tag")
	msg["from"], msg["to"], msg["fromName"], msg["toName"] = from.ID, best.ID, from.Name, best.Name
	m.sendMatch(mt, msg)
	return true
}

// lockTouches: a ready Crown Game Lock is taken by whoever touches it (crown_game_lock.js onPlayerCollision).
func (m *crowns) lockTouches(mt *crownMatch, now time.Time) bool {
	sent := false
	for i, at := range mt.locks {
		if now.Before(at) {
			continue
		}
		lx, ly := m.a.Locks[i][0], m.a.Locks[i][1]
		for _, o := range mt.members {
			if !o.playing {
				continue
			}
			oc := m.present(mt, o)
			if oc == nil {
				continue
			}
			x, y := oc.Pos()
			if math.Abs(x-lx) > m.a.LockReach.X+crownsSlack || math.Abs(y-ly) > m.a.LockReach.Y {
				continue
			}
			mt.locks[i] = now.Add(time.Duration(m.a.LockRespawnMs) * time.Millisecond)
			locked := o.ID == mt.it
			if locked {
				o.lockUntil = now.Add(time.Duration(m.a.PickupLockMs) * time.Millisecond)
			}
			msg := m.stateJSON(mt, "crown_lock")
			msg["i"], msg["id"], msg["name"], msg["locked"], msg["x"], msg["y"] = i, o.ID, o.Name, locked, lx, ly
			m.sendMatch(mt, msg)
			sent = true
			break
		}
	}
	return sent
}

func (m *crowns) scoreRows(mt *crownMatch) []map[string]any {
	rows := []map[string]any{}
	for _, o := range mt.members {
		if o.playing && !o.gone {
			rows = append(rows, map[string]any{"id": o.ID, "name": o.Name, "ms": o.ms})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i]["ms"].(int64) > rows[j]["ms"].(int64) })
	return rows
}

func (m *crowns) stateJSON(mt *crownMatch, kind string) map[string]any {
	now := m.h.Now()
	mt.lastState = now
	locked := int64(0)
	if it := mt.member(mt.it); it != nil && now.Before(it.lockUntil) {
		locked = it.lockUntil.Sub(now).Milliseconds()
	}
	locks := make([]bool, len(mt.locks))
	for i, at := range mt.locks {
		locks[i] = !now.Before(at)
	}
	return map[string]any{"t": kind, "match": mt.id, "round": mt.round, "it": mt.it, "locked": locked, "clock": mt.clock,
		"scores": m.scoreRows(mt), "locks": locks}
}

// end settles a round for everyone (games_end_it_game 613-737; global_game.js cancel 735-763).
func (m *crowns) end(mt *crownMatch, winner *crownRef, why string) {
	if mt.status == "done" {
		return
	}
	mt.status = "done"
	for _, stop := range mt.timers {
		stop()
	}
	mt.timers = nil
	if mt.ticker != nil {
		mt.ticker()
		mt.ticker = nil
	}
	if why != "won" && mt.life != nil {
		mt.life()
		mt.life = nil
	}
	scores := m.scoreRows(mt)
	for _, mem := range mt.members {
		mem.ready = false
		if mem.gone {
			continue
		}
		msg := map[string]any{"t": "crown_end", "match": mt.id, "round": mt.round, "winner": nil, "why": why, "scores": scores,
			"seconds": mem.ms / 1000, "played": mem.playing}
		if winner != nil {
			msg["winner"] = *winner
		}
		if oc := m.h.ClientLocked(mem.ID); oc != nil {
			m.h.SendLocked(oc, msg)
		}
		pid := mem.ID
		if mem.autoLeave != nil {
			mem.autoLeave()
		}
		mem.autoLeave = m.h.After(crownsAutoLeave, func() { m.idle(mt, pid) })
	}
	if winner != nil {
		log.Printf("crowns %s round %d: %s won", mt.id, mt.round, winner.Name)
		m.reward(mt, *winner)
	} else {
		log.Printf("crowns %s round %d: over (%s)", mt.id, mt.round, why)
	}
	if why == "short" || why == "empty" || why == "timeout" {
		// Nobody can play again in a game that ran out of players or time.
		for _, mem := range mt.members {
			if !mem.gone && mem.autoLeave != nil {
				mem.autoLeave()
				mem.autoLeave = nil
			}
		}
		mt.timers = append(mt.timers, m.h.After(crownsLinger, func() { m.forget(mt) }))
	}
}

// reward pays the winner: the currants through the site's ledger, then crown_reward until acked.
func (m *crowns) reward(mt *crownMatch, w crownRef) {
	id := fmt.Sprintf("crowns-%s-%d-%d", mt.id, mt.round, w.ID)
	msg := map[string]any{"t": "crown_reward", "id": id, "match": mt.id, "round": mt.round, "xp": m.a.Reward.XP, "currants": m.a.Reward.Currants}
	if m.ledger == nil || m.a.Reward.Currants <= 0 {
		if m.ledger == nil && !m.trusted {
			msg["currants"] = int64(0) // no ledger to put them in
		}
		m.owe(w.ID, id, msg)
		return
	}
	site := m.ledger
	got := tradeGoods{Items: []tradeItem{}, Currants: m.a.Reward.Currants}
	m.spawn(func() {
		a := site.crowns(id, w.ID, got)
		m.h.After(0, func() { m.credited(w.ID, id, msg, a, 0) })
	})
}

func (m *crowns) credited(player int64, id string, msg map[string]any, a ledgerAnswer, tries int) {
	switch a.Status {
	case "settled":
		m.owe(player, id, msg)
	case "refused", "void":
		log.Printf("crowns reward %s: the ledger said %s; sent without currants", id, a.Status)
		msg["currants"] = int64(0)
		m.owe(player, id, msg)
	default:
		tries++
		if tries > 6 {
			log.Printf("crowns reward %s: no ledger answer after %d tries; sent without currants", id, tries)
			msg["currants"] = int64(0)
			m.owe(player, id, msg)
			return
		}
		wait := min(time.Minute, time.Second<<tries)
		log.Printf("crowns reward %s: no ledger answer (try %d): again in %s", id, tries, wait)
		site := m.ledger
		got := tradeGoods{Items: []tradeItem{}, Currants: m.a.Reward.Currants}
		m.h.After(wait, func() {
			m.spawn(func() {
				a := site.crowns(id, player, got)
				m.h.After(0, func() { m.credited(player, id, msg, a, tries) })
			})
		})
	}
}

// owe keeps a reward for a player until they ack it, and sends it now when they are online.
func (m *crowns) owe(player int64, id string, msg map[string]any) {
	for _, o := range m.owed[player] {
		if o.id == id {
			return
		}
	}
	b, _ := json.Marshal(msg)
	list := append(m.owed[player], crownOwed{id: id, msg: b})
	if len(list) > crownsMaxOwed {
		list = list[len(list)-crownsMaxOwed:]
	}
	m.owed[player] = list
	if cl := m.h.ClientLocked(player); cl != nil {
		m.h.pushLocked(cl, b)
	}
}

func (m *crowns) ack(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(frame, &msg) != nil || !crownsIDRe.MatchString(msg.ID) {
		return
	}
	list := m.owed[cl.ID()]
	for i, o := range list {
		if o.id == msg.ID {
			list = append(list[:i:i], list[i+1:]...)
			if len(list) == 0 {
				delete(m.owed, cl.ID())
			} else {
				m.owed[cl.ID()] = list
			}
			return
		}
	}
}

func (m *crowns) forget(mt *crownMatch) {
	if m.matches[mt.id] != mt {
		return
	}
	for _, stop := range mt.timers {
		stop()
	}
	if mt.ticker != nil {
		mt.ticker()
	}
	if mt.life != nil {
		mt.life()
	}
	for _, mem := range mt.members {
		if mem.grace != nil {
			mem.grace()
		}
		if mem.autoLeave != nil {
			mem.autoLeave()
		}
		if m.matchOf[mem.ID] == mt {
			delete(m.matchOf, mem.ID)
		}
	}
	delete(m.matches, mt.id)
}

// drop takes a member out of the match (global_game.js playerLeft 803-813 for it_game).
func (m *crowns) drop(mt *crownMatch, id int64) {
	mem := mt.member(id)
	if mem == nil || mem.gone {
		return
	}
	if mt.status == "running" && mem.playing {
		if oc := m.h.ClientLocked(id); oc != nil {
			m.h.SendLocked(oc, map[string]any{"t": "crown_left", "match": mt.id, "round": mt.round, "seconds": mem.ms / 1000})
		}
	}
	mem.gone = true
	mem.ready = false
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
	live := 0
	for _, o := range mt.members {
		if !o.gone {
			live++
		}
	}
	switch mt.status {
	case "running":
		playing := 0
		for _, o := range mt.members {
			if !o.gone && o.playing {
				playing++
			}
		}
		if playing == 0 {
			m.end(mt, nil, "empty")
		} else if mt.it == id {
			m.chooseIt(mt, id)
			m.sense(mt, m.h.Now())
			m.sendMatch(mt, m.stateJSON(mt, "crown_state"))
		} else {
			m.sendMatch(mt, m.stateJSON(mt, "crown_state"))
		}
	case "waiting", "soon", "countdown":
		// Nobody else can join a ticket instance: under three, the game is off (ours).
		if live < m.a.Players.Retry {
			m.end(mt, nil, "short")
		} else if mt.status == "waiting" && mt.round == 1 && mt.lastTick.IsZero() {
			// Everyone left may already be ready.
			for _, o := range mt.members {
				if !o.gone && o.ready {
					o.ready = false
					m.markReady(mt, o, o.Name)
					break
				}
			}
		}
	}
	if live == 0 {
		m.forget(mt)
	}
}

// idle is games_auto_leave (inc_games.js 2301-2306).
func (m *crowns) idle(mt *crownMatch, id int64) {
	mem := mt.member(id)
	if mem == nil || mem.gone || mem.ready || (mt.status != "waiting" && mt.status != "done") {
		return
	}
	if oc := m.h.ClientLocked(id); oc != nil {
		m.h.SendLocked(oc, map[string]any{"t": "crown_removed", "match": mt.id, "text": "You were removed from the game due to inactivity."})
	}
	m.drop(mt, id)
}

func (m *crowns) leave(cl *Client, frame json.RawMessage) {
	var msg struct {
		Match string `json:"match"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	if mt, _ := m.matchFor(cl, msg.Match); mt != nil {
		m.drop(mt, cl.ID())
	}
}

// —— Hooks ——

func (m *crowns) onConnect(cl *Client) {
	for _, o := range m.owed[cl.ID()] {
		m.h.SendLocked(cl, json.RawMessage(o.msg))
	}
}

func (m *crowns) onJoin(cl *Client) {
	id := cl.ID()
	if mt := m.matchOf[id]; mt != nil {
		mem := mt.member(id)
		if cl.Street() == mt.street {
			if mem != nil && mem.grace != nil {
				mem.grace()
				mem.grace = nil
			}
		} else {
			// Off to another street: out of the game (instance.js player_exited_location).
			m.drop(mt, id)
		}
	}
	for _, inv := range m.invites {
		if inv.street == cl.Street() && !inv.full && inv.from.ID != id {
			m.h.SendLocked(cl, m.challengedMsg(inv))
		}
	}
}

func (m *crowns) onLeave(cl *Client, street string) {
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

func (m *crowns) startGrace(mt *crownMatch, id int64) {
	mem := mt.member(id)
	if mem == nil || mem.gone || mem.grace != nil {
		return
	}
	if mt.status == "done" {
		m.drop(mt, id)
		return
	}
	// A holder who drops passes the crown at once (playerLeft on logout).
	if mt.status == "running" && mt.it == id {
		m.chooseIt(mt, id)
		m.sense(mt, m.h.Now())
		m.sendMatch(mt, m.stateJSON(mt, "crown_state"))
	}
	mem.grace = m.h.After(crownsGrace, func() {
		mem.grace = nil
		m.drop(mt, id)
	})
}

func (m *crowns) onDisconnect(cl *Client) {
	id := cl.ID()
	if _, ok := m.invites[id]; ok {
		m.withdraw(id, "")
	}
	delete(m.asks, id)
	if mt := m.matchOf[id]; mt != nil {
		m.startGrace(mt, id)
	}
}
