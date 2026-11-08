package main

// Shared street Patches (register G564, slices 0 to 3): a street Patch, the Seedling planted in it,
// the tree it grows into and the dead tree that tree leaves are the same for everyone on the street,
// as in the original, where a Patch was an item in the location (items/patch.js, patch_dark.js,
// patch_seedling.js, include/trants.js, wood_tree.js, trant_*_dead.js). The server keeps each plot
// from weeds to grown tree to dead tree and back, grows it and decides every contested act. Home
// Patches, solo play and private places stay the player's own (js/patches.js, patches-feature.js).
// The plan: research/plans/g564-shared-street-patches.md. The wire format is also in the session's
// patches/wire.md, which the game lane builds against.
//
// Client to server:
//
//	{"t":"patch_sync"}                                               after a join: this street's plots, please
//	{"t":"patch_act","req":1,"id":"patch:0","verb":"tend","do":"start"}   the tend's bar begins: the one-tender
//	                                                                  lock, 10 s (patch.js 408-409, onTendTimeout)
//	{"t":"patch_act","req":2,"id":"patch:0","verb":"tend","do":"stop"}    cancelled: the lock goes back
//	{"t":"patch_act","req":3,"id":"patch:0","verb":"tend"}           the tend landed (the page's roll passed)
//	{"t":"patch_act","req":4,"id":"patch:0","verb":"dig","do":"start"}    a dig begins (several may dig; "stop")
//	{"t":"patch_act","req":5,"id":"patch:0","verb":"dig","skill":"soil_appreciation_2","drop":true}
//	                                                                  the dig landed ("drop": the page's Loam
//	                                                                  drop rolled, so no Earth receipt)
//	{"t":"patch_act","req":6,"id":"patch:0","verb":"plant","bean":"bean_spice","grow":3}
//	                                                                  "grow": the planter's bean_grow card
//	                                                                  level (0, 3 or 4)
//	{"t":"patch_act","req":7,"id":"patch:0","verb":"pet"}            a Seedling, or a grown tree ("skill")
//	{"t":"patch_act","req":8,"id":"patch:0","verb":"water","skill":"light_green_thumb_2"}   a grown tree
//	{"t":"patch_act","req":9,"id":"patch:0","verb":"harvest","skill":"gardening_3"}         a grown tree
//	{"t":"patch_act","req":10,"id":"patch:0","verb":"poison"}        a Tree Poison pour landed ("antidote")
//	                                                                  ("ms": the die time, dev only)
//	{"t":"patch_act","req":11,"id":"trant_spice_dead:0","verb":"clear"}   a dead tree's clear landed
//	{"t":"patch_act","req":12,"id":"patch:0","verb":"trantsform","k":0}   a Trantsformation Fluid pour
//	                                                                  landed ("k": the bottle, its place among
//	                                                                  the player's bottles in the pack)
//	Fertilidust (and Lite) on Patch trees is objects.go's obj_dust, which asks this module (below).
//	{"t":"patch_ack","id":"pg-…"}                                    a grant's items are in the saved pack
//	{"t":"patch_dev","clean":5000,"dug":5000,"min":5000,"max":60000,"cycle":5000,"wood":1000,"roll":0,"day":true}
//	                                                                  QA only, GRODDLE_PATCHES_DEV=1
//
// Server to client:
//
//	{"t":"patches","street":"GM4…","snap":true,"now":ms,"timers":{…},"plots":{"patch:0":Plot}}
//	        the answer to patch_sync: every plot that differs from its fresh start (a weedy Patch, or
//	        for a trant_*_dead:<n> key that dead tree), and every tree:<i> plot. "none":true: nothing is
//	        shared on this street (the page keeps its own). "timers": clean, dug, min, max, cycle1,
//	        cycle, wood (ms, the rules in force)
//	{"t":"patches","street":"GM4…","now":ms,"plots":{…}}           a change, a timer, a new tree:<i> plot
//	{"t":"patch_ok","req":3,"id":"patch:0","verb":"tend","now":ms,"before":Plot,"plot":Plot,…extra}
//	        tend start "lock"; dig start "diggers"; dig, harvest, clear "grant":{"id","c","n"}; plant,
//	        poison, antidote "use" (the ledger's take, applied once); Seedling pet "petted","first",
//	        "third","owner","grew"; tree pet/water "roll","success","grew" (a Wood Tree "matured");
//	        harvest "take","harvests","limit"; poison "poison" (ms); trantsform "use","from","to"
//	{"t":"patch_no","req":3,"id":"patch:0","verb":"tend","code":"busy","message":"…","now":ms,"plot":Plot}
//	{"t":"patch_grant","id":"pg-…","c":"plank","n":25}             on connect: a grant not acknowledged yet
//	{"t":"patch_ev","street":"GM4…","by":7,"id":"patch:0","verb":"tend","do":"start"}
//	        to the others: someone began, stopped ("stop") or finished (no "do") tending or digging
//	{"t":"patch_dev_ok","timers":{…},"roll":-1}
//
// Plot (only what applies): {"class":"patch","kind":"patch","stage":"dirty|clean|dug|seedling|tree|dead",
// "since":ms,"due":ms,"tender":7,"lockLeft":ms,"diggers":[7],"x":…,"y":… (tree:<i> only),
// "seedling":{"tree","bean","plantedAt","minAt","maxAt","petted","planter","grow","thirdAt"},
// "tree":{"class","maturity","health","fruit","cap","petted","watered","grownAt","cycleAt","count",
// "woodPetted","woodWatered","clearedAt","poison":ms left,"by"},"dead":"trant_spice_dead"}.
//
// Plot ids are the page's own keys: `<soilClass>:<n>` (patches-feature soilSpawnPlan) and `tree:<i>`,
// the plot a dead shared street tree left (objects.go killTree -> patches.adopt). Refusal codes:
// slow_down, unknown, far, busy, not_messy, planted, dug, weeds, hole, wrong_bean, no_item, no_answer,
// enough, small, weak, empty, daily_limit, not_wanted, cleared, poisoned, not_poisoned, nothing,
// not_tree, no_tool, rooked.
//
// The rules (js/patches.js, from the source; data/patches.json carries the numbers):
//   - Tend a weedy Patch: one tender at a time ("Someone else is already working on this patch.",
//     patch.js 373-381), never while someone digs; the lock lapses after 10 s, on stop, on leaving.
//     Landed: tended, weeds back in 90 s (setClean).
//   - Dig: not while someone else tends (171-174), not a hole, not a Seedling (175-178). The first
//     dig to land makes the hole (fills in 10 min, setDug). Receipt: dig_patch bonus_amount Earth.
//   - Plant a tended Patch: the bean's tree must grow in the street's planting hub
//     (can_plant_mappings; archived hubs 28 and 52 plant as 56) and fit the kind (a Dark Patch takes
//     Egg Plant Beans only, a Patch never: canPlant 463-486). The plot is held while the site's ledger
//     takes the bean (whole item, LedgerUse.php); two planters at once: one Seedling, one bean.
//   - Seedling (patch_seedling.js): three pettings from anyone (onPetted 197-251; the fourth "That's
//     enough, thnx."), each player's first counted once (petters), the planter's third counted
//     (seedlings_fully_petted). It grows at the later of its earliest time (5 min, 3 or 1 with the
//     planter's cards: setTimers 280-293) and the third petting, if before 4 h; else it withers to a
//     weedy Patch (a Dark Patch for an Egg Plant: replaceWithPatch 271-278). Grown: maturity = the
//     planter's grow level or 1, health 7, no fruit (growUp 129-151, initTrant).
//   - Tree: the shared tree rules (trees.go, the same as a street tree's): pet and water once a game
//     day for the whole tree, success rolled here; harvest each player's own Arborology limit a game
//     day (Ours: the plan's decision 5, enforced here); maturity rolls every 10 then 20 minutes with
//     maturityGrowChance, caught up after downtime. A Wood Tree grows by care (wood_tree.js
//     maturityMap), each player harvests once a game day, and at maturity 0 it is a weedy Patch 2 s
//     later (onDie). Neglect and old age are not run (register G45, as in solo and objects.go).
//   - Poison: the 3-minute clock (applyPoison 1268-1274), anyone's antidote; a trant then becomes its
//     dead class, a Wood Tree a weedy Patch and 25 Planks for the poisoner.
//   - Clear a dead tree (trant_*_dead.js clear 75-110): the first clear wins 25 Planks
//     (dead_trant_clearing bonus_amount), and a weedy Patch is left (Dark for a Dead Egg Plant).
//   - Grants (dig Earth, harvest fruit or Planks, clear Planks, a poisoned Wood Tree's Planks) are
//     credited to the site's ledger as "take" receipts (credits.go) and kept, resent on connect and in
//     the state file, until the page acknowledges them (patch_ack), as gardens' picks.
//   - Energy, mood, XP, tool wear, skill success rolls (tend, dig drops), tend drops, super harvests
//     and talkback stay the page's (through the earning budget), as on every shared object.
//
// Easy to change, named here (Austin's open calls, built with the plan's recommendations):
//   - The shared streets: data/patches.json's "pilot", one data value. null (slice 3, Austin 2026-11-08:
//     "all 100 streets", "no pilot: every street at once") shares every street laid out; a list (slice 1
//     had Old Main Street, GM4103L52ID23) shares only those.
//   - Poisoning: anyone with Botany, as the original (the page checks Botany and the guilt).
//   - Growth at the original's (today's) speed: rules.timers and rules.chance, data.
//   - Neglect off (G45); the per-player harvest limit on (rules.harvestLimit).
//
// Ours: a Seedling whose 4 hours ran out while the server was down gets a fresh hour instead of
// withering; a plot whose key the layout no longer has is kept aside (parked, logged) and written
// back, never dropped, so a later layout can move it by key.
//
// Slice 2, every tree verb (the plan's section 5, from the source and as street trees online):
//   - The Rook (rook.go): a Patch tree is rookable as the street's own trees are, keyed by its plot,
//     "<class>:p<plot key>" (the page's js/rook-attack.js rookKeyFor). At each wing beat the Rook asks
//     "patches.rookable" for the street's living Patch trees and strips the ones it rooks through
//     "patches.rookTree": include/trants.js onRooked 1140-1151 by the shared rule (trees.go treeRook:
//     every fruit gone, health down 2), and a trant already at health 1 dies to its dead class, as
//     objects.go rookTree does a street tree. A Wood Tree is rooked and stripped of nothing (objects.go
//     strips trants only). While rooked it waits for its revival: pet, water, harvest and
//     Trantsformation Fluid answer "rooked" ("It's rooked. Revive it first.", as objects.go), and a
//     rooked trant grows its fruit back from step 6 (12 s on) every 4 s until it stops changing
//     (include/trants.js 1170-1232, objects.go armRookFruit). A Patch tree that dies or is cleared is
//     no longer rooked ("rook.unrook"), so a tree grown later in the plot starts clean.
//   - Fertilidust (objects.go handleDust, obj_dust, the one jar route online): the full jar revives
//     every dead trant of the street's shared Patches at once, as the jar is used ("patches.revive":
//     fertilidust.js onUse 266-345, replaceWith the living class at health 4 + 1 and maturity 3 + 1, no
//     fruit: js/patches.js REVIVED_TRANT), and 3 s on grows every living Patch tree with the street's
//     own ("patches.dust": trees.go treeDust, woodDust). Lite names one tree: a Patch tree by its plot
//     key ("patches.dustSpot" says where it stands) grows alone (fertilidust_lite.js onUse 266-296).
//   - Trantsformation Fluid (potion_trantsformation_fluid.js canPour 512-527, getValidTargets 529-534,
//     onPour 536-592): a grown tree, never an Egg Plant on a street and never a poisoned one, maturity
//     4 or more and (a trant) health 4 or more, becomes a different random tree of this.replacements
//     (595; never an Egg Plant on a street) at health 4 and maturity max(4, maturity - 3) (a Wood Tree at
//     most 6, as js/patches.js growTree), with no fruit (the page's createTrantState fruitCount 0). The
//     bottle is the ledger's to use first (LedgerUse.php CLASSES potion_trantsformation_fluid); a tree
//     that changed while it waited gives the use back.
//
// -patches (default off: every patch_* frame is the hub's bad_frame, so pages keep their own
// Patches); -patches-file (default realtime-patches.json beside -secret-file; off: memory only). Plant,
// grow, death, clear and grants write the file at once, the rest within a second.
// GRODDLE_PATCHES_DEV=1 takes patch_dev (shorter timers, a forced roll, a game day turn).
//
// The one-time hand-over of players' own Patch trees (slice 3, the plan's section 10, Austin's pick
// "decide by the count" = option B, one winner per Patch): -patches-seed names a file the site's payout
// wrote (site src/PatchPayout.php through the game's tools/patches/payout.mjs --seed-out); at the
// first connect after a start each winner's Seedling or tree lands in its plot with them as planter
// (patches_seed.go). Each is applied once (the state file remembers it), and one that can't land is
// refunded to its planter as a grant (its bean, and the fruit or Planks on it).

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	mrand "math/rand/v2"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

//go:embed data/patches.json
var patchesJSON []byte

const (
	patchLockMs       = 10_000             // patch.js tend: apiSetTimer('onTendTimeout', 10000)
	patchFreshHour    = int64(3_600_000)   // Ours: a Seedling that ran out while the server was down
	patchGrantKeep    = 7 * 24 * time.Hour // a grant nobody acknowledged this long goes
	patchGrantsPerOne = 200

	patchBusy         = "Someone else is already working on this patch."    // patch.js 171-174, 373-381
	patchNoTending    = "Nope, no tending needed."                          // patch.js 334
	patchSeedTend     = "A seedling is growing - no need for tending!"      // patch.js 331
	patchSeedDig      = "A seedling is growing - why would you dig it out?" // patch.js 175-178
	patchSeedPlant    = "There's already a seedling here."                  // patch.js 235-240
	patchHole         = "There is already a hole there."                    // patch.js 179-182
	patchWeeds        = "You should probably tend to the weeds first."      // js/patches.js plotVerbs
	patchFillFirst    = "You need to wait for the hole to fill in first."   // js/patches.js plotVerbs
	patchWrongBean    = "That bean will not grow here. Beans only grow in certain regions. Like attracts like."
	patchNoBean       = "But what to plant? You need a *seasoned* bean. Plain beans will not work, so ask a friend where to find a seasoned one." // js/patches.js NO_SEASONED_BEAN_MSG
	patchEnough       = "That's enough, thnx."                                                                                                    // patch_seedling.js onStatus 257-268
	patchNothingHere  = "There's nothing here to clear."                                                                                          // the second clear
	patchNotTree      = "That isn't a tree."                                                                                                      // ours: a tree verb on soil
	patchRooked       = "It's rooked. Revive it first."                                                                                           // objects.go actOn
	patchNoTrantsform = "You must be near a tree to apply this."                                                                                  // potion_trantsformation_fluid.js canPour 512
	patchTrantSmall   = "This tree is too small to be trantsformed"                                                                               // canPour 523
	patchTrantWeak    = "This tree is too weak to be trantsformed"                                                                                // canPour 524
	patchUnknown      = "That is not something here."
)

var (
	patchesFlag     = flag.String("patches", "off", "on: street Patches are shared (patches.go: every street data/patches.json lays out, or its pilot list); off: pages keep their own, as before")
	patchesFileFlag = flag.String("patches-file", "", "file that keeps the shared street Patches and the grants not yet saved (default: realtime-patches.json beside -secret-file; off: memory only)")
	// patchesFileForTest gives the module a state file under go test; patchesOffForTest turns it off.
	patchesFileForTest string
	patchesOffForTest  bool

	patchKeyRe  = regexp.MustCompile(`^(?:(patch|patch_dark|trant_[a-z]+_dead):(\d{1,2})|tree:(\d{1,2}))$`)
	patchVerbs  = map[string]bool{"tend": true, "dig": true, "plant": true, "pet": true, "water": true, "harvest": true, "poison": true, "antidote": true, "clear": true, "trantsform": true}
	deadTrants  = map[string]bool{"trant_bean_dead": true, "trant_bubble_dead": true, "trant_egg_dead": true, "trant_fruit_dead": true, "trant_gas_dead": true, "trant_spice_dead": true}
	patchStages = map[string]bool{"dirty": true, "clean": true, "dug": true, "seedling": true, "tree": true, "dead": true}
	// potion_trantsformation_fluid.js 595 this.replacements, in its order.
	patchTrantsformInto = []string{"trant_bean", "trant_bubble", "trant_egg", "trant_fruit", "trant_gas", "trant_spice", "wood_tree"}
	patchToolFor        = map[string]string{"tend": "patch_hoe", "dig": "shovel", "clear": "patch_hatchet", "water": "tree_can", "harvest": "hatchet"}
)

// ---------------------------------------------------------------------------
// The layout and rules (data/patches.json)
// ---------------------------------------------------------------------------

type patchSpot struct {
	key   string
	class string // patch | patch_dark | trant_*_dead (a tree:<i> plot: what the tree left)
	x, y  float64
}

type patchStreet struct {
	hub   int
	spots map[string]*patchSpot
}

type patchRules struct {
	Timers struct {
		Clean   int64 `json:"clean"`
		Dug     int64 `json:"dug"`
		Min     int64 `json:"min"`
		Max     int64 `json:"max"`
		Cycle1  int64 `json:"cycle1"`
		Cycle   int64 `json:"cycle"`
		Wood    int64 `json:"wood"`
		Pets    int   `json:"pets"`
		CatchUp int   `json:"catchUp"`
	} `json:"timers"`
	Grow  map[string]int64 `json:"grow"`
	Start struct {
		Maturity int `json:"maturity"`
		Health   int `json:"health"`
	} `json:"start"`
	Chance       [][]float64                   `json:"chance"`
	Plant        map[string][]int              `json:"plant"`
	Aliases      map[string]int                `json:"aliases"`
	Beans        map[string]string             `json:"beans"`
	Dig          map[string]int                `json:"dig"`
	Clear        int                           `json:"clear"`
	WoodMap      []int                         `json:"woodMap"`
	HarvestLimit map[string]int                `json:"harvestLimit"`
	WoodCare     map[string]map[string]float64 `json:"woodCare"`
}

type patchWorld struct {
	rules   patchRules
	streets map[string]*patchStreet
	pilot   map[string]bool // nil: every street in the layout
}

func loadPatchWorld(raw []byte) (*patchWorld, error) {
	var file struct {
		Pilot   []string   `json:"pilot"`
		Rules   patchRules `json:"rules"`
		Streets map[string]struct {
			Hub   int     `json:"hub"`
			Plots [][]any `json:"plots"`
		} `json:"streets"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	r := file.Rules
	t := r.Timers
	if t.Clean <= 0 || t.Dug <= 0 || t.Min <= 0 || t.Max <= t.Min || t.Cycle1 <= 0 || t.Cycle <= 0 || t.Wood <= 0 || t.Pets < 1 || t.CatchUp < 1 {
		return nil, fmt.Errorf("bad timers %+v", t)
	}
	if len(r.Chance) != 11 || r.Start.Maturity < 1 || r.Start.Health < 1 || r.Clear < 1 || len(r.WoodMap) != 6 || r.Dig["no_skill"] < 1 || r.HarvestLimit["no_skill"] < 1 || len(r.Beans) == 0 {
		return nil, fmt.Errorf("bad rules")
	}
	for _, row := range r.Chance {
		if len(row) != 11 {
			return nil, fmt.Errorf("bad chance table")
		}
	}
	w := &patchWorld{rules: r, streets: map[string]*patchStreet{}}
	for tsid, row := range file.Streets {
		st := &patchStreet{hub: row.Hub, spots: map[string]*patchSpot{}}
		for _, p := range row.Plots {
			if len(p) != 4 {
				return nil, fmt.Errorf("%s: bad plot %v", tsid, p)
			}
			key, ok1 := p[0].(string)
			class, ok2 := p[1].(string)
			x, ok3 := p[2].(float64)
			y, ok4 := p[3].(float64)
			m := patchKeyRe.FindStringSubmatch(key)
			if !(ok1 && ok2 && ok3 && ok4) || m == nil || m[1] != class || st.spots[key] != nil {
				return nil, fmt.Errorf("%s: bad plot %v", tsid, p)
			}
			st.spots[key] = &patchSpot{key: key, class: class, x: x, y: y}
		}
		w.streets[tsid] = st
	}
	if file.Pilot != nil {
		w.pilot = map[string]bool{}
		for _, tsid := range file.Pilot {
			if w.streets[tsid] == nil {
				return nil, fmt.Errorf("pilot street %s has no Patches", tsid)
			}
			w.pilot[tsid] = true
		}
	}
	return w, nil
}

var patchWorldData *patchWorld

func init() {
	w, err := loadPatchWorld(patchesJSON)
	if err != nil {
		panic("realtime: data/patches.json: " + err.Error())
	}
	patchWorldData = w
	modules = append(modules, newPatches)
}

// shared: the street's Patches are the server's.
func (w *patchWorld) shared(street string) bool {
	return w.streets[street] != nil && (w.pilot == nil || w.pilot[street])
}

// plantingHub is js/patches.js plantingHub: the archived Groddle hubs plant as Groddle Forest.
func (w *patchWorld) plantingHub(hub int) int {
	if to, ok := w.rules.Aliases[strconv.Itoa(hub)]; ok {
		return to
	}
	return hub
}

// canPlant is patch.js canPlant (js/patches.js canPlantBean): a Dark Patch takes only Egg Plant
// Beans, a Patch never does, and the bean's tree must grow in the street's hub.
func (w *patchWorld) canPlant(tree, kind string, hub int) bool {
	if kind == "patch_dark" {
		return tree == "trant_egg"
	}
	if tree == "trant_egg" {
		return false
	}
	return slices.Contains(w.rules.Plant[tree], w.plantingHub(hub))
}

// chance is trants.js onMaturityCycle's grow chance (js/patches.js maturityGrowChance).
func (w *patchWorld) chance(maturity, health int) float64 {
	if maturity < 0 || maturity > 10 || health < 0 || health > 10 {
		return 0
	}
	return w.rules.Chance[maturity][health]
}

func patchKindFor(class string) string {
	if class == "patch_dark" || class == "trant_egg_dead" {
		return "patch_dark"
	}
	return "patch"
}

// ---------------------------------------------------------------------------
// Plots
// ---------------------------------------------------------------------------

type pSeedling struct {
	tree, bean string
	plantedAt  int64
	minAt      int64
	maxAt      int64
	petted     int
	petters    []int64
	planter    int64
	grow       int
	thirdAt    int64
}

type pTree struct {
	class string // trant_* or wood_tree
	treeCore
	grownAt, cycleAt int64
	// Wood Tree: harvest_count, the care counts toward the next maturity (maturityMap), when it was
	// harvested down to 0, and who harvested it this game day.
	count                   int
	woodPetted, woodWatered int
	clearedAt               int64
	woodBy                  map[int64]bool
	// A trant: each player's harvests on hday (Ours: the per-player Arborology limit).
	harvests map[int64]int
	hday     int64
	// Tree Poison: when it dies, and who poured it.
	poisonEnd, poisonBy int64
	// Rooked (a trant): the next step of the fruit it grows back while it waits (0: none, or it stopped
	// changing; objects.go armRookFruit).
	rookNext int64
}

func (t *pTree) wood() bool { return t.class == "wood_tree" }

type pPlot struct {
	spot    *patchSpot
	adopted bool // a tree:<i> plot: kept for good, its spot its own
	kind    string
	stage   string
	since   int64
	due     int64 // clean: weeds back; dug: the hole fills
	tender  int64
	lockEnd int64
	diggers []int64
	seed    *pSeedling
	tree    *pTree
	dead    string
}

// freshPatchPlot is a plot as the layout starts it: a weedy Patch, or the dead tree its class names.
func freshPatchPlot(spot *patchSpot) *pPlot {
	p := &pPlot{spot: spot, kind: patchKindFor(spot.class), stage: "dirty"}
	if deadTrants[spot.class] {
		p.stage, p.dead = "dead", spot.class
	}
	return p
}

func (p *pPlot) fresh() bool {
	if p.adopted || p.tender != 0 || len(p.diggers) > 0 || p.seed != nil || p.tree != nil || p.kind != patchKindFor(p.spot.class) {
		return false
	}
	if deadTrants[p.spot.class] {
		return p.stage == "dead" && p.dead == p.spot.class
	}
	return p.stage == "dirty"
}

// setDirty is js/patches.js setDirty: a weedy Patch from at.
func (p *pPlot) setDirty(at int64) {
	p.stage, p.since, p.due, p.seed, p.tree, p.dead = "dirty", at, 0, nil, nil, ""
}

// ---------------------------------------------------------------------------
// The module
// ---------------------------------------------------------------------------

type patchGrant struct {
	ID   string `json:"id"`
	User int64  `json:"user"`
	C    string `json:"c"`
	N    int    `json:"n"`
	At   int64  `json:"at"`
}

type patchTimers struct {
	clean, dug, min, max, cycle1, cycle, wood int64
}

type patches struct {
	h       *Hub
	w       *patchWorld
	ow      *objWorld // the tree tables (data/objects.json rules)
	off     bool
	plots   map[string]map[string]*pPlot // street -> key -> touched plot
	parked  map[string]map[string]*patchRec
	holding map[string]bool // street|key: a plant or a pour waiting for the ledger
	limits  map[int64]*objBucket
	uses    *useAsker
	grants  []patchGrant
	boot    string
	seq     uint64
	roll    func() float64

	timers  patchTimers
	dev     bool
	devRoll float64 // dev: every roll returns this (-1: random)

	timerStop func() bool
	timerAt   int64

	path    string
	saveDue bool
	resumed bool
	savedAt int64

	// The payout's winners waiting for the first connect, and every seed id applied or refunded
	// (patches_seed.go).
	seeds  []patchSeed
	seeded map[string]int64
}

func newPatches(h *Hub) Module {
	var b [4]byte
	rand.Read(b[:])
	w := patchWorldData
	t := w.rules.Timers
	g := &patches{h: h, w: w, ow: sharedWorld, plots: map[string]map[string]*pPlot{}, parked: map[string]map[string]*patchRec{},
		holding: map[string]bool{}, limits: map[int64]*objBucket{}, seeded: map[string]int64{}, boot: hex.EncodeToString(b[:]), roll: mrand.Float64, devRoll: -1,
		timers: patchTimers{clean: t.Clean, dug: t.Dug, min: t.Min, max: t.Max, cycle1: t.Cycle1, cycle: t.Cycle, wood: t.Wood}, resumed: true}
	if testing.Testing() {
		g.off = patchesOffForTest
	} else {
		g.off = *patchesFlag != "on"
		g.dev = os.Getenv("GRODDLE_PATCHES_DEV") == "1"
	}
	if g.off {
		log.Printf("patches: off (pages keep their own street Patches)")
		return g
	}
	g.uses = newUseAsker(h, "patches: Seasoned Beans, Tree Poison and Antidote")
	if g.path = statePath(patchesFileForTest, patchesFileFlag, "realtime-patches.json"); g.path != "" {
		if err := g.load(); err != nil {
			log.Printf("patches %s: %v (the Patches are kept in memory only)", g.path, err)
			g.path = ""
		}
	}
	g.loadSeeds()
	log.Printf("patches: on, %d shared streets", len(g.sharedStreets()))
	return g
}

func (g *patches) sharedStreets() []string {
	var out []string
	for tsid := range g.w.streets {
		if g.w.shared(tsid) {
			out = append(out, tsid)
		}
	}
	sort.Strings(out)
	return out
}

func (g *patches) Register(r *Registry) {
	if g.off {
		return
	}
	r.Handle("patch_sync", g.handleSync)
	r.Handle("patch_act", g.handleAct)
	r.Handle("patch_ack", g.handleAck)
	r.Handle("patch_dev", g.handleDev)
	r.OnConnect(func(cl *Client) {
		g.resume()
		g.resendGrants(cl)
	})
	r.OnLeave(func(cl *Client, street string) { g.release(cl.ID(), street) })
	r.OnDisconnect(func(cl *Client) { delete(g.limits, cl.ID()) })
	r.OnShutdown(func() { g.save() })
	r.Provide("patches.adopt", g.adopt)
	// Slice 2: the Rook (rook.go) and Fertilidust (objects.go handleDust) on Patch trees.
	r.Provide("patches.rookable", g.rookable)
	r.Provide("patches.rookTree", g.rookTree)
	r.Provide("patches.dustSpot", g.dustSpot)
	r.Provide("patches.dust", g.dust)
	r.Provide("patches.revive", g.revive)
}

func (g *patches) nowMs() int64 { return g.h.Now().UnixMilli() }

func (g *patches) today() int64 {
	return int64(math.Floor(float64(g.nowMs()-gameEpochMs) / gameDayMs))
}

func (g *patches) dayOf(ms int64) int64 {
	return int64(math.Floor(float64(ms-gameEpochMs) / gameDayMs))
}

func (g *patches) rnd() float64 {
	if g.devRoll >= 0 {
		return g.devRoll
	}
	return g.roll()
}

func (g *patches) allow(cl *Client) bool {
	now := g.h.Now()
	b := g.limits[cl.ID()]
	if b == nil {
		b = &objBucket{tokens: objBurst, at: now}
		g.limits[cl.ID()] = b
	}
	b.tokens = math.Min(objBurst, b.tokens+now.Sub(b.at).Seconds()*objEvery)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// spotOf is the place a key names on a street: the layout's, or a tree:<i> plot's own.
func (g *patches) spotOf(street, key string) *patchSpot {
	if st := g.w.streets[street]; st != nil {
		if s := st.spots[key]; s != nil {
			return s
		}
	}
	if p := g.plots[street][key]; p != nil && p.adopted {
		return p.spot
	}
	return nil
}

// plot is the live plot a key names (a fresh one, kept, if nobody touched it), brought to now.
func (g *patches) plot(street string, spot *patchSpot, now int64) *pPlot {
	room := g.plots[street]
	if room == nil {
		room = map[string]*pPlot{}
		g.plots[street] = room
	}
	p := room[spot.key]
	if p == nil {
		p = freshPatchPlot(spot)
		room[spot.key] = p
	}
	g.advance(street, p, now)
	return p
}

// forget drops a street's plots that are fresh again.
func (g *patches) forget(street string) {
	room := g.plots[street]
	for key, p := range room {
		if p.fresh() && !g.holding[street+"|"+key] {
			delete(room, key)
		}
	}
	if len(room) == 0 {
		delete(g.plots, street)
	}
}

// ---------------------------------------------------------------------------
// Time
// ---------------------------------------------------------------------------

func (g *patches) cycleMs(maturity int) int64 {
	if maturity <= 1 {
		return g.timers.cycle1
	}
	return g.timers.cycle
}

func (g *patches) seedReady(s *pSeedling) (int64, bool) {
	if s.petted < g.w.rules.Timers.Pets {
		return 0, false
	}
	at := max(s.minAt, s.thirdAt)
	return at, at <= s.maxAt
}

// growTree is patch_seedling.js growUp at at: a trant at the planter's grow level (or initTrant's
// maturity 1), health 7, no fruit; a Wood Tree at min(6, that).
func (g *patches) growTree(p *pPlot, at int64) {
	s := p.seed
	m := g.w.rules.Start.Maturity
	if s.grow > 0 {
		m = s.grow
	}
	t := &pTree{class: s.tree, grownAt: at, cycleAt: at}
	t.day = g.dayOf(at)
	if t.wood() {
		t.maturity = min(6, m)
	} else {
		t.maturity, t.health = m, g.w.rules.Start.Health
		t.cap, t.fruit = g.ow.capAt(t.class, t.maturity, t.health), 0
	}
	p.stage, p.since, p.due, p.seed, p.tree = "tree", at, 0, nil, t
}

// killTree is include/trants.js die 734-773 on a Patch tree (wood_tree.js onDie: a weedy Patch and
// 25 Planks for the poisoner): everyone sees it at once. The file is written before the Planks are
// granted, so a restart never finds the clock still running.
func (g *patches) killTree(street string, p *pPlot, at int64) {
	t := p.tree
	by := t.poisonBy
	if t.wood() {
		p.kind = "patch"
		p.setDirty(at)
	} else {
		p.stage, p.since, p.due, p.tree, p.dead = "dead", at, 0, nil, t.class+"_dead"
	}
	log.Printf("patches: the %s in %s %s died (poisoned by %d)", t.class, street, p.spot.key, by)
	g.unrook(street, t.class, p.spot.key)
	g.save()
	if t.wood() && by > 0 {
		gr := g.grant(by, "plank", deadWoodPlanks)
		creditLocked(g.h, by, "take", creditItem("plank", deadWoodPlanks))
		if cl := g.h.ClientLocked(by); cl != nil && cl.Online() {
			g.h.SendLocked(cl, map[string]any{"t": "patch_grant", "id": gr.ID, "c": gr.C, "n": gr.N})
		}
		g.save()
	}
}

// advance brings a plot to now: the tend lock's lapse, weeds back, the hole filled, the Seedling's
// growth or withering, a poisoned tree's death, maturity rolls, a Wood Tree gone to a Patch and the
// game day's tending window. True when something everyone sees changed.
func (g *patches) advance(street string, p *pPlot, now int64) bool {
	changed := false
	if p.tender != 0 && now >= p.lockEnd {
		p.tender, p.lockEnd = 0, 0
		changed = true
	}
	if (p.stage == "clean" || p.stage == "dug") && p.due > 0 && now >= p.due {
		// patch.js onRegrowTimer / onFillTimer: weedy again.
		p.setDirty(p.due)
		changed = true
	}
	if p.stage == "seedling" && p.seed != nil {
		if at, ok := g.seedReady(p.seed); ok && at <= now {
			g.growTree(p, at)
			log.Printf("patches: the Seedling in %s %s grew into a %s", street, p.spot.key, p.tree.class)
			changed = true
		} else if now >= p.seed.maxAt {
			// onMaxTimePassed -> replaceWithPatch: a Dark Patch for an Egg Plant.
			if p.seed.tree == "trant_egg" {
				p.kind = "patch_dark"
			}
			log.Printf("patches: the Seedling in %s %s withered", street, p.spot.key)
			p.setDirty(p.seed.maxAt)
			changed = true
		}
	}
	if p.stage == "tree" && p.tree != nil {
		t := p.tree
		if t.poisonEnd > 0 && now >= t.poisonEnd {
			g.killTree(street, p, t.poisonEnd)
			return true
		}
		if t.wood() {
			if t.maturity <= 0 && t.clearedAt > 0 && now >= t.clearedAt+g.timers.wood {
				p.kind = "patch"
				p.setDirty(t.clearedAt + g.timers.wood)
				return true
			}
		} else {
			if g.rookFruit(street, p, now) {
				changed = true
			}
			guard := 0
			for guard < g.w.rules.Timers.CatchUp {
				next := t.cycleAt + g.cycleMs(t.maturity)
				if next > now {
					break
				}
				t.cycleAt = next
				guard++
				if c := g.w.chance(t.maturity, t.health); c > 0 && g.rnd() < c {
					t.maturity = min(10, t.maturity+1)
					t.cap = g.ow.capAt(t.class, t.maturity, t.health)
					changed = true
				}
			}
			if guard >= g.w.rules.Timers.CatchUp {
				t.cycleAt = now
			}
		}
		today := g.dayOf(now)
		if t.day < today {
			if t.wood() {
				t.day, t.petted, t.watered = today, false, false
			} else {
				g.ow.treeNewDay(&t.treeCore, t.class, today)
			}
			changed = true
		}
		if t.hday < today && (len(t.harvests) > 0 || len(t.woodBy) > 0) {
			t.hday, t.harvests, t.woodBy = today, nil, nil
		}
	}
	return changed
}

// due is when the plot next changes on its own (0: never).
func (g *patches) due(p *pPlot) int64 {
	var at int64
	pick := func(t int64) {
		if t > 0 && (at == 0 || t < at) {
			at = t
		}
	}
	if p.tender != 0 {
		pick(p.lockEnd)
	}
	if p.stage == "clean" || p.stage == "dug" {
		pick(p.due)
	}
	if s := p.seed; p.stage == "seedling" && s != nil {
		if t, ok := g.seedReady(s); ok {
			pick(t)
		} else {
			pick(s.maxAt)
		}
	}
	if t := p.tree; p.stage == "tree" && t != nil {
		pick(t.poisonEnd)
		pick(t.rookNext)
		if t.wood() {
			if t.maturity <= 0 && t.clearedAt > 0 {
				pick(t.clearedAt + g.timers.wood)
			}
		} else if t.maturity < 10 && g.w.chance(t.maturity, t.health) > 0 {
			pick(t.cycleAt + g.cycleMs(t.maturity))
		}
		if t.petted || t.watered || len(t.harvests) > 0 || len(t.woodBy) > 0 {
			pick(gameEpochMs + (g.dayOf(g.nowMs())+1)*gameDayMs + 50)
		}
	}
	return at
}

// arm keeps one timer for the soonest plot (as gardens.go).
func (g *patches) arm() {
	var next int64
	for _, room := range g.plots {
		for _, p := range room {
			if d := g.due(p); d > 0 && (next == 0 || d < next) {
				next = d
			}
		}
	}
	if next == g.timerAt && g.timerStop != nil {
		return
	}
	if g.timerStop != nil {
		g.timerStop()
		g.timerStop = nil
	}
	g.timerAt = next
	if next == 0 {
		return
	}
	wait := time.Duration(max(0, next-g.nowMs())) * time.Millisecond
	g.timerStop = g.h.After(wait, g.tick)
}

func (g *patches) tick() {
	g.timerStop, g.timerAt = nil, 0
	now := g.nowMs()
	touched := false
	for street, room := range g.plots {
		var changed []string
		for key, p := range room {
			if g.advance(street, p, now) {
				changed = append(changed, key)
			}
		}
		if len(changed) > 0 {
			sort.Strings(changed)
			g.broadcast(street, changed, 0)
			g.forget(street)
			touched = true
		}
	}
	if touched {
		g.saveSoon()
	}
	g.arm()
}

// resume (the first connect after a start) brings every plot to now and arms the timer. Ours: a
// Seedling whose 4 hours ran out while the server was down gets a fresh hour.
func (g *patches) resume() {
	if g.resumed {
		return
	}
	g.resumed = true
	now := g.nowMs()
	g.applySeeds(now)
	for street, room := range g.plots {
		for _, p := range room {
			if s := p.seed; p.stage == "seedling" && s != nil && g.savedAt > 0 {
				if _, ok := g.seedReady(s); !ok && s.maxAt <= now && s.maxAt >= g.savedAt {
					s.maxAt = now + patchFreshHour
					log.Printf("patches: the Seedling in %s %s ran out while the server was down: a fresh hour", street, p.spot.key)
				}
			}
			g.advance(street, p, now)
		}
		g.forget(street)
	}
	g.saveSoon()
	g.arm()
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

func (g *patches) view(p *pPlot, now int64) map[string]any {
	m := map[string]any{"class": p.spot.class, "kind": p.kind, "stage": p.stage}
	if p.since > 0 {
		m["since"] = p.since
	}
	if p.due > 0 {
		m["due"] = p.due
	}
	if p.tender != 0 {
		m["tender"], m["lockLeft"] = p.tender, max(1, p.lockEnd-now)
	}
	if len(p.diggers) > 0 {
		m["diggers"] = append([]int64(nil), p.diggers...)
	}
	if p.adopted {
		m["x"], m["y"] = p.spot.x, p.spot.y
	}
	if s := p.seed; s != nil {
		m["seedling"] = map[string]any{"tree": s.tree, "bean": s.bean, "plantedAt": s.plantedAt, "minAt": s.minAt, "maxAt": s.maxAt,
			"petted": s.petted, "planter": s.planter, "grow": s.grow, "thirdAt": s.thirdAt}
	}
	if t := p.tree; t != nil {
		tv := map[string]any{"class": t.class, "maturity": t.maturity, "petted": t.petted, "watered": t.watered, "grownAt": t.grownAt}
		if t.wood() {
			tv["count"], tv["woodPetted"], tv["woodWatered"] = t.count, t.woodPetted, t.woodWatered
			if t.clearedAt > 0 {
				tv["clearedAt"] = t.clearedAt
			}
		} else {
			tv["health"], tv["fruit"], tv["cap"], tv["cycleAt"] = t.health, t.fruit, t.cap, t.cycleAt
		}
		if t.poisonEnd > 0 {
			tv["poison"], tv["by"] = max(1, t.poisonEnd-now), t.poisonBy
		}
		m["tree"] = tv
	}
	if p.dead != "" {
		m["dead"] = p.dead
	}
	return m
}

func (g *patches) views(street string, keys []string, now int64) map[string]any {
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		if p := g.plots[street][key]; p != nil {
			out[key] = g.view(p, now)
		} else if spot := g.spotOf(street, key); spot != nil {
			out[key] = g.view(freshPatchPlot(spot), now)
		}
	}
	return out
}

func (g *patches) broadcast(street string, keys []string, except int64) {
	if len(keys) == 0 {
		return
	}
	now := g.nowMs()
	g.h.BroadcastStreetLocked(street, map[string]any{"t": "patches", "street": street, "now": now, "plots": g.views(street, keys, now)}, except)
}

func (g *patches) timerView() map[string]any {
	return map[string]any{"clean": g.timers.clean, "dug": g.timers.dug, "min": g.timers.min, "max": g.timers.max,
		"cycle1": g.timers.cycle1, "cycle": g.timers.cycle, "wood": g.timers.wood}
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

func (g *patches) handleSync(cl *Client, _ json.RawMessage) {
	street := cl.Street()
	if street == "" || !g.allow(cl) {
		return
	}
	if !g.w.shared(street) {
		g.h.SendLocked(cl, map[string]any{"t": "patches", "street": street, "snap": true, "none": true, "plots": map[string]any{}})
		return
	}
	g.resume()
	now := g.nowMs()
	keys := make([]string, 0, len(g.plots[street]))
	for key, p := range g.plots[street] {
		g.advance(street, p, now)
		keys = append(keys, key)
	}
	sort.Strings(keys)
	g.h.SendLocked(cl, map[string]any{"t": "patches", "street": street, "snap": true, "now": now, "timers": g.timerView(), "plots": g.views(street, keys, now)})
	g.forget(street)
	g.arm()
}

type patchAct struct {
	Req   int64  `json:"req"`
	ID    string `json:"id"`
	Verb  string `json:"verb"`
	Do    string `json:"do"`
	Skill string `json:"skill"`
	Bean  string `json:"bean"`
	Grow  int    `json:"grow"`
	Drop  bool   `json:"drop"`
	Ms    int64  `json:"ms"`
	K     *int   `json:"k"` // trantsform: the bottle (its place among the player's bottles in the pack)
}

// patchReply answers one act.
type patchReply struct {
	g      *patches
	cl     *Client
	m      patchAct
	street string
	spot   *patchSpot
	before map[string]any
}

func (rp *patchReply) no(code, message string) {
	g := rp.g
	if rp.cl.Online() {
		now := g.nowMs()
		msg := map[string]any{"t": "patch_no", "req": rp.m.Req, "id": rp.m.ID, "verb": rp.m.Verb, "code": code, "message": message, "now": now}
		if rp.spot != nil {
			msg["plot"] = g.views(rp.street, []string{rp.spot.key}, now)[rp.spot.key]
		}
		g.h.SendLocked(rp.cl, msg)
	}
	if rp.street != "" {
		g.forget(rp.street)
	}
}

// ok answers the act, tells the street what changed (and, for a tend or dig, who is at it), and
// keeps the change: at once when now, else within a second.
func (rp *patchReply) ok(extra map[string]any, now bool) {
	g := rp.g
	t := g.nowMs()
	key := rp.spot.key
	if rp.cl.Online() {
		msg := map[string]any{"t": "patch_ok", "req": rp.m.Req, "id": rp.m.ID, "verb": rp.m.Verb, "now": t, "before": rp.before,
			"plot": g.views(rp.street, []string{key}, t)[key]}
		if rp.m.Do != "" {
			msg["do"] = rp.m.Do
		}
		for k, v := range extra {
			msg[k] = v
		}
		g.h.SendLocked(rp.cl, msg)
	}
	g.broadcast(rp.street, []string{key}, rp.cl.ID())
	if rp.m.Verb == "tend" || rp.m.Verb == "dig" {
		ev := map[string]any{"t": "patch_ev", "street": rp.street, "by": rp.cl.ID(), "id": key, "verb": rp.m.Verb}
		if rp.m.Do != "" {
			ev["do"] = rp.m.Do
		}
		g.h.BroadcastStreetLocked(rp.street, ev, rp.cl.ID())
	}
	g.forget(rp.street)
	if now {
		g.save()
	} else {
		g.saveSoon()
	}
	g.arm()
}

// live re-reads the plot after a wait (a tool check, the ledger), for "before" and the act.
func (rp *patchReply) live() *pPlot {
	now := rp.g.nowMs()
	p := rp.g.plot(rp.street, rp.spot, now)
	rp.before = rp.g.view(p, now)
	return p
}

func (g *patches) handleAct(cl *Client, frame json.RawMessage) {
	var m patchAct
	if json.Unmarshal(frame, &m) != nil || m.Req < 1 || m.Req > 1<<31 || len(m.ID) > 40 || len(m.Skill) > 40 || len(m.Bean) > 40 || (m.Do != "" && m.Do != "start" && m.Do != "stop") {
		g.h.ErrorLocked(cl, "bad_patch", "That patch message did not check out.")
		return
	}
	rp := &patchReply{g: g, cl: cl, m: m}
	if !g.allow(cl) {
		rp.no("slow_down", "Slow down a little.")
		return
	}
	street := cl.Street()
	if street == "" || !g.w.shared(street) || !patchVerbs[m.Verb] || !patchKeyRe.MatchString(m.ID) {
		rp.no("unknown", patchUnknown)
		return
	}
	spot := g.spotOf(street, m.ID)
	if spot == nil {
		rp.no("unknown", patchUnknown)
		return
	}
	rp.street = street
	if x, y := cl.Pos(); math.Abs(x-spot.x) > objReachX || math.Abs(y-spot.y) > objReachY {
		rp.no("far", "You need to be closer.")
		return
	}
	rp.spot = spot
	g.resume()
	p := rp.live()
	if g.holding[street+"|"+spot.key] {
		rp.no("busy", patchBusy)
		return
	}
	switch m.Verb {
	case "tend":
		g.tend(rp, p)
	case "dig":
		g.dig(rp, p)
	case "plant":
		g.plant(rp, p)
	case "pet":
		g.pet(rp, p)
	case "water":
		g.water(rp, p)
	case "harvest":
		g.harvest(rp, p)
	case "poison", "antidote":
		g.pour(rp, p)
	case "clear":
		g.clear(rp, p)
	case "trantsform":
		g.trantsform(rp, p)
	}
}

// withTool runs act once the site's ledger says the player holds the verb's tool (objects_tools.go,
// through "objects.withTool": the same answer the street's rocks and trees use), or at once without
// that module or ledger. credit false: the site gave no answer, so the take is not a receipt.
func (g *patches) withTool(rp *patchReply, group string, act func(p *pPlot, credit bool)) {
	fn, ok := g.h.ProvidedLocked("objects.withTool").(func(*Client, string, string, func(string, string), func(bool)))
	if !ok {
		act(g.plots[rp.street][rp.spot.key], true)
		return
	}
	fn(rp.cl, rp.street, group, func(code, message string) { rp.no(code, message) }, func(credit bool) {
		act(rp.live(), credit)
	})
}

// othersAt: someone else is tending, or digging (patch.js 171-174, 373-381).
func othersAt(p *pPlot, who int64, digs bool) bool {
	if p.tender != 0 && p.tender != who {
		return true
	}
	if digs {
		for _, d := range p.diggers {
			if d != who {
				return true
			}
		}
	}
	return false
}

// soilRefusal is what refuses a soil verb on a plot that is not soil now (a Seedling, a tree, a dead tree).
func soilRefusal(p *pPlot, seedLine string) (string, string) {
	switch p.stage {
	case "seedling", "tree":
		return "planted", seedLine
	case "dead":
		return "unknown", patchUnknown
	}
	return "", ""
}

func (g *patches) tend(rp *patchReply, p *pPlot) {
	who := rp.cl.ID()
	if rp.m.Do == "stop" {
		if p.tender == who {
			p.tender, p.lockEnd = 0, 0
		}
		rp.ok(nil, false)
		return
	}
	check := func(p *pPlot) (string, string) {
		if code, line := soilRefusal(p, patchSeedTend); code != "" {
			return code, line
		}
		if othersAt(p, who, true) {
			return "busy", patchBusy
		}
		if p.stage != "dirty" {
			return "not_messy", patchNoTending
		}
		return "", ""
	}
	if code, line := check(p); code != "" {
		rp.no(code, line)
		return
	}
	g.withTool(rp, patchToolFor["tend"], func(p *pPlot, _ bool) {
		if code, line := check(p); code != "" {
			rp.no(code, line)
			return
		}
		now := g.nowMs()
		if rp.m.Do == "start" {
			p.tender, p.lockEnd = who, now+patchLockMs
			rp.ok(map[string]any{"lock": int64(patchLockMs)}, false)
			return
		}
		// onTendComplete: setClean, the weeds back in 90 s.
		p.tender, p.lockEnd = 0, 0
		p.stage, p.since, p.due = "clean", now, now+g.timers.clean
		rp.ok(nil, false)
	})
}

func (g *patches) dig(rp *patchReply, p *pPlot) {
	who := rp.cl.ID()
	if rp.m.Do == "stop" {
		p.diggers = slices.DeleteFunc(p.diggers, func(d int64) bool { return d == who })
		rp.ok(nil, false)
		return
	}
	check := func(p *pPlot) (string, string) {
		if code, line := soilRefusal(p, patchSeedDig); code != "" {
			return code, line
		}
		if othersAt(p, who, false) {
			return "busy", patchBusy
		}
		if p.stage == "dug" {
			return "dug", patchHole
		}
		return "", ""
	}
	if code, line := check(p); code != "" {
		p.diggers = slices.DeleteFunc(p.diggers, func(d int64) bool { return d == who })
		rp.no(code, line)
		return
	}
	g.withTool(rp, patchToolFor["dig"], func(p *pPlot, credit bool) {
		if code, line := check(p); code != "" {
			p.diggers = slices.DeleteFunc(p.diggers, func(d int64) bool { return d == who })
			rp.no(code, line)
			return
		}
		now := g.nowMs()
		if rp.m.Do == "start" {
			if !slices.Contains(p.diggers, who) {
				p.diggers = append(p.diggers, who)
			}
			rp.ok(map[string]any{"diggers": append([]int64(nil), p.diggers...)}, false)
			return
		}
		// onDigComplete: setDug (the hole fills in 10 min); the others' digs are over.
		p.diggers = nil
		p.stage, p.since, p.due = "dug", now, now+g.timers.dug
		var extra map[string]any
		if !rp.m.Drop {
			n, ok := g.w.rules.Dig[rp.m.Skill]
			if !ok {
				n = g.w.rules.Dig["no_skill"]
			}
			gr := g.grant(who, "earth", n)
			if credit {
				creditLocked(g.h, who, "take", creditItem("earth", n))
			}
			extra = map[string]any{"grant": map[string]any{"id": gr.ID, "c": gr.C, "n": gr.N}}
			rp.ok(extra, true)
			return
		}
		rp.ok(nil, false)
	})
}

func (g *patches) plant(rp *patchReply, p *pPlot) {
	check := func(p *pPlot) (string, string) {
		if code, line := soilRefusal(p, patchSeedPlant); code != "" {
			return code, line
		}
		switch p.stage {
		case "dirty":
			return "weeds", patchWeeds
		case "dug":
			return "hole", patchFillFirst
		}
		return "", ""
	}
	if code, line := check(p); code != "" {
		rp.no(code, line)
		return
	}
	tree, ok := g.w.rules.Beans[rp.m.Bean]
	if !ok {
		rp.no("no_item", patchNoBean)
		return
	}
	if !g.w.canPlant(tree, p.kind, g.w.streets[rp.street].hub) {
		rp.no("wrong_bean", patchWrongBean)
		return
	}
	grow := 0
	if _, ok := g.w.rules.Grow[strconv.Itoa(rp.m.Grow)]; ok {
		grow = rp.m.Grow
	}
	key := rp.street + "|" + rp.spot.key
	g.holding[key] = true
	g.uses.ask(rp.cl, rp.m.Bean, 1, func(use map[string]any, code, message string) {
		delete(g.holding, key)
		if code != "" {
			if code == "no_item" {
				message = patchNoBean
			}
			rp.no(code, message)
			return
		}
		p := rp.live()
		if code, line := check(p); code != "" {
			// Held, so nobody planted it meanwhile, but the weeds can come back: the bean is given back.
			log.Printf("patches: %s %s was no longer tended when %d's bean came (%s); the bean goes back", rp.street, rp.spot.key, rp.cl.ID(), p.stage)
			if use != nil {
				g.uses.giveBack(use, rp.cl.ID())
			}
			rp.no(code, line)
			return
		}
		now := g.nowMs()
		minMs := g.timers.min
		if grow > 0 {
			minMs = min(minMs, g.w.rules.Grow[strconv.Itoa(grow)])
		}
		// plantBean: the Seedling, its owner, its timers (setTimers with the planter's cards).
		p.stage, p.since, p.due, p.tender, p.lockEnd = "seedling", now, 0, 0, 0
		p.seed = &pSeedling{tree: tree, bean: rp.m.Bean, plantedAt: now, minAt: now + minMs, maxAt: now + g.timers.max, planter: rp.cl.ID(), grow: grow}
		log.Printf("patches: %d planted %s in %s %s", rp.cl.ID(), rp.m.Bean, rp.street, rp.spot.key)
		var extra map[string]any
		if use != nil {
			extra = map[string]any{"use": use}
		}
		rp.ok(extra, true)
	})
}

func (g *patches) pet(rp *patchReply, p *pPlot) {
	who := rp.cl.ID()
	switch {
	case p.stage == "seedling" && p.seed != nil:
		s := p.seed
		pets := g.w.rules.Timers.Pets
		if s.petted >= pets {
			rp.no("enough", patchEnough)
			return
		}
		now := g.nowMs()
		s.petted++
		extra := map[string]any{"petted": s.petted}
		if !slices.Contains(s.petters, who) {
			s.petters = append(s.petters, who)
			extra["first"] = true
		}
		if s.petted == pets {
			s.thirdAt = now
			extra["third"] = true
			if s.planter == who {
				extra["owner"] = true
			}
		}
		grew := false
		if at, ok := g.seedReady(s); ok && at <= now {
			g.growTree(p, now)
			grew = true
			log.Printf("patches: the Seedling in %s %s grew into a %s (petted by %d)", rp.street, rp.spot.key, p.tree.class, who)
		}
		if grew {
			extra["grew"] = true
		}
		rp.ok(extra, grew)
	case p.stage == "tree" && p.tree != nil:
		if g.rooked(rp.street, p) {
			rp.no("rooked", patchRooked)
			return
		}
		g.care(rp, p, "pet")
	default:
		rp.no("not_tree", patchNotTree)
	}
}

func (g *patches) water(rp *patchReply, p *pPlot) {
	if p.stage != "tree" || p.tree == nil {
		rp.no("not_tree", patchNotTree)
		return
	}
	if g.rooked(rp.street, p) {
		rp.no("rooked", patchRooked)
		return
	}
	if v, no := g.careRefusal(p.tree, "water"); no {
		rp.no(v.code, v.message)
		return
	}
	g.withTool(rp, patchToolFor["water"], func(p *pPlot, _ bool) {
		if p.stage != "tree" || p.tree == nil {
			rp.no("not_tree", patchNotTree)
			return
		}
		if g.rooked(rp.street, p) {
			rp.no("rooked", patchRooked)
			return
		}
		g.care(rp, p, "water")
	})
}

func (g *patches) careRefusal(t *pTree, verb string) (verdict, bool) {
	if verb == "pet" && t.petted {
		return refuse("not_wanted", "This tree was already petted today."), true
	}
	if verb == "water" && t.watered {
		return refuse("not_wanted", "This tree was already watered today."), true
	}
	return verdict{}, false
}

// care is a pet or a water on a grown tree: a trant by the shared rules (trees.go treeCare); a Wood
// Tree (js/trant-care.js wood_pet / wood_water, js/patches.js noteWoodTreeCare): each new petting and
// watering counts, and once both are done and both counts reach maturityMap[m-1] it gains a maturity.
func (g *patches) care(rp *patchReply, p *pPlot, verb string) {
	t := p.tree
	if !t.wood() {
		v := g.ow.treeCare(&t.treeCore, t.class, verb, rp.m.Skill, g.rnd)
		if !v.ok {
			rp.no(v.code, v.message)
			return
		}
		rp.ok(v.extra, false)
		return
	}
	if v, no := g.careRefusal(t, verb); no {
		rp.no(v.code, v.message)
		return
	}
	rates := g.w.rules.WoodCare[verb]
	rate, ok := rates[rp.m.Skill]
	if !ok {
		rate = rates["no_skill"]
	}
	roll := g.rnd()
	success := roll*100 < rate
	extra := map[string]any{"roll": roll, "success": success}
	if success {
		if verb == "pet" {
			t.petted = true
			t.woodPetted++
		} else {
			t.watered = true
			t.woodWatered++
		}
		if t.petted && t.watered && t.maturity >= 1 && t.maturity < 6 {
			need := g.w.rules.WoodMap[t.maturity-1]
			if t.woodPetted >= need && t.woodWatered >= need {
				t.maturity++
				t.count, t.woodPetted, t.woodWatered = 0, 0, 0
				extra["matured"] = true
			}
		}
	}
	rp.ok(extra, extra["matured"] == true)
}

func (g *patches) harvest(rp *patchReply, p *pPlot) {
	if p.stage != "tree" || p.tree == nil {
		rp.no("not_tree", patchNotTree)
		return
	}
	who := rp.cl.ID()
	if g.rooked(rp.street, p) {
		rp.no("rooked", patchRooked)
		return
	}
	if p.tree.wood() {
		g.withTool(rp, patchToolFor["harvest"], func(p *pPlot, credit bool) {
			if p.stage != "tree" || p.tree == nil || !p.tree.wood() {
				rp.no("not_tree", patchNotTree)
				return
			}
			if g.rooked(rp.street, p) {
				rp.no("rooked", patchRooked)
				return
			}
			t := p.tree
			v := woodChop(&t.treeCore, &t.count, &t.woodBy, who)
			if !v.ok {
				rp.no(v.code, v.message)
				return
			}
			t.hday = g.today()
			if t.maturity <= 0 {
				// onDie: a Patch 2 s on (wood_tree.js 788).
				t.clearedAt = g.nowMs()
			} else if t.woodPetted > 0 || t.woodWatered > 0 {
				// endHarvest zeroes the care counts when the tree survives the harvest.
				t.woodPetted, t.woodWatered = 0, 0
			}
			n := v.got.Items[0].N
			gr := g.grant(who, "plank", n)
			if credit {
				creditLocked(g.h, who, "take", v.got)
			}
			rp.ok(map[string]any{"grant": map[string]any{"id": gr.ID, "c": gr.C, "n": gr.N}}, true)
		})
		return
	}
	t := p.tree
	limit, ok := g.w.rules.HarvestLimit[rp.m.Skill]
	if !ok {
		limit = g.w.rules.HarvestLimit["no_skill"]
	}
	if t.maturity >= 3 && t.fruit > 0 && t.harvests[who] >= limit {
		times := "time"
		if limit > 1 {
			times = "times"
		}
		rp.no("daily_limit", fmt.Sprintf("You can only harvest from each tree %d %s per game day.", limit, times))
		return
	}
	v := g.ow.treeHarvest(&t.treeCore, t.class, rp.m.Skill)
	if !v.ok {
		rp.no(v.code, v.message)
		return
	}
	if t.harvests == nil {
		t.harvests = map[int64]int{}
	}
	t.harvests[who]++
	t.hday = g.today()
	it := v.got.Items[0]
	gr := g.grant(who, it.C, it.N)
	creditLocked(g.h, who, "take", v.got)
	extra := map[string]any{"take": v.extra["take"], "harvests": t.harvests[who], "limit": limit, "grant": map[string]any{"id": gr.ID, "c": gr.C, "n": gr.N}}
	log.Printf("patches: %d harvested %d %s in %s %s (%s)", who, it.N, it.C, rp.street, rp.spot.key, gr.ID)
	rp.ok(extra, true)
}

// pourPatchRefusal is a pour's refusal before anything is used.
func pourPatchRefusal(p *pPlot, verb string) (string, string) {
	switch {
	case p.stage != "tree" || p.tree == nil:
		return "not_tree", patchNotTree
	case verb == "antidote" && p.tree.poisonEnd == 0:
		return "not_poisoned", noPoisonedTree
	case verb == "poison" && p.tree.poisonEnd != 0:
		return "poisoned", alreadyPoison
	}
	return "", ""
}

// pour is a Tree Poison or Antidote pour landing on a Patch tree (the potions' onPourComplete:
// applyPoison 1268-1274, cure). The bottle is the ledger's to use first; the guilt, the mood and the
// Botany gate are the pourer's own (js/tree-poison.js). Anyone's antidote stops the clock.
func (g *patches) pour(rp *patchReply, p *pPlot) {
	if code, line := pourPatchRefusal(p, rp.m.Verb); code != "" {
		rp.no(code, line)
		return
	}
	class := usePoison
	if rp.m.Verb == "antidote" {
		class = useAntidote
	}
	key := rp.street + "|" + rp.spot.key
	g.holding[key] = true
	g.uses.ask(rp.cl, class, 0, func(use map[string]any, code, message string) {
		delete(g.holding, key)
		if code != "" {
			rp.no(code, message)
			return
		}
		p := rp.live()
		if code, line := pourPatchRefusal(p, rp.m.Verb); code != "" {
			log.Printf("patches: a %s on %s %s by %d was paid for, but the tree changed first (%s)", rp.m.Verb, rp.street, rp.spot.key, rp.cl.ID(), code)
			rp.no(code, line)
			return
		}
		extra := map[string]any{}
		if use != nil {
			extra["use"] = use
		}
		t := p.tree
		if rp.m.Verb == "antidote" {
			t.poisonEnd, t.poisonBy = 0, 0
		} else {
			d := treePoison.Milliseconds()
			if g.dev && rp.m.Ms >= 1000 && rp.m.Ms < d {
				d = rp.m.Ms
			}
			t.poisonEnd, t.poisonBy = g.nowMs()+d, rp.cl.ID()
			extra["poison"] = d
		}
		rp.ok(extra, true)
	})
}

func (g *patches) clear(rp *patchReply, p *pPlot) {
	if p.stage != "dead" {
		rp.no("nothing", patchNothingHere)
		return
	}
	g.withTool(rp, patchToolFor["clear"], func(p *pPlot, credit bool) {
		if p.stage != "dead" {
			rp.no("nothing", patchNothingHere)
			return
		}
		who := rp.cl.ID()
		// trant_*_dead.js clear: the Planks, and a Patch (a Dark Patch for a Dead Egg Plant).
		p.kind = patchKindFor(p.dead)
		p.setDirty(g.nowMs())
		n := g.w.rules.Clear
		gr := g.grant(who, "plank", n)
		if credit {
			creditLocked(g.h, who, "take", creditItem("plank", n))
		}
		log.Printf("patches: %d cleared %s %s (%s)", who, rp.street, rp.spot.key, gr.ID)
		rp.ok(map[string]any{"grant": map[string]any{"id": gr.ID, "c": gr.C, "n": gr.N}}, true)
	})
}

// release lets go of a player's tend lock and digs on a street they left (patch.js
// onOverlayDismissed 640-652: the tend or dig is over).
func (g *patches) release(who int64, street string) {
	var changed []string
	verbs := map[string]string{}
	for key, p := range g.plots[street] {
		switch {
		case p.tender == who:
			p.tender, p.lockEnd = 0, 0
			verbs[key] = "tend"
		case slices.Contains(p.diggers, who):
			verbs[key] = "dig"
		default:
			continue
		}
		p.diggers = slices.DeleteFunc(p.diggers, func(d int64) bool { return d == who })
		changed = append(changed, key)
	}
	if len(changed) == 0 {
		return
	}
	sort.Strings(changed)
	g.broadcast(street, changed, 0)
	for _, key := range changed {
		g.h.BroadcastStreetLocked(street, map[string]any{"t": "patch_ev", "street": street, "by": who, "id": key, "verb": verbs[key], "do": "stop"}, who)
	}
	g.forget(street)
	g.arm()
}

// adopt is objects.go killTree's hook: a shared street tree that died on a shared street leaves a
// tree:<i> plot for everyone at once (its dead class, or a weedy Patch for a Wood Tree), where the
// page draws it (the tree's x, its feet less 4: patches-feature adoptedPlan).
func (g *patches) adopt(street string, tree *objSpot) {
	if tree == nil || !g.w.shared(street) || !strings.HasPrefix(tree.id, "tree:") {
		return
	}
	if p := g.plots[street][tree.id]; p != nil {
		return
	}
	class := tree.class + "_dead"
	if tree.kind == "wood" || !deadTrants[class] {
		class = "patch"
	}
	spot := &patchSpot{key: tree.id, class: class, x: tree.x, y: tree.y - 4}
	p := freshPatchPlot(spot)
	p.adopted, p.since = true, g.nowMs()
	if g.plots[street] == nil {
		g.plots[street] = map[string]*pPlot{}
	}
	g.plots[street][tree.id] = p
	log.Printf("patches: the dead %s %s on %s is a shared %s plot", tree.class, tree.id, street, class)
	g.broadcast(street, []string{tree.id}, 0)
	g.save()
}

// ---------------------------------------------------------------------------
// Grants: what a dig, harvest or clear gave, until the page has saved it
// ---------------------------------------------------------------------------

func (g *patches) grant(user int64, c string, n int) patchGrant {
	g.seq++
	gr := patchGrant{ID: "pg-" + g.boot + "-" + strconv.FormatUint(g.seq, 36), User: user, C: c, N: n, At: g.nowMs()}
	g.grants = append(g.grants, gr)
	g.trimGrants()
	return gr
}

func (g *patches) trimGrants() {
	cut := g.nowMs() - patchGrantKeep.Milliseconds()
	per := map[int64]int{}
	for _, gr := range g.grants {
		per[gr.User]++
	}
	kept := g.grants[:0]
	for _, gr := range g.grants {
		if gr.At < cut {
			log.Printf("patches: %s for %d was never acknowledged; dropped", gr.ID, gr.User)
			per[gr.User]--
			continue
		}
		if per[gr.User] > patchGrantsPerOne {
			per[gr.User]--
			continue
		}
		kept = append(kept, gr)
	}
	g.grants = kept
}

func (g *patches) resendGrants(cl *Client) {
	for _, gr := range g.grants {
		if gr.User == cl.ID() {
			g.h.SendLocked(cl, map[string]any{"t": "patch_grant", "id": gr.ID, "c": gr.C, "n": gr.N})
		}
	}
}

func (g *patches) handleAck(cl *Client, frame json.RawMessage) {
	var m struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(frame, &m) != nil || !strings.HasPrefix(m.ID, "pg-") || len(m.ID) > 40 {
		return
	}
	for i, gr := range g.grants {
		if gr.ID == m.ID && gr.User == cl.ID() {
			g.grants = append(g.grants[:i], g.grants[i+1:]...)
			g.save()
			return
		}
	}
}

// handleDev (GRODDLE_PATCHES_DEV=1 only): shorter timers, a forced roll and a game day turn for QA,
// since the online page strips its query parameters.
func (g *patches) handleDev(cl *Client, frame json.RawMessage) {
	if !g.dev {
		g.h.ErrorLocked(cl, "bad_frame", "Unknown message.")
		return
	}
	var m struct {
		Clean, Dug, Min, Max, Cycle, Wood int64
		Roll                              *float64
		Day                               bool
	}
	if json.Unmarshal(frame, &m) != nil {
		return
	}
	set := func(dst *int64, v int64) {
		if v >= 1000 {
			*dst = v
		}
	}
	set(&g.timers.clean, m.Clean)
	set(&g.timers.dug, m.Dug)
	set(&g.timers.min, m.Min)
	set(&g.timers.max, m.Max)
	if m.Cycle >= 1000 {
		g.timers.cycle1, g.timers.cycle = m.Cycle, m.Cycle
	}
	set(&g.timers.wood, m.Wood)
	if m.Roll != nil {
		g.devRoll = -1
		if *m.Roll >= 0 && *m.Roll < 1 {
			g.devRoll = *m.Roll
		}
	}
	if m.Day {
		today := g.today()
		for street, room := range g.plots {
			var changed []string
			for key, p := range room {
				if t := p.tree; t != nil {
					t.day, t.hday = today-1, today-1
					if g.advance(street, p, g.nowMs()) {
						changed = append(changed, key)
					}
				}
			}
			sort.Strings(changed)
			g.broadcast(street, changed, 0)
		}
	}
	log.Printf("patches: dev timers %+v, roll %v, day %v (by %d)", g.timers, g.devRoll, m.Day, cl.ID())
	g.h.SendLocked(cl, map[string]any{"t": "patch_dev_ok", "timers": g.timerView(), "roll": g.devRoll})
	g.arm()
}
