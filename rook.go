package main

// Group Rook attacks (multiplayer phase 4): a Rook over a street is one shared
// event that everyone on the street fights together, as in the original. The
// server owns each street's attack: when it starts, its phase timeline, the
// Rook's health and stun, every Focusing Orb and primed shrine, the wing beats
// and what they rook, the outcome, each player's contribution and the
// aftermath (which critters and trees are rooked, and the revive doses given).
// Each player's own rewards and costs (iMG, energy, mood, favor, offerings out
// of the pack) are sent to that player and applied by their client.
//
// The rules are GameServerJS groups/rook_attack.js, locations/inc_rook.js,
// items/include/rook.js, items/focusing_orb.js stun_attack and
// items/include/shrines.js + npc_shrine_*.js prime / donate_to (cited below).
//
// Client to server:
//
//	{"t":"rook_sync","keys":["npc_piggy:0","trant_bean:2"]}  after a join: the street's attack, please;
//	                                       keys are the rookable things this client has here
//	{"t":"rook_orb"}                        start a stun attack (Focusing Orb)
//	{"t":"rook_orb_join","by":7}            focus your imagination on player 7's orb
//	{"t":"rook_prime","bonus":1}            prime the street's shrine (bonus: your Shrine On card, 0-2)
//	{"t":"rook_strike","item":"plank","count":40}  offer to the primed shrine
//	{"t":"rook_revive","key":"npc_piggy:0","balm":false}  one revive dose (balm: a full revive)
//	{"t":"rook_dev","do":"start"}           start | attack | wingbeat | stun; only with GRODDLE_ROOK_DEV=1
//
// Server to client:
//
//	{"t":"rook","street":"GA5…","snap":true,"attack":Attack|null,"rooked":{key:Rooked}}
//	                                        the answer to rook_sync, and every change to everyone on the
//	                                        street, with "ev":{"type":"…",…} naming what happened
//	{"t":"rook_you","why":"strike","xp":40,"energy":0,"mood":0,"favor":0,…}  your own share, to apply
//	{"t":"rook_no","what":"orb","code":"…","message":"…"}  a refusal
//
// Attack: {"phase":"herald|build_up|angry|stunned|dead|departed","angry":"angry1|angry2|angry3",
// "health","maxHealth","stun","maxStun","crowd","wingBeats","clock":ms to the phase's next turn,
// "orbs":[{"by":7,"name":"Ada","joiners":[8],"in":ms}],"primed":{"by":7,"name":"Ada","in":ms}|null,
// "shrine":{"x":865.6,"y":940.6,"giant":"spriggan"}}.
// Rooked: {"class":"npc_piggy","doses":1,"by":[7]}.
//
// ev types: herald_warning{n}, attack_start, angry2, angry3, wing_beat{n,hit:[keys]}, depart,
// orb_start{by,name}, orb_join{by,id,name,count}, orb_cancel{by}, stun{by,name,joiners,damage,
// stunned,first,text}, stun_failed{by,name,broke,first,text}, stun_recovery, primed{by,name},
// prime_end{by}, strike{by,name,damage,beam,health,text}, defeated, clear{defeated},
// revive{key,by,name,doses,done,balm}, rooked{n} (a sync added keys after a wing beat: none yet).
//
// rook_you why: orb_start{energy}, orb_join{energy,xp}, stun_lead{xp}, stun_assist{xp,damage},
// stun_fail{xp,mood,broke}, wing_beat{energy,mood}, prime{energy}, prime_done{xp,damage},
// strike{xp,damage,item,count}, defeat_help{xp}, defeat_witness{xp},
// revive{xp,energy,mood,favor,key,doses,done,balm}. Rook Armor is the player's own buff, so the
// client applies it to the costs (focusing_orb.js:590, 609; npc_shrine_*.js prime 216;
// include/rook.js 15-21; inc_rook.js:794).
//
// What is the remake's own, not the source's (players are few; the source fight was tuned for a
// hub-wide crowd and its rook_attack group ran until somebody killed the Rook):
//   - The Rook's health and full stun scale with the crowd on the street when it arrives (and grow
//     when more arrive): health 100 per player up to the source default 1000 at ten
//     (groups/rook_attack.js:105-109; config_prod.js:62 rook_attack_strength 5000 was the hub-wide
//     prod figure), full stun five orbs of the biggest orb the crowd can make, 1000 at seven or more
//     (rook_attack.js:15). The source has no such scaling.
//   - A 30 s herald (the Forehorseman, items/npc_forehorseman.js, and the 2011 "Der Rook iz Coming!"
//     warning) comes before the Rook, as in solo.
//   - After three wing beats the Rook flies off, and a recovery from a stun re-arms the 10-minute
//     wing beat (rook_attack.js stunRecovery 466-473 leaves none), so an abandoned fight still ends.
//   - Attacks come to a street with someone on it (newLocation 30-66 picks any rookable street of
//     the hub), and a street rests an hour after one before the Rook can come back (the source only
//     skips the last location, line 50).
//   - An offering too small to hurt the Rook is refused and kept (shrines.js launchAttack 1036-1038
//     would take it for nothing).
//
// The server forgets everything when it restarts (accepted for now).

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed data/rook.json
var rookJSON []byte

const (
	// admin.js createRookAttack 398: setSchedule(60, 60) minutes by default.
	rookScheduleMin = 60 * time.Minute
	rookScheduleMax = 60 * time.Minute
	// Ours: a street rests this long after an attack clears.
	rookStreetRest = 60 * time.Minute

	rookHerald     = 30 * time.Second // ours (js/rook-attack.js ROOK_SOLO.heraldMs)
	rookHeraldWarn = 15 * time.Second // ours (ROOK_SOLO.heraldWarnEveryMs)

	rookWingBeat      = 10 * time.Minute // rook_attack.js:115, 198
	rookAngryWingBeat = 2 * time.Minute  // doAngry 401
	rookAngry2After   = 60 * time.Second // doAngry 402
	rookAngry3After   = 30 * time.Second // nextAngryState 385
	rookStunRecovery  = 2 * time.Minute  // doStunned 413
	rookClearAfter    = 15 * time.Second // completeAttack 305
	rookFailWingBeat  = 1 * time.Second  // completeStun 365
	rookMaxFailures   = 5                // setup 16
	rookMaxWingBeats  = 3                // ours (ROOK_SOLO.maxWingBeats)
	rookDepartClear   = 6 * time.Second  // ours (ROOK_SOLO.departMs)
	rookOrbWindow     = 15 * time.Second // focusing_orb.js stun_attack 604
	rookMaxOrbs       = 3                // inc_rook.js addOrb 337
	rookOrbBreak      = 9                // orbAddPlayer 586, 600
	rookPrimeTime     = 10 * time.Second // shrines.js primeStart 930
	rookStrikeEvery   = 3 * time.Second  // shrineAttack 967: the donate overlay comes back after 3 s
	rookTunedCrowd    = 10               // ours: the crowd the source numbers were tuned for
	rookReachX        = 500              // how close to the shrine a primer or striker stands
	rookReachY        = 400
	rookMaxKeys       = 128 // rookable keys remembered per street
	rookMaxCount      = 999 // one offering's count

	rookStunEnergy   = 50  // focusing_orb.js stun_attack 607
	rookJoinEnergy   = 25  // onOverlayClicked 1720
	rookJoinXp       = 10  // 1722: under 8 joined
	rookAssistXp     = 25  // inc_rook.js 762-763
	rookFailXp       = 5   // 792
	rookFailMood     = 5   // 795
	rookPrimeEnergy  = 10  // npc_shrine_*.js prime 216
	rookEmblemDamage = 250 // shrines.js launchAttack 1036
	rookWingEnergy   = 20  // include/rook.js 15-21 (the comment's 20; the code's armor test is inverted)
	rookWingMood     = 20
	rookChance       = 0.5 // include/rook.js:35
	rookWitnessXp    = 50  // inc_rook.js:201
	rookDefeatBaseXp = 50  // 198: damage * 2 + 50
	rookReviveXp     = 25  // include/rook.js doReviveComplete 157
	rookBalmXp       = 200 // 166 force_full_revive
	rookReviveFinal  = 25  // 194
	rookReviveFavor  = 10  // 199
	rookReviveEnergy = 25  // 187
	rookReviveMood   = 25  // 188
	rookHealers      = 3   // 178, canRevive 365-372

	rookBurst = 6.0
	rookEvery = 4.0
)

// rookStun is doOrbAttack's table (inc_rook.js 713-737): joiners -> stun, initiator iMG.
func rookStun(joiners int) (damage, xp int) {
	switch {
	case joiners == 1:
		return 25, 10
	case joiners == 2:
		return 40, 20
	case joiners == 3:
		return 60, 20
	case joiners == 4:
		return 85, 30
	case joiners == 5:
		return 130, 30
	case joiners >= 6 && joiners <= 8:
		return 200, 50
	}
	return 0, 0
}

// rookScale is the remake's crowd scaling (header): max health and max stun for n players.
func rookScale(n int) (health, stun int) {
	n = max(1, min(n, rookTunedCrowd))
	health = 1000 * n / rookTunedCrowd
	d, _ := rookStun(min(n-1, 8))
	stun = min(1000, max(125, 5*d))
	return
}

// Rookable classes (classes that include items/include/rook.js; players excluded, rook_wrath was retired).
var rookableClasses = map[string]bool{
	"trant_fruit": true, "trant_egg": true, "trant_bean": true, "trant_bubble": true, "trant_spice": true, "trant_gas": true,
	"wood_tree": true, "paper_tree": true, "npc_piggy": true, "npc_chicken": true, "npc_butterfly": true, "npc_kitty_chicken": true,
}

var (
	rookKeyRe  = regexp.MustCompile(`^([a-z_]{1,24}):(\d{1,3})$`)
	rookItemRe = regexp.MustCompile(`^[a-z0-9_]{1,48}$`)
)

// ---------------------------------------------------------------------------
// Data (data/rook.json, tools/build-realtime-rook.mjs)
// ---------------------------------------------------------------------------

type rookSpot struct {
	hub   string
	x, y  float64
	giant string
}

type rookWorld struct {
	streets map[string]rookSpot
	hubs    map[string][]string
	values  map[string]int
	refused map[string]bool
}

func loadRookWorld(raw []byte) (*rookWorld, error) {
	var file struct {
		Streets map[string][]any `json:"streets"`
		Values  map[string]int   `json:"values"`
		Refused []string         `json:"refused"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	w := &rookWorld{streets: map[string]rookSpot{}, hubs: map[string][]string{}, values: file.Values, refused: map[string]bool{}}
	for tsid, row := range file.Streets {
		if len(row) < 4 {
			return nil, fmt.Errorf("%s: bad street %v", tsid, row)
		}
		hub, ok1 := row[0].(string)
		x, ok2 := row[1].(float64)
		y, ok3 := row[2].(float64)
		giant, ok4 := row[3].(string)
		if !(ok1 && ok2 && ok3 && ok4) || giant == "" {
			return nil, fmt.Errorf("%s: bad street %v", tsid, row)
		}
		w.streets[tsid] = rookSpot{hub: hub, x: x, y: y, giant: giant}
		w.hubs[hub] = append(w.hubs[hub], tsid)
	}
	for _, list := range w.hubs {
		sort.Strings(list)
	}
	for _, id := range file.Refused {
		w.refused[id] = true
	}
	return w, nil
}

var rookData *rookWorld

func init() {
	w, err := loadRookWorld(rookJSON)
	if err != nil {
		panic("realtime: data/rook.json: " + err.Error())
	}
	rookData = w
	modules = append(modules, newRook)
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

type rookOrb struct {
	by      int64
	name    string
	joiners []int64
	fireAt  time.Time
	stop    func() bool
}

type rookPrime struct {
	by    int64
	name  string
	bonus float64
	until time.Time
	total int
	stop  func() bool
}

type rookAttack struct {
	street     string
	phase      string
	angry      string
	health     int
	maxHealth  int
	stun       int
	maxStun    int
	crowd      int
	failures   int
	wingBeats  int
	warnings   int
	clockAt    time.Time // the phase's next turn, for clients' clocks
	wingAt     time.Time
	timers     map[string]*rookTimer
	orbs       map[int64]*rookOrb
	inOrb      map[int64]int64 // player -> the initiator of the orb they are in
	primed     *rookPrime
	attackers  map[int64]int // strike damage per player (completeAttack 290-298)
	lastStrike map[int64]time.Time
}

// rookTimer is one named timer of an attack; a fired timer runs only while it is still the one named.
type rookTimer struct{ stop func() bool }

type rookedThing struct {
	class   string
	healers []int64
}

type rookStreet struct {
	attack    *rookAttack
	rooked    map[string]*rookedThing
	keys      map[string]string // key -> class, as clients report them
	restUntil time.Time
}

type rookHub struct {
	timer *rookTimer
	last  string // newLocation 50: never the last location again
}

type rook struct {
	h       *Hub
	w       *rookWorld
	roll    func() float64
	dev     bool
	streets map[string]*rookStreet
	hubs    map[string]*rookHub
	limits  map[int64]*objBucket
}

func newRook(h *Hub) Module {
	m := &rook{h: h, w: rookData, roll: rand.Float64, dev: os.Getenv("GRODDLE_ROOK_DEV") == "1",
		streets: map[string]*rookStreet{}, hubs: map[string]*rookHub{}, limits: map[int64]*objBucket{}}
	if m.dev {
		log.Printf("rook: GRODDLE_ROOK_DEV=1, rook_dev messages are on (QA only)")
	}
	return m
}

func (m *rook) Register(r *Registry) {
	r.Handle("rook_sync", m.handleSync)
	r.Handle("rook_orb", m.handleOrb)
	r.Handle("rook_orb_join", m.handleOrbJoin)
	r.Handle("rook_prime", m.handlePrime)
	r.Handle("rook_strike", m.handleStrike)
	r.Handle("rook_revive", m.handleRevive)
	r.Handle("rook_dev", m.handleDev)
	r.OnJoin(m.joined)
	r.OnLeave(m.left)
	r.OnDisconnect(func(cl *Client) { delete(m.limits, cl.ID()) })
}

func (m *rook) allow(cl *Client) bool {
	now := m.h.Now()
	b := m.limits[cl.ID()]
	if b == nil {
		b = &objBucket{tokens: rookBurst, at: now}
		m.limits[cl.ID()] = b
	}
	b.tokens = math.Min(rookBurst, b.tokens+now.Sub(b.at).Seconds()*rookEvery)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (m *rook) street(tsid string) *rookStreet {
	s := m.streets[tsid]
	if s == nil {
		s = &rookStreet{rooked: map[string]*rookedThing{}, keys: map[string]string{}}
		m.streets[tsid] = s
	}
	return s
}

// tidy forgets a street with nothing left to remember.
func (m *rook) tidy(tsid string) {
	s := m.streets[tsid]
	if s != nil && s.attack == nil && len(s.rooked) == 0 && !s.restUntil.After(m.h.Now()) && len(m.h.StreetLocked(tsid)) == 0 {
		delete(m.streets, tsid)
	}
}

func (m *rook) here(cl *Client) (*rookStreet, *rookAttack) {
	s := m.streets[cl.Street()]
	if s == nil {
		return nil, nil
	}
	return s, s.attack
}

func (m *rook) no(cl *Client, what, code, message string) {
	m.h.SendLocked(cl, map[string]any{"t": "rook_no", "what": what, "code": code, "message": message})
}

func (m *rook) you(cl *Client, why string, fields map[string]any) {
	msg := map[string]any{"t": "rook_you", "why": why, "street": cl.Street()}
	for k, v := range fields {
		msg[k] = v
	}
	m.h.SendLocked(cl, msg)
}

func (m *rook) youID(id int64, street, why string, fields map[string]any) {
	if cl := m.h.ClientLocked(id); cl != nil && cl.Street() == street {
		m.you(cl, why, fields)
	}
}

func rookMsLeft(now, t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return max(1, t.Sub(now).Milliseconds())
}

func (m *rook) attackState(a *rookAttack) map[string]any {
	if a == nil {
		return nil
	}
	now := m.h.Now()
	orbs := make([]map[string]any, 0, len(a.orbs))
	for _, o := range a.orbs {
		j := append([]int64{}, o.joiners...)
		orbs = append(orbs, map[string]any{"by": o.by, "name": o.name, "joiners": j, "in": rookMsLeft(now, o.fireAt)})
	}
	sort.Slice(orbs, func(i, j int) bool { return orbs[i]["by"].(int64) < orbs[j]["by"].(int64) })
	var primed any
	if p := a.primed; p != nil {
		primed = map[string]any{"by": p.by, "name": p.name, "in": rookMsLeft(now, p.until)}
	}
	spot := m.w.streets[a.street]
	return map[string]any{
		"phase": a.phase, "angry": a.angry, "health": a.health, "maxHealth": a.maxHealth,
		"stun": min(a.stun, a.maxStun), "maxStun": a.maxStun, "crowd": a.crowd, "wingBeats": a.wingBeats,
		"clock": rookMsLeft(now, a.clockAt), "orbs": orbs, "primed": primed,
		"shrine": map[string]any{"x": spot.x, "y": spot.y, "giant": spot.giant},
	}
}

func (m *rook) rookedState(s *rookStreet) map[string]any {
	out := map[string]any{}
	if s == nil {
		return out
	}
	for key, r := range s.rooked {
		out[key] = map[string]any{"class": r.class, "doses": len(r.healers), "by": append([]int64{}, r.healers...)}
	}
	return out
}

func (m *rook) message(tsid string, ev map[string]any) map[string]any {
	s := m.streets[tsid]
	var a *rookAttack
	if s != nil {
		a = s.attack
	}
	msg := map[string]any{"t": "rook", "street": tsid, "attack": m.attackState(a), "rooked": m.rookedState(s)}
	if ev != nil {
		msg["ev"] = ev
	}
	return msg
}

func (m *rook) broadcast(tsid string, ev map[string]any) {
	m.h.BroadcastStreetLocked(tsid, m.message(tsid, ev), 0)
}

// ---------------------------------------------------------------------------
// Timers
// ---------------------------------------------------------------------------

// after runs fn under the lock in d, as long as a is still the street's attack.
func (m *rook) after(a *rookAttack, name string, d time.Duration, fn func()) {
	m.cancel(a, name)
	t := &rookTimer{}
	a.timers[name] = t
	t.stop = m.h.After(d, func() {
		s := m.streets[a.street]
		if s == nil || s.attack != a || a.timers[name] != t {
			return
		}
		delete(a.timers, name)
		fn()
	})
}

func (m *rook) cancel(a *rookAttack, name string) {
	if t := a.timers[name]; t != nil {
		t.stop()
		delete(a.timers, name)
	}
}

// scheduleWingBeat is rook_attack.js 168-184: a sooner wing beat replaces a later one.
func (m *rook) scheduleWingBeat(a *rookAttack, d time.Duration) {
	at := m.h.Now().Add(d)
	if a.timers["wing"] != nil && !at.Before(a.wingAt) {
		return
	}
	a.wingAt = at
	m.after(a, "wing", d, func() { m.wingBeat(a) })
}

func (m *rook) cancelWingBeat(a *rookAttack) {
	m.cancel(a, "wing")
	a.wingAt = time.Time{}
}

// ---------------------------------------------------------------------------
// Schedule (groups/rook_attack.js setAttackTimer 79-97, newLocation 30-66)
// ---------------------------------------------------------------------------

func (m *rook) armHub(hub string) {
	hb := m.hubs[hub]
	if hb == nil {
		hb = &rookHub{}
		m.hubs[hub] = hb
	}
	if hb.timer != nil {
		return
	}
	d := rookScheduleMin
	if rookScheduleMax > rookScheduleMin {
		d += time.Duration(m.roll() * float64(rookScheduleMax-rookScheduleMin))
	}
	t := &rookTimer{}
	hb.timer = t
	t.stop = m.h.After(d, func() {
		if hb.timer != t {
			return
		}
		hb.timer = nil
		m.hubFires(hub)
	})
}

// hubFires picks a street of the hub for the next attack, or waits for the next turn.
func (m *rook) hubFires(hub string) {
	hb := m.hubs[hub]
	now := m.h.Now()
	var candidates []string
	anyone := false
	busy := false
	for _, tsid := range m.w.hubs[hub] {
		if len(m.h.StreetLocked(tsid)) == 0 {
			continue
		}
		anyone = true
		s := m.streets[tsid]
		if s != nil && s.attack != nil {
			busy = true
			continue
		}
		if tsid == hb.last || (s != nil && s.restUntil.After(now)) {
			continue
		}
		candidates = append(candidates, tsid)
	}
	if busy {
		return // the hub's attack re-arms the schedule when it clears
	}
	if len(candidates) > 0 {
		tsid := candidates[min(len(candidates)-1, int(m.roll()*float64(len(candidates))))]
		m.start(tsid, true)
		return
	}
	if anyone {
		m.armHub(hub)
	}
	// Nobody in the hub: the next arrival arms it again.
}

// start begins an attack on a street (the herald first, unless herald is false).
func (m *rook) start(tsid string, herald bool) *rookAttack {
	s := m.street(tsid)
	if s.attack != nil {
		return nil
	}
	a := &rookAttack{street: tsid, timers: map[string]*rookTimer{}, orbs: map[int64]*rookOrb{}, inOrb: map[int64]int64{},
		attackers: map[int64]int{}, lastStrike: map[int64]time.Time{}}
	s.attack = a
	if m.dev {
		log.Printf("rook: attack on %s (herald %v, %d there)", tsid, herald, len(m.h.StreetLocked(tsid)))
	}
	if hb := m.hubs[m.w.streets[tsid].hub]; hb != nil {
		hb.last = tsid
		if hb.timer != nil {
			hb.timer.stop()
			hb.timer = nil
		}
	}
	if !herald {
		m.arrive(a, nil)
		return a
	}
	a.phase = "herald"
	a.clockAt = m.h.Now().Add(rookHerald)
	m.after(a, "attack", rookHerald, func() { m.arrive(a, map[string]any{"type": "attack_start"}) })
	m.heraldWarning(a)
	return a
}

func (m *rook) heraldWarning(a *rookAttack) {
	a.warnings++
	m.broadcast(a.street, map[string]any{"type": "herald_warning", "n": a.warnings})
	if left := a.clockAt.Sub(m.h.Now()); left > rookHeraldWarn {
		m.after(a, "warn", rookHeraldWarn, func() { m.heraldWarning(a) })
	}
}

// arrive is startAttack 99-128: the Rook over the street in build_up, health at full.
func (m *rook) arrive(a *rookAttack, ev map[string]any) {
	m.cancel(a, "warn")
	a.crowd = max(1, len(m.h.StreetLocked(a.street)))
	a.maxHealth, a.maxStun = rookScale(a.crowd)
	a.health, a.stun, a.phase = a.maxHealth, 0, "build_up"
	m.scheduleWingBeat(a, rookWingBeat)
	a.clockAt = a.wingAt
	if ev == nil {
		ev = map[string]any{"type": "attack_start"}
	}
	m.broadcast(a.street, ev)
}

// grow raises the Rook's strength when the crowd on its street grows (ours, header).
func (m *rook) grow(a *rookAttack) {
	if a.phase != "build_up" && a.phase != "angry" && a.phase != "stunned" {
		return
	}
	n := len(m.h.StreetLocked(a.street))
	if n <= a.crowd || a.crowd >= rookTunedCrowd {
		return
	}
	a.crowd = min(n, rookTunedCrowd)
	h, st := rookScale(a.crowd)
	a.health += h - a.maxHealth
	a.maxHealth = h
	a.maxStun = st
	if a.phase == "stunned" {
		a.stun = st
	}
}

// ---------------------------------------------------------------------------
// The attack's own turns
// ---------------------------------------------------------------------------

// stateChange is inc_rook.js rookStateChange 173-215: orbs fold, and on build_up or dead the
// shrines close (a primed one completes, shrines.js closeRookAttack 741-748).
func (m *rook) stateChange(a *rookAttack, state string) {
	for by := range a.orbs {
		m.dropOrb(a, by)
	}
	if state == "build_up" || state == "dead" {
		m.primeComplete(a)
	}
}

// wingBeat is doWingBeat 194-236 with rookWingBeat 248-278.
func (m *rook) wingBeat(a *rookAttack) {
	if a.phase != "build_up" && a.phase != "angry" {
		return
	}
	m.cancelWingBeat(a)
	m.scheduleWingBeat(a, rookWingBeat)
	m.stateChange(a, "build_up")
	s := m.streets[a.street]
	// Each rookable thing has a 50% chance (include/rook.js:35); one already rooked stays as it is.
	keys := make([]string, 0, len(s.keys))
	for key := range s.keys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hit := []string{}
	for _, key := range keys {
		if s.rooked[key] != nil {
			continue
		}
		if m.roll() < rookChance {
			s.rooked[key] = &rookedThing{class: s.keys[key]}
			hit = append(hit, key)
		}
	}
	a.stun, a.phase, a.angry, a.failures = 0, "build_up", "", 0
	m.cancel(a, "angry")
	a.wingBeats++
	// Every player there loses 20 energy and 20 mood (include/rook.js 15-21).
	for _, cl := range m.h.StreetLocked(a.street) {
		m.you(cl, "wing_beat", map[string]any{"energy": -rookWingEnergy, "mood": -rookWingMood})
	}
	ev := map[string]any{"type": "wing_beat", "n": a.wingBeats, "hit": hit}
	if a.wingBeats >= rookMaxWingBeats {
		m.cancelWingBeat(a)
		a.phase = "departed"
		a.clockAt = m.h.Now().Add(rookDepartClear)
		m.after(a, "clear", rookDepartClear, func() { m.clear(a) })
		m.broadcast(a.street, ev)
		m.broadcast(a.street, map[string]any{"type": "depart"})
		return
	}
	a.clockAt = a.wingAt
	m.broadcast(a.street, ev)
}

// angryOn is doAngry 396-403.
func (m *rook) angryOn(a *rookAttack) {
	a.phase, a.angry = "angry", "angry1"
	m.scheduleWingBeat(a, rookAngryWingBeat)
	a.clockAt = a.wingAt
	m.after(a, "angry", rookAngry2After, func() {
		if a.phase != "angry" {
			return
		}
		a.angry = "angry2"
		m.broadcast(a.street, map[string]any{"type": "angry2"})
		m.after(a, "angry", rookAngry3After, func() {
			if a.phase != "angry" {
				return
			}
			a.angry = "angry3" // "ATTACK IMMINENT!" (nextAngryState 389)
			m.broadcast(a.street, map[string]any{"type": "angry3"})
		})
	})
}

// stunnedOn is doStunned 405-416.
func (m *rook) stunnedOn(a *rookAttack) {
	a.phase, a.angry, a.stun = "stunned", "", a.maxStun
	m.cancelWingBeat(a)
	m.cancel(a, "angry")
	m.stateChange(a, "stunned")
	a.clockAt = m.h.Now().Add(rookStunRecovery)
	m.after(a, "recovery", rookStunRecovery, func() {
		// stunRecovery 466-473, plus our re-armed wing beat.
		a.phase, a.stun = "build_up", 0
		m.stateChange(a, "build_up")
		m.scheduleWingBeat(a, rookWingBeat)
		a.clockAt = a.wingAt
		m.broadcast(a.street, map[string]any{"type": "stun_recovery"})
	})
}

// clear is stopAttack 428-451 and rookClear 280-296: the street rests, the hub's schedule goes on.
func (m *rook) clear(a *rookAttack) {
	s := m.streets[a.street]
	if s == nil || s.attack != a {
		return
	}
	for name := range a.timers {
		m.cancel(a, name)
	}
	defeated := a.phase == "dead"
	if m.dev {
		log.Printf("rook: %s clear (defeated %v, wing beats %d, health %d/%d)", a.street, defeated, a.wingBeats, a.health, a.maxHealth)
	}
	s.attack = nil
	s.restUntil = m.h.Now().Add(rookStreetRest)
	m.broadcast(a.street, map[string]any{"type": "clear", "defeated": defeated})
	if hub := m.w.streets[a.street].hub; m.hubs[hub] != nil {
		m.armHub(hub)
	}
	m.tidy(a.street)
}

// ---------------------------------------------------------------------------
// Focusing Orbs (inc_rook.js addOrb 330-379, orbAddPlayer 583-619, doOrbAttack 684-812)
// ---------------------------------------------------------------------------

func rookCanStun(a *rookAttack) bool {
	return a != nil && (a.phase == "build_up" || a.phase == "angry")
}

func (m *rook) handleOrb(cl *Client, _ json.RawMessage) {
	if !m.allow(cl) {
		m.no(cl, "orb", "slow_down", "Slow down a little.")
		return
	}
	_, a := m.here(cl)
	if !rookCanStun(a) {
		m.no(cl, "orb", "wait", "You'll have to wait until the right time to unleash the awesome power of Martial Imagination.") // focusing_orb.js:585
		return
	}
	if _, in := a.inOrb[cl.ID()]; in {
		m.no(cl, "orb", "busy", "You are already focused on an orb.")
		return
	}
	if len(a.orbs) >= rookMaxOrbs {
		m.no(cl, "orb", "full", "Several people here are already trying to stun the Rook. Help them first!") // inc_rook.js:338
		return
	}
	o := &rookOrb{by: cl.ID(), name: cl.Name(), fireAt: m.h.Now().Add(rookOrbWindow)}
	a.orbs[o.by] = o
	a.inOrb[o.by] = o.by
	stop := m.h.After(rookOrbWindow, func() {
		if s := m.streets[a.street]; s == nil || s.attack != a || a.orbs[o.by] != o {
			return
		}
		m.fireOrb(a, o)
	})
	o.stop = stop
	m.you(cl, "orb_start", map[string]any{"energy": -rookStunEnergy})
	m.broadcast(a.street, map[string]any{"type": "orb_start", "by": o.by, "name": o.name})
}

func (m *rook) handleOrbJoin(cl *Client, frame json.RawMessage) {
	var msg struct {
		By int64 `json:"by"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.By < 1 {
		m.no(cl, "orb_join", "bad", "That orb message did not check out.")
		return
	}
	if !m.allow(cl) {
		m.no(cl, "orb_join", "slow_down", "Slow down a little.")
		return
	}
	_, a := m.here(cl)
	if a == nil || a.orbs[msg.By] == nil {
		m.no(cl, "orb_join", "gone", "That orb has already been released.")
		return
	}
	o := a.orbs[msg.By]
	// orbCanAddPlayer 576-581: not twice, not while in an orb of your own.
	if _, in := a.inOrb[cl.ID()]; in || msg.By == cl.ID() {
		m.no(cl, "orb_join", "busy", "You are already focused on an orb.")
		return
	}
	if len(o.joiners) >= rookOrbBreak {
		return
	}
	o.joiners = append(o.joiners, cl.ID())
	a.inOrb[cl.ID()] = o.by
	n := len(o.joiners)
	xp := 0
	if n < 8 {
		xp = rookJoinXp
	}
	m.you(cl, "orb_join", map[string]any{"energy": -rookJoinEnergy, "xp": xp, "by": o.by})
	m.broadcast(a.street, map[string]any{"type": "orb_join", "by": o.by, "id": cl.ID(), "name": cl.Name(), "count": n})
	if n >= rookOrbBreak {
		m.fireOrb(a, o) // pushed past the breaking point at once (600-603)
	}
}

// dropOrb is removeOrb 531-566: the orb and its players' focus are gone, nothing fires.
func (m *rook) dropOrb(a *rookAttack, by int64) {
	o := a.orbs[by]
	if o == nil {
		return
	}
	if o.stop != nil {
		o.stop()
	}
	delete(a.orbs, by)
	delete(a.inOrb, by)
	for _, id := range o.joiners {
		delete(a.inOrb, id)
	}
}

func (m *rook) fireOrb(a *rookAttack, o *rookOrb) {
	n := len(o.joiners)
	first := a.phase == "build_up" // doOrbAttack 691: FIRST STUN ATTEMPT!
	players := append([]int64{o.by}, o.joiners...)
	m.dropOrb(a, o.by)
	if n >= 1 && n <= 8 {
		damage, xp := rookStun(n)
		m.youID(o.by, a.street, "stun_lead", map[string]any{"xp": xp})
		for _, id := range players {
			m.youID(id, a.street, "stun_assist", map[string]any{"xp": rookAssistXp, "damage": damage})
		}
		m.completeStun(a, o, n, damage, first)
		return
	}
	broke := n > 8
	for _, id := range players {
		m.youID(id, a.street, "stun_fail", map[string]any{"xp": rookFailXp, "mood": -rookFailMood, "broke": broke})
	}
	ev := map[string]any{"type": "stun_failed", "by": o.by, "name": o.name, "broke": broke, "first": first}
	if broke {
		// 807-808: a broken orb counts as a failed stun (it angers the Rook); a lone one does not.
		m.completeStun(a, o, n, 0, first)
		return
	}
	ev["text"] = rookStunText(o.name, 0, 0, a.stun, a.maxStun)
	m.broadcast(a.street, ev)
}

// stunText is completeStun 336-348.
func rookStunText(label string, assists, damage, stun, maxStun int) string {
	assist := ""
	if assists > 0 {
		assist = "and " + rookPlural(assists, "other", "others")
	}
	attempt := "attempted to stun the Rook, but failed."
	if damage > 0 {
		attempt = fmt.Sprintf("led an attack against the Rook, stunning it for %d points", damage)
	}
	text := strings.Join(strings.Fields(label+" "+assist+" "+attempt), " ")
	if damage > 0 {
		if stun < maxStun {
			text += fmt.Sprintf(". %d remaining to stun the Rook.", maxStun-stun)
		} else {
			text += ", stunning it."
		}
	}
	return text
}

func rookPlural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// completeStun is rook_attack.js 327-375 (the source waits a second in processAttacks; this does not).
func (m *rook) completeStun(a *rookAttack, o *rookOrb, assists, damage int, first bool) {
	if !rookCanStun(a) {
		return
	}
	a.stun += damage
	text := rookStunText(o.name, assists, damage, min(a.stun, a.maxStun), a.maxStun)
	stunned := false
	if a.stun >= a.maxStun {
		damage -= a.stun - a.maxStun
		stunned = true
	}
	ev := map[string]any{"type": "stun", "by": o.by, "name": o.name, "joiners": assists, "damage": damage, "stunned": stunned, "first": first, "text": text}
	if damage == 0 {
		ev["type"] = "stun_failed"
		ev["broke"] = true
	}
	if stunned {
		m.stunnedOn(a)
	} else if a.phase != "angry" {
		m.angryOn(a)
	}
	if damage == 0 {
		a.failures++
		if a.failures >= rookMaxFailures {
			m.scheduleWingBeat(a, rookFailWingBeat)
			if a.phase == "angry" {
				a.clockAt = a.wingAt
			}
		}
	}
	m.broadcast(a.street, ev)
}

// ---------------------------------------------------------------------------
// Shrines (items/include/shrines.js primeStart 881-925, primeComplete 927-958, launchAttack 1031-1102)
// ---------------------------------------------------------------------------

func rookCanStrike(a *rookAttack) bool { return a != nil && a.phase == "stunned" && a.health > 0 }

func (m *rook) atShrine(cl *Client) bool {
	spot, ok := m.w.streets[cl.Street()]
	if !ok {
		return false
	}
	x, y := cl.Pos()
	return math.Abs(x-spot.x) <= rookReachX && math.Abs(y-spot.y) <= rookReachY
}

func (m *rook) handlePrime(cl *Client, frame json.RawMessage) {
	var msg struct {
		Bonus int `json:"bonus"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.Bonus < 0 || msg.Bonus > 2 {
		m.no(cl, "prime", "bad", "That shrine message did not check out.")
		return
	}
	if !m.allow(cl) {
		m.no(cl, "prime", "slow_down", "Slow down a little.")
		return
	}
	_, a := m.here(cl)
	if a == nil || a.phase == "herald" || a.phase == "dead" || a.phase == "departed" {
		m.no(cl, "prime", "not_rooked", "You can prime a shrine only in the presence of enemies of imagination.") // npc_shrine_*.js:197
		return
	}
	if !rookCanStrike(a) {
		m.no(cl, "prime", "not_stunned", "You must stun the Rook using your Focusing Orb before it is vulnerable to shrine attacks.") // :200
		return
	}
	if a.primed != nil {
		m.no(cl, "prime", "primed", "This shrine is already primed.")
		return
	}
	if !m.atShrine(cl) {
		m.no(cl, "prime", "far", "You need to be closer.")
		return
	}
	p := &rookPrime{by: cl.ID(), name: cl.Name(), bonus: []float64{0, 0.25, 0.5}[msg.Bonus], until: m.h.Now().Add(rookPrimeTime)}
	a.primed = p
	p.stop = m.h.After(rookPrimeTime, func() {
		if s := m.streets[a.street]; s == nil || s.attack != a || a.primed != p {
			return
		}
		m.primeComplete(a)
		m.broadcast(a.street, map[string]any{"type": "prime_end", "by": p.by})
	})
	m.you(cl, "prime", map[string]any{"energy": -rookPrimeEnergy})
	m.broadcast(a.street, map[string]any{"type": "primed", "by": p.by, "name": p.name})
}

// primeComplete pays the primer half the damage dealt through the shrine (937).
func (m *rook) primeComplete(a *rookAttack) {
	p := a.primed
	if p == nil {
		return
	}
	a.primed = nil
	if p.stop != nil {
		p.stop()
	}
	m.youID(p.by, a.street, "prime_done", map[string]any{"xp": int(math.Round(float64(p.total) / 2)), "damage": p.total})
}

// strikeDamage is launchAttack 1034-1046 with the primer's Shrine On bonus.
func (m *rook) strikeDamage(item string, count int, giant string, bonus float64) (int, bool, string) {
	emblem := item == "emblem_"+giant
	if !emblem && (m.w.refused[item] || strings.HasPrefix(item, "emblem_") || strings.HasPrefix(item, "bag_")) {
		return 0, false, "The shrine will not take that."
	}
	var damage int
	if emblem {
		damage = rookEmblemDamage * count
	} else {
		value := m.w.values[item]
		if value == 0 {
			value = 1 // js/shrines.js donationValueFor's floor
		}
		damage = int(float64(value*count) * 0.1)
	}
	if damage > 0 && bonus > 0 {
		damage += int(math.Round(float64(damage) * bonus))
	}
	if damage <= 0 {
		return 0, emblem, "That offering is too small to hurt the Rook."
	}
	return damage, emblem, ""
}

func (m *rook) handleStrike(cl *Client, frame json.RawMessage) {
	var msg struct {
		Item  string `json:"item"`
		Count int    `json:"count"`
	}
	if json.Unmarshal(frame, &msg) != nil || !rookItemRe.MatchString(msg.Item) || msg.Count < 1 || msg.Count > rookMaxCount {
		m.no(cl, "strike", "bad", "That offering did not check out.")
		return
	}
	if !m.allow(cl) {
		m.no(cl, "strike", "slow_down", "Slow down a little.")
		return
	}
	_, a := m.here(cl)
	if a == nil || a.phase == "herald" || a.phase == "departed" || a.phase == "dead" {
		m.no(cl, "strike", "not_rooked", "There is no Rook here to strike.")
		return
	}
	if !rookCanStrike(a) {
		m.no(cl, "strike", "blocked", "Interference from the Rook is blocking donations.") // npc_shrine_*.js:338
		return
	}
	if a.primed == nil {
		m.no(cl, "strike", "not_primed", "This shrine must be primed in order to attack the Rook.") // :340
		return
	}
	if !m.atShrine(cl) {
		m.no(cl, "strike", "far", "You need to be closer.")
		return
	}
	now := m.h.Now()
	if last, ok := a.lastStrike[cl.ID()]; ok && now.Sub(last) < rookStrikeEvery {
		m.no(cl, "strike", "recharging", "The shrine is still recharging.")
		return
	}
	spot := m.w.streets[a.street]
	damage, emblem, why := m.strikeDamage(msg.Item, msg.Count, spot.giant, a.primed.bonus)
	if why != "" {
		m.no(cl, "strike", "refused", why)
		return
	}
	a.lastStrike[cl.ID()] = now
	damage = min(damage, a.health) // 1048-1051
	a.primed.total += damage
	// completeAttack 281-325.
	a.health -= damage
	a.attackers[cl.ID()] += damage
	beam := 1 // shrine_laser_beam_1/2/3 (1061-1067)
	if emblem {
		beam = 3
	} else if damage >= 100 {
		beam = 2
	}
	text := fmt.Sprintf("%s attacked the Rook for %d damage. %d health remaining.", cl.Name(), damage, a.health)
	if a.health <= 0 {
		text = fmt.Sprintf("%s attacked the Rook for %d damage, defeating it.", cl.Name(), damage)
	}
	m.you(cl, "strike", map[string]any{"xp": damage, "damage": damage, "item": msg.Item, "count": msg.Count})
	m.broadcast(a.street, map[string]any{"type": "strike", "by": cl.ID(), "name": cl.Name(), "damage": damage, "beam": beam, "health": a.health, "text": text})
	if a.health <= 0 {
		m.defeat(a)
	}
}

// defeat is completeAttack 300-309 and rookStateChange('dead') 187-206.
func (m *rook) defeat(a *rookAttack) {
	a.health, a.phase, a.angry = 0, "dead", ""
	m.cancel(a, "recovery")
	m.cancelWingBeat(a)
	m.stateChange(a, "dead")
	for _, cl := range m.h.StreetLocked(a.street) {
		if dealt := a.attackers[cl.ID()]; dealt > 0 {
			m.you(cl, "defeat_help", map[string]any{"xp": dealt*2 + rookDefeatBaseXp, "damage": dealt})
		} else {
			m.you(cl, "defeat_witness", map[string]any{"xp": rookWitnessXp})
		}
	}
	a.clockAt = m.h.Now().Add(rookClearAfter)
	m.after(a, "clear", rookClearAfter, func() { m.clear(a) })
	m.broadcast(a.street, map[string]any{"type": "defeated"})
}

// ---------------------------------------------------------------------------
// Rooked things (items/include/rook.js startRevive 106-133, doReviveComplete 151-223)
// ---------------------------------------------------------------------------

func (m *rook) handleRevive(cl *Client, frame json.RawMessage) {
	var msg struct {
		Key  string `json:"key"`
		Balm bool   `json:"balm"`
	}
	if json.Unmarshal(frame, &msg) != nil || !rookKeyRe.MatchString(msg.Key) {
		m.no(cl, "revive", "bad", "That revive did not check out.")
		return
	}
	if !m.allow(cl) {
		m.no(cl, "revive", "slow_down", "Slow down a little.")
		return
	}
	s := m.streets[cl.Street()]
	if s == nil || s.rooked[msg.Key] == nil {
		m.no(cl, "revive", "not_rooked", "That is not rooked any more.")
		return
	}
	r := s.rooked[msg.Key]
	if !msg.Balm {
		// 112: one dose per player; canRevive 365-372: three at most.
		for _, id := range r.healers {
			if id == cl.ID() {
				m.no(cl, "revive", "done", "You have already helped revive this one.")
				return
			}
		}
		if len(r.healers) >= rookHealers {
			m.no(cl, "revive", "full", "Enough doses are already reviving this one.")
			return
		}
	}
	r.healers = append(r.healers, cl.ID())
	doses := len(r.healers)
	done := msg.Balm || doses >= rookHealers
	xp := rookReviveXp
	if msg.Balm {
		xp += rookBalmXp
	}
	favor := 0
	if done {
		delete(s.rooked, msg.Key)
		xp += rookReviveFinal
		favor = rookReviveFavor
		doses = rookHealers
	}
	m.you(cl, "revive", map[string]any{"xp": xp, "energy": -rookReviveEnergy, "mood": -rookReviveMood, "favor": favor, "key": msg.Key, "doses": doses, "done": done, "balm": msg.Balm, "class": r.class})
	m.broadcast(cl.Street(), map[string]any{"type": "revive", "key": msg.Key, "by": cl.ID(), "name": cl.Name(), "doses": doses, "done": done, "balm": msg.Balm, "class": r.class})
	m.tidy(cl.Street())
}

// ---------------------------------------------------------------------------
// Sync, arrivals, departures, dev
// ---------------------------------------------------------------------------

func (m *rook) handleSync(cl *Client, frame json.RawMessage) {
	var msg struct {
		Keys []string `json:"keys"`
	}
	if json.Unmarshal(frame, &msg) != nil || len(msg.Keys) > 64 {
		m.no(cl, "sync", "bad", "That rook message did not check out.")
		return
	}
	tsid := cl.Street()
	if tsid == "" || !m.allow(cl) {
		return
	}
	_, rookable := m.w.streets[tsid]
	if rookable || m.streets[tsid] != nil {
		s := m.street(tsid)
		for _, key := range msg.Keys {
			sm := rookKeyRe.FindStringSubmatch(key)
			if sm == nil || !rookableClasses[sm[1]] || len(s.keys) >= rookMaxKeys {
				continue
			}
			s.keys[key] = sm[1]
		}
	}
	snap := m.message(tsid, nil)
	snap["snap"] = true
	if !rookable {
		snap["none"] = true
	}
	m.h.SendLocked(cl, snap)
	m.tidy(tsid)
}

func (m *rook) joined(cl *Client) {
	tsid := cl.Street()
	spot, rookable := m.w.streets[tsid]
	if !rookable {
		return
	}
	m.armHub(spot.hub)
	if s := m.streets[tsid]; s != nil && s.attack != nil {
		before := s.attack.crowd
		m.grow(s.attack)
		if s.attack.crowd != before {
			m.broadcast(tsid, map[string]any{"type": "crowd", "crowd": s.attack.crowd})
		}
	}
}

func (m *rook) left(cl *Client, tsid string) {
	s := m.streets[tsid]
	if s == nil {
		return
	}
	if a := s.attack; a != nil {
		// An orb's initiator who walks off takes the orb along; a primer's shrine completes.
		if a.orbs[cl.ID()] != nil {
			m.dropOrb(a, cl.ID())
			m.broadcast(tsid, map[string]any{"type": "orb_cancel", "by": cl.ID()})
		}
		if a.primed != nil && a.primed.by == cl.ID() {
			p := a.primed
			m.primeComplete(a)
			m.broadcast(tsid, map[string]any{"type": "prime_end", "by": p.by})
		}
	}
	m.tidy(tsid)
}

// handleDev is main.js /rook_attack 2667-2675, /stun_rook 2681-2684 and a wing beat on demand, for QA.
func (m *rook) handleDev(cl *Client, frame json.RawMessage) {
	if !m.dev {
		m.h.ErrorLocked(cl, "bad_frame", "That message kind is not one the server knows.")
		return
	}
	var msg struct {
		Do string `json:"do"`
	}
	_ = json.Unmarshal(frame, &msg)
	tsid := cl.Street()
	if _, ok := m.w.streets[tsid]; !ok {
		m.no(cl, "dev", "not_rookable", "Failed to Rook this location: Location cannot be rooked.")
		return
	}
	s := m.street(tsid)
	switch msg.Do {
	case "start", "attack":
		if s.attack != nil {
			m.no(cl, "dev", "rooked", "Failed to Rook this location: Location is already rooked.")
			return
		}
		m.start(tsid, msg.Do == "start")
	case "wingbeat":
		if s.attack == nil {
			return
		}
		m.wingBeat(s.attack)
	case "stun":
		if rookCanStun(s.attack) {
			m.completeStun(s.attack, &rookOrb{by: cl.ID(), name: cl.Name()}, 1, s.attack.maxStun, false)
		}
	default:
		m.no(cl, "dev", "bad", "start, attack, wingbeat or stun")
	}
}
