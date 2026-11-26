package main

// Dust Traps (register G946, report #700; Austin's ruling 2026-11-26: as Glitch). A Dust Trap in
// the Ancestral Lands was one item on the street (items/dust_trap.js), so it was everyone's: the
// first player to walk into it sprang it and it stayed down for everyone on the street until it
// reset. doPlayerResponse 132-153:
//
//	if(this.ready) {
//		if(this.ready_time) {
//			var reset_time = 2 * (time() - this.ready_time);
//			if(reset_time < min_time || reset_time/2 > full_reset_time) { reset_time = min_time; }
//			else if (reset_time > max_time) { reset_time = max_time; }
//		} else { var reset_time = min_time; }
//		this.ready = false;
//		this.pc = pc;
//		...
//		this.apiSetTimer('reset', (reset_time * 1000) + 250);
//
// with min_time 180, max_time 3600 and full_reset_time a day (119-123). reset (237-242) raises it
// for everyone (setAndBroadcastState('up'), announce_sound_to_all('DUST_TRAP_UP')) and makeReady,
// 1 s later (185-189), sets it idle with ready = true and ready_time = time(). Every state change
// went to the whole street (setAndBroadcastState: smackDown 245, down 249).
//
// Here a trap is a resource node (node:dust_trap:<n>, js/street-objects.js SHARED_NODE_CLASSES):
// units 1 while ready, 0 while down, "in" the ms until it is ready again, "sprung" how long ago it
// went down (each page plays smackDown, down, then up from it). The server decides who sprang it
// (the first obj_act "trigger" while it is ready: obj_ok to that player, "objs" to the street) and
// how long it stays down; a trigger while it is down is obj_no "not_ready" (the source's collision
// did nothing). What the trap gives (a remnant's 10 to 60 s, or the drop table) stays the player's
// own roll on their page, as for every node's yield beyond the base take, so the grant rules are the
// solo ones (js/resource-nodes.js harvestResourceNode dust_trap); nothing is credited here.
//
// The trap keeps its ready_time (readyAt) once it has been sprung, so a later trigger stays down 2 x
// the time it stood ready: such a trap is never forgotten, and the state file keeps it. A trap
// nobody has sprung since the server started has no ready_time (onCreate 197-201: ready, no
// ready_time), so its first spring is the minimum.
//
// Not run: onPlayerEnter 207-212 (a player arriving alone on the street springs it empty 20% of
// the time), which the solo game never ran either.

import "time"

const (
	dustTrapClass = "dust_trap"
	// dustTrapRaise: the reset timer's + 250 ms (153), then makeReady 1 s after reset (238).
	dustTrapRaise = 1250 * time.Millisecond
)

// dustTrapReset is doPlayerResponse's reset_time (132-145) for a trap ready since readyAt (zero:
// no ready_time).
func dustTrapReset(rule nodeRule, readyAt, now time.Time) time.Duration {
	if len(rule.Reset) < 3 {
		return 0
	}
	lo := time.Duration(rule.Reset[0]) * time.Millisecond
	hi := time.Duration(rule.Reset[1]) * time.Millisecond
	full := time.Duration(rule.Reset[2]) * time.Millisecond
	if readyAt.IsZero() {
		return lo
	}
	reset := 2 * now.Sub(readyAt)
	if reset < lo || reset/2 > full {
		return lo
	}
	return min(reset, hi)
}

// springTrap is one player walking into a Dust Trap: sprung for everyone while it was ready.
func (o *objects) springTrap(street string, ob *liveObj, rule nodeRule) verdict {
	if ob.units < 1 {
		return refuse("not_ready", notReady[dustTrapClass])
	}
	now := o.h.Now()
	reset := dustTrapReset(rule, ob.readyAt, now)
	ob.units = 0
	ob.sprungAt = now
	o.schedule(street, ob, reset+dustTrapRaise, o.trapReady)
	return verdict{ok: true, extra: map[string]any{"reset": reset.Milliseconds()}}
}

// trapReady is makeReady (185-189): idle and ready again, its ready_time now.
func (o *objects) trapReady(_ string, ob *liveObj) {
	ob.units = 1
	ob.readyAt = o.h.Now()
	ob.sprungAt = time.Time{}
}

// resumeTrap re-arms a trap the state file kept down: ready at its due time if that passed while
// the server was down (its ready_time then), else the rest of the wait.
func (o *objects) resumeTrap(street string, ob *liveObj, now time.Time) {
	if ob.due.IsZero() {
		return
	}
	due := ob.due
	ob.due = time.Time{}
	if d := due.Sub(now); d > 0 {
		o.schedule(street, ob, d, o.trapReady)
		return
	}
	ob.units, ob.readyAt, ob.sprungAt = 1, due, time.Time{}
}
