package main

// Piggies on a street (G334), part of the shared street objects (objects.go): a street Piggy's
// mood and hunger are everyone's, as in the original, where one npc_piggy item on the street
// carried them for every player there. The server holds each street Piggy (pig:<i>, the client's
// spawnPiggies index, laid out by tools/build-realtime-objects.mjs) from the first time anyone is
// on its street, runs its twice-daily tick, and decides pet, nibble, feed and a Piggy eating.
// Each player's own side stays theirs, as in solo: energy, the iMG and Plops a verb gives, and the
// food they spend; the server also counts each player's nibbles (the daily limit) and credits the
// Meat a nibble gives (see the end of this header).
//
// The rules (glitch-GameServerJS items/npc_piggy.js, as the remake has them in js/animal-mood.js):
//   - onCreate 1433-1472: mood 1, hunger 0, is_daily 1, a tick every game_days_to_ms(0.5) (two real
//     hours). Here a Piggy's clock starts when the server first has its street.
//   - onTwiceDaily 1832-2040: is_daily flips; on the daily half the mood falls by 1 unless
//     has_been_petted (the remake sets that flag on a pet, PIGGY_PET_PROTECTS_MOOD: Austin's fix of
//     the source bug G63b), and a mood-0 Piggy whose street's other Piggies average mood 1 or more
//     goes to 1 ("Other animals make me happy!"); hunger +1 each half below 2, then +1 on the daily
//     half up to 10; at 10 it croaks. A street Piggy never starves (PIGGY_STARVATION_DEATH, Austin's
//     call G63: only owned piggies die, and those are each player's own). A hungry Piggy goes for
//     food, dropped food first (onTwiceDaily 1879-1913 and 1975-1990: food_nearby before
//     trants_nearby): a stack dropped for everyone on the street (drops.go) it walks to and eats one
//     of (pigFood below); else the nearest tree on its level with fruit, which it walks to and eats
//     one fruit of on arrival (onFoodPathing 1482-1533), when the street hears "ate".
//   - Food dropped for everyone (onContainerItemAdded 1393-1411): a hungry Piggy that is not sad goes
//     for it at once if it lies in reach (its move_limits; here pigReachX across, pigEatDY up or
//     down), and on arrival eats one of the stack (onFoodPathing 1489-1500: apiConsume(1), hunger 0,
//     "Yum!"). The walk takes the source's walk speed over the distance (npc_walk_speed 30-55,
//     onCreate 1442; rules.pigtrip.speed), and the stack may be picked up meanwhile, as in the source.
//   - A fruiting tree a player harvests, waters or pets (onContainerItemStateChanged; the client
//     sends "food" on the tree): every hungry Piggy of the street in reach goes for it (pigsToTree).
//     Petters from an earlier game day are forgotten (1857-1867).
//   - A sad Piggy (a depressing street, include/animal_sadness.js) skips its tick and refuses.
//   - pet (onPetComplete 1709-1735): the ak_piggy_pet success roll by rank; on success mood +1
//     (incMood, at most 2), has_been_petted, and the petter is remembered for today.
//   - nibble (handler 862-910): petted by this player today (or Animal Kinship VI, the "ak6" perk)
//     and the Piggy's mood 0 or hunger over 2: "That Piggy doesn't feel like being nibbled right
//     now."; not petted today: "Give that Piggy some love first!"; else the ak_piggy_nibble roll.
//   - feed (feed handler 560-750): food of base cost 5 or more sates a hungry Piggy. With the site's
//     ledger (G335 slice 5, askFeed) the client names the food and how many ("c", "n", one stack at
//     most) and the ledger takes them before the Piggy is sated for everyone: which foods sate is
//     data/trade.json "sates" (the game's tools/build-realtime-trade.mjs), never the client's word.
//     Food fed to a Piggy that is not hungry, or food under base cost 5, changes nothing anyone else
//     sees, so it stays the player's own loss and the ledger is not asked. Without the ledger the
//     client says so with the "sate" perk, as before.
//   - eat: a tree's fruit ("on"), eaten where only that player saw the Piggy walk to it; the fruit
//     comes off the shared tree. A stack only that player sees (their own drop: a class the ledger
//     does not carry, or one dropped while the server was out of reach) no longer sates a street
//     Piggy (G335 slice 5): the server cannot see that food, and every stack the ledger carries on a
//     public street is dropped for everyone and eaten through drops.go (pigFood), which can. The
//     game's shared Piggies leave such stacks alone.
// Chickens, Butterflies and Batterflies are street animals the server shares too (objects_creatures.go).
//
// Where a street Piggy walks (G334, Austin 2026-11-07; before it was each page's own): one npc_piggy
// item was the same animal for everyone, so every page now draws it on one path from the server's
// clock, as Chickens and Butterflies (objects_creatures.go): to and fro round its spot with the
// original's rooting stops (npc_piggy.js startMoving 1226-1245, onRooting 1131-1160), rolled from a
// seed every page shares (the game's js/street-creatures.js piggyWalkAt). The server never moves it:
// what it keeps is "lag" and "hold", so an untouched Piggy costs nothing more. A player at it holds it
// for everyone ("wait", onInteractionStarting), a landed pet, nibble or feed holds it too and the
// street sees it (obj_ev "pet", "nibble", "feed"), and the Rook stops its path (rookHoldCreature).
// Its walk to food and home is the server's too (slice 1 of research/plans/street-animals-shared.md,
// pigTrip): the server knows where the path has it (pigpath.go, the game's piggyPlan ported number
// for number, from the snapshot's "homes" and "trunks" every page walks it from), sets it off from
// there to beside the food, eats on arrival and lets its path take it on where it left it. The trip
// runs on the path's clock, so a player at it, a landed verb and the Rook freeze it for everyone.
// A nibble is held to each player's daily limit of this Piggy (ak_piggy_nibble interval_limit) and
// its Meat is credited to the ledger (credits.go "take").
// A street Piggy's name is everyone's too (namePig below), as the source's user_name on the one
// npc_piggy item: items/include/animal_naming.js canName 3-22 and onInputBoxResponse 38-62,
// npc_piggy.js verbs.name 750-784 and rename 786-816. Animal Kinship V names (the client says so
// with the "ak5" perk, as "ak6" for a nibble); a Piggy with no name, the player who named it, or
// anyone an hour after the last naming may (re)name it; a sad Piggy refuses a first name (765).
// The name is cut to 32 characters of a-z, 0-9 and spaces (41) and goes through the word filter
// (wordfilter.go, the remake's own addition) before anyone sees it. The street hears it as obj_ev
// "name" (sendBubble with no pc, 49: everyone sees 'And they shall call me "..."'). The street's
// Chickens, Butterflies and Batterflies (objects_creatures.go) take the same include, so nameAnimal
// names them by the same rules (npc_chicken.js verbs.name 677, rename 707; npc_butterfly.js 471, 506;
// npc_batterfly.js 635, 670), without the sad check, which only npc_piggy.js has.
// A street Piggy outlives a restart of the server (objects_persist.go, -objects-file): its mood,
// hunger, petters and clock are kept, and the ticks it missed while the server was down run when
// it is back.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	pigTick       = 2 * time.Hour // PIGGY_TICK_MS: half of a four-hour game day
	pigMoodMax    = 2
	pigHungerMax  = 10
	pigNibbleMax  = 2 // npc_piggy.js 875: hunger > 2 refuses a nibble
	pigHalfDayMax = 2 // below this hunger rises every half day
	pigReachX     = 1600
	pigReachY     = 1000
	pigEatDY      = 100 // EAT_RADIUS: food much higher or lower is out of reach (apiFindPath)
	pigWalkPx     = 42  // npc_piggy.js onCreate 1442: npc_walk_speed randInt(30,55) px/s, the middle
	pigPatrol     = 90  // ANIMAL_PATROL.piggies: a Piggy's walk each way of its spot
)

// Copy from npc_piggy.js.
const (
	pigSad       = "Can't do that! Piggy is sad!."                         // 568-570, 836-838, 930-932
	pigNotMood   = "That Piggy doesn't feel like being nibbled right now." // 876
	pigNeedsPet  = "Give that Piggy some love first!"                      // 897
	pigNotHungry = "That Piggy is not hungry."
	pigNameSad   = "This piggy is sad, and in no mood to be called names. Even nice ones." // 765
)

// Naming (items/include/animal_naming.js). The source hides the verb where these refuse; the lines are ours.
const (
	pigNameMax     = 32        // onInputBoxResponse 41: value.substr(0, 32)
	pigRenameAfter = time.Hour // canName 18: time() - named_on > 3600
	pigNameSkill   = "You need to know Animal Kinship V to name an animal."
	pigNameEmpty   = "Names need at least one letter or number." // js/animal-husbandry.js nameAnimal
)

// nameState adds an animal's name to its state: who gave it and how long ago (ms).
func (o *objects) nameState(ob *liveObj, m map[string]any) {
	m["name"], m["namer"], m["named"] = ob.userName, ob.namer, max(0, o.h.Now().Sub(ob.namedAt).Milliseconds())
}

// sanitizeAnimalName is onInputBoxResponse 41 (substr(0, 32), then only a-z, 0-9 and spaces) and the
// word filter after it, as the game's js/animal-husbandry.js nameAnimal does.
func sanitizeAnimalName(raw string) string {
	if len(raw) > pigNameMax {
		raw = raw[:pigNameMax]
	}
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; isWordLetter(c) || isWordDigit(c) || c == ' ' {
			b.WriteByte(c)
		}
	}
	return filterWords(b.String())
}

// nameSingle is each nameable street animal's name_single (npc_piggy.js 5, npc_chicken.js,
// npc_butterfly.js, npc_batterfly.js).
var nameSingle = map[string]string{"pig": "Piggy", "chicken": "Chicken", "butterfly": "Butterfly", "batterfly": "Batterfly"}

// nameTaken is ours: the source hid the verb from anyone else within the hour (canName 12-18).
func nameTaken(kind string) string {
	return "Someone named this " + nameSingle[kind] + " less than an hour ago."
}

// nameAnimal is the name and rename verbs on a street Piggy, Chicken, Butterfly or Batterfly (see the header).
func (o *objects) nameAnimal(street string, ob *liveObj, m objAct, perks map[string]bool, user int64) verdict {
	if !perks["ak5"] {
		return refuse("skill", pigNameSkill) // canName 6
	}
	now := o.h.Now()
	if ob.userName != "" && ob.namer != user && now.Sub(ob.namedAt) <= pigRenameAfter {
		return refuse("named", nameTaken(ob.spot.kind)) // canName 12-18
	}
	if ob.spot.kind == "pig" && ob.userName == "" && o.sadOn(street) {
		return refuse("sad", pigNameSad) // verbs.name conditions 764-766 (rename has no such check)
	}
	name := sanitizeAnimalName(m.Name)
	if strings.TrimSpace(name) == "" {
		return refuse("empty", pigNameEmpty)
	}
	if name == ob.userName {
		return refuse("unchanged", "") // 43: value != this.user_name
	}
	ob.userName, ob.namer, ob.namedAt = name, user, now
	return verdict{ok: true, extra: map[string]any{"name": name}, ev: "name"}
}

// pigDev is GRODDLE_TREES_DEV=1 only, for QA: {"t":"obj_dev","id":"pig:4","hunger":5,"mood":0,"tick":true}
// sets a Piggy on the player's street and, with tick, runs its next twice-daily tick now.
func (o *objects) pigDev(cl *Client, frame json.RawMessage) {
	var m struct {
		ID     string `json:"id"`
		Hunger *int   `json:"hunger"`
		Mood   *int   `json:"mood"`
		Tick   bool   `json:"tick"`
	}
	street := cl.Street()
	if !o.dev || json.Unmarshal(frame, &m) != nil || street == "" {
		return
	}
	spot := o.spotOf(street, m.ID)
	if spot == nil || spot.kind != "pig" {
		return
	}
	ob := o.get(street, spot)
	if m.Hunger != nil {
		ob.hunger = min(pigHungerMax, max(0, *m.Hunger))
	}
	if m.Mood != nil {
		ob.mood = min(pigMoodMax, max(0, *m.Mood))
	}
	if m.Tick {
		ob.tickAt = o.h.Now()
		o.tickPigs()
		o.armPigs()
		return
	}
	o.broadcast(street, 0, ob)
}

// freshPig is onCreate: mood 1, hunger 0, is_daily, the first tick two hours on.
func (o *objects) freshPig(ob *liveObj) {
	ob.mood, ob.hunger, ob.daily = 1, 0, true
	ob.tickAt = o.h.Now().Add(pigTick)
}

// wakePigs makes every Piggy of a street live: their clocks start (get arms the tick timer).
func (o *objects) wakePigs(street string) {
	for _, spot := range o.spots(street) {
		if spot.kind == "pig" && o.streets[street][spot.id] == nil {
			o.get(street, spot)
		}
	}
}

// armPigs sets the one timer for the soonest Piggy tick anywhere.
func (o *objects) armPigs() {
	var due time.Time
	for _, room := range o.streets {
		for _, ob := range room {
			if ob.spot.kind == "pig" && !ob.pigGone() && (due.IsZero() || ob.tickAt.Before(due)) {
				due = ob.tickAt
			}
		}
	}
	if due.IsZero() || (o.pigStop != nil && !o.pigDue.After(due)) {
		return
	}
	if o.pigStop != nil {
		o.pigStop()
	}
	o.pigDue = due
	o.pigStop = o.h.After(max(0, due.Sub(o.h.Now()))+10*time.Millisecond, func() {
		o.pigStop, o.pigDue = nil, time.Time{}
		o.tickPigs()
		o.armPigs()
		o.saveSoon()
	})
}

// tickPigs runs every Piggy tick that is due, in time order per street, and tells each street.
func (o *objects) tickPigs() {
	now := o.h.Now()
	streets := make([]string, 0, len(o.streets))
	for street := range o.streets {
		streets = append(streets, street)
	}
	sort.Strings(streets)
	for _, street := range streets {
		var pigs []*liveObj
		for _, ob := range o.streets[street] {
			if ob.spot.kind == "pig" && !ob.pigGone() {
				pigs = append(pigs, ob)
			}
		}
		sort.Slice(pigs, func(i, j int) bool { return pigs[i].spot.id < pigs[j].spot.id })
		changed := map[*liveObj]bool{}
		var events []map[string]any
		for guard := 0; guard < 1000; guard++ {
			var next *liveObj
			for _, ob := range pigs {
				if !ob.tickAt.After(now) && (next == nil || ob.tickAt.Before(next.tickAt)) {
					next = ob
				}
			}
			if next == nil {
				break
			}
			next.tickAt = next.tickAt.Add(pigTick)
			changed[next] = true
			for _, ev := range o.pigTwiceDaily(street, next, pigs) {
				if tree, ok := ev["tree"].(*liveObj); ok {
					delete(ev, "tree")
					changed[tree] = true
				}
				events = append(events, ev)
			}
		}
		if len(changed) == 0 {
			continue
		}
		list := make([]*liveObj, 0, len(changed))
		for ob := range changed {
			list = append(list, ob)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].spot.id < list[j].spot.id })
		o.broadcast(street, 0, list...)
		for _, ev := range events {
			o.h.BroadcastStreetLocked(street, ev, 0)
		}
		for _, ob := range list {
			o.forget(street, ob)
		}
	}
}

// pigTwiceDaily is one onTwiceDaily tick (npc_piggy.js 1832-2040; js/animal-mood.js piggyTwiceDailyTick).
func (o *objects) pigTwiceDaily(street string, ob *liveObj, pigs []*liveObj) []map[string]any {
	today := o.today()
	for id, day := range ob.petters {
		if day != today {
			delete(ob.petters, id)
		}
	}
	if o.sadOn(street) {
		return nil
	}
	ev := func(kind string) map[string]any {
		return map[string]any{"t": "obj_ev", "street": street, "id": ob.spot.id, "ev": kind}
	}
	var out []map[string]any
	ob.daily = !ob.daily
	if ob.daily {
		if !ob.petted {
			ob.mood = max(0, ob.mood-1)
		}
		ob.petted = false
		sum, n := 0, 0
		for _, other := range pigs {
			if other != ob {
				sum, n = sum+other.mood, n+1
			}
		}
		if n > 0 && float64(sum)/float64(n) >= 1 && ob.mood == 0 {
			ob.mood = 1
			out = append(out, ev("happy"))
		}
	}
	hunger := ob.hunger
	scanned := ob.daily || hunger > 0
	switch {
	case hunger < pigHalfDayMax:
		ob.hunger = hunger + 1
	case hunger < pigHungerMax:
		if ob.daily {
			ob.hunger = hunger + 1
		}
	default:
		// A street Piggy croaks at 10 and never starves (PIGGY_STARVATION_DEATH).
		ob.croak = true
		out = append(out, ev("croak"))
	}
	// Dropped food first (goForFood), else a tree's fruit: either way it walks there on its shared
	// path's clock (startTrip) and eats on arrival (onFoodPathing), and everyone sees the same walk.
	if scanned && ob.hunger > 0 && ob.trip == nil && !o.pigRooked(street, ob) && !o.goForFood(street, ob, "") {
		x := o.pigX(street, ob)
		if tree := o.fruitNear(street, x, ob.spot.y); tree != nil {
			o.startTrip(street, ob, tree.spot.x, tree.spot.id, "")
			o.forget(street, tree)
		}
	}
	return out
}

// pigFood is onContainerItemAdded for a stack dropped for everyone (drops.go calls it once the
// site has it): the nearest hungry Piggy on the street that is not sad, not rooked, not already on
// its way to food and has it in reach goes for it.
func (o *objects) pigFood(street, id string, x, y float64) {
	if o.sadOn(street) {
		return
	}
	var best *liveObj
	bestD := math.Inf(1)
	for _, spot := range o.spots(street) {
		if spot.kind != "pig" {
			continue
		}
		ob := o.streets[street][spot.id]
		if ob == nil || ob.pigGone() || ob.hunger <= 0 || ob.trip != nil || math.Abs(spot.y-y) > pigEatDY || o.pigRooked(street, ob) {
			continue
		}
		px := o.pigX(street, ob)
		if math.Abs(px-x) > pigReachX {
			continue
		}
		if d := math.Abs(px - x); d < bestD || (d == bestD && spot.id < best.spot.id) {
			best, bestD = ob, d
		}
	}
	if best != nil && o.goForFood(street, best, id) {
		o.broadcast(street, 0, best)
		o.saveSoon()
	}
}

// goForFood sends a Piggy for a stack on the ground (id, or the nearest food in reach when ""): it
// walks there (startTrip), then eats one if the stack is still there (onFoodPathing). False when
// there is none.
func (o *objects) goForFood(street string, ob *liveObj, id string) bool {
	food, _ := o.h.ProvidedLocked("drops.food").(func(string, string, float64, float64, float64, float64) (string, float64))
	if food == nil || o.h.ProvidedLocked("drops.eat") == nil {
		return false
	}
	id, at := food(street, id, o.pigX(street, ob), ob.spot.y, pigReachX, pigEatDY)
	if id == "" {
		return false
	}
	o.startTrip(street, ob, at, "", id)
	return true
}

// fruitNear is the nearest living, unrooked tree on the Piggy's level (y) with fruit from where it
// stands (x) (onTwiceDaily's trant scan, then the pathing, js/animal-mood-feature.js findFoodFor),
// or nil.
func (o *objects) fruitNear(street string, x, y float64) *liveObj {
	var best *liveObj
	bestD := math.Inf(1)
	ids := make([]string, 0)
	for id, spot := range o.spots(street) {
		if spot.kind == "tree" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		spot := o.spotOf(street, id)
		if math.Abs(spot.y-y) > pigEatDY || math.Abs(spot.x-x) > pigReachX || o.rooked(street, id) {
			continue
		}
		tree := o.get(street, spot)
		if tree.dead || tree.fruit <= 0 {
			o.forget(street, tree)
			continue
		}
		if d := math.Abs(spot.x - x); d < bestD {
			best, bestD = tree, d
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Walks to food (slice 1 of research/plans/street-animals-shared.md)
// ---------------------------------------------------------------------------

// pigTripRule is the game's PIGGY_TRIP (data/objects.json rules.pigtrip): the walk's pace (px a
// second), the pause at the food (onFoodPathing 1485-1531: chew, then startMoving 3 s on), and where
// it stops: beside a tree's foot (treeStop: half the tree's footprint and half its body), each Piggy
// on its own spot (gap apart, body wide; lane PIGSTUCK).
type pigTripRule struct {
	Speed    float64 `json:"speed"`
	EatMs    int64   `json:"eatMs"`
	TreeStop float64 `json:"treeStop"`
	Gap      float64 `json:"gap"`
	Body     float64 `json:"body"`
}

func (r *pigTripRule) fill() {
	if r.Speed <= 0 {
		r.Speed = pigWalkPx
	}
	if r.EatMs <= 0 {
		r.EatMs = 3000
	}
	if r.TreeStop <= 0 {
		r.TreeStop = 120
	}
	if r.Body <= 0 {
		r.Body = 60
	}
	if r.Gap <= 0 {
		r.Gap = r.Body + 8
	}
}

// pigTrip is a street Piggy's walk to food and back to where it left its path. In the original the
// server walked the one npc_piggy item to the food (npc_animal goForFood: apiFindPath(item.x, item.y,
// 0, 'onFoodPathing'), npc_piggy.js 1404, 1428, 1986-2002), it chewed, idled 3 s and walked on
// (onFoodPathing 1482-1533), and every client drew it where the server said. Here the walk is laid
// on the Piggy's shared path's clock (C0: the path's clock, server ms minus lag, when it set off), so
// a player at it (wait), a landed verb or the Rook freezes the walk where it is, for everyone, the way
// it freezes its path: X0 to X1 in Leg ms, Eat ms at the food, X1 back to X0 in Leg ms. Its path stood
// still meanwhile, and runs on from X0 when it is back (the trip's whole length goes on its lag).
type pigTrip struct {
	X0      float64 `json:"x0"`
	X1      float64 `json:"x1"`
	C0      int64   `json:"c0"`
	Leg     int64   `json:"leg"`
	Eat     int64   `json:"eat"`
	Tree    string  `json:"tree,omitempty"`  // tree:<i> whose fruit it goes for
	Stack   string  `json:"stack,omitempty"` // or the shared ground stack (drops.go)
	Arrived bool    `json:"arrived,omitempty"`
}

func (t *pigTrip) total() int64 { return 2*t.Leg + t.Eat }

// pathClock is the Piggy's path clock now: server ms minus lag, frozen while it is held.
func (o *objects) pathClock(ob *liveObj) int64 {
	at := o.h.Now()
	if ob.holdEnd.After(at) {
		at = ob.holdEnd
	}
	return at.UnixMilli() - ob.lag
}

func (o *objects) pigHome(street string, spot *objSpot) pigHome {
	dir := 1
	if spot.index%2 != 0 {
		dir = -1
	}
	return pigHome{baseX: spot.x, rng: spot.rng, dir: dir, trunks: o.trunksOf(street), trunkPx: o.w.rules.PigTrip.TreeStop}
}

// pigX is where the Piggy stands now, as every page draws it: on its trip, else on its path.
func (o *objects) pigX(street string, ob *liveObj) float64 {
	pc := o.pathClock(ob)
	if t := ob.trip; t != nil {
		if e := pc - t.C0; e >= 0 && e < t.total() {
			switch {
			case e < t.Leg:
				return t.X0 + (t.X1-t.X0)*float64(e)/float64(t.Leg)
			case e < t.Leg+t.Eat:
				return t.X1
			default:
				return t.X1 + (t.X0-t.X1)*float64(e-t.Leg-t.Eat)/float64(t.Leg)
			}
		}
		if pc >= t.C0+t.total() {
			pc -= t.total()
		} else {
			pc = t.C0
		}
	}
	x, _, _ := pigAt(o.pigHome(street, ob.spot), creatureSeed(ob.spot.index, ob.spot.x), float64(pc-gameEpochMs))
	return x
}

// pigRooked: the Rook has this Piggy (it goes after no food, npc_animal onContainerItemAdded 1394, 1419).
func (o *objects) pigRooked(street string, ob *liveObj) bool {
	fn, ok := o.h.ProvidedLocked("rook.thingRooked").(func(string, string) bool)
	return ok && fn(street, rookCreatureKey(ob.spot))
}

// tripStop is where a Piggy going for food at fx stops (the game's animal-mood-feature stopFor):
// beside a tree's foot (or on a stack), on its own side, the first spot no other Piggy walking to
// food has taken (a body further out each time; the far side when its own side is full).
func (o *objects) tripStop(street string, ob *liveObj, x0, fx float64, tree bool) float64 {
	rule := o.w.rules.PigTrip
	near := 1.0
	switch {
	case x0 < fx:
		near = -1
	case x0 == fx && ob.spot.index%2 == 0:
		near = -1
	}
	first := 0.0
	if tree {
		first = rule.TreeStop
	}
	var taken []float64
	for _, other := range o.streets[street] {
		if other != ob && other.spot.kind == "pig" && other.trip != nil {
			taken = append(taken, other.trip.X1)
		}
	}
	free := func(x float64) bool {
		for _, t := range taken {
			if math.Abs(t-x) < rule.Body {
				return false
			}
		}
		return true
	}
	for _, side := range []float64{near, -near} {
		for k := 0; k < 4; k++ {
			if x := fx + side*(first+float64(k)*rule.Gap); free(x) {
				return x
			}
		}
	}
	return fx + near*(first+4*rule.Gap)
}

// startTrip sets a Piggy off for food at fx (a tree's fruit, or a stack on the ground) from where
// its path has it now. The caller tells the street (its state carries "trip").
func (o *objects) startTrip(street string, ob *liveObj, fx float64, tree, stack string) {
	rule := o.w.rules.PigTrip
	x0 := o.pigX(street, ob)
	x1 := o.tripStop(street, ob, x0, fx, tree != "")
	leg := int64(mathRoundJS(math.Abs(x1-x0) / rule.Speed * 1000))
	ob.trip = &pigTrip{X0: math.Round(x0*10) / 10, X1: math.Round(x1*10) / 10, C0: o.pathClock(ob), Leg: leg, Eat: rule.EatMs, Tree: tree, Stack: stack}
	ob.eatingFood = stack
	o.armTrip(street, ob)
}

// armTrip sets the timer for the trip's next step (arriving at the food, then back on its path), on
// the path's clock: a hold pushes it on, so the timer checks the clock again when it fires.
func (o *objects) armTrip(street string, ob *liveObj) {
	if ob.tripStop != nil {
		ob.tripStop()
		ob.tripStop = nil
	}
	t := ob.trip
	if t == nil {
		return
	}
	next := t.C0 + t.Leg
	if t.Arrived {
		next = t.C0 + t.total()
	}
	pc := o.pathClock(ob)
	if pc >= next {
		o.tripStep(street, ob)
		return
	}
	now := o.h.Now()
	wait := time.Duration(next-pc) * time.Millisecond
	if ob.holdEnd.After(now) {
		wait += ob.holdEnd.Sub(now)
	}
	ob.tripStop = o.h.After(wait, func() {
		ob.tripStop = nil
		o.tripStep(street, ob)
		o.saveSoon()
	})
}

// tripStep is the trip's next step once its clock got there: it eats at the food (onFoodPathing
// 1482-1533), then, back where it left its path, the path takes it on.
func (o *objects) tripStep(street string, ob *liveObj) {
	t := ob.trip
	if t == nil {
		return
	}
	pc := o.pathClock(ob)
	if !t.Arrived && pc >= t.C0+t.Leg {
		t.Arrived = true
		o.tripEat(street, ob, t)
	}
	if t.Arrived && pc >= t.C0+t.total() {
		// Back where it set off: its path runs on from there (the whole trip goes on its lag).
		ob.trip = nil
		ob.lag += t.total()
		o.broadcast(street, 0, ob)
		return
	}
	o.armTrip(street, ob)
}

// tripEat is the arrival: one fruit off the tree it walked to, or one of the stack, while it is
// still hungry and the food is still there (it may have been taken meanwhile, as in the source).
func (o *objects) tripEat(street string, ob *liveObj, t *pigTrip) {
	if t.Stack != "" {
		eat, _ := o.h.ProvidedLocked("drops.eat").(func(string, string, func(bool)))
		if eat == nil {
			ob.eatingFood = ""
			return
		}
		eat(t.Stack, ob.spot.id, func(ok bool) {
			if ob.eatingFood == t.Stack {
				ob.eatingFood = ""
			}
			if !ok || ob.hunger <= 0 {
				return
			}
			ob.hunger, ob.croak = 0, false
			o.broadcast(street, 0, ob)
			o.saveSoon()
		})
		return
	}
	spot := o.spotOf(street, t.Tree)
	if spot == nil || ob.hunger <= 0 || o.rooked(street, t.Tree) {
		return
	}
	tree := o.get(street, spot)
	if tree.dead || tree.fruit <= 0 {
		o.forget(street, tree)
		return
	}
	tree.fruit--
	ob.hunger, ob.croak = 0, false
	o.broadcast(street, 0, ob, tree)
	o.h.BroadcastStreetLocked(street, map[string]any{"t": "obj_ev", "street": street, "id": ob.spot.id, "ev": "ate", "on": tree.spot.id}, 0)
	o.forget(street, tree)
}

// pigsToTree is onContainerItemStateChanged on a fruiting tree a player harvested, watered or petted
// (the game's animal-mood-feature onEvent): every hungry Piggy of the street on the tree's level and
// in reach that is not sad, rooked or already on its way to food goes for it.
func (o *objects) pigsToTree(street string, treeSpot *objSpot) {
	if o.sadOn(street) || o.rooked(street, treeSpot.id) {
		return
	}
	tree := o.get(street, treeSpot)
	alive := !tree.dead && tree.fruit > 0
	o.forget(street, tree)
	if !alive {
		return
	}
	o.wakePigs(street)
	ids := make([]string, 0)
	for id, spot := range o.spots(street) {
		if spot.kind == "pig" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var sent []*liveObj
	for _, id := range ids {
		ob := o.streets[street][id]
		if ob == nil || ob.hunger <= 0 || ob.trip != nil || math.Abs(ob.spot.y-treeSpot.y) > pigEatDY || o.pigRooked(street, ob) {
			continue
		}
		if math.Abs(o.pigX(street, ob)-treeSpot.x) > pigReachX {
			continue
		}
		o.startTrip(street, ob, treeSpot.x, treeSpot.id, "")
		sent = append(sent, ob)
	}
	if len(sent) > 0 {
		o.broadcast(street, 0, sent...)
		o.saveSoon()
	}
}

// tendPig is a player's verb on a street Piggy.
func (o *objects) tendPig(street string, ob *liveObj, m objAct, perks map[string]bool, user int64) verdict {
	if ob.pigGone() {
		return refuse("gone", pigTextGone) // G605 (objects_pigs_gone.go)
	}
	if m.Verb == "name" {
		return o.nameAnimal(street, ob, m, perks, user)
	}
	if o.sadOn(street) {
		return refuse("sad", pigSad)
	}
	today := o.today()
	rates := o.w.rules.Pig[m.Verb]
	rate, ok := rates[m.Skill]
	if !ok {
		rate = rates["no_skill"]
	}
	switch m.Verb {
	case "pet":
		// ak_piggy_pet: the success roll comes first (inc_skill_packages.js), then onPetComplete.
		roll := o.roll()
		success := roll*100 < rate
		if success {
			ob.mood = min(pigMoodMax, ob.mood+1)
			ob.petted = true
			if ob.petters == nil {
				ob.petters = map[int64]int64{}
			}
			ob.petters[user] = today
			o.hold(ob, creatureHold)
			return verdict{ok: true, extra: map[string]any{"roll": roll, "success": success}, ev: "pet"}
		}
		return verdict{ok: true, extra: map[string]any{"roll": roll, "success": success}}
	case "nibble":
		if ob.petters[user] != today && !perks["ak6"] {
			return refuse("needs_pet", pigNeedsPet)
		}
		if ob.mood == 0 || ob.hunger > pigNibbleMax {
			return refuse("not_in_mood", pigNotMood)
		}
		// Each player's nibbles of this Piggy a game day (ak_piggy_nibble interval_limit,
		// objects_creatures.go): the Meat it gives is credited, so the limit is the server's too.
		o.creatureDay(ob)
		limit, _, amount := o.akRow("npc_piggy:nibble", akRank(m.Skill))
		if ob.usesOf(user, "nibble") >= limit {
			return refuse("daily_limit", dailyLimitLine("nibble", "Piggy", limit))
		}
		roll := o.roll()
		v := verdict{ok: true, extra: map[string]any{"roll": roll, "success": roll*100 < rate}}
		if roll*100 < rate {
			ob.use(user, "nibble")
			v.got = creditItem(creatureItem["nibble"], amount)
			o.hold(ob, creatureHold)
			v.ev = "nibble"
		}
		return v
	case "feed":
		// js/animal-husbandry.js feedPiggyStack: base cost 5 or more, and hungry (npc_piggy.js 672-676).
		if perks["sate"] && ob.hunger != 0 {
			ob.hunger, ob.croak = 0, false
		}
		o.hold(ob, creatureHold)
		return verdict{ok: true, ev: "feed"}
	case "eat":
		if ob.hunger <= 0 {
			return refuse("not_hungry", pigNotHungry)
		}
		if m.On == "" {
			// G335 slice 5: food only this player sees is not the street's to believe in.
			return refuse("unseen", "That Piggy can't see any food there.")
		}
		var also []*liveObj
		{
			spot := o.spotOf(street, m.On)
			if spot == nil || spot.kind != "tree" || !strings.HasPrefix(m.On, "tree:") || math.Abs(spot.x-ob.spot.x) > pigReachX {
				return refuse("unknown", "That is not something here.")
			}
			tree := o.get(street, spot)
			if tree.dead || tree.fruit <= 0 || o.rooked(street, m.On) {
				o.forget(street, tree)
				return refuse("empty", notReady["tree"])
			}
			tree.fruit--
			also = append(also, tree)
		}
		ob.hunger, ob.croak = 0, false
		return verdict{ok: true, also: also}
	}
	return refuse("bad_verb", "You can't do that to a Piggy.")
}

// pigFoods is data/trade.json's food classes: whether each sates a hungry Piggy, and its stack size.
var pigFoods = func() map[string]tradeClass {
	var table tradeTable
	if err := json.Unmarshal(tradeJSON, &table); err != nil {
		panic(fmt.Sprintf("realtime: data/trade.json: %v", err))
	}
	foods := map[string]tradeClass{}
	for c, it := range table.Items {
		if it.Food {
			foods[c] = it
		}
	}
	return foods
}()

// askFeed is feeding a street Piggy when the site's ledger holds the food (G335 slice 5): the
// Piggy's own refusals first, then, when the food would sate it, the ledger takes the food before
// the Piggy is sated for everyone (obj_ok carries "use" with the op the page applies once).
func (o *objects) askFeed(cl *Client, m objAct, street string, spot *objSpot, perks map[string]bool) {
	ob := o.get(street, spot)
	before := o.state(ob)
	food, isFood := pigFoods[m.C]
	switch {
	case o.sadOn(street):
		o.finishAct(cl, m, street, ob, before, refuse("sad", pigSad))
		return
	case !isFood || m.N < 1 || m.N > food.Max:
		o.finishAct(cl, m, street, ob, before, refuse("not_food", "That Piggy doesn't want to eat that."))
		return
	case (!food.Sates || ob.hunger == 0) && o.plopSeed(m.C) == "":
		// Nothing but the feed itself changes: the food is the player's own to lose.
		o.hold(ob, creatureHold)
		o.finishAct(cl, m, street, ob, before, verdict{ok: true, ev: "feed"})
		return
	}
	// A crop always goes through the ledger, hungry or not: its count queues Plops for the street (G605).
	name := cl.Name()
	o.uses.ask(cl, m.C, m.N, func(use map[string]any, code, message string) {
		ob := o.get(street, spot)
		before := o.state(ob)
		if code != "" {
			o.finishAct(cl, m, street, ob, before, refuse(code, message))
			return
		}
		if food.Sates && ob.hunger != 0 {
			ob.hunger, ob.croak = 0, false
		}
		o.feedCrops(street, ob, m.C, m.N, cl.ID(), name)
		o.hold(ob, creatureHold)
		v := verdict{ok: true, ev: "feed"}
		if use != nil {
			v.extra = map[string]any{"use": use}
		}
		o.finishAct(cl, m, street, ob, before, v)
	})
}
