package main

// Plops from a street Piggy (G605, Austin 2026-11-09), part of the shared street Piggies
// (objects_pigs.go). In the source the crops fed to a Piggy are counted on the one npc_piggy item,
// whoever feeds it (npc_piggy.js feed 685-693: crops[produced_by] += count; a pumpkin or 3 of a seed
// class queue Plops, the count reset), and each Plop lies on the ground for everyone with its seeds
// (doPlop 1315-1350: randInt(2,7) seeds of the queue's head, capped by what is left, one every 5 s,
// placed 30 px behind the Piggy; plop.js instanceProps seed_class, seed_count). Here:
//
//   - A crop fed to a street Piggy always goes through the site's ledger (askFeed), hungry or not, so
//     the server knows the count it queues Plops from (the game's js/animal-husbandry.js
//     feedPiggyStack, number for number: pumpkin count x 2, else round(count / 3)).
//   - The server rolls each Plop's seeds and places it with drops.go place, under the feeder (ours:
//     the feeder has the 100 px guard first, Austin 2026-11-09; the source's Plop had no dropper),
//     and the street hears obj_ev "plop" (the feeder's activity line, doPlop 1347-1349).
//   - Its seeds travel as the Plop's own words: the one-copy stack's "t", copy tag kind "plop",
//     {"seed":"seed_potato","n":5} (copytags.go, the game's js/copy-tags.js, the site's
//     CopyTags.php: seed one of the crop seed classes, n 1 to 7), so a pickup gives the picker that
//     Plop's seeds, through the ledger as any stack, and a Plop dropped from a pack takes its seeds
//     down with it.
//   - A gone Piggy (objects_pigs_gone.go) drops the Plops it had still to come.

import (
	"encoding/json"
	"log"
	"math"
	"time"
)

const (
	plopEvery    = 5 * time.Second // doPlop 1327: apiSetTimerX('doPlop', 5*1000)
	plopSeedsMin = 2               // 1321: randInt(2, 7)
	plopSeedsMax = 7
	plopCrops    = 3   // 690: this.crops[produced_by] >= 3
	plopBehind   = 30  // 1341-1345: x-30 walking right, else x+30
	plopQueueMax = 200 // ours: a queue that never grows without end
)

// plopEntry is one queued Plop run (doPlop's plopping row) and the feeder who made it.
type plopEntry struct {
	Seed string `json:"seed"`
	Num  int    `json:"num"`
	By   int64  `json:"by"`
	Name string `json:"name,omitempty"`
}

// plopSeed is the seed class a fed crop is counted toward (crop_base produced_by_class), or "".
func (o *objects) plopSeed(class string) string {
	return cropSeeds[class]
}

// cropSeeds is data/trade.json's crops: each one's seed class (crop_base produced_by_class), from
// the game's js/animal-husbandry.js PIGGY_CROP_SEEDS (tools/build-realtime-trade.mjs "seed").
var cropSeeds = func() map[string]string {
	out := map[string]string{}
	var table tradeTable
	if err := json.Unmarshal(tradeJSON, &table); err != nil {
		panic("realtime: data/trade.json: " + err.Error())
	}
	for c, it := range table.Items {
		if it.Seed != "" {
			out[c] = it.Seed
		}
	}
	return out
}()

// feedCrops counts a crop fed to a street Piggy and queues its Plops (feed 685-729).
func (o *objects) feedCrops(street string, ob *liveObj, class string, n int, by int64, name string) {
	seed := o.plopSeed(class)
	if seed == "" || n < 1 || ob.pigGone() {
		return
	}
	if ob.crops == nil {
		ob.crops = map[string]int{}
	}
	ob.crops[seed] += n
	if seed != "seed_pumpkin" && ob.crops[seed] < plopCrops {
		return
	}
	ob.crops[seed] = 0
	num := int(math.Round(float64(n) / 3))
	if seed == "seed_pumpkin" {
		num = n * 2
	}
	if num < 1 || len(ob.plops) >= plopQueueMax {
		return
	}
	ob.plops = append(ob.plops, plopEntry{Seed: seed, Num: num, By: by, Name: name})
	if ob.plopStop == nil {
		o.doPlop(street, ob) // 727: doPlop(pc) at once, then every 5 s
	}
}

// doPlop takes one Plop off the Piggy's queue and places it (doPlop 1315-1350).
func (o *objects) doPlop(street string, ob *liveObj) {
	ob.plopStop = nil
	if len(ob.plops) == 0 || ob.pigGone() {
		return
	}
	head := &ob.plops[0]
	count := min(o.randInt(plopSeedsMin, plopSeedsMax), head.Num)
	e := *head
	if head.Num > count {
		head.Num -= count
	} else {
		ob.plops = ob.plops[1:]
	}
	x := o.pigX(street, ob)
	if x != x {
		x = ob.spot.x
	}
	// Behind it: the source's go_dir, here the side away from its spot's middle (its walk turns there).
	if x >= ob.spot.x {
		x += plopBehind
	} else {
		x -= plopBehind
	}
	tag := []byte(`{"seed":"` + e.Seed + `","n":` + itoa(count) + `}`)
	placed := false
	if place, ok := o.h.ProvidedLocked("drops.place").(func(string, int64, string, float64, float64, tradeItem) bool); ok {
		placed = place(street, e.By, e.Name, x, ob.spot.y-1, tradeItem{C: "plop", N: 1, T: tag})
	}
	if placed {
		o.h.BroadcastStreetLocked(street, map[string]any{"t": "obj_ev", "street": street, "id": ob.spot.id, "ev": "plop", "by": e.By, "seed": e.Seed, "n": count}, 0)
	} else {
		log.Printf("objects: %s on %s could not place a Plop (%s x%d) for %d", ob.spot.id, street, e.Seed, count, e.By)
	}
	if len(ob.plops) > 0 {
		ob.plopStop = o.h.After(plopEvery, func() {
			o.doPlop(street, ob)
			o.saveSoon()
		})
	}
	o.saveSoon()
}

// plopsFromRecord is a Piggy's crop counts and queue as the state file kept them, checked.
func (o *objects) plopsFromRecord(crops map[string]int, plops []plopEntry) (map[string]int, []plopEntry) {
	var c map[string]int
	for seed, n := range crops {
		if n > 0 && n < plopCrops && seedClassKnown(seed) {
			if c == nil {
				c = map[string]int{}
			}
			c[seed] = n
		}
	}
	var q []plopEntry
	for _, e := range plops {
		if e.Num > 0 && e.By > 0 && seedClassKnown(e.Seed) && len(q) < plopQueueMax {
			q = append(q, e)
		}
	}
	return c, q
}

// resumePlops arms a Piggy's next Plop after a restart (its queue was kept).
func (o *objects) resumePlops(street string, ob *liveObj) {
	if len(ob.plops) > 0 && ob.plopStop == nil && !ob.pigGone() {
		ob.plopStop = o.h.After(plopEvery, func() {
			o.doPlop(street, ob)
			o.saveSoon()
		})
	}
}

// seedClassKnown: a seed class some crop is counted toward.
func seedClassKnown(seed string) bool {
	if seed == "" {
		return false
	}
	for _, s := range cropSeeds {
		if s == seed {
			return true
		}
	}
	return false
}

// stopTrip ends a Piggy's walk to food and its timer (a gone Piggy walks nowhere), and its Plops' timer.
func (ob *liveObj) stopTrip() {
	if ob.tripStop != nil {
		ob.tripStop()
		ob.tripStop = nil
	}
	if ob.plopStop != nil {
		ob.plopStop()
		ob.plopStop = nil
	}
	ob.trip, ob.eatingFood = nil, ""
}
