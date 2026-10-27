package main

// Transcendental Radiation and meditating together (register G330): the one
// meditation of the original that acts on other players. A radiator with the
// skill and a Focusing Orb radiates for a rolled time; every three seconds a
// pulse costs the radiator mood and energy and gives every other player in
// range some of each; at the end the radiator gets a bonus. The rules are
// GameServerJS items/focusing_orb.js (verbs.radiate 464-514,
// startTranscendentalMeditation 2023-2183, onTick 1736-1798,
// onTRUpdatePlayers 1800-1900, endTranscendentalRadiationNew 1152-1311,
// endTRForPlayer 1313-1335, getNumPlayersInRadius 1337-1354) and
// players/inc_imagination.js:906-933 (the range); the copy is theirs where
// marked. This module also tells a street who is meditating (the source's
// pc_overlay "bubble" over a meditating player, focusing_orb.js:2091-2104)
// and each meditator how many others meditate on the street at once.
//
// Client to server:
//
//	{"t":"radiance_watch","token":"…"}            the skill and cards the site read from the save (groddle/v1/radiance answers one)
//	{"t":"radiate_start","moodless":false}        Radiate, from the orb's menu; moodless: your mood is 0 (no mood for anyone)
//	{"t":"radiate_mood","moodless":true}          your mood reached 0 (or came back) while radiating
//	{"t":"radiate_stop","why":"stopped"}          you stopped: stopped | moved | verb | chat | tired | street
//	{"t":"meditate_start","kind":"standing"}      a standing or focused meditation began (for the bubble and the count)
//	{"t":"meditate_stop"}                         it ended
//
// Server to client:
//
//	{"t":"radiance_status","rank":1,"dist":400,"cooldown":0}                     after each watch; dist -1 is the whole street
//	{"t":"radiate_started","rad":"7-1","rank":1,"duration":6000,"durationMax":18000,"dist":400,"recipients":2}   to the radiator
//	{"t":"radiate_felt","rad":"7-1","from":{"id":7,"name":"Ada"}}                to a player the first time it is in range
//	{"t":"radiate_pulse","rad":"7-1","from":{"id":7,"name":"Ada"},"rank":1,"mood":true,"n":1}   to each player in range, every 3 s
//	{"t":"radiate_burst","rad":"7-1","n":1,"recipients":2}                       to the radiator at the same pulse: pay its cost
//	{"t":"radiate_ended","rad":"7-1","from":{"id":7,"name":"Ada"},"complete":true}   to each recipient at the end (false: you left first)
//	{"t":"radiate_done","rad":"7-1","why":"timer","seconds":7,"full":true,"maxRelax":false,"effectSeconds":6,
//	     "pulses":2,"recipients":2,"crowd":3,"flags":["transcendence_5players"]}  to the radiator: work out the bonus
//	{"t":"meditating","id":7,"kind":"transcendental"}                           to the street ("" when it stopped)
//	{"t":"meditators","list":[{"id":7,"kind":"standing"}]}                     to a player joining a street where someone meditates
//	{"t":"meditate_together","n":3,"kind":"standing"}                          to each meditator when the number of others meditating there changes
//	{"t":"error","code":"radiance","message":"…"}
//
// The watch token is base64url(JSON) "." base64url(HMAC-SHA256(secret, the
// first part)) with JSON {"k":"radiance","sub":7,"r":1,"u":"transcendental_radius_1,…",
// "iat":…,"exp":…}: r the highest Transcendental Radiation rank the save has
// learned (0 to 3), u the Transcendental cards it holds.
//
// Rules kept from the source:
//   - Radiate needs the skill (state null otherwise), no meditation cooldown
//     ("You are too scattered to radiate right now."), somebody in range
//     ("There's nobody near enough for you to radiate.") (verbs.radiate 475-501).
//   - The range is |dx| <= 400, 800 or 1500 px by rank; Transcendental Radius
//     I and II multiply it by 1.5 and 2, Radius III is the whole street
//     (imagination_get_radiation_distance). Only x counts.
//   - The time: randInt(duration, duration_max) seconds of the rank's package
//     (catalog_skill_packages.js:2397-2426, inc_skill_packages.js:188-195),
//     made a multiple of 3, then 20% longer with Radiation Prolonger or 50%
//     with Radiation Prolongest, then a multiple of 3 again (2031-2072).
//   - The orb ticks every second from the start; at every third tick (3, 6, …)
//     a pulse: the radiator pays, everyone in range gains, a player in range
//     for the first time is told "You feel calmness radiating from <name>." (startTRString),
//     players who left the street are told they were "somewhat transcended",
//     and with nobody in range the radiator is told "There's nobody near enough
//     for you to radiate." and stops (onTRUpdatePlayers). The radiation ends at
//     the tick where ticks x 1000 >= the time (onTick 1784-1795).
//   - Recipients gain no mood while the radiator's mood is 0 (1857-1862).
//   - The end: everyone in range is told they were transcended (endTRForPlayer
//     complete); seconds = ceil(elapsed), full when the time <= seconds x 1000,
//     the bonus over the time (full) or the last whole pulse (not full), Max
//     Relax when duration_max == seconds x 1000; the Sharing the Oneness flag
//     transcendence_5players when seconds > 0 and 6 or more players are on the
//     street (1298-1300); then meditation_cooldown 120 s (1305).
//   - Any action interrupts (players/player.js:1846-1883): the game sends
//     radiate_stop, and moving more than radMoveSlack px is caught here too.
//
// Ours: the radiator's own mood is the client's word (radiate_start and
// radiate_mood); each pulse's amounts are worked out by the receiver from its
// own tank and the radiator's rank, applied once per "rad" and "n"; a cooldown
// after a standing or focused meditation is the source's floor (20 s, 30 s;
// the tank-sized rest is the game's); "Everyone around you gains some mood
// and energy." whenever anyone gained (the source tested a list it had just
// emptied, 1269); Max Luck's longest roll is not seen here (a buff the game
// keeps). Cooldowns and radiations live in memory: a restart forgets them.
//
// Trust. The server decides who is in range (the positions it holds), when
// each pulse happens, how long it lasts, the rank and cards (from the save
// the site signs), the cooldown and the quest flag. Each player's mood,
// energy and iMG are the client's to apply to its own save, as every reward
// is: nothing of value moves.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	radTick         = time.Second       // focusing_orb.js:1797 apiSetTimer("onTick", 1000)
	radPulseEvery   = 3                 // ticks between pulses (1765)
	radCooldown     = 120 * time.Second // meditation_cooldown after radiating (1305)
	radStandingRest = 20 * time.Second  // max(20, tank/50) (focusing_orb.js standing cooldown): the floor
	radFocusedRest  = 30 * time.Second  // max(30, tank/40): the floor
	radWholeStreet  = 10000             // inc_imagination.js:923 "radiate to all on level"
	radMoveSlack    = 24.0              // px the radiator may shift before it counts as moving
	radCrowdFlag    = 6                 // num_keys(players) >= 6 (1298)
	radStartBurst   = 3
	radStartEvery   = 2 * time.Second
	radMsgBurst     = 12
	radMsgEvery     = time.Second

	radNobody    = "There's nobody near enough for you to radiate." // focusing_orb.js:485, 1895
	radScattered = "You are too scattered to radiate right now."    // 480
	radNoSkill   = "You need to know Transcendental Radiation to radiate."
	radBusy      = "You are already meditating."
	radOff       = "Radiating is not set up on this server."
	radNoWatch   = "Your skills have not reached the server yet. Try again in a moment."
)

// radPackages is catalog_skill_packages.js:2397-2426 by rank: duration, duration_max (ms).
var radPackages = map[int][2]int{1: {6000, 18000}, 2: {9000, 21000}, 3: {12000, 30000}}

// radCards are the cards a token may name (inc_data_imagination.js transcendental_*).
var radCards = map[string]bool{"transcendental_radius_1": true, "transcendental_radius_2": true, "transcendental_radius_3": true,
	"transcendental_benefits_1": true, "transcendental_benefits_2": true, "transcendental_imagination": true}

var radMeditationKinds = map[string]bool{"standing": true, "focused": true}

// radSecretForTest is the secret in tests (the flag is not parsed there).
var radSecretForTest []byte

func init() { modules = append(modules, newRadiance) }

type radWatch struct {
	rank  int
	cards map[string]bool
	exp   time.Time
}

type radRecipient struct {
	inRange bool
}

type radiation struct {
	id         string
	by         int64
	name       string
	street     string
	x, y       float64
	rank       int
	dist       float64 // radWholeStreet: everyone on the street
	duration   int     // ms, a multiple of 3000
	durMax     int     // the package's duration_max (Max Relax)
	start      time.Time
	ticks      int
	pulses     int
	moodless   bool
	recipients map[int64]*radRecipient
	ever       int
	stop       func() bool
}

type radianceModule struct {
	h        *Hub
	secret   []byte
	roll     func(lo, hi int) int // randInt(lo, hi), inclusive
	watch    map[int64]*radWatch
	active   map[int64]*radiation
	cooldown map[int64]time.Time
	medit    map[int64]string // player -> "standing" | "focused" | "transcendental"
	together map[int64]int    // the last meditate_together n each meditator was told
	rates    map[string]*bucket
	serial   int
}

func newRadiance(h *Hub) Module {
	secret := radSecretForTest
	if !testing.Testing() {
		path := ""
		if f := flag.Lookup("secret-file"); f != nil {
			path = expandHome(f.Value.String())
		}
		secret, _ = readSecret(path)
	}
	return &radianceModule{h: h, secret: secret, roll: func(lo, hi int) int { return lo + rand.IntN(hi-lo+1) },
		watch: map[int64]*radWatch{}, active: map[int64]*radiation{}, cooldown: map[int64]time.Time{},
		medit: map[int64]string{}, together: map[int64]int{}, rates: map[string]*bucket{}}
}

func (m *radianceModule) Register(r *Registry) {
	r.Handle("radiance_watch", m.watchMsg)
	r.Handle("radiate_start", m.startMsg)
	r.Handle("radiate_mood", m.moodMsg)
	r.Handle("radiate_stop", m.stopMsg)
	r.Handle("meditate_start", m.meditateStart)
	r.Handle("meditate_stop", m.meditateStop)
	r.OnJoin(m.joined)
	r.OnLeave(m.left)
	r.Provide("radiance.meditating", func(id int64) string { return m.medit[id] })
}

func radLog(format string, args ...any) {
	if !testing.Testing() {
		log.Printf("radiance: "+format, args...)
	}
}

func (m *radianceModule) allow(kind string, cl *Client, burst float64, every time.Duration) bool {
	key := fmt.Sprintf("%s:%d", kind, cl.ID())
	b := m.rates[key]
	if b == nil {
		b = &bucket{}
		m.rates[key] = b
	}
	return b.take(m.h.Now(), burst, every)
}

func (m *radianceModule) refuse(cl *Client, message string) { m.h.ErrorLocked(cl, "radiance", message) }

// —— The token ——

type radClaims struct {
	K   string `json:"k"`
	Sub int64  `json:"sub"`
	R   int    `json:"r"`
	U   string `json:"u"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

var (
	errRadOff     = errors.New("radiating is not set up on this server")
	errRadToken   = errors.New("that is not a token the site signed")
	errRadExpired = errors.New("that token has expired")
	errRadOwner   = errors.New("that token is not yours")
)

func (m *radianceModule) verify(token string, owner int64) (radClaims, error) {
	var c radClaims
	if len(m.secret) == 0 {
		return c, errRadOff
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return c, errRadToken
	}
	want := hmac.New(sha256.New, m.secret)
	want.Write([]byte(body))
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want.Sum(nil)) {
		return c, errRadToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.K != "radiance" || c.Sub <= 0 || c.R < 0 || c.R > 3 {
		return radClaims{}, errRadToken
	}
	if c.Sub != owner {
		return radClaims{}, errRadOwner
	}
	if c.Exp < m.h.Now().Unix() {
		return radClaims{}, errRadExpired
	}
	return c, nil
}

func (m *radianceModule) watchMsg(cl *Client, frame json.RawMessage) {
	if !m.allow("watch", cl, radMsgBurst, radMsgEvery) {
		return
	}
	var f struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(frame, &f)
	c, err := m.verify(f.Token, cl.ID())
	if err != nil {
		radLog("watch from %d refused: %v", cl.ID(), err)
		if errors.Is(err, errRadOff) {
			m.refuse(cl, radOff)
		} else {
			m.refuse(cl, radNoWatch)
		}
		return
	}
	cards := map[string]bool{}
	for _, w := range strings.Split(c.U, ",") {
		if radCards[w] {
			cards[w] = true
		}
	}
	w := &radWatch{rank: c.R, cards: cards, exp: time.Unix(c.Exp, 0)}
	m.watch[cl.ID()] = w
	m.h.SendLocked(cl, map[string]any{"t": "radiance_status", "rank": w.rank, "dist": radDistOut(radDistance(w)), "cooldown": m.cooldownLeft(cl.ID())})
}

// radDistance is imagination_get_radiation_distance (inc_imagination.js:906-933).
func radDistance(w *radWatch) float64 {
	if w == nil {
		return 0
	}
	dist := map[int]float64{1: 400, 2: 800, 3: 1500}[w.rank]
	switch {
	case w.cards["transcendental_radius_3"]:
		dist = radWholeStreet
	case w.cards["transcendental_radius_2"]:
		dist *= 2
	case w.cards["transcendental_radius_1"]:
		dist *= 1.5
	}
	return dist
}

func radDistOut(d float64) float64 {
	if d > 9000 {
		return -1
	}
	return d
}

// radDuration is the rolled time in ms (startTranscendentalMeditation 2023-2072).
func radDuration(rank int, cards map[string]bool, roll func(lo, hi int) int) int {
	pkg := radPackages[rank]
	secs := float64(roll(pkg[0]/1000, pkg[1]/1000))
	three := func(d float64) float64 {
		d = math.Round(d)
		switch int(d) % 3 {
		case 1:
			d--
		case 2:
			d++
		}
		return d
	}
	secs = three(secs)
	if cards["transcendental_benefits_2"] {
		secs *= 1.5
	} else if cards["transcendental_benefits_1"] {
		secs *= 1.2
	}
	return int(three(secs)) * 1000
}

func (m *radianceModule) cooldownLeft(id int64) int {
	left := m.cooldown[id].Sub(m.h.Now())
	if left <= 0 {
		return 0
	}
	return int(math.Ceil(left.Seconds()))
}

func (m *radianceModule) rest(id int64, d time.Duration) {
	if until := m.h.Now().Add(d); until.After(m.cooldown[id]) {
		m.cooldown[id] = until
	}
}

// inRange is getNumPlayersInRadius's test for one other player.
func (r *radiation) inRange(o *Client) bool {
	if o.ID() == r.by || o.Street() != r.street {
		return false
	}
	if r.dist > 9000 {
		return true
	}
	x, _ := o.Pos()
	return math.Abs(x-r.x) <= r.dist
}

func (m *radianceModule) countInRange(cl *Client, dist float64) int {
	x, _ := cl.Pos()
	n := 0
	for _, o := range m.h.StreetLocked(cl.Street()) {
		if o.ID() == cl.ID() {
			continue
		}
		if ox, _ := o.Pos(); dist > 9000 || math.Abs(ox-x) <= dist {
			n++
		}
	}
	return n
}

func radWho(id int64, name string) map[string]any { return map[string]any{"id": id, "name": name} }

// —— Radiating ——

func (m *radianceModule) startMsg(cl *Client, frame json.RawMessage) {
	if !m.allow("start", cl, radStartBurst, radStartEvery) {
		return
	}
	var f struct {
		Moodless bool `json:"moodless"`
	}
	_ = json.Unmarshal(frame, &f)
	id := cl.ID()
	if len(m.secret) == 0 {
		m.refuse(cl, radOff)
		return
	}
	w := m.watch[id]
	if w == nil || w.exp.Before(m.h.Now()) {
		m.refuse(cl, radNoWatch)
		return
	}
	if w.rank < 1 {
		m.refuse(cl, radNoSkill)
		return
	}
	if cl.Street() == "" {
		m.refuse(cl, radNobody)
		return
	}
	if m.medit[id] != "" || m.active[id] != nil {
		m.refuse(cl, radBusy)
		return
	}
	if m.cooldownLeft(id) > 0 {
		m.refuse(cl, radScattered)
		return
	}
	dist := radDistance(w)
	if m.countInRange(cl, dist) <= 0 {
		m.refuse(cl, radNobody)
		return
	}
	m.serial++
	x, y := cl.Pos()
	r := &radiation{id: fmt.Sprintf("%d-%d", id, m.serial), by: id, name: cl.Name(), street: cl.Street(), x: x, y: y, rank: w.rank, dist: dist,
		duration: radDuration(w.rank, w.cards, m.roll), durMax: radPackages[w.rank][1], start: m.h.Now(), moodless: f.Moodless,
		recipients: map[int64]*radRecipient{}}
	m.active[id] = r
	// The start: everyone in range hears it once (2109-2134).
	for _, o := range m.h.StreetLocked(r.street) {
		if r.inRange(o) {
			r.recipients[o.ID()] = &radRecipient{inRange: true}
			r.ever++
			m.h.SendLocked(o, map[string]any{"t": "radiate_felt", "rad": r.id, "from": radWho(id, r.name)})
		}
	}
	m.h.SendLocked(cl, map[string]any{"t": "radiate_started", "rad": r.id, "rank": r.rank, "duration": r.duration, "durationMax": r.durMax,
		"dist": radDistOut(r.dist), "recipients": len(r.recipients)})
	m.setMeditating(cl, "transcendental")
	radLog("%s (%d) radiates on %s: rank %d, %d ms, %d in range", r.name, id, r.street, r.rank, r.duration, len(r.recipients))
	m.armTick(r)
}

func (m *radianceModule) armTick(r *radiation) {
	r.stop = m.h.After(radTick, func() { m.tick(r) })
}

// tick is the orb's onTick (1736-1798) for a radiation.
func (m *radianceModule) tick(r *radiation) {
	if m.active[r.by] != r {
		return
	}
	// Any action interrupts (players/player.js:1874-1882): walking off is one the server sees itself.
	if m.movedLocked(r, m.h.ClientLocked(r.by)) {
		m.finish(r, "moved")
		return
	}
	ticks := r.ticks
	if ticks > 0 && ticks%radPulseEvery == 0 {
		if !m.pulse(r) {
			return
		}
	}
	r.ticks++
	if ticks*1000 >= r.duration {
		m.finish(r, "timer")
		return
	}
	m.armTick(r)
}

// pulse is onTRUpdatePlayers (1800-1900); false when it ended the radiation.
func (m *radianceModule) pulse(r *radiation) bool {
	r.pulses++
	n := r.pulses
	from := radWho(r.by, r.name)
	here := map[int64]bool{}
	count := 0
	for _, o := range m.h.StreetLocked(r.street) {
		if o.ID() == r.by {
			continue
		}
		here[o.ID()] = true
		rec := r.recipients[o.ID()]
		if !r.inRange(o) {
			if rec != nil {
				rec.inRange = false
			}
			continue
		}
		count++
		if rec == nil {
			rec = &radRecipient{}
			r.recipients[o.ID()] = rec
			r.ever++
			m.h.SendLocked(o, map[string]any{"t": "radiate_felt", "rad": r.id, "from": from})
		}
		rec.inRange = true
		m.h.SendLocked(o, map[string]any{"t": "radiate_pulse", "rad": r.id, "from": from, "rank": r.rank, "mood": !r.moodless, "n": n})
	}
	if by := m.h.ClientLocked(r.by); by != nil {
		m.h.SendLocked(by, map[string]any{"t": "radiate_burst", "rad": r.id, "n": n, "recipients": count})
	}
	// Anybody who left the street or went offline (1886-1891).
	for id := range r.recipients {
		if !here[id] {
			m.endFor(r, id, false)
		}
	}
	if count <= 0 {
		m.finish(r, "alone")
		return false
	}
	return true
}

// endFor is endTRForPlayer: tell a recipient its share is over.
func (m *radianceModule) endFor(r *radiation, id int64, complete bool) {
	if _, ok := r.recipients[id]; !ok {
		return
	}
	delete(r.recipients, id)
	if o := m.h.ClientLocked(id); o != nil {
		m.h.SendLocked(o, map[string]any{"t": "radiate_ended", "rad": r.id, "from": radWho(r.by, r.name), "complete": complete})
	}
}

// finish is endTranscendentalRadiationNew (1152-1311) and cancelTranscendentalMeditation.
func (m *radianceModule) finish(r *radiation, why string) {
	if m.active[r.by] != r {
		return
	}
	delete(m.active, r.by)
	if r.stop != nil {
		r.stop()
	}
	now := m.h.Now()
	elapsed := now.Sub(r.start)
	seconds := int(math.Ceil(float64(elapsed) / float64(time.Second)))
	actual := seconds * 1000
	full := r.duration <= actual
	effect := r.duration / 1000
	if !full {
		effect = int(elapsed / time.Second)
		effect -= effect % 3
	}
	crowd := 0
	for _, o := range m.h.StreetLocked(r.street) {
		crowd++
		if r.inRange(o) {
			m.endFor(r, o.ID(), true)
		}
	}
	// Recipients still on the street but out of range hear nothing (the source's loop is over those
	// in range); one who left since the last pulse is told it was somewhat transcended.
	for id := range r.recipients {
		if o := m.h.ClientLocked(id); o != nil && o.Street() == r.street {
			delete(r.recipients, id)
		} else {
			m.endFor(r, id, false)
		}
	}
	flags := []string{}
	if seconds > 0 && crowd >= radCrowdFlag {
		flags = append(flags, "transcendence_5players")
	}
	if by := m.h.ClientLocked(r.by); by != nil {
		m.h.SendLocked(by, map[string]any{"t": "radiate_done", "rad": r.id, "why": why, "seconds": seconds, "full": full,
			"maxRelax": r.durMax == actual, "effectSeconds": effect, "pulses": r.pulses, "recipients": r.ever, "crowd": crowd, "flags": flags})
	}
	m.rest(r.by, radCooldown)
	if m.medit[r.by] == "transcendental" {
		if by := m.h.ClientLocked(r.by); by != nil && by.Street() == r.street {
			m.setMeditating(by, "")
		} else {
			m.clearMeditating(r.by, r.street)
		}
	}
	radLog("%s (%d) stopped radiating on %s (%s): %d s, %d pulses, %d reached, %d on the street", r.name, r.by, r.street, why, seconds, r.pulses, r.ever, crowd)
}

func (m *radianceModule) moodMsg(cl *Client, frame json.RawMessage) {
	if !m.allow("mood", cl, radMsgBurst, radMsgEvery) {
		return
	}
	var f struct {
		Moodless bool `json:"moodless"`
	}
	_ = json.Unmarshal(frame, &f)
	if r := m.active[cl.ID()]; r != nil {
		r.moodless = f.Moodless
	}
}

var radStopWhy = map[string]bool{"stopped": true, "moved": true, "verb": true, "chat": true, "tired": true, "street": true}

func (m *radianceModule) stopMsg(cl *Client, frame json.RawMessage) {
	var f struct {
		Why string `json:"why"`
	}
	_ = json.Unmarshal(frame, &f)
	if !radStopWhy[f.Why] {
		f.Why = "stopped"
	}
	if r := m.active[cl.ID()]; r != nil {
		m.finish(r, f.Why)
	}
}

// —— Who is meditating ——

func (m *radianceModule) meditateStart(cl *Client, frame json.RawMessage) {
	if !m.allow("meditate", cl, radMsgBurst, radMsgEvery) {
		return
	}
	var f struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(frame, &f)
	if !radMeditationKinds[f.Kind] || cl.Street() == "" || m.active[cl.ID()] != nil {
		return
	}
	m.setMeditating(cl, f.Kind)
}

func (m *radianceModule) meditateStop(cl *Client, _ json.RawMessage) {
	kind := m.medit[cl.ID()]
	if kind == "" || kind == "transcendental" {
		return
	}
	if kind == "standing" {
		m.rest(cl.ID(), radStandingRest)
	} else {
		m.rest(cl.ID(), radFocusedRest)
	}
	m.setMeditating(cl, "")
}

func (m *radianceModule) setMeditating(cl *Client, kind string) {
	id := cl.ID()
	if m.medit[id] == kind {
		return
	}
	if kind == "" {
		delete(m.medit, id)
		delete(m.together, id)
	} else {
		m.medit[id] = kind
	}
	m.h.BroadcastStreetLocked(cl.Street(), map[string]any{"t": "meditating", "id": id, "kind": kind}, id)
	m.recount(cl.Street())
}

// clearMeditating forgets a player who is no longer on street (it left or went offline).
func (m *radianceModule) clearMeditating(id int64, street string) {
	if m.medit[id] == "" {
		return
	}
	delete(m.medit, id)
	delete(m.together, id)
	m.h.BroadcastStreetLocked(street, map[string]any{"t": "meditating", "id": id, "kind": ""}, id)
	m.recount(street)
}

// recount tells each meditator on a street how many others meditate there now, when that changed.
func (m *radianceModule) recount(street string) {
	if street == "" {
		return
	}
	var on []*Client
	for _, o := range m.h.StreetLocked(street) {
		if m.medit[o.ID()] != "" {
			on = append(on, o)
		}
	}
	for _, o := range on {
		n := len(on) - 1
		if last, told := m.together[o.ID()]; told && last == n {
			continue
		}
		m.together[o.ID()] = n
		m.h.SendLocked(o, map[string]any{"t": "meditate_together", "n": n, "kind": m.medit[o.ID()]})
	}
}

func (m *radianceModule) joined(cl *Client) {
	var list []map[string]any
	for _, o := range m.h.StreetLocked(cl.Street()) {
		if k := m.medit[o.ID()]; k != "" && o.ID() != cl.ID() {
			list = append(list, map[string]any{"id": o.ID(), "kind": k})
		}
	}
	if len(list) > 0 {
		sort.Slice(list, func(i, j int) bool { return list[i]["id"].(int64) < list[j]["id"].(int64) })
		m.h.SendLocked(cl, map[string]any{"t": "meditators", "list": list})
	}
}

func (m *radianceModule) left(cl *Client, street string) {
	id := cl.ID()
	if r := m.active[id]; r != nil {
		m.finish(r, "street")
	}
	kind := m.medit[id]
	if kind == "standing" {
		m.rest(id, radStandingRest)
	} else if kind == "focused" {
		m.rest(id, radFocusedRest)
	}
	m.clearMeditating(id, street)
}

// movedLocked is whether the radiator walked off: the hub has no move hook, so each tick looks;
// radiate_stop from the game (it stops at a 6 px step) is the usual way.
func (m *radianceModule) movedLocked(r *radiation, by *Client) bool {
	if by == nil || by.Street() != r.street {
		return false
	}
	x, y := by.Pos()
	return math.Abs(x-r.x) > radMoveSlack || math.Abs(y-r.y) > 2*radMoveSlack
}
