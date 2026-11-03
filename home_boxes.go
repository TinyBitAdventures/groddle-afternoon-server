package main

// Storage in a real player's house that other players reach (ticket 51,
// register G404): buying from a Storage Display Box, and a key-holder taking
// from or putting into a cabinet. The rules are GameServerJS
// items/bag_furniture_sdb.js (purchase 577-609 and sellItem 1139-1278: anyone
// but the owner, standing in the house) and items/bag_furniture_cabinet.js
// (verbs.open 294-340 and isOwner 452-466: the owner or a key-holder); the
// site decides everything else from what it stores
// (groddle-afternoon-site src/LedgerBoxes.php): the price and the stock are
// the stored box's, never a client's, the count is cut to the stock, the
// currants and the things move in the ledger in one transaction, and the
// currants wait on the box until the owner collects them. Each purchase or
// move carries the client's own op id and settles once.
//
// Here: the buyer or key-holder must stand in HOUSE_<owner> (homes.go lets in
// only a key-holder, or someone the owner, online, let in), not be blocked
// by the owner, and not be asking too fast. The owner, online, is told, and
// so is everyone else in the house (the box's stock, a cabinet changed).
//
// Client to server:
//
//	{"t":"sdb_buy","owner":7,"uid":4,"op":"sdb-<16 hex>","count":3,"price":12}
//	{"t":"cabinet_move","owner":7,"uid":5,"op":"cab-<16 hex>","kind":"take|put","slot":2,"class":"apple","n":3}
//
// Server to client:
//
//	{"t":"sdb_bought","owner":7,"op":"sdb-…","uid":4,"status":"settled|refused","why":"…","text":"…","class":"apple","n":3,"price":12,"total":36,"stock":7}
//	{"t":"sdb_sold","op":"sdb-…","uid":4,"buyer":{"id":9,"name":"Bo"},"class":"apple","n":3,"total":36}             to the owner
//	{"t":"box_changed","owner":7,"uid":4,"stock":7}                                                                 to everyone else in the house
//	{"t":"cabinet_moved","owner":7,"op":"cab-…","uid":5,"status":"settled|refused","why":"…","text":"…","kind":"take","class":"apple","slot":2,"n":3,"contents":[…]}
//	{"t":"cabinet_changed","owner":7,"op":"cab-…","uid":5,"by":{"id":9,"name":"Bo"},"kind":"take","class":"apple","slot":2,"n":3}   to the owner and everyone else in the house
//
// why: away (not in the house), own, bad, blocked, busy (that op is being
// weighed), off (no ledger), or the site's (gone, price, short, zero, key,
// cant, tool, full, for_sale).

import (
	"encoding/json"
	"flag"
	"log"
	"regexp"
	"strconv"
	"testing"
	"time"
)

const (
	ledgerRouteSDB     = "/groddle/v1/ledger/sdb"
	ledgerRouteCabinet = "/groddle/v1/ledger/cabinet"
	sdbPriceMax        = 9999999 // bag_furniture_sdb.js set_selling_price input_max_chars 7
	sdbBuyMax          = 999     // StoreBuyUI.as updateMax
	cabinetMoveMax     = 9999
	sdbBuyBurst        = 4
	sdbBuyEvery        = 3 * time.Second
	cabinetMoveBurst   = 8
	cabinetMoveEvery   = time.Second

	sdbTextAway = "You need to be in their house to buy that."      // ours
	sdbTextOwn  = "That's yours already."                           // ours: the source shows the owner no purchase verb
	sdbTextBad  = "Oops, something went wrong with that store."     // bag_furniture_sdb.js:1167
	sdbTextZero = "You can't buy a negative/zero amount of things!" // :1155
	sdbTextGone = "Oops, that store is no longer selling."          // :1144
	boxTextBusy = "Hold on, that is still going through."           // ours
	boxTextOff  = "Nobody can buy anything here right now."         // ours: no ledger to carry it
	cabTextAway = "You need to be in their house to do that."       // ours
	cabTextOwn  = "Your own cabinet is yours to open."              // ours: the owner's moves are their save's
	cabTextBad  = "That did not work. Try again in a moment."       // ours
)

var (
	sdbOpRe  = regexp.MustCompile(`^sdb-[0-9a-f]{16}$`)
	cabOpRe  = regexp.MustCompile(`^cab-[0-9a-f]{16}$`)
	boxClsRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// boxAnswer is the site's word on a purchase or a move. Status "" means no answer came.
type boxAnswer struct {
	Status   string          `json:"status"` // settled | refused
	Why      string          `json:"why,omitempty"`
	Text     string          `json:"text,omitempty"`
	Class    string          `json:"class,omitempty"`
	UID      int64           `json:"uid,omitempty"`
	N        int64           `json:"n,omitempty"`
	Price    int64           `json:"price,omitempty"`
	Total    int64           `json:"total,omitempty"`
	Stock    int64           `json:"stock"`
	Kind     string          `json:"kind,omitempty"`
	Slot     *int64          `json:"slot,omitempty"`
	Contents json.RawMessage `json:"contents,omitempty"`
}

type sdbCall struct {
	Op    string `json:"op"`
	Buyer int64  `json:"buyer"`
	Owner int64  `json:"owner"`
	UID   int64  `json:"uid"`
	Count int64  `json:"count"`
	Price int64  `json:"price"`
}

type cabinetCall struct {
	Op    string `json:"op"`
	Actor int64  `json:"actor"`
	Owner int64  `json:"owner"`
	UID   int64  `json:"uid"`
	Kind  string `json:"kind"`
	Slot  *int64 `json:"slot"`
	Class string `json:"class"`
	N     int64  `json:"n"`
}

// boxSite is the site's ledger (httpLedger). Calls block; the module makes them off the hub lock.
type boxSite interface {
	sdbBuy(c sdbCall) boxAnswer
	cabinetMove(c cabinetCall) boxAnswer
}

func (l *httpLedger) boxAsk(route string, body []byte) boxAnswer {
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * 400 * time.Millisecond)
		}
		var a boxAnswer
		err := l.dropPost(route, body, &a)
		if err == nil && (a.Status == "settled" || a.Status == "refused") {
			return a
		}
		if err == nil {
			log.Printf("boxes: %s: status %q", route, a.Status)
		} else {
			log.Printf("boxes: %s: %v", route, err)
		}
	}
	return boxAnswer{}
}

func (l *httpLedger) sdbBuy(c sdbCall) boxAnswer {
	body, _ := json.Marshal(c)
	return l.boxAsk(ledgerRouteSDB, body)
}

func (l *httpLedger) cabinetMove(c cabinetCall) boxAnswer {
	body, _ := json.Marshal(c)
	return l.boxAsk(ledgerRouteCabinet, body)
}

func init() { modules = append(modules, newHomeBoxes) }

type homeBoxes struct {
	h     *Hub
	site  boxSite // nil: nothing crosses (no -ledger-url, or -trade-ledger off)
	spawn func(func())
	busy  map[string]int64 // op -> the player the site is weighing it for
	rates map[string]*bucket
}

func newHomeBoxes(h *Hub) Module {
	m := &homeBoxes{h: h, spawn: func(fn func()) { go fn() }, busy: map[string]int64{}, rates: map[string]*bucket{}}
	if !testing.Testing() && flag.Parsed() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.site = l
			}
		}
	}
	return m
}

func (m *homeBoxes) Register(r *Registry) {
	r.Handle("sdb_buy", m.buy)
	r.Handle("cabinet_move", m.move)
	r.OnDisconnect(m.disconnected)
}

func (m *homeBoxes) allow(kind string, cl *Client, burst float64, every time.Duration) bool {
	key := kind + ":" + strconv.FormatInt(cl.ID(), 10)
	b := m.rates[key]
	if b == nil {
		b = &bucket{}
		m.rates[key] = b
	}
	if !b.take(m.h.Now(), burst, every) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return false
	}
	return true
}

func (m *homeBoxes) blocked(owner, visitor int64) bool {
	fn, _ := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return fn != nil && fn(owner, visitor)
}

func houseOf(owner int64) string { return houseStreetPrefix + strconv.FormatInt(owner, 10) }

// buy is purchase and sellItem for a visitor in the owner's house.
func (m *homeBoxes) buy(cl *Client, frame json.RawMessage) {
	if !m.allow("sdb", cl, sdbBuyBurst, sdbBuyEvery) {
		return
	}
	var msg struct {
		Owner int64  `json:"owner"`
		UID   int64  `json:"uid"`
		Op    string `json:"op"`
		Count int64  `json:"count"`
		Price int64  `json:"price"`
	}
	_ = json.Unmarshal(frame, &msg)
	owner, buyer, op := msg.Owner, cl.ID(), msg.Op
	refuse := func(why, text string) {
		m.h.SendLocked(cl, map[string]any{"t": "sdb_bought", "owner": owner, "op": op, "uid": msg.UID, "status": "refused", "why": why, "text": text})
	}
	if owner <= 0 || msg.UID <= 0 || !sdbOpRe.MatchString(op) || msg.Price < 0 || msg.Price > sdbPriceMax {
		refuse("bad", sdbTextBad)
		return
	}
	if msg.Count <= 0 {
		refuse("zero", sdbTextZero)
		return
	}
	if owner == buyer {
		refuse("own", sdbTextOwn)
		return
	}
	if cl.Street() != houseOf(owner) {
		refuse("away", sdbTextAway)
		return
	}
	if m.blocked(owner, buyer) {
		refuse("blocked", homeTextBlocked)
		return
	}
	if m.busy[op] != 0 {
		refuse("busy", boxTextBusy)
		return
	}
	if m.site == nil {
		refuse("off", boxTextOff)
		return
	}
	count := msg.Count
	if count > sdbBuyMax {
		count = sdbBuyMax
	}
	m.busy[op] = buyer
	site, name := m.site, cl.Name()
	call := sdbCall{Op: op, Buyer: buyer, Owner: owner, UID: msg.UID, Count: count, Price: msg.Price}
	m.spawn(func() {
		a := site.sdbBuy(call)
		m.h.After(0, func() { m.bought(call, name, a) })
	})
}

func (m *homeBoxes) bought(c sdbCall, name string, a boxAnswer) {
	if m.busy[c.Op] == c.Buyer {
		delete(m.busy, c.Op)
	}
	if a.Status == "" {
		// No answer: the op settles once if a late answer did land, so asking again is safe.
		a = boxAnswer{Status: "refused", Why: "off", Text: boxTextOff}
	}
	if a.Status == "refused" && a.Text == "" {
		a.Text = sdbTextGone
	}
	if b := m.h.ClientLocked(c.Buyer); b != nil {
		out := map[string]any{"t": "sdb_bought", "owner": c.Owner, "op": c.Op, "uid": c.UID, "status": a.Status, "why": a.Why, "text": a.Text}
		if a.Status == "settled" {
			out["class"], out["n"], out["price"], out["total"], out["stock"] = a.Class, a.N, a.Price, a.Total, a.Stock
		}
		m.h.SendLocked(b, out)
	}
	if a.Status != "settled" {
		return
	}
	log.Printf("boxes: %d bought %d %s from %d's box %d for %d (%s)", c.Buyer, a.N, a.Class, c.Owner, c.UID, a.Total, c.Op)
	if o := m.h.ClientLocked(c.Owner); o != nil {
		m.h.SendLocked(o, map[string]any{"t": "sdb_sold", "op": c.Op, "uid": c.UID, "buyer": map[string]any{"id": c.Buyer, "name": name}, "class": a.Class, "n": a.N, "total": a.Total})
	}
	changed := map[string]any{"t": "box_changed", "owner": c.Owner, "uid": c.UID, "stock": a.Stock}
	for _, other := range m.h.StreetLocked(houseOf(c.Owner)) {
		if other.ID() != c.Buyer && other.ID() != c.Owner {
			m.h.SendLocked(other, changed)
		}
	}
}

// move is a key-holder's inventory move to or from a cabinet slot (CabinetDialog.as).
func (m *homeBoxes) move(cl *Client, frame json.RawMessage) {
	if !m.allow("cabinet", cl, cabinetMoveBurst, cabinetMoveEvery) {
		return
	}
	var msg struct {
		Owner int64  `json:"owner"`
		UID   int64  `json:"uid"`
		Op    string `json:"op"`
		Kind  string `json:"kind"`
		Slot  *int64 `json:"slot"`
		Class string `json:"class"`
		N     int64  `json:"n"`
	}
	_ = json.Unmarshal(frame, &msg)
	owner, actor, op := msg.Owner, cl.ID(), msg.Op
	refuse := func(why, text string) {
		m.h.SendLocked(cl, map[string]any{"t": "cabinet_moved", "owner": owner, "op": op, "uid": msg.UID, "status": "refused", "why": why, "text": text})
	}
	if owner <= 0 || msg.UID <= 0 || !cabOpRe.MatchString(op) || (msg.Kind != "take" && msg.Kind != "put") || !boxClsRe.MatchString(msg.Class) || msg.N <= 0 || msg.N > cabinetMoveMax || (msg.Slot != nil && (*msg.Slot < 0 || *msg.Slot > 255)) {
		refuse("bad", cabTextBad)
		return
	}
	if owner == actor {
		refuse("own", cabTextOwn)
		return
	}
	if cl.Street() != houseOf(owner) {
		refuse("away", cabTextAway)
		return
	}
	if m.blocked(owner, actor) {
		refuse("blocked", homeTextBlocked)
		return
	}
	if m.busy[op] != 0 {
		refuse("busy", boxTextBusy)
		return
	}
	if m.site == nil {
		refuse("off", boxTextOff)
		return
	}
	m.busy[op] = actor
	site, name := m.site, cl.Name()
	call := cabinetCall{Op: op, Actor: actor, Owner: owner, UID: msg.UID, Kind: msg.Kind, Slot: msg.Slot, Class: msg.Class, N: msg.N}
	m.spawn(func() {
		a := site.cabinetMove(call)
		m.h.After(0, func() { m.moved(call, name, a) })
	})
}

func (m *homeBoxes) moved(c cabinetCall, name string, a boxAnswer) {
	if m.busy[c.Op] == c.Actor {
		delete(m.busy, c.Op)
	}
	if a.Status == "" {
		a = boxAnswer{Status: "refused", Why: "off", Text: boxTextOff}
	}
	if a.Status == "refused" && a.Text == "" {
		a.Text = cabTextBad
	}
	if b := m.h.ClientLocked(c.Actor); b != nil {
		out := map[string]any{"t": "cabinet_moved", "owner": c.Owner, "op": c.Op, "uid": c.UID, "status": a.Status, "why": a.Why, "text": a.Text}
		if a.Status == "settled" {
			out["kind"], out["class"], out["n"] = a.Kind, a.Class, a.N
			if a.Slot != nil {
				out["slot"] = *a.Slot
			}
			if len(a.Contents) > 0 {
				out["contents"] = a.Contents
			}
		}
		m.h.SendLocked(b, out)
	}
	if a.Status != "settled" {
		return
	}
	log.Printf("boxes: %d %s %d %s, %d's cabinet %d (%s)", c.Actor, a.Kind, a.N, a.Class, c.Owner, c.UID, c.Op)
	changed := map[string]any{"t": "cabinet_changed", "owner": c.Owner, "op": c.Op, "uid": c.UID, "by": map[string]any{"id": c.Actor, "name": name}, "kind": a.Kind, "class": a.Class, "n": a.N}
	if a.Slot != nil {
		changed["slot"] = *a.Slot
	}
	told := map[int64]bool{c.Actor: true}
	if o := m.h.ClientLocked(c.Owner); o != nil {
		m.h.SendLocked(o, changed)
		told[c.Owner] = true
	}
	for _, other := range m.h.StreetLocked(houseOf(c.Owner)) {
		if !told[other.ID()] {
			m.h.SendLocked(other, changed)
		}
	}
}

func (m *homeBoxes) disconnected(cl *Client) {
	for op, who := range m.busy {
		if who == cl.ID() {
			// The site still answers; the answer finds nobody to tell. Free the op now.
			delete(m.busy, op)
		}
	}
}
