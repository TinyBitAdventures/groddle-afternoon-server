package main

// The Quarter's house lots (register G1186, Austin 2026-11-23): a shared block street of lots,
// each claimed by one player and showing their house (the game's js/quarter.js and
// features/quarter-feature.js). In Glitch a quarter's houses were ordinary locations bought
// through the familiar (GameServerJS players/inc_houses.js houses_familiar_no_owner 318-430,
// locations/inc_pols.js pols_canBuy 15-63): a house with an owner could not be bought
// ("owned", 24-29), and a player who owned one could not buy another ("has_other", 31-36: "This
// location is for sale. You already own a location, which you'll need to sell before you can
// buy a new one."). Ours: a lot is free, one per player, and giving it up puts it back on sale.
//
// The site keeps the claims (groddle-afternoon-site src/Quarter.php, table groddle_quarter_lots,
// one row per lot, one lot per player by its keys); this module holds the same table for the
// players online, checks a claim against it before asking the site, and tells everyone on the
// block when an owner changes. It loads the table from the site when it starts (POST
// groddle/v1/ledger/quarter/lots), so a restart loses nothing. Every claim and give-up asks the
// site (POST groddle/v1/ledger/quarter/claim, .../release, signed as ledger.go signs), whose
// answer carries the whole table: that answer is the truth, here and for the players.
//
// Client to server:
//
//	{"t":"quarter_claim","lot":1105}   on the block, a lot it has
//	{"t":"quarter_release"}            the sender's lot, wherever it is
//
// Server to client:
//
//	{"t":"quarter_lots","street":"GQR…","lots":[{"lot":1105,"owner":{"id":7,"name":"Ada"}}]}   after each join of a block, and to the block after each change
//	{"t":"quarter_claimed","op":"quarter-…","lot":1105,"status":"settled"|"refused","why":"lot|taken|have|no_home|newxp|busy|off","text":"…"}
//	{"t":"quarter_released","op":"quarter-…","lot":1105,"status":"settled"|"refused","why":"none|busy|off","text":"…"}
//
// Ours: one claim or give-up at a time per player and per lot; a lot the site is weighing a
// claim on is refused to everyone else as taken.

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"testing"
	"time"
)

//go:embed data/quarters.json
var quartersJSON []byte

const (
	ledgerRouteQuarterLots    = "/groddle/v1/ledger/quarter/lots"
	ledgerRouteQuarterClaim   = "/groddle/v1/ledger/quarter/claim"
	ledgerRouteQuarterRelease = "/groddle/v1/ledger/quarter/release"

	quarterLoadRetry = 15 * time.Second
	quarterBurst     = 4
	quarterEvery     = 2 * time.Second

	quarterTextTaken = "Someone else has just claimed that lot."                                                                             // ours
	quarterTextHave  = "This location is for sale. You already own a location, which you'll need to give up before you can claim a new one." // inc_houses.js:344 (sell and buy as give up and claim)
	quarterTextLot   = "There is no lot like that here."                                                                                     // ours
	quarterTextNone  = "You don't have a lot in the Quarter."                                                                                // ours
	quarterTextBusy  = "Wait a moment, the Quarter is still answering."                                                                      // ours
	quarterTextOff   = "The Quarter can not be reached right now."                                                                           // ours
	quarterTextHome  = "You need a home street first."                                                                                       // ours (Neighbours TEXT_NO_HOME)
	quarterTextNewxp = "You can't do that until you finish the tutorial."                                                                    // inc_houses.js admin_houses_visit newxp_us
)

func init() { modules = append(modules, newQuarter) }

// quarterData is data/quarters.json (the game's tools/build-realtime-quarters.mjs).
type quarterData struct {
	Streets map[string]struct {
		Label string `json:"label"`
		Lots  []int  `json:"lots"`
	} `json:"streets"`
}

func parseQuarterData(raw []byte) quarterData {
	var d quarterData
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Streets) == 0 {
		panic(fmt.Sprintf("realtime: data/quarters.json: %v", err))
	}
	return d
}

// quarterRow is one claimed lot as the site sends it.
type quarterRow struct {
	Street string `json:"street"`
	Lot    int    `json:"lot"`
	Owner  int64  `json:"owner"`
	Name   string `json:"name"`
}

// quarterCall is a claim or a give-up as the site is asked it.
type quarterCall struct {
	Op     string `json:"op"`
	ID     int64  `json:"id"`
	Street string `json:"street,omitempty"`
	Lot    int    `json:"lot,omitempty"`
}

// quarterAnswer is the site's word. Status "" means no answer came.
type quarterAnswer struct {
	Status string       `json:"status"` // settled | refused
	Why    string       `json:"why,omitempty"`
	Text   string       `json:"text,omitempty"`
	Street string       `json:"street,omitempty"`
	Lot    int          `json:"lot,omitempty"`
	Lots   []quarterRow `json:"lots"` // the whole table after it (nil: none sent)
}

// quarterSite is the site's ledger (httpLedger). Calls block; the module makes them off the hub lock.
type quarterSite interface {
	quarterClaim(c quarterCall) quarterAnswer
	quarterRelease(c quarterCall) quarterAnswer
	quarterLoad() ([]quarterRow, error)
}

func (l *httpLedger) quarterAsk(route string, c quarterCall) quarterAnswer {
	body, _ := json.Marshal(c)
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * ledgerPauseStep)
		}
		var a quarterAnswer
		err := l.dropPost(route, body, &a)
		if err == nil && (a.Status == "settled" || a.Status == "refused") {
			return a
		}
		if err == nil {
			err = fmt.Errorf("status %q", a.Status)
		}
		log.Printf("quarter: %s: %v", route, err)
	}
	return quarterAnswer{}
}

func (l *httpLedger) quarterClaim(c quarterCall) quarterAnswer {
	return l.quarterAsk(ledgerRouteQuarterClaim, c)
}

func (l *httpLedger) quarterRelease(c quarterCall) quarterAnswer {
	return l.quarterAsk(ledgerRouteQuarterRelease, c)
}

func (l *httpLedger) quarterLoad() ([]quarterRow, error) {
	var out struct {
		Lots []quarterRow `json:"lots"`
	}
	if err := l.dropPost(ledgerRouteQuarterLots, []byte(`{}`), &out); err != nil {
		return nil, err
	}
	return out.Lots, nil
}

// quarterHold is one claimed lot here.
type quarterHold struct {
	owner int64
	name  string
}

type quarter struct {
	h        *Hub
	data     quarterData
	site     quarterSite // nil: claims are off (no -ledger-url)
	spawn    func(func())
	newID    func() string
	loaded   bool
	loading  bool
	lots     map[string]map[int]*quarterHold // street -> lot -> owner
	asking   map[int64]string                // player -> the op the site is weighing
	claiming map[string]int64                // "street#lot" -> the player whose claim the site is weighing
	rates    map[int64]*bucket
}

func newQuarter(h *Hub) Module {
	m := &quarter{h: h, data: parseQuarterData(quartersJSON), spawn: func(fn func()) { go fn() },
		lots: map[string]map[int]*quarterHold{}, asking: map[int64]string{}, claiming: map[string]int64{}, rates: map[int64]*bucket{},
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return hex.EncodeToString(b)
		}}
	if !testing.Testing() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.site = l
				m.load()
			}
		}
	}
	return m
}

func (m *quarter) Register(r *Registry) {
	r.Handle("quarter_claim", m.claimFrame)
	r.Handle("quarter_release", m.releaseFrame)
	r.OnJoin(m.sendLots)
	r.OnDisconnect(func(cl *Client) { delete(m.rates, cl.ID()) })
	// Who holds a lot (for other modules and tests): 0 when nobody.
	r.Provide("quarter.owner", func(street string, lot int) int64 {
		if h := m.lots[street][lot]; h != nil {
			return h.owner
		}
		return 0
	})
}

// isBlock: a quarter block street.
func (m *quarter) isBlock(street string) bool {
	_, ok := m.data.Streets[street]
	return ok
}

func (m *quarter) hasLot(street string, lot int) bool {
	for _, n := range m.data.Streets[street].Lots {
		if n == lot {
			return true
		}
	}
	return false
}

// lotOf is the lot a player holds: its street and number, or "", 0.
func (m *quarter) lotOf(id int64) (string, int) {
	for street, lots := range m.lots {
		for lot, h := range lots {
			if h.owner == id {
				return street, lot
			}
		}
	}
	return "", 0
}

// setTable takes the site's whole table; it answers the blocks whose owners changed.
func (m *quarter) setTable(rows []quarterRow) []string {
	next := map[string]map[int]*quarterHold{}
	seen := map[int64]bool{}
	for _, r := range rows {
		if !m.hasLot(r.Street, r.Lot) || r.Owner <= 0 || seen[r.Owner] {
			continue
		}
		if next[r.Street] == nil {
			next[r.Street] = map[int]*quarterHold{}
		}
		if next[r.Street][r.Lot] != nil {
			continue
		}
		name := r.Name
		if name == "" {
			name = unnamedPlayer
		}
		seen[r.Owner] = true
		next[r.Street][r.Lot] = &quarterHold{owner: r.Owner, name: name}
	}
	changed := []string{}
	for street := range m.data.Streets {
		if quarterKey(m.lots[street]) != quarterKey(next[street]) {
			changed = append(changed, street)
		}
	}
	sort.Strings(changed)
	m.lots = next
	return changed
}

func quarterKey(lots map[int]*quarterHold) string {
	keys := make([]int, 0, len(lots))
	for lot := range lots {
		keys = append(keys, lot)
	}
	sort.Ints(keys)
	out := ""
	for _, lot := range keys {
		out += fmt.Sprintf("%d:%d:%s,", lot, lots[lot].owner, lots[lot].name)
	}
	return out
}

// load asks the site for every claimed lot, again until it answers.
func (m *quarter) load() {
	if m.site == nil || m.loading || m.loaded {
		return
	}
	m.loading = true
	site := m.site
	m.spawn(func() {
		rows, err := site.quarterLoad()
		m.h.After(0, func() {
			m.loading = false
			if err != nil {
				log.Printf("quarter: loading the lots: %v (again in %s)", err, quarterLoadRetry)
				m.h.After(quarterLoadRetry, m.load)
				return
			}
			m.loaded = true
			m.setTable(rows)
			log.Printf("quarter: %d lots claimed", len(rows))
			for street := range m.data.Streets {
				m.h.BroadcastStreetLocked(street, m.lotsMsg(street), 0)
			}
		})
	})
}

func (m *quarter) lotsMsg(street string) map[string]any {
	keys := []int{}
	for lot := range m.lots[street] {
		keys = append(keys, lot)
	}
	sort.Ints(keys)
	rows := []map[string]any{}
	for _, lot := range keys {
		h := m.lots[street][lot]
		rows = append(rows, map[string]any{"lot": lot, "owner": map[string]any{"id": h.owner, "name": h.name}})
	}
	return map[string]any{"t": "quarter_lots", "street": street, "lots": rows}
}

func (m *quarter) sendLots(cl *Client) {
	if m.site == nil || !m.loaded || !m.isBlock(cl.Street()) {
		return
	}
	m.h.SendLocked(cl, m.lotsMsg(cl.Street()))
}

func (m *quarter) allow(cl *Client) bool {
	b := m.rates[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.rates[cl.ID()] = b
	}
	if !b.take(m.h.Now(), quarterBurst, quarterEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return false
	}
	return true
}

func quarterText(why string) string {
	switch why {
	case "taken":
		return quarterTextTaken
	case "have":
		return quarterTextHave
	case "lot":
		return quarterTextLot
	case "none":
		return quarterTextNone
	case "busy":
		return quarterTextBusy
	case "no_home":
		return quarterTextHome
	case "newxp":
		return quarterTextNewxp
	}
	return quarterTextOff
}

func (m *quarter) answer(cl *Client, kind, op string, lot int, status, why, text string) {
	if text == "" && status == "refused" {
		text = quarterText(why)
	}
	m.h.SendLocked(cl, map[string]any{"t": kind, "op": op, "lot": lot, "status": status, "why": why, "text": text})
}

// judgeClaim is pols_canBuy against the table here (the site checks again): "" or why.
func (m *quarter) judgeClaim(id int64, street string, lot int) string {
	if !m.isBlock(street) || !m.hasLot(street, lot) {
		return "lot"
	}
	if h := m.lots[street][lot]; h != nil && h.owner != id {
		return "taken" // inc_pols.js 24-29 'owned'
	}
	if by, ok := m.claiming[fmt.Sprintf("%s#%d", street, lot)]; ok && by != id {
		return "taken"
	}
	if s, l := m.lotOf(id); l != 0 && (s != street || l != lot) {
		return "have" // inc_pols.js 31-36 'has_other'
	}
	return ""
}

// claimFrame claims a lot on the block the sender stands on.
func (m *quarter) claimFrame(cl *Client, frame json.RawMessage) {
	if !m.allow(cl) {
		return
	}
	var msg struct {
		Lot int `json:"lot"`
	}
	_ = json.Unmarshal(frame, &msg)
	street, id := cl.Street(), cl.ID()
	if m.site == nil || !m.loaded {
		m.answer(cl, "quarter_claimed", "", msg.Lot, "refused", "off", "")
		if m.site != nil {
			m.load()
		}
		return
	}
	if _, busy := m.asking[id]; busy {
		m.answer(cl, "quarter_claimed", "", msg.Lot, "refused", "busy", "")
		return
	}
	if why := m.judgeClaim(id, street, msg.Lot); why != "" {
		m.answer(cl, "quarter_claimed", "", msg.Lot, "refused", why, "")
		return
	}
	call := quarterCall{Op: "quarter-" + m.newID(), ID: id, Street: street, Lot: msg.Lot}
	key := fmt.Sprintf("%s#%d", street, msg.Lot)
	m.asking[id] = call.Op
	m.claiming[key] = id
	site := m.site
	m.spawn(func() {
		a := site.quarterClaim(call)
		m.h.After(0, func() {
			if m.asking[id] == call.Op {
				delete(m.asking, id)
			}
			if m.claiming[key] == id {
				delete(m.claiming, key)
			}
			m.settle(id, "quarter_claimed", call, a)
		})
	})
}

// releaseFrame gives up the sender's lot.
func (m *quarter) releaseFrame(cl *Client, frame json.RawMessage) {
	if !m.allow(cl) {
		return
	}
	id := cl.ID()
	if m.site == nil || !m.loaded {
		m.answer(cl, "quarter_released", "", 0, "refused", "off", "")
		return
	}
	if _, busy := m.asking[id]; busy {
		m.answer(cl, "quarter_released", "", 0, "refused", "busy", "")
		return
	}
	street, lot := m.lotOf(id)
	if lot == 0 {
		m.answer(cl, "quarter_released", "", 0, "refused", "none", "")
		return
	}
	call := quarterCall{Op: "quarter-" + m.newID(), ID: id, Street: street, Lot: lot}
	m.asking[id] = call.Op
	site := m.site
	m.spawn(func() {
		a := site.quarterRelease(call)
		m.h.After(0, func() {
			if m.asking[id] == call.Op {
				delete(m.asking, id)
			}
			m.settle(id, "quarter_released", call, a)
		})
	})
}

// settle tells the asker and, when the site sent the table, every block whose owners changed.
func (m *quarter) settle(id int64, kind string, call quarterCall, a quarterAnswer) {
	if a.Status == "" {
		a.Status, a.Why = "refused", "off"
	}
	lot := call.Lot
	if a.Lot != 0 {
		lot = a.Lot
	}
	if c := m.h.ClientLocked(id); c != nil {
		m.answer(c, kind, call.Op, lot, a.Status, a.Why, a.Text)
	}
	if a.Lots == nil {
		return
	}
	for _, street := range m.setTable(a.Lots) {
		log.Printf("quarter: %s %d on %s: %s", kind, id, street, a.Status)
		m.h.BroadcastStreetLocked(street, m.lotsMsg(street), 0)
	}
}
