package main

// The shared tree rules (G564, the plan's section 5): what a pet, a water, a harvest, a new game day,
// Fertilidust, the Rook and a Wood Tree's chop do to a tree, in one place, so a street tree
// (objects.go) and a tree grown in a shared Patch (patches.go) can never drift apart. Each works on a
// treeCore, the fields every living tree has; the module keeps the rest (its place, its poison, who
// harvested it). The amounts are data/objects.json's rules (the game's trants.json, trant-care.js and
// trants.js tables), the same for both.

// treeCore is a living tree's shared state: a trant's fruit, capacity, health and maturity, and its
// tending window (the game day its flags belong to). A Wood Tree uses maturity, petted, watered and day.
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

// treeHarvest is a trant's harvest (js/trants.js canHarvestTrant: too small below maturity 3;
// include/trants.js performHarvest 234-237): catalog_skill_packages.js gardening_harvest
// bonus_amount by Arborology rank, at most what is there (removeFruit 447-461). A super harvest's
// bonus never comes off the tree (trants.js harvestTrant), so it stays the client's.
func (w *objWorld) treeHarvest(t *treeCore, class, skill string) verdict {
	if t.maturity < 3 {
		return refuse("small", "This tree is too small to harvest.")
	}
	if t.fruit <= 0 {
		return refuse("empty", notReady["tree"])
	}
	amount, ok := w.rules.Harvest[skill]
	if !ok {
		amount = w.rules.Harvest["no_skill"]
	}
	take := min(amount, t.fruit)
	t.fruit -= take
	return verdict{ok: true, extra: map[string]any{"take": take}, got: creditItem(objFruit[class], take)}
}

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

// woodDust is a Wood Tree's onFertilidust (wood_tree.js 870-874): one maturity below 6. A Wood Tree
// already cleared (onDie: gone in the source) takes nothing. True when it grew.
func woodDust(t *treeCore) bool {
	if t.maturity <= 0 || t.maturity >= 6 {
		return false
	}
	t.maturity++
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
// one each time; at 0 it is cleared (onDie). The planks, energy and Hatchet wear are the harvester's
// own (js/woodcutting.js harvestWoodTree).
func woodChop(t *treeCore, count *int, by *map[int64]bool, harvester int64) verdict {
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
	return verdict{ok: true, got: got}
}
