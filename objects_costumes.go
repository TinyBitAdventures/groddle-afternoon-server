package main

// Pot Luck Day's chicken costumes (G982, Austin 2026-11-17: "seen by everyone online, the server
// holds the costume, it falls off at season end"). Ours: Glitch never ran a Thanksgiving event and
// never dressed an animal (the game's inventions register, pot-luck-costumes).
//
// A street Chicken (objects_creatures.go) may wear one costume, everyone's: "cos" in its state,
// the class of the Turkey Tailfan or the Rooster Plume that dressed it. The game's dress verb
// (js/features/pot-luck-feature.js, a drop of the costume on the Chicken or its menu row) asks
// here first:
//
//	{"t":"obj_act","req":7,"id":"chicken:1","verb":"dress","c":"turkey_tailfan"}
//
// Refused, in this order, with the game's own lines (js/pot-luck.js POT_LUCK_LINES): out of
// season ("no_season"), a class that is no costume ("not_costume"), a Chicken already wearing that
// costume ("same"). A Chicken in the other costume is re-dressed (G1181, Austin 2026-11-23: one
// costume can replace the other). Then the site's ledger takes the costume, a whole item
// (objects_uses.go, as an incubated egg), and the Chicken wears it for everyone: obj_ok to the
// dresser (with "use"), its new state to the street, obj_ev "dress" with "c". Who may dress a
// Chicken they dressed before is the player's own record (each player counts each Chicken once,
// the game's pot-luck-feature `counted`); the server holds only what the Chicken wears. When Pot Luck Day ends (Ur's calendar, Pacific time, as the game's js/seasons.js) every
// costume falls off for everyone: a timer at the window's end clears and broadcasts them, and a
// costume read after the end (a state file, a restart) is dropped as it is read.
//
// The season is the game's (js/seasons.js potLuckWindow): from the Sunday before US Thanksgiving
// (the fourth Thursday of November) to the Sunday after, whole days on Pacific time. QA:
// -season-date YYYY-MM-DD holds the server's calendar on that date (the page's ?seasondate=).

import (
	"flag"
	"testing"
	"time"
)

var seasonDateFlag = flag.String("season-date", "", "QA: hold the seasons' calendar on this date (YYYY-MM-DD, the page's ?seasondate=); empty: the server's clock")

// seasonDateForTest lets a test hold the calendar; under go test the flag is ignored.
var seasonDateForTest string

// The costumes a street Chicken can wear (js/pot-luck.js COSTUME_CLASSES).
var chickenCostumes = map[string]bool{"turkey_tailfan": true, "rooster_plume": true}

// The game's lines (js/pot-luck.js POT_LUCK_LINES).
const (
	costumeNoSeason = "Chickens only dress up for Pot Luck Day."
	costumeNotOne   = "That isn't something a Chicken can wear."
	costumeTooSlow  = "Too slow! Somebody else dressed this Chicken first."
)

// costumeSame is the game's POT_LUCK_LINES.same: re-dressing in the costume it already wears (G1181).
func costumeSame(c string) string {
	return "This Chicken is already wearing a " + costumeLabels[c] + "."
}

// The costumes' names (js/pot-luck.js POT_LUCK_ITEMS label).
var costumeLabels = map[string]string{"turkey_tailfan": "Turkey Tailfan", "rooster_plume": "Rooster Plume"}

var urZone = func() *time.Location {
	if z, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return z
	}
	return time.FixedZone("PST", -8*3600)
}()

// seasonDate is the calendar date the seasons read: the QA flag's, else now on Ur's clock.
func seasonDate(now time.Time) (int, time.Month, int, bool) {
	fixed := seasonDateForTest
	if !testing.Testing() && *seasonDateFlag != "" {
		fixed = *seasonDateFlag
	}
	if fixed != "" {
		if t, err := time.Parse("2006-01-02", fixed); err == nil {
			return t.Year(), t.Month(), t.Day(), true
		}
	}
	t := now.In(urZone)
	return t.Year(), t.Month(), t.Day(), false
}

// potLuckWindow is js/seasons.js potLuckWindow: the November days the season runs in year y.
func potLuckWindow(y int) (from, to int) {
	firstDow := int(time.Date(y, time.November, 1, 0, 0, 0, 0, time.UTC).Weekday())
	thanksgiving := 1 + (4-firstDow+7)%7 + 21
	return thanksgiving - 4, thanksgiving + 3
}

// potLuckOn: is it Pot Luck Day on Ur's calendar?
func potLuckOn(now time.Time) bool {
	y, m, d, _ := seasonDate(now)
	from, to := potLuckWindow(y)
	return m == time.November && d >= from && d <= to
}

// potLuckLeft is how long the season has left from now (0 when it is off; a held QA date never ends).
func potLuckLeft(now time.Time) time.Duration {
	if !potLuckOn(now) {
		return 0
	}
	if _, _, _, held := seasonDate(now); held {
		return 365 * 24 * time.Hour
	}
	t := now.In(urZone)
	_, to := potLuckWindow(t.Year())
	end := time.Date(t.Year(), time.November, to+1, 0, 0, 0, 0, urZone)
	return max(time.Second, end.Sub(now))
}

// potLuckStartIn is how long until the next Pot Luck Day starts (midnight on Ur's clock, the window's
// first day), 0 while it is on or under a held QA date.
func potLuckStartIn(now time.Time) time.Duration {
	if _, _, _, held := seasonDate(now); held || potLuckOn(now) {
		return 0
	}
	return nextUrMidnight(now, func(y int) int { from, _ := potLuckWindow(y); return from })
}

// nextUrMidnight is how long from now to the start (00:00 on Ur's clock) of the November day dayOf
// names, this year's if it is still to come, else next year's.
func nextUrMidnight(now time.Time, dayOf func(y int) int) time.Duration {
	y := now.In(urZone).Year()
	start := time.Date(y, time.November, dayOf(y), 0, 0, 0, 0, urZone)
	if !start.After(now) {
		start = time.Date(y+1, time.November, dayOf(y+1), 0, 0, 0, 0, urZone)
	}
	return start.Sub(now)
}

// seasonWakeMax caps a wait for a season's start: the timer wakes at least this often, settles and
// arms again, so a long wait follows the wall clock (a clock step, a suspended host) rather than drifting.
const seasonWakeMax = 6 * time.Hour

// askDress is a Pot Luck Day costume on a street Chicken (bare, or in the other costume): the
// refusals, then the ledger takes the costume, then the Chicken wears it for everyone.
func (o *objects) askDress(cl *Client, m objAct, street string, spot *objSpot) {
	ob := o.get(street, spot)
	before := o.state(ob)
	switch {
	case !potLuckOn(o.h.Now()):
		o.finishAct(cl, m, street, ob, before, refuse("no_season", costumeNoSeason))
		return
	case !chickenCostumes[m.C]:
		o.finishAct(cl, m, street, ob, before, refuse("not_costume", costumeNotOne))
		return
	case ob.costume == m.C:
		o.finishAct(cl, m, street, ob, before, refuse("same", costumeSame(m.C)))
		return
	}
	was := ob.costume
	o.uses.ask(cl, m.C, 1, func(use map[string]any, code, message string) {
		ob := o.get(street, spot)
		before := o.state(ob)
		if code != "" {
			o.finishAct(cl, m, street, ob, before, refuse(code, message))
			return
		}
		if ob.costume != was {
			// Dressed (or re-dressed) by someone else while the ledger answered: the costume it took
			// stays used, as an incubated egg does in the same race (askIncubate).
			o.finishAct(cl, m, street, ob, before, refuse("dressed", costumeTooSlow))
			return
		}
		ob.costume = m.C
		o.hold(ob, creatureHold)
		o.armCostumes()
		v := verdict{ok: true, ev: "dress"}
		if use != nil {
			v.extra = map[string]any{"use": use}
		}
		o.finishAct(cl, m, street, ob, before, v)
		o.saveSoon()
	})
}

// costumeSettle drops a costume read after the season ended (a state file, a restart). Returns
// whether it changed.
func (o *objects) costumeSettle(ob *liveObj) bool {
	if ob.costume == "" || potLuckOn(o.h.Now()) {
		return false
	}
	ob.costume = ""
	return true
}

// armCostumes sets the timer that takes every costume off when the season ends.
func (o *objects) armCostumes() {
	if o.costumeStop != nil {
		return
	}
	left := potLuckLeft(o.h.Now())
	o.costumeStop = o.h.After(left, func() {
		o.costumeStop = nil
		o.undressAll()
	})
}

// undressAll is the season's end: every dressed Chicken loses its costume, and its street sees it.
func (o *objects) undressAll() {
	if potLuckOn(o.h.Now()) {
		// Woken a little early (a clock step): try again at the end.
		o.armCostumes()
		return
	}
	changed := false
	for street, room := range o.streets {
		for _, ob := range room {
			if ob.spot.kind != "chicken" || ob.costume == "" {
				continue
			}
			ob.costume = ""
			changed = true
			o.broadcast(street, 0, ob)
			o.forget(street, ob)
		}
	}
	if changed {
		o.saveSoon()
	}
}
