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
//     of (pigFood below); else one fruit from the nearest tree on its level with fruit
//     (onFoodPathing 1482-1533) at once, as the remake's replay of a tick while nobody watched does,
//     and the street hears "ate" so it can walk there.
//   - Food dropped for everyone (onContainerItemAdded 1393-1411): a hungry Piggy that is not sad goes
//     for it at once if it lies in reach (its move_limits; here pigReachX across, pigEatDY up or
//     down), and on arrival eats one of the stack (onFoodPathing 1489-1500: apiConsume(1), hunger 0,
//     "Yum!"). The walk takes the source's walk speed over the distance (npc_walk_speed 30-55,
//     onCreate 1442; pigWalkPx), and the stack may be picked up meanwhile, as in the source.
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
// A walk to food stays each page's own from the same event ("ate"), and then it walks back on.
// A nibble is held to each player's daily limit of this Piggy (ak_piggy_nibble interval_limit) and
// its Meat is credited to the ledger (credits.go "take").
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
	pigWalkMin    = time.Second
	pigWalkMax    = 20 * time.Second
)

// Copy from npc_piggy.js.
const (
	pigSad       = "Can't do that! Piggy is sad!."                         // 568-570, 836-838, 930-932
	pigNotMood   = "That Piggy doesn't feel like being nibbled right now." // 876
	pigNeedsPet  = "Give that Piggy some love first!"                      // 897
	pigNotHungry = "That Piggy is not hungry."
)

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
	spot := o.w.streets[street][m.ID]
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
	for _, spot := range o.w.streets[street] {
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
			if ob.spot.kind == "pig" && (due.IsZero() || ob.tickAt.Before(due)) {
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
			if ob.spot.kind == "pig" {
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
	if o.w.sad[street] {
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
	// Dropped food first (goForFood: the Piggy walks to it), else a tree's fruit.
	if scanned && ob.hunger > 0 && ob.eatingFood == "" && !o.goForFood(street, ob, "") {
		if tree := o.fruitNear(street, ob.spot); tree != nil {
			tree.fruit--
			ob.hunger, ob.croak = 0, false
			e := ev("ate")
			e["on"], e["tree"] = tree.spot.id, tree
			out = append(out, e)
		}
	}
	return out
}

// pigFood is onContainerItemAdded for a stack dropped for everyone (drops.go calls it once the
// site has it): the nearest hungry Piggy on the street that is not sad and has it in reach goes for it.
func (o *objects) pigFood(street, id string, x, y float64) {
	if o.w.sad[street] {
		return
	}
	var best *liveObj
	bestD := math.Inf(1)
	for _, spot := range o.w.streets[street] {
		if spot.kind != "pig" {
			continue
		}
		ob := o.streets[street][spot.id]
		if ob == nil || ob.hunger <= 0 || ob.eatingFood != "" || math.Abs(spot.y-y) > pigEatDY || math.Abs(spot.x-x) > pigReachX {
			continue
		}
		if d := math.Abs(spot.x - x); d < bestD || (d == bestD && spot.id < best.spot.id) {
			best, bestD = ob, d
		}
	}
	if best != nil {
		o.goForFood(street, best, id)
	}
}

// goForFood sends a Piggy for a stack on the ground (id, or the nearest food in reach when ""): it
// walks there, then eats one if the stack is still there (onFoodPathing). False when there is none.
func (o *objects) goForFood(street string, ob *liveObj, id string) bool {
	food, _ := o.h.ProvidedLocked("drops.food").(func(string, string, float64, float64, float64, float64) (string, float64))
	eat, _ := o.h.ProvidedLocked("drops.eat").(func(string, string, func(bool)))
	if food == nil || eat == nil {
		return false
	}
	id, at := food(street, id, ob.spot.x, ob.spot.y, pigReachX, pigEatDY)
	if id == "" {
		return false
	}
	walk := time.Duration(math.Abs(at-ob.spot.x) / pigWalkPx * float64(time.Second))
	walk = min(pigWalkMax, max(pigWalkMin, walk))
	ob.eatingFood = id
	o.h.After(walk, func() {
		if ob.eatingFood != id {
			return
		}
		eat(id, ob.spot.id, func(ok bool) {
			ob.eatingFood = ""
			if !ok || ob.hunger <= 0 {
				return
			}
			ob.hunger, ob.croak = 0, false
			o.broadcast(street, 0, ob)
			o.saveSoon()
		})
	})
	return true
}

// fruitNear is the nearest living, unrooked tree on the Piggy's level with fruit (onTwiceDaily's
// trant scan, then the pathing, js/animal-mood-feature.js findFoodFor), or nil.
func (o *objects) fruitNear(street string, pig *objSpot) *liveObj {
	var best *liveObj
	bestD := math.Inf(1)
	ids := make([]string, 0)
	for id, spot := range o.w.streets[street] {
		if spot.kind == "tree" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		spot := o.w.streets[street][id]
		if math.Abs(spot.y-pig.y) > pigEatDY || o.rooked(street, id) {
			continue
		}
		tree := o.get(street, spot)
		if tree.dead || tree.fruit <= 0 {
			o.forget(street, tree)
			continue
		}
		if d := math.Abs(spot.x - pig.x); d < bestD {
			best, bestD = tree, d
		}
	}
	return best
}

// tendPig is a player's verb on a street Piggy.
func (o *objects) tendPig(street string, ob *liveObj, m objAct, perks map[string]bool, user int64) verdict {
	if o.w.sad[street] {
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
			spot := o.w.streets[street][m.On]
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
	case o.w.sad[street]:
		o.finishAct(cl, m, street, ob, before, refuse("sad", pigSad))
		return
	case !isFood || m.N < 1 || m.N > food.Max:
		o.finishAct(cl, m, street, ob, before, refuse("not_food", "That Piggy doesn't want to eat that."))
		return
	case !food.Sates || ob.hunger == 0:
		// Nothing but the feed itself changes: the food is the player's own to lose.
		o.hold(ob, creatureHold)
		o.finishAct(cl, m, street, ob, before, verdict{ok: true, ev: "feed"})
		return
	}
	o.uses.ask(cl, m.C, m.N, func(use map[string]any, code, message string) {
		ob := o.get(street, spot)
		before := o.state(ob)
		if code != "" {
			o.finishAct(cl, m, street, ob, before, refuse(code, message))
			return
		}
		if ob.hunger != 0 {
			ob.hunger, ob.croak = 0, false
		}
		o.hold(ob, creatureHold)
		v := verdict{ok: true, ev: "feed"}
		if use != nil {
			v.extra = map[string]any{"use": use}
		}
		o.finishAct(cl, m, street, ob, before, v)
	})
}
