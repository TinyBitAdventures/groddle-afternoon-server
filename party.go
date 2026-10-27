package main

// Parties (multiplayer phase 4): players form a party and talk on its own
// channel, as in the original. The rules are GameServerJS groups/party.js,
// players/inc_party.js and main.js (doPartyInvite 4581, doPartyChat 1829,
// doPartyLeave 4591); the copy players read is theirs. Parties live in memory,
// keyed by character id, so a replaced socket or a short drop keeps them; a
// server restart forgets them.
//
// Client to server:
//
//	{"t":"party_invite","id":7}          invite someone online by player id
//	{"t":"party_invite","name":"Ada"}    or by the name of someone online
//	{"t":"party_accept","from":7}        answer the invite player 7 sent
//	{"t":"party_decline","from":7}
//	{"t":"party_leave"}
//	{"t":"party_chat","text":"hi"}
//
// Server to client:
//
//	{"t":"party","party":{"leader":7,"members":[Member…]}}   on every change; "party":null when you are in none
//	{"t":"party_invited","from":{"id":7,"name":"Ada"},"text":"…","timeout":89}
//	{"t":"party_uninvited","from":7}     that invite is gone (answered elsewhere, expired, party closed)
//	{"t":"party_activity","text":"…"}    a line for the party chat pane
//	{"t":"party_said","id":7,"name":"Ada","text":"hi","at":1730000000000}
//
// Member is {"id","name","leader","online","away","street"}: away while a
// dropped socket has partyGrace to come back, street the tsid they stand on
// ("" in a private place or offline).
//
// Rules kept from the source:
//   - Inviting with no party makes one with you as its only member and leader
//     (inc_party.js party_invite 17-37); it is not shown until a second member
//     joins (party_members 370-378), and it closes quietly when its last invite
//     is answered or expires with nobody else in it (party.js decline_invite 342).
//   - An invite fails if one is already out to them, they are offline, already
//     in a party, or the party is full (party.js send_invite 26-59, size
//     config_prod.js:40 max_party_size 71). It expires after 90 s
//     (config_base.js:12 party_invite_timeout); the prompt shows for 89 (inc_party.js:131).
//   - Accepting while in another party leaves it and brings your own pending
//     invites along (party.js accept_invite 236-249).
//   - Leaving: under two members the party disbands; a leaving leader passes the
//     lead to the longest-standing member (party.js leave 150-164, promote_leader).
//     The leader has no other powers outside Party Spaces.
//   - Going offline tells the others; once nobody in it is online the party
//     closes (party.js player_logout 387-413). Members who are offline stay in it.
//   - Party chat reaches members only, and refuses slash commands (main.js:1833).
//
// Ours: partyGrace (a socket that drops has a minute to come back before it
// counts as logging out), the invite rate and the caps on pending invites.
// Party Spaces (party.js create_space and the rest, the creator's kick and the
// kicked list) are party_spaces.go, on this module.

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	partyMaxSize       = 71               // config_prod.js:40 max_party_size
	partyInviteTimeout = 90 * time.Second // config_base.js:12 party_invite_timeout
	partyPromptSeconds = 89               // inc_party.js:131 timeout: party_invite_timeout-1
	partyGrace         = 60 * time.Second // ours: a dropped socket's time to come back
	partySettle        = 300 * time.Millisecond
	partyMaxInvites    = 20 // ours: invites one party may have waiting
	partyMaxHeld       = 5  // ours: invites one player may have waiting for them
	partyInviteBurst   = 5
	partyInviteEvery   = 2 * time.Second
	partyMaxName       = 64
)

func init() { modules = append(modules, newParties) }

type partyMember struct {
	id     int64
	name   string
	leader bool
	online bool // false once partyGrace has run out (inc_party.js party_logout)
}

type partyInvite struct {
	from, to         int64
	fromName, toName string
	expires          time.Time
	stop             func() bool
}

type party struct {
	members []*partyMember // in join order: promote_leader picks the first
	invites map[int64]*partyInvite
	settle  func() bool // a roster push waiting for a street change to finish

	// Party Spaces (party_spaces.go).
	space    *partySpace
	starting bool           // a pack's spend is with the ledger
	kicked   map[int64]bool // party.js kicked_players (kicked 129-135)
}

type bucket struct {
	tokens float64
	at     time.Time
}

func (b *bucket) take(now time.Time, burst float64, every time.Duration) bool {
	if b.at.IsZero() {
		b.tokens = burst
	} else {
		b.tokens = math.Min(burst, b.tokens+now.Sub(b.at).Seconds()/every.Seconds())
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type parties struct {
	h       *Hub
	of      map[int64]*party
	all     map[*party]struct{}
	away    map[int64]func() bool // grace timers for dropped sockets
	chat    map[int64]*bucket
	invites map[int64]*bucket
	names   map[int64]string // everyone connected, for invites by name

	spaces partySpaces // party_spaces.go
}

func newParties(h *Hub) Module {
	m := &parties{h: h, of: map[int64]*party{}, all: map[*party]struct{}{}, away: map[int64]func() bool{},
		chat: map[int64]*bucket{}, invites: map[int64]*bucket{}, names: map[int64]string{}}
	m.initSpaces()
	return m
}

func (m *parties) Register(r *Registry) {
	r.Handle("party_invite", m.invite)
	r.Handle("party_accept", func(cl *Client, frame json.RawMessage) { m.answer(cl, frame, true) })
	r.Handle("party_decline", func(cl *Client, frame json.RawMessage) { m.answer(cl, frame, false) })
	r.Handle("party_leave", func(cl *Client, frame json.RawMessage) { m.leaveParty(cl.ID()) })
	r.Handle("party_chat", m.chatLine)
	r.OnConnect(m.connected)
	r.OnJoin(func(cl *Client) { m.moved(cl.ID(), false) })
	r.OnLeave(func(cl *Client, _ string) { m.moved(cl.ID(), true) })
	r.OnDisconnect(m.disconnected)
	m.registerSpaces(r)
}

// —— Sending ——

func (m *parties) to(id int64, msg any) {
	if cl := m.h.ClientLocked(id); cl != nil {
		m.h.SendLocked(cl, msg)
	}
}

func (m *parties) activity(id int64, text string) {
	m.to(id, map[string]any{"t": "party_activity", "text": text})
}

// shown is a party the client draws: two members or more (inc_party.js party_members).
func shown(p *party) bool { return p != nil && len(p.members) >= 2 }

func (m *parties) roster(p *party) map[string]any {
	members := make([]map[string]any, 0, len(p.members))
	var leader int64
	for _, mem := range p.members {
		cl := m.h.ClientLocked(mem.id)
		street := ""
		if cl != nil {
			street = cl.Street()
		}
		if mem.leader {
			leader = mem.id
		}
		members = append(members, map[string]any{"id": mem.id, "name": mem.name, "leader": mem.leader, "online": mem.online, "away": mem.online && cl == nil, "street": street})
	}
	return map[string]any{"leader": leader, "members": members}
}

// push sends the roster to every member online (party_join / party_add /
// party_remove / party_online / party_offline in the source, one message here).
func (m *parties) push(p *party) {
	m.heal(p)
	if p.settle != nil {
		p.settle()
		p.settle = nil
	}
	if !shown(p) {
		return
	}
	msg := map[string]any{"t": "party", "party": m.roster(p)}
	for _, mem := range p.members {
		m.to(mem.id, msg)
	}
}

func (m *parties) pushOne(id int64) {
	p := m.of[id]
	if shown(p) {
		m.to(id, map[string]any{"t": "party", "party": m.roster(p)})
	} else {
		m.to(id, map[string]any{"t": "party", "party": nil})
	}
}

// —— Party bookkeeping ——

func (p *party) member(id int64) *partyMember {
	for _, mem := range p.members {
		if mem.id == id {
			return mem
		}
	}
	return nil
}

func (m *parties) create(leader *Client) *party {
	p := &party{members: []*partyMember{{id: leader.ID(), name: leader.Name(), leader: true, online: true}}, invites: map[int64]*partyInvite{}}
	m.of[leader.ID()] = p
	m.all[p] = struct{}{}
	return p
}

func (m *parties) held(to int64) int {
	n := 0
	for p := range m.all {
		if p.invites[to] != nil {
			n++
		}
	}
	return n
}

func (m *parties) dropInvite(p *party, inv *partyInvite) {
	if p.invites[inv.to] == inv {
		delete(p.invites, inv.to)
	}
	if inv.stop != nil {
		inv.stop()
	}
}

// close ends a party (party.js close): invites are withdrawn, members let go.
// reason "disband" tells the members; "invites-done" and "offline" are quiet
// (inc_party.js party_left 185-209).
func (m *parties) close(p *party, reason string) {
	if p.space != nil {
		m.deleteSpace(p, "closed") // party.js close 375-378
	}
	for _, inv := range p.invites {
		m.dropInvite(p, inv)
		m.to(inv.to, map[string]any{"t": "party_uninvited", "from": inv.from})
		m.activity(inv.to, "Your invite from "+inv.fromName+" to join their party has expired")
	}
	if p.settle != nil {
		p.settle()
		p.settle = nil
	}
	for _, mem := range p.members {
		delete(m.of, mem.id)
		m.to(mem.id, map[string]any{"t": "party", "party": nil})
		if reason == "disband" {
			m.activity(mem.id, "Your party has been disbanded")
		}
	}
	p.members = nil
	delete(m.all, p)
}

// remove takes one member out (party.js leave): under two left disbands, a
// leader leaving promotes the longest-standing member.
func (m *parties) remove(p *party, id int64, reason string) {
	mem := p.member(id)
	if mem == nil {
		return
	}
	wasShown := shown(p)
	for i, o := range p.members {
		if o == mem {
			p.members = append(p.members[:i], p.members[i+1:]...)
			break
		}
	}
	delete(m.of, id)
	m.spaceMemberGone(p, id) // party.js leave 178-185: instances_exit('party_space')
	if reason == "left" {
		if wasShown {
			m.to(id, map[string]any{"t": "party", "party": nil})
			m.activity(id, "You have left the party")
		}
	}
	if len(p.members) < 2 {
		m.close(p, "disband")
		return
	}
	if mem.leader {
		p.members[0].leader = true
		// inc_party.js party_now_leader sends "now leader of party".
		m.activity(p.members[0].id, "You are now leader of the party")
	}
	for _, o := range p.members {
		m.activity(o.id, mem.name+" has left the party")
	}
	m.push(p)
}

// —— Handlers ——

func (m *parties) invite(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(frame, &msg) != nil || (msg.ID <= 0 && strings.TrimSpace(msg.Name) == "") {
		m.h.ErrorLocked(cl, "bad_party", "No player specified")
		return
	}
	b := m.invites[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.invites[cl.ID()] = b
	}
	if !b.take(m.h.Now(), partyInviteBurst, partyInviteEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	var target *Client
	if msg.ID > 0 {
		target = m.h.ClientLocked(msg.ID)
		if target == nil {
			m.activity(cl.ID(), "That player is currently offline.")
			return
		}
	} else {
		name := cleanChat(msg.Name)
		if r := []rune(name); len(r) > partyMaxName {
			name = string(r[:partyMaxName])
		}
		var found []int64
		for id, n := range m.names {
			if strings.EqualFold(n, name) {
				found = append(found, id)
			}
		}
		switch len(found) {
		case 0:
			m.activity(cl.ID(), name+" is currently offline.")
			return
		case 1:
			target = m.h.ClientLocked(found[0])
		default:
			m.activity(cl.ID(), "More than one player online is called "+name+". Click their Glitch to invite them.")
			return
		}
		if target == nil {
			m.activity(cl.ID(), name+" is currently offline.")
			return
		}
	}
	if target.ID() == cl.ID() {
		m.activity(cl.ID(), "You can't invite yourself.")
		return
	}
	if blockedBetween(m.h, cl.ID(), target.ID()) {
		m.activity(cl.ID(), "You are blocking or blocked by that player.") // inc_trading.js:128, the source's words for a block
		return
	}
	p := m.of[cl.ID()]
	created := false
	if p == nil {
		p = m.create(cl)
		created = true
	}
	ret := m.sendInvite(p, cl, target)
	if ret != "ok" && created {
		m.close(p, "invites-done")
	}
	label := target.Name()
	switch ret {
	case "invited":
		m.activity(cl.ID(), label+" has already been invited to join your party.")
	case "member":
		// InteractionMenuController.as:588 disabled_reason.
		m.activity(cl.ID(), label+" is already in your party.")
	case "other_party":
		m.activity(cl.ID(), label+" is already in a party.")
	case "full":
		m.activity(cl.ID(), "Your party is full!")
	case "kicked":
		// inc_party.js:57-58.
		m.activity(cl.ID(), label+" has been removed from your party - only the party creator can re-invite them.")
	case "busy":
		m.activity(cl.ID(), "Your party has a lot of invites waiting. Give them a moment to answer.")
	case "held":
		m.activity(cl.ID(), label+" has a lot of invites waiting right now.")
	case "ok":
		m.activity(cl.ID(), "You have invited "+label+" to join your party.")
	}
}

// sendInvite is party.js send_invite: "ok" or why not.
func (m *parties) sendInvite(p *party, from, to *Client) string {
	if p.invites[to.ID()] != nil {
		return "invited"
	}
	if other := m.of[to.ID()]; other != nil {
		if other == p {
			return "member"
		}
		return "other_party"
	}
	if len(p.members) >= partyMaxSize {
		return "full"
	}
	if why := m.kickedInvite(p, from.ID(), to.ID()); why != "" {
		return why
	}
	if len(p.invites) >= partyMaxInvites {
		return "busy"
	}
	if m.held(to.ID()) >= partyMaxHeld {
		return "held"
	}
	inv := &partyInvite{from: from.ID(), to: to.ID(), fromName: from.Name(), toName: to.Name(), expires: m.h.Now().Add(partyInviteTimeout)}
	p.invites[to.ID()] = inv
	inv.stop = m.h.After(partyInviteTimeout, func() { m.expire(inv) })
	m.sendInvited(to.ID(), inv, partyPromptSeconds)
	return "ok"
}

// sendInvited is inc_party.js party_invited: the prompt with OK / No thanks.
// With a Party Space going it names the space (inc_party.js:110-121).
func (m *parties) sendInvited(to int64, inv *partyInvite, seconds int) {
	where := "Party Chat."
	if p := m.partyOfInvite(inv); p != nil && p.space != nil {
		where = "Party in " + partyTemplates[p.space.template].label + "."
	}
	m.to(to, map[string]any{"t": "party_invited", "from": map[string]any{"id": inv.from, "name": inv.fromName},
		"text": inv.fromName + " has invited you to join their " + where, "timeout": seconds})
}

func (m *parties) partyOfInvite(inv *partyInvite) *party {
	for p := range m.all {
		if p.invites[inv.to] == inv {
			return p
		}
	}
	return nil
}

// expire is party.js party_expire_invite: a decline marked expired.
func (m *parties) expire(inv *partyInvite) {
	p := m.partyOfInvite(inv)
	if p == nil {
		return
	}
	m.to(inv.to, map[string]any{"t": "party_uninvited", "from": inv.from})
	m.decline(p, inv, true)
}

// decline is party.js decline_invite.
func (m *parties) decline(p *party, inv *partyInvite, expired bool) {
	m.dropInvite(p, inv)
	if expired {
		m.activity(inv.from, inv.toName+" has declined your invitation (expired)")
	} else {
		m.activity(inv.from, inv.toName+" has declined your invitation")
	}
	if len(p.invites) == 0 && len(p.members) == 1 {
		m.close(p, "invites-done")
	}
}

func (m *parties) answer(cl *Client, frame json.RawMessage, accept bool) {
	var msg struct {
		From int64 `json:"from"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.From <= 0 {
		m.h.ErrorLocked(cl, "bad_party", "No player specified")
		return
	}
	var p *party
	var inv *partyInvite
	for q := range m.all {
		if i := q.invites[cl.ID()]; i != nil && i.from == msg.From {
			p, inv = q, i
			break
		}
	}
	if inv == nil {
		m.to(cl.ID(), map[string]any{"t": "party_uninvited", "from": msg.From})
		if accept {
			m.activity(cl.ID(), "Your invitation to join this party has expired - sorry!")
		}
		return
	}
	if !accept {
		m.decline(p, inv, false)
		return
	}
	m.accept(p, inv, cl)
}

// accept is party.js accept_invite.
func (m *parties) accept(p *party, inv *partyInvite, cl *Client) {
	id := cl.ID()
	if len(p.members) >= partyMaxSize {
		m.activity(id, "The party is full - sorry!")
		m.dropInvite(p, inv)
		if len(p.invites) == 0 && len(p.members) == 1 {
			m.close(p, "invites-done")
		}
		return
	}
	if old := m.of[id]; old != nil {
		if old == p {
			m.dropInvite(p, inv)
			return
		}
		// Invites you sent from the old party come along (party.js 241-248).
		var moving []*partyInvite
		for _, i := range old.invites {
			if i.from == id {
				moving = append(moving, i)
			}
		}
		for _, i := range moving {
			delete(old.invites, i.to)
		}
		m.remove(old, id, "left")
		for _, i := range moving {
			if p.invites[i.to] == nil && m.of[i.to] != p {
				p.invites[i.to] = i
			} else {
				if i.stop != nil {
					i.stop()
				}
				m.to(i.to, map[string]any{"t": "party_uninvited", "from": i.from})
			}
		}
	}
	m.dropInvite(p, inv)
	if p.members == nil { // the inviter's party closed meanwhile
		m.activity(id, "Your invitation to join this party has expired - sorry!")
		return
	}
	p.members = append(p.members, &partyMember{id: id, name: cl.Name(), online: true})
	m.of[id] = p
	var names []string
	for _, mem := range p.members {
		if mem.id != id {
			names = append(names, mem.name)
		}
	}
	m.activity(id, "You joined a party with "+prettyList(names, " and "))
	for _, mem := range p.members {
		switch {
		case mem.id == id:
		case mem.id == inv.from || len(p.members) == 2:
			m.activity(mem.id, cl.Name()+" accepted your party invite.")
		default:
			// party.js:295 skips everyone here (its test is inverted); party_member_joined is what it meant.
			m.activity(mem.id, cl.Name()+" has joined the party")
		}
	}
	m.push(p)
	m.spaceJoined(p, cl) // party.js accept_invite 305-316
}

func (m *parties) leaveParty(id int64) {
	p := m.of[id]
	if p == nil {
		m.pushOne(id)
		return
	}
	m.remove(p, id, "left")
}

func (m *parties) chatLine(cl *Client, frame json.RawMessage) {
	var msg struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	text := cleanChat(msg.Text)
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "/") {
		m.h.ErrorLocked(cl, "party_slash", "Slash commands are for global/local chat only")
		return
	}
	p := m.of[cl.ID()]
	if !shown(p) {
		m.activity(cl.ID(), "You're not in a party!")
		return
	}
	b := m.chat[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.chat[cl.ID()] = b
	}
	if !b.take(m.h.Now(), chatBurst, chatEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	out := map[string]any{"t": "party_said", "id": cl.ID(), "name": cl.Name(), "text": text, "at": m.h.Now().UnixMilli()}
	for _, mem := range p.members {
		m.to(mem.id, out)
	}
}

// —— Lifecycle ——

func (m *parties) connected(cl *Client) {
	id := cl.ID()
	m.names[id] = cl.Name()
	if stop := m.away[id]; stop != nil {
		stop()
		delete(m.away, id)
	}
	if p := m.of[id]; p != nil {
		mem := p.member(id)
		back := mem != nil && !mem.online
		if mem != nil {
			mem.online = true
			mem.name = cl.Name()
		}
		if back {
			// party.js player_login: the others hear you are back.
			for _, o := range p.members {
				if o.id != id && o.online {
					m.activity(o.id, "Party member "+cl.Name()+" has come online")
				}
			}
		}
		m.push(p)
		m.spaceConnected(p, cl) // party.js player_login 431
	}
	// Invites still waiting for this player come back with the time they have left.
	now := m.h.Now()
	for q := range m.all {
		if inv := q.invites[id]; inv != nil {
			if left := int(inv.expires.Sub(now).Seconds()) - 1; left > 0 {
				m.sendInvited(id, inv, left)
			}
		}
	}
}

// moved pushes a member's street to the party. Leaving a street waits a moment,
// since a street change is a leave then a join.
func (m *parties) moved(id int64, leaving bool) {
	p := m.of[id]
	if !shown(p) {
		return
	}
	if !leaving {
		m.push(p)
		return
	}
	if p.settle == nil {
		p.settle = m.h.After(partySettle, func() {
			p.settle = nil
			if _, live := m.all[p]; live {
				m.push(p)
			}
		})
	}
}

func (m *parties) disconnected(cl *Client) {
	m.startGrace(cl.ID())
	if p := m.of[cl.ID()]; p != nil {
		m.push(p) // shows them away
	}
}

// startGrace gives a dropped socket partyGrace to come back before it logs out.
func (m *parties) startGrace(id int64) {
	delete(m.names, id)
	if stop := m.away[id]; stop != nil {
		stop()
	}
	m.away[id] = m.h.After(partyGrace, func() {
		delete(m.away, id)
		if m.h.ClientLocked(id) == nil {
			m.logout(id)
		}
	})
}

// heal starts the grace for a member whose socket is gone without a
// disconnect hook (the hub drops a client that stopped reading, pushLocked,
// without running OnDisconnect).
func (m *parties) heal(p *party) {
	for _, mem := range p.members {
		if mem.online && m.away[mem.id] == nil && m.h.ClientLocked(mem.id) == nil {
			m.startGrace(mem.id)
		}
	}
}

// logout is inc_party.js party_logout / party.js player_logout.
func (m *parties) logout(id int64) {
	delete(m.chat, id)
	delete(m.invites, id)
	p := m.of[id]
	if p == nil {
		return
	}
	mem := p.member(id)
	if mem == nil {
		return
	}
	mem.online = false
	onlines := 0
	for _, o := range p.members {
		if o.id != id && o.online {
			m.activity(o.id, "Party member "+mem.name+" has gone offline")
			onlines++
		}
	}
	if onlines == 0 {
		m.close(p, "offline")
		return
	}
	m.push(p)
}

// prettyList is common.js pretty_list.
func prettyList(list []string, joiner string) string {
	switch len(list) {
	case 0:
		return ""
	case 1:
		return list[0]
	}
	return strings.Join(list[:len(list)-1], ", ") + joiner + list[len(list)-1]
}

// partyIDs is for tests: each party's member ids, sorted by first member.
func (m *parties) partyIDs() [][]int64 {
	var out [][]int64
	for p := range m.all {
		var ids []int64
		for _, mem := range p.members {
			ids = append(ids, mem.id)
		}
		out = append(out, ids)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}
