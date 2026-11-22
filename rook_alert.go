package main

import (
	"encoding/json"
	"sort"
)

// Rook alerts (register G799): who is being attacked right now, for every player's Butler.
//
// Glitch kept one rook_attack_tracker object (groups/rook_attack_tracker.js) holding the attacks
// under way: createRookAttack added each one (admin.js 403-405), every wing beat re-added one the
// tracker had lost (groups/rook_attack.js doWingBeat 202-208, "Hack to let butler announce attacks
// that didn't get tracked") and stopAttack removed it (rook_attack.js 440-442). Nothing broadcast
// it: each player's own Butler read it (items/include/butler_hints.js getRookNotification 664-694)
// when its owner came near it at home (items/bag_butler.js onPlayerCollision "near" 4003-4041,
// giveHint), for owners above level 4, an attack that was running, and said "Hurry, boss! I just
// heard on the butlervine that <street> is being attacked by the rook! Ur needs your help!".
//
// Here the server is the tracker: the attacks running are the streets whose Rook is over them
// (build_up, angry or stunned: startAttack 126 running, until it is beaten or flies off), and the
// Butler is the client's (js/butler.js rookAlert, js/features/butler-feature.js). So every game
// socket is told the list whenever it changes, and a client asks for it when it connects:
//
//	{"t":"rook_attacks"}                                           client: the attacks under way, please
//	{"t":"rook_attacks","attacks":[{"street":"GLI…","hub":"58","since":ms}]}   server: answer, and to
//	                                                                everyone when the list changes
//
// since is when the Rook arrived (the herald's end), so a client tells one attack from the next on
// the same street. A herald (30 s, ours) is not an attack yet; a Rook beaten or flown off is over.

// rookRunning is whether an attack counts as under way for the alerts.
func rookRunning(a *rookAttack) bool {
	return a != nil && (a.phase == "build_up" || a.phase == "angry" || a.phase == "stunned")
}

// alertList is the attacks under way, by street.
func (m *rook) alertList() []map[string]any {
	out := []map[string]any{}
	for _, tsid := range m.alertStreets() {
		a := m.streets[tsid].attack
		spot, _ := m.spot(tsid)
		out = append(out, map[string]any{"street": tsid, "hub": spot.hub, "since": a.arrivedAt.UnixMilli()})
	}
	return out
}

func (m *rook) alertStreets() []string {
	var list []string
	for tsid, s := range m.streets {
		if rookRunning(s.attack) {
			list = append(list, tsid)
		}
	}
	sort.Strings(list)
	return list
}

func (m *rook) alertMessage() map[string]any {
	return map[string]any{"t": "rook_attacks", "attacks": m.alertList()}
}

// alertChanged tells every game socket the list when it differs from the last one told.
func (m *rook) alertChanged() {
	sig := ""
	for _, tsid := range m.alertStreets() {
		sig += tsid + "@" + m.streets[tsid].attack.arrivedAt.String() + ";"
	}
	if sig == m.alertSig {
		return
	}
	m.alertSig = sig
	b, _ := json.Marshal(m.alertMessage())
	for _, cl := range m.h.clients {
		m.h.pushLocked(cl, b)
	}
}

// handleAttacks answers a client's ask (on connect and on reconnect).
func (m *rook) handleAttacks(cl *Client, _ json.RawMessage) {
	if !m.allow(cl) {
		return
	}
	m.h.SendLocked(cl, m.alertMessage())
}
