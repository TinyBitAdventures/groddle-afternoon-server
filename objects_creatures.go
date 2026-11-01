package main

// A street's Chickens, Butterflies and Batterflies, part of the shared street objects (objects.go):
// in the original one npc_chicken, npc_butterfly or npc_batterfly item on the street was the same
// animal for everyone there, so where it is, what it is doing and what it remembers are shared.
// Ids are the client's spawn index on the street (data/objects.json from the game's
// tools/build-realtime-objects.mjs, the same index the Rook's keys use): chicken:<i> (game.js
// spawnChickens), butterfly:<i> (spawnButterflies: all flying since ticket 25; a resting spot still holds),
// batterfly:<i> (animal-husbandry-feature, batterfly:<i>).
//
// Where they are (the remake's own paths, shared): every client draws each animal on the same path
// from the same clock, so the server never moves them and an animal nobody touched costs nothing.
// A Chicken walks to and fro across its spot, a Butterfly rests or flies across the street, a
// Batterfly loops low over its spot (the game's js/street-creatures.js has the paths). What the
// server keeps is when the path stood still: "lag", the ms the animal has spent held, and "hold",
// the ms it holds still from now, so path time is (now - lag), frozen while held. A verb that lands
// holds the animal for everyone, as the source stops it for the player at it (npc_chicken.js
// squeeze 774 stopMoving, onWaitStart 1630-1639; npc_butterfly.js onWaitStart 1513-1531, it hovers
// by the one player it waits for; npc_batterfly.js onWaitStart 1074-1081), and a Chicken sitting
// on an egg holds until it hatches (incubate 642-650: state 'sit'). Every "objs" frame carries
// "now" (server Unix ms) and the snapshot "creatures":true, so a client knows this server shares
// them and sets its clock.
//
// Departures from the source, on purpose: the source's Chicken walks edge to edge at 45 px/s
// (onCreate 1008-1023, onPathing 1168-1214) and a Butterfly or Batterfly flies anywhere in a band
// over the whole street (flyAround 886-969; npc_batterfly.js 1039-1052); the remake keeps the
// paths its solo game always drew (a Chicken's 100 px patrol, the drifting flyers), now on a
// shared clock. Its pauses to peck (onPause 1216-1277) and its kicks into the air
// (onPlayerCollision 1299-1343) are each player's own decoration, not shared.
//
// What each animal remembers (shared) and what each player has (per player):
//   - Chicken (npc_chicken.js): one egg at a time, owned by the one player who asked (incubate
//     547-675: incubating, player_tsid, egg_class); while it sits it refuses squeeze ("What kind of
//     weirdo squeezes an incubating chicken?", 755-757), another egg ("Sorry, I've already got a bun
//     in the oven...", 605-613) and butter up (178-180, the client's). The egg is the ledger's to
//     take first (objects_uses.go, whole items), and at 60 s (onIncubationComplete 1049-1069) the
//     hatchling is credited to its owner if they are still on the street (else the egg is lost,
//     onPlayerExit 1365-1373), through credits.go ("take"). Twins (894-937, the upgrades) are the
//     owner's client's roll and go through the budget.
//   - Squeeze, massage and milk: each player's uses of each animal a game day
//     (inc_skill_packages.js package_intervals[class][day][pc.tsid] 695-719; the limits by Animal
//     Kinship rank, catalog_skill_packages.js ak_chicken_squeeze 222-334, ak_butterfly_massage
//     2-113, ak_butterfly_milk 114-221, the game's js/animal-kinship.js). The server keeps them per
//     player and animal and refuses past the limit, with the source's line. A missed success roll
//     uses nothing (finishSkillPackage 439-479: the roll comes first). Milk needs this player's own
//     massage of this Butterfly today (milk handler 734-735, massagers[pc.tsid]), or Animal Kinship
//     VI. The item each gives (Grain, Butterfly Milk: the package's bonus_amount at the player's
//     rank) is credited ("take"), so the save shows it outside the earning budget; a super harvest
//     and the AK V+ drop are the client's rolls and go through the budget, as a rock's bonus does.
//   - What everyone sees: how many times anyone squeezed, massaged or milked it today ("sq", "ms",
//     "mk"; the source has no flag for it, but everyone on the street saw the verb's animation, so
//     the street hears obj_ev "squeeze" | "massage" | "milk" with "by").
//   - Batterfly (npc_batterfly.js): its food counter is everyone's (feed 789-824: count x base_cost
//     from every feeder, reset when Guano drops; 15/50/100 make 1/2/3 Guano, the bonus cards 50 -> 3
//     and 80 -> 5). The food is the ledger's to take first (objects_uses.go), the server decides the
//     Guano from the shared counter, and the feeder is credited it ("take"). Feed has no limit
//     (707-851). Its hourly Guano (onInterval 1064-1072) stays each player's own pile.
//   - Not kept: life points (npc_chicken.js 1436, npc_butterfly.js 1212: lost only on a home street
//     or when more than one of the class is there, 2000 and 400 of them), sing to (the singer's
//     iMG, once a day each) and the crowding moods, which the client runs as in solo.
//
// A street Piggy's nibble (objects_pigs.go) is held to the same per player daily limit
// (ak_piggy_nibble) and credits its Meat the same way.
//
// Client to server (obj_act, as objects.go): verbs squeeze (chicken), massage, milk (butterfly),
// incubate with "c":"chicken_egg" (chicken), feed with "c" and "n" (batterfly, perks "guano1" /
// "guano2" for the bonus cards). "skill" is the player's Animal Kinship rank (animalkinship_<n>).
//
// Server to client:
//
//	{"t":"objs","street":"GA5…","now":1730000000000,"objs":[{"id":"chicken:0","lag":3000,"hold":1200,"sq":2,
//	   "inc":{"egg":"chicken_egg","by":7,"in":41000}}]}
//	{"t":"obj_ok","req":4,"id":"butterfly:2","verb":"milk","roll":0.2,"success":true,"uses":1,"limit":1,…}
//	{"t":"obj_ok","req":5,"id":"batterfly:0","verb":"feed","guano":2,"use":{…},…}
//	{"t":"obj_ev","street":"GA5…","id":"chicken:0","ev":"squeeze","by":7}   (and "hatched", "lost")

import (
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	// creatureHold is how long a landed verb holds the animal still for everyone (the verb's
	// pose: the source stops it while the player is at it, onWaitStart).
	creatureHold = 2500 * time.Millisecond
	// creatureWait is how long one "wait" holds a Chicken or Butterfly while a player is at it; the
	// page sends it again while its menu or a verb stays open (creatureWaitEvery in the game), so it
	// walks on about a second after the player leaves it (npc_chicken.js onWaitEnd 1621-1628).
	creatureWait = 1200 * time.Millisecond
	// batterflyChew: the Batterfly's chew sheet, 120 frames at 30 fps (animal-husbandry-feature).
	batterflyChew  = 4 * time.Second
	creatureReachX = pigReachX
	creatureReachY = pigReachY
)

// Copy from the source (refusals; the daily limit line is js/animal-kinship.js quoteAnimalKinship).
const (
	chickenSitting    = "What kind of weirdo squeezes an incubating chicken?"                                        // npc_chicken.js 756
	chickenBusy       = "Sorry, I've already got a bun in the oven. Come back when I have free space for more eggs." // 609
	chickenNotEgg     = "Chickens incubate eggs. That isn't one."
	butterflyNoWarmup = "What? No warmup? Massage this Butterfly first." // js/animal-kinship.js (npc_butterfly.js 743)
	batterflyNotFood  = "That isn't something a Batterfly eats."
)

// creatureVerbs is which verbs each kind takes, and the Animal Kinship package behind each.
var creatureVerbs = map[string]map[string]string{
	"chicken":   {"squeeze": "npc_chicken:squeeze", "incubate": ""},
	"butterfly": {"massage": "npc_butterfly:massage", "milk": "npc_butterfly:milk"},
	"batterfly": {"feed": ""},
}

// creatureItem is what a landed verb gives (js/animal-kinship.js performAnimalKinship).
var creatureItem = map[string]string{"squeeze": "grain", "milk": "milk_butterfly", "nibble": "meat"}

var creatureLabel = map[string]string{"chicken": "Chicken", "butterfly": "Butterfly", "batterfly": "Batterfly", "pig": "Piggy"}

func isCreature(kind string) bool {
	return kind == "chicken" || kind == "butterfly" || kind == "batterfly"
}

// akRank is the Animal Kinship rank a skill string names (animalkinship_<n>, 0 for anything else).
func akRank(skill string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(skill, "animalkinship_"))
	if err != nil || !strings.HasPrefix(skill, "animalkinship_") || n < 0 || n > 7 {
		return 0
	}
	return n
}

// akRow is the package's [interval_limit, success_rate, bonus_amount] at a rank.
func (o *objects) akRow(pkg string, rank int) (limit int, rate float64, amount int) {
	rows := o.w.rules.Ak[pkg]
	if len(rows) == 0 {
		return 1, 100, 0
	}
	row := rows[min(rank, len(rows)-1)]
	return int(row[0]), row[1], int(row[2])
}

// rollDay forgets the uses and tallies of an earlier game day.
func (o *objects) creatureDay(ob *liveObj) {
	if today := o.today(); ob.usesDay != today {
		ob.usesDay, ob.uses, ob.tally = today, nil, nil
	}
}

func (ob *liveObj) usesOf(user int64, verb string) int {
	return ob.uses[user][verb]
}

func (ob *liveObj) use(user int64, verb string) {
	if ob.uses == nil {
		ob.uses = map[int64]map[string]int{}
	}
	if ob.uses[user] == nil {
		ob.uses[user] = map[string]int{}
	}
	ob.uses[user][verb]++
	if ob.tally == nil {
		ob.tally = map[string]int{}
	}
	ob.tally[verb]++
}

// hold stills the animal's path for d from now, for everyone (lag grows by the time held).
func (o *objects) hold(ob *liveObj, d time.Duration) {
	now := o.h.Now()
	end := now.Add(d)
	if ob.holdEnd.After(now) {
		if !end.After(ob.holdEnd) {
			return
		}
		ob.lag += end.Sub(ob.holdEnd).Milliseconds()
	} else {
		ob.lag += d.Milliseconds()
	}
	ob.holdEnd = end
}

// waitCreature is npc.js onInteractionStarting 1981-1990 and checkWaiting 1878-1900 for a shared
// Chicken or Butterfly: while any player has it open (its menu, a verb on it) it stands still for
// everyone (npc_chicken.js onWaitStart 1630-1639 apiStopMoving; npc_butterfly.js onWaitStart
// 1513-1531), not only on that player's screen. Each "wait" holds it creatureWait from now and the
// street hears the new hold. A Batterfly does not stop for a menu (its page never held it).
func (o *objects) waitCreature(street string, spot *objSpot) {
	ob := o.get(street, spot)
	before := ob.holdEnd
	o.hold(ob, creatureWait)
	if !ob.holdEnd.Equal(before) {
		o.broadcast(street, 0, ob)
	}
	o.forget(street, ob)
}

func (o *objects) creatureFresh(ob *liveObj) bool {
	return ob.lag == 0 && !ob.holdEnd.After(o.h.Now()) && ob.incEgg == "" && ob.food == 0 &&
		(ob.usesDay != o.today() || (len(ob.uses) == 0 && len(ob.tally) == 0))
}

func (o *objects) creatureState(ob *liveObj) map[string]any {
	now := o.h.Now()
	m := map[string]any{"id": ob.spot.id, "lag": ob.lag, "hold": int64(0)}
	if ob.holdEnd.After(now) {
		m["hold"] = ob.holdEnd.Sub(now).Milliseconds()
	}
	today := ob.usesDay == o.today()
	tally := func(key, verb string) {
		if today && ob.tally[verb] > 0 {
			m[key] = ob.tally[verb]
		}
	}
	switch ob.spot.kind {
	case "chicken":
		tally("sq", "squeeze")
		if ob.incEgg != "" {
			m["inc"] = map[string]any{"egg": ob.incEgg, "by": ob.incBy, "in": max(1, ob.incEnd.Sub(now).Milliseconds())}
		}
	case "butterfly":
		tally("ms", "massage")
		tally("mk", "milk")
	case "batterfly":
		m["food"] = ob.food
	}
	return m
}

// creatureReach is how far from its spot a player may act on it: a flyer crosses the whole street.
func creatureReach(spot *objSpot) (float64, float64) {
	if spot.fly {
		return math.Inf(1), creatureReachY
	}
	return creatureReachX, creatureReachY
}

// tendCreature decides squeeze, massage and milk (incubate and a Batterfly's feed ask the ledger
// first: askIncubate, askBatterfly).
func (o *objects) tendCreature(street string, ob *liveObj, m objAct, perks map[string]bool, user int64) verdict {
	kind := ob.spot.kind
	pkg, ok := creatureVerbs[kind][m.Verb]
	if !ok || pkg == "" {
		return refuse("bad_verb", "You can't do that to a "+creatureLabel[kind]+".")
	}
	o.creatureDay(ob)
	if kind == "chicken" && ob.incEgg != "" {
		return refuse("sitting", chickenSitting)
	}
	rank := akRank(m.Skill)
	limit, rate, amount := o.akRow(pkg, rank)
	if ob.usesOf(user, m.Verb) >= limit {
		return refuse("daily_limit", dailyLimitLine(m.Verb, creatureLabel[kind], limit))
	}
	if m.Verb == "milk" && rank < 6 && !perks["ak6"] && ob.usesOf(user, "massage") < 1 {
		return refuse("needs_massage", butterflyNoWarmup)
	}
	roll := o.roll()
	if roll*100 >= rate {
		return verdict{ok: true, extra: map[string]any{"roll": roll, "success": false}}
	}
	ob.use(user, m.Verb)
	o.hold(ob, creatureHold)
	v := verdict{ok: true, extra: map[string]any{"roll": roll, "success": true, "uses": ob.usesOf(user, m.Verb), "limit": limit}}
	if c := creatureItem[m.Verb]; c != "" {
		v.got = creditItem(c, amount)
	}
	v.ev = m.Verb
	return v
}

// dailyLimitLine is js/animal-kinship.js quoteAnimalKinship's (npc_chicken.js 763, npc_butterfly.js 709-718).
func dailyLimitLine(verb, label string, limit int) string {
	times := "times"
	if limit == 1 {
		times = "time"
	}
	return "You can only " + verb + " this " + label + " " + strconv.Itoa(limit) + " " + times + " per game day."
}

// askIncubate is "request incubation" on a Chicken: its refusals first, then the ledger takes the
// egg, then the Chicken sits on it for everyone until it hatches (npc_chicken.js 547-675).
func (o *objects) askIncubate(cl *Client, m objAct, street string, spot *objSpot) {
	ob := o.get(street, spot)
	before := o.state(ob)
	hatch, isEgg := o.w.rules.Incubate.Hatches[m.C]
	switch {
	case ob.incEgg != "":
		o.finishAct(cl, m, street, ob, before, refuse("busy", chickenBusy))
		return
	case !isEgg || hatch == "":
		o.finishAct(cl, m, street, ob, before, refuse("not_egg", chickenNotEgg))
		return
	}
	o.uses.ask(cl, m.C, 1, func(use map[string]any, code, message string) {
		ob := o.get(street, spot)
		before := o.state(ob)
		if code != "" {
			o.finishAct(cl, m, street, ob, before, refuse(code, message))
			return
		}
		if ob.incEgg != "" {
			// "Too slow! Someone else got to me first." (636-640): the egg the ledger took stays used,
			// as the source took it only once it sat; here the ledger already has, so it is the player's
			// loss only if the race is lost in the instant the ledger answered.
			o.finishAct(cl, m, street, ob, before, refuse("busy", "Too slow! Someone else got to me first."))
			return
		}
		o.startIncubation(street, ob, m.C, cl.ID(), time.Duration(o.w.rules.Incubate.Ms)*time.Millisecond)
		v := verdict{ok: true, ev: "incubate"}
		if use != nil {
			v.extra = map[string]any{"use": use}
		}
		o.finishAct(cl, m, street, ob, before, v)
	})
}

func (o *objects) startIncubation(street string, ob *liveObj, egg string, by int64, d time.Duration) {
	ob.incEgg, ob.incBy, ob.incEnd = egg, by, o.h.Now().Add(d)
	o.hold(ob, d)
	o.armIncubation(street, ob, d)
}

func (o *objects) armIncubation(street string, ob *liveObj, d time.Duration) {
	if ob.incStop != nil {
		ob.incStop()
	}
	ob.incStop = o.h.After(max(0, d), func() {
		ob.incStop = nil
		o.hatch(street, ob)
		o.saveSoon()
	})
}

// hatch is onIncubationComplete (npc_chicken.js 1049-1069): the owner still on the street gets the
// hatchling (credited), else the egg is lost (onPlayerExit 1365-1373).
func (o *objects) hatch(street string, ob *liveObj) {
	if ob.incEgg == "" {
		return
	}
	egg, by := ob.incEgg, ob.incBy
	ob.incEgg, ob.incBy, ob.incEnd = "", 0, time.Time{}
	ev := "lost"
	if cl := o.h.ClientLocked(by); cl != nil && cl.Online() && cl.Street() == street {
		ev = "hatched"
		creditLocked(o.h, by, "take", creditItem(o.w.rules.Incubate.Hatches[egg], 1))
	}
	o.broadcast(street, 0, ob)
	o.h.BroadcastStreetLocked(street, map[string]any{"t": "obj_ev", "street": street, "id": ob.spot.id, "ev": ev, "by": by, "c": egg}, 0)
	o.forget(street, ob)
}

// batterflyGuano is npc_batterfly.js feed 789-824: the Guano a food counter releases.
func batterflyGuano(counter int, perks map[string]bool) int {
	switch {
	case perks["guano2"] && counter >= 80:
		return 5
	case counter >= 100 || (perks["guano1"] && counter >= 50):
		return 3
	case counter >= 50:
		return 2
	case counter >= 15:
		return 1
	}
	return 0
}

// askBatterfly is feeding a Batterfly: the ledger takes the food, then the shared counter decides
// the Guano, which the feeder is credited.
func (o *objects) askBatterfly(cl *Client, m objAct, street string, spot *objSpot, perks map[string]bool) {
	ob := o.get(street, spot)
	before := o.state(ob)
	food, isFood := pigFoods[m.C]
	if !isFood || m.N < 1 || m.N > food.Max {
		o.finishAct(cl, m, street, ob, before, refuse("not_food", batterflyNotFood))
		return
	}
	o.uses.ask(cl, m.C, m.N, func(use map[string]any, code, message string) {
		ob := o.get(street, spot)
		before := o.state(ob)
		if code != "" {
			o.finishAct(cl, m, street, ob, before, refuse(code, message))
			return
		}
		ob.food += m.N * food.Cost
		guano := batterflyGuano(ob.food, perks)
		if guano > 0 {
			ob.food = 0
		}
		o.hold(ob, batterflyChew)
		v := verdict{ok: true, ev: "feed", got: creditItem("guano", guano), extra: map[string]any{"guano": guano}}
		if use != nil {
			v.extra["use"] = use
		}
		o.finishAct(cl, m, street, ob, before, v)
	})
}
