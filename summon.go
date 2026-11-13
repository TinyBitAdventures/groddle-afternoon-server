package main

// Summoning another player (Teleportation V): a player who knows
// Teleportation V asks another player online to come to where they stand,
// the other says Yes or No, and on Yes the summoner pays and the other is
// moved there. The server decides whether the two may (both online, no block,
// the places), keeps the one request open, and moves the target; the
// summoner's game decides and pays what is its own (the skill, the energy,
// the day's count, the cooldown, Pooped, death), and the target's game refuses
// for what is its own (dead, in the tutorial or a quest room, contraband), as
// the server cannot see either save. The rules are GameServerJS main.js
// doPcVerbMenu 3661-3715 (the Summon <name> entry) and doPcMenu 3878-3935
// (the command), players/inc_teleportation.js teleportation_accept_summons
// 602-616, teleportation_summon 618-704, teleportation_cancel_summons 706-727,
// teleportation_can_summon 729-835 and teleportation_get_max_summons 969-976;
// the copy players read is theirs (js/teleportation.js SUMMON_TEXT, js/summon-mp.js).
//
// Client to server:
//
//	{"t":"summon","id":8}                                  Summon <name> (the summoner's game has checked its own side)
//	{"t":"summon_answer","from":7,"accept":true}           the target's Yes (No: false; "timeout":true when the prompt ran out)
//	{"t":"summon_answer","from":7,"accept":false,"why":"dead"}   the target's game refusing without a prompt: dead, busy, contraband
//	{"t":"summon_paid","id":8,"ok":true}                   the summoner's game paid for the summons (ok false: it could not, and said why itself)
//
// Server to client:
//
//	{"t":"summon_waiting","id":8,"name":"Bo","timeout":30}                       to the summoner: "Waiting for Bo to confirm."
//	{"t":"summon_asked","from":{"id":7,"name":"Ada"},"street":"GA5…","party":"Toxic Moon","timeout":30}   to the target ("party" only into a Party Space)
//	{"t":"summon_gone","from":7}                                                 to the target: that summons is over (offline, answered elsewhere)
//	{"t":"summon_refused","id":8,"name":"Bo","why":"…","text":"…"}               to the summoner
//	{"t":"summon_pay","id":8,"name":"Bo"}                                        to the summoner: Bo said yes, pay now (teleportation_summon)
//	{"t":"summon_declined","id":8,"name":"Bo","text":"Bo did not accept your summons."}
//	{"t":"summon_go","from":{"id":7,"name":"Ada"},"street":"GA5…","x":120,"y":-80,"party":false}   to the target: go there
//	{"t":"summon_failed","text":"Oops. Something happened with that teleport."}  to the target, after a Yes that could not go through
//
// Rules kept from the source:
//   - The entry is offered for another player online and unblocked
//     (main.js:3661); the summoner's own checks are teleportation_can_teleport
//     and the rest of can_summon, in the summoner's game.
//   - One summons at a time: "You can only summon one player at a time."
//     (inc_teleportation.js:733-738, !summons_uid until it is answered or the
//     30 second prompt runs out, main.js:3890-3897).
//   - A target with too many prompts is "too busy to be summoned"
//     (main.js:3666-3673; prompts_over_limit is 100 prompts of any kind,
//     inc_prompts.js:83-86; here summonMaxHeld summonses waiting).
//   - Not out of an instance other than a Party Space: "You can't summon people
//     here." (inc_teleportation.js:776-797). In the remake that is any street
//     id with an underscore (RACE_, CROWNS_, MAYHEM_, TRAIN_ …) and no street
//     at all (a quest room or a scene, which the client leaves the street for).
//   - Into a home (799-806): the owner summons anyone into their own home
//     street, house or Tower (pols_canEnter is not asked of the owner); a
//     visitor on someone's home street or Tower floor, which are public
//     (inc_pols.js 448-458), summons anyone that owner does not block; from
//     inside someone else's house it is "You can't summon them here." (ours:
//     the source asked pols_canEnter and then left the target at the door
//     outside, 663-665; a target's key is the site's, not known here). The
//     target's game fetches the site's pass as for any visit (homes.go
//     GateJoin), and a summons into the owner's house lets the target in for
//     that one arrival (homes.go homes.summoned), never as a key.
//   - Into a Party Space (682-687, main.js:3899-3913): the target is asked
//     "<name> would like to invite you to their party in <space>. Do you
//     accept?", and Yes joins the summoner's party and enters the space
//     (party_invite_accepted with auto_space, party_enter_space) at its
//     arrival point. No tells the summoner the invite was declined as well
//     (715, party_invite_declined).
//   - Yes is checked again (teleportation_summon 627), then the summoner pays
//     (631-645) and sees "<name> is on their way!" (648-655), and the target
//     goes to the summoner's street 20 px to their right (658-662), their
//     followers let go (673). A Yes that cannot go through is "Oops.
//     Something happened with that teleport." (614).
//   - No, or the 30 seconds running out, is "<name> did not accept your
//     summons." (608-610, 718-725).
//
// Ours: the request rate, summonMaxHeld, summonPayWait (the summoner's game
// has that long to pay), a target in another party or a full party refused
// up front for a Party Space (the source sent the prompt and failed on Yes),
// and the server's refusal words for a target's own why.

import (
	"encoding/json"
	"log"
	"strings"
	"time"
)

const (
	summonAskWait  = 30 * time.Second // main.js:3893, 3907, 3922: timeout 30
	summonAskGrace = 2 * time.Second  // ours: the target's own timeout answer comes first
	summonPayWait  = 10 * time.Second // ours: the summoner's game pays within this
	summonMaxHeld  = 5                // ours: the source's limit was 100 prompts of any kind
	summonBurst    = 3
	summonEvery    = 5 * time.Second
	summonSpotDX   = 20 // inc_teleportation.js:660 x: this.x+20

	summonTextOne        = "You can only summon one player at a time."                                      // inc_teleportation.js:736
	summonTextHere       = "You can't summon people here."                                                  // 794
	summonTextThem       = "You can't summon them here."                                                    // 803
	summonTextOtherParty = "They are in another party."                                                     // 781
	summonTextFull       = "Your party is full."                                                            // 788
	summonTextCant       = "They cannot be summoned right now."                                             // 757
	summonTextDead       = "They are dead."                                                                 // 750
	summonTextContraband = "They're carrying some fragile contraband, and it won't survive a summoning"     // 743
	summonTextBusy       = "They are too busy to be summoned."                                              // main.js:3670
	summonTextDeclined   = " did not accept your summons."                                                  // inc_teleportation.js:719
	summonTextOops       = "Oops. Something happened with that teleport."                                   // 614
	summonTextOffline    = "That player is currently offline."                                              // as party.go (inc_party.js)
	summonTextBlocked    = "You are blocking or blocked by that player."                                    // as party.go (inc_trading.js:128)
	summonTextSelf       = "You can't summon yourself."                                                     // ours: the menu never offered it
	summonTextKicked     = " has been removed from your party - only the party creator can re-invite them." // inc_party.js:57-58
)

// summonWhy is the summoner's line for the target game's own refusals (summon_answer why).
var summonWhy = map[string]string{
	"dead":       summonTextDead,
	"busy":       summonTextCant,
	"contraband": summonTextContraband,
	"crowded":    summonTextBusy,
}

func init() { modules = append(modules, newSummons) }

type summonReq struct {
	from, to         int64
	fromName, toName string
	paying           bool   // the target said yes; the summoner's game is paying
	party            string // the Party Space's label when it was asked from one
	stop             func() bool
}

type summons struct {
	h     *Hub
	by    map[int64]*summonReq           // summoner -> their one open summons
	held  map[int64]map[int64]*summonReq // target -> summoner -> summons
	rates map[int64]*bucket
}

func newSummons(h *Hub) Module {
	return &summons{h: h, by: map[int64]*summonReq{}, held: map[int64]map[int64]*summonReq{}, rates: map[int64]*bucket{}}
}

func (m *summons) Register(r *Registry) {
	r.Handle("summon", m.ask)
	r.Handle("summon_answer", m.answer)
	r.Handle("summon_paid", m.paid)
	r.OnDisconnect(func(cl *Client) {
		id := cl.ID()
		if q := m.by[id]; q != nil {
			m.end(q)
			m.send(q.to, map[string]any{"t": "summon_gone", "from": id})
		}
		for _, q := range m.held[id] {
			m.end(q)
			m.declined(q)
		}
		delete(m.rates, id)
	})
}

func (m *summons) send(id int64, msg any) {
	if cl := m.h.ClientLocked(id); cl != nil {
		m.h.SendLocked(cl, msg)
	}
}

func (m *summons) refuse(cl *Client, id int64, name, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "summon_refused", "id": id, "name": name, "why": why, "text": text})
}

// end closes a summons: no longer open for either player.
func (m *summons) end(q *summonReq) {
	if q.stop != nil {
		q.stop()
		q.stop = nil
	}
	if m.by[q.from] == q {
		delete(m.by, q.from)
	}
	if set := m.held[q.to]; set != nil && set[q.from] == q {
		delete(set, q.from)
		if len(set) == 0 {
			delete(m.held, q.to)
		}
	}
}

// declined is teleportation_cancel_summons 706-727 with a target.
func (m *summons) declined(q *summonReq) {
	if q.party != "" {
		if in, ok := m.h.ProvidedLocked("party.summonIn").(func(from, to int64) bool); ok && !in(q.from, q.to) {
			// 712-716: party_invite_declined (party.js decline_invite's line to the inviter).
			m.send(q.from, map[string]any{"t": "party_activity", "text": q.toName + " has declined your invitation"})
		}
	}
	m.send(q.from, map[string]any{"t": "summon_declined", "id": q.to, "name": q.toName, "text": q.toName + summonTextDeclined})
}

// place is where the summoner stands, as the source checked it (can_summon 776-806):
// ok, or the refusal; party is the Party Space's label when they are in one.
func (m *summons) place(from, to *Client) (why, text, party string) {
	street := from.Street()
	switch {
	case street == "":
		return "here", summonTextHere, ""
	case strings.HasPrefix(street, partySpaceStreet):
		check, ok := m.h.ProvidedLocked("party.summonCheck").(func(from, to int64) (string, string))
		if !ok {
			return "here", summonTextHere, ""
		}
		label, why := check(from.ID(), to.ID())
		switch why {
		case "":
			return "", "", label
		case "other_party":
			return why, summonTextOtherParty, ""
		case "full":
			return why, summonTextFull, ""
		case "kicked":
			return why, to.Name() + summonTextKicked, ""
		}
		return "here", summonTextHere, ""
	case strings.HasPrefix(street, homeStreetPrefix), strings.HasPrefix(street, houseStreetPrefix), strings.HasPrefix(street, towerStreetPrefix):
		return m.homePlace(from, to, street)
	case strings.Contains(street, "_"):
		return "here", summonTextHere, ""
	}
	return "", "", ""
}

// homePlace is can_summon 799-806 in a home: the owner's own (street, house, Tower floor), or
// someone's public home street or Tower floor the owner does not block the target from.
func (m *summons) homePlace(from, to *Client, street string) (why, text, party string) {
	owner, house := homeOwner(street)
	if t := towerOwner(street); t > 0 {
		owner, house = t, false
	}
	switch {
	case owner == 0:
		return "here", summonTextHere, ""
	case owner == from.ID():
		return "", "", ""
	case house:
		return "them", summonTextThem, ""
	}
	if ign, ok := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool); ok && ign(owner, to.ID()) {
		return "them", summonTextThem, ""
	}
	return "", "", ""
}

// target is the target's side the server can see: online, unblocked, on a street.
func (m *summons) target(cl *Client, id int64) (to *Client, why, text string) {
	if id == cl.ID() {
		return nil, "self", summonTextSelf
	}
	to = m.h.ClientLocked(id)
	if to == nil {
		return nil, "offline", summonTextOffline
	}
	if blockedBetween(m.h, cl.ID(), id) {
		return nil, "blocked", summonTextBlocked
	}
	// 754-759: in the tutorial or a skill quest. Off every street here: a quest room or a scene.
	if to.Street() == "" {
		return nil, "busy", summonTextCant
	}
	return to, "", ""
}

// ask is main.js doPcMenu 'summon' 3878-3935.
func (m *summons) ask(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.ID <= 0 {
		m.h.ErrorLocked(cl, "bad_summon", "No player specified")
		return
	}
	b := m.rates[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.rates[cl.ID()] = b
	}
	if !b.take(m.h.Now(), summonBurst, summonEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	to, why, text := m.target(cl, msg.ID)
	name := ""
	if to != nil {
		name = to.Name()
	}
	if why != "" {
		m.refuse(cl, msg.ID, name, why, text)
		return
	}
	if m.by[cl.ID()] != nil {
		m.refuse(cl, msg.ID, name, "one", summonTextOne)
		return
	}
	if len(m.held[msg.ID]) >= summonMaxHeld {
		m.refuse(cl, msg.ID, name, "crowded", summonTextBusy)
		return
	}
	why, text, party := m.place(cl, to)
	if why != "" {
		m.refuse(cl, msg.ID, name, why, text)
		return
	}
	q := &summonReq{from: cl.ID(), to: msg.ID, fromName: cl.Name(), toName: name, party: party}
	m.by[q.from] = q
	if m.held[q.to] == nil {
		m.held[q.to] = map[int64]*summonReq{}
	}
	m.held[q.to][q.from] = q
	q.stop = m.h.After(summonAskWait+summonAskGrace, func() {
		if m.by[q.from] != q || q.paying {
			return
		}
		q.stop = nil
		m.end(q)
		m.send(q.to, map[string]any{"t": "summon_gone", "from": q.from})
		m.declined(q)
	})
	seconds := int(summonAskWait / time.Second)
	m.h.SendLocked(cl, map[string]any{"t": "summon_waiting", "id": q.to, "name": q.toName, "timeout": seconds})
	asked := map[string]any{"t": "summon_asked", "from": map[string]any{"id": q.from, "name": q.fromName}, "street": cl.Street(), "timeout": seconds}
	if party != "" {
		asked["party"] = party
	}
	m.h.SendLocked(to, asked)
}

// answer is teleportation_accept_summons 602-616: the target's Yes, No or timeout.
func (m *summons) answer(cl *Client, frame json.RawMessage) {
	var msg struct {
		From   int64  `json:"from"`
		Accept bool   `json:"accept"`
		Why    string `json:"why"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.From <= 0 {
		return
	}
	q := m.held[cl.ID()][msg.From]
	if q == nil || q.paying {
		if q == nil {
			m.h.SendLocked(cl, map[string]any{"t": "summon_gone", "from": msg.From})
		}
		return
	}
	if !msg.Accept {
		m.end(q)
		if text, own := summonWhy[msg.Why]; own {
			if from := m.h.ClientLocked(q.from); from != nil {
				m.refuse(from, q.to, q.toName, msg.Why, text)
			}
			return
		}
		m.declined(q)
		return
	}
	from := m.h.ClientLocked(q.from)
	if from == nil {
		m.end(q)
		m.h.SendLocked(cl, map[string]any{"t": "summon_failed", "text": summonTextOops})
		return
	}
	// teleportation_summon 627: can_summon again, where the summoner stands now.
	if _, why, text := m.target(from, cl.ID()); why != "" {
		m.fail(q, from, cl, why, text)
		return
	}
	why, text, party := m.place(from, cl)
	if why != "" {
		m.fail(q, from, cl, why, text)
		return
	}
	q.party = party
	q.paying = true
	if q.stop != nil {
		q.stop()
	}
	q.stop = m.h.After(summonPayWait, func() {
		if m.by[q.from] != q {
			return
		}
		q.stop = nil
		m.end(q)
		m.send(q.to, map[string]any{"t": "summon_failed", "text": summonTextOops})
	})
	m.h.SendLocked(from, map[string]any{"t": "summon_pay", "id": q.to, "name": q.toName})
}

// fail is a Yes that cannot go through: the summoner hears why, the target "Oops" (614).
func (m *summons) fail(q *summonReq, from, to *Client, why, text string) {
	m.end(q)
	m.refuse(from, q.to, q.toName, why, text)
	m.h.SendLocked(to, map[string]any{"t": "summon_failed", "text": summonTextOops})
}

// paid is the rest of teleportation_summon 647-701 once the summoner's game has paid.
func (m *summons) paid(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID int64 `json:"id"`
		OK bool  `json:"ok"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	q := m.by[cl.ID()]
	if q == nil || !q.paying || q.to != msg.ID {
		return
	}
	m.end(q)
	to := m.h.ClientLocked(q.to)
	if to == nil {
		return
	}
	if !msg.OK {
		m.h.SendLocked(to, map[string]any{"t": "summon_failed", "text": summonTextOops})
		return
	}
	why, text, party := m.place(cl, to)
	if why != "" {
		m.refuse(cl, q.to, q.toName, why, text)
		m.h.SendLocked(to, map[string]any{"t": "summon_failed", "text": summonTextOops})
		return
	}
	q.party = party
	// 673: pc.removeFollowers(); and the target's own follow ends, as a teleport ends it (player.js:671).
	if fn, ok := m.h.ProvidedLocked("follow.summoned").(func(int64)); ok {
		fn(q.to)
	}
	from := map[string]any{"id": q.from, "name": q.fromName}
	street := cl.Street()
	if q.party != "" {
		// 682-687: the party first (party_invite_accepted with auto_space), then the space.
		join, ok := m.h.ProvidedLocked("party.summonJoin").(func(from int64, to *Client) bool)
		if !ok || !join(q.from, to) {
			m.refuse(cl, q.to, q.toName, "here", summonTextHere)
			m.h.SendLocked(to, map[string]any{"t": "summon_failed", "text": summonTextOops})
			return
		}
		log.Printf("summon: %d summoned %d into Party Space %s", q.from, q.to, street)
		m.h.SendLocked(to, map[string]any{"t": "summon_go", "from": from, "street": street, "party": true})
		return
	}
	// Into the owner's own house: in for this one arrival (homes.go), as the source's teleport was.
	if owner, house := homeOwner(street); house && owner == q.from {
		if let, ok := m.h.ProvidedLocked("homes.summoned").(func(visitor, owner int64)); ok {
			let(q.to, owner)
		}
	}
	x, y := cl.Pos()
	log.Printf("summon: %d summoned %d to %s", q.from, q.to, street)
	m.h.SendLocked(to, map[string]any{"t": "summon_go", "from": from, "street": street, "x": x + summonSpotDX, "y": y, "party": false})
}
