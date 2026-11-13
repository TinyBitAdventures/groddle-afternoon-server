package main

// The shared tree rules (G564, the plan's section 5): what a pet, a water, a harvest, a new game day,
// Fertilidust, the Rook and a Wood Tree's chop do to a tree, in one place, so a street tree
// (objects.go) and a tree grown in a shared Patch (patches.go) can never drift apart. Each works on a
// treeCore, the fields every living tree has; the module keeps the rest (its place, its poison, who
// harvested it). The amounts are data/objects.json's rules (the game's trants.json, trant-care.js and
// trants.js tables), the same for both.

// treeCore is a living tree's shared state: a trant's fruit, capacity, health and maturity, and its
// tending window (the game day its flags belong to). A Wood Tree uses maturity, petted and watered
// (been_petted / been_watered, cleared by its woodCare clock) and day (its harvesters' game day).
type treeCore struct {
	fruit, cap int
	health     int
	maturity   int
	petted     bool
	watered    bool
	day        int64
}

// treeFruitCycle is include/trants.js onFruitCycle 1043-1054 on the tree, as plusHealth runs it:
// the capacity for its maturity and health (updateFruitCap 1026-1041), then that fruitProdMap cell
// pushed back on, never past the capacity (pushFruit 466-473). The fruit it added.
func (w *objWorld) treeFruitCycle(t *treeCore, class string) int {
	t.cap = w.capAt(class, t.maturity, t.health)
	before := min(t.fruit, t.cap)
	t.fruit = min(t.cap, before+w.prodAt(class, t.maturity, t.health))
	return t.fruit - before
}

// treeHarvest is a trant's harvest (js/trants.js canHarvestTrant; include/trants.js performHarvest
// 234-237): no maturity gate, the fruit first (trant_fruit.js harvest conditions 496-503: a young
// tree has none, fruitCapMap is 0 below maturity 4), then catalog_skill_packages.js
// gardening_harvest bonus_amount by Arborology rank, at most what is there (removeFruit 447-461). A
// super harvest's bonus never comes off the tree (trants.js harvestTrant), so it stays the client's.
func (w *objWorld) treeHarvest(t *treeCore, class, skill string) verdict {
	if t.fruit <= 0 {
		return refuse("empty", treeNoFruit)
	}
	amount, ok := w.rules.Harvest[skill]
	if !ok {
		amount = w.rules.Harvest["no_skill"]
	}
	take := min(amount, t.fruit)
	t.fruit -= take
	return verdict{ok: true, extra: map[string]any{"take": take}, got: creditItem(objFruit[class], take)}
}

// treeNoFruit is trant_fruit.js 497, the harvest verb's refusal on a tree with no fruit (js/trants.js
// canHarvestTrant says the same).
const treeNoFruit = "There's nothing to pick, cowboy!"

// treeCare is a trant's pet or water (trant-care.js performTrantCare): once a window each; the
// success roll comes first (light_green_thumb_pet / _water success_rate by rank). On success, both
// together lift health once, and that plusHealth (697-704) regrows the fruit (include/trants.js
// performTending 670-684, onFruitCycle 1043-1054).
func (w *objWorld) treeCare(t *treeCore, class, verb, skill string, rollFn func() float64) verdict {
	if verb == "pet" && t.petted {
		return refuse("not_wanted", "This tree was already petted today.")
	}
	if verb == "water" && t.watered {
		return refuse("not_wanted", "This tree was already watered today.")
	}
	rates := w.rules.Care[verb]
	rate, ok := rates[skill]
	if !ok {
		rate = rates["no_skill"]
	}
	roll := rollFn()
	success := roll*100 < rate
	if !success {
		return verdict{ok: true, extra: map[string]any{"roll": roll, "success": success}}
	}
	if verb == "pet" {
		t.petted = true
	} else {
		t.watered = true
	}
	grew := 0
	if t.petted && t.watered {
		t.health = min(10, max(1, t.health+1))
		grew = w.treeFruitCycle(t, class)
	} else {
		t.cap = w.capAt(class, t.maturity, t.health)
		t.fruit = min(t.fruit, t.cap)
	}
	return verdict{ok: true, extra: map[string]any{"roll": roll, "success": success, "grew": grew}}
}

// treeNewDay is game.js resetAllTrantsForNewDay for one trant: the tending flags clear, a new window
// to pet and water it. The fruit stays as it is: GSJS never refills trees at the day turn (fruit
// comes back from tending and Fertilidust: treeFruitCycle). False when the day had not turned.
func (w *objWorld) treeNewDay(t *treeCore, class string, today int64) bool {
	if t.day >= today {
		return false
	}
	t.day = today
	t.petted, t.watered = false, false
	t.cap = w.capAt(class, t.maturity, t.health)
	t.fruit = min(t.fruit, t.cap)
	return true
}

// treeDust is a trant's onFertilidust (include/trants.js 1236-1244): one maturity below 10
// (plusMaturity 785-790), health 9 and then plusHealth, which lifts it to 10 and regrows its fruit.
// True when the tree changed.
func (w *objWorld) treeDust(t *treeCore, class string) bool {
	before := [3]int{t.maturity, t.health, t.fruit}
	if t.maturity < min(10, len(w.rules.Trants[class])) {
		t.maturity++
	}
	t.health = 10
	w.treeFruitCycle(t, class)
	return before != [3]int{t.maturity, t.health, t.fruit}
}

// Wood Tree care (items/wood_tree.js): each player may pet a Wood Tree, and water it, once every 5
// minutes (canPet 614-626, canWater 628-640), and its been_petted / been_watered marks clear 10
// minutes after a tending finds both set (onTendComplete 1002-1003, onNeedsReset 904-909). The same
// numbers as the game's js/woodcutting.js WOOD_TREE_CARE.
const (
	woodCareEveryMs  = 5 * 60 * 1000
	woodNeedsResetMs = 10 * 60 * 1000
)

// woodCare is a Wood Tree's care beside its treeCore: petted_count and watered_count toward the next
// maturity, when the marks clear (0: not set), and each player's last pet and water (pet_intervals,
// water_intervals; Unix ms). One for a street Wood Tree (objects.go) and one for a Wood Tree grown
// in a shared Patch (patches.go), so the two can never drift apart.
type woodCare struct {
	pets, waters int
	resetAt      int64
	petAt        map[int64]int64
	waterAt      map[int64]int64
}

// woodCareWait is the ms before who may pet (or water) the tree again; 0 when they may.
func woodCareWait(c *woodCare, verb string, who, now int64) int64 {
	at := c.petAt[who]
	if verb == "water" {
		at = c.waterAt[who]
	}
	if at <= 0 {
		return 0
	}
	if left := at + woodCareEveryMs - now; left > 0 && left <= woodCareEveryMs {
		return left
	}
	return 0
}

// woodCareExpire clears the marks once their 10 minutes are up (onNeedsReset). True when it did.
func woodCareExpire(t *treeCore, c *woodCare, now int64) bool {
	if c.resetAt <= 0 || now < c.resetAt {
		return false
	}
	c.resetAt = 0
	t.petted, t.watered = false, false
	return true
}

// woodCareFresh is a Wood Tree nobody has tended (or whose tending has all lapsed).
func woodCareFresh(t *treeCore, c *woodCare) bool {
	return c.pets == 0 && c.waters == 0 && c.resetAt == 0 && !t.petted && !t.watered && len(c.petAt) == 0 && len(c.waterAt) == 0
}

// woodTend is a Wood Tree's pet or water (wood_tree.js verbs pet / water, performTending 1051-1088,
// onTendComplete 955-1010), the success rate by Light Green Thumb rank (rates: catalog_skill_packages.js
// wood_tree_pet / wood_tree_water). Refused within who's 5 minutes: "I don't want to be petted right
// now!" (525, 564). A failed roll changes nothing (the failure branch sets no interval). A success
// counts (981, 992), marks the tree and starts who's clock (987-988, 997-998); once both marks are
// set they clear 10 minutes on, and both counts at maturityMap[m-1] (need, 889-896) grow it one
// maturity and start the counts over (1005-1007, plusMaturity 1091-1095). Extra: "roll", "success",
// and "matured" with the new "maturity" when it grew.
func woodTend(t *treeCore, c *woodCare, count *int, verb string, who, now int64, rate, roll float64, need []int) verdict {
	if verb != "pet" && verb != "water" {
		return refuse("bad_verb", "You can't do that to a Wood Tree here.")
	}
	if t.maturity <= 0 {
		return refuse("cleared", "This Wood Tree has been cleared.")
	}
	if woodCareWait(c, verb, who, now) > 0 {
		word := "petted"
		if verb == "water" {
			word = "watered"
		}
		return refuse("not_wanted", "I don't want to be "+word+" right now!")
	}
	success := roll*100 < rate
	extra := map[string]any{"roll": roll, "success": success}
	if !success {
		return verdict{ok: true, extra: extra}
	}
	woodCareExpire(t, c, now)
	for _, m := range []map[int64]int64{c.petAt, c.waterAt} {
		for id, at := range m {
			if now-at > woodCareEveryMs {
				delete(m, id) // cleanupPlayerIntervals 642-653
			}
		}
	}
	if verb == "pet" {
		c.pets++
		t.petted = true
		if c.petAt == nil {
			c.petAt = map[int64]int64{}
		}
		c.petAt[who] = now
	} else {
		c.waters++
		t.watered = true
		if c.waterAt == nil {
			c.waterAt = map[int64]int64{}
		}
		c.waterAt[who] = now
	}
	if t.petted && t.watered {
		c.resetAt = now + woodNeedsResetMs
		if t.maturity < 6 && t.maturity-1 < len(need) {
			if n := need[t.maturity-1]; c.pets >= n && c.waters >= n {
				t.maturity++
				*count, c.pets, c.waters = 0, 0, 0
				extra["matured"], extra["maturity"] = true, t.maturity
			}
		}
	}
	return verdict{ok: true, extra: extra}
}

// woodDust is a Wood Tree's onFertilidust (wood_tree.js 870-874): one maturity below 6, through
// plusMaturity (1091-1095), which starts harvest_count and both care counts over. A Wood Tree
// already cleared (onDie: gone in the source) takes nothing. True when it grew.
func woodDust(t *treeCore, care *woodCare, count *int) bool {
	if t.maturity <= 0 || t.maturity >= 6 {
		return false
	}
	t.maturity++
	*count, care.pets, care.waters = 0, 0, 0
	return true
}

// treeRook is items/include/trants.js onRooked 1140-1151 on a trant: every fruit gone, health down
// 2 (1 at health 2). True when the tree dies instead (already at health 1, as in the source).
func (w *objWorld) treeRook(t *treeCore, class string) (dies bool) {
	if t.health <= 1 {
		return true
	}
	t.fruit = 0
	loss := 2
	if t.health == 2 {
		loss = 1
	}
	t.health = max(1, t.health-loss)
	t.cap = w.capAt(class, t.maturity, t.health)
	return false
}

// woodChop is a Wood Tree's harvest (wood_tree.js 722-788): once a game day per player
// (intervals[today][pc]: by), planks by maturity (plankProdMap 880, endHarvest 766). A mature tree
// loses one maturity every second harvest (harvest_count), and a wee tree (maturity 2 or less) loses
// one each time; at 0 it is cleared (onDie); one it survives starts its care counts over. The
// planks, energy and Hatchet wear are the harvester's own (js/woodcutting.js harvestWoodTree).
func woodChop(t *treeCore, care *woodCare, count *int, by *map[int64]bool, harvester int64) verdict {
	if t.maturity <= 0 {
		return refuse("cleared", "This Wood Tree has been cleared.")
	}
	if (*by)[harvester] {
		return refuse("daily_limit", "You can only harvest from this tree once per game day.")
	}
	if *by == nil {
		*by = map[int64]bool{}
	}
	(*by)[harvester] = true
	got := creditItem("plank", woodPlanks[min(max(t.maturity, 1), 6)-1])
	if t.maturity <= 2 || *count != 0 {
		t.maturity--
		*count = 0
	} else {
		*count = 1
	}
	if t.maturity > 0 {
		// endHarvest 783-786: a tree that lives on starts its care counts over.
		care.pets, care.waters = 0, 0
	}
	return verdict{ok: true, got: got}
}
