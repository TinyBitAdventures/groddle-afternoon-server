package main

// Trade requests (register G329, players/inc_requests.js): a player tells the
// street they are looking for someone to trade with, and whoever accepts
// starts a trade with them. Part of the trade module: trade.go registers these
// kinds and hooks, and an accept goes through its start. The race and game challenges, the other kinds of
// action_request, are each game's own (races.go, crowns.go, hoghaul.go).
//
//   - broadcastActionRequest (inc_requests.js:16-61): "<name> is looking for
//     someone to trade with." to everyone on the street and to the requester,
//     with a 5 minute timeout (19); a new request replaces the player's old
//     one (24-26).
//   - location.js:555-570: a player arriving on the street hears every request
//     still open there, with the time it has left; an expired one is skipped.
//   - location.js:895-950: the requester leaving the street cancels it
//     (cancelActionRequestBroadcast, no words).
//   - actionRequestCancel (183-187), also the requester's timer running out
//     (ActionRequestUI.as onTimerTick: TradeManager.localUpdate(true)): the line
//     becomes "<name> was looking for someone to trade with." and goes.
//   - actionRequestReply (110-121): "<name> was looking for someone to trade
//     with. <accepter> accepted.", the request goes, and the accepter starts a
//     trade with the requester (trading_request_start, trade.go's start and
//     its refusals, inc_trading.js:79-210).
//   - TradeManager.as:454-456: nobody else on the street: "Wait until there's
//     someone around to hear your offer before you make it!"
//   - A blocked pair never hears each other's requests
//     (RightSideManager.as actionRequestUpdate skips an ignored player).
//
// Client to server:
//
//	{"t":"trade_request"}                     look for someone to trade with here
//	{"t":"trade_request_cancel"}              End trade
//	{"t":"trade_request_accept","from":7}     Start negotiation!
//
// Server to client:
//
//	{"t":"trade_requested","from":{"id":7,"name":"Ada"},"text":"Ada is looking for someone to trade with.","timeout":300,"mine":false}
//	{"t":"trade_unrequested","from":7,"text":"…"}   it ended (text is absent when the requester just left)
//	{"t":"error","code":"trade","message":"…"}
//
// Ours: a rate limit; requests live in memory only.

import (
	"encoding/json"
	"log"
	"math"
	"time"
)

const (
	tradeRequestTimeout = 5 * time.Minute // inc_requests.js:19
	tradeRequestBurst   = 3
	tradeRequestEvery   = 5 * time.Second

	tradeRequestTextAlone   = "Wait until there's someone around to hear your offer before you make it!" // TradeManager.as:455
	tradeRequestTextLooking = " is looking for someone to trade with."
	tradeRequestTextWas     = " was looking for someone to trade with."
	tradeRequestTextGone    = "That player is not looking for someone to trade with any more."
)

type tradeRequest struct {
	from    raceRef
	street  string
	offered time.Time
	expire  func() bool
}

type tradeRequests struct {
	h     *Hub
	open  map[int64]*tradeRequest // by requester
	posts map[int64]*bucket
	start func(cl *Client, id int64) // trade.go: the accepter starts a trade with the requester
}

func newTradeRequests(h *Hub, start func(*Client, int64)) *tradeRequests {
	return &tradeRequests{h: h, open: map[int64]*tradeRequest{}, posts: map[int64]*bucket{}, start: start}
}

// left is location.js:895-950: the requester leaving the street cancels the request.
func (m *tradeRequests) left(cl *Client, street string) {
	if q := m.open[cl.ID()]; q != nil && q.street == street {
		m.end(q, "")
	}
}

func (m *tradeRequests) offline(cl *Client) {
	if q := m.open[cl.ID()]; q != nil {
		m.end(q, "")
	}
	delete(m.posts, cl.ID())
}

func (m *tradeRequests) secondsLeft(q *tradeRequest) int {
	left := tradeRequestTimeout - m.h.Now().Sub(q.offered)
	return int(math.Ceil(left.Seconds()))
}

func (m *tradeRequests) requestedMsg(q *tradeRequest, mine bool) map[string]any {
	return map[string]any{"t": "trade_requested", "from": q.from, "text": q.from.Name + tradeRequestTextLooking,
		"timeout": m.secondsLeft(q), "mine": mine}
}

// toStreet sends msg to everyone on the request's street who is not blocking or blocked by the requester.
func (m *tradeRequests) toStreet(q *tradeRequest, msg map[string]any) {
	for _, o := range m.h.StreetLocked(q.street) {
		if o.ID() != q.from.ID && blockedBetween(m.h, o.ID(), q.from.ID) {
			continue
		}
		m.h.SendLocked(o, msg)
	}
}

func (m *tradeRequests) post(cl *Client, frame json.RawMessage) {
	if cl.Street() == "" {
		return
	}
	b := m.posts[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.posts[cl.ID()] = b
	}
	if !b.take(m.h.Now(), tradeRequestBurst, tradeRequestEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	alone := true
	for _, o := range m.h.StreetLocked(cl.Street()) {
		if o.ID() != cl.ID() {
			alone = false
			break
		}
	}
	if alone {
		m.h.ErrorLocked(cl, "trade", tradeRequestTextAlone)
		return
	}
	if old := m.open[cl.ID()]; old != nil {
		m.end(old, "") // inc_requests.js:24-26: the new one replaces it
	}
	q := &tradeRequest{from: raceRef{ID: cl.ID(), Name: cl.Name()}, street: cl.Street(), offered: m.h.Now()}
	m.open[cl.ID()] = q
	log.Printf("requests: %d (%s) is looking for someone to trade with on %s", q.from.ID, q.from.Name, q.street)
	q.expire = m.h.After(tradeRequestTimeout, func() {
		if m.open[q.from.ID] == q {
			m.end(q, q.from.Name+tradeRequestTextWas)
		}
	})
	for _, o := range m.h.StreetLocked(q.street) {
		if o.ID() != q.from.ID && blockedBetween(m.h, o.ID(), q.from.ID) {
			continue
		}
		m.h.SendLocked(o, m.requestedMsg(q, o.ID() == q.from.ID))
	}
}

func (m *tradeRequests) cancelMsg(cl *Client, frame json.RawMessage) {
	if q := m.open[cl.ID()]; q != nil {
		m.end(q, q.from.Name+tradeRequestTextWas) // inc_requests.js:184
	}
}

func (m *tradeRequests) accept(cl *Client, frame json.RawMessage) {
	var msg struct {
		From int64 `json:"from"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.From <= 0 || msg.From == cl.ID() {
		return
	}
	q := m.open[msg.From]
	if q == nil || q.street != cl.Street() || blockedBetween(m.h, cl.ID(), msg.From) {
		m.h.ErrorLocked(cl, "trade", tradeRequestTextGone)
		return
	}
	// inc_requests.js:110-121: the line says who accepted, the request goes, the accepter starts the trade.
	m.end(q, q.from.Name+tradeRequestTextWas+" "+cl.Name()+" accepted.")
	log.Printf("requests: %d accepted %d's trade request", cl.ID(), q.from.ID)
	if m.start != nil {
		m.start(cl, q.from.ID)
	}
}

func (m *tradeRequests) end(q *tradeRequest, text string) {
	if m.open[q.from.ID] != q {
		return
	}
	delete(m.open, q.from.ID)
	if q.expire != nil {
		q.expire()
	}
	msg := map[string]any{"t": "trade_unrequested", "from": q.from.ID}
	if text != "" {
		msg["text"] = text
	}
	m.toStreet(q, msg)
	// The requester hears it even after leaving the street (their own End trade, the timeout).
	if cl := m.h.ClientLocked(q.from.ID); cl != nil && cl.Street() != q.street {
		m.h.SendLocked(cl, msg)
	}
}

// joined is location.js:555-570: an arrival hears the requests still open on the street.
func (m *tradeRequests) joined(cl *Client) {
	for _, q := range m.open {
		if q.street != cl.Street() || q.from.ID == cl.ID() || blockedBetween(m.h, cl.ID(), q.from.ID) {
			continue
		}
		if m.secondsLeft(q) <= 0 {
			continue
		}
		m.h.SendLocked(cl, m.requestedMsg(q, false))
	}
}
