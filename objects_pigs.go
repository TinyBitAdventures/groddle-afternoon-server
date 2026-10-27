package main

// Piggies on a street (G334), part of the shared street objects (objects.go): a street Piggy's
// mood and hunger are everyone's, as in the original, where one npc_piggy item on the street
// carried them for every player there. The server holds each street Piggy (pig:<i>, the client's
// spawnPiggies index, laid out by tools/build-realtime-objects.mjs) from the first time anyone is
// on its street, runs its twice-daily tick, and decides pet, nibble, feed and a Piggy eating.
// Each player's own side stays theirs, as in solo: the Animal Kinship daily limits and energy, the
// Meat, iMG and Plops a verb gives, and the food they spend.
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
//     food: the server sees no dropped stacks (each player's own), so it eats one fruit from the
//     nearest tree on its level with fruit (onFoodPathing 1482-1533) at once, as the remake's replay
//     of a tick while nobody watched does, and the street hears "ate" so it can walk there.
//     Petters from an earlier game day are forgotten (1857-1867).
//   - A sad Piggy (a depressing street, include/animal_sadness.js) skips its tick and refuses.
//   - pet (onPetComplete 1709-1735): the ak_piggy_pet success roll by rank; on success mood +1
//     (incMood, at most 2), has_been_petted, and the petter is remembered for today.
//   - nibble (handler 862-910): petted by this player today (or Animal Kinship VI, the "ak6" perk)
//     and the Piggy's mood 0 or hunger over 2: "That Piggy doesn't feel like being nibbled right
//     now."; not petted today: "Give that Piggy some love first!"; else the ak_piggy_nibble roll.
//   - feed (feed handler 560-750): food of base cost 5 or more sates a hungry Piggy (the client
//     says so with the "sate" perk: the food and its cost are its own); eat: one of the player's own
//     dropped stacks, or a tree's fruit ("on"), eaten where only that player sees the food.
// Chickens and Butterflies have no mood or hunger in the source (npc_chicken.js, npc_butterfly.js);
// what they remember is per player (massagers[pc.tsid], the squeeze and milk limits), so it stays in
// each player's save, as in solo.
// A street Piggy outlives a restart of the server (objects_persist.go, -objects-file): its mood,
// hunger, petters and clock are kept, and the ticks it missed while the server was down run when
// it is back.

import (
	"encoding/json"
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
	if scanned && ob.hunger > 0 {
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
		}
		return verdict{ok: true, extra: map[string]any{"roll": roll, "success": success}}
	case "nibble":
		if ob.petters[user] != today && !perks["ak6"] {
			return refuse("needs_pet", pigNeedsPet)
		}
		if ob.mood == 0 || ob.hunger > pigNibbleMax {
			return refuse("not_in_mood", pigNotMood)
		}
		roll := o.roll()
		return verdict{ok: true, extra: map[string]any{"roll": roll, "success": roll*100 < rate}}
	case "feed":
		// js/animal-husbandry.js feedPiggyStack: base cost 5 or more, and hungry (npc_piggy.js 672-676).
		if perks["sate"] && ob.hunger != 0 {
			ob.hunger, ob.croak = 0, false
		}
		return verdict{ok: true}
	case "eat":
		if ob.hunger <= 0 {
			return refuse("not_hungry", pigNotHungry)
		}
		var also []*liveObj
		if m.On != "" {
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
