package main

// The parties' state file (party.go, party_spaces.go): a party and its Party
// Space outlive a restart of this server (the launchd agent restarts on every
// deploy), so a Party Pack or Party ATM currants spent through the site's
// ledger are never lost with the process.
//
// -party-file (default realtime-party.json beside -secret-file; off: memory
// only) is the whole state, written through writeFileSynced within a second
// of a change, and at once when anything of value moves: a spend asked of the
// ledger, its answer, a space opened or extended, a refund owed or done. It
// keeps each party (members in join order, the leader, who had logged out,
// the invites still waiting, the kicked list, who missed their space ending)
// and its space (the instance id and so its street PARTY_<ID>, the template,
// the pack, the clock's end, the extensions, the warning, and every spend it
// was paid with). Who stands where, chat buckets and the settle timer are not
// kept.
//
// After a start, as in the source when a player logs back in (players/
// player.js:169 onLoggedin calls party_login, inc_party.js:342-345 hands it
// to party.js player_login 415-432, which tells the others and sends the
// space again with party_space_change; inc_instances.js:273-278
// instances_login leaves a player in the instance they logged out in, and
// instance.js:370-374 player_login only acts for a game): a member who comes
// back is in the party again and is sent the space, and the space's street
// lets them back in (GateJoin). Members online before the restart have
// partyRestartGrace to reconnect before they count as logging out (party.js
// player_logout 387-413: once nobody is online the party closes, and its
// space with it, as the source does). A space whose clock ran out while the
// server was down closes as it would have ("Our Party Space expired.",
// party.js delete_space 732-753), told to each member when they come back; a
// live one comes back with its clock, warning and end timers re-armed. The
// timers are armed on the first connect after a start, or a second after it
// (resume), not when the module is built.
//
// A space that cannot come back (a record that does not fit: a template or
// pack this build does not know, a bad instance id, a party under two
// members; or a file that does not parse at all, whose spends are read back
// from its text before it is moved aside) has what was spent on it given back
// through the site's ledger (ledger/spend/refund, LedgerSpend.php), and so is
// a spend asked of the ledger with no answer before the restart. The site
// keeps the refund once per spend id (the trade id party-<16 hex>-r), so a
// refund asked twice is given once, and never after a void of the same spend.
// See party_spaces.go for why it is the site's refund and not a credit.

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var partyFileFlag = flag.String("party-file", "", "file that keeps parties and their Party Spaces: members, the leader, invites, each space's clock and what was spent on it (default: realtime-party.json beside -secret-file; off: memory only)")

// partyFileForTest lets a test give the module a state file; under go test the flag is ignored.
var partyFileForTest string

const (
	partyRestartGrace   = 2 * time.Minute // ours: after a restart, a member online before it has this long to come back
	partyRefundRetryMin = 2 * time.Second
	partyRefundRetryMax = 5 * time.Minute
	partyNoticeKeep     = 24 * time.Hour // Ledger::PENDING_FOR: how long a refund waits for a save to apply it
	partyNoticesKept    = 20
)

// partySpendRec is one spend through the ledger. Until is the end of the space it paid for (Unix
// ms) when the file was written, 0 for a spend owed back whatever happens; the field order is fixed
// (salvage reads it back from a file that does not parse).
type partySpendRec struct {
	Op    string `json:"op"`
	User  int64  `json:"user"`
	Why   string `json:"why"`
	Until int64  `json:"until"`
}

type partyMemberRec struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Leader bool   `json:"leader,omitempty"`
	Online bool   `json:"online,omitempty"`
}

type partyInviteRec struct {
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	FromName string `json:"fromName"`
	ToName   string `json:"toName"`
	Expires  int64  `json:"expires"`
}

type partySpaceRec struct {
	ID          string          `json:"id"`
	Template    string          `json:"template"`
	Pack        string          `json:"pack"`
	Taster      bool            `json:"taster,omitempty"`
	Creator     int64           `json:"creator"`
	CreatorName string          `json:"creatorName"`
	Expires     int64           `json:"expires"`
	Extensions  int             `json:"extensions,omitempty"`
	Warned      bool            `json:"warned,omitempty"`
	Spends      []partySpendRec `json:"spends"`
}

type partyRec struct {
	Members []partyMemberRec `json:"members"`
	Invites []partyInviteRec `json:"invites,omitempty"`
	Kicked  []int64          `json:"kicked,omitempty"`
	Missed  map[int64]string `json:"missed,omitempty"`
	Space   *partySpaceRec   `json:"space,omitempty"`
}

// partyNotice is a refund the player is told about (party_space_refunded) whenever they connect,
// until a day has passed; the client applies it once by its id.
type partyNotice struct {
	User   int64      `json:"user"`
	Spend  string     `json:"spend"`
	Refund string     `json:"refund"`
	Goods  tradeGoods `json:"goods"`
	At     int64      `json:"at"`
}

type partyFile struct {
	V       int             `json:"v"`
	Parties []partyRec      `json:"parties"`
	Asked   []partySpendRec `json:"asked,omitempty"` // with the ledger, no answer yet
	Owed    []partySpendRec `json:"owed,omitempty"`  // to be given back
	Notices []partyNotice   `json:"notices,omitempty"`
}

// partyOwed is a refund still to be asked of the site, or being asked.
type partyOwed struct {
	rec    partySpendRec
	asking bool
	tries  int
	retry  func() bool
}

// partyPersist is the parties module's state file bookkeeping.
type partyPersist struct {
	path     string
	saveAt   bool
	resumed  bool
	restored []int64 // members online before the restart: their grace starts at resume
	asked    map[string]partySpendRec
	owed     map[string]*partyOwed
	notices  map[int64][]partyNotice
}

var partySpaceIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// partySalvageRe reads the spends back from a file that does not parse (partySpendRec's JSON).
var partySalvageRe = regexp.MustCompile(`\{"op":"(party-[0-9a-f]{16})","user":([0-9]{1,19}),"why":"(party_pack|party_atm)","until":([0-9]{1,19})\}`)

func (m *parties) initPersist() {
	m.persist = partyPersist{asked: map[string]partySpendRec{}, owed: map[string]*partyOwed{}, notices: map[int64][]partyNotice{}}
	m.persist.path = statePath(partyFileForTest, partyFileFlag, "realtime-party.json")
	if m.persist.path == "" {
		m.persist.resumed = true
		return
	}
	if err := m.load(); err != nil {
		log.Printf("parties %s: %v (parties are kept in memory only)", m.persist.path, err)
		m.persist.path = ""
	}
	if !testing.Testing() {
		// Nobody may connect for a while: the graces, the expiries and the refunds go on anyway.
		m.h.After(time.Second, m.resume)
	}
}

// load reads the state file into the module; resume arms the timers and asks for the refunds.
func (m *parties) load() error {
	raw, err := os.ReadFile(m.persist.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f partyFile
	if perr := json.Unmarshal(raw, &f); perr != nil || f.V != 1 {
		n := m.salvage(raw)
		if perr != nil {
			if _, err := readStateFile("parties", m.persist.path, &f); err != nil { // moves it aside
				return err
			}
		} else {
			log.Printf("parties %s: version %d is not this build's (starting empty)", m.persist.path, f.V)
		}
		log.Printf("parties %s: %d spends read back from a file that could not be used, to be given back", m.persist.path, n)
		return nil
	}
	now := m.h.Now()
	parties, spaces, lost := 0, 0, 0
	for _, pr := range f.Parties {
		switch m.revive(pr, now) {
		case "space":
			parties++
			spaces++
		case "party":
			parties++
		case "lost":
			parties++
			lost++
		case "gone":
			if pr.Space != nil {
				lost++
			}
		}
	}
	for _, list := range [][]partySpendRec{f.Asked, f.Owed} {
		for _, s := range list {
			m.owe(s)
		}
	}
	keep := now.Add(-partyNoticeKeep).UnixMilli()
	for _, n := range f.Notices {
		if n.User > 0 && n.At > keep && len(m.persist.notices[n.User]) < partyNoticesKept {
			m.persist.notices[n.User] = append(m.persist.notices[n.User], n)
		}
	}
	log.Printf("parties %s: %d parties, %d spaces, %d spaces that could not come back, %d refunds owed", m.persist.path, parties, spaces, lost, len(m.persist.owed))
	return nil
}

// salvage owes back every spend a file that cannot be used names, unless the space it paid for
// had ended by now anyway. The number of spends.
func (m *parties) salvage(raw []byte) int {
	now := m.h.Now().UnixMilli()
	n := 0
	for _, g := range partySalvageRe.FindAllSubmatch(raw, -1) {
		user, err1 := strconv.ParseInt(string(g[2]), 10, 64)
		until, err2 := strconv.ParseInt(string(g[4]), 10, 64)
		if err1 != nil || err2 != nil || (until != 0 && until <= now) {
			continue
		}
		if m.owe(partySpendRec{Op: string(g[1]), User: user, Why: string(g[3])}) {
			n++
		}
	}
	return n
}

// owe records a refund to ask for (once per spend id); false when it was owed already or does not fit.
func (m *parties) owe(s partySpendRec) bool {
	if s.User <= 0 || !partySpendID(s.Op) || m.persist.owed[s.Op] != nil {
		return false
	}
	s.Until = 0
	m.persist.owed[s.Op] = &partyOwed{rec: s}
	return true
}

func partySpendID(op string) bool {
	id, ok := strings.CutPrefix(op, "party-")
	return ok && partySpaceIDRe.MatchString(id)
}

// revive is one party record back: "space" with its space, "party" without one, "lost" when its
// space could not come back (its spends are owed), "gone" when the party itself could not.
func (m *parties) revive(pr partyRec, now time.Time) string {
	p := &party{invites: map[int64]*partyInvite{}}
	leader := false
	for _, mr := range pr.Members {
		if mr.ID <= 0 || m.of[mr.ID] != nil || p.member(mr.ID) != nil {
			continue
		}
		name := mr.Name
		if name == "" {
			name = "Someone"
		}
		mem := &partyMember{id: mr.ID, name: name, leader: mr.Leader && !leader, online: mr.Online}
		leader = leader || mem.leader
		p.members = append(p.members, mem)
	}
	online := false
	for _, mem := range p.members {
		online = online || mem.online
	}
	if len(p.members) < 2 || !online {
		// Under two members it would have disbanded; with nobody online it would have closed
		// (party.js player_logout), and its space with it, as the source does: nothing to give back.
		if pr.Space != nil && len(p.members) < 2 {
			m.oweSpends(pr.Space)
		}
		return "gone"
	}
	if !leader {
		p.members[0].leader = true // party.js promote_leader: the longest-standing member
	}
	for _, mem := range p.members {
		m.of[mem.id] = p
		if mem.online {
			m.persist.restored = append(m.persist.restored, mem.id)
		}
	}
	m.all[p] = struct{}{}
	for _, id := range pr.Kicked {
		if id > 0 {
			if p.kicked == nil {
				p.kicked = map[int64]bool{}
			}
			p.kicked[id] = true
		}
	}
	for id, why := range pr.Missed {
		if p.member(id) != nil && (why == "expired" || why == "closed") {
			if p.missed == nil {
				p.missed = map[int64]string{}
			}
			p.missed[id] = why
		}
	}
	for _, ir := range pr.Invites {
		exp := timeOf(ir.Expires)
		if ir.To <= 0 || p.member(ir.From) == nil || p.member(ir.To) != nil || p.invites[ir.To] != nil || !exp.After(now) || len(p.invites) >= partyMaxInvites {
			continue
		}
		p.invites[ir.To] = &partyInvite{from: ir.From, to: ir.To, fromName: ir.FromName, toName: ir.ToName, expires: exp}
	}
	if pr.Space == nil {
		return "party"
	}
	s := m.reviveSpace(pr.Space)
	if s == nil {
		m.oweSpends(pr.Space)
		for _, mem := range p.members {
			if p.missed == nil {
				p.missed = map[int64]string{}
			}
			p.missed[mem.id] = "closed"
		}
		log.Printf("party space %s: could not come back (template %q, pack %q): what was spent on it is owed back", partySpaceStreet+strings.ToUpper(pr.Space.ID), pr.Space.Template, pr.Space.Pack)
		return "lost"
	}
	p.space = s
	m.spaces.byStreet[s.street] = p
	return "space"
}

// reviveSpace is a space record back (no timers yet), or nil when it does not fit this build.
func (m *parties) reviveSpace(r *partySpaceRec) *partySpace {
	if !partySpaceIDRe.MatchString(r.ID) || r.Creator <= 0 || r.Expires <= 0 || r.Extensions < 0 {
		return nil
	}
	if _, ok := partyTemplates[r.Template]; !ok {
		return nil
	}
	if tpl, taster, ok := partyPackTemplate(r.Pack); !ok || tpl != r.Template || taster != r.Taster {
		return nil
	}
	street := partySpaceStreet + strings.ToUpper(r.ID)
	if m.spaces.byStreet[street] != nil {
		return nil
	}
	s := &partySpace{id: r.ID, street: street, template: r.Template, pack: r.Pack, taster: r.Taster, creator: r.Creator,
		creatorName: r.CreatorName, expires: timeOf(r.Expires), extensions: r.Extensions, warned: r.Warned}
	for _, sp := range r.Spends {
		if sp.User > 0 && partySpendID(sp.Op) {
			sp.Until = 0
			s.spends = append(s.spends, sp)
		}
	}
	return s
}

func (m *parties) oweSpends(r *partySpaceRec) {
	for _, sp := range r.Spends {
		m.owe(sp)
	}
}

// resume arms what the file's parties were waiting for, once, on the first connect (or a second
// after a start): the restart grace of members who were online, the invites' expiry, each live
// space's warning and end; a space whose clock ran out while the server was down closes now.
// Then the refunds owed are asked for.
func (m *parties) resume() {
	if m.persist.resumed {
		return
	}
	m.persist.resumed = true
	now := m.h.Now()
	for _, id := range m.persist.restored {
		if m.of[id] != nil && m.h.ClientLocked(id) == nil {
			m.startGraceFor(id, partyRestartGrace)
		}
	}
	m.persist.restored = nil
	for _, p := range m.sortedParties() {
		for _, inv := range p.invites {
			if inv.stop == nil {
				inv.stop = m.h.After(max(0, inv.expires.Sub(now)), func() { m.expire(inv) })
			}
		}
		if s := p.space; s != nil && s.warn == nil && s.end == nil {
			if s.expires.After(now) {
				m.schedule(p, s)
			} else {
				m.deleteSpace(p, "expired")
			}
		}
	}
	if m.spaces.ledger == nil && len(m.persist.owed) > 0 {
		// Without the ledger (-trade-ledger off) nothing was spent through it, so nothing goes back.
		log.Printf("parties: %d refunds owed but no ledger to give them back through: dropped", len(m.persist.owed))
		m.persist.owed = map[string]*partyOwed{}
	}
	for _, op := range m.owedOps() {
		m.askRefund(op)
	}
	m.saveSoon()
}

func (m *parties) sortedParties() []*party {
	out := make([]*party, 0, len(m.all))
	for p := range m.all {
		if len(p.members) > 0 {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].members[0].id < out[j].members[0].id })
	return out
}

func (m *parties) owedOps() []string {
	ops := make([]string, 0, len(m.persist.owed))
	for op := range m.persist.owed {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return ops
}

// notices sends a player the refunds they have not been told about long enough (on connect).
func (m *parties) sendNotices(id int64) {
	list := m.persist.notices[id]
	if len(list) == 0 {
		return
	}
	keep := m.h.Now().Add(-partyNoticeKeep).UnixMilli()
	kept := list[:0]
	for _, n := range list {
		if n.At > keep {
			kept = append(kept, n)
			m.sendRefunded(n)
		}
	}
	if len(kept) == 0 {
		delete(m.persist.notices, id)
	} else {
		m.persist.notices[id] = kept
	}
}

func (m *parties) addNotice(n partyNotice) {
	list := append(m.persist.notices[n.User], n)
	if len(list) > partyNoticesKept {
		list = list[len(list)-partyNoticesKept:]
	}
	m.persist.notices[n.User] = list
	m.sendRefunded(n)
}

// refundBackoff doubles from partyRefundRetryMin to partyRefundRetryMax.
func refundBackoff(tries int) time.Duration {
	d := partyRefundRetryMin
	for i := 1; i < tries && d < partyRefundRetryMax; i++ {
		d *= 2
	}
	return min(d, partyRefundRetryMax)
}

// —— Writing ——

func (m *parties) record() partyFile {
	f := partyFile{V: 1, Parties: []partyRec{}}
	for _, p := range m.sortedParties() {
		if len(p.members) < 2 && p.space == nil {
			continue // a lone inviter's party: it closes when its invites are answered
		}
		pr := partyRec{}
		for _, mem := range p.members {
			pr.Members = append(pr.Members, partyMemberRec{ID: mem.id, Name: mem.name, Leader: mem.leader, Online: mem.online})
		}
		tos := make([]int64, 0, len(p.invites))
		for to := range p.invites {
			tos = append(tos, to)
		}
		sort.Slice(tos, func(i, j int) bool { return tos[i] < tos[j] })
		for _, to := range tos {
			inv := p.invites[to]
			pr.Invites = append(pr.Invites, partyInviteRec{From: inv.from, To: inv.to, FromName: inv.fromName, ToName: inv.toName, Expires: msOf(inv.expires)})
		}
		for id := range p.kicked {
			pr.Kicked = append(pr.Kicked, id)
		}
		sort.Slice(pr.Kicked, func(i, j int) bool { return pr.Kicked[i] < pr.Kicked[j] })
		if len(p.missed) > 0 {
			pr.Missed = map[int64]string{}
			for id, why := range p.missed {
				pr.Missed[id] = why
			}
		}
		if s := p.space; s != nil {
			sr := &partySpaceRec{ID: s.id, Template: s.template, Pack: s.pack, Taster: s.taster, Creator: s.creator, CreatorName: s.creatorName,
				Expires: msOf(s.expires), Extensions: s.extensions, Warned: s.warned, Spends: []partySpendRec{}}
			for _, sp := range s.spends {
				sp.Until = sr.Expires
				sr.Spends = append(sr.Spends, sp)
			}
			pr.Space = sr
		}
		f.Parties = append(f.Parties, pr)
	}
	ops := make([]string, 0, len(m.persist.asked))
	for op := range m.persist.asked {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		f.Asked = append(f.Asked, m.persist.asked[op])
	}
	for _, op := range m.owedOps() {
		f.Owed = append(f.Owed, m.persist.owed[op].rec)
	}
	users := make([]int64, 0, len(m.persist.notices))
	for id := range m.persist.notices {
		users = append(users, id)
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	for _, id := range users {
		f.Notices = append(f.Notices, m.persist.notices[id]...)
	}
	return f
}

// save writes the whole state now; false when it could not (it stays in memory).
func (m *parties) save() bool {
	if m.persist.path == "" {
		return true
	}
	b, err := json.Marshal(m.record())
	if err == nil {
		err = writeFileSynced(m.persist.path, b)
	}
	if err != nil {
		log.Printf("parties %s: %v", m.persist.path, err)
		return false
	}
	return true
}

// saveSoon writes within a second (invites and rosters change often; one write a second is plenty).
func (m *parties) saveSoon() {
	if m.persist.path == "" || m.persist.saveAt {
		return
	}
	m.persist.saveAt = true
	m.h.After(time.Second, func() {
		m.persist.saveAt = false
		m.save()
	})
}

// saveNow writes at once: something of value moved (a spend, a space, a refund).
func (m *parties) saveNow() {
	if m.persist.path == "" {
		return
	}
	m.save()
}

// kept wraps a message handler: the file's timers are armed first, and a save follows.
func (m *parties) kept(fn func(cl *Client, frame json.RawMessage)) func(cl *Client, frame json.RawMessage) {
	return func(cl *Client, frame json.RawMessage) {
		m.resume()
		fn(cl, frame)
		m.saveSoon()
	}
}
