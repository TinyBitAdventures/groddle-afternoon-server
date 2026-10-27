package main

// Following another player (registers G329's left-outs and G334): one player
// asks to follow another, the leader says yes, and from then on the follower
// walks after the leader and goes where the leader goes, street to street and
// on a teleport, until any input of theirs, the leader going somewhere they
// cannot follow, or either of them going offline ends it. The server decides
// who follows whom and where the leader went; a client only asks, answers,
// stops, and says it is teleporting. The rules are GameServerJS main.js
// doStartFollow / doStopFollow / doEndMove, players/player.js respondToFollow,
// stopFollowing, removeFollowers and updateGlitchTrain, and
// players/inc_teleportation.js; the copy players read is the game's
// (js/follow-mp.js).
//
// Client to server:
//
//	{"t":"follow_start","id":7}                 ask to follow player 7 (the menu's Follow)
//	{"t":"follow_answer","id":9,"accept":true}  the leader's answer to player 9's ask
//	{"t":"follow_stop"}                         stop following (any input, or the menu's Stop following)
//	{"t":"follow_teleport","street":"GA5…","kind":"plain"}   "I am teleporting there" (plain or map)
//
// Server to client:
//
//	{"t":"follow_asked","id":9,"name":"Bo"}                  to the leader: Bo would like to follow you
//	{"t":"follow_waiting","id":7,"name":"Ada"}               to the asker: waiting for Ada to confirm
//	{"t":"follow_ask_gone","id":9}                           to the leader: Bo's ask is over (offline, or answered elsewhere)
//	{"t":"follow_refused","id":7,"name":"Ada","why":"…"}     to the asker: self, street, already, busy, blocked, declined, far, gone
//	{"t":"follow_too_far","id":9,"name":"Bo"}                to the leader who said yes to someone too far away
//	{"t":"follow_started","id":7,"name":"Ada","streets":1}   to the follower
//	{"t":"follow_list","followers":[{"id":9,"name":"Bo"}],"added":9,"removed":0,"why":""}   to the leader, after each change
//	{"t":"follow_ended","id":7,"name":"Ada","why":"…"}       to the follower: stopped, offline, private, left, reset
//	{"t":"follow_go","id":7,"name":"Ada","street":"GA5…","x":0,"y":0,"dir":1,"via":"walk"}   to each follower: the leader went there (via teleport too)
//	{"t":"follow_arrived","id":7,"street":"GA5…","streets":2}   to the follower, on reaching where the leader went
//	{"t":"follow_teleported","street":"GA5…","kind":"plain","followers":3,"complete":3}   to the leader, teleport_complete
//	{"t":"follow_train","street":"GA5…","chain":20,"on":true}  to a player in a follow, at each updateGlitchTrain point
//
// Rules kept from the source:
//   - Asking follows the chain to the top: asking someone who follows a
//     leader asks that leader (main.js:1497-1509), and asking yourself so is
//     "You can't follow yourself!" (1511-1515). The menu offers Follow only on
//     the same street and not while following anyone (glitch-client
//     InteractionMenuController.as 592-614): checked here as well.
//   - The leader is asked "<name> would like to follow you." with OK and No
//     thanks for 30 seconds, no answer declining; the asker is told "Waiting
//     for <name> to confirm..."; too many prompts reads "is too busy to be
//     followed..." (main.js:1517-1550; prompts_over_limit, inc_prompts.js 83-86).
//   - On OK the two must still be within 1000 px on one street
//     (player.js:1485-1492, distanceFromPlayer 1816-1820), else both are told
//     who was too far; then "You started following <name>." and
//     streets_followed starts at 1 (1494-1497). No thanks: "<name> declined
//     to be followed." (1514-1516).
//   - A follower goes where its leader went (the Java server's follow move,
//     main.js:27 follow_move_end -> doEndMove 'pc_follow_move'), and each such
//     move counts streets_followed; 3 is Stalker, Non-Creepy Designation
//     (main.js:1479-1484).
//   - Any input of the follower's stops it (glitch-client AvatarController.as
//     _endFollowing on the arrow keys and Space, 373-430; doStopFollow
//     main.js:1553-1559). Teleporting yourself stops it too (player.js:671).
//   - Going offline stops following and removes one's followers
//     (player.js:309-311); so does a reload here (a new socket starts unfollowed,
//     as the source's follow was never saved). An instance or a private place
//     the leader enters removes the followers (main.js:1316-1321, 1356-1357).
//   - Teleporting with followers: the counters teleportation_self_withfollowers
//     (and _5, _11) count the followers at the teleport (inc_teleportation.js
//     404-414 plain, 533-542 map), and teleport_complete, 3 seconds later, sets
//     teleportation_self_withfollowers_3 with 3 or more still following
//     (1048-1057): Three's a Cloud. The followers come along (follow_go via
//     teleport).
//   - The Glitch Train (player.js:3115-3154): a chain is the player's own
//     followers plus their leader's, plus one; at 20 the train is on for that
//     location (glitch_train, 5 locations for the badge) and below it the
//     continuous count starts over. Checked on entering a street
//     (location.js:123) and whenever a follow starts or stops.
//
// Ours: a private place in the remake is any street id with an underscore
// (HOME_, HOUSE_, PARTY_, RACE_) and any place the client leaves the street
// for (a quest room, a scene): the leader there for followAwayWait removes
// the followers. That includes the leader's own home, where the source let
// followers in after the owner (main.js:1290 skips the check for the owner):
// a follower here would need the home pass the site signs (homes.go). The
// source refused a teleport when a follower carried contraband
// (inc_teleportation.js:267-277); the server cannot see another player's
// buffs, so that check is the leader's own game's. Blocked players cannot ask
// each other (buddies.ignoring, as party.go and trade.go refuse them). Asks
// are rate limited and a leader holds at most followMaxAsks.

import (
	"encoding/json"
	"log"
	"math"
	"strings"
	"time"
)

const (
	followMaxDistance  = 1000             // player.js:1487
	followAskWait      = 30 * time.Second // main.js:1522 timeout 30, timeout_value decline
	followMaxAsks      = 20               // ours: the source's limit is 100 prompts of any kind
	followTeleportDone = 3 * time.Second  // inc_teleportation.js:402 apiSetTimer('teleport_complete', 3000)
	followTeleportWait = 10 * time.Second // ours: a teleport said before the join it names
	followAwayWait     = 30 * time.Second // ours: a leader off every street this long is somewhere private
	followCloud        = 3                // inc_teleportation.js:1054 countFollowers() >= 3
	followStalker      = 3                // main.js:1482 streets_followed >= 3
	glitchTrainLength  = 20               // player.js:3138 required_length (3 on dev)
	followSentKeep     = 4                // ours: go targets a lagging follower may still arrive at
	followAskBurst     = 4
	followAskEvery     = 5 * time.Second
	followTpBurst      = 3
	followTpEvery      = 10 * time.Second
)

func init() { modules = append(modules, newFollows) }

type followTeleport struct {
	street string
	kind   string
	exp    time.Time
}

type follows struct {
	h        *Hub
	leaderOf map[int64]int64                 // follower -> leader
	of       map[int64]map[int64]bool        // leader -> followers
	asks     map[int64]map[int64]func() bool // leader -> askers -> timeout stop
	asking   map[int64]int64                 // asker -> the leader asked
	streets  map[int64]int                   // follower -> streets_followed
	sent     map[int64][]string              // follower -> go targets not yet reached
	away     map[int64]func() bool           // leader off every street
	tp       map[int64]*followTeleport       // leader -> a teleport said
	joinedAt map[int64]time.Time             // when each player last joined a street
	train    map[int64]bool                  // the train was on for this player
	rates    map[int64]*bucket
	tpRates  map[int64]*bucket
}

func newFollows(h *Hub) Module {
	return &follows{h: h, leaderOf: map[int64]int64{}, of: map[int64]map[int64]bool{}, asks: map[int64]map[int64]func() bool{},
		asking: map[int64]int64{}, streets: map[int64]int{}, sent: map[int64][]string{}, away: map[int64]func() bool{},
		tp: map[int64]*followTeleport{}, joinedAt: map[int64]time.Time{}, train: map[int64]bool{}, rates: map[int64]*bucket{}, tpRates: map[int64]*bucket{}}
}

func (m *follows) Register(r *Registry) {
	r.Handle("follow_start", m.start)
	r.Handle("follow_answer", m.answer)
	r.Handle("follow_stop", func(cl *Client, _ json.RawMessage) { m.stopFollowing(cl.ID(), "stopped") })
	r.Handle("follow_teleport", m.teleportSaid)
	r.OnConnect(func(cl *Client) { m.forget(cl.ID(), "reset") })
	r.OnJoin(m.joined)
	r.OnLeave(m.left)
	r.OnDisconnect(func(cl *Client) {
		m.forget(cl.ID(), "offline")
		delete(m.rates, cl.ID())
		delete(m.tpRates, cl.ID())
		delete(m.joinedAt, cl.ID())
		delete(m.train, cl.ID())
	})
	// For other modules: who a player follows (0: nobody), and how many follow them.
	r.Provide("follow.leaderOf", func(id int64) int64 { return m.leaderOf[id] })
	r.Provide("follow.count", func(id int64) int { return len(m.of[id]) })
}

func (m *follows) allow(set map[int64]*bucket, id int64, burst float64, every time.Duration) bool {
	b := set[id]
	if b == nil {
		b = &bucket{}
		set[id] = b
	}
	return b.take(m.h.Now(), burst, every)
}

// top is the leader at the head of id's chain (main.js:1500-1509, at most 100 steps).
func (m *follows) top(id int64) int64 {
	for i := 0; i < 100; i++ {
		next := m.leaderOf[id]
		if next == 0 {
			break
		}
		id = next
	}
	return id
}

func followPrivate(street string) bool { return street == "" || strings.Contains(street, "_") }

func (m *follows) refuse(cl *Client, id int64, name, why string) {
	m.h.SendLocked(cl, map[string]any{"t": "follow_refused", "id": id, "name": name, "why": why})
}

// —— Asking and answering ——

func (m *follows) start(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.ID <= 0 {
		return
	}
	if !m.allow(m.rates, cl.ID(), followAskBurst, followAskEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	target := m.h.ClientLocked(msg.ID)
	if target == nil {
		m.refuse(cl, msg.ID, "", "gone")
		return
	}
	if l := m.leaderOf[cl.ID()]; l != 0 {
		m.refuse(cl, l, m.nameOf(l), "already")
		return
	}
	if cl.Street() == "" || target.Street() != cl.Street() {
		m.refuse(cl, target.ID(), target.Name(), "street")
		return
	}
	leader := m.h.ClientLocked(m.top(target.ID()))
	if leader == nil {
		leader = target
	}
	if leader.ID() == cl.ID() {
		m.refuse(cl, target.ID(), target.Name(), "self")
		return
	}
	if blockedBetween(m.h, cl.ID(), leader.ID()) {
		m.refuse(cl, leader.ID(), leader.Name(), "blocked")
		return
	}
	if prev := m.asking[cl.ID()]; prev != 0 {
		m.dropAsk(prev, cl.ID())
	}
	set := m.asks[leader.ID()]
	if len(set) >= followMaxAsks {
		m.refuse(cl, leader.ID(), leader.Name(), "busy")
		return
	}
	if set == nil {
		set = map[int64]func() bool{}
		m.asks[leader.ID()] = set
	}
	lid, aid := leader.ID(), cl.ID()
	set[aid] = m.h.After(followAskWait, func() { m.timedOut(lid, aid) })
	m.asking[aid] = lid
	m.h.SendLocked(leader, map[string]any{"t": "follow_asked", "id": aid, "name": cl.Name()})
	m.h.SendLocked(cl, map[string]any{"t": "follow_waiting", "id": lid, "name": leader.Name()})
}

// dropAsk takes asker's ask off leader's list and tells the leader, who may still show the prompt.
func (m *follows) dropAsk(leader, asker int64) bool {
	set := m.asks[leader]
	stop, ok := set[asker]
	if !ok {
		return false
	}
	stop()
	delete(set, asker)
	if len(set) == 0 {
		delete(m.asks, leader)
	}
	if m.asking[asker] == leader {
		delete(m.asking, asker)
	}
	if l := m.h.ClientLocked(leader); l != nil {
		m.h.SendLocked(l, map[string]any{"t": "follow_ask_gone", "id": asker})
	}
	return true
}

func (m *follows) timedOut(leader, asker int64) {
	if _, ok := m.asks[leader][asker]; !ok {
		return
	}
	m.dropAsk(leader, asker)
	if a := m.h.ClientLocked(asker); a != nil {
		m.refuse(a, leader, m.nameOf(leader), "declined")
	}
}

func (m *follows) answer(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID     int64 `json:"id"`
		Accept bool  `json:"accept"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.ID <= 0 {
		return
	}
	set := m.asks[cl.ID()]
	stop, ok := set[msg.ID]
	if !ok {
		return
	}
	stop()
	delete(set, msg.ID)
	if len(set) == 0 {
		delete(m.asks, cl.ID())
	}
	if m.asking[msg.ID] == cl.ID() {
		delete(m.asking, msg.ID)
	}
	f := m.h.ClientLocked(msg.ID)
	if f == nil {
		return
	}
	if !msg.Accept {
		m.refuse(f, cl.ID(), cl.Name(), "declined")
		return
	}
	// player.js:1485-1492: still in range to be followed (another street is 9999999 away).
	if f.Street() == "" || f.Street() != cl.Street() || followDistance(cl, f) > followMaxDistance {
		log.Printf("follow: %d too far from %d to follow (%s %s, %.0f px)", f.ID(), cl.ID(), f.Street(), cl.Street(), followDistance(cl, f))
		m.refuse(f, cl.ID(), cl.Name(), "far")
		m.h.SendLocked(cl, map[string]any{"t": "follow_too_far", "id": f.ID(), "name": f.Name()})
		return
	}
	// Since the ask: already following someone, or the chain now loops back to the asker.
	if m.leaderOf[f.ID()] != 0 {
		m.refuse(f, m.leaderOf[f.ID()], m.nameOf(m.leaderOf[f.ID()]), "already")
		return
	}
	if m.top(cl.ID()) == f.ID() {
		m.refuse(f, cl.ID(), cl.Name(), "self")
		return
	}
	set2 := m.of[cl.ID()]
	if set2 == nil {
		set2 = map[int64]bool{}
		m.of[cl.ID()] = set2
	}
	set2[f.ID()] = true
	m.leaderOf[f.ID()] = cl.ID()
	m.streets[f.ID()] = 1
	delete(m.sent, f.ID())
	// main.js:1498 apiLogAction('FOLLOW_START').
	log.Printf("follow: %d follows %d", f.ID(), cl.ID())
	m.h.SendLocked(f, map[string]any{"t": "follow_started", "id": cl.ID(), "name": cl.Name(), "streets": 1})
	m.sendList(cl.ID(), f.ID(), 0, "")
	// player.js:1506-1513: the leader's train, then every follower's.
	m.trainFor(cl.ID())
	for id := range set2 {
		m.trainFor(id)
	}
}

func followDistance(a, b *Client) float64 {
	ax, ay := a.Pos()
	bx, by := b.Pos()
	return math.Hypot(ax-bx, ay-by)
}

func (m *follows) nameOf(id int64) string {
	if cl := m.h.ClientLocked(id); cl != nil {
		return cl.Name()
	}
	return ""
}

func (m *follows) sendList(leader, added, removed int64, why string) {
	l := m.h.ClientLocked(leader)
	if l == nil {
		return
	}
	list := []map[string]any{}
	for id := range m.of[leader] {
		list = append(list, map[string]any{"id": id, "name": m.nameOf(id)})
	}
	sortByID(list)
	m.h.SendLocked(l, map[string]any{"t": "follow_list", "followers": list, "added": added, "removed": removed, "why": why})
}

func sortByID(list []map[string]any) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j]["id"].(int64) < list[j-1]["id"].(int64); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// —— Ending ——

// stopFollowing is player.js:3068-3090: off the leader's list, both trains updated.
func (m *follows) stopFollowing(id int64, why string) {
	leader := m.leaderOf[id]
	if leader == 0 {
		return
	}
	// main.js:1554 apiLogAction('FOLLOW_STOP').
	log.Printf("follow: %d stopped following %d (%s)", id, leader, why)
	delete(m.leaderOf, id)
	delete(m.streets, id)
	delete(m.sent, id)
	if set := m.of[leader]; set != nil {
		delete(set, id)
		if len(set) == 0 {
			delete(m.of, leader)
			m.cancelAway(leader)
			delete(m.tp, leader)
		}
	}
	if f := m.h.ClientLocked(id); f != nil {
		m.h.SendLocked(f, map[string]any{"t": "follow_ended", "id": leader, "name": m.nameOf(leader), "why": why})
	}
	m.sendList(leader, 0, id, why)
	m.trainFor(leader)
	for other := range m.of[leader] {
		m.trainFor(other)
	}
	m.trainFor(id)
}

// removeFollowers is player.js:3092-3105: every follower let go, each told why.
func (m *follows) removeFollowers(leader int64, why string) {
	set := m.of[leader]
	if len(set) == 0 {
		return
	}
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	log.Printf("follow: %d let %d followers go (%s)", leader, len(ids), why)
	for _, id := range ids {
		delete(m.leaderOf, id)
		delete(m.streets, id)
		delete(m.sent, id)
		if f := m.h.ClientLocked(id); f != nil {
			m.h.SendLocked(f, map[string]any{"t": "follow_ended", "id": leader, "name": m.nameOf(leader), "why": why})
		}
	}
	delete(m.of, leader)
	m.cancelAway(leader)
	delete(m.tp, leader)
	m.sendList(leader, 0, 0, why)
	for _, id := range ids {
		m.trainFor(id)
	}
	m.trainFor(leader)
}

// forget ends everything a player was part of: their follow, their followers, their asks.
func (m *follows) forget(id int64, why string) {
	m.stopFollowing(id, why)
	m.removeFollowers(id, why)
	if l := m.asking[id]; l != 0 {
		m.dropAsk(l, id)
	}
	for asker, stop := range m.asks[id] {
		stop()
		delete(m.asking, asker)
		if a := m.h.ClientLocked(asker); a != nil {
			m.refuse(a, id, m.nameOf(id), "gone")
		}
	}
	delete(m.asks, id)
	m.cancelAway(id)
	delete(m.tp, id)
}

func (m *follows) cancelAway(leader int64) {
	if stop := m.away[leader]; stop != nil {
		stop()
		delete(m.away, leader)
	}
}

// —— Moving ——

func (m *follows) left(cl *Client, _ string) {
	id := cl.ID()
	if len(m.of[id]) == 0 || m.away[id] != nil {
		return
	}
	m.away[id] = m.h.After(followAwayWait, func() {
		delete(m.away, id)
		if c := m.h.ClientLocked(id); c != nil && c.Street() == "" {
			m.removeFollowers(id, "private")
		}
	})
}

func (m *follows) joined(cl *Client) {
	id, street := cl.ID(), cl.Street()
	m.joinedAt[id] = m.h.Now()
	// As a follower: where the leader went, a street they were sent to on the way, or somewhere else.
	if leader := m.leaderOf[id]; leader != 0 {
		l := m.h.ClientLocked(leader)
		sent := m.sent[id]
		at := -1
		for i, s := range sent {
			if s == street {
				at = i
				break
			}
		}
		switch {
		case at >= 0:
			m.sent[id] = append(sent[:at:at], sent[at+1:]...)
			if len(m.sent[id]) == 0 {
				delete(m.sent, id)
			}
			m.streets[id]++
			m.h.SendLocked(cl, map[string]any{"t": "follow_arrived", "id": leader, "street": street, "streets": m.streets[id]})
		case l != nil && l.Street() == street:
			// Back on the leader's street (a new outfit joins again): nothing to count.
		default:
			m.stopFollowing(id, "left")
		}
	}
	// As a leader: the followers go where the leader went, or are let go at a private place.
	if set := m.of[id]; len(set) > 0 {
		m.cancelAway(id)
		if followPrivate(street) {
			m.removeFollowers(id, "private")
		} else {
			via := "walk"
			tp := m.tp[id]
			if tp != nil && tp.street == street && m.h.Now().Before(tp.exp) {
				via = "teleport"
			}
			m.goFollowers(cl, via)
			if via == "teleport" {
				delete(m.tp, id)
				m.teleported(cl, tp.kind)
			}
		}
	}
	m.trainFor(id)
}

// goFollowers tells each follower not already there where the leader went.
func (m *follows) goFollowers(l *Client, via string) {
	x, y := l.Pos()
	street := l.Street()
	for fid := range m.of[l.ID()] {
		f := m.h.ClientLocked(fid)
		if f == nil || f.Street() == street {
			continue
		}
		sent := append(m.sent[fid], street)
		if len(sent) > followSentKeep {
			sent = sent[len(sent)-followSentKeep:]
		}
		m.sent[fid] = sent
		log.Printf("follow: %d goes after %d to %s (%s)", fid, l.ID(), street, via)
		m.h.SendLocked(f, map[string]any{"t": "follow_go", "id": l.ID(), "name": l.Name(), "street": street, "x": x, "y": y, "dir": l.player.Dir, "via": via})
	}
}

// —— Teleporting with followers ——

func (m *follows) teleportSaid(cl *Client, frame json.RawMessage) {
	var msg struct {
		Street string `json:"street"`
		Kind   string `json:"kind"`
	}
	if json.Unmarshal(frame, &msg) != nil || !streetRe.MatchString(msg.Street) || followPrivate(msg.Street) {
		return
	}
	if len(m.of[cl.ID()]) == 0 || !m.allow(m.tpRates, cl.ID(), followTpBurst, followTpEvery) {
		return
	}
	kind := "plain"
	if msg.Kind == "map" {
		kind = "map"
	}
	// Said just after the join it names (the game tells on arrival): the followers were sent already.
	if cl.Street() == msg.Street && m.h.Now().Sub(m.joinedAt[cl.ID()]) <= followTeleportDone {
		m.teleported(cl, kind)
		return
	}
	m.tp[cl.ID()] = &followTeleport{street: msg.Street, kind: kind, exp: m.h.Now().Add(followTeleportWait)}
}

// teleported counts the followers at the teleport and again at teleport_complete.
func (m *follows) teleported(l *Client, kind string) {
	id, street := l.ID(), l.Street()
	n := len(m.of[id])
	if n == 0 {
		return
	}
	m.h.After(followTeleportDone, func() {
		lc := m.h.ClientLocked(id)
		if lc == nil {
			return
		}
		log.Printf("follow: %d teleported to %s with %d followers, %d at teleport_complete", id, street, n, len(m.of[id]))
		m.h.SendLocked(lc, map[string]any{"t": "follow_teleported", "street": street, "kind": kind, "followers": n, "complete": len(m.of[id])})
	})
}

// —— The Glitch Train ——

// chain is player.js:3126-3133: own followers, the leader's followers (this one among them), plus one.
func (m *follows) chain(id int64) int {
	n := len(m.of[id])
	if l := m.leaderOf[id]; l != 0 {
		n += len(m.of[l])
	}
	return n + 1
}

func (m *follows) trainFor(id int64) {
	cl := m.h.ClientLocked(id)
	if cl == nil {
		return
	}
	n := m.chain(id)
	on := n >= glitchTrainLength
	if !on && !m.train[id] && n == 1 && m.leaderOf[id] == 0 {
		return // never in a train, in no follow: nothing to say
	}
	if on {
		m.train[id] = true
	} else {
		delete(m.train, id)
	}
	m.h.SendLocked(cl, map[string]any{"t": "follow_train", "street": cl.Street(), "chain": n, "on": on})
}
