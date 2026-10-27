package main

// Party Spaces between real players (G334): a Party Pack opens one private
// party location for the activating player's party, as in the original. The
// rules are GameServerJS groups/party.js (create_space 542-613, enter_space
// 709-726, space_warning 728-730, delete_space 732-753, extend_space_time
// 755-771, kicked 129-135, the kicked check in send_invite 70-87),
// players/inc_party.js (party_invited 110-121, party_start_space 403-410,
// party_space_prompt 456-475, party_extend_space_time 538-548),
// items/party_pack.js (verbs.activate conditions, activate: an hour, ten
// minutes for a party_pack_taster_ class), items/party_atm.js (verbs.extend
// 184-203, onConversation 223-255), main.js (party_space_kick 3730-3739 and
// 3963-3976) and config_prod.js:1298-1307 party_spaces; the copy players read
// is theirs. The space lives here, on the party module (party.go), so a
// party's space and its members can never disagree.
//
// Client to server:
//
//	{"t":"party_space_start","pack":"party_pack_toxic_moon"}   Activate on a pack
//	{"t":"party_space_extend","minutes":10}                    the Party ATM (in the space)
//	{"t":"party_space_kick","id":8}                            the creator's Kick out <name>
//	{"t":"party_space_dev","expire_in":20}                     QA only, GRODDLE_PARTY_DEV=1
//
// Server to client:
//
//	{"t":"party_space","space":Space|null,"why":"expired|closed|left"}   to every member on any change, on connect
//	{"t":"party_space_opened","space":Space,"pack":"…","spend":"party-…"} to the creator: the pack is spent, the teleporter is theirs
//	{"t":"party_space_prompt","space":Space,"pcs":["Ada"]}                 to the others: the offer of a teleporter (party_space_start)
//	{"t":"party_space_extended","minutes":10,"cost":4500,"spend":"party-…","text":"Party on, Wayne."}   to the payer
//	{"t":"party_space_refused","why":"party|space|template|pack|currants|late|ledger|slow","text":"…"}
//	{"t":"party_space_kicked","by":{"id":7,"name":"Ada"},"text":"Ada removed you from their Party Space."}
//	{"t":"party_space_refunded","spend":"party-…","refund":"party-…-r","goods":Goods,"text":"…"}   a spend given back (on every connect for a day)
//
// Space is {"id","street":"PARTY_<ID>","template","label","pack","taster",
// "creator":{"id","name"},"left":ms,"extensions","warned"}. Members share it on
// the hub street PARTY_<ID>; only the party's members may join that street
// (GateJoin), so a kicked player or anyone outside the party is refused. The
// party's announcements ("… made us a new Party Space!", the five-minute
// warning, "Our Party Space expired.", "… extended the party time!") are
// party_activity lines to every member (party.js announce 458-464).
//
// The pack and the ATM's currants are spent in the site's ledger first
// (ledger.go; POST groddle/v1/ledger/spend, idempotent per spend id): only a
// pack the ledger holds opens a space, and only currants it holds extend one.
// The spend is recorded like a settled trade the save has not applied, so the
// player's save drops the pack (or the currants) once, whether the page hears
// the answer or the site applies it on the next load. With -trade-ledger off
// (development) the pack and the currants are taken on the client's word.
//
// A spend that bought nothing (the party ended or the space was gone by the
// answer, no answer came, or the space could not come back after a restart:
// party_persist.go) is given back through the site's ledger/spend/refund,
// owed in the state file until the site answers. It is the site's refund, not
// a credit through credits.go: a credit is only a receipt that lets a save
// show a gain, so a player who never loaded again, or whose page missed the
// message, would get nothing back, while the refund is a settled trade from
// nobody (trade id <spend>-r) that moves the ledger at once and that the next
// load applies to the save (Ledger::catchUp), like the spend itself. The page
// is told too (party_space_refunded) and applies it once by its id: the pack
// or the currants come back only if it had applied the spend, and marks both
// applied either way, so a spend and its refund always net to nothing. The
// site keeps one refund per spend and none after a void, so asking twice
// gives it once.
//
// Rules kept from the source:
//   - Activating needs a party and no space going ("You must be in a party to
//     activate. Didn't you read the instructions?", "Your party already has a
//     Party Space going.", party_pack.js verbs.activate conditions).
//   - The creator gets a free single-use teleporter; every other member
//     online is offered one (party_space_prompt; the energy it costs is the
//     player's own, paid on their client), and so is anyone who joins the
//     party while the space lives (accept_invite 305-316). Invites name the
//     space ("… has invited you to join their Party in Toxic Moon.").
//   - The warning comes five minutes before the end, and the timers start
//     again from the new end on every extension (extend_space_time).
//   - The ATM offers 5 or 15 minutes on a taster's space, 10 or 30 on a full
//     one, at the template's prices; "Down low, too slow." once it has ended.
//   - Leaving the party leaves the space (party.js leave 178-185); the party
//     closing (under two members, everyone offline) deletes it (close 375-378).
//   - Only the creator, standing in the space, may kick someone standing
//     there: they leave the party, join its kicked list, and only the creator
//     may invite them back while the space lives.
//
// Ours: the spend through the site's ledger, the refunds above, and the
// space kept across a restart of this server (party_persist.go).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	partyFullSpace    = 60 * time.Minute // party_pack.js activate: duration 60*60
	partyTasterSpace  = 10 * time.Minute // 10*60 for a party_pack_taster_ class
	partySpaceWarning = 5 * time.Minute  // party.js:591 space_warning at duration - 5 min
	partySpaceStreet  = "PARTY_"
	partySpaceBurst   = 4
	partySpaceEvery   = 2 * time.Second
	partySpaceDevMax  = 2 * time.Hour

	ledgerRouteSpend  = "/groddle/v1/ledger/spend"
	ledgerRouteRefund = "/groddle/v1/ledger/spend/refund"

	partyTextNoParty  = "You must be in a party to activate. Didn't you read the instructions?"     // party_pack.js verbs.activate
	partyTextGoing    = "Your party already has a Party Space going."                               // party_pack.js verbs.activate
	partyTextTemplate = "Bad template"                                                              // party_pack.js activate
	partyTextNoPack   = "You don't have that Party Pack."                                           // ours: the source never heard of a pack you lack
	partyTextLedger   = "That did not go through, so nothing was spent. Try again in a moment."     // ours
	partyTextWarning  = "Our Party Space will expire in 5 minutes."                                 // party.js:729
	partyTextExpired  = "Our Party Space expired."                                                  // party.js:735
	partyTextAtmOK    = "Party on, Wayne."                                                          // party_atm.js:250
	partyTextAtmBroke = "You don't have enough currants to do that."                                // party_atm.js:253
	partyTextAtmLate  = "Down low, too slow."                                                       // party_atm.js:235
	partyTextCant     = "You can't do that"                                                         // main.js:3976
	partyTextPrivate  = "That Party Space is private."                                              // ours: the source never let anyone try
	partyTextRefunded = "That Party Space could not go on, so what you spent on it was given back." // ours
)

// partyTemplate is one config_prod.js party_spaces row (1298-1307): the
// template location as the game's street files name it (G for L), the label
// the game shows (js/party-spaces.js) and the ATM prices by minutes.
type partyTemplate struct {
	label, street string
	prices        map[int]int64
}

var (
	partyPricesA = map[int]int64{5: 3000, 10: 4500, 15: 8500, 30: 13250}
	partyPricesB = map[int]int64{5: 2000, 10: 3000, 15: 6000, 30: 9250}
	partyPricesC = map[int]int64{5: 5000, 10: 8500, 15: 15000, 30: 25000}
)

var partyTemplates = map[string]partyTemplate{
	"moon":      {"Toxic Moon", "GDOBUP8IR8E2IPH", partyPricesA},
	"mazzala":   {"Mazza'la Gala", "GDOE67MFL6E2FBR", partyPricesA},
	"rainbow":   {"Double Rainbow", "GDOGBH4BG1E2BM0", partyPricesB},
	"pitchen":   {"Pitchen Lilliputt", "GDOGNQICG1E2PNI", partyPricesA},
	"nylon":     {"Nylon Phool", "GDOK0A8OG1E2C7O", map[int]int64{5: 3500, 10: 5500, 15: 10000, 30: 16000}},
	"val_holla": {"Val Holla", "GUVNGTHAGP93U34", partyPricesC},
	"aquarius":  {"Aquarius", "GUVU11R1HP9341R", partyPricesC},
	"glitchmas": {"Winter Wingding", "GDOOS17UPNJ2HT7", partyPricesB},
}

// partyPackThemes: each items/party_pack_<slug>.js classProps party_id.
var partyPackThemes = map[string]string{
	"toxic_moon":        "moon",
	"mazzala_gala":      "mazzala",
	"double_rainbow":    "rainbow",
	"pitchen_lilliputt": "pitchen",
	"nylon_phool":       "nylon",
	"val_holla":         "val_holla",
	"aquarius":          "aquarius",
	"winter_wingding":   "glitchmas",
}

// partyPackTemplate is a pack class's template and whether it is a taster.
func partyPackTemplate(class string) (template string, taster, ok bool) {
	slug, taster := strings.CutPrefix(class, "party_pack_taster_")
	if !taster {
		var full bool
		if slug, full = strings.CutPrefix(class, "party_pack_"); !full {
			return "", false, false
		}
	}
	template, ok = partyPackThemes[slug]
	return template, taster, ok
}

// partyExtendMinutes: the ATM's two offers (party_atm.js verbs.extend 186-203).
func partyExtendMinutes(taster bool) [2]int {
	if taster {
		return [2]int{5, 15}
	}
	return [2]int{10, 30}
}

// partySpace is one live space (the source's party_space instance).
type partySpace struct {
	id, street, template, pack string
	taster                     bool
	creator                    int64
	creatorName                string
	expires                    time.Time
	extensions                 int
	warned                     bool
	warn, end                  func() bool
	spends                     []partySpendRec // the pack and each extension, given back if it cannot come back (party_persist.go)
}

// spendLedger is the site's ledger as Party Spaces use it (httpLedger).
type spendLedger interface {
	spend(op string, id int64, gave tradeGoods, why string) ledgerAnswer
	// refundSpend gives a spend back: "void" with Gave[0] what went back, "refused" when nothing
	// was spent (it never came, it was refused or voided, it is not this player's), "" no answer.
	refundSpend(op string, id int64) ledgerAnswer
}

func (l *httpLedger) spend(op string, id int64, gave tradeGoods, why string) ledgerAnswer {
	return l.call(ledgerRouteSpend, map[string]any{"op": op, "id": id, "gave": goodsJSON(gave), "why": why})
}

func (l *httpLedger) refundSpend(op string, id int64) ledgerAnswer {
	return l.call(ledgerRouteRefund, map[string]any{"op": op, "id": id})
}

// partySpaces is the parties module's Party Space state.
type partySpaces struct {
	byStreet map[string]*party
	asks     map[int64]*bucket
	ledger   spendLedger  // nil: the client's word (-trade-ledger off)
	noLedger bool         // the ledger is on but not set up: spaces are refused
	spawn    func(func()) // runs a ledger call off the hub lock (tests run it in place)
	newID    func() string
	dev      bool
}

func (m *parties) initSpaces() {
	m.spaces = partySpaces{
		byStreet: map[string]*party{},
		asks:     map[int64]*bucket{},
		spawn:    func(fn func()) { go fn() },
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return hex.EncodeToString(b)
		},
		dev: os.Getenv("GRODDLE_PARTY_DEV") == "1",
	}
	if !testing.Testing() {
		site, missing := newLedgerFromFlags()
		if l, ok := site.(*httpLedger); ok {
			m.spaces.ledger = l
			log.Printf("party spaces: packs and the Party ATM spend through the ledger at %s", l.base)
		}
		m.spaces.noLedger = missing
	}
}

func (m *parties) registerSpaces(r *Registry) {
	r.Handle("party_space_start", m.kept(m.startSpace))
	r.Handle("party_space_extend", m.kept(m.extendSpace))
	r.Handle("party_space_kick", m.kept(m.kickFromSpace))
	if m.spaces.dev {
		r.Handle("party_space_dev", m.kept(m.devSpace))
	}
	r.GateJoin(m.spaceGate)
	// quoins.go: a space's instance has its template's natural quoins (party quoins between the members).
	r.Provide("party.spaceStreet", func(street string) string {
		if p := m.spaces.byStreet[street]; p != nil && p.space != nil {
			return partyTemplates[p.space.template].street
		}
		return ""
	})
}

// —— Sending ——

func (m *parties) spaceView(s *partySpace) map[string]any {
	left := s.expires.Sub(m.h.Now())
	if left < 0 {
		left = 0
	}
	return map[string]any{"id": s.id, "street": s.street, "template": s.template, "label": partyTemplates[s.template].label,
		"pack": s.pack, "taster": s.taster, "creator": map[string]any{"id": s.creator, "name": s.creatorName},
		"left": left.Milliseconds(), "extensions": s.extensions, "warned": s.warned}
}

// pushSpace is party_space_change to every member (party.js 603-609).
func (m *parties) pushSpace(p *party) {
	for _, mem := range p.members {
		m.sendSpace(mem.id, p.space, "")
	}
}

func (m *parties) sendSpace(id int64, s *partySpace, why string) {
	msg := map[string]any{"t": "party_space", "space": nil}
	if s != nil {
		msg["space"] = m.spaceView(s)
	} else if why != "" {
		msg["why"] = why
	}
	m.to(id, msg)
}

// announce is party.js announce: a line for every member.
func (m *parties) announce(p *party, text string) {
	for _, mem := range p.members {
		m.activity(mem.id, text)
	}
}

func (m *parties) refuseSpace(id int64, why, text string) {
	m.to(id, map[string]any{"t": "party_space_refused", "why": why, "text": text})
}

// prompt is party_space_prompt: the offer of a teleporter, with who is there.
func (m *parties) prompt(p *party, id int64) {
	pcs := []string{}
	for _, o := range m.h.StreetLocked(p.space.street) {
		pcs = append(pcs, o.Name())
	}
	sort.Strings(pcs)
	m.to(id, map[string]any{"t": "party_space_prompt", "space": m.spaceView(p.space), "pcs": pcs})
}

func (m *parties) allowSpace(id int64) bool {
	b := m.spaces.asks[id]
	if b == nil {
		b = &bucket{}
		m.spaces.asks[id] = b
	}
	return b.take(m.h.Now(), partySpaceBurst, partySpaceEvery)
}

// ask spends through the ledger off the hub lock; then runs under it with the answer.
func (m *parties) ask(call func(spendLedger) ledgerAnswer, then func(ledgerAnswer)) {
	site := m.spaces.ledger
	if site == nil {
		then(ledgerAnswer{Status: "settled"})
		return
	}
	m.spaces.spawn(func() {
		a := call(site)
		m.h.After(0, func() { then(a) })
	})
}

// refund gives a spend back (what it paid for could not happen, or no answer came): owed in the
// state file until the site answers.
func (m *parties) refund(op string, id int64, why string) {
	if m.spaces.ledger == nil {
		return
	}
	m.owe(partySpendRec{Op: op, User: id, Why: why})
	m.saveNow()
	m.askRefund(op)
}

// askRefund asks the site for one owed refund (ledger/spend/refund), again after a backoff while no
// answer comes. A refund goes out once resume has run (the ledger is set by then).
func (m *parties) askRefund(op string) {
	o := m.persist.owed[op]
	if o == nil || o.asking || o.retry != nil || !m.persist.resumed || m.spaces.ledger == nil {
		return
	}
	o.asking = true
	rec := o.rec
	m.ask(func(l spendLedger) ledgerAnswer { return l.refundSpend(rec.Op, rec.User) }, func(a ledgerAnswer) {
		o.asking = false
		if m.persist.owed[op] != o {
			return
		}
		switch a.Status {
		case "void":
			delete(m.persist.owed, op)
			goods := tradeGoods{}
			if len(a.Gave) > 0 {
				goods = a.Gave[0]
			}
			log.Printf("party space: spend %s (%s) for %d given back: %+v", op, rec.Why, rec.User, goods)
			if !goods.empty() {
				m.addNotice(partyNotice{User: rec.User, Spend: op, Refund: op + "-r", Goods: goods, At: m.h.Now().UnixMilli()})
			}
		case "refused", "settled":
			delete(m.persist.owed, op)
			log.Printf("party space: spend %s (%s) for %d: nothing was spent, nothing to give back", op, rec.Why, rec.User)
		default:
			o.tries++
			d := refundBackoff(o.tries)
			log.Printf("party space: spend %s for %d: no answer from the ledger, asking again in %s", op, rec.User, d)
			o.retry = m.h.After(d, func() {
				o.retry = nil
				m.askRefund(op)
			})
		}
		m.saveNow()
	})
}

// sendRefunded is party_space_refunded: the page gives the pack or the currants back once.
func (m *parties) sendRefunded(n partyNotice) {
	m.to(n.User, map[string]any{"t": "party_space_refunded", "spend": n.Spend, "refund": n.Refund, "goods": goodsJSON(n.Goods), "text": partyTextRefunded})
}

func (m *parties) newSpend() string { return "party-" + m.spaces.newID() }

// —— Handlers ——

// startSpace is party_pack.js activate + inc_party.js party_start_space.
func (m *parties) startSpace(cl *Client, frame json.RawMessage) {
	var msg struct {
		Pack string `json:"pack"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.Pack == "" || len(msg.Pack) > 64 {
		m.h.ErrorLocked(cl, "bad_party_space", "That made no sense.")
		return
	}
	id := cl.ID()
	if !m.allowSpace(id) {
		m.refuseSpace(id, "slow", "Slow down a little.")
		return
	}
	template, taster, ok := partyPackTemplate(msg.Pack)
	if !ok {
		m.refuseSpace(id, "template", partyTextTemplate)
		return
	}
	p := m.of[id]
	if p == nil {
		m.refuseSpace(id, "party", partyTextNoParty)
		return
	}
	if p.space != nil || p.starting {
		m.refuseSpace(id, "space", partyTextGoing)
		return
	}
	if m.spaces.noLedger {
		m.refuseSpace(id, "ledger", partyTextLedger)
		return
	}
	p.starting = true
	op, pack, name := m.newSpend(), msg.Pack, cl.Name()
	gave := tradeGoods{Items: []tradeItem{{C: pack, N: 1}}}
	m.asking(op, id, "party_pack")
	m.ask(func(l spendLedger) ledgerAnswer { return l.spend(op, id, gave, "party_pack") }, func(a ledgerAnswer) {
		p.starting = false
		delete(m.persist.asked, op)
		defer m.saveNow()
		switch a.Status {
		case "settled":
		case "refused":
			log.Printf("party space: %s (%d) has no %s in the ledger: refused", name, id, pack)
			m.refuseSpace(id, "pack", partyTextNoPack)
			return
		default:
			m.refund(op, id, "party_pack") // it may have settled unheard; the refund voids it if not
			m.refuseSpace(id, "ledger", partyTextLedger)
			return
		}
		if _, live := m.all[p]; !live || p.member(id) == nil || p.space != nil {
			// The party ended, or the creator left it, while the ledger answered.
			m.refund(op, id, "party_pack")
			if p.member(id) == nil {
				m.refuseSpace(id, "party", partyTextNoParty)
			} else {
				m.refuseSpace(id, "space", partyTextGoing)
			}
			return
		}
		m.createSpace(p, id, name, pack, template, taster, op)
	})
}

// asking keeps a spend with the ledger in the state file: if the server stops before the answer,
// the next one gives it back (party_persist.go).
func (m *parties) asking(op string, id int64, why string) {
	if m.spaces.ledger == nil {
		return
	}
	m.persist.asked[op] = partySpendRec{Op: op, User: id, Why: why}
	m.saveNow()
}

// createSpace is party.js create_space.
func (m *parties) createSpace(p *party, creator int64, name, pack, template string, taster bool, op string) {
	sid := m.spaces.newID()
	for m.spaces.byStreet[partySpaceStreet+strings.ToUpper(sid)] != nil {
		sid = m.spaces.newID()
	}
	dur := partyFullSpace
	if taster {
		dur = partyTasterSpace
	}
	s := &partySpace{id: sid, street: partySpaceStreet + strings.ToUpper(sid), template: template, pack: pack, taster: taster,
		creator: creator, creatorName: name, expires: m.h.Now().Add(dur)}
	if m.spaces.ledger != nil {
		s.spends = []partySpendRec{{Op: op, User: creator, Why: "party_pack"}}
	}
	p.space = s
	p.missed = nil
	m.spaces.byStreet[s.street] = p
	m.schedule(p, s)
	log.Printf("party space %s: %s (%d) opened %s with %s for %d members", s.street, name, creator, template, pack, len(p.members))
	m.pushSpace(p)
	m.announce(p, name+" made us a new Party Space!") // party.js:564
	for _, mem := range p.members {
		if mem.id == creator {
			m.to(mem.id, map[string]any{"t": "party_space_opened", "space": m.spaceView(s), "pack": pack, "spend": op})
		} else if m.h.ClientLocked(mem.id) != nil {
			m.prompt(p, mem.id)
		}
	}
}

// schedule sets the warning and the end from the space's expiry (create_space 591-592, extend_space_time 765-769).
func (m *parties) schedule(p *party, s *partySpace) {
	if s.warn != nil {
		s.warn()
	}
	if s.end != nil {
		s.end()
	}
	left := s.expires.Sub(m.h.Now())
	warnIn := max(0, left-partySpaceWarning)
	s.warn = m.h.After(warnIn, func() {
		if p.space != s {
			return
		}
		s.warned = true
		m.announce(p, partyTextWarning)
		m.pushSpace(p)
		m.saveSoon()
	})
	s.end = m.h.After(max(0, left), func() {
		if p.space == s {
			m.deleteSpace(p, "expired")
			m.saveNow()
		}
	})
}

// deleteSpace is party.js delete_space: everyone is told, and everyone in it is put out.
func (m *parties) deleteSpace(p *party, why string) {
	s := p.space
	if s == nil {
		return
	}
	if s.warn != nil {
		s.warn()
	}
	if s.end != nil {
		s.end()
	}
	p.space = nil
	delete(m.spaces.byStreet, s.street)
	if why == "expired" {
		m.announce(p, partyTextExpired)
	}
	for _, mem := range p.members {
		m.sendSpace(mem.id, nil, why)
		if m.h.ClientLocked(mem.id) == nil && (why == "expired" || why == "closed") {
			// Away (a dropped socket, a restart): told when they come back (spaceConnected).
			if p.missed == nil {
				p.missed = map[int64]string{}
			}
			p.missed[mem.id] = why
		}
	}
	for _, o := range m.h.StreetLocked(s.street) {
		m.h.leaveLocked(o)
	}
	log.Printf("party space %s: closed (%s)", s.street, why)
	m.saveSoon()
}

// spaceMemberGone: a member left the party (or was kicked): out of the space too.
func (m *parties) spaceMemberGone(p *party, id int64) {
	s := p.space
	if s == nil {
		return
	}
	m.sendSpace(id, nil, "left")
	if cl := m.h.ClientLocked(id); cl != nil && cl.Street() == s.street {
		m.h.leaveLocked(cl)
	}
}

// spaceJoined: someone joined a party with a space (accept_invite 305-316).
func (m *parties) spaceJoined(p *party, cl *Client) {
	if p.space == nil || m.of[cl.ID()] != p {
		return
	}
	m.sendSpace(cl.ID(), p.space, "")
	m.prompt(p, cl.ID())
}

// spaceConnected is party.js player_login 430-432: the space again, or, for a member who was
// away when it ended, that it ended (party.js delete_space 732-753 told only those online).
func (m *parties) spaceConnected(p *party, cl *Client) {
	id := cl.ID()
	why := p.missed[id]
	delete(p.missed, id)
	if p.space != nil {
		m.sendSpace(id, p.space, "")
		return
	}
	if why != "" {
		if why == "expired" {
			m.activity(id, partyTextExpired)
		}
		m.sendSpace(id, nil, why)
	}
}

// kickedInvite is send_invite 70-87: while a space lives, someone kicked comes
// back only on the creator's invite (which takes them off the list).
func (m *parties) kickedInvite(p *party, from, to int64) string {
	if p.space == nil || !p.kicked[to] {
		return ""
	}
	if p.space.creator != from {
		return "kicked"
	}
	delete(p.kicked, to)
	return ""
}

// spaceGate keeps a space's street to its party (party.js enter_space: only a member enters).
func (m *parties) spaceGate(cl *Client, street string) (bool, string, string) {
	if !strings.HasPrefix(street, partySpaceStreet) {
		return true, "", ""
	}
	if p := m.spaces.byStreet[street]; p != nil && p.space != nil && m.of[cl.ID()] == p {
		return true, "", ""
	}
	log.Printf("party space %s: refused %s (%d), not in its party", street, cl.Name(), cl.ID())
	return false, "private_street", partyTextPrivate
}

// extendSpace is party_atm.js onConversation + inc_party.js party_extend_space_time.
func (m *parties) extendSpace(cl *Client, frame json.RawMessage) {
	var msg struct {
		Minutes int `json:"minutes"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		m.h.ErrorLocked(cl, "bad_party_space", "That made no sense.")
		return
	}
	id := cl.ID()
	if !m.allowSpace(id) {
		m.refuseSpace(id, "slow", "Slow down a little.")
		return
	}
	p := m.of[id]
	if p == nil {
		m.refuseSpace(id, "currants", partyTextAtmBroke) // party_extend_space_time: no party, false
		return
	}
	s := p.space
	if s == nil || !s.expires.After(m.h.Now()) {
		m.refuseSpace(id, "late", partyTextAtmLate)
		return
	}
	if cl.Street() != s.street {
		m.h.ErrorLocked(cl, "party_space", partyTextCant) // the ATM stands in the space
		return
	}
	opts := partyExtendMinutes(s.taster)
	if msg.Minutes != opts[0] && msg.Minutes != opts[1] {
		m.h.ErrorLocked(cl, "bad_party_space", "That is not one of the choices.")
		return
	}
	if m.spaces.noLedger {
		m.refuseSpace(id, "ledger", partyTextLedger)
		return
	}
	cost := partyTemplates[s.template].prices[msg.Minutes]
	op, name, minutes := m.newSpend(), cl.Name(), msg.Minutes
	m.asking(op, id, "party_atm")
	m.ask(func(l spendLedger) ledgerAnswer {
		return l.spend(op, id, tradeGoods{Currants: cost}, "party_atm")
	}, func(a ledgerAnswer) {
		delete(m.persist.asked, op)
		defer m.saveNow()
		switch a.Status {
		case "settled":
		case "refused":
			m.refuseSpace(id, "currants", partyTextAtmBroke)
			return
		default:
			m.refund(op, id, "party_atm")
			m.refuseSpace(id, "ledger", partyTextLedger)
			return
		}
		if p.space != s || !s.expires.After(m.h.Now()) {
			m.refund(op, id, "party_atm")
			m.refuseSpace(id, "late", partyTextAtmLate)
			return
		}
		s.expires = s.expires.Add(time.Duration(minutes) * time.Minute)
		s.extensions++
		if m.spaces.ledger != nil {
			s.spends = append(s.spends, partySpendRec{Op: op, User: id, Why: "party_atm"})
		}
		if s.expires.Sub(m.h.Now()) > partySpaceWarning {
			s.warned = false
		}
		m.schedule(p, s)
		log.Printf("party space %s: %s (%d) extended it %d min for %dc", s.street, name, id, minutes, cost)
		m.to(id, map[string]any{"t": "party_space_extended", "minutes": minutes, "cost": cost, "spend": op, "text": partyTextAtmOK})
		m.announce(p, name+" extended the party time!") // party.js:770
		m.pushSpace(p)
	})
}

// kickFromSpace is main.js party_space_kick 3963-3976.
func (m *parties) kickFromSpace(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		m.h.ErrorLocked(cl, "bad_party_space", "That made no sense.")
		return
	}
	p := m.of[cl.ID()]
	target := m.h.ClientLocked(msg.ID)
	if p == nil || p.space == nil || p.space.creator != cl.ID() || cl.Street() != p.space.street ||
		target == nil || target.ID() == cl.ID() || target.Street() != p.space.street {
		m.h.ErrorLocked(cl, "party_space", partyTextCant)
		return
	}
	street := p.space.street
	if tp := m.of[target.ID()]; tp != nil {
		if tp.kicked == nil {
			tp.kicked = map[int64]bool{}
		}
		tp.kicked[target.ID()] = true
	}
	m.to(target.ID(), map[string]any{"t": "party_space_kicked", "by": map[string]any{"id": cl.ID(), "name": cl.Name()},
		"text": cl.Name() + " removed you from their Party Space."})
	m.leaveParty(target.ID())
	m.activity(cl.ID(), target.Name()+" was removed from your Party Space.")
	m.activity(target.ID(), cl.Name()+" removed you from their Party Space.")
	log.Printf("party space %s: %s (%d) kicked %s (%d)", street, cl.Name(), cl.ID(), target.Name(), target.ID())
}

// devSpace (GRODDLE_PARTY_DEV=1) brings the end near for QA (the game's &partyexpire).
func (m *parties) devSpace(cl *Client, frame json.RawMessage) {
	var msg struct {
		ExpireIn int `json:"expire_in"`
	}
	p := m.of[cl.ID()]
	if json.Unmarshal(frame, &msg) != nil || p == nil || p.space == nil || msg.ExpireIn <= 0 {
		return
	}
	d := min(time.Duration(msg.ExpireIn)*time.Second, partySpaceDevMax)
	p.space.expires = m.h.Now().Add(d)
	p.space.warned = false
	m.schedule(p, p.space)
	log.Printf("party space %s: dev end in %s", p.space.street, d)
	m.pushSpace(p)
}
