package main

// Giving things to another player (register G332), the ways the original had
// besides the trade window:
//
//   - The pack's Give verb ("Or, drag item to player"): every takeable item
//     (GameServerJS items/include/takeable.js takeable_give_conditions
//     220-246, takeable_give 248-292) goes to a player in the same location
//     (pc.location.activePlayers, 250), never yourself, never someone offline
//     (main.js doVerb 3158-3206), never between a blocked pair (takeable.js
//     258-264); give has no reach limit (main.js:3203, "bugs.tinyspeck.com/9235").
//     The giver hears "You gave <name> <stack>." and the receiver
//     "<name> gave you <stack>." (272-273).
//   - A written Note is a takeable item like any other; its title and text
//     travel with it (items/note.js: title 150, body 1000 characters, 246-247).
//   - A wrapped Gift Box (items/bag_gift_box.js wrap 254-297, at most three
//     stacks, capacity 3 in onCreate 684-690, no bags, elements or furniture,
//     canContain 666-672 and bag_generic's parent_canContain 719-721) is given
//     whole, contents unseen; the receiver unwraps it
//     (bag_gift_box_wrapped.js unwrap 419-484).
//
// Nothing moves on the client's word. A give is asked of the site's ledger
// (groddle-afternoon-site src/LedgerGift.php, POST groddle/v1/ledger/gift,
// signed as ledger.go signs): the goods leave the giver's ledger and reach
// the receiver's in one transaction, recorded once per op (a settled trade,
// a: the giver, b: the receiver, b gave nothing), refused with the source's
// words when the giver's ledger lacks them. Only then do both players hear
// it; each game applies the op once through the trade slice's applied list,
// and a game that missed the answer applies it on its next load or connect
// (the site lists a player's gifts not yet applied).
//
// Client to server:
//
//	{"t":"gift_give","to":7,"ref":"…","item":{"c":"apple","n":3,"r":40}}                     a stack (r: a tool's points left)
//	{"t":"gift_give","to":7,"ref":"…","item":{"c":"note","n":1},"note":{"title":"…","body":"…"}}  a written note
//	{"t":"gift_give","to":7,"ref":"<box id>","box":[{"c":"apple","n":3},…]}                  a wrapped Gift Box
//
// Server to client:
//
//	{"t":"gift_given","op":"gift-…","ref":"…","to":{"id":7,"name":"Ada"},"status":"settled"|"refused","why":"…","text":"…","item":{…}|null,"box":[…]|null,"note":bool,"level":4}
//	{"t":"gift_got","op":"gift-…","from":{"id":9,"name":"Bo"},"item":{…}|null,"box":[…]|null,"note":{"title":"…","body":"…"}|null}
//
// level is the receiver's level as the site has it saved (for the giver's
// quests, takeable.js 283-289: give_player_level1); 0 when it is not known.
//
// Ours: one give at a time per giver (the site answers in milliseconds); a
// class the ledger does not carry cannot be given (the source let no_trade
// items be given, but nothing here could vouch for them); the receiver's
// pack is not asked whether it has room (the source said "Their bag is
// full!", 268-270): the receiver's game takes what the ledger gave it.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ledgerRouteGift = "/groddle/v1/ledger/gift"
	giftBoxSlots    = 3    // bag_gift_box.js onCreate 687: capacity 3
	giftNoteTitle   = 150  // note.js:246
	giftNoteBody    = 1000 // note.js:247
	giftBurst       = 6
	giftEvery       = time.Second

	giftTextSelf      = "You can't do that to yourself."                                           // main.js:3166
	giftTextOffline   = "That player has gone offline."                                            // main.js:3182
	giftTextNotThere  = "You can't do that to a player that isn't there!"                          // main.js:3177
	giftTextBlockedBy = "You are blocked by that player and cannot give them anything."            // takeable.js:259
	giftTextBlocking  = "You are blocking that player and cannot give them anything."              // takeable.js:263
	giftTextSoulbound = "This item is locked to you and can't be given to another player."         // takeable.js:222
	giftTextCant      = "That can't be given to another player."                                   // ours: the ledger does not carry it
	giftTextTooMany   = "You don't have enough of that item."                                      // inc_trading.js:456
	giftTextBoxBad    = "A Gift Box holds up to three stacks, and no bags, elements or furniture." // ours (bag_gift_box.js canContain 666-672)
	giftTextBusy      = "Wait a moment, you are still giving something."                           // ours
	giftTextOff       = "You can't give things right now."                                         // ours: no ledger to move them
	giftTextBad       = "Give a stack, a note or a wrapped Gift Box."                              // ours
)

var (
	giftRefRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	// bag_generic parent_canContain 719-721: no bags, elements, furniture or no_bag (npc_crafty_bot) in a box.
	giftNoBoxRe = regexp.MustCompile(`^(bag_|furniture_|element_)|^npc_crafty_bot$`)
)

func init() { modules = append(modules, newGifts) }

// giftAnswer is the site's word on a give. Status "" means no answer came.
type giftAnswer struct {
	Status string `json:"status"` // settled | refused
	Why    string `json:"why,omitempty"`
	Text   string `json:"text,omitempty"`
	Level  int    `json:"level,omitempty"`
}

// giftCall is one give as the site is asked it.
type giftCall struct {
	Op   string     `json:"op"`
	From int64      `json:"from"`
	To   int64      `json:"to"`
	Gave tradeGoods `json:"gave"`
	Box  bool       `json:"box"`
	Ref  string     `json:"ref,omitempty"`
	Note *giftNote  `json:"note,omitempty"`
}

type giftNote struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// giftSite is the site's ledger (httpLedger). Calls block; the module makes them off the hub lock.
type giftSite interface {
	gift(c giftCall) giftAnswer
}

func (l *httpLedger) gift(c giftCall) giftAnswer {
	body, _ := json.Marshal(map[string]any{"op": c.Op, "from": c.From, "to": c.To, "gave": goodsJSON(c.Gave), "box": c.Box, "ref": c.Ref, "note": c.Note})
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * 400 * time.Millisecond)
		}
		a, err := l.giftOnce(body)
		if err == nil {
			return a
		}
		log.Printf("gifts: %s: %v", ledgerRouteGift, err)
	}
	return giftAnswer{}
}

func (l *httpLedger) giftOnce(body []byte) (giftAnswer, error) {
	var nb [8]byte
	rand.Read(nb[:])
	req, err := http.NewRequest(http.MethodPost, l.base+"/wp-json"+ledgerRouteGift, bytes.NewReader(body))
	if err != nil {
		return giftAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Groddle-Service", signService(l.secret, http.MethodPost, ledgerRouteGift, l.now().Unix(), hex.EncodeToString(nb[:]), body))
	res, err := l.client.Do(req)
	if err != nil {
		return giftAnswer{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	var a giftAnswer
	if (res.StatusCode == http.StatusOK || res.StatusCode == http.StatusConflict) && json.Unmarshal(raw, &a) == nil && (a.Status == "settled" || a.Status == "refused") {
		return a, nil
	}
	return giftAnswer{}, fmt.Errorf("HTTP %d: %.200s", res.StatusCode, raw)
}

type gifts struct {
	h     *Hub
	table tradeTable
	site  giftSite // nil: nothing can be given (no -ledger-url, or -trade-ledger off)
	spawn func(func())
	newID func() string
	busy  map[int64]string // giver -> the op the site is weighing
	rates map[int64]*bucket
}

func newGifts(h *Hub) Module {
	var table tradeTable
	if err := json.Unmarshal(tradeJSON, &table); err != nil || len(table.Items) == 0 {
		panic(fmt.Sprintf("realtime: data/trade.json: %v", err))
	}
	m := &gifts{h: h, table: table, spawn: func(fn func()) { go fn() }, busy: map[int64]string{}, rates: map[int64]*bucket{},
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return hex.EncodeToString(b)
		}}
	if !testing.Testing() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.site = l
			}
		}
	}
	return m
}

func (m *gifts) Register(r *Registry) {
	r.Handle("gift_give", m.give)
	r.OnDisconnect(func(cl *Client) { delete(m.busy, cl.ID()); delete(m.rates, cl.ID()) })
}

// cleanNoteText is a note's title or body: valid text with no control characters but
// line breaks (a body keeps them; a title is one line), no tags, at most n characters.
func cleanNoteText(s string, n int, lines bool) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' && lines:
			b.WriteRune('\n')
		case r == '\r' || r == '\t' || unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsControl(r) || r == '<' || r == '>':
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > n {
		out = strings.TrimSpace(string([]rune(out)[:n]))
	}
	return out
}

// stack checks one stack against the trade table (the classes the ledger carries).
func (m *gifts) stack(it *tradeItem) string {
	cls, ok := m.table.Items[it.C]
	if !ok {
		if m.table.Untradeable[it.C] == "soulbound" {
			return giftTextSoulbound
		}
		return giftTextCant
	}
	if it.N < 1 || it.N > cls.Max || it.N > tradeSlotMax {
		return giftTextTooMany
	}
	if it.R != nil {
		if *it.R < 0 || *it.R > tradeMaxPoints {
			return giftTextCant
		}
		if cls.Pts > 0 && *it.R > cls.Pts {
			r := cls.Pts
			it.R = &r
		}
	}
	return ""
}

func (m *gifts) refuse(cl *Client, ref string, to int64, toName, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "gift_given", "ref": ref, "to": map[string]any{"id": to, "name": toName}, "status": "refused", "why": why, "text": text})
}

// give is takeable_give: one stack, a note or a wrapped Gift Box to a player on your street.
func (m *gifts) give(cl *Client, frame json.RawMessage) {
	b := m.rates[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.rates[cl.ID()] = b
	}
	if !b.take(m.h.Now(), giftBurst, giftEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	var msg struct {
		To   int64       `json:"to"`
		Ref  string      `json:"ref"`
		Item *tradeItem  `json:"item"`
		Box  []tradeItem `json:"box"`
		Note *giftNote   `json:"note"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		m.refuse(cl, "", 0, "", "bad", giftTextBad)
		return
	}
	ref := ""
	if giftRefRe.MatchString(msg.Ref) {
		ref = msg.Ref
	}
	to, from := msg.To, cl.ID()
	target := m.h.ClientLocked(to)
	toName := ""
	if target != nil {
		toName = target.Name()
	}
	switch {
	case to <= 0:
		m.refuse(cl, ref, to, toName, "bad", giftTextNotThere)
		return
	case to == from:
		m.refuse(cl, ref, to, toName, "self", giftTextSelf)
		return
	case target == nil:
		m.refuse(cl, ref, to, toName, "offline", giftTextOffline)
		return
	case cl.Street() == "" || target.Street() != cl.Street():
		m.refuse(cl, ref, to, toName, "away", giftTextNotThere)
		return
	}
	if ign, ok := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool); ok {
		if ign(to, from) {
			m.refuse(cl, ref, to, toName, "blocked", giftTextBlockedBy)
			return
		}
		if ign(from, to) {
			m.refuse(cl, ref, to, toName, "blocking", giftTextBlocking)
			return
		}
	}
	call := giftCall{From: from, To: to, Ref: ref}
	var itemOut, boxOut any
	switch {
	case msg.Item != nil && msg.Box == nil:
		it := *msg.Item
		if why := m.stack(&it); why != "" {
			m.refuse(cl, ref, to, toName, "item", why)
			return
		}
		if it.C == "note" {
			if msg.Note != nil {
				n := giftNote{Title: cleanNoteText(msg.Note.Title, giftNoteTitle, false), Body: cleanNoteText(msg.Note.Body, giftNoteBody, true)}
				if n.Title != "" || n.Body != "" {
					call.Note = &n
				}
			}
			it.N, it.R = 1, nil
		} else if msg.Note != nil {
			m.refuse(cl, ref, to, toName, "bad", giftTextBad)
			return
		}
		call.Gave = tradeGoods{Items: []tradeItem{it}}
		itemOut = it
	case msg.Box != nil && msg.Item == nil && msg.Note == nil:
		if len(msg.Box) < 1 || len(msg.Box) > giftBoxSlots {
			m.refuse(cl, ref, to, toName, "box", giftTextBoxBad)
			return
		}
		items := []tradeItem{{C: "bag_gift_box", N: 1}}
		seen := map[string]bool{}
		for _, raw := range msg.Box {
			it := raw
			if giftNoBoxRe.MatchString(it.C) || seen[it.C] {
				m.refuse(cl, ref, to, toName, "box", giftTextBoxBad)
				return
			}
			seen[it.C] = true
			if why := m.stack(&it); why != "" {
				m.refuse(cl, ref, to, toName, "item", why)
				return
			}
			items = append(items, it)
		}
		call.Gave, call.Box = tradeGoods{Items: items}, true
		boxOut = items[1:]
	default:
		m.refuse(cl, ref, to, toName, "bad", giftTextBad)
		return
	}
	if _, waiting := m.busy[from]; waiting {
		m.refuse(cl, ref, to, toName, "busy", giftTextBusy)
		return
	}
	if m.site == nil {
		m.refuse(cl, ref, to, toName, "off", giftTextOff)
		return
	}
	call.Op = "gift-" + m.newID()
	m.busy[from] = call.Op
	site := m.site
	fromName := cl.Name()
	m.spawn(func() {
		a := site.gift(call)
		m.h.After(0, func() {
			if m.busy[from] == call.Op {
				delete(m.busy, from)
			}
			if a.Status == "" {
				// No answer: the op is idempotent at the site; a settled one reaches both games on their next connect.
				a.Status, a.Why, a.Text = "refused", "off", giftTextOff
			}
			out := map[string]any{"t": "gift_given", "op": call.Op, "ref": ref, "to": map[string]any{"id": to, "name": toName}, "status": a.Status, "why": a.Why, "text": a.Text, "item": itemOut, "box": boxOut, "note": call.Note != nil}
			if a.Status == "settled" {
				out["level"] = a.Level
			}
			if c := m.h.ClientLocked(from); c != nil {
				m.h.SendLocked(c, out)
			}
			if a.Status != "settled" {
				return
			}
			log.Printf("gifts: %d gave %d %s", from, to, call.Op)
			if o := m.h.ClientLocked(to); o != nil {
				var note any
				if call.Note != nil {
					note = call.Note
				}
				m.h.SendLocked(o, map[string]any{"t": "gift_got", "op": call.Op, "from": map[string]any{"id": from, "name": fromName}, "item": itemOut, "box": boxOut, "note": note})
			}
		})
	})
}
