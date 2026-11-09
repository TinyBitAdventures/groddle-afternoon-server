package main

// A street Piggy leaving the street for everyone (G605, Austin 2026-11-09), part of the shared
// street Piggies (objects_pigs.go):
//
//   - capture (items/npc_piggy.js verbs.capture 361-541): Herdkeeping (the client's "herd" perk, as
//     "ak5" for a name), not rooked, one capture a player per street per 42 game days (383-386:
//     achievements_place_time_ago_enough 60*24*24*7 seconds; none on a depressing street, the sad
//     streets here), one Pig Bait (the site's ledger takes it first, objects_uses.go), then the 75%
//     roll (439-444; a sad Piggy is always caught). Caught, the Piggy is gone for everyone
//     (apiDelete 535) and the catcher takes a Hog-tied Piggy (credited to the ledger as a take); the
//     place-time is set only on a catch (474-475). Missed, the bait is spent all the same and the
//     Piggy stays (the -30 mood is the player's own).
//   - youth (npc_piggy.js verbs.apply_youth 321-359, potion_animal_youth.js onPour 547-569): one of
//     the Potion of Animal Youth's three pours (the ledger's, objects_uses.go useYouth), then the
//     Piggy is gone for everyone (replaceWithPoofIfOnGround) and a Piglet lies where it stood, on the
//     ground for everyone (drops.go place, under the pourer: their 100 px guard). The source passed
//     a name on to the baby (onPour 563-565); the remake's Piglet is a plain pack item with no name,
//     so the name goes with the Piggy.
//
// Ours (Austin 2026-11-09): a Piggy gone this way comes back to its spot at the next game day, a new
// Piggy (onCreate: mood 1, hunger 0, no name, nobody's pets), as street animals that left a crowded
// street come back at the next game-day turn in the remake (register G82). The source never brought
// a static street Piggy back: a street emptied by captures stayed empty, which on a busy street lets
// one player end everyone else's Piggy play for good.

import (
	"log"
	"time"
)

const (
	pigCaptureEvery  = 60 * 24 * 24 * 7 * time.Second // npc_piggy.js 383: the source's "42 game-days"
	pigCaptureChance = 0.75                           // 439
	useYouth         = "potion_animal_youth"          // potion_animal_youth.js:25 points_capacity 3
	useBait          = "pig_bait"                     // npc_piggy.js 418-420

	pigTextGone        = "That Piggy isn't here any more."                                                    // ours
	pigTextHerd        = "You need to know Herdkeeping to attempt capture."                                   // 393-395
	pigTextOncePlace   = "You can only capture one piggy per location per 42 game-days..."                    // 385
	pigTextBaitless    = "The piggy ran away! You'll need something in your inventory with which to lure it." // 536
	pigTextYouthRooked = "There is nothing to pour this on!"                                                  // potion_animal_youth.js:73 (ours here: a rooked one)
)

// pigGone: a capture or a youth potion took it off the street, until the next game day.
func (ob *liveObj) pigGone() bool { return ob.spot.kind == "pig" && ob.goneDay > 0 }

// pigBackIfDue brings a gone Piggy back once the game day has turned (see the header); true when it
// came back now.
func (o *objects) pigBackIfDue(ob *liveObj) bool {
	if !ob.pigGone() || o.today() <= ob.goneDay {
		return false
	}
	o.freshPig(ob)
	ob.goneDay, ob.goneWhy = 0, ""
	ob.userName, ob.namer, ob.namedAt = "", 0, time.Time{}
	ob.petters, ob.uses, ob.tally, ob.usesDay = nil, nil, nil, 0
	ob.crops, ob.plops = nil, nil
	ob.stopTrip()
	return true
}

// pigLeaves takes a Piggy off the street for everyone until the next game day.
func (o *objects) pigLeaves(street string, ob *liveObj, why string) {
	ob.stopTrip()
	ob.plops = nil
	ob.goneDay, ob.goneWhy = o.today(), why
	o.armDay()
	log.Printf("objects: %s on %s left the street (%s) until the next game day", ob.spot.id, street, why)
}

// askCapture is the capture verb on a street Piggy (see the header).
func (o *objects) askCapture(cl *Client, m objAct, street string, spot *objSpot, perks map[string]bool) {
	ob := o.get(street, spot)
	before := o.state(ob)
	sad := o.w.sad[street]
	switch {
	case ob.pigGone():
		o.finishAct(cl, m, street, ob, before, refuse("gone", pigTextGone))
		return
	case o.pigRooked(street, ob):
		o.finishAct(cl, m, street, ob, before, refuse("rooked", "It's rooked. Revive it first."))
		return
	case !perks["herd"]:
		o.finishAct(cl, m, street, ob, before, refuse("skill", pigTextHerd))
		return
	case !sad && o.capturedHereLately(street, cl.ID()):
		o.finishAct(cl, m, street, ob, before, refuse("once_here", pigTextOncePlace))
		return
	}
	o.uses.ask(cl, useBait, 1, func(use map[string]any, code, message string) {
		ob := o.get(street, spot)
		before := o.state(ob)
		if code == "no_item" {
			code, message = "no_bait", pigTextBaitless
		}
		if code != "" {
			o.finishAct(cl, m, street, ob, before, refuse(code, message))
			return
		}
		if ob.pigGone() {
			// Someone else caught it while the bait was asked for: the bait goes back.
			if use != nil {
				o.uses.giveBack(use, cl.ID())
			}
			o.finishAct(cl, m, street, ob, before, refuse("gone", pigTextGone))
			return
		}
		roll := o.roll()
		caught := roll < pigCaptureChance || o.w.sad[street]
		v := verdict{ok: true, extra: map[string]any{"roll": roll, "success": caught}}
		if use != nil {
			v.extra["use"] = use
		}
		if caught {
			o.pigLeaves(street, ob, "capture")
			o.setCaptured(street, cl.ID())
			v.got = creditItem("hogtied_piggy", 1)
			v.ev = "capture"
		}
		o.finishAct(cl, m, street, ob, before, v)
	})
}

// askYouth is a Potion of Animal Youth poured on a street Piggy (see the header). k: the bottle
// poured, its place among the player's bottles in the pack (-1: the one in use).
func (o *objects) askYouth(cl *Client, m objAct, street string, spot *objSpot) {
	ob := o.get(street, spot)
	before := o.state(ob)
	switch {
	case ob.pigGone():
		o.finishAct(cl, m, street, ob, before, refuse("gone", pigTextGone))
		return
	case o.pigRooked(street, ob):
		o.finishAct(cl, m, street, ob, before, refuse("rooked", pigTextYouthRooked))
		return
	}
	k := -1
	if m.K != nil {
		k = copyOf(m.K)
	}
	x := o.pigX(street, ob)
	o.askUseCopy(cl, useYouth, k, func(use map[string]any, code, message string) {
		ob := o.get(street, spot)
		before := o.state(ob)
		if code != "" {
			o.finishAct(cl, m, street, ob, before, refuse(code, message))
			return
		}
		if ob.pigGone() {
			if use != nil {
				o.uses.giveBack(use, cl.ID())
			}
			o.finishAct(cl, m, street, ob, before, refuse("gone", pigTextGone))
			return
		}
		if now := o.pigX(street, ob); now == now {
			x = now
		}
		o.pigLeaves(street, ob, "youth")
		placed := false
		if place, ok := o.h.ProvidedLocked("drops.place").(func(string, int64, string, float64, float64, tradeItem) bool); ok {
			placed = place(street, cl.ID(), cl.Name(), x, spot.y-1, tradeItem{C: "piglet", N: 1})
		}
		v := verdict{ok: true, ev: "youth", extra: map[string]any{"x": x, "y": spot.y, "placed": placed}}
		if use != nil {
			v.extra["use"] = use
		}
		o.finishAct(cl, m, street, ob, before, v)
	})
}

// capturedHereLately: this player caught a Piggy on this street within the 42 "game days".
func (o *objects) capturedHereLately(street string, user int64) bool {
	at, ok := o.captures[street][user]
	return ok && o.h.Now().Sub(at) < pigCaptureEvery
}

func (o *objects) setCaptured(street string, user int64) {
	if o.captures == nil {
		o.captures = map[string]map[int64]time.Time{}
	}
	if o.captures[street] == nil {
		o.captures[street] = map[int64]time.Time{}
	}
	o.captures[street][user] = o.h.Now()
	o.saveSoon()
}

// capturesRecord is the place-times as the state file keeps them (Unix ms), the expired ones dropped.
func (o *objects) capturesRecord() map[string]map[int64]int64 {
	now := o.h.Now()
	out := map[string]map[int64]int64{}
	for street, users := range o.captures {
		for id, at := range users {
			if now.Sub(at) >= pigCaptureEvery {
				continue
			}
			if out[street] == nil {
				out[street] = map[int64]int64{}
			}
			out[street][id] = at.UnixMilli()
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (o *objects) capturesLoad(rec map[string]map[int64]int64) {
	for street, users := range rec {
		for id, ms := range users {
			if ms <= 0 || id <= 0 {
				continue
			}
			if o.captures == nil {
				o.captures = map[string]map[int64]time.Time{}
			}
			if o.captures[street] == nil {
				o.captures[street] = map[int64]time.Time{}
			}
			o.captures[street][id] = time.UnixMilli(ms)
		}
	}
}

// pigsBack is the game-day turn for gone Piggies (rollDay): those due come back and the street
// hears it; true while any Piggy is still away (the day timer stays armed).
func (o *objects) pigsBack() bool {
	away := false
	for street, room := range o.streets {
		var back []*liveObj
		for _, ob := range room {
			if !ob.pigGone() {
				continue
			}
			if o.pigBackIfDue(ob) {
				back = append(back, ob)
			} else {
				away = true
			}
		}
		if len(back) > 0 {
			o.broadcast(street, 0, back...)
			o.armPigs()
		}
	}
	return away
}
