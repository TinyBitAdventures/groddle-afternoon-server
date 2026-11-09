package main

// The Crown Game Teleporter online (G387): pay 50 currants at the teleporter
// on The forgotten floor and join a Game of Crowns the way the shared
// instance manager joined you, with no ticket and nobody to challenge.
//
// The rules are GameServerJS's:
//   - items/game_teleporter.js verbs.activate 177-205 (the game asks "Do you
//     want to play <b>Game of Crowns</b> for <b>50c</b>?") and onConversation
//     211-237: stats_try_remove_currants(50) or "Oh dear, you don't have
//     enough currants to play!"; paid, the overlay, hit1 and
//     PAY_FIFTY_CURRANTS (the game's), "You are transported to a mystical
//     land of fun and adventure!" and 1 s later (apiSetTimerX 1000)
//     shared_instance_manager.playerJoinInstance(pc, 'it_game').
//   - groups/shared_instance_manager.js playerJoinInstance 36-87 and
//     findAvailableInstances 585-633: the fullest open game (not started,
//     under max_players, a spawn point free), else a new one.
//     onSharedInstanceEnter 89-229: Click to Start, games_auto_leave after
//     2 min unclicked (210). dismissOverlaysAndStart 231-342 with instance.js
//     can_start_game 579-602: min_retry_players (3) ready and nobody unready;
//     "You must wait for more players before this game can begin." with
//     Leave (259-266). The 50 currants are not given back for leaving: the
//     source never did.
//
// Ours:
//   - The teleporter's games are its own: a ticket's game stays its four
//     (crowns.go), and a teleporter player joins only a game the teleporter
//     made (findAvailableInstances would have offered a ticket game with a
//     free spawn point; a ticket fills all four).
//   - A game holds as many as the arena has starts (four; max_players 5,
//     but the instance has four spawn points: countSpawnPoints).
//   - The currants go through the site's ledger (POST groddle/v1/ledger/spend,
//     why game_teleporter, LedgerSpend.php), so the server, not the page,
//     decides there were 50. A spend that bought nothing (no answer from the
//     ledger, or the player gone, dead or busy when the second is up) is given
//     back (ledger/spend/refund), owed in -crowns-file until the site answers.
//   - The spend and its refund reach the page as crown_paid and
//     crown_paid_back, owed until acked (crown_ack), so a page that missed
//     one hears it on its next connect; the page applies each once by id (the
//     trade slice's applied list).
//
// Client to server:
//
//	{"t":"crown_teleport"}                      Sounds fun! at the teleporter
//
// Server to client:
//
//	{"t":"crown_paid","id":"gtele-…","spend":"gtele-…","cost":50,"text":"You are transported…"}
//	{"t":"crown_paid_back","id":"gtele-…-r","spend":"gtele-…","refund":"gtele-…-r","currants":50,"text":"…"}
//	{"t":"crown_refused","why":"poor|ledger|busy|dead|street|slow","text":"…"}
//	{"t":"crown_match", …, "open":true}          as crowns.go, one player or more
//	{"t":"crown_joined","match":"…","players":[{"id","name","slot"}],"name":"Ada"}   to the others when someone arrives

import (
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"time"
)

const (
	crownsTeleporterStreet = "GDOOSA8V29J24I3" // The forgotten floor (LDOOSA8V29J24I3), the one archived teleporter
	crownsTeleporterPrice  = 50                // game_teleporter.js:213
	crownsTeleportJoin     = time.Second       // game_teleporter.js:230 apiSetTimerX('playerJoinInstance', 1000, …)
	crownsTeleportWhy      = "game_teleporter"

	crownsTeleportPaid   = "You are transported to a mystical land of fun and adventure!" // game_teleporter.js:231
	crownsTeleportPoor   = "Oh dear, you don't have enough currants to play!"             // game_teleporter.js:236
	crownsTeleportLedger = "That did not go through, so nothing was spent. Try again in a moment."
	crownsTeleportStreet = "You need to be at the Crown Game Teleporter."
	crownsTeleportBack   = "Your Game of Crowns never started, so your 50 currants were given back."
)

var crownsSpendRe = regexp.MustCompile(`^gtele-[0-9a-f]{16}$`)

// crownsSpender is the site's ledger/spend as the teleporter uses it (httpLedger, party_spaces.go).
type crownsSpender interface {
	spend(op string, id int64, gave tradeGoods, why string) ledgerAnswer
	refundSpend(op string, id int64) ledgerAnswer
}

// crownRefund is a teleporter spend owed back until the site answers (-crowns-file).
type crownRefund struct {
	Op     string `json:"op"`
	User   int64  `json:"user"`
	tries  int
	asking bool
	retry  func() bool
}

// teleport is onConversation's yes (game_teleporter.js 211-237).
func (m *crowns) teleport(cl *Client, _ json.RawMessage) {
	if !m.allow(cl) {
		m.refuse(cl, "slow", "Slow down a little.")
		return
	}
	id := cl.ID()
	if m.busy(id) {
		m.refuse(cl, "busy", "You are already racing!")
		return
	}
	street := cl.Street()
	if inNaraka(street) {
		m.refuse(cl, "dead", raceTooDead)
		return
	}
	if strings.HasPrefix(street, "L") {
		street = "G" + street[1:] // the template's L tsid is the same street
	}
	if street != crownsTeleporterStreet {
		m.refuse(cl, "street", crownsTeleportStreet)
		return
	}
	if m.noSpend {
		m.refuse(cl, "ledger", crownsTeleportLedger)
		return
	}
	op := "gtele-" + m.newID()
	for !crownsSpendRe.MatchString(op) {
		op = "gtele-" + m.newID()
	}
	m.paying[id] = op
	name := cl.Name()
	paid := func(a ledgerAnswer) {
		switch a.Status {
		case "settled":
		case "refused":
			delete(m.paying, id)
			if oc := m.h.ClientLocked(id); oc != nil {
				m.refuse(oc, "poor", crownsTeleportPoor)
			}
			return
		default:
			// It may have settled unheard: the refund gives it back if it did, and voids it if not.
			delete(m.paying, id)
			m.refundTele(op, id)
			if oc := m.h.ClientLocked(id); oc != nil {
				m.refuse(oc, "ledger", crownsTeleportLedger)
			}
			return
		}
		log.Printf("crowns teleporter: %s (%d) paid %d currants (%s)", name, id, crownsTeleporterPrice, op)
		m.owe(id, op, map[string]any{"t": "crown_paid", "id": op, "spend": op, "cost": crownsTeleporterPrice, "text": crownsTeleportPaid})
		m.h.After(crownsTeleportJoin, func() { m.teleJoin(id, op) })
	}
	if m.spender == nil {
		paid(ledgerAnswer{Status: "settled"}) // -trade-ledger off: the client's word (development only)
		return
	}
	site := m.spender
	gave := tradeGoods{Items: []tradeItem{}, Currants: crownsTeleporterPrice}
	m.spawn(func() {
		a := site.spend(op, id, gave, crownsTeleportWhy)
		m.h.After(0, func() { paid(a) })
	})
}

// teleJoin is playerJoinInstance(pc, 'it_game') a second after paying: the fullest open game, else a new one.
func (m *crowns) teleJoin(id int64, op string) {
	if m.paying[id] != op {
		return
	}
	delete(m.paying, id)
	cl := m.h.ClientLocked(id)
	if cl == nil || !cl.Online() || m.busy(id) || inNaraka(cl.Street()) {
		// Gone, croaked or in another game meanwhile: the 50 bought nothing.
		m.refundTele(op, id)
		return
	}
	mt := m.openMatch()
	if mt == nil {
		mt = m.newOpenMatch()
	}
	mem := &crownMember{crownRef: crownRef{id, cl.Name()}, slot: m.freeSlot(mt), fresh: true}
	mt.members = append(mt.members, mem)
	m.matchOf[id] = mt
	mem.autoLeave = m.h.After(crownsAutoLeave, func() { m.idle(mt, id) })
	players := m.playersJSON(mt)
	m.h.SendLocked(cl, map[string]any{"t": "crown_match", "match": mt.id, "street": mt.street, "title": m.a.Title,
		"slot": mem.slot, "spawn": m.a.Starts[mem.slot%len(m.a.Starts)], "players": players, "open": true,
		"playLine": m.a.PlayLineY, "locks": m.a.Locks, "winMs": m.a.WinMs, "autoLeave": int(crownsAutoLeave.Seconds())})
	for _, o := range mt.members {
		if o.gone || o.ID == id {
			continue
		}
		if oc := m.h.ClientLocked(o.ID); oc != nil {
			m.h.SendLocked(oc, map[string]any{"t": "crown_joined", "match": mt.id, "players": players, "name": mem.Name})
		}
	}
	log.Printf("crowns %s: %s came by the teleporter (%d here)", mt.id, mem.Name, len(players))
}

// crownsSeats is how many one game holds: the arena's starts (countSpawnPoints), at most max_players.
func (m *crowns) crownsSeats() int {
	return min(len(m.a.Starts), m.a.Players.Max)
}

func liveMembers(mt *crownMatch) int {
	n := 0
	for _, o := range mt.members {
		if !o.gone {
			n++
		}
	}
	return n
}

// openMatch is findAvailableInstances plus playerJoinInstance's choice: of the teleporter's games
// still waiting with a seat free, the one with the most players (the oldest on a tie).
func (m *crowns) openMatch() *crownMatch {
	var best *crownMatch
	bestN := 0
	for _, mt := range m.matches {
		if !mt.open || mt.closed || mt.status != "waiting" {
			continue
		}
		n := liveMembers(mt)
		if n == 0 || n >= m.crownsSeats() {
			continue
		}
		if best == nil || n > bestN || (n == bestN && mt.id < best.id) {
			best, bestN = mt, n
		}
	}
	return best
}

func (m *crowns) newOpenMatch() *crownMatch {
	id := m.newID()
	for m.matches[id] != nil || !crownsMatchRe.MatchString(id) {
		id = m.newID()
	}
	mt := &crownMatch{id: id, street: crownsStreet(id), status: "waiting", round: 1, locks: make([]time.Time, len(m.a.Locks)), open: true}
	m.matches[id] = mt
	mt.life = m.h.After(crownsMaxLife, func() {
		if mt.status != "done" {
			m.end(mt, nil, "timeout")
		}
	})
	return mt
}

// freeSlot is the first start nobody in the game stands on (get_spawn_point).
func (m *crowns) freeSlot(mt *crownMatch) int {
	used := map[int]bool{}
	for _, o := range mt.members {
		if !o.gone {
			used[o.slot] = true
		}
	}
	for i := 0; ; i++ {
		if !used[i] {
			return i
		}
	}
}

// refundTele gives a teleporter spend back: owed in the state file until the site answers.
func (m *crowns) refundTele(op string, id int64) {
	if m.spender == nil {
		// -trade-ledger off: nothing went through a ledger; the page gives back what it took.
		m.paidBack(op, id, crownsTeleporterPrice)
		return
	}
	rf := m.refunds[op]
	if rf == nil {
		rf = &crownRefund{Op: op, User: id}
		m.refunds[op] = rf
		m.save()
	}
	m.askRefund(rf)
}

// askRefund asks the site for one owed refund (ledger/spend/refund), again after a backoff.
func (m *crowns) askRefund(rf *crownRefund) {
	if rf.asking || rf.retry != nil || m.spender == nil || m.refunds[rf.Op] != rf {
		return
	}
	rf.asking = true
	site, op, user := m.spender, rf.Op, rf.User
	m.spawn(func() {
		a := site.refundSpend(op, user)
		m.h.After(0, func() {
			rf.asking = false
			if m.refunds[op] != rf {
				return
			}
			switch a.Status {
			case "void":
				delete(m.refunds, op)
				var back int64
				if len(a.Gave) > 0 {
					back = a.Gave[0].Currants
				}
				log.Printf("crowns teleporter: spend %s for %d given back (%d currants)", op, user, back)
				m.paidBack(op, user, back) // saves
				return
			case "refused", "settled":
				delete(m.refunds, op)
				log.Printf("crowns teleporter: spend %s for %d: nothing was spent, nothing to give back", op, user)
				// The page may still hold the spend unapplied (it never heard): the refund id settles both.
				m.paidBack(op, user, 0)
				return
			}
			rf.tries++
			d := refundBackoff(rf.tries)
			log.Printf("crowns teleporter: spend %s for %d: no answer from the ledger, asking again in %s", op, user, d)
			rf.retry = m.h.After(d, func() {
				rf.retry = nil
				m.askRefund(rf)
			})
		})
	})
}

// paidBack tells the page (now and on every connect until acked) that a spend came back.
func (m *crowns) paidBack(op string, id, currants int64) {
	msg := map[string]any{"t": "crown_paid_back", "id": op + "-r", "spend": op, "refund": op + "-r", "currants": currants}
	if currants > 0 {
		msg["text"] = crownsTeleportBack
	}
	m.owe(id, op+"-r", msg)
}
