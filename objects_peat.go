package main

// Peter out Peat (register G451, Austin's ruling 2026-11-07: faithful, online only):
// quests/bogspecialization_harvest_full_peat_bog_in_time_period.js 67-92, items/catalog_buffs.js
// 149-169. Started within 100 px of a Peat Bog with all 4 harvests left (onStarted: findCloseStack,
// "You need to be near a full Peat Bog."), the bog says "Empty me out! Go!" for 5 s and a 9 second
// timer runs; when it ends (the buff's on_remove, testBog) the bog is looked at: no harvests left
// sets the quest's flag, anything else fails it. The players do the emptying: a harvest takes one
// use off the shared bog (objects.go useNode), and one player may harvest a bog once a game day,
// twice with the upgrade (peat_1.js 142-160), so it takes four players, or two with the upgrade.
//
// The server decides: which bog, that it is full, that the player is close, and what it holds when
// the 9 s end. The game only asks.
//
// Client to server:
//
//	{"t":"bog_start","req":3,"id":"node:peat_1:0"}
//
// Server to client:
//
//	{"t":"bog_started","req":3,"ok":true,"id":"node:peat_1:0","ms":9000}     to the player (ok false: "message")
//	{"t":"bog_call","street":"GA5…","id":"node:peat_1:0"}                    to the street: the bog's bubble
//	{"t":"bog_test","id":"node:peat_1:0","emptied":true}                     to the player, when the 9 s end
//
// Ours: only a street's shared bogs (a bog in a player's yard is that player's own, home_yard.go);
// a player offline when the 9 s end hears nothing, and their game's timer fails the quest (Try again
// starts it over), where the source's offline buff could still have set the flag.

import (
	"encoding/json"
	"math"
	"time"
)

const (
	bogReach    = 100             // onStarted: pc.findCloseStack(is_bog, 100)
	bogFor      = 9 * time.Second // catalog_buffs.js:152 duration 9
	bogNotClose = "You need to be near a full Peat Bog."
	bogAlready  = "You're already doing that."
)

// bogRun is one player's nine seconds.
type bogRun struct {
	street string
	spot   *objSpot
	stop   func() bool
}

func (o *objects) handleBogStart(cl *Client, frame json.RawMessage) {
	var m struct {
		Req int64  `json:"req"`
		ID  string `json:"id"`
	}
	if json.Unmarshal(frame, &m) != nil || !o.allow(cl) {
		return
	}
	no := func(message string) {
		o.h.SendLocked(cl, map[string]any{"t": "bog_started", "req": m.Req, "ok": false, "id": m.ID, "message": message})
	}
	id := cl.ID()
	if o.bogs[id] != nil {
		no(bogAlready)
		return
	}
	street := cl.Street()
	spot := o.spotOf(street, m.ID)
	if street == "" || spot == nil || spot.kind != "node" || spot.class != "peat_1" {
		no(bogNotClose)
		return
	}
	x, y := cl.Pos()
	if math.Hypot(x-spot.x, y-spot.y) > bogReach {
		no(bogNotClose)
		return
	}
	ob := o.get(street, spot)
	full := ob.units == o.w.rules.Nodes[spot.class].Max
	o.forget(street, ob)
	if !full {
		no(bogNotClose)
		return
	}
	run := &bogRun{street: street, spot: spot}
	run.stop = o.h.After(bogFor, func() { o.testBog(id, run) })
	o.bogs[id] = run
	o.h.SendLocked(cl, map[string]any{"t": "bog_started", "req": m.Req, "ok": true, "id": m.ID, "ms": bogFor.Milliseconds()})
	o.h.BroadcastStreetLocked(street, map[string]any{"t": "bog_call", "street": street, "id": m.ID}, 0)
}

// testBog is the quest's testBog at the buff's end: !bog.isUseable() (peat_1.js 205-206, harvests_remaining 0).
func (o *objects) testBog(id int64, run *bogRun) {
	if o.bogs[id] != run {
		return
	}
	delete(o.bogs, id)
	ob := o.get(run.street, run.spot)
	emptied := ob.units == 0
	o.forget(run.street, ob)
	if cl := o.h.ClientLocked(id); cl != nil && cl.Online() {
		o.h.SendLocked(cl, map[string]any{"t": "bog_test", "id": run.spot.id, "emptied": emptied})
	}
}
