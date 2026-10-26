package main

// Trading (multiplayer phase 4): two players on the same street offer items
// and currants, both accept, and the server says what each one receives, as
// in the original. The rules are GameServerJS players/inc_trading.js and
// main.js (doStartTrade 5868 .. doTradeUnlock 5952); the client flow and its
// words are glitch-client TradeDialog.as and TradeManager.as.
//
// Client to server:
//
//	{"t":"trade_start","id":7}                     offer to trade with player 7 (or "name":"Ada", someone on your street)
//	{"t":"trade_offer","trade":"…","v":3,"items":[{"c":"apple","n":5,"r":40}],"currants":20}
//	                                               your whole offer, version v (each one higher); r is a tool's points left
//	{"t":"trade_accept","trade":"…","mine":3,"seen":2}  accept: your offer v3 for theirs v2
//	{"t":"trade_unlock","trade":"…"}               edit again (your acceptance goes)
//	{"t":"trade_cancel","trade":"…"}
//	{"t":"trade_check","trade":"…"}                you hold items for this trade: what became of it?
//	{"t":"trade_ack","trade":"…"}                  you applied its trade_done and saved
//
// Server to client:
//
//	{"t":"trade_open","trade":"…","with":{"id":7,"name":"Ada"},"by":7}
//	{"t":"trade_state","trade":"…","mine":Side,"theirs":Side,"rejected":4}   after every change; rejected: your offer v4 was refused
//	{"t":"trade_cancel","trade":"…","by":{"id":7,"name":"Ada"}}           it ended without a swap (by is absent when you ended it)
//	{"t":"trade_void","trade":"…"}                 the answer to a check: it never completed, take your items back
//	{"t":"trade_done","trade":"…","with":{…},"gave":Offer,"got":Offer}   sent until you trade_ack it, again on each connect
//	{"t":"trade_activity","text":"…"}              a line for the activity log
//	{"t":"error","code":"trade","message":"…"}     the source's refusal, said to the player
//
// Side is {"v","items","currants","accepted"}; Offer is {"items","currants"}.
//
// Escrow. The source moves offered items out of the pack into a hidden escrow
// bag (trading_add_item_do 422-579, bag_escrow, 6 slots) and currants into
// trading.currants (805-868); cancel or logout puts them back (rollback
// 288-349, player.js:242 and :171), completion moves each escrow into the
// other pack (trading_transfer 1065-1130). Here each client keeps its own
// escrow, the trade hold, saved with the character before it sends an offer;
// the server holds both offers and decides. A trade_done lists exactly what
// each side gave and gets; the client removes the gave part of its hold,
// returns the rest, adds the got part and saves, then acks. Until the ack the
// done stays pending: it is sent again on every connect and answers every
// check, and that character cannot start another trade (the source's
// "unsettled items from a previous trade", 99-104). Pending dones are written
// to a journal before they are sent (tradeJournal), so a server restart does
// not forget a swap one side has not applied yet.
//
// Rules kept from the source:
//   - Start (trading_request_start 79-210): not while trading or unsettled,
//     not with yourself, the other player online and on the same street, not
//     busy or unsettled (they hear why); the window opens for both at once
//     (trade_start is sent to the target, the requester opens on its ok).
//   - Any change to either offer clears both acceptances (389-390, 611-612,
//     740-741, 831-832); unlock clears only your own (948); when both have
//     accepted the trade completes (911-916).
//   - Leaving the street (teleport, player.js:611), going offline (logout,
//     player.js:242) and cancelling end it; the other player is told
//     "<name> canceled their trade with you." (268-275).
//   - Items: at most 6 slots (TradeDialog.as:50), each one stack of one
//     class up to its stackmax (MakingSlot.as:162); no_trade and soulbound
//     classes refused (TradeDialog.as:1061, inc_trading.js:473-479), from
//     realtime/data/trade.json (tools/build-realtime-trade.mjs).
//
// Ours: an accept names the offer versions it accepts, so nobody accepts an
// offer they have not seen; a re-join within tradeSettle (a new outfit) keeps
// the trade; a new socket for a character ends its open trade (the old page's
// escrow comes back through trade_check); rates and caps.
//
// Client-trusted limits (saves are the client's in this phase): the server
// cannot see packs, so it trusts that an offer is backed by items the client
// moved into its hold; a modified client can offer what it does not have, or
// never apply a done. What the server does guarantee: an offer is only ever
// swapped once, both sides get the same record, and a swap is never both
// completed and voided.

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

//go:embed data/trade.json
var tradeJSON []byte

const (
	tradeSlots       = 6             // TradeDialog.as:50 MAX_TRADE_SLOTS
	tradeSlotMax     = 9999          // MakingSlot.as:25 MAX_COUNT
	tradeMaxCurrants = 1_000_000_000 // ours: far above any purse
	tradeMaxPoints   = 1_000_000     // ours: a tool's points left, passed on as sent
	tradeSettle      = 1500 * time.Millisecond
	tradeStartBurst  = 3
	tradeStartEvery  = 3 * time.Second
	tradeEditBurst   = 20
	tradeEditEvery   = 150 * time.Millisecond
	tradeMaxName     = 64
	tradeMaxID       = 40
	tradeCompactAt   = 2000 // journal lines before it is rewritten
)

// Words players read: inc_trading.js unless marked ours.
const (
	tradeTextBusySelf     = "You are already trading with someone else. Finish that up first."                                                      // 91
	tradeTextUnsettled    = "You have unsettled items from a previous trade. Make some room in your pack for them before attempting another trade." // 102
	tradeTextSelf         = "No trading with yourself."                                                                                             // 113
	tradeTextNoPlayer     = "Not a valid target player."                                                                                            // 121
	tradeTextOffline      = "That player is not online."                                                                                            // 135
	tradeTextElsewhere    = "Oh come on, they're not even in the same location as you!"                                                             // 142
	tradeTextTheyBusy     = "That player is already trading with someone else. Please wait until they are done."                                    // 154
	tradeTextUnavailable  = "That player is not available for trading right now."                                                                   // 166
	tradeTextNotTrading   = "You are not trading with that player."                                                                                 // 225
	tradeTextCantTrade    = "You can't trade this item!"                                                                                            // TradeDialog.as:1063
	tradeTextSoulbound    = "That item is locked to you, and can't be traded."                                                                      // 477
	tradeTextTooMany      = "You don't have enough of that item."                                                                                   // 456
	tradeTextSlot         = "That won't fit in the trade window."                                                                                   // ours
	tradeTextCurrants     = "You don't have enough currants for that."                                                                              // 846
	tradeTextSeenStale    = "Their offer just changed. Have another look before you accept."                                                        // ours
	tradeTextNotFinished  = "The trade could not be finished, so nothing changed hands."                                                            // ours
	tradeTextBusyHeard    = " wanted to trade with you, but you are busy."                                                                          // 151
	tradeTextUnsettledHrd = " wanted to trade with you, but you have an unsettled previous trade."                                                  // 163
)

// tradeJournalPath is where finished trades wait for both acks. Empty: beside
// the -secret-file (realtime-trades.jsonl), or memory only without one;
// "off": memory only.
var tradeJournalPath = flag.String("trade-journal", "", "file that keeps finished trades until both players have applied them (default: realtime-trades.jsonl beside -secret-file; off: memory only)")

// tradeJournalForTest lets a test give the module a journal; under go test the flag is ignored.
var tradeJournalForTest string

func init() { modules = append(modules, newTrades) }

type tradeClass struct {
	Max    int    `json:"max"`
	Name   string `json:"name"`
	Plural string `json:"plural"`
}

type tradeTable struct {
	Slots       int                   `json:"slots"`
	Items       map[string]tradeClass `json:"items"`
	Untradeable map[string]string     `json:"untradeable"`
}

type tradeItem struct {
	C string `json:"c"`
	N int    `json:"n"`
	R *int   `json:"r,omitempty"`
}

// tradeGoods is what one side puts in: items and currants.
type tradeGoods struct {
	Items    []tradeItem `json:"items"`
	Currants int64       `json:"currants"`
}

func (g tradeGoods) empty() bool { return len(g.Items) == 0 && g.Currants == 0 }

func (g tradeGoods) same(o tradeGoods) bool {
	if g.Currants != o.Currants || len(g.Items) != len(o.Items) {
		return false
	}
	for i := range g.Items {
		a, b := g.Items[i], o.Items[i]
		if a.C != b.C || a.N != b.N || (a.R == nil) != (b.R == nil) || (a.R != nil && *a.R != *b.R) {
			return false
		}
	}
	return true
}

type tradeSide struct {
	id       int64
	name     string
	v        int64
	goods    tradeGoods
	accepted bool
}

type openTrade struct {
	id     string
	street string
	by     int64
	sides  [2]*tradeSide
	settle func() bool
}

func (t *openTrade) side(id int64) (me, them *tradeSide) {
	if t.sides[0].id == id {
		return t.sides[0], t.sides[1]
	}
	if t.sides[1].id == id {
		return t.sides[1], t.sides[0]
	}
	return nil, nil
}

// tradeRecord is a finished trade until both sides ack it (journalled).
type tradeRecord struct {
	Trade string        `json:"trade"`
	At    int64         `json:"at"`
	IDs   [2]int64      `json:"ids"`
	Names [2]string     `json:"names"`
	Gave  [2]tradeGoods `json:"gave"`
	Acked [2]bool       `json:"acked"`
}

func (r *tradeRecord) index(id int64) int {
	if r.IDs[0] == id && !r.Acked[0] {
		return 0
	}
	if r.IDs[1] == id && !r.Acked[1] {
		return 1
	}
	return -1
}

type trades struct {
	h       *Hub
	table   tradeTable
	boot    string
	seq     int64
	of      map[int64]*openTrade
	byID    map[string]*openTrade
	done    map[string]*tradeRecord
	starts  map[int64]*bucket
	edits   map[int64]*bucket
	journal *tradeJournal
}

func newTrades(h *Hub) Module {
	var table tradeTable
	if err := json.Unmarshal(tradeJSON, &table); err != nil || len(table.Items) == 0 {
		panic(fmt.Sprintf("realtime: data/trade.json: %v", err))
	}
	path := tradeJournalForTest
	if !testing.Testing() && tradeJournalForTest == "" {
		path = *tradeJournalPath
		if path == "" {
			if f := flag.Lookup("secret-file"); f != nil && f.Value.String() != "" {
				path = filepath.Join(filepath.Dir(expandHome(f.Value.String())), "realtime-trades.jsonl")
			}
		}
	}
	if path == "off" {
		path = ""
	}
	return newTradesWith(h, table, path)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func newTradesWith(h *Hub, table tradeTable, journalPath string) *trades {
	if table.Slots <= 0 || table.Slots > tradeSlots {
		table.Slots = tradeSlots
	}
	m := &trades{h: h, table: table, boot: strconv.FormatInt(h.Now().UnixMilli(), 36),
		of: map[int64]*openTrade{}, byID: map[string]*openTrade{}, done: map[string]*tradeRecord{},
		starts: map[int64]*bucket{}, edits: map[int64]*bucket{}}
	if journalPath != "" {
		j, pending, err := openTradeJournal(journalPath)
		if err != nil {
			log.Printf("trade journal %s: %v (finished trades are kept in memory only)", journalPath, err)
		} else {
			m.journal = j
			for _, r := range pending {
				m.done[r.Trade] = r
			}
			log.Printf("trade journal %s: %d finished trades waiting for an ack", journalPath, len(pending))
		}
	}
	return m
}

func (m *trades) Register(r *Registry) {
	r.Handle("trade_start", m.start)
	r.Handle("trade_offer", m.offer)
	r.Handle("trade_accept", m.accept)
	r.Handle("trade_unlock", m.unlock)
	r.Handle("trade_cancel", m.cancelMsg)
	r.Handle("trade_check", m.check)
	r.Handle("trade_ack", m.ack)
	r.OnConnect(m.connected)
	r.OnJoin(m.joined)
	r.OnLeave(func(cl *Client, _ string) { m.left(cl) })
	r.OnDisconnect(func(cl *Client) {
		if t := m.of[cl.ID()]; t != nil {
			m.cancel(t, cl.ID())
		}
	})
}

// —— Sending ——

func (m *trades) to(id int64, msg any) {
	if cl := m.h.ClientLocked(id); cl != nil {
		m.h.SendLocked(cl, msg)
	}
}

func (m *trades) refuse(cl *Client, text string) { m.h.ErrorLocked(cl, "trade", text) }

func sideJSON(s *tradeSide) map[string]any {
	items := s.goods.Items
	if items == nil {
		items = []tradeItem{}
	}
	return map[string]any{"v": s.v, "items": items, "currants": s.goods.Currants, "accepted": s.accepted}
}

func goodsJSON(g tradeGoods) map[string]any {
	items := g.Items
	if items == nil {
		items = []tradeItem{}
	}
	return map[string]any{"items": items, "currants": g.Currants}
}

// pushState sends both sides the trade as each sees it.
func (m *trades) pushState(t *openTrade) {
	for _, s := range t.sides {
		m.stateTo(t, s.id, 0)
	}
}

// stateTo sends one side the trade; rejected names an offer version of theirs that was refused.
func (m *trades) stateTo(t *openTrade, id int64, rejected int64) {
	me, them := t.side(id)
	msg := map[string]any{"t": "trade_state", "trade": t.id, "mine": sideJSON(me), "theirs": sideJSON(them)}
	if rejected > 0 {
		msg["rejected"] = rejected
	}
	m.to(id, msg)
}

func (m *trades) sendOpen(t *openTrade, id int64) {
	_, them := t.side(id)
	m.to(id, map[string]any{"t": "trade_open", "trade": t.id, "with": map[string]any{"id": them.id, "name": them.name}, "by": t.by})
}

func (m *trades) sendDone(r *tradeRecord, i int) {
	o := 1 - i
	m.to(r.IDs[i], map[string]any{"t": "trade_done", "trade": r.Trade, "with": map[string]any{"id": r.IDs[o], "name": r.Names[o]},
		"gave": goodsJSON(r.Gave[i]), "got": goodsJSON(r.Gave[o])})
}

// unsettled: a finished trade this character has not acked yet.
func (m *trades) unsettled(id int64) bool {
	for _, r := range m.done {
		if r.index(id) >= 0 {
			return true
		}
	}
	return false
}

func (m *trades) pendingFor(id int64) []*tradeRecord {
	var out []*tradeRecord
	for _, r := range m.done {
		if r.index(id) >= 0 {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].At < out[b].At })
	return out
}

// —— Rates ——

func (m *trades) allow(set map[int64]*bucket, cl *Client, burst float64, every time.Duration) bool {
	b := set[cl.ID()]
	if b == nil {
		b = &bucket{}
		set[cl.ID()] = b
	}
	if b.take(m.h.Now(), burst, every) {
		return true
	}
	m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
	return false
}

// —— Handlers ——

func (m *trades) start(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(frame, &msg) != nil || (msg.ID <= 0 && strings.TrimSpace(msg.Name) == "") {
		m.refuse(cl, tradeTextNoPlayer)
		return
	}
	if !m.allow(m.starts, cl, tradeStartBurst, tradeStartEvery) {
		return
	}
	if m.of[cl.ID()] != nil {
		m.refuse(cl, tradeTextBusySelf)
		return
	}
	if m.unsettled(cl.ID()) {
		m.refuse(cl, tradeTextUnsettled)
		return
	}
	var target *Client
	if msg.ID > 0 {
		if msg.ID == cl.ID() {
			m.refuse(cl, tradeTextSelf)
			return
		}
		target = m.h.ClientLocked(msg.ID)
		if target == nil {
			m.refuse(cl, tradeTextOffline)
			return
		}
	} else {
		name := cleanChat(msg.Name)
		if r := []rune(name); len(r) > tradeMaxName {
			name = string(r[:tradeMaxName])
		}
		if strings.EqualFold(name, cl.Name()) {
			m.refuse(cl, tradeTextSelf)
			return
		}
		if cl.Street() != "" {
			for _, o := range m.h.StreetLocked(cl.Street()) {
				if o.ID() != cl.ID() && strings.EqualFold(o.Name(), name) {
					target = o
					break
				}
			}
		}
		if target == nil {
			m.refuse(cl, tradeTextNoPlayer)
			return
		}
	}
	if cl.Street() == "" || target.Street() != cl.Street() {
		m.refuse(cl, tradeTextElsewhere)
		return
	}
	if m.of[target.ID()] != nil {
		m.to(target.ID(), map[string]any{"t": "trade_activity", "text": cl.Name() + tradeTextBusyHeard})
		m.refuse(cl, tradeTextTheyBusy)
		return
	}
	if m.unsettled(target.ID()) {
		m.to(target.ID(), map[string]any{"t": "trade_activity", "text": cl.Name() + tradeTextUnsettledHrd})
		m.refuse(cl, tradeTextUnavailable)
		return
	}
	m.seq++
	t := &openTrade{id: m.boot + "-" + strconv.FormatInt(m.seq, 36), street: cl.Street(), by: cl.ID(),
		sides: [2]*tradeSide{{id: cl.ID(), name: cl.Name()}, {id: target.ID(), name: target.Name()}}}
	m.of[cl.ID()] = t
	m.of[target.ID()] = t
	m.byID[t.id] = t
	m.sendOpen(t, cl.ID())
	m.sendOpen(t, target.ID())
	m.pushState(t)
}

// mine finds the open trade a message names, for a character in it.
func (m *trades) mine(cl *Client, id string) (*openTrade, *tradeSide, *tradeSide) {
	t := m.of[cl.ID()]
	if t == nil || t.id != id {
		return nil, nil, nil
	}
	me, them := t.side(cl.ID())
	return t, me, them
}

func tradeIDOK(id string) bool {
	if id == "" || len(id) > tradeMaxID {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// checkGoods validates an offer against the trade table; "" when it is fine.
func (m *trades) checkGoods(g *tradeGoods) string {
	if len(g.Items) > m.table.Slots {
		return tradeTextSlot
	}
	for i := range g.Items {
		it := &g.Items[i]
		cls, ok := m.table.Items[it.C]
		if !ok {
			if m.table.Untradeable[it.C] == "soulbound" {
				return tradeTextSoulbound
			}
			return tradeTextCantTrade
		}
		if it.N < 1 {
			return tradeTextTooMany
		}
		if it.N > cls.Max || it.N > tradeSlotMax {
			return tradeTextSlot
		}
		if it.R != nil && (*it.R < 0 || *it.R > tradeMaxPoints) {
			return tradeTextCantTrade
		}
	}
	if g.Currants < 0 || g.Currants > tradeMaxCurrants {
		return tradeTextCurrants
	}
	return ""
}

func (m *trades) offer(cl *Client, frame json.RawMessage) {
	var msg struct {
		Trade    string      `json:"trade"`
		V        int64       `json:"v"`
		Items    []tradeItem `json:"items"`
		Currants int64       `json:"currants"`
	}
	if json.Unmarshal(frame, &msg) != nil || !tradeIDOK(msg.Trade) {
		m.refuse(cl, tradeTextNotTrading)
		return
	}
	if !m.allow(m.edits, cl, tradeEditBurst, tradeEditEvery) {
		if t, me, _ := m.mine(cl, msg.Trade); t != nil && msg.V > me.v {
			m.stateTo(t, cl.ID(), msg.V)
		}
		return
	}
	t, me, them := m.mine(cl, msg.Trade)
	if t == nil {
		m.refuse(cl, tradeTextNotTrading)
		return
	}
	if msg.V <= me.v || msg.V > 1<<40 {
		return // a stale or repeated version: the newer one already stands
	}
	goods := tradeGoods{Items: msg.Items, Currants: msg.Currants}
	if why := m.checkGoods(&goods); why != "" {
		m.refuse(cl, why)
		m.stateTo(t, cl.ID(), msg.V)
		return
	}
	changed := !goods.same(me.goods)
	me.v = msg.V
	me.goods = goods
	if changed {
		me.accepted = false
		them.accepted = false
	}
	m.pushState(t)
}

func (m *trades) accept(cl *Client, frame json.RawMessage) {
	var msg struct {
		Trade string `json:"trade"`
		Mine  int64  `json:"mine"`
		Seen  int64  `json:"seen"`
	}
	if json.Unmarshal(frame, &msg) != nil || !tradeIDOK(msg.Trade) {
		m.refuse(cl, tradeTextNotTrading)
		return
	}
	if !m.allow(m.edits, cl, tradeEditBurst, tradeEditEvery) {
		return
	}
	t, me, them := m.mine(cl, msg.Trade)
	if t == nil {
		m.refuse(cl, tradeTextNotTrading)
		return
	}
	if msg.Mine != me.v || msg.Seen != them.v {
		m.refuse(cl, tradeTextSeenStale)
		m.pushState(t)
		return
	}
	me.accepted = true
	if them.accepted {
		m.complete(t)
		return
	}
	m.pushState(t)
}

func (m *trades) unlock(cl *Client, frame json.RawMessage) {
	var msg struct {
		Trade string `json:"trade"`
	}
	if json.Unmarshal(frame, &msg) != nil || !tradeIDOK(msg.Trade) {
		return
	}
	if !m.allow(m.edits, cl, tradeEditBurst, tradeEditEvery) {
		return
	}
	t, me, _ := m.mine(cl, msg.Trade)
	if t == nil {
		m.refuse(cl, tradeTextNotTrading)
		return
	}
	me.accepted = false
	m.pushState(t)
}

func (m *trades) cancelMsg(cl *Client, frame json.RawMessage) {
	var msg struct {
		Trade string `json:"trade"`
	}
	if json.Unmarshal(frame, &msg) != nil || !tradeIDOK(msg.Trade) {
		return
	}
	if t, _, _ := m.mine(cl, msg.Trade); t != nil {
		m.cancel(t, cl.ID())
		return
	}
	m.answer(cl, msg.Trade)
}

func (m *trades) check(cl *Client, frame json.RawMessage) {
	var msg struct {
		Trade string `json:"trade"`
	}
	if json.Unmarshal(frame, &msg) != nil || !tradeIDOK(msg.Trade) {
		return
	}
	if t, _, _ := m.mine(cl, msg.Trade); t != nil {
		m.sendOpen(t, cl.ID())
		m.pushState(t)
		return
	}
	m.answer(cl, msg.Trade)
}

// answer says what became of a trade that is not open for cl: done (until
// acked) or void.
func (m *trades) answer(cl *Client, id string) {
	if r := m.done[id]; r != nil {
		if i := r.index(cl.ID()); i >= 0 {
			m.sendDone(r, i)
			return
		}
	}
	m.h.SendLocked(cl, map[string]any{"t": "trade_void", "trade": id})
}

func (m *trades) ack(cl *Client, frame json.RawMessage) {
	var msg struct {
		Trade string `json:"trade"`
	}
	if json.Unmarshal(frame, &msg) != nil || !tradeIDOK(msg.Trade) {
		return
	}
	r := m.done[msg.Trade]
	if r == nil {
		return
	}
	i := r.index(cl.ID())
	if i < 0 {
		return
	}
	r.Acked[i] = true
	if m.journal != nil {
		if err := m.journal.write(tradeEntry{Ack: msg.Trade, Who: cl.ID()}); err != nil {
			log.Printf("trade journal: %v", err)
		}
	}
	if r.Acked[0] && r.Acked[1] {
		delete(m.done, msg.Trade)
		m.compact()
	}
}

// —— Endings ——

func (m *trades) drop(t *openTrade) {
	if t.settle != nil {
		t.settle()
		t.settle = nil
	}
	for _, s := range t.sides {
		if m.of[s.id] == t {
			delete(m.of, s.id)
		}
	}
	delete(m.byID, t.id)
}

// cancel ends a trade with no swap (trading_cancel 216-286): both escrows go
// back; the other side is told who ended it.
func (m *trades) cancel(t *openTrade, by int64) {
	m.drop(t)
	var byName string
	if s, _ := t.side(by); s != nil {
		byName = s.name
	}
	for _, s := range t.sides {
		msg := map[string]any{"t": "trade_cancel", "trade": t.id}
		if s.id != by && byName != "" {
			msg["by"] = map[string]any{"id": by, "name": byName}
		}
		m.to(s.id, msg)
	}
}

// complete swaps the offers (trading_complete 968-1058): the record is
// journalled before anyone hears of it.
func (m *trades) complete(t *openTrade) {
	r := &tradeRecord{Trade: t.id, At: m.h.Now().UnixMilli(),
		IDs: [2]int64{t.sides[0].id, t.sides[1].id}, Names: [2]string{t.sides[0].name, t.sides[1].name},
		Gave: [2]tradeGoods{t.sides[0].goods, t.sides[1].goods}}
	if m.journal != nil {
		if err := m.journal.write(tradeEntry{Done: r}); err != nil {
			log.Printf("trade journal: %v (trade %s cancelled)", err, t.id)
			for _, s := range t.sides {
				m.to(s.id, map[string]any{"t": "trade_activity", "text": tradeTextNotFinished})
			}
			m.cancel(t, 0)
			return
		}
	}
	m.drop(t)
	m.done[r.Trade] = r
	m.sendDone(r, 0)
	m.sendDone(r, 1)
}

// —— Hooks ——

// connected: a new socket ends any open trade (its page starts afresh) and
// hears every done it has not acked.
func (m *trades) connected(cl *Client) {
	if t := m.of[cl.ID()]; t != nil {
		m.cancel(t, cl.ID())
	}
	for _, r := range m.pendingFor(cl.ID()) {
		m.sendDone(r, r.index(cl.ID()))
	}
}

func (m *trades) joined(cl *Client) {
	t := m.of[cl.ID()]
	if t == nil {
		return
	}
	if cl.Street() != t.street {
		m.cancel(t, cl.ID())
	}
}

// left: off the street. A join back within tradeSettle (a new outfit joins
// again) keeps the trade; anything else ends it (player.js:611).
func (m *trades) left(cl *Client) {
	t := m.of[cl.ID()]
	if t == nil || t.settle != nil {
		return
	}
	id := cl.ID()
	t.settle = m.h.After(tradeSettle, func() {
		t.settle = nil
		if m.byID[t.id] != t {
			return
		}
		for _, s := range t.sides {
			o := m.h.ClientLocked(s.id)
			if o == nil || o.Street() != t.street {
				m.cancel(t, id)
				return
			}
		}
	})
}

// —— Journal ——

type tradeEntry struct {
	Done *tradeRecord `json:"done,omitempty"`
	Ack  string       `json:"ack,omitempty"`
	Who  int64        `json:"who,omitempty"`
}

// tradeJournal is an append-only file of finished trades and acks, synced on
// every write; on open it is replayed and rewritten with only what is still
// waiting for an ack.
type tradeJournal struct {
	path  string
	f     *os.File
	lines int
}

func openTradeJournal(path string) (*tradeJournal, []*tradeRecord, error) {
	pending := map[string]*tradeRecord{}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			var e tradeEntry
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue // a torn last line from a crash
			}
			if e.Done != nil && tradeIDOK(e.Done.Trade) {
				if old := pending[e.Done.Trade]; old != nil {
					e.Done.Acked[0] = e.Done.Acked[0] || old.Acked[0]
					e.Done.Acked[1] = e.Done.Acked[1] || old.Acked[1]
				}
				pending[e.Done.Trade] = e.Done
			} else if r := pending[e.Ack]; r != nil {
				if r.IDs[0] == e.Who {
					r.Acked[0] = true
				}
				if r.IDs[1] == e.Who {
					r.Acked[1] = true
				}
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	var list []*tradeRecord
	for id, r := range pending {
		if r.Acked[0] && r.Acked[1] {
			delete(pending, id)
			continue
		}
		list = append(list, r)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].At < list[b].At })
	j := &tradeJournal{path: path}
	if err := j.rewrite(list); err != nil {
		return nil, nil, err
	}
	return j, list, nil
}

// rewrite replaces the file with one done line per record (through a temp file and a rename).
func (j *tradeJournal) rewrite(list []*tradeRecord) error {
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return err
	}
	tmp := j.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range list {
		b, _ := json.Marshal(tradeEntry{Done: r})
		w.Write(append(b, '\n'))
	}
	if err := w.Flush(); err == nil {
		err = f.Sync()
	}
	f.Close()
	if err := os.Rename(tmp, j.path); err != nil {
		return err
	}
	if j.f != nil {
		j.f.Close()
	}
	j.f, err = os.OpenFile(j.path, os.O_APPEND|os.O_WRONLY, 0o600)
	j.lines = len(list)
	return err
}

func (j *tradeJournal) write(e tradeEntry) error {
	if j.f == nil {
		return errors.New("journal closed")
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return err
	}
	j.lines++
	return j.f.Sync()
}

// compact rewrites a long journal down to what is still pending.
func (m *trades) compact() {
	if m.journal == nil || m.journal.lines < tradeCompactAt {
		return
	}
	var list []*tradeRecord
	for _, r := range m.done {
		list = append(list, r)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].At < list[b].At })
	if err := m.journal.rewrite(list); err != nil {
		log.Printf("trade journal: %v", err)
	}
}
