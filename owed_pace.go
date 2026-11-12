package main

import (
	"encoding/json"
	"time"
)

// Paced redelivery of owed messages (the 2026-11-11 reconnect loop). A module
// that keeps a player's messages until the game acknowledges them (projects.go
// project_given and project_reward, feats.go feat rewards, the gardens' and
// patches' grants) used to send every one on each connect or sync, in one go
// under the hub lock. The send queue holds sendBuffer frames (hub.go); a burst
// bigger than that dropped the socket as "too far behind" (4008), and as the
// list only shrinks when the game acks (after a save), a player with more
// than about 60 owed reconnected every few seconds forever.
//
// Now a connect or sync sends at most owedBatch of one module's owed messages,
// oldest first, and the rest follow as acks come: an ack frees its slot and
// the next one goes (a sliding window, so one message the game never acks
// holds back only its own slot). A sync starts the window over, sending the
// oldest again: the game remembers what it applied (street-jobs-mp.js applied
// and rewarded) and acks a repeat without applying it twice. A window send
// waits while the client's queue has owedReserve free frames or fewer, so it
// never fills the queue whatever else the connect queued; what waits goes on
// the next ack, sync or owedRetry later. A message owed while its player is
// online still goes at once (one event owes a player one or two).

const (
	owedBatch   = 16 // owed messages of one module in flight to one client, unacknowledged
	owedReserve = 24 // free frames left in the client's send queue after a window send
)

// owedRetry is how long a window that the send queue had no room for waits to try again.
var owedRetry = 2 * time.Second

// owedItem is one owed message as the pacer sees it.
type owedItem struct {
	ID  string
	Msg json.RawMessage
}

// owedPacer is one module's windows, by player. Hub lock held for every call.
type owedPacer struct {
	h   *Hub
	all func(player int64) []owedItem // everything owed to a player, oldest first (nil: start names the list)
	out map[int64]*owedFlight
}

// owedFlight is what went to one client and has not been acked, and where the rest comes from.
type owedFlight struct {
	cl    *Client
	ids   map[string]bool
	list  func() []owedItem // read again at each top-up: the module's state then, not a copy
	retry bool              // a later try is set (owedRetry)
}

func newOwedPacer(h *Hub, all func(player int64) []owedItem) *owedPacer {
	return &owedPacer{h: h, all: all, out: map[int64]*owedFlight{}}
}

func (p *owedPacer) fresh(cl *Client, list func() []owedItem) *owedFlight {
	if list == nil && p.all != nil {
		id := cl.ID()
		list = func() []owedItem { return p.all(id) }
	}
	f := &owedFlight{cl: cl, ids: map[string]bool{}, list: list}
	p.out[cl.ID()] = f
	return f
}

// start begins cl's window again from the oldest owed (a sync or a connect) and sends the first
// of them. list nil is everything owed (the pacer's all).
func (p *owedPacer) start(cl *Client, list func() []owedItem) {
	p.top(p.fresh(cl, list))
}

// now sends a message just owed to cl at once, as it always went (an event owes each player one
// or two, never a burst), and it takes a slot in the window until it is acked.
func (p *owedPacer) now(cl *Client, o owedItem) {
	f := p.out[cl.ID()]
	if f == nil || f.cl != cl {
		f = p.fresh(cl, nil)
	}
	if !f.ids[o.ID] && cl.Online() {
		f.ids[o.ID] = true
		p.h.SendLocked(cl, o.Msg)
	}
}

// acked frees id's slot in cl's window, and the next owed message takes it.
func (p *owedPacer) acked(cl *Client, id string) {
	if f := p.out[cl.ID()]; f != nil && f.cl == cl {
		delete(f.ids, id)
		p.top(f)
	}
}

// drop forgets cl (it disconnected).
func (p *owedPacer) drop(cl *Client) {
	if f := p.out[cl.ID()]; f != nil && f.cl == cl {
		delete(p.out, cl.ID())
	}
}

// top fills f's window from its list, in order, skipping what is already in flight.
func (p *owedPacer) top(f *owedFlight) {
	cl := f.cl
	if f.list == nil || !cl.Online() {
		return
	}
	for _, o := range f.list() {
		if len(f.ids) >= owedBatch {
			return
		}
		if f.ids[o.ID] {
			continue
		}
		if p.h.RoomLocked(cl) <= owedReserve {
			// The send queue is busy (the connect's other frames): try again shortly, once.
			if !f.retry {
				f.retry = true
				p.h.After(owedRetry, func() {
					f.retry = false
					if p.out[cl.ID()] == f {
						p.top(f)
					}
				})
			}
			return
		}
		f.ids[o.ID] = true
		p.h.SendLocked(cl, o.Msg)
	}
}
