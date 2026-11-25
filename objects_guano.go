package main

// A Batterfly's hourly Guano, one pile under each street Batterfly for everyone there (register
// G641, Austin 2026-11-10), part of the shared street animals (objects_creatures.go).
//
// The source (GameServerJS items/npc_batterfly.js): every Batterfly runs its own clock from the
// moment it is made (onCreate 1058-1062: apiSetInterval('onInterval', 1*60), in minutes, as
// npc_piggy.js 1460 passes game_days_to_ms(0.5) / 1000 / 60), and each hour it puts one Guano on
// the street while the street holds fewer than 10 (onInterval 1064-1072:
// this.container.countItemClass('guano') < 10, then createItemStackFromSource('guano', 1, this.x,
// this.y, this)). The Guano lies in the location for whoever picks it up (guano.js verbs.pickup,
// takeable_pickup). The clock has no player test and nothing stops it when the last player leaves
// (onPlayerExit 1841-1843), and timers do run on a street with nobody on it (npc_piggy.js onRooting
// 1131-1164 asks getNumActivePlayers() inside one).
//
// Here (Austin's rulings, 2026-11-10, and lane flyk 2026-11-24):
//   - Each Guano lands where its Batterfly is flying when it comes due, as the source's
//     createItemStackFromSource at this.x (lane flyk, Austin 2026-11-24; before, one pile under its
//     spot): the server works out that x from the Batterfly's shared flight (flierpath.go, the
//     game's flierAt) at the tick's moment, and each page drops it to the floor under the
//     Batterfly there. One lying within guanoMergePx of a pile of the same Batterfly joins it (the
//     game's dropGuano), so a Batterfly has a few piles, the same for everyone; a street's piles hold at most
//     guanoCap Guano between them: a tick adds one only while they hold fewer (the source's test,
//     counted in Guano as the game's own clock always counted it, js/animal-husbandry.js
//     passiveGuano; the source counted stacks, location.js countItemClass 1948-1953).
//   - Guano a player dropped on the street (drops.go) does not count toward that, and Guano from
//     feeding a Batterfly still goes to the feeder (askBatterfly), not to the pile. Both depart from
//     the source, which counted every Guano stack in the location and dropped the fed Guano there too
//     (feed 817-820).
//   - The clock runs whether or not anyone is on the street. No timer runs for an empty street: the
//     piles are settled from the clock when someone looks (obj_sync), picks one up, or stands there
//     as a tick comes due (armGuano), so a street nobody visits costs nothing and has still grown
//     when someone arrives. A tick that comes while the street is full is lost, as in the source.
//   - Each Batterfly ticks at its own minute of the hour (guanoPhase, from its street and id), as
//     the source's clocks each started when their Batterfly was made.
//   - Every pile starts empty: the clocks start (guanoStart) the first time a server with this
//     module starts, kept in the state file, so a state file from before reads as no Guano yet and
//     the first tick is under an hour away.
//   - A rooked Batterfly makes no Guano in the source (onInterval 1065); the Rook here never rooks
//     one (rookCreatureKey), so there is nothing to skip.
//
// Pick up (obj_act verb "pickup" on the Batterfly's id, "x" the pile's x): the server settles the
// street, gives the asker the whole pile nearest that x and credits it to the site's ledger (credits.go "take", as a feed's Guano
// is), and the street hears the pile is gone ("objs"). A second asker finds nothing there.
//
// Client to server:
//
//	{"t":"obj_act","req":7,"id":"batterfly:0","verb":"pickup","x":2210.5}
//
// Server to client:
//
//	{"t":"objs","street":"GA9…","snap":true,"guano":1731300000000,…,"objs":[{"id":"batterfly:0","guano":3,"piles":[[2210.5,2,1731300400000],[480,1,1731304000000]],…}]}
//	     "guano": this server shares the hourly Guano, its clocks running since that Unix ms (a
//	     server from before has no such word, and the game keeps each player's own clock as it did);
//	     on a Batterfly "guano" is its Guano lying on the street and "piles" where: [x, count, the
//	     path's clock (server ms minus lag) when the pile's first Guano fell, 0 when unknown], so a
//	     page knows how high the Batterfly flew there and drops it to the floor under it
//	{"t":"obj_ok","req":7,"id":"batterfly:0","verb":"pickup","take":3,"before":{…,"guano":3},"obj":{…}}
//	{"t":"obj_no","req":7,"id":"batterfly:0","verb":"pickup","code":"gone","message":"…","obj":{…}}
//
// QA (GRODDLE_TREES_DEV=1, as the other street object dev words): {"t":"obj_guano_dev","skip":ms}
// moves every Guano clock that far on.

import (
	"encoding/json"
	"hash/fnv"
	"math"
	"sort"
	"time"
)

const (
	// guanoEvery: npc_batterfly.js onCreate 1061, apiSetInterval('onInterval', 1*60) minutes.
	guanoEvery = time.Hour
	// guanoCap: onInterval 1069, countItemClass('guano') < 10 (js/animal-husbandry.js BATTERFLY.passiveCap).
	guanoCap = 10
	// guanoGone: ours (in the source the stack was simply no longer there to click).
	guanoGone = "Someone else picked that Guano up first."
	// guanoDevSkipMax bounds one QA skip.
	guanoDevSkipMax = 30 * 24 * time.Hour
	// guanoMergePx: a Guano this close to a pile of its Batterfly joins it (the game's
	// animal-husbandry-feature dropGuano, which merges within 40 px).
	guanoMergePx = 40
)

// guanoPile is Guano lying where a Batterfly dropped it: x, how many, and the path's clock (server
// ms minus lag) when its first one fell (0: unknown, a pile from a state file of before).
type guanoPile struct {
	x float64
	n int
	c int64
}

// batterflyAt is where a Batterfly is flying at t: its x on its shared flight (flierpath.go) and
// the path's clock then. A spot with no street width (data from before) stays at its spot.
func (o *objects) batterflyAt(ob *liveObj, t time.Time) (float64, int64) {
	at := t
	if ob.holdEnd.After(at) {
		at = ob.holdEnd
	}
	c := at.UnixMilli() - ob.lag
	spot := ob.spot
	if spot.worldW <= 0 {
		return spot.x, c
	}
	return flierX("batterfly", flierSeed("batterfly", spot.index, spot.x), spot.worldW, float64(c-gameEpochMs)), c
}

// dropGuano lays one Guano where the Batterfly is flying at t: on a pile of its within
// guanoMergePx, else a new one.
func (o *objects) dropGuano(ob *liveObj, t time.Time) {
	x, c := o.batterflyAt(ob, t)
	x = math.Round(x*10) / 10
	ob.guano++
	for i := range ob.guanoPiles {
		if math.Abs(ob.guanoPiles[i].x-x) < guanoMergePx {
			ob.guanoPiles[i].n++
			return
		}
	}
	ob.guanoPiles = append(ob.guanoPiles, guanoPile{x: x, n: 1, c: c})
}

// guanoPileNear is the index of a Batterfly's pile nearest x, or -1 with none.
func guanoPileNear(ob *liveObj, x float64) int {
	best := -1
	for i, p := range ob.guanoPiles {
		if best < 0 || math.Abs(p.x-x) < math.Abs(ob.guanoPiles[best].x-x) {
			best = i
		}
	}
	return best
}

// guanoPilesState is a Batterfly's piles as a page reads them: [[x, n, c], ...].
func guanoPilesState(ob *liveObj) [][3]float64 {
	out := make([][3]float64, 0, len(ob.guanoPiles))
	for _, p := range ob.guanoPiles {
		out = append(out, [3]float64{p.x, float64(p.n), float64(p.c)})
	}
	return out
}

// guanoSpots is a street's Batterflies, by id.
func (o *objects) guanoSpots(street string) []*objSpot {
	var out []*objSpot
	for _, spot := range o.spots(street) {
		if spot.kind == "batterfly" {
			out = append(out, spot)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// guanoPhase is where in the hour one Batterfly's clock ticks: 1 s to an hour after each hour of
// guanoStart, fixed by its street and id.
func guanoPhase(street, id string) time.Duration {
	h := fnv.New32a()
	h.Write([]byte(street))
	h.Write([]byte{0})
	h.Write([]byte(id))
	return time.Duration(1+h.Sum32()%uint32(guanoEvery/time.Second)) * time.Second
}

// guanoTickAfter is the first tick of a Batterfly's clock after t.
func (o *objects) guanoTickAfter(street, id string, t time.Time) time.Time {
	first := o.guanoStart.Add(guanoPhase(street, id))
	if t.Before(first) {
		return first
	}
	return first.Add((t.Sub(first)/guanoEvery + 1) * guanoEvery)
}

// guanoSettled is the time a street's piles were last settled to (the clocks' start when never).
func (o *objects) guanoSettled(street string) time.Time {
	if at, ok := o.guanoAt[street]; ok && at.After(o.guanoStart) {
		return at
	}
	return o.guanoStart
}

// settleGuano brings a street's piles up to now: every tick since they were last settled drops one
// Guano where its Batterfly was flying then (dropGuano) while the street's piles hold fewer than
// guanoCap; the rest are lost.
// Returns the Batterflies whose pile grew.
func (o *objects) settleGuano(street string) []*liveObj {
	spots := o.guanoSpots(street)
	if len(spots) == 0 {
		return nil
	}
	now := o.h.Now()
	from := o.guanoSettled(street)
	if !now.After(from) {
		return nil
	}
	obs := make([]*liveObj, len(spots))
	next := make([]time.Time, len(spots))
	total := 0
	for i, spot := range spots {
		obs[i] = o.get(street, spot)
		total += obs[i].guano
		next[i] = o.guanoTickAfter(street, spot.id, from)
	}
	grew := make([]bool, len(spots))
	for total < guanoCap {
		first := -1
		for i := range next {
			if !next[i].After(now) && (first < 0 || next[i].Before(next[first])) {
				first = i
			}
		}
		if first < 0 {
			break
		}
		o.dropGuano(obs[first], next[first])
		total++
		grew[first] = true
		next[first] = next[first].Add(guanoEvery)
	}
	if o.guanoAt == nil {
		o.guanoAt = map[string]time.Time{}
	}
	o.guanoAt[street] = now
	var out []*liveObj
	for i, ob := range obs {
		if grew[i] {
			out = append(out, ob)
		} else {
			o.forget(street, ob)
		}
	}
	return out
}

// guanoTotal is the Guano in a street's piles now (as last settled).
func (o *objects) guanoTotal(street string) int {
	n := 0
	for _, ob := range o.streets[street] {
		if ob.spot.kind == "batterfly" {
			n += ob.guano
		}
	}
	return n
}

// armGuano keeps a street's piles growing in front of whoever stands there: one timer for the
// street's next tick while someone is on it and its piles hold fewer than guanoCap. A street with
// nobody on it has none (its piles are settled when someone arrives).
func (o *objects) armGuano(street string) {
	if o.guanoStop[street] != nil {
		return
	}
	spots := o.guanoSpots(street)
	if len(spots) == 0 || len(o.h.StreetLocked(street)) == 0 || o.guanoTotal(street) >= guanoCap {
		return
	}
	now := o.h.Now()
	var next time.Time
	for _, spot := range spots {
		if t := o.guanoTickAfter(street, spot.id, now); next.IsZero() || t.Before(next) {
			next = t
		}
	}
	if o.guanoStop == nil {
		o.guanoStop = map[string]func() bool{}
	}
	o.guanoStop[street] = o.h.After(next.Sub(now), func() {
		delete(o.guanoStop, street)
		o.guanoGrew(street, 0)
		o.armGuano(street)
	})
}

// guanoGrew settles a street and tells everyone on it (but except) which piles grew.
func (o *objects) guanoGrew(street string, except int64) {
	if grew := o.settleGuano(street); len(grew) > 0 {
		o.broadcast(street, except, grew...)
		o.saveSoon()
	}
}

// guanoPickX is the x a pickup asks for: the pile's ("x"), else (a page from before) the asker's own.
func guanoPickX(cl *Client, m objAct) float64 {
	if m.X != nil {
		return *m.X
	}
	x, _ := cl.Pos()
	return x
}

// pickGuano is "pick up" on one of a Batterfly's piles (the one at "x"): the whole pile to the
// asker, credited. A pile someone else took first is gone, even with another of its piles elsewhere.
func (o *objects) pickGuano(cl *Client, m objAct, street string, spot *objSpot) {
	grew := o.settleGuano(street)
	ob := o.get(street, spot)
	var also []*liveObj
	for _, other := range grew {
		if other != ob {
			also = append(also, other)
		}
	}
	before := o.state(ob)
	v := refuse("gone", guanoGone)
	x := guanoPickX(cl, m)
	if i := guanoPileNear(ob, x); i >= 0 && (m.X == nil || math.Abs(ob.guanoPiles[i].x-x) < guanoMergePx) {
		n := ob.guanoPiles[i].n
		ob.guanoPiles = append(ob.guanoPiles[:i], ob.guanoPiles[i+1:]...)
		ob.guano = max(0, ob.guano-n)
		v = verdict{ok: true, got: creditItem("guano", n), extra: map[string]any{"take": n, "x": x}}
	}
	if len(also) > 0 {
		// A refusal answers the asker only (finishAct): the piles that grew go to the whole street here.
		o.broadcast(street, 0, also...)
	}
	o.finishAct(cl, m, street, ob, before, v)
	o.armGuano(street)
}

// guanoDev is the QA word that moves every Guano clock on by "skip" ms (GRODDLE_TREES_DEV=1): the
// clocks' start and every street's settled time step back that far, so the ticks that would have
// come in that time are due now. The sender's street, and every street with a tick timer, settles
// at once.
func (o *objects) guanoDev(cl *Client, frame json.RawMessage) {
	var m struct {
		Skip int64 `json:"skip"`
	}
	if !o.dev || json.Unmarshal(frame, &m) != nil || m.Skip <= 0 || time.Duration(m.Skip)*time.Millisecond > guanoDevSkipMax {
		return
	}
	d := time.Duration(m.Skip) * time.Millisecond
	o.guanoStart = o.guanoStart.Add(-d)
	for street, at := range o.guanoAt {
		o.guanoAt[street] = at.Add(-d)
	}
	streets := map[string]bool{}
	if street := cl.Street(); street != "" {
		streets[street] = true
	}
	for street, stop := range o.guanoStop {
		stop()
		streets[street] = true
	}
	o.guanoStop = nil
	for street := range streets {
		o.guanoGrew(street, 0)
		o.armGuano(street)
	}
}
