package main

// Buying furniture in a real player's house, and telling visitors what
// changed on a home street (register G332 and G328, lane HOMES3; homes.go
// is the visiting itself, homes_help.go the Butler and tending).
//
//   - Furniture for sale (GameServerJS furniture_base, e.g.
//     items/furniture_bed.js buy 344-375 and sellItem 511-645): a visitor
//     standing in the owner's house (HOUSE_<owner>, which homes.go lets in
//     only a key-holder or someone the owner let in) buys a piece the owner
//     put up for sale. The price is the site's (groddle-afternoon-site
//     src/LedgerFurniture.php, the owner set it there): the buyer's client
//     names the price it was shown, and the site refuses a sale whose price
//     changed ("Oops, the price appears to have changed underneath you.
//     Please try your purchase again.", 576), whose piece is gone, or that
//     the buyer can't pay. The site moves the currants and the piece in the
//     ledger in one signed call (ledger/furniture); the currants wait in the
//     owner's Pile of Currants until they collect it. The owner, online, is
//     told now ("<buyer> just bought your <label> for N currants.", 606),
//     and everyone else in the house sees the piece go.
//   - Refreshes: the owner's game says when its signpost, Note Poles or
//     price tags changed (after its save is written); everyone on the
//     owner's home street or in their house reads them again from the site
//     (groddle/v1/homes/<id>/street). Nothing of the owner's words passes
//     through here.
//
// Client to server:
//
//	{"t":"furn_buy","owner":7,"op":"furn-<16 hex>","price":1200}
//	{"t":"home_refresh","what":"poles|tags|signpost"}            the sender's own home
//
// Server to client:
//
//	{"t":"furn_bought","owner":7,"op":"furn-…","status":"settled|refused","why":"gone|price|short|blocked|own|busy|away|off","text":"…","class":"furniture_bed","uid":4,"price":1200}
//	{"t":"furn_sold","op":"furn-…","buyer":{"id":9,"name":"Bo"},"class":"furniture_bed","uid":4,"price":1200,"x":610}   to the owner
//	{"t":"furn_gone","owner":7,"op":"furn-…","uid":4}                                                                    to everyone else in the house
//	{"t":"home_refresh","owner":7,"what":"poles"}                                                                       to everyone on the owner's street and in their house
//
// Ours: one buyer at a time for a piece ("Someone is buying that right
// now."); a purchase the site has not answered is refused here (the site's
// op is the listing's id and settles once, so asking again later is safe).

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"
)

const (
	ledgerRouteFurniture = "/groddle/v1/ledger/furniture"
	furnPriceMax         = 999999 // furniture_bed.js:154 input_max_chars 6
	furnBuyBurst         = 4
	furnBuyEvery         = 3 * time.Second
	homeRefreshBurst     = 6
	homeRefreshEvery     = 2 * time.Second

	furnTextAway = "You need to be in their house to buy that."    // ours
	furnTextBusy = "Someone is buying that right now."              // ours
	furnTextOff  = "Nobody can buy anything here right now."        // ours: no ledger to carry it
	furnTextGone = "That store is no longer selling."               // furniture_bed.js:514
	furnTextOwn  = "That's yours already."                          // ours: the source shows the owner no buy verb
	furnTextBad  = "Oops, something is wrong with that store."      // furniture_bed.js:532
)

var furnOpRe = regexp.MustCompile(`^furn-[0-9a-f]{16}$`)

// furnAnswer is the site's word on a purchase. Status "" means no answer came.
type furnAnswer struct {
	Status string `json:"status"` // settled | refused
	Why    string `json:"why,omitempty"`
	Text   string `json:"text,omitempty"`
	Class  string `json:"class,omitempty"`
	UID    int64  `json:"uid,omitempty"`
	Price  int64  `json:"price,omitempty"`
	X      int64  `json:"x,omitempty"`
}

// furnSite is the site's furniture sales (httpLedger). Calls block; the module makes them off the hub lock.
type furnSite interface {
	furniture(op string, buyer, owner, price int64) furnAnswer
}

func (l *httpLedger) furniture(op string, buyer, owner, price int64) furnAnswer {
	body, _ := json.Marshal(map[string]any{"op": op, "buyer": buyer, "owner": owner, "price": price})
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * 400 * time.Millisecond)
		}
		a, err := l.furnitureOnce(body)
		if err == nil {
			return a
		}
		log.Printf("furniture: %s: %v", ledgerRouteFurniture, err)
	}
	return furnAnswer{}
}

func (l *httpLedger) furnitureOnce(body []byte) (furnAnswer, error) {
	var nb [8]byte
	rand.Read(nb[:])
	req, err := http.NewRequest(http.MethodPost, l.base+"/wp-json"+ledgerRouteFurniture, bytes.NewReader(body))
	if err != nil {
		return furnAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Groddle-Service", signService(l.secret, http.MethodPost, ledgerRouteFurniture, l.now().Unix(), hex.EncodeToString(nb[:]), body))
	res, err := l.client.Do(req)
	if err != nil {
		return furnAnswer{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	var a furnAnswer
	if (res.StatusCode == http.StatusOK || res.StatusCode == http.StatusConflict) && json.Unmarshal(raw, &a) == nil && (a.Status == "settled" || a.Status == "refused") {
		return a, nil
	}
	return furnAnswer{}, fmt.Errorf("HTTP %d: %.200s", res.StatusCode, raw)
}

func init() { modules = append(modules, newHomeStreet) }

type homeStreet struct {
	h     *Hub
	site  furnSite // nil: nothing is for sale (no -ledger-url, or -trade-ledger off)
	spawn func(func())
	busy  map[string]int64 // listing op -> the buyer the site is weighing
	rates map[string]*bucket
}

func newHomeStreet(h *Hub) Module {
	m := &homeStreet{h: h, spawn: func(fn func()) { go fn() }, busy: map[string]int64{}, rates: map[string]*bucket{}}
	if !testing.Testing() && flag.Parsed() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.site = l
			}
		}
	}
	return m
}

func (m *homeStreet) Register(r *Registry) {
	r.Handle("furn_buy", m.buy)
	r.Handle("home_refresh", m.refresh)
	r.OnDisconnect(m.disconnected)
}

func (m *homeStreet) allow(kind string, cl *Client, burst float64, every time.Duration) bool {
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

func (m *homeStreet) blocked(owner, visitor int64) bool {
	fn, _ := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return fn != nil && fn(owner, visitor)
}

func (m *homeStreet) refuse(cl *Client, owner int64, op, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "furn_bought", "owner": owner, "op": op, "status": "refused", "why": why, "text": text})
}

// buy is the buy verb and sellItem for a visitor in the owner's house.
func (m *homeStreet) buy(cl *Client, frame json.RawMessage) {
	if !m.allow("furn", cl, furnBuyBurst, furnBuyEvery) {
		return
	}
	var msg struct {
		Owner int64  `json:"owner"`
		Op    string `json:"op"`
		Price int64  `json:"price"`
	}
	_ = json.Unmarshal(frame, &msg)
	owner, buyer, op := msg.Owner, cl.ID(), msg.Op
	if owner <= 0 || !furnOpRe.MatchString(op) || msg.Price <= 0 || msg.Price > furnPriceMax {
		m.refuse(cl, owner, op, "bad", furnTextBad)
		return
	}
	if owner == buyer {
		m.refuse(cl, owner, op, "own", furnTextOwn)
		return
	}
	house := houseStreetPrefix + strconv.FormatInt(owner, 10)
	if cl.Street() != house {
		m.refuse(cl, owner, op, "away", furnTextAway)
		return
	}
	if m.blocked(owner, buyer) {
		m.refuse(cl, owner, op, "blocked", homeTextBlocked)
		return
	}
	if who := m.busy[op]; who != 0 {
		m.refuse(cl, owner, op, "busy", furnTextBusy)
		return
	}
	if m.site == nil {
		m.refuse(cl, owner, op, "off", furnTextOff)
		return
	}
	m.busy[op] = buyer
	site, price, name := m.site, msg.Price, cl.Name()
	m.spawn(func() {
		a := site.furniture(op, buyer, owner, price)
		m.h.After(0, func() { m.answered(op, buyer, name, owner, a) })
	})
}

func (m *homeStreet) answered(op string, buyer int64, name string, owner int64, a furnAnswer) {
	if m.busy[op] == buyer {
		delete(m.busy, op)
	}
	if a.Status == "" {
		// No answer: nobody here asks again; the listing settles once if a late answer did land.
		a = furnAnswer{Status: "refused", Why: "off", Text: furnTextOff}
	}
	if a.Status == "refused" && a.Text == "" {
		a.Text = furnTextGone
	}
	if c := m.h.ClientLocked(buyer); c != nil {
		out := map[string]any{"t": "furn_bought", "owner": owner, "op": op, "status": a.Status, "why": a.Why, "text": a.Text}
		if a.Status == "settled" {
			out["class"], out["uid"], out["price"] = a.Class, a.UID, a.Price
		}
		m.h.SendLocked(c, out)
	}
	if a.Status != "settled" {
		return
	}
	log.Printf("furniture: %d bought %s (%s, uid %d) from %d for %d", buyer, op, a.Class, a.UID, owner, a.Price)
	if o := m.h.ClientLocked(owner); o != nil {
		m.h.SendLocked(o, map[string]any{"t": "furn_sold", "op": op, "buyer": map[string]any{"id": buyer, "name": name}, "class": a.Class, "uid": a.UID, "price": a.Price, "x": a.X})
	}
	gone := map[string]any{"t": "furn_gone", "owner": owner, "op": op, "uid": a.UID}
	for _, c := range m.h.StreetLocked(houseStreetPrefix + strconv.FormatInt(owner, 10)) {
		if c.ID() != buyer && c.ID() != owner {
			m.h.SendLocked(c, gone)
		}
	}
}

// refresh: the owner's signpost, poles or tags changed; everyone at their home reads them again.
func (m *homeStreet) refresh(cl *Client, frame json.RawMessage) {
	if !m.allow("refresh", cl, homeRefreshBurst, homeRefreshEvery) {
		return
	}
	var msg struct {
		What string `json:"what"`
	}
	_ = json.Unmarshal(frame, &msg)
	switch msg.What {
	case "poles", "tags", "signpost":
	default:
		return
	}
	id := strconv.FormatInt(cl.ID(), 10)
	out := map[string]any{"t": "home_refresh", "owner": cl.ID(), "what": msg.What}
	for _, street := range []string{homeStreetPrefix + id, houseStreetPrefix + id} {
		for _, c := range m.h.StreetLocked(street) {
			if c.ID() != cl.ID() {
				m.h.SendLocked(c, out)
			}
		}
	}
}

func (m *homeStreet) disconnected(cl *Client) {
	for op, who := range m.busy {
		if who == cl.ID() {
			// The site still answers; the answer finds nobody to tell. Free the piece now.
			delete(m.busy, op)
		}
	}
}
