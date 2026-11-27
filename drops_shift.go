package main

// Un-expanding the house moves the shared stacks in its last stretch (G1327, R50).
//
// In the original homes_unexpand (GameServerJS locations/inc_homes.js 154-183) took the last wall
// segment off every floor and moved every item in the house standing in the last 300 px 150 px to
// the left (166-172: "move all items over", the ground stacks too, as every item in the location),
// then the players there (174-179). The game moves the owner's furniture and the stacks its save
// holds (js/home-decor.js unexpandInterior); the stacks dropped since shared house floors (G1190)
// are this server's and the site's, and they stayed where they were, so a stack near the old right
// wall lay past the new one, out of sight and out of reach, and the owner's own stacks at home are
// never sent back (dropKeep). Now the owner's game tells the shift and the stacks here move with it:
//
//	{"t":"ground_shift","from":1750,"dx":-150}
//	{"t":"ground_shifted","op":"shift-…","status":"settled"|"refused","why":"…","text":"…","n":2}
//	then ground_state to each floor whose stacks moved
//
// from is a room x (a house's ground floor counts from the house's left end, as its stacks are kept
// here; an upper floor and a Tower floor from their own 0). Only the owner, standing in their own house
// (every floor of it moves, R5) or on one of their Tower floors (that floor alone: unexpandInterior on the
// floor stood on), and only homes_unexpand's one shift (dx -150). The site moves every stack there at x
// >= from in one transaction, once per op (LedgerDrop::shiftStacks, POST groddle/v1/ledger/drop/shift),
// and answers where each one lies now; this server then moves its own copies to those places. So no
// stack moves half: the site's rows all move or none do, and a lost answer is asked again with the same
// op (answered as recorded) until it comes. While a shift waits, nothing new is dropped or moved on those
// floors (busy), and the shift waits for the drops and moves already on their way to the site to land,
// so each one is moved with the rest (its x was the old house's). A game from before sends no shift: its
// shared stacks stay where they were, as before.

import (
	"encoding/json"
	"log"
	"math"
	"regexp"
	"time"
)

const (
	ledgerRouteDropShift = "/groddle/v1/ledger/drop/shift"

	shiftSegment = 150.0    // inc_homes.js:170 x - 150
	shiftFromMax = 100000.0 // ours: a room x a house never reaches
	shiftWait    = 250 * time.Millisecond
	shiftWaits   = 40 // 10 s for drops and moves on their way to land
	shiftRetries = 20 // a lost answer: asked again every dropLoadRetry, for 5 minutes
)

var shiftOpRe = regexp.MustCompile(`^shift-[0-9a-f]{16}$`)

// shiftCall is one shift as the site is asked it.
type shiftCall struct {
	Op      string   `json:"op"`
	ID      int64    `json:"id"`
	Streets []string `json:"streets"`
	From    int      `json:"from"`
	Dx      int      `json:"dx"`
}

// shiftedRow is a stack the site moved and where it lies now.
type shiftedRow struct {
	ID string `json:"id"`
	X  int    `json:"x"`
}

func (l *httpLedger) dropShift(c shiftCall) dropAnswer {
	body, _ := json.Marshal(c)
	return l.dropAsk(ledgerRouteDropShift, body)
}

func (m *drops) refuseShift(cl *Client, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "ground_shifted", "status": "refused", "why": why, "text": text, "n": 0})
}

// shiftStreets is every place a shift by the owner standing on `street` moves: each floor of their
// house, or the Tower floor alone; nil anywhere else.
func shiftStreets(street string) []string {
	if owner, _ := houseFloorOwner(street); owner > 0 {
		return houseStreets(owner)
	}
	if towerOwner(street) > 0 {
		return []string{street}
	}
	return nil
}

// shiftFrame is the owner's homes_unexpand: the stacks in the last stretch of their floors move over.
func (m *drops) shiftFrame(cl *Client, frame json.RawMessage) {
	if !m.allow(cl) {
		return
	}
	var msg struct {
		From *float64 `json:"from"`
		Dx   *float64 `json:"dx"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.From == nil || msg.Dx == nil || math.IsNaN(*msg.From) || *msg.From < 0 || *msg.From > shiftFromMax || *msg.Dx != -shiftSegment {
		m.refuseShift(cl, "bad", pickTextOff)
		return
	}
	street := cl.Street()
	streets := shiftStreets(street)
	if streets == nil || dropHomeOwner(street) != cl.ID() {
		m.refuseShift(cl, "place", moveTextPlace)
		return
	}
	if m.site == nil || !m.loaded {
		m.refuseShift(cl, "off", pickTextOff)
		return
	}
	for _, s := range streets {
		if m.shifting[s] != "" {
			m.refuseShift(cl, "busy", dropTextBusy)
			return
		}
	}
	call := shiftCall{Op: "shift-" + m.newID(), ID: cl.ID(), From: int(math.Round(*msg.From)), Dx: int(*msg.Dx)}
	for _, s := range streets {
		call.Streets = append(call.Streets, dropSiteStreet(s))
	}
	if m.shifting == nil {
		m.shifting = map[string]string{}
	}
	for _, s := range streets {
		m.shifting[s] = call.Op
	}
	m.shiftWhenStill(call, streets, 0)
}

// shiftWhenStill asks the site for the shift once no drop or move is on its way to those floors (up to
// shiftWaits waits; then it goes anyway: a late one is set where its game put it, as before).
func (m *drops) shiftWhenStill(call shiftCall, streets []string, waited int) {
	if waited < shiftWaits {
		for _, s := range streets {
			if m.placing[s] > 0 {
				m.h.After(shiftWait, func() { m.shiftWhenStill(call, streets, waited+1) })
				return
			}
		}
	}
	m.askShift(call, streets, 0)
}

// placingStarted and placingDone count the drops and moves on their way to the site per floor.
func (m *drops) placingStarted(street string) {
	if m.placing == nil {
		m.placing = map[string]int{}
	}
	m.placing[street]++
}

func (m *drops) placingDone(street string) {
	if m.placing[street]--; m.placing[street] <= 0 {
		delete(m.placing, street)
	}
}

// askShift sends the shift to the site; with no answer it asks again, the same op, until it answers.
func (m *drops) askShift(call shiftCall, streets []string, tries int) {
	site := m.site
	m.spawn(func() {
		a := site.dropShift(call)
		m.h.After(0, func() {
			if a.Status == "" && tries < shiftRetries {
				log.Printf("drops: shift %s for %d: no answer, again in %s", call.Op, call.ID, dropLoadRetry)
				m.h.After(dropLoadRetry, func() { m.askShift(call, streets, tries+1) })
				return
			}
			for _, s := range streets {
				if m.shifting[s] == call.Op {
					delete(m.shifting, s)
				}
			}
			if a.Status == "" {
				a.Status, a.Why, a.Text = "refused", "off", pickTextOff
			}
			out := map[string]any{"t": "ground_shifted", "op": call.Op, "status": a.Status, "why": a.Why, "text": a.Text, "n": 0}
			if a.Status != "settled" {
				log.Printf("drops: shift %s for %d refused: %s", call.Op, call.ID, a.Why)
				if c := m.h.ClientLocked(call.ID); c != nil {
					m.h.SendLocked(c, out)
				}
				return
			}
			on := map[string]bool{}
			for _, s := range streets {
				on[s] = true
			}
			moved := map[string]bool{}
			for _, row := range a.Moved {
				if s := m.stacks[row.ID]; s != nil && on[s.Street] {
					s.X = float64(row.X)
					moved[s.Street] = true
				}
			}
			out["n"] = len(a.Moved)
			if c := m.h.ClientLocked(call.ID); c != nil {
				m.h.SendLocked(c, out)
			}
			log.Printf("drops: %d un-expanded: %d stacks moved %d from x %d (%s)", call.ID, len(a.Moved), call.Dx, call.From, call.Op)
			for _, s := range streets {
				if moved[s] {
					m.h.BroadcastStreetLocked(s, m.stateMsg(s), 0)
				}
			}
		})
	})
}
