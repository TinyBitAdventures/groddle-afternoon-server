package main

// The administrators' console's Rook calls (groddle-afternoon-site
// src/AdminConsole.php, /console/; Austin 2026-11-03, after three players on
// Groddle Forest Junction could not fight a Rook: with others on the street a
// Focusing Orb needs Martial Imagination). Signed site calls as in admin.go
// (POST /ws?call=<call>, X-Groddle-Service over /realtime/v1/<call> and the
// body, 60 s, each nonce once); never socket messages.
//
//	rooks    {}                    200 {"status":"ok","attacks":[{"street":"GA5…","hub":"89","phase":"build_up",
//	                                    "angry":"","health":200,"maxHealth":200,"stun":0,"maxStun":125,"crowd":2,
//	                                    "wingBeats":1,"clock":540000,"orbs":0,"primed":false,"scheduled":true,
//	                                    "players":[{"id":7,"name":"Ada"}]}]}
//	                                    every street with a Rook over it now (herald to its clear), by street id;
//	                                    the site names the street from its street index
//	rook_end {"street":"GA5…"}     200 {"status":"ended","street":"GA5…","phase":"angry","players":2}
//	                                    | {"status":"ending"} (it is already flying off, or defeated and clearing)
//	                                    | {"status":"none"} (no Rook over that street)
//	                               400 {"status":"bad"} not a street id
//
// Ending is the way an abandoned fight ends after its third wing beat
// (rook.go wingBeat's departed path), not a defeat: the Rook flies off, the
// street's players get the usual "depart" (the game's THE ROOK FLIES OFF) and
// 6 s later the "clear" with defeated false; the street rests its hour and the
// hub's schedule goes on (rook.go clear, which writes the state file). What it
// rooked stays rooked until revived. Nothing is paid: no defeat iMG, no
// witness iMG, no essence_of_rookswort badge count, no stun or prime iMG
// (orbs fold without firing, an open primed shrine closes without its
// prime_done), and no wing beat costs either.
//
// Glitch's own admin tools did otherwise. The god command /kill_rook
// (main.js 2675-2680) called location.stopRookAttack (inc_rook.js 96-100), so
// rook_attack.js stopAttack 428-451, which on a living Rook runs
// rookStateChange('dead') (433): inc_rook.js 187-206 then shows THE ROOK IS
// DEFEATED! and pays every player there the defeat iMG (damage x 2 + 50, or 50
// for witnessing) and the badge count; rookAttackSetInfo with health 0
// (rook_attack.js 521-555, the admin tool's setter) is the same defeat after
// 15 s. admin.js createRookAttack 391-408 only starts a hub's attack, and
// items/include/rook.js adminRook 338-356 rooks one animal. Austin asked for no
// rewards, so this follows the departure instead.

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"time"
)

func init() {
	serviceHandlers["rooks"] = adminRooks
	serviceHandlers["rook_end"] = adminRookEnd
}

// rookOf is the hub's Rook module (nil in a build without it).
func (h *Hub) rookOf() *rook {
	m, _ := h.provided["rook.self"].(*rook)
	return m
}

func adminRooks(h *Hub, body []byte) (int, any) {
	if !json.Valid(body) {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	attacks := []map[string]any{}
	m := h.rookOf()
	if m == nil {
		return http.StatusOK, map[string]any{"status": "ok", "attacks": attacks}
	}
	now := h.now()
	for tsid, s := range m.streets {
		a := s.attack
		if a == nil {
			continue
		}
		type who struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		players := []who{}
		for _, cl := range h.StreetLocked(tsid) {
			if !cl.closed {
				players = append(players, who{ID: cl.ID(), Name: cl.Name()})
			}
		}
		sort.Slice(players, func(i, j int) bool { return players[i].ID < players[j].ID })
		attacks = append(attacks, map[string]any{
			"street": tsid, "hub": m.w.streets[tsid].hub, "phase": a.phase, "angry": a.angry,
			"health": a.health, "maxHealth": a.maxHealth, "stun": min(a.stun, a.maxStun), "maxStun": a.maxStun,
			"crowd": a.crowd, "wingBeats": a.wingBeats, "clock": rookMsLeft(now, a.clockAt),
			"orbs": len(a.orbs), "primed": a.primed != nil, "scheduled": a.rests, "players": players,
		})
	}
	sort.Slice(attacks, func(i, j int) bool { return attacks[i]["street"].(string) < attacks[j]["street"].(string) })
	return http.StatusOK, map[string]any{"status": "ok", "attacks": attacks}
}

func adminRookEnd(h *Hub, body []byte) (int, any) {
	var c struct {
		Street string `json:"street"`
	}
	if json.Unmarshal(body, &c) != nil || !streetRe.MatchString(c.Street) {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.rookOf()
	if m == nil {
		return http.StatusOK, statusOf("none")
	}
	s := m.streets[c.Street]
	if s == nil || s.attack == nil {
		return http.StatusOK, statusOf("none")
	}
	a := s.attack
	if a.phase == "departed" || a.phase == "dead" {
		return http.StatusOK, statusOf("ending")
	}
	was := a.phase
	players := len(h.StreetLocked(c.Street))
	m.endByAdmin(a)
	log.Printf("rook: an administrator sent the Rook off %s (was %s, wing beats %d, health %d/%d, %d there)", c.Street, was, a.wingBeats, a.health, a.maxHealth, players)
	return http.StatusOK, map[string]any{"status": "ended", "street": c.Street, "phase": was, "players": players}
}

// endByAdmin sends the Rook off its street as after its last wing beat, paying nothing (header).
func (m *rook) endByAdmin(a *rookAttack) {
	for name := range a.timers {
		m.cancel(a, name)
	}
	a.wingAt = time.Time{}
	for by := range a.orbs {
		m.dropOrb(a, by)
	}
	if p := a.primed; p != nil {
		a.primed = nil
		if p.stop != nil {
			p.stop()
		}
	}
	a.angry = ""
	m.departLocked(a, map[string]any{"type": "depart", "admin": true})
}
