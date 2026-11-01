package main

// Math Mayhem between real players (G397): Game of Crowns' sister game on the
// same arena. Players join one open game from a Game Pig, two teams each get
// a number, and when two players touch the server does their math; the first
// answer equal to the target wins. The server decides all of it (the teams,
// the numbers, the target, every touch from the positions players report in
// "move", the locks and the winner); the client sends no numbers.
//
// The rules are GameServerJS's math_mayhem (eleven-gsjs):
//   - config_prod.js:1282 shared_instances.math_mayhem: locations
//     ['LHV2H026E492IAF'] (the Game of Crowns street), min_players 4,
//     max_players 20, can_retry.
//   - items/test_game_starter.js 41-86: the Game Pig's Admire asks "Which game
//     do you want to play?" and Maths calls playerJoinInstance; no ticket or
//     other item opens the mode.
//   - groups/shared_instance_manager.js playerJoinInstance 36-87 and
//     findAvailableInstances 585-633: the player goes to the fullest open game
//     (not started, under max_players), else a new one;
//     onSharedInstanceEnter 89-229 (Click to Start, games_auto_leave after
//     2 min, 210); dismissOverlaysAndStart 231-342 with instance.js
//     can_start_game 582-602 (min_players ready and nobody unready; no
//     min_retry_players for math_mayhem, so 4 every round); "YAY! THE GAME IS
//     STARTING SOON." 5 s (276); startCountdown 646-689 (the 321 countdown).
//     endGame 714-747 opens the game again (instance.js clear_game_running),
//     so a finished game takes new players for its next round.
//   - groups/global_game.js initMathMayhemInInstance 271-327: teams blue
//     then red in turn, each player's first number 1 + the players already
//     on their team (322), the target randInt(10, 100) (298).
//   - players/inc_games.js games_do_math 982-1010: two unlocked players touch;
//     the same team adds, two teams take |a - b|; both get the answer and are
//     locked 1 s (games_math_mayhem_lock 1232-1250).
//     games_set_math_mayhem_score 1024-1039: an answer equal to the target
//     wins. games_end_math_mayhem 1048-1155: the winner +200 iMG (1077,
//     stats_add_xp(200, false)) +50 currants (1078), math_mayhem:won; the rest
//     math_mayhem:lost. global_game.js playerLeft 816-818: a player who leaves
//     takes their number with them and the game goes on.
//
// Ours, where the source stops (js/math-mayhem.js has the same list):
//   - math_mayhem_set_winner (global_game.js 338-340) only records the
//     winner; nothing ends the game. The winner's own end cancels it for
//     everyone (inc_games.js:1082), so the server ends it at the win.
//   - Both touching players reach the target together; the source records the
//     second one set, the player touched (games_do_math sets `this`, the
//     mover, then `pc`). The mover is the one who moved further in the last
//     tick; the other wins.
//   - A touch counts once: the pair must part before they do math again.
//     Pairs touching at GO start as touched.
//   - A round ends with no winner after roundMs, or when fewer than two
//     players are left in it.
//   - Play Again counts as clicking start for the next round (as crowns.go);
//     a player who played and has not chosen when the next countdown starts
//     is taken out; a new player in the arena must click before it starts.
//   - The street a player joins from hears that they want a game (as
//     crowns.go's challenge, inc_games.js 2392), and anyone there can join.
//   - A member who drops has mayhemGrace to come back; a game ends by itself
//     after mayhemMaxLife.
//   - Each touch reach has mayhemSlack more across, because positions arrive
//     every 100 ms (crownsSlack).
//
// The winner's currants go into the site's ledger through a signed call (POST
// groddle/v1/ledger/mayhem, idempotent per id, LedgerMayhem.php); the iMG and
// the badges are the client's to apply from mayhem_reward and mayhem_end.
// What the server owes a winner (mayhem_reward until acked, and a win the
// ledger has not answered for) is written to -mayhem-file at once, as
// crowns.go keeps -crowns-file, and sent again on every connect.
//
// Client to server:
//
//	{"t":"mayhem_join"}                       Maths from a Game Pig (or Join from a street's prompt)
//	{"t":"mayhem_ready","room":"…"}           Click to Start
//	{"t":"mayhem_again","room":"…"}           Play Again (after an end)
//	{"t":"mayhem_leave","room":"…"}           Leave the Game
//	{"t":"mayhem_ack","id":"mayhem-…"}        applied a mayhem_reward and saved
//
// Server to client:
//
//	{"t":"mayhem_room","room":"…","street":"MAYHEM_…","title":"Math Mayhem","slot":0,"spawn":[x,y],"players":[{"id","name"}],"min":4,"max":20,"autoLeave":120}
//	{"t":"mayhem_refused","why":"busy|street|dead|slow|full","text":"…"}
//	{"t":"mayhem_looking","from":{"id","name"},"text":"…","need":2}     to the joiner's street
//	{"t":"mayhem_players","room":"…","players":[…],"ready":[7],"need":2,"text":"…"}
//	{"t":"mayhem_soon","room":"…","in":5000,"text":"YAY! THE GAME IS STARTING SOON."}
//	{"t":"mayhem_countdown","room":"…","round":1,"in":4000,"players":[…]}
//	{"t":"mayhem_go","room":"…","round":1,"target":42,"players":[{"id","name","team","score","locked"}],"ms":300000}
//	{"t":"mayhem_state","room":"…","round":1,"target":42,"players":[…]}
//	{"t":"mayhem_bump","room":"…","round":1,"target":42,"players":[…],"a":{"id","name"},"b":{"id","name"},"answer":7}
//	{"t":"mayhem_end","room":"…","round":1,"target":42,"winner":{"id","name"}|null,"why":"won|timeout|short|empty","players":[…],"played":true}
//	{"t":"mayhem_removed","room":"…","text":"You were removed from the game due to inactivity."}
//	{"t":"mayhem_reward","id":"mayhem-<room>-<round>-<player>","room":"…","round":1,"xp":200,"currants":50}

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
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

//go:embed data/mayhem.json
var mayhemJSON []byte

const (
	ledgerRouteMayhem = "/groddle/v1/ledger/mayhem"

	mayhemAutoLeave = 2 * time.Minute  // shared_instance_manager.js:210
	mayhemSoon      = 5 * time.Second  // shared_instance_manager.js:276
	mayhemGrace     = 15 * time.Second // ours, as crownsGrace
	mayhemMaxLife   = time.Hour        // ours
	mayhemLinger    = 5 * time.Minute  // ours: a finished, empty game is forgotten
	mayhemTickEvery = 100 * time.Millisecond
	mayhemSlack     = 12.0 // as crownsSlack
	mayhemMaxOwed   = 20
	mayhemMaxScore  = 1_000_000 // ours: a guard, far past any target

	mayhemAsksBurst = 6
	mayhemAsksEvery = time.Second

	mayhemSoonText = "YAY! THE GAME IS STARTING SOON."
	mayhemMustWait = "You must wait for more players before this game can begin."
	mayhemRemoved  = "You were removed from the game due to inactivity."
	mayhemTooDead  = "You are too dead to play games."
)

var mayhemFileFlag = flag.String("mayhem-file", "", "file that keeps Math Mayhem rewards not yet applied and winners' currants not yet in the site's ledger (default: realtime-mayhem.json beside -secret-file; off: memory only)")

// mayhemFileForTest lets a test give the module a state file; under go test the flag is ignored.
var mayhemFileForTest string

var (
	mayhemRoomRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
	mayhemIDRe   = regexp.MustCompile(`^mayhem-[0-9a-f]{16}-[0-9]{1,3}-[0-9]{1,12}$`)
	// Streets nobody joins from: instances and homes (the Game Pig stands on public streets).
	mayhemPrivatePrefix = []string{"MAYHEM_", "CROWNS_", "RACE_", "PARTY_", "HOME_", "HOUSE_"}
)

type mayhemRules struct {
	Street string       `json:"street"`
	Title  string       `json:"title"`
	Starts [][2]float64 `json:"starts"`
	Spread float64      `json:"spread"`
	Touch  struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"touch"`
	LockMs      int64 `json:"lockMs"`
	CountdownMs int64 `json:"countdownMs"`
	RoundMs     int64 `json:"roundMs"`
	Players     struct {
		Min int `json:"min"`
		Max int `json:"max"`
	} `json:"players"`
	Target struct {
		Min int64 `json:"min"`
		Max int64 `json:"max"`
	} `json:"target"`
	Teams  []string `json:"teams"`
	Reward struct {
		XP       int64 `json:"xp"`
		Currants int64 `json:"currants"`
	} `json:"reward"`
}

func parseMayhemRules(b []byte) (*mayhemRules, error) {
	var a mayhemRules
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	if a.Title == "" || len(a.Starts) == 0 || len(a.Teams) != 2 || a.Players.Min < 2 || a.Players.Max < a.Players.Min ||
		a.Target.Min < 1 || a.Target.Max < a.Target.Min || a.RoundMs <= 0 || a.Touch.X <= 0 {
		return nil, fmt.Errorf("not the Math Mayhem rules")
	}
	return &a, nil
}

type mayhemRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type mayhemMember struct {
	mayhemRef
	slot      int
	team      string
	score     int64
	ready     bool // clicked start (or Play Again) for the coming round
	playing   bool // in the round under way
	played    bool // has played a round in this game
	gone      bool
	lockUntil time.Time
	lastX     float64
	lastY     float64
	moved     float64 // how far they moved in the last tick
	autoLeave func() bool
	grace     func() bool
}

type mayhemRoom struct {
	id, street string
	members    []*mayhemMember
	status     string // waiting, soon, countdown, running, done
	round      int
	rounds     int // rounds played to the end
	target     int64
	touched    map[[2]int64]bool // pairs that did math and have not parted since
	timers     []func() bool
	ticker     func() bool
	clock      func() bool
	life       func() bool
}

// mayhemLedger is the site's ledger for a winner's currants: put them in, once per id.
type mayhemLedger interface {
	mayhem(id string, user int64, got tradeGoods) ledgerAnswer
}

func (l *httpLedger) mayhem(id string, user int64, got tradeGoods) ledgerAnswer {
	return l.call(ledgerRouteMayhem, map[string]any{"id": id, "user": user, "got": goodsJSON(got)})
}

type mayhemOwed struct {
	ID  string          `json:"id"`
	Msg json.RawMessage `json:"msg"`
}

// mayhemCredit is a win whose currants the site's ledger has not answered for yet.
type mayhemCredit struct {
	ID     string          `json:"id"`
	Player int64           `json:"player"`
	Msg    json.RawMessage `json:"msg"`
	tries  int
	busy   bool
}

type mayhemSaved struct {
	Owed    map[int64][]mayhemOwed   `json:"owed"`
	Credits map[string]*mayhemCredit `json:"credits"`
}

type mayhem struct {
	h       *Hub
	a       *mayhemRules
	rooms   map[string]*mayhemRoom
	roomOf  map[int64]*mayhemRoom
	asks    map[int64]*crownsAsk
	owed    map[int64][]mayhemOwed
	credits map[string]*mayhemCredit
	path    string
	ledger  mayhemLedger
	trusted bool
	spawn   func(func())
	newID   func() string
	pick    func(lo, hi int64) int64
}

func init() { modules = append(modules, newMayhem) }

func newMayhem(h *Hub) Module {
	a, err := parseMayhemRules(mayhemJSON)
	if err != nil {
		panic("realtime: data/mayhem.json: " + err.Error())
	}
	m := newMayhemWith(h, a)
	m.useFile(statePath(mayhemFileForTest, mayhemFileFlag, "realtime-mayhem.json"))
	if !testing.Testing() {
		site, missing := newLedgerFromFlags()
		if hl, ok := site.(*httpLedger); ok && hl != nil {
			m.ledger = hl
		}
		m.trusted = site == nil && !missing
	}
	return m
}

func newMayhemWith(h *Hub, a *mayhemRules) *mayhem {
	return &mayhem{
		h: h, a: a,
		rooms: map[string]*mayhemRoom{}, roomOf: map[int64]*mayhemRoom{},
		asks: map[int64]*crownsAsk{}, owed: map[int64][]mayhemOwed{}, credits: map[string]*mayhemCredit{},
		spawn: func(fn func()) { go fn() },
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return hex.EncodeToString(b)
		},
		pick: func(lo, hi int64) int64 { return lo + mrand.Int64N(hi-lo+1) },
	}
}

func (m *mayhem) Register(r *Registry) {
	r.Handle("mayhem_join", m.join)
	r.Handle("mayhem_ready", m.ready)
	r.Handle("mayhem_again", m.again)
	r.Handle("mayhem_leave", m.leave)
	r.Handle("mayhem_ack", m.ack)
	r.OnConnect(m.onConnect)
	r.OnJoin(m.onJoin)
	r.OnLeave(m.onLeave)
	r.OnDisconnect(m.onDisconnect)
	r.GateJoin(m.gate)
	// crowns.go asks before a challenge: a player in a Math Mayhem game is busy.
	r.Provide("mayhem.busy", func(id int64) bool { return m.roomOf[id] != nil })
}

// mayhemStreet is the hub street a game's players share: unguessable.
func mayhemStreet(id string) string { return "MAYHEM_" + strings.ToUpper(id) }

func (m *mayhem) gate(cl *Client, street string) (bool, string, string) {
	if !strings.HasPrefix(street, "MAYHEM_") {
		return true, "", ""
	}
	if rm := m.roomOf[cl.ID()]; rm != nil && rm.street == street {
		if mem := rm.member(cl.ID()); mem != nil && !mem.gone {
			return true, "", ""
		}
	}
	return false, "private_street", "That game is not yours."
}

func (m *mayhem) allow(cl *Client) bool {
	a := m.asks[cl.ID()]
	now := m.h.Now()
	if a == nil {
		a = &crownsAsk{tokens: mayhemAsksBurst, at: now}
		m.asks[cl.ID()] = a
	}
	a.tokens = math.Min(mayhemAsksBurst, a.tokens+now.Sub(a.at).Seconds()/mayhemAsksEvery.Seconds())
	a.at = now
	if a.tokens < 1 {
		return false
	}
	a.tokens--
	return true
}

func (m *mayhem) refuse(cl *Client, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "mayhem_refused", "why": why, "text": text})
}

func mayhemPrivate(street string) bool {
	for _, p := range mayhemPrivatePrefix {
		if strings.HasPrefix(street, p) {
			return true
		}
	}
	return false
}

func (rm *mayhemRoom) member(id int64) *mayhemMember {
	for _, mem := range rm.members {
		if mem.ID == id {
			return mem
		}
	}
	return nil
}

func (rm *mayhemRoom) live() []*mayhemMember {
	var out []*mayhemMember
	for _, mem := range rm.members {
		if !mem.gone {
			out = append(out, mem)
		}
	}
	return out
}

// open: a game that takes new players (findAvailableInstances: not started, under max_players).
func (m *mayhem) open(rm *mayhemRoom) bool {
	return (rm.status == "waiting" || rm.status == "done") && len(rm.live()) < m.a.Players.Max
}

// —— Joining (playerJoinInstance) ——

func (m *mayhem) join(cl *Client, _ json.RawMessage) {
	if !m.allow(cl) {
		m.refuse(cl, "slow", "Slow down a little.")
		return
	}
	id := cl.ID()
	if m.roomOf[id] != nil {
		m.refuse(cl, "busy", "You are already in a game!")
		return
	}
	if busy, ok := m.h.ProvidedLocked("crowns.busy").(func(int64) bool); ok && busy(id) {
		m.refuse(cl, "busy", "You are already in a game!")
		return
	}
	street := cl.Street()
	if inNaraka(street) {
		m.refuse(cl, "dead", mayhemTooDead)
		return
	}
	if street == "" || mayhemPrivate(street) {
		m.refuse(cl, "street", "You cannot start a game from here.")
		return
	}
	// Fill first: the open game with the most players (playerJoinInstance 46-66).
	var rm *mayhemRoom
	for _, r := range m.rooms {
		if !m.open(r) {
			continue
		}
		if rm == nil || len(r.live()) > len(rm.live()) || (len(r.live()) == len(rm.live()) && r.id < rm.id) {
			rm = r
		}
	}
	if rm == nil {
		rid := m.newID()
		for m.rooms[rid] != nil || !mayhemRoomRe.MatchString(rid) {
			rid = m.newID()
		}
		rm = &mayhemRoom{id: rid, street: mayhemStreet(rid), status: "waiting", round: 1, touched: map[[2]int64]bool{}}
		m.rooms[rid] = rm
		rm.life = m.h.After(mayhemMaxLife, func() { m.close(rm) })
	}
	if rm.status == "done" {
		// A new player opens the next round (endGame 714-747: the game takes players again).
		rm.status = "waiting"
		for _, o := range rm.members {
			o.playing = false
		}
	}
	// The lowest free seat.
	taken := map[int]bool{}
	for _, o := range rm.live() {
		taken[o.slot] = true
	}
	slot := 0
	for taken[slot] {
		slot++
	}
	// A member who left this game and comes back takes a new row.
	mem := &mayhemMember{mayhemRef: mayhemRef{id, cl.Name()}, slot: slot}
	for i, o := range rm.members {
		if o.ID == id && o.gone {
			rm.members = append(rm.members[:i], rm.members[i+1:]...)
			break
		}
	}
	rm.members = append(rm.members, mem)
	m.roomOf[id] = rm
	mem.autoLeave = m.h.After(mayhemAutoLeave, func() { m.idle(rm, id) })
	sx, sy := m.spawnFor(slot)
	m.h.SendLocked(cl, map[string]any{"t": "mayhem_room", "room": rm.id, "street": rm.street, "title": m.a.Title,
		"slot": slot, "spawn": []float64{sx, sy}, "players": m.namesJSON(rm), "min": m.a.Players.Min, "max": m.a.Players.Max,
		"autoLeave": int(mayhemAutoLeave.Seconds())})
	need := m.need(rm)
	m.sendRoom(rm, m.playersMsg(rm, cl.Name()+" joined "+m.a.Title+"."), id)
	// The street hears (ours, as a crowns challenge): anyone there can join.
	m.h.BroadcastStreetLocked(street, map[string]any{"t": "mayhem_looking", "from": mem.mayhemRef, "title": m.a.Title,
		"text": cl.Name() + " wants to play " + m.a.Title + "." + func() string {
			if need > 0 {
				return " Need " + itoa(need) + " more."
			}
			return ""
		}(), "need": need}, id)
	log.Printf("mayhem %s: %s joined (%d)", rm.id, cl.Name(), len(rm.live()))
}

// spawnFor is a seat's start: the arena's starts in turn, spread beside them past the touch reach
// (js/math-mayhem.js mayhemSpawn).
func (m *mayhem) spawnFor(slot int) (float64, float64) {
	base := m.a.Starts[slot%len(m.a.Starts)]
	ring := slot / len(m.a.Starts)
	step := 0
	if ring > 0 {
		step = (ring + 1) / 2
		if ring%2 == 0 {
			step = -step
		}
	}
	return math.Round(base[0] + float64(step)*m.a.Spread), math.Round(base[1])
}

// need: how many more must click before a start (min_players less those ready).
func (m *mayhem) need(rm *mayhemRoom) int {
	return max(0, m.a.Players.Min-len(m.readyIDs(rm)))
}

func (m *mayhem) readyIDs(rm *mayhemRoom) []int64 {
	ids := []int64{}
	for _, o := range rm.live() {
		if o.ready {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

func (m *mayhem) namesJSON(rm *mayhemRoom) []map[string]any {
	out := []map[string]any{}
	for _, o := range rm.live() {
		out = append(out, map[string]any{"id": o.ID, "name": o.Name, "ready": o.ready})
	}
	return out
}

func (m *mayhem) playersMsg(rm *mayhemRoom, text string) map[string]any {
	return map[string]any{"t": "mayhem_players", "room": rm.id, "players": m.namesJSON(rm), "ready": m.readyIDs(rm), "need": m.need(rm), "text": text}
}

func (m *mayhem) sendRoom(rm *mayhemRoom, msg map[string]any, except int64) {
	b, _ := json.Marshal(msg)
	for _, mem := range rm.live() {
		if mem.ID == except {
			continue
		}
		if oc := m.h.ClientLocked(mem.ID); oc != nil {
			m.h.pushLocked(oc, b)
		}
	}
}

func (m *mayhem) after(rm *mayhemRoom, d time.Duration, fn func()) {
	rm.timers = append(rm.timers, m.h.After(d, fn))
}

// roomFor finds the caller's game from a frame's "room", or nil.
func (m *mayhem) roomFor(cl *Client, frame json.RawMessage) (*mayhemRoom, *mayhemMember) {
	var msg struct {
		Room string `json:"room"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return nil, nil
	}
	rm := m.roomOf[cl.ID()]
	if rm == nil || rm.id != msg.Room {
		return nil, nil
	}
	mem := rm.member(cl.ID())
	if mem == nil || mem.gone {
		return nil, nil
	}
	return rm, mem
}

// present: a member standing in the game's arena now.
func (m *mayhem) present(rm *mayhemRoom, mem *mayhemMember) *Client {
	if mem == nil || mem.gone {
		return nil
	}
	oc := m.h.ClientLocked(mem.ID)
	if oc == nil || !oc.Online() || oc.Street() != rm.street {
		return nil
	}
	return oc
}

// —— Starting (dismissOverlaysAndStart, can_start_game, startCountdown) ——

func (m *mayhem) ready(cl *Client, frame json.RawMessage) {
	rm, mem := m.roomFor(cl, frame)
	if rm == nil || rm.status != "waiting" || mem.ready || cl.Street() != rm.street {
		return
	}
	m.markReady(rm, mem)
}

// again is Play Again (games_leave_instance_overlay 'again', inc_games.js 2212-2233).
func (m *mayhem) again(cl *Client, frame json.RawMessage) {
	rm, mem := m.roomFor(cl, frame)
	if rm == nil || mem.ready || cl.Street() != rm.street || !mem.played {
		return
	}
	switch rm.status {
	case "done":
		rm.status = "waiting"
		for _, o := range rm.members {
			o.playing = false
		}
	case "waiting":
	case "soon":
		// Joining a round about to start.
		mem.ready = true
		m.stopAutoLeave(mem)
		m.sendRoom(rm, m.playersMsg(rm, cl.Name()+" is ready to play. LET'S GO!"), 0)
		return
	default:
		return
	}
	m.markReady(rm, mem)
}

func (m *mayhem) stopAutoLeave(mem *mayhemMember) {
	if mem.autoLeave != nil {
		mem.autoLeave()
		mem.autoLeave = nil
	}
}

func (m *mayhem) markReady(rm *mayhemRoom, mem *mayhemMember) {
	mem.ready = true
	m.stopAutoLeave(mem)
	m.check(rm, mem.Name)
}

// check starts the soon timer when the game can start (can_start_game 582-602): min_players ready
// and nobody unready, where a player who played the last round and has not chosen does not hold it up.
func (m *mayhem) check(rm *mayhemRoom, name string) {
	if rm.status != "waiting" {
		return
	}
	unready := 0
	for _, o := range rm.live() {
		if !o.ready && !o.played {
			unready++
		}
	}
	need := m.need(rm)
	if need == 0 && unready == 0 {
		m.sendRoom(rm, m.playersMsg(rm, name+" is ready to play. LET'S GO!"), 0)
		rm.status = "soon"
		m.sendRoom(rm, map[string]any{"t": "mayhem_soon", "room": rm.id, "in": mayhemSoon.Milliseconds(), "text": mayhemSoonText}, 0)
		m.after(rm, mayhemSoon, func() { m.countdown(rm) })
		return
	}
	text := name + " is ready to play. Need " + itoa(need) + " more."
	if need == 0 {
		text = name + " is ready to play. Just waiting on a few more slackers to HIT THE START BUTTON."
	}
	if name == "" {
		text = mayhemMustWait
	}
	m.sendRoom(rm, m.playersMsg(rm, text), 0)
}

// countdown is startWithMinimumPlayers + startCountdown (345-358, 646-689).
func (m *mayhem) countdown(rm *mayhemRoom) {
	if rm.status != "soon" {
		return
	}
	if len(m.readyIDs(rm)) < m.a.Players.Min {
		rm.status = "waiting"
		m.sendRoom(rm, m.playersMsg(rm, mayhemMustWait), 0)
		return
	}
	// The round's players are the ready ones; anyone still choosing is out of the game.
	for _, o := range rm.live() {
		if o.ready {
			continue
		}
		if oc := m.h.ClientLocked(o.ID); oc != nil {
			m.h.SendLocked(oc, map[string]any{"t": "mayhem_removed", "room": rm.id, "text": mayhemRemoved})
		}
		m.drop(rm, o.ID)
	}
	if rm.rounds > 0 {
		rm.round = rm.rounds + 1
	}
	// initMathMayhemInInstance 304-322: in seat order, blue first, then red in turn.
	live := rm.live()
	sort.SliceStable(live, func(i, j int) bool { return live[i].slot < live[j].slot })
	counts := map[string]int64{}
	last := ""
	for _, o := range live {
		o.playing = o.ready
		o.ready = false
		o.lockUntil = time.Time{}
		if !o.playing {
			o.team, o.score = "", 0
			continue
		}
		team := m.a.Teams[0]
		if last == m.a.Teams[0] {
			team = m.a.Teams[1]
		}
		last = team
		counts[team]++
		o.team, o.score = team, counts[team]
	}
	rm.target = m.pick(m.a.Target.Min, m.a.Target.Max)
	rm.touched = map[[2]int64]bool{}
	rm.status = "countdown"
	m.sendRoom(rm, map[string]any{"t": "mayhem_countdown", "room": rm.id, "round": rm.round, "in": m.a.CountdownMs,
		"players": m.namesJSON(rm)}, 0)
	m.after(rm, time.Duration(m.a.CountdownMs)*time.Millisecond, func() { m.goNow(rm) })
}

// goNow starts the round: games_start_math_mayhem for each player (958-979), the target shown.
func (m *mayhem) goNow(rm *mayhemRoom) {
	if rm.status != "countdown" {
		return
	}
	if m.playingCount(rm) < 2 {
		m.end(rm, nil, "short")
		return
	}
	rm.status = "running"
	for _, o := range rm.members {
		if oc := m.present(rm, o); oc != nil {
			o.lastX, o.lastY = oc.Pos()
		}
		o.played = o.played || o.playing
	}
	// Pairs already touching at GO start as touched: they part first.
	m.touching(rm, func(a, b *mayhemMember, _ float64) { rm.touched[mayhemPair(a.ID, b.ID)] = true })
	msg := m.stateJSON(rm, "mayhem_go")
	msg["ms"] = m.a.RoundMs
	m.sendRoom(rm, msg, 0)
	log.Printf("mayhem %s round %d: GO, target %d, %d players", rm.id, rm.round, rm.target, m.playingCount(rm))
	rm.clock = m.h.After(time.Duration(m.a.RoundMs)*time.Millisecond, func() {
		if rm.status == "running" {
			m.end(rm, nil, "timeout")
		}
	})
	m.arm(rm)
}

func (m *mayhem) playingCount(rm *mayhemRoom) int {
	n := 0
	for _, o := range rm.live() {
		if o.playing {
			n++
		}
	}
	return n
}

func (m *mayhem) arm(rm *mayhemRoom) {
	if rm.ticker != nil {
		rm.ticker()
	}
	rm.ticker = m.h.After(mayhemTickEvery, func() {
		rm.ticker = nil
		if rm.status != "running" {
			return
		}
		m.tick(rm)
		if rm.status == "running" {
			m.arm(rm)
		}
	})
}

func mayhemPair(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}

// touching calls fn for every pair of playing members in the arena within reach, nearest first.
func (m *mayhem) touching(rm *mayhemRoom, fn func(a, b *mayhemMember, d float64)) {
	type at struct {
		mem  *mayhemMember
		x, y float64
	}
	var here []at
	for _, o := range rm.members {
		if !o.playing {
			continue
		}
		if oc := m.present(rm, o); oc != nil {
			x, y := oc.Pos()
			here = append(here, at{o, x, y})
		}
	}
	type pair struct {
		a, b *mayhemMember
		d    float64
	}
	var pairs []pair
	for i := 0; i < len(here); i++ {
		for j := i + 1; j < len(here); j++ {
			dx, dy := math.Abs(here[i].x-here[j].x), math.Abs(here[i].y-here[j].y)
			if dx <= m.a.Touch.X+mayhemSlack && dy <= m.a.Touch.Y {
				pairs = append(pairs, pair{here[i].mem, here[j].mem, dx + dy})
			}
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].d < pairs[j].d })
	for _, p := range pairs {
		fn(p.a, p.b, p.d)
	}
}

// tick reads where everyone stands and does the math of every new touch (games_do_math 982-1010).
func (m *mayhem) tick(rm *mayhemRoom) {
	now := m.h.Now()
	for _, o := range rm.members {
		o.moved = 0
		if oc := m.present(rm, o); oc != nil && o.playing {
			x, y := oc.Pos()
			o.moved = math.Hypot(x-o.lastX, y-o.lastY)
			o.lastX, o.lastY = x, y
		}
	}
	near := map[[2]int64]bool{}
	m.touching(rm, func(a, b *mayhemMember, _ float64) {
		key := mayhemPair(a.ID, b.ID)
		near[key] = true
		if rm.status != "running" || rm.touched[key] {
			return
		}
		// Locked? (games_math_mayhem_is_locked 1267-1273): the touch waits, the pair still touching.
		if now.Before(a.lockUntil) || now.Before(b.lockUntil) {
			return
		}
		rm.touched[key] = true
		m.bump(rm, a, b, now)
	})
	// Pairs that parted can do math again.
	for key := range rm.touched {
		if !near[key] {
			delete(rm.touched, key)
		}
	}
}

// bump is games_do_math: both get the answer and a 1 s lock; an answer at the target wins.
func (m *mayhem) bump(rm *mayhemRoom, a, b *mayhemMember, now time.Time) {
	var answer int64
	if a.team == b.team {
		answer = a.score + b.score
	} else {
		answer = a.score - b.score
		if answer < 0 {
			answer = -answer
		}
	}
	answer = min(answer, mayhemMaxScore)
	a.score, b.score = answer, answer
	lock := now.Add(time.Duration(m.a.LockMs) * time.Millisecond)
	a.lockUntil, b.lockUntil = lock, lock
	msg := m.stateJSON(rm, "mayhem_bump")
	msg["a"], msg["b"], msg["answer"] = a.mayhemRef, b.mayhemRef, answer
	m.sendRoom(rm, msg, 0)
	if answer == rm.target {
		// Both reached it; the source's winner is the second one set, the player touched (the one who
		// moved less).
		w := b
		if a.moved < b.moved || (a.moved == b.moved && a.slot > b.slot) {
			w = a
		}
		m.end(rm, &w.mayhemRef, "won")
	}
}

func (m *mayhem) rows(rm *mayhemRoom) []map[string]any {
	now := m.h.Now()
	rows := []map[string]any{}
	live := rm.live()
	sort.SliceStable(live, func(i, j int) bool { return live[i].slot < live[j].slot })
	for _, o := range live {
		if !o.playing {
			continue
		}
		locked := int64(0)
		if now.Before(o.lockUntil) {
			locked = o.lockUntil.Sub(now).Milliseconds()
		}
		rows = append(rows, map[string]any{"id": o.ID, "name": o.Name, "team": o.team, "score": o.score, "locked": locked})
	}
	return rows
}

func (m *mayhem) stateJSON(rm *mayhemRoom, kind string) map[string]any {
	return map[string]any{"t": kind, "room": rm.id, "round": rm.round, "target": rm.target, "players": m.rows(rm)}
}

// —— The end (games_end_math_mayhem 1048-1155, global_game.js cancel 735-763) ——

func (m *mayhem) end(rm *mayhemRoom, winner *mayhemRef, why string) {
	if rm.status == "done" {
		return
	}
	wasRunning := rm.status == "running"
	rm.status = "done"
	for _, stop := range rm.timers {
		stop()
	}
	rm.timers = nil
	if rm.ticker != nil {
		rm.ticker()
		rm.ticker = nil
	}
	if rm.clock != nil {
		rm.clock()
		rm.clock = nil
	}
	if wasRunning {
		rm.rounds = rm.round
	}
	rows := m.rows(rm)
	for _, mem := range rm.live() {
		mem.ready = false
		msg := map[string]any{"t": "mayhem_end", "room": rm.id, "round": rm.round, "target": rm.target, "winner": nil,
			"why": why, "players": rows, "played": mem.playing && wasRunning}
		if winner != nil {
			msg["winner"] = *winner
		}
		if oc := m.h.ClientLocked(mem.ID); oc != nil {
			m.h.SendLocked(oc, msg)
		}
		pid := mem.ID
		m.stopAutoLeave(mem)
		mem.autoLeave = m.h.After(mayhemAutoLeave, func() { m.idle(rm, pid) })
	}
	if winner != nil {
		log.Printf("mayhem %s round %d: %s won at %d", rm.id, rm.round, winner.Name, rm.target)
		m.reward(rm, *winner)
	} else {
		log.Printf("mayhem %s round %d: over (%s)", rm.id, rm.round, why)
	}
	if len(rm.live()) == 0 {
		m.forget(rm)
	}
}

// reward pays the winner: the currants through the site's ledger, then mayhem_reward until acked.
func (m *mayhem) reward(rm *mayhemRoom, w mayhemRef) {
	id := fmt.Sprintf("mayhem-%s-%d-%d", rm.id, rm.round, w.ID)
	msg := map[string]any{"t": "mayhem_reward", "id": id, "room": rm.id, "round": rm.round, "xp": m.a.Reward.XP, "currants": m.a.Reward.Currants}
	if m.ledger == nil || m.a.Reward.Currants <= 0 {
		if m.ledger == nil && !m.trusted {
			msg["currants"] = int64(0) // no ledger to put them in
		}
		m.owe(w.ID, id, msg)
		return
	}
	b, _ := json.Marshal(msg)
	c := &mayhemCredit{ID: id, Player: w.ID, Msg: b}
	m.credits[id] = c
	m.save()
	m.askCredit(c)
}

func (m *mayhem) askCredit(c *mayhemCredit) {
	c.busy = true
	site := m.ledger
	id, player := c.ID, c.Player
	got := tradeGoods{Items: []tradeItem{}, Currants: m.a.Reward.Currants}
	m.spawn(func() {
		a := site.mayhem(id, player, got)
		m.h.After(0, func() { m.credited(c, a) })
	})
}

func (m *mayhem) credited(c *mayhemCredit, a ledgerAnswer) {
	c.busy = false
	if m.credits[c.ID] != c {
		return
	}
	var msg map[string]any
	json.Unmarshal(c.Msg, &msg)
	switch a.Status {
	case "settled":
	case "refused", "void":
		log.Printf("mayhem reward %s: the ledger said %s; sent without currants", c.ID, a.Status)
		msg["currants"] = int64(0)
	default:
		c.tries++
		if c.tries <= 6 {
			wait := min(time.Minute, time.Second<<c.tries)
			log.Printf("mayhem reward %s: no ledger answer (try %d): again in %s", c.ID, c.tries, wait)
			m.h.After(wait, func() {
				if m.credits[c.ID] == c && !c.busy {
					m.askCredit(c)
				}
			})
			return
		}
		log.Printf("mayhem reward %s: no ledger answer after %d tries; sent without currants", c.ID, c.tries)
		msg["currants"] = int64(0)
	}
	delete(m.credits, c.ID)
	m.owe(c.Player, c.ID, msg)
}

func (m *mayhem) owe(player int64, id string, msg map[string]any) {
	for _, o := range m.owed[player] {
		if o.ID == id {
			return
		}
	}
	b, _ := json.Marshal(msg)
	list := append(m.owed[player], mayhemOwed{ID: id, Msg: b})
	if len(list) > mayhemMaxOwed {
		list = list[len(list)-mayhemMaxOwed:]
	}
	m.owed[player] = list
	m.save()
	if cl := m.h.ClientLocked(player); cl != nil {
		m.h.pushLocked(cl, b)
	}
}

func (m *mayhem) ack(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(frame, &msg) != nil || !mayhemIDRe.MatchString(msg.ID) {
		return
	}
	list := m.owed[cl.ID()]
	for i, o := range list {
		if o.ID == msg.ID {
			list = append(list[:i:i], list[i+1:]...)
			if len(list) == 0 {
				delete(m.owed, cl.ID())
			} else {
				m.owed[cl.ID()] = list
			}
			m.save()
			return
		}
	}
}

// —— Persistence (-mayhem-file, as -crowns-file) ——

func (m *mayhem) useFile(path string) {
	m.path = path
	if path == "" {
		return
	}
	var st mayhemSaved
	ok, err := readStateFile("mayhem", path, &st)
	if err != nil {
		log.Printf("mayhem %s: %v (rewards are kept in memory only)", path, err)
		m.path = ""
		return
	}
	if ok {
		m.keep(st)
	}
}

func (m *mayhem) keep(st mayhemSaved) {
	for player, list := range st.Owed {
		kept := []mayhemOwed{}
		for _, o := range list {
			if player > 0 && mayhemIDRe.MatchString(o.ID) && json.Valid(o.Msg) {
				kept = append(kept, o)
			}
		}
		if len(kept) > 0 {
			m.owed[player] = kept
		}
	}
	for id, c := range st.Credits {
		if c != nil && c.ID == id && c.Player > 0 && mayhemIDRe.MatchString(id) && json.Valid(c.Msg) {
			m.credits[id] = c
		}
	}
}

func (m *mayhem) save() bool {
	if m.path == "" {
		return true
	}
	b, err := json.Marshal(mayhemSaved{Owed: m.owed, Credits: m.credits})
	if err == nil {
		err = writeFileSynced(m.path, b)
	}
	if err != nil {
		log.Printf("mayhem %s: %v", m.path, err)
		return false
	}
	return true
}

// —— Leaving ——

func (m *mayhem) forget(rm *mayhemRoom) {
	if m.rooms[rm.id] != rm {
		return
	}
	for _, stop := range rm.timers {
		stop()
	}
	for _, stop := range []func() bool{rm.ticker, rm.clock, rm.life} {
		if stop != nil {
			stop()
		}
	}
	for _, mem := range rm.members {
		if mem.grace != nil {
			mem.grace()
		}
		m.stopAutoLeave(mem)
		if m.roomOf[mem.ID] == rm {
			delete(m.roomOf, mem.ID)
		}
	}
	delete(m.rooms, rm.id)
}

// close ends a game that has run for mayhemMaxLife and lets everyone go.
func (m *mayhem) close(rm *mayhemRoom) {
	if m.rooms[rm.id] != rm {
		return
	}
	if rm.status != "done" {
		m.end(rm, nil, "timeout")
	}
	for _, mem := range rm.live() {
		if oc := m.h.ClientLocked(mem.ID); oc != nil {
			m.h.SendLocked(oc, map[string]any{"t": "mayhem_removed", "room": rm.id, "text": "This game is over."})
		}
	}
	m.forget(rm)
}

// drop takes a member out (global_game.js playerLeft 816-818: their number goes, the game goes on).
func (m *mayhem) drop(rm *mayhemRoom, id int64) {
	mem := rm.member(id)
	if mem == nil || mem.gone {
		return
	}
	wasPlaying := mem.playing
	mem.gone, mem.ready, mem.playing = true, false, false
	if mem.grace != nil {
		mem.grace()
		mem.grace = nil
	}
	m.stopAutoLeave(mem)
	if m.roomOf[id] == rm {
		delete(m.roomOf, id)
	}
	for key := range rm.touched {
		if key[0] == id || key[1] == id {
			delete(rm.touched, key)
		}
	}
	if len(rm.live()) == 0 {
		if rm.status == "running" || rm.status == "countdown" {
			rm.status = "done"
		}
		m.forget(rm)
		return
	}
	switch rm.status {
	case "running":
		if wasPlaying && m.playingCount(rm) < 2 {
			m.end(rm, nil, "short")
			return
		}
		m.sendRoom(rm, m.stateJSON(rm, "mayhem_state"), 0)
	case "countdown":
		if wasPlaying && m.playingCount(rm) < 2 {
			m.end(rm, nil, "short")
			return
		}
		m.sendRoom(rm, m.playersMsg(rm, mem.Name+" left."), 0)
	case "soon":
		if len(m.readyIDs(rm)) < m.a.Players.Min {
			for _, stop := range rm.timers {
				stop()
			}
			rm.timers = nil
			rm.status = "waiting"
			m.sendRoom(rm, m.playersMsg(rm, mayhemMustWait), 0)
			return
		}
		m.sendRoom(rm, m.playersMsg(rm, mem.Name+" left."), 0)
	case "waiting":
		m.sendRoom(rm, m.playersMsg(rm, mem.Name+" left."), 0)
		// Someone who held the start up may be gone.
		if len(m.readyIDs(rm)) > 0 {
			m.check(rm, "")
		}
	default:
		m.sendRoom(rm, m.playersMsg(rm, mem.Name+" left."), 0)
	}
}

// idle is games_auto_leave (inc_games.js 2301-2306).
func (m *mayhem) idle(rm *mayhemRoom, id int64) {
	mem := rm.member(id)
	if mem == nil || mem.gone || mem.ready || (rm.status != "waiting" && rm.status != "done") {
		return
	}
	if oc := m.h.ClientLocked(id); oc != nil {
		m.h.SendLocked(oc, map[string]any{"t": "mayhem_removed", "room": rm.id, "text": mayhemRemoved})
	}
	m.drop(rm, id)
}

func (m *mayhem) leave(cl *Client, frame json.RawMessage) {
	if rm, _ := m.roomFor(cl, frame); rm != nil {
		m.drop(rm, cl.ID())
	}
}

// —— Hooks ——

func (m *mayhem) onConnect(cl *Client) {
	for _, o := range m.owed[cl.ID()] {
		m.h.SendLocked(cl, o.Msg)
	}
	if m.ledger != nil {
		for _, c := range m.credits {
			if c.Player == cl.ID() && !c.busy {
				m.askCredit(c)
			}
		}
	}
}

func (m *mayhem) onJoin(cl *Client) {
	id := cl.ID()
	rm := m.roomOf[id]
	if rm == nil {
		return
	}
	mem := rm.member(id)
	if cl.Street() != rm.street {
		// Off to another street: out of the game (instance.js player_exited_location).
		m.drop(rm, id)
		return
	}
	if mem != nil && mem.grace != nil {
		mem.grace()
		mem.grace = nil
	}
	// In the arena: what the game looks like now.
	switch rm.status {
	case "running":
		if mem != nil && mem.playing {
			mem.lastX, mem.lastY = cl.Pos()
		}
		m.h.SendLocked(cl, m.stateJSON(rm, "mayhem_state"))
	case "waiting", "soon":
		m.h.SendLocked(cl, m.playersMsg(rm, ""))
	}
}

func (m *mayhem) onLeave(cl *Client, street string) {
	if rm := m.roomOf[cl.ID()]; rm != nil && street == rm.street {
		m.startGrace(rm, cl.ID())
	}
}

func (m *mayhem) startGrace(rm *mayhemRoom, id int64) {
	mem := rm.member(id)
	if mem == nil || mem.gone || mem.grace != nil {
		return
	}
	mem.grace = m.h.After(mayhemGrace, func() {
		mem.grace = nil
		m.drop(rm, id)
	})
}

func (m *mayhem) onDisconnect(cl *Client) {
	id := cl.ID()
	delete(m.asks, id)
	if rm := m.roomOf[id]; rm != nil {
		m.startGrace(rm, id)
	}
}
