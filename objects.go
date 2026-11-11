package main

// Shared street objects (multiplayer phase 4): what one player takes from a
// rock, a resource node or a tree is gone for everyone on the street until it
// comes back, as in the original. The server holds each touched object's
// state (chunks left, uses left, fruit, tending flags), its regrow and respawn
// timers and the rolls the source makes about the object; each player's own
// limits, tools, energy, skills and rewards stay on the client.
//
// Client to server:
//
//	{"t":"obj_sync"}                                    after a join: the street's state, please
//	{"t":"obj_act","req":7,"id":"rock:2","verb":"mine","perks":["luck"],"skill":"gardening_2"}
//	{"t":"obj_mine","id":"rock:2","do":"start"}         a mining swing begins (or "stop": cancelled)
//	{"t":"obj_dig","id":"node:dirt_pile:0","do":"start"} a dig at a Dirt Pile begins (or "stop")
//	{"t":"obj_act","req":8,"id":"tree:1","verb":"poison"}  a Tree Poison pour landed ("antidote" too)
//	{"t":"obj_dust","req":10}                            Fertilidust used: every living tree on the street
//	{"t":"obj_dust","req":11,"id":"tree:2"}              Fertilidust Lite used on that tree (near you)
//	{"t":"obj_dust","req":11,"id":"patch:0"}             …or on the tree in that shared Patch (G564: a plot
//	                                                       key, patches_trees.go; the full jar reaches
//	                                                       every Patch tree of the street too)
//	{"t":"obj_day_dev"}                                  QA only, GRODDLE_TREES_DEV=1: the game day turns now
//	{"t":"obj_dev","id":"pig:0","hunger":5,"tick":true}  QA only, GRODDLE_TREES_DEV=1 (objects_pigs.go pigDev)
//	{"t":"obj_act","req":9,"id":"pig:0","verb":"pet","skill":"animalkinship_2"}  a Piggy (objects_pigs.go):
//	                                                       pet, nibble, feed (perks ["sate"]), eat
//	                                                       ("on":"tree:3" when it ate a tree's fruit)
//	{"t":"obj_act","req":13,"id":"tree:3","verb":"food"}   a player harvested, watered or petted this
//	                                                       fruiting tree: hungry Piggies go for it (no answer;
//	                                                       objects_pigs.go pigsToTree)
//	{"t":"obj_act","req":12,"id":"chicken:1","verb":"squeeze","skill":"animalkinship_2"}  a street animal
//	                                                       (objects_creatures.go): squeeze, incubate
//	                                                       ("c":"chicken_egg"); butterfly:<i> massage,
//	                                                       milk; batterfly:<i> feed ("c", "n")
//
// Server to client:
//
//	{"t":"objs","street":"GA5…","snap":true,"objs":[State…]}  the answer to obj_sync: every
//	                                                       object that differs from fresh ("none":true:
//	                                                       nothing on this street is shared)
//	{"t":"objs","street":"GA5…","objs":[State…]}           a change (to the others on the street)
//	                                                       or a regrowth, respawn or new day (to everyone)
//	{"t":"obj_ok","req":7,"id":"rock:2","verb":"mine","before":State,"obj":State,"chunks":7,"roll":0.31}
//	                                                       (a tree's pet or water: "roll", "success", and
//	                                                       "grew": the fruit the health rise brought back)
//	{"t":"obj_no","req":7,"id":"rock:2","verb":"mine","code":"empty","message":"…","obj":State}
//	{"t":"obj_dust_used","req":10,"c":"fertilidust","use":{…}}  the ledger used the jar's charge
//	                                                       (objects_uses.go; no "use" without a ledger):
//	                                                       the dust lands 3 s on; "revived":["trant_spice_dead:0"]:
//	                                                       the dead trants in shared Patches the full jar
//	                                                       brought back now (all hear "patches")
//	{"t":"obj_dusted","req":10,"ids":["tree:0","tree:3"]}  the trees the dust grew (all hear "objs"; shared
//	                                                       Patch trees by plot key, all hear "patches")
//	{"t":"obj_mine","id":"rock:2","helping":true}             your swing joined someone else's
//	{"t":"obj_dig","id":"node:dirt_pile:0","helping":true}    your dig joined someone else's
//	{"t":"obj_ev","street":"GA5…","id":"pig:0","ev":"happy"}  a Piggy's twice-daily tick ("croak", or
//	                                                       "ate" with "on":"tree:3": it got to that tree
//	                                                       and ate a fruit)
//	{"t":"obj_ev","street":"GA5…","id":"chicken:1","ev":"squeeze","by":7}  someone's verb landed on a
//	                                                       street animal ("massage", "milk", "feed",
//	                                                       "incubate"; "hatched" or "lost" at 60 s)
//
// Every "objs" frame carries "now" (the server's Unix ms, the street animals' shared clock); the
// snapshot says "creatures":true when this server shares the street animals, "animals" names them
// and "spots" gives each Batterfly's, Heli Kitty's and Crab's place ({"batterfly:0":[x,y]}).
//
// A Wood Tree is tree:<i> too: {"id":"tree:3","maturity":5,"count":1}.
//
// Tree death (G334): a street tree is everyone's, so its poison and its death are the server's.
// A pour of Tree Poison (trant_base / wood_tree apply_poison, the potion's onPourComplete) starts
// the source's three-minute die timer (include/trants.js applyPoison 1268-1274, wood_tree.js
// 578-586); anyone's antidote (apply_antidote, cure) stops it; at the end the tree dies for
// everyone, on the street or off it (die 734-773: the dead trant class; wood_tree.js onDie: a
// Patch and 25 Planks). A tree State then reads "poison": ms left and "by": who poured it, and
// "dead": true once it died. A dead tree refuses every verb ("code":"dead") and never comes back
// while the server runs: what grows in its plot is each player's own Patch, except on a street whose
// Patches are shared (patches.go, -patches on): there killTree hands the plot to patches.adopt. The poisoner is credited the dead Wood Tree's 25 Planks (credits.go "take").
// A rooked tree at health 1 dies too (trants.js onRooked 1151); solo keeps it at 1.
// The source's hourly neglect (onInterval 576-664: health falls every 15 to 120 minutes by
// health_change_rate unless petted and watered in time, onMaturityCycle's 10-35% death roll at
// health 1-3, a 2% a day old-age roll) is not run, in solo or here: register G45 is Austin's open
// call ("later; a gentle hourly-care version"). With real players it would reward a busy street
// (any player's pet and water count, and a pair resets the fall clock) and kill nearly every
// tree on the empty ones, which is why it waits.
//
// Help digging (G329, dirt_pile.js addDigger 153-166, removeDigger 497-521, onDigComplete
// 293-485): a Dirt Pile keeps a dig crew the way a rock keeps miners. Each digger's helpers are
// the others who started digging while that dig ran (each counts once); when a dig lands its
// answer carries "help":{"n":2,"items":1,"class":"earth","loam":false,"by":[…]}: 1 more item with
// one helper, 2 with three, 3 with six; a 5% (two helpers) or 15% (six) chance that the dig's
// yield is Loam, normalised against the skill's own drop chance (which the server rolls too: "roll"
// is the client's first draw); the bonus items are Loam when either Loam came. Peat Bogs have no
// helpers in the source (peat_1.js), so they have none here.
//
// State: {"id":"rock:2","chunks":43,"mega":false,"in":0,"miners":[3,7]} (in: ms to the
// respawn; miners: who is swinging at it now, so the verb reads "Help mine"),
// {"id":"node:peat_1:0","units":3,"in":120000} (in: ms to the next +1; a Paper
// Tree adds "petted","watered","reset"; a Dirt Pile "diggers"), {"id":"tree:1","fruit":40,"cap":98,
// "health":8,"maturity":7,"petted":true,"watered":false}, {"id":"pig:0","mood":1,"hunger":3,
// "petted":false,"daily":true,"croak":false,"in":3600000,"lag":0,"hold":0} (in: ms to its next
// twice-daily tick; lag and hold: its shared path, as a Chicken's; "trip":{"x0","x1","c0","leg","eat"}
// while it walks to food and home, objects_pigs.go pigTrip),
// {"id":"chicken:0","lag":2500,"hold":0,"sq":1,"inc":{…}} (objects_creatures.go).
//
// Ids are the client's own (js/street-objects.js): rock:<i>, tree:<i>,
// node:<class>:<n>. data/objects.json (tools/build-realtime-objects.mjs) lays
// every street out as game.js does and carries the rule numbers from the
// game's modules, so the server checks the class and the place of every
// object a player names, and that the player stands near it.
//
// Help mine (items/include/mining.js 8-122, 327-383): a player who starts
// mining a rock while others swing at it helps each of them; when a swing
// lands, each help it had adds min(i+1, 4) chunks (up to 9 helps) to the take,
// from the rock, and the answer names the helpers ("help":{"bonus":3,"by":[…]}).
//
// A tree the Rook rooks (rook.go, through the hooks below) loses its fruit and
// health for everyone, and refuses harvest, pet and water ("code":"rooked")
// until it is revived. While it stays rooked its fruit grows back, as the
// source's rooked sequence and complaint run plusHealth (see rookFruit).
//
// Fruit (G361): a shared trant regrows fruit only as the source's do, never at
// the game day turn (nothing in GSJS refills trees then; the day clears only
// the tending flags, the remake's tending window). Fruit comes back when the
// tree's health rises: pet and water, once each a window, by any players
// (performTending 670-684: plusHealth, whose onFruitCycle 1043-1054 pushes
// fruitProdMap[maturity][health] up to the capacity, pushFruit 466-473), and
// with Fertilidust (onFertilidust 1236-1244: a maturity step below 10, health
// 9 then plusHealth). The amounts are the server's own tables (rules.prod, the
// game's trants.json fruitProdMap); a client says only that a pet, water or
// dust landed. Fertilidust's jar and Tree Poison's and its Antidote's bottles are confirmed and
// used through the site's ledger before the trees change (objects_uses.go, G335 slice 4).
//
// What a player takes is theirs to apply, and the site's ledger is told what
// the take was (credits.go, objCredit), so the save can show it outside the
// earning budget: a rock's whole take, a tree's or Paper Tree's fruit, a Wood
// Tree's planks, and a resource node's yield with no skill bonus (bonuses,
// super harvests and drops are the client's and go through the budget).
//
// The server forgets an object once it is fresh again. What it has not forgotten
// outlives a restart: dead and poisoned trees, street Piggies, what was taken
// and when it comes back are kept in -objects-file (objects_persist.go).

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"
)

//go:embed data/objects.json
var objectsJSON []byte

const (
	// mining.js mineRock (the remake's 45 s; include/mining.js:421 waits 8 min,
	// plus up to 16 more once 80 chunks were taken). Solo and multiplayer keep
	// the same pace.
	rockRespawn = 45 * time.Second
	// How far from an object the server still takes an action on it: about a
	// screen (game.js VIEW_W 960), since a verb starts from a click and the
	// last move can trail the swing by a frame or two.
	objReachX = 700
	objReachY = 500

	// The rooked tree's fruit push (rookFruit): onRookedStep 6 at 12 s (events_add 12), then
	// onRookedComplaint's drop and lift, 2000 ms apart (apiSetTimer 2000).
	rookFruitFirst = 12 * time.Second
	rookFruitEvery = 4 * time.Second

	objBurst = 6.0 // actions and syncs in a burst
	objEvery = 4.0 // then this many a second

	// js/game-day.js: Glitch's game day is four real hours from GAME_EPOCH_MS.
	gameEpochMs = 1_238_562_000_000
	gameDayMs   = 14_400_000

	// include/trants.js applyPoison 1268-1274 (wood_tree.js 578-586): var duration = 3*60.
	treePoison = 3 * time.Minute
	// wood_tree.js onDie: createItemStack('plank', 25) beside the Patch (js/tree-poison.js DEAD_WOOD_PLANKS).
	deadWoodPlanks = 25
)

var (
	objIDRe  = regexp.MustCompile(`^(?:(rock|tree|pig|chicken|butterfly|batterfly|kitty|crab|dragon):(\d{1,2})|node:([a-z0-9_]{1,24}):(\d{1,2}))$`)
	objVerbs = map[string]bool{"mine": true, "harvest": true, "pet": true, "water": true, "scrape": true, "scoop": true, "dig": true,
		"poison": true, "antidote": true, "nibble": true, "feed": true, "eat": true, "squeeze": true, "massage": true, "milk": true, "incubate": true,
		"wait": true, "food": true, "pocket": true, "flatter": true, "hug": true, "insult": true, "pinch": true, "name": true, "capture": true, "youth": true, "pickup": true}
	objPerks = map[string]bool{"luck": true, "expert": true, "whisperer": true, "paper8": true, "ak6": true, "sate": true, "guano1": true, "guano2": true, "ak5": true, "herd": true}
)

// The source's refusals (items/*.js), with the remake's copy where the source
// only hides the verb.
var notReady = map[string]string{
	"mortar_barnacle": "This barnacle is not ready yet!",                // mortar_barnacle.js:64
	"ice_knob":        "This Ice Nubbin is not ready yet!",              // ice_knob.js:63
	"jellisac":        "This Jellisac is not ready yet!",                // jellisac.js:54
	"dirt_pile":       "There isn't enough dirt left to dig.",           // dirt_pile.js:113
	"peat_1":          "No more peat here!",                             // peat_1.js:143
	"paper_tree":      "There's nothing to pick, cowboy!",               // paper_tree.js:252
	"npc_salmon":      "The Salmon is gone. Another will turn up soon.", // js/resource-nodes.js (apiDelete on pocket)
	"rock":            "This is not the rock you are looking for.",      // include/mining.js:216
	"tree":            "There's nothing to pick, cowboy.",               // include/trants.js:235
}

// Tree Poison copy (js/tree-poison.js POISON_COPY, from trant_base and the potions; the remake's
// plain words where the source only hides the verb).
const (
	noPoisonedTree = "There are no poisoned trees nearby." // tree_poison_antidote.js canPour
	alreadyPoison  = "That tree is already poisoned."
	treeIsDead     = "This tree has died."
)

// ---------------------------------------------------------------------------
// The layout and rules (data/objects.json)
// ---------------------------------------------------------------------------

type rockRule struct {
	Type   string `json:"type"`
	Min    int    `json:"min"`
	Max    int    `json:"max"`
	Chunks int    `json:"chunks"`
}

type nodeRule struct {
	Model   string   `json:"model"`
	Min     int      `json:"min"`
	Max     int      `json:"max"`
	Initial int      `json:"initial"`
	Ready   int      `json:"ready"`
	Verbs   []string `json:"verbs"`
	After   *int     `json:"after"`
	Consume int      `json:"consume"`
	Regrow  []int64  `json:"regrow"` // min ms, max ms, step ms
	Tend    *struct {
		Refill  int   `json:"refill"`
		ResetMs int64 `json:"resetMs"`
	} `json:"tend"`
}

type objRules struct {
	Rocks map[string]rockRule `json:"rocks"`
	Mega  struct {
		Chunks int                `json:"chunks"`
		Chance map[string]float64 `json:"chance"`
	} `json:"mega"`
	Nodes   map[string]nodeRule           `json:"nodes"`
	Harvest map[string]int                `json:"harvest"`
	Care    map[string]map[string]float64 `json:"care"`
	Trants  map[string][][]int            `json:"trants"`
	// Prod is each trant's fruitProdMap (include/trants.js onFruitCycle 1043-1054): what one
	// health rise pushes back on, by maturity and health.
	Prod map[string][][]int `json:"prod"`
	// Dig is catalog_skill_packages.js dig_dirt_pile by skill: drop_chance (percent) and bonus_amount.
	Dig map[string]struct {
		Drop  float64 `json:"drop"`
		Bonus int     `json:"bonus"`
	} `json:"dig"`
	// Pig is the Animal Kinship success rates (percent) for a Piggy's pet and nibble by rank.
	Pig map[string]map[string]float64 `json:"pig"`
	// Ak is Animal Kinship by rank (no skill, then ranks 1-7): [interval_limit, success_rate,
	// bonus_amount] for npc_chicken:squeeze, npc_butterfly:massage and :milk and npc_piggy:nibble
	// (objects_creatures.go).
	Ak map[string][][]float64 `json:"ak"`
	// Incubate is a Chicken's incubation: what hatches from each egg, and how long it sits (ms).
	Incubate struct {
		Hatches map[string]string `json:"hatches"`
		Ms      int64             `json:"ms"`
	} `json:"incubate"`
	// PigTrip is a street Piggy's walk to food and home (the game's js/street-creatures.js
	// PIGGY_TRIP, objects_pigs.go startTrip).
	PigTrip pigTripRule `json:"pigtrip"`
	// Holds is how long a landed verb on a Heli Kitty or a Crab holds it for everyone (ms):
	// "kittyPet" its sleep (npc_kitty_chicken.js 240-279: 3sleepStart then one sleep cycle),
	// "crabVerb" its menu wait (npc_crab onWaitEnd: walks on 1.5 s later).
	Holds map[string]int64 `json:"holds"`
}

// objSpot is one object as laid out: its kind, class, place and (trees) its fresh maturity and health.
type objSpot struct {
	id       string
	kind     string // rock | node | tree | wood (a Wood Tree: tree:<i> like the trants) | pig | chicken | butterfly | batterfly
	class    string
	x, y     float64
	maturity int
	health   int
	fly      bool    // a flying Butterfly (objects_creatures.go)
	index    int     // pig: its spawn index (the seed its stops are rolled from, pigpath.go)
	rng      float64 // pig: how far it walks each way of its spot (animalPatrolRange)
}

type objWorld struct {
	rules   objRules
	streets map[string]map[string]*objSpot
	// trunks: each street's tree x, where a Piggy never stops to root (pigpath.go).
	trunks map[string][]float64
	// sad: streets where a Piggy is sad (include/animal_sadness.js locationIsDepressing); its tick
	// waits and its verbs refuse (npc_piggy.js onTwiceDaily 1834, verb conditions).
	sad map[string]bool
}

func loadObjWorld(raw []byte) (*objWorld, error) {
	var file struct {
		Rules   objRules `json:"rules"`
		Streets map[string]struct {
			Rocks [][]any   `json:"rocks"`
			Trees [][]any   `json:"trees"`
			Nodes [][]any   `json:"nodes"`
			Pigs  [][]any   `json:"pigs"`
			Chick [][]any   `json:"chickens"`
			Bfly  [][]any   `json:"butterflies"`
			Bat   [][]any   `json:"batterflies"`
			Kitty [][]any   `json:"kitties"`
			Crab  [][]any   `json:"crabs"`
			Drag  [][]any   `json:"dragons"`
			Trunk []float64 `json:"trunks"`
			Sad   bool      `json:"sad"`
		} `json:"streets"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	w := &objWorld{rules: file.Rules, streets: map[string]map[string]*objSpot{}, sad: map[string]bool{}, trunks: map[string][]float64{}}
	w.rules.PigTrip.fill()
	num := func(v any) (float64, bool) { f, ok := v.(float64); return f, ok }
	str := func(v any) (string, bool) { s, ok := v.(string); return s, ok }
	for tsid, row := range file.Streets {
		spots := map[string]*objSpot{}
		for _, r := range row.Rocks {
			i, ok1 := num(r[0])
			class, ok2 := str(r[1])
			x, ok3 := num(r[2])
			y, ok4 := num(r[3])
			if !(ok1 && ok2 && ok3 && ok4) || w.rules.Rocks[class].Chunks == 0 {
				return nil, fmt.Errorf("%s: bad rock %v", tsid, r)
			}
			id := "rock:" + strconv.Itoa(int(i))
			spots[id] = &objSpot{id: id, kind: "rock", class: class, x: x, y: y}
		}
		for _, r := range row.Trees {
			if len(r) < 6 {
				return nil, fmt.Errorf("%s: bad tree %v", tsid, r)
			}
			i, ok1 := num(r[0])
			class, ok2 := str(r[1])
			x, ok3 := num(r[2])
			y, ok4 := num(r[3])
			m, ok5 := num(r[4])
			hl, ok6 := num(r[5])
			kind := "tree"
			if class == "wood_tree" {
				kind = "wood"
			}
			if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) || (kind == "tree" && w.rules.Trants[class] == nil) || (kind == "wood" && (m < 1 || m > 6)) {
				return nil, fmt.Errorf("%s: bad tree %v", tsid, r)
			}
			id := "tree:" + strconv.Itoa(int(i))
			spots[id] = &objSpot{id: id, kind: kind, class: class, x: x, y: y, maturity: int(m), health: int(hl)}
		}
		for _, r := range row.Nodes {
			key, ok1 := str(r[0])
			class, ok2 := str(r[1])
			x, ok3 := num(r[2])
			y, ok4 := num(r[3])
			if _, known := w.rules.Nodes[class]; !(ok1 && ok2 && ok3 && ok4) || !known {
				return nil, fmt.Errorf("%s: bad node %v", tsid, r)
			}
			id := "node:" + key
			spots[id] = &objSpot{id: id, kind: "node", class: class, x: x, y: y}
		}
		for _, r := range row.Pigs {
			if len(r) < 3 {
				return nil, fmt.Errorf("%s: bad pig %v", tsid, r)
			}
			i, ok1 := num(r[0])
			x, ok2 := num(r[1])
			y, ok3 := num(r[2])
			if !(ok1 && ok2 && ok3) {
				return nil, fmt.Errorf("%s: bad pig %v", tsid, r)
			}
			// Its walk each way (an older layout has none: ANIMAL_PATROL.piggies).
			rng := float64(pigPatrol)
			if len(r) > 3 {
				if v, ok := num(r[3]); ok && v >= 0 {
					rng = v
				}
			}
			id := "pig:" + strconv.Itoa(int(i))
			spots[id] = &objSpot{id: id, kind: "pig", class: "npc_piggy", x: x, y: y, index: int(i), rng: rng}
		}
		for kind, rows := range map[string][][]any{"chicken": row.Chick, "butterfly": row.Bfly, "batterfly": row.Bat, "kitty": row.Kitty, "crab": row.Crab, "dragon": row.Drag} {
			for _, r := range rows {
				if len(r) < 3 {
					return nil, fmt.Errorf("%s: bad %s %v", tsid, kind, r)
				}
				i, ok1 := num(r[0])
				x, ok2 := num(r[1])
				y, ok3 := num(r[2])
				if !(ok1 && ok2 && ok3) {
					return nil, fmt.Errorf("%s: bad %s %v", tsid, kind, r)
				}
				fly := false
				if len(r) > 3 {
					f, _ := num(r[3])
					fly = f == 1
				}
				id := kind + ":" + strconv.Itoa(int(i))
				class := "npc_" + kind
				switch kind {
				case "kitty":
					class = "npc_kitty_chicken"
				case "dragon":
					class = "npc_fire_dragon"
				}
				spots[id] = &objSpot{id: id, kind: kind, class: class, x: x, y: y, fly: fly, index: int(i)}
			}
		}
		if row.Sad {
			w.sad[tsid] = true
		}
		if len(row.Trunk) > 0 {
			w.trunks[tsid] = row.Trunk
		}
		w.streets[tsid] = spots
	}
	return w, nil
}

// capAt is include/trants.js updateFruitCap: fruitCapMap[maturity-1][health-1], 0 outside the table.
func (w *objWorld) capAt(class string, maturity, health int) int {
	t := w.rules.Trants[class]
	if maturity < 1 || health < 1 || maturity > len(t) || health > len(t[maturity-1]) {
		return 0
	}
	return t[maturity-1][health-1]
}

// prodAt is include/trants.js onFruitCycle's fruitProdMap[maturity-1][health-1], 0 outside the table.
func (w *objWorld) prodAt(class string, maturity, health int) int {
	t := w.rules.Prod[class]
	if maturity < 1 || health < 1 || maturity > len(t) || health > len(t[maturity-1]) {
		return 0
	}
	return t[maturity-1][health-1]
}

var sharedWorld *objWorld

func init() {
	w, err := loadObjWorld(objectsJSON)
	if err != nil {
		panic("realtime: data/objects.json: " + err.Error())
	}
	sharedWorld = w
	modules = append(modules, newObjects)
}

// ---------------------------------------------------------------------------
// Live state
// ---------------------------------------------------------------------------

// liveObj is one object someone has touched. Only the fields of its kind are used.
type liveObj struct {
	spot *objSpot

	chunks   int  // rock
	mega     bool // rock: came back at 150
	nextMega bool // rock: will come back at 150
	units    int  // node
	// treeCore (trees.go): a tree's fruit, capacity, health, maturity, tending flags and the game
	// day they belong to (wood: its harvesters' day); petted and watered are a Paper Tree's too, petted
	// a Piggy's.
	treeCore
	resetAt   time.Time      // Paper Tree: tending flags clear
	woodCount int            // wood: harvest_count (wood_tree.js 732-782)
	woodBy    map[int64]bool // wood: who harvested it this game day (the source's intervals[today][pc])

	// tree, wood: Tree Poison's die timer and who poured it; dead once it ran out (or the Rook
	// struck a tree at health 1), with deadBy the poisoner (0: the Rook).
	poisonEnd  time.Time
	poisonBy   int64
	poisonStop func() bool
	dead       bool
	deadBy     int64

	// pig (objects_pigs.go): npc_piggy.js instanceProps mood and hunger, is_daily, has_been_petted,
	// the croak, the next onTwiceDaily tick, and who petted it on which game day (petters).
	mood, hunger int
	daily        bool
	croak        bool
	tickAt       time.Time
	petters      map[int64]int64
	eatingFood   string         // pig: the shared ground stack it is on its way to eat (objects_pigs.go pigFood)
	trip         *pigTrip       // pig: its walk to food and home (objects_pigs.go startTrip)
	userName     string         // pig, chicken, butterfly, batterfly: its name, everyone's (animal_naming.js user_name; objects_pigs.go nameAnimal)
	namer        int64          // who named it (pc_namer)
	namedAt      time.Time      // when (named_on)
	goneDay      int64          // pig: the game day a capture or a youth potion took it off the street (0: here; objects_pigs_gone.go)
	goneWhy      string         // pig: "capture" | "youth"
	crops        map[string]int // pig: crops fed per seed class, toward a Plop (npc_piggy.js feed 685-693; objects_pigs_plops.go)
	plops        []plopEntry    // pig: Plops still to come, oldest first (doPlop 1315-1350)
	plopStop     func() bool    // pig: the timer for the next Plop
	tripStop     func() bool    // pig: the timer for the trip's next step

	// chicken, butterfly, batterfly (objects_creatures.go) and a Piggy's uses: each player's uses of
	// each verb on usesDay (package_intervals[day][pc]) and today's tally by anyone; lag and holdEnd
	// still its shared path; a Chicken's egg, its owner and when it hatches; a Batterfly's food counter.
	uses    map[int64]map[string]int
	usesDay int64
	tally   map[string]int
	lag     int64
	holdEnd time.Time
	incEgg  string
	incBy   int64
	incEnd  time.Time
	incStop func() bool
	food    int
	guano   int // batterfly: the hourly Guano lying under it (objects_guano.go)

	// rock: who is mining it now, in the order they started, and each miner's helpers with how
	// many times each helped (mining.js addMiner 68-91, removeMiner 93-122). A Dirt Pile keeps its
	// diggers and their helpers here too (dirt_pile.js addDigger 153-166: each helper once).
	miners  []int64
	helpers map[int64]map[int64]int

	due  time.Time   // rock respawn or node's next +1
	stop func() bool // that timer
	// resetStop is the Paper Tree's flag reset timer.
	resetStop func() bool
	// rookStop is a rooked tree's next fruit push (rookFruit); rookSteady: its fruit stopped
	// changing, so no push is due until it is rooked again or its health or maturity moves.
	rookStop   func() bool
	rookSteady bool
}

type objBucket struct {
	tokens float64
	at     time.Time
}

type objects struct {
	h       *Hub
	w       *objWorld
	roll    func() float64
	streets map[string]map[string]*liveObj
	// captures: each player's last Piggy capture per street (objects_pigs_gone.go, G605).
	captures map[string]map[int64]time.Time
	limits   map[int64]*objBucket
	dayStop  func() bool
	pigStop  func() bool // the one timer for the next Piggy tick (objects_pigs.go)
	pigDue   time.Time
	// dev (GRODDLE_TREES_DEV=1): a poison pour may name its own die time ("ms"), for QA.
	dev bool
	// Fertilidust, Tree Poison and Antidote through the site's ledger (objects_uses.go).
	uses objUses

	// A Batterfly's hourly Guano (objects_guano.go): when the clocks started, the time each street's
	// piles were last settled to, and the tick timer of each street someone stands on.
	guanoStart time.Time
	guanoAt    map[string]time.Time
	guanoStop  map[string]func() bool

	// The state file (objects_persist.go): "" keeps everything in memory only.
	path    string
	saveAt  bool              // a save is due
	resumed bool              // the file's timers are armed
	bogs    map[int64]*bogRun // Peter out Peat's nine seconds, by player (objects_peat.go)
}

func newObjects(h *Hub) Module {
	o := &objects{h: h, w: sharedWorld, roll: rand.Float64, streets: map[string]map[string]*liveObj{}, limits: map[int64]*objBucket{}, bogs: map[int64]*bogRun{},
		dev: os.Getenv("GRODDLE_TREES_DEV") == "1", resumed: true}
	o.initUses()
	if o.path = statePath(objectsFileForTest, objectsFileFlag, "realtime-objects.json"); o.path != "" {
		if err := o.load(); err != nil {
			log.Printf("objects %s: %v (street objects are kept in memory only)", o.path, err)
			o.path = ""
		}
	}
	if o.guanoStart.IsZero() {
		// The Guano clocks start now (objects_guano.go): a new world, or a state file from before
		// them. Written at once, so a crash before the first change does not start them again.
		o.guanoStart = time.UnixMilli(h.Now().UnixMilli())
		o.saveNow()
	}
	return o
}

func (o *objects) Register(r *Registry) {
	r.Handle("obj_sync", o.kept(o.handleSync))
	r.Handle("obj_act", o.kept(o.handleAct))
	r.OnConnect(func(*Client) { o.resume() })
	r.OnShutdown(func() { o.save() })
	r.OnDisconnect(func(cl *Client) { delete(o.limits, cl.ID()); delete(o.uses.tools, cl.ID()) })
	r.Handle("obj_mine", o.kept(o.handleMine))
	r.Handle("obj_dig", o.kept(o.handleDig))
	r.Handle("obj_dev", o.kept(o.pigDev))
	r.Handle("obj_dust", o.kept(o.handleDust))
	r.Handle("obj_day_dev", o.kept(o.dayDev))
	r.Handle("obj_guano_dev", o.kept(o.guanoDev))
	r.Handle("bog_start", o.kept(o.handleBogStart))
	r.OnLeave(o.leftStreet)
	r.Provide("objects.treeClass", o.treeClass)
	r.Provide("objects.rookTree", o.rookTree)
	r.Provide("objects.rookCreature", o.rookCreature)
	r.Provide("objects.pigFood", o.pigFood)
	r.Provide("objects.withTool", o.withGardenTool)
}

// treeClass is the class of the tree an id names on a street ("" when it names no tree there, or a
// tree that died), so the Rook can check the tree a client pairs with one of its keys (rook_sync "objs").
func (o *objects) treeClass(street, id string) string {
	o.resume()
	spot := o.w.streets[street][id]
	if spot == nil || (spot.kind != "tree" && spot.kind != "wood") {
		return ""
	}
	if ob := o.streets[street][id]; ob != nil && ob.dead {
		return ""
	}
	return spot.class
}

// rookTree is items/include/trants.js onRooked 1140-1151 on the shared tree, as the client does on
// its copy (js/rook-attack.js rookTrantState): every fruit gone, health down 2 (1 at health 2). A tree
// already at health 1 dies, as in the source (1151; solo keeps it at 1). Everyone on the street is
// told. False when the id names no living tree there.
func (o *objects) rookTree(street, id string) bool {
	o.resume()
	defer o.saveSoon()
	spot := o.w.streets[street][id]
	if spot == nil || spot.kind != "tree" {
		return false
	}
	ob := o.get(street, spot)
	if ob.dead {
		return false
	}
	if o.w.treeRook(&ob.treeCore, spot.class) {
		o.killTree(street, ob, 0)
		return true
	}
	o.broadcast(street, 0, ob)
	// onRookedStep 6 (12 s on) is the sequence's last plusHealth; the complaint follows it.
	o.armRookFruit(street, ob, rookFruitFirst)
	return true
}

// rookFruit is the fruit a rooked trant grows back while it waits (include/trants.js 1170-1232):
// onRookedStep 2 and 6 run plusHealth, and onRookedComplaint then drops its health one and lifts it
// back every 2 s until it is revived, each lift a plusHealth whose onFruitCycle pushes
// fruitProdMap[maturity][health], each drop an updateFruitCap that trims the fruit to the lower
// health's capacity (minusHealth). The server keeps the sequence's net health (two down, as the
// source's comment at 1144 says) and runs one drop and lift every 4 s from step 6 on, so a
// revived tree has the fruit it grew while rooked (unRook leaves it; trants have no onRevived).
// Harvest, pet and water still wait for the revival. The push stops once the fruit stops
// changing, and goes on (get) after a restart or a change to the tree.
func (o *objects) armRookFruit(street string, ob *liveObj, d time.Duration) {
	if ob.rookStop != nil {
		ob.rookStop()
	}
	ob.rookSteady = false
	ob.rookStop = o.h.After(d, func() {
		ob.rookStop = nil
		if o.streets[street][ob.spot.id] != ob || ob.dead || !o.rooked(street, ob.spot.id) {
			return
		}
		class := ob.spot.class
		before := ob.fruit
		low := o.w.capAt(class, ob.maturity, ob.health-1)
		ob.cap = o.w.capAt(class, ob.maturity, ob.health)
		ob.fruit = min(ob.cap, min(ob.fruit, low)+o.w.prodAt(class, ob.maturity, ob.health))
		if ob.fruit == before {
			ob.rookSteady = true
			return
		}
		o.broadcast(street, 0, ob)
		o.saveSoon()
		o.armRookFruit(street, ob, rookFruitEvery)
	})
}

// rooked asks the Rook whether a shared tree is rooked: harvest, pet and water wait for its revival
// (trants.js wantsWater 1120, wantsPet 1131, and its verbs while rooked 940).
func (o *objects) rooked(street, id string) bool {
	fn, ok := o.h.ProvidedLocked("rook.treeRooked").(func(string, string) bool)
	return ok && fn(street, id)
}

func (o *objects) allow(cl *Client) bool {
	now := o.h.Now()
	b := o.limits[cl.ID()]
	if b == nil {
		b = &objBucket{tokens: objBurst, at: now}
		o.limits[cl.ID()] = b
	}
	b.tokens = math.Min(objBurst, b.tokens+now.Sub(b.at).Seconds()*objEvery)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (o *objects) today() int64 {
	return int64(math.Floor(float64(o.h.Now().UnixMilli()-gameEpochMs) / gameDayMs))
}

func (o *objects) randInt(lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + int(math.Floor(o.roll()*float64(hi-lo+1)))
}

// fresh makes an object as nobody has touched it.
func (o *objects) fresh(spot *objSpot) *liveObj {
	ob := &liveObj{spot: spot}
	switch spot.kind {
	case "rock":
		ob.chunks = o.w.rules.Rocks[spot.class].Chunks
	case "node":
		ob.units = o.w.rules.Nodes[spot.class].Initial
	case "tree":
		ob.maturity, ob.health = spot.maturity, spot.health
		ob.cap = o.w.capAt(spot.class, ob.maturity, ob.health)
		ob.fruit = ob.cap
		ob.day = o.today()
	case "wood":
		ob.maturity, ob.day = spot.maturity, o.today()
	case "pig":
		o.freshPig(ob)
	}
	return ob
}

func (o *objects) isFresh(ob *liveObj) bool {
	s := ob.spot
	switch s.kind {
	case "rock":
		return ob.chunks == o.w.rules.Rocks[s.class].Chunks && !ob.mega && ob.stop == nil && len(ob.miners) == 0
	case "node":
		return ob.units == o.w.rules.Nodes[s.class].Initial && !ob.petted && !ob.watered && ob.stop == nil && ob.resetStop == nil && len(ob.miners) == 0
	case "tree":
		return ob.fruit == ob.cap && ob.maturity == s.maturity && ob.health == s.health && !ob.petted && !ob.watered && !ob.dead && ob.poisonEnd.IsZero()
	case "wood":
		return ob.maturity == s.maturity && ob.woodCount == 0 && len(ob.woodBy) == 0 && !ob.dead && ob.poisonEnd.IsZero()
	}
	if isCreature(s.kind) {
		return o.creatureFresh(ob)
	}
	// A Piggy keeps its own clock once the street has one: never forgotten.
	return false
}

// get returns the live object for a spot (fresh if nobody has touched it), trees rolled to today.
func (o *objects) get(street string, spot *objSpot) *liveObj {
	room := o.streets[street]
	if room == nil {
		room = map[string]*liveObj{}
		o.streets[street] = room
	}
	ob := room[spot.id]
	if ob == nil {
		ob = o.fresh(spot)
		room[spot.id] = ob
		if spot.kind == "tree" {
			o.armDay()
		}
		if spot.kind == "pig" {
			o.armPigs()
		}
	}
	if spot.kind == "pig" {
		o.pigBackIfDue(ob) // G605: a gone Piggy is back after the game day turned (objects_pigs_gone.go)
	}
	if spot.kind == "tree" {
		o.newDay(ob)
		// A rooked tree whose fruit push is not running (a restart, or a change since it settled).
		if !ob.dead && ob.rookStop == nil && !ob.rookSteady && o.rooked(street, spot.id) {
			o.armRookFruit(street, ob, rookFruitEvery)
		}
	}
	if spot.kind == "chicken" || spot.kind == "butterfly" || spot.kind == "pig" || spot.kind == "kitty" {
		// Rooked: it lies where the Rook struck it until it is revived (objects_creatures.go rookHoldCreature).
		if o.rookHoldCreature(street, ob) && ob.trip != nil {
			// A Piggy on its way to food stops there and walks on when revived: its trip's clock moved.
			o.armTrip(street, ob)
		}
	}
	if spot.kind == "wood" && ob.day < o.today() {
		// A new game day: everyone may harvest it again (canHarvest by intervals[today]).
		ob.day, ob.woodBy = o.today(), nil
	}
	return ob
}

// forget drops an object that is fresh again.
func (o *objects) forget(street string, ob *liveObj) {
	if !o.isFresh(ob) {
		return
	}
	room := o.streets[street]
	delete(room, ob.spot.id)
	if len(room) == 0 {
		delete(o.streets, street)
	}
}

func (o *objects) state(ob *liveObj) map[string]any {
	s := ob.spot
	now := o.h.Now()
	left := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return max(1, t.Sub(now).Milliseconds())
	}
	switch s.kind {
	case "rock":
		m := map[string]any{"id": s.id, "chunks": ob.chunks, "mega": ob.mega, "in": left(ob.due)}
		if len(ob.miners) > 0 {
			m["miners"] = append([]int64(nil), ob.miners...)
		}
		return m
	case "node":
		m := map[string]any{"id": s.id, "units": ob.units, "in": left(ob.due)}
		if o.w.rules.Nodes[s.class].Tend != nil {
			m["petted"], m["watered"], m["reset"] = ob.petted, ob.watered, left(ob.resetAt)
		}
		if len(ob.miners) > 0 {
			m["diggers"] = append([]int64(nil), ob.miners...)
		}
		return m
	case "pig":
		hold := int64(0)
		if now := o.h.Now(); ob.holdEnd.After(now) {
			hold = ob.holdEnd.Sub(now).Milliseconds()
		}
		m := map[string]any{"id": s.id, "mood": ob.mood, "hunger": ob.hunger, "petted": ob.petted, "daily": ob.daily, "croak": ob.croak, "in": left(ob.tickAt), "lag": ob.lag, "hold": hold}
		if t := ob.trip; t != nil {
			// Its walk to food and home, on its path's clock (objects_pigs.go startTrip).
			m["trip"] = map[string]any{"x0": t.X0, "x1": t.X1, "c0": t.C0, "leg": t.Leg, "eat": t.Eat}
		}
		if ob.userName != "" {
			o.nameState(ob, m)
		}
		if ob.pigGone() {
			m["gone"] = true // a capture or a youth potion took it off the street until the next game day (G605)
		}
		return m
	case "wood":
		return o.deathState(ob, map[string]any{"id": s.id, "maturity": ob.maturity, "count": ob.woodCount})
	case "chicken", "butterfly", "batterfly", "kitty", "crab", "dragon":
		return o.creatureState(ob)
	default:
		return o.deathState(ob, map[string]any{"id": s.id, "fruit": ob.fruit, "cap": ob.cap, "health": ob.health, "maturity": ob.maturity, "petted": ob.petted, "watered": ob.watered})
	}
}

// deathState adds a tree's poison ("poison": ms left, "by": who poured it) and death ("dead", "by").
func (o *objects) deathState(ob *liveObj, m map[string]any) map[string]any {
	if ob.dead {
		m["dead"] = true
		if ob.deadBy > 0 {
			m["by"] = ob.deadBy
		}
	} else if !ob.poisonEnd.IsZero() {
		m["poison"] = max(1, ob.poisonEnd.Sub(o.h.Now()).Milliseconds())
		m["by"] = ob.poisonBy
	}
	return m
}

func (o *objects) broadcast(street string, except int64, obs ...*liveObj) {
	list := make([]map[string]any, 0, len(obs))
	for _, ob := range obs {
		list = append(list, o.state(ob))
	}
	o.h.BroadcastStreetLocked(street, map[string]any{"t": "objs", "street": street, "now": o.h.Now().UnixMilli(), "objs": list}, except)
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

func (o *objects) handleSync(cl *Client, frame json.RawMessage) {
	street := cl.Street()
	if street == "" || !o.allow(cl) {
		return
	}
	spots := o.w.streets[street]
	if spots == nil {
		o.h.SendLocked(cl, map[string]any{"t": "objs", "street": street, "snap": true, "none": true, "objs": []any{}})
		return
	}
	// A street's Piggies start their clocks the first time anyone is there (objects_pigs.go).
	o.wakePigs(street)
	// The hourly Guano under its Batterflies, up to now (objects_guano.go): the others there see
	// what grew, and the street's next tick is armed while someone stands on it.
	o.guanoGrew(street, cl.ID())
	o.armGuano(street)
	ids := make([]string, 0, len(o.streets[street]))
	for id := range o.streets[street] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	list := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		ob := o.get(street, spots[id])
		list = append(list, o.state(ob))
	}
	// A rooked animal nobody has touched lies where the Rook struck it too (rookHoldCreature).
	for _, id := range o.rookedCreatures(street, spots) {
		list = append(list, o.state(o.get(street, spots[id])))
	}
	// The street animals this server shares here (objects_creatures.go): the client shares only these.
	// A Batterfly's place comes from the server ("spots"), as its planner group follows groups a
	// player's own projects add (street-jobs): the same place for everyone.
	animals := make([]string, 0)
	places := map[string][]float64{}
	for id, spot := range spots {
		if isCreature(spot.kind) {
			animals = append(animals, id)
		}
		if spot.kind == "batterfly" || walker(spot.kind) {
			// A Heli Kitty's and a Crab's spot too (slice 2): their planner groups come after groups a
			// player's own projects and the seasons add, so every page walks them from the server's.
			places[id] = []float64{spot.x, spot.y}
		}
	}
	sort.Strings(animals)
	// "pigpaths": this server keeps street Piggies' paths (lag, hold in their state), so the page walks them on the shared clock (G334).
	// "pigtrips": it walks them to food and home too ("trip" in their state, objects_pigs.go startTrip),
	// from "homes" (each Piggy's spot and walk, {"pig:0":[x,range]}) and the street's "trunks", so every
	// page and the server put a Piggy in the same place.
	// "guano": this server shares the Batterflies' hourly Guano, its clocks running since then (objects_guano.go).
	msg := map[string]any{"t": "objs", "street": street, "snap": true, "creatures": true, "pigpaths": true, "pigtrips": true, "guano": o.guanoStart.UnixMilli(), "animals": animals, "now": o.h.Now().UnixMilli(), "objs": list}
	if len(places) > 0 {
		msg["spots"] = places
	}
	homes := map[string][]float64{}
	for id, spot := range spots {
		if spot.kind == "pig" {
			homes[id] = []float64{spot.x, spot.rng}
		}
	}
	if len(homes) > 0 {
		msg["homes"] = homes
		if t := o.w.trunks[street]; len(t) > 0 {
			msg["trunks"] = t
		} else {
			msg["trunks"] = []float64{}
		}
	}
	o.h.SendLocked(cl, msg)
}

type objAct struct {
	Req   int64    `json:"req"`
	ID    string   `json:"id"`
	Verb  string   `json:"verb"`
	Perks []string `json:"perks"`
	Skill string   `json:"skill"`
	On    string   `json:"on"`   // eat: the tree whose fruit a Piggy ate
	Ms    int64    `json:"ms"`   // poison, GRODDLE_TREES_DEV=1 only: the die timer for QA
	C     string   `json:"c"`    // feed: the food's class (G335 slice 5)
	N     int      `json:"n"`    // feed: how many of it, one stack at most
	At    int64    `json:"at"`   // wait: the path's clock (server ms minus lag) where the player's screen stopped the animal
	Name  string   `json:"name"` // name: what the player typed for a street animal (objects_pigs.go nameAnimal)
	K     *int     `json:"k"`    // youth: the bottle poured, its place among the player's bottles (objects_uses.go copyOf)
}

// verdict is what one verb decided: ok, or a refusal code and line; extra rides on obj_ok.
type verdict struct {
	ok      bool
	code    string
	message string
	extra   map[string]any
	got     tradeGoods // what the player takes, credited to the site's ledger
	also    []*liveObj // other objects the verb changed
	ev      string     // the street sees the verb happen (obj_ev, objects_creatures.go)
}

// objFruit is each trant's fruit (js/trants.js:115-120; items/trant_*.js).
var objFruit = map[string]string{"trant_fruit": "cherry", "trant_egg": "egg_plain", "trant_bean": "bean_plain", "trant_bubble": "plain_bubble", "trant_spice": "all_spice", "trant_gas": "general_vapour"}

// objNodeYield is a resource node's yield with no skill package bonus
// (js/resource-nodes.js:1377-1420: mortar_barnacle 1, ice_knob at least 1,
// jellisac 1, dirt_pile 2 earth unless a drop rolls, peat_1 3; a pocketed
// npc_salmon one Pocket Salmon, npc_salmon.js verbs.pocket). More than this,
// a dirt pile's drops and a Salmon's 10% boot go through the budget.
var objNodeYield = map[string]struct {
	c string
	n int
}{"mortar_barnacle": {"barnacle", 1}, "ice_knob": {"ice", 1}, "jellisac": {"jellisac_clump", 1}, "dirt_pile": {"earth", 2}, "peat_1": {"peat", 3}, "npc_salmon": {"salmon", 1}}

// woodPlanks is items/wood_tree.js:880 plankProdMap, the planks by maturity (endHarvest 766).
var woodPlanks = [6]int{25, 30, 40, 50, 60, 80}

func refuse(code, message string) verdict { return verdict{code: code, message: message} }

func (o *objects) handleAct(cl *Client, frame json.RawMessage) {
	var m objAct
	if json.Unmarshal(frame, &m) != nil || m.Req < 1 || m.Req > 1<<31 || len(m.Perks) > 8 || len(m.Skill) > 32 || len(m.On) > 40 || len(m.C) > 64 || len(m.Name) > 64 || m.N < 0 || m.N > tradeSlotMax {
		o.h.ErrorLocked(cl, "bad_obj", "That object message did not check out.")
		return
	}
	no := func(code, message string, obj any) {
		msg := map[string]any{"t": "obj_no", "req": m.Req, "id": m.ID, "verb": m.Verb, "code": code, "message": message}
		if obj != nil {
			msg["obj"] = obj
		}
		o.h.SendLocked(cl, msg)
	}
	if !o.allow(cl) {
		no("slow_down", "Slow down a little.", nil)
		return
	}
	if !objIDRe.MatchString(m.ID) || !objVerbs[m.Verb] {
		no("unknown", "That is not something here.", nil)
		return
	}
	street := cl.Street()
	spot := o.w.streets[street][m.ID]
	if street == "" || spot == nil {
		no("unknown", "That is not something here.", nil)
		return
	}
	reachX, reachY := float64(objReachX), float64(objReachY)
	if spot.kind == "pig" {
		// A Piggy wanders and walks to food, each client's its own way: about two screens.
		reachX, reachY = pigReachX, pigReachY
	}
	if isCreature(spot.kind) {
		reachX, reachY = creatureReach(spot)
	}
	if x, y := cl.Pos(); math.Abs(x-spot.x) > reachX || math.Abs(y-spot.y) > reachY {
		no("far", "You need to be closer.", nil)
		return
	}
	if m.Verb == "food" {
		// A player harvested, watered or petted a fruiting tree: the street's hungry Piggies go for it
		// (objects_pigs.go pigsToTree). No answer: everyone sees them set off ("trip" in their state).
		if spot.kind != "tree" {
			no("bad_verb", "That is not something here.", nil)
			return
		}
		o.pigsToTree(street, spot)
		return
	}
	if m.Verb == "wait" {
		// A player at a street animal (its menu open, a verb on it): it stands for everyone, no answer.
		if (!isCreature(spot.kind) && spot.kind != "pig") || spot.kind == "batterfly" {
			no("bad_verb", "That is not something here.", nil)
			return
		}
		if spot.kind == "pig" && o.get(street, spot).pigGone() {
			return
		}
		o.waitCreature(street, spot, m.At)
		return
	}
	perks := map[string]bool{}
	for _, p := range m.Perks {
		if objPerks[p] {
			perks[p] = true
		}
	}
	if (spot.kind == "tree" || spot.kind == "wood") && (m.Verb == "poison" || m.Verb == "antidote") && o.uses.ledger != nil {
		// G335: the bottle is the ledger's to confirm and use first (objects_uses.go).
		o.askPour(cl, m, street, spot)
		return
	}
	if spot.kind == "pig" && m.Verb == "capture" {
		o.askCapture(cl, m, street, spot, perks) // G605 (objects_pigs_gone.go)
		return
	}
	if spot.kind == "pig" && m.Verb == "youth" {
		o.askYouth(cl, m, street, spot)
		return
	}
	if spot.kind == "pig" && m.Verb == "feed" && o.uses.ledger != nil {
		// G335 slice 5: food that sates a hungry Piggy is the ledger's to confirm and take (objects_pigs.go).
		o.askFeed(cl, m, street, spot, perks)
		return
	}
	if spot.kind == "chicken" && m.Verb == "incubate" {
		// The egg is the ledger's to take before the Chicken sits on it (objects_creatures.go).
		o.askIncubate(cl, m, street, spot)
		return
	}
	if m.Verb == "pickup" {
		// The Guano under a Batterfly, everyone's until someone picks it up (objects_guano.go).
		if spot.kind != "batterfly" {
			no("bad_verb", "That is not something here.", nil)
			return
		}
		o.pickGuano(cl, m, street, spot)
		return
	}
	if spot.kind == "batterfly" && m.Verb == "feed" {
		// The food is the ledger's to take before the shared counter moves (objects_creatures.go).
		o.askBatterfly(cl, m, street, spot, perks)
		return
	}
	if need := toolFor(spot, m.Verb); need != nil {
		// G335 slice 5: the tool behind the take is the ledger's to confirm (objects_tools.go).
		o.withTool(cl, street, need, func(code, message string) {
			no(code, message, o.state(o.get(street, spot)))
			o.forget(street, o.get(street, spot))
		},
			func(credit bool) { o.actOn(cl, m, street, spot, perks, credit) })
		return
	}
	o.actOn(cl, m, street, spot, perks, true)
}

// actOn decides one verb on a shared object and answers it; credit false keeps the take out of the
// ledger's receipts (its tool could not be confirmed: objects_tools.go).
func (o *objects) actOn(cl *Client, m objAct, street string, spot *objSpot, perks map[string]bool, credit bool) {
	ob := o.get(street, spot)
	before := o.state(ob)
	var v verdict
	switch {
	case spot.kind == "rock":
		v = o.mine(street, ob, m.Verb, perks, cl.ID())
	case spot.kind == "node":
		v = o.useNode(street, ob, m.Verb, perks, cl.ID(), m.Skill)
	case spot.kind == "pig":
		v = o.tendPig(street, ob, m, perks, cl.ID())
	case m.Verb == "name" && nameSingle[spot.kind] != "":
		v = o.nameAnimal(street, ob, m, perks, cl.ID())
	case isCreature(spot.kind):
		v = o.tendCreature(street, ob, m, perks, cl.ID())
	case ob.dead:
		v = refuse("dead", treeIsDead)
	case m.Verb == "poison" || m.Verb == "antidote":
		v = o.pour(street, ob, m.Verb, cl.ID(), m.Ms)
	case o.rooked(street, spot.id):
		v = refuse("rooked", "It's rooked. Revive it first.")
	case spot.kind == "wood":
		v = o.chopWood(ob, m.Verb, cl.ID())
	default:
		v = o.tendTree(ob, m.Verb, m.Skill)
	}
	if !credit {
		v.got = tradeGoods{}
	}
	o.finishAct(cl, m, street, ob, before, v)
}

// finishAct answers one verb on a shared object (obj_ok or obj_no), credits what it took and tells
// the street. Also the end of a pour the ledger answered later (objects_uses.go askPour).
func (o *objects) finishAct(cl *Client, m objAct, street string, ob *liveObj, before map[string]any, v verdict) {
	spot := ob.spot
	if !v.ok && (spot.kind == "rock" || spot.kind == "node") && ob.mining(cl.ID()) {
		// A refused swing or dig is over (cancelMining, removeDigger): off the rock or pile, its helps taken back.
		ob.removeMiner(cl.ID(), true)
		o.broadcast(street, cl.ID(), ob)
	}
	after := o.state(ob)
	if !v.ok {
		if cl.Online() {
			o.h.SendLocked(cl, map[string]any{"t": "obj_no", "req": m.Req, "id": m.ID, "verb": m.Verb, "code": v.code, "message": v.message, "obj": after})
		}
		o.forget(street, ob)
		return
	}
	msg := map[string]any{"t": "obj_ok", "req": m.Req, "id": m.ID, "verb": m.Verb, "before": before, "obj": after}
	for k, val := range v.extra {
		msg[k] = val
	}
	if cl.Online() {
		o.h.SendLocked(cl, msg)
	}
	if !v.got.empty() {
		creditLocked(o.h, cl.ID(), "take", v.got)
	}
	o.broadcast(street, cl.ID(), ob)
	if v.ev != "" {
		// The others on the street see the verb happen (the squeeze, the milking, the feed).
		ev := map[string]any{"t": "obj_ev", "street": street, "id": ob.spot.id, "ev": v.ev, "by": cl.ID()}
		if m.C != "" {
			ev["c"] = m.C
		}
		if name, ok := v.extra["name"].(string); ok && v.ev == "name" {
			ev["name"] = name // a Piggy named: everyone sees its bubble (animal_naming.js 49)
		}
		o.h.BroadcastStreetLocked(street, ev, cl.ID())
	}
	for _, other := range v.also {
		// Something else the verb changed (a Piggy ate a tree's fruit): everyone hears it.
		o.broadcast(street, 0, other)
		o.forget(street, other)
	}
	o.forget(street, ob)
}

// ---------------------------------------------------------------------------
// Rocks (items/include/mining.js)
// ---------------------------------------------------------------------------

func (o *objects) mine(street string, ob *liveObj, verb string, perks map[string]bool, miner int64) verdict {
	if verb != "mine" {
		return refuse("bad_verb", "You can't do that to a rock.")
	}
	rule := o.w.rules.Rocks[ob.spot.class]
	// isUseable / startMining 215-217.
	if ob.chunks <= 0 {
		return refuse("empty", notReady["rock"])
	}
	// onMiningComplete 293-305: randInt per rock type, the top roll with max_luck.
	toGet := rule.Max
	if !perks["luck"] {
		toGet = o.randInt(rule.Min, rule.Max)
	}
	// 307-325: Mining Expert for this rock type adds round(15%). The client adds
	// it to its own pack (mining.js miningExpertBonus); the rock loses it here.
	taken := toGet
	if perks["expert"] {
		taken += int(math.Round(0.15 * float64(toGet)))
	}
	// 327-342: each help this swing had adds min(i+1, 4), up to 9 helps; the
	// helpers are named for the "You got N additional …" line (373).
	helped, by := 0, []string{}
	for id, n := range ob.helpers[miner] {
		helped += n
		if cl := o.h.ClientLocked(id); cl != nil {
			by = append(by, cl.Name())
		}
	}
	sort.Strings(by)
	bonus := helpMineBonus(helped)
	// The swing is done: off the rock, its helps kept for the others (removeMiner, not cancelled).
	ob.removeMiner(miner, false)
	// 383: the whole take comes off, even past what is left.
	ob.chunks -= taken + bonus
	if ob.chunks <= 0 {
		ob.chunks = 0
		ob.mega = false
		// onRespawn 143-170: 0/5/10/20% by type come back at 150, half as likely
		// again with the clearing player's Rock Whisperer (418-421), rolled now.
		chance := o.w.rules.Mega.Chance[rule.Type]
		if perks["whisperer"] {
			chance += 0.5 * chance
		}
		ob.nextMega = o.roll() < chance
		o.schedule(street, ob, rockRespawn, o.respawnRock)
	}
	extra := map[string]any{"chunks": toGet}
	if bonus > 0 {
		extra["help"] = map[string]any{"bonus": bonus, "by": by}
	}
	// js/mining.js: the chunk is the rock's type (chunkId), the whole take and the help bonus.
	return verdict{ok: true, extra: extra, got: creditItem(rule.Type, taken+bonus)}
}

// chopWood is items/wood_tree.js harvest (conditions 485-497, doHarvest/endHarvest 725-790) on the
// shared tree: once a game day for each player; every second harvest (any player's) takes one
// maturity, and a wee tree (maturity 2 or less) loses one each time; at 0 it is cleared for
// everyone (onDie: the remake keeps a patch). The planks, energy and Hatchet wear are the
// harvester's own (js/woodcutting.js harvestWoodTree, from "before").
func (o *objects) chopWood(ob *liveObj, verb string, harvester int64) verdict {
	if verb != "harvest" {
		return refuse("bad_verb", "You can't do that to a Wood Tree here.")
	}
	return woodChop(&ob.treeCore, &ob.woodCount, &ob.woodBy, harvester)
}

// helpMineBonus is mining.js 335-337: min(i+1, 4) for each of up to 9 helps.
func helpMineBonus(helps int) int {
	bonus := 0
	for i := 0; i < helps && i < 9; i++ {
		bonus += min(i+1, 4)
	}
	return bonus
}

// addMiner is mining.js addMiner 68-91: the newcomer helps everyone already swinging, and starts a
// helper list of its own.
func (ob *liveObj) addMiner(id int64) {
	if ob.helpers == nil {
		ob.helpers = map[int64]map[int64]int{}
	}
	for _, list := range ob.helpers {
		list[id]++
	}
	ob.helpers[id] = map[int64]int{}
	ob.miners = append(ob.miners, id)
}

// removeMiner is mining.js removeMiner 93-122: a cancelled swing takes back its helps too.
func (ob *liveObj) removeMiner(id int64, cancel bool) {
	if cancel {
		for _, list := range ob.helpers {
			if list[id] > 0 {
				if list[id]--; list[id] == 0 {
					delete(list, id)
				}
			}
		}
	}
	delete(ob.helpers, id)
	for i, m := range ob.miners {
		if m == id {
			ob.miners = append(ob.miners[:i], ob.miners[i+1:]...)
			break
		}
	}
}

func (ob *liveObj) mining(id int64) bool {
	for _, m := range ob.miners {
		if m == id {
			return true
		}
	}
	return false
}

// handleMine is a swing starting or stopping (startMining, cancelMining 124-134).
func (o *objects) handleMine(cl *Client, frame json.RawMessage) {
	var m struct {
		ID string `json:"id"`
		Do string `json:"do"`
	}
	if json.Unmarshal(frame, &m) != nil || (m.Do != "start" && m.Do != "stop") || !o.allow(cl) {
		return
	}
	street := cl.Street()
	spot := o.w.streets[street][m.ID]
	if street == "" || spot == nil || spot.kind != "rock" {
		return
	}
	ob := o.get(street, spot)
	if m.Do == "stop" {
		if ob.mining(cl.ID()) {
			ob.removeMiner(cl.ID(), true)
			o.broadcast(street, 0, ob)
		}
		o.forget(street, ob)
		return
	}
	if x, y := cl.Pos(); ob.chunks <= 0 || math.Abs(x-spot.x) > objReachX || math.Abs(y-spot.y) > objReachY {
		o.forget(street, ob)
		return
	}
	// getVerbLabel 8-23: "Help mine" when someone else is already swinging.
	ob.removeMiner(cl.ID(), true)
	helping := len(ob.miners) > 0
	ob.addMiner(cl.ID())
	o.h.SendLocked(cl, map[string]any{"t": "obj_mine", "id": m.ID, "helping": helping})
	o.broadcast(street, 0, ob)
}

// handleDig is a dig at a Dirt Pile starting or stopping (dirt_pile.js doDigging 202-234 adds the
// digger before the skill package runs; onOverlayDismissed 486-489 is a cancel). "Help dig" is the
// verb while someone else digs (getVerbLabel 273-281).
func (o *objects) handleDig(cl *Client, frame json.RawMessage) {
	var m struct {
		ID string `json:"id"`
		Do string `json:"do"`
	}
	if json.Unmarshal(frame, &m) != nil || (m.Do != "start" && m.Do != "stop") || !o.allow(cl) {
		return
	}
	street := cl.Street()
	spot := o.w.streets[street][m.ID]
	if street == "" || spot == nil || spot.kind != "node" || spot.class != "dirt_pile" {
		return
	}
	ob := o.get(street, spot)
	if m.Do == "stop" {
		if ob.mining(cl.ID()) {
			ob.removeMiner(cl.ID(), true)
			o.broadcast(street, 0, ob)
		}
		o.forget(street, ob)
		return
	}
	if x, y := cl.Pos(); ob.units < o.w.rules.Nodes[spot.class].Ready || math.Abs(x-spot.x) > objReachX || math.Abs(y-spot.y) > objReachY {
		o.forget(street, ob)
		return
	}
	ob.removeMiner(cl.ID(), true)
	helping := len(ob.miners) > 0
	ob.addMiner(cl.ID())
	o.h.SendLocked(cl, map[string]any{"t": "obj_dig", "id": m.ID, "helping": helping})
	o.broadcast(street, 0, ob)
}

// leftStreet is miningSanityCheck 53-66 (dirt_pile.js onPlayerExit 491-495): a player who leaves is off
// every rock and Dirt Pile, their helps taken back.
func (o *objects) leftStreet(cl *Client, street string) {
	for _, ob := range o.streets[street] {
		if (ob.spot.kind == "rock" || ob.spot.kind == "node") && ob.mining(cl.ID()) {
			ob.removeMiner(cl.ID(), true)
			o.broadcast(street, 0, ob)
			o.forget(street, ob)
		}
	}
}

func (o *objects) respawnRock(street string, ob *liveObj) {
	ob.chunks = o.w.rules.Rocks[ob.spot.class].Chunks
	ob.mega = ob.nextMega
	if ob.mega {
		ob.chunks = o.w.rules.Mega.Chunks
	}
	ob.nextMega = false
}

// schedule (re)starts an object's one timer; fn runs with the lock held, then
// the street hears the new state and a fresh object is forgotten.
func (o *objects) schedule(street string, ob *liveObj, d time.Duration, fn func(street string, ob *liveObj)) {
	if ob.stop != nil {
		ob.stop()
	}
	ob.due = o.h.Now().Add(d)
	var stop func() bool
	stop = o.h.After(d, func() {
		if o.streets[street][ob.spot.id] != ob || ob.stop == nil {
			return
		}
		ob.stop, ob.due = nil, time.Time{}
		fn(street, ob)
		o.broadcast(street, 0, ob)
		o.forget(street, ob)
		o.saveSoon()
	})
	ob.stop = stop
}

// ---------------------------------------------------------------------------
// Resource nodes (js/resource-nodes.js rules; items/*.js cited there)
// ---------------------------------------------------------------------------

func (o *objects) regrowDelay(rule nodeRule) time.Duration {
	// resource-nodes.js regrowDelayMs: randInt over whole steps.
	lo, hi, step := rule.Regrow[0], rule.Regrow[1], rule.Regrow[2]
	if step <= 0 {
		step = 1000
	}
	n := o.randInt(int(lo/step), int(hi/step))
	return time.Duration(int64(n)*step) * time.Millisecond
}

func (o *objects) useNode(street string, ob *liveObj, verb string, perks map[string]bool, user int64, skill string) verdict {
	class := ob.spot.class
	rule := o.w.rules.Nodes[class]
	known := false
	for _, v := range rule.Verbs {
		known = known || v == verb
	}
	if !known {
		return refuse("bad_verb", "You can't do that here.")
	}
	if rule.Model == "tend" {
		return o.tendPaper(street, ob, verb, perks, rule)
	}
	if ob.units < rule.Ready {
		return refuse("not_ready", notReady[class])
	}
	var dig verdict
	if class == "dirt_pile" {
		// The crew's bonus, read before the dig comes off (onDigComplete 293-462).
		dig = o.digHelp(ob, user, skill)
	}
	if rule.After != nil {
		// mortar_barnacle.js / ice_knob.js / jellisac.js: back to the first state.
		ob.units = *rule.After
	} else {
		// dirt_pile.js / peat_1.js: one use comes off.
		ob.units = max(rule.Min, ob.units-max(1, rule.Consume))
	}
	// The regrow timer restarts on every use (resource-nodes.js harvestResourceNode).
	o.schedule(street, ob, o.regrowDelay(rule), o.regrowNode)
	if class == "dirt_pile" {
		return dig
	}
	y := objNodeYield[class]
	return verdict{ok: true, got: creditItem(y.c, y.n)}
}

// digHelp is dirt_pile.js onDigComplete 293-462 for one landed dig: the skill package's own Loam
// drop (rolled here, sent as "roll" for the client's first draw), then the crew's bonus. With n
// helpers (each counted once, num_keys(this.helpers[pc.tsid])): 1 bonus item past 0 helpers, 2
// past 2, 3 past 5; a 5% chance past 1 helper, 15% past 5, that the dig's own yield is Loam
// (normalised by 1 - drop_chance, since the drop was already missed: 380-389); the bonus items are
// Loam when either Loam came (bonus_item_class). The digger is off the pile, its helpers kept for
// the others (removeDigger(pc, false), 484). What the server decided is credited: the yield (the
// skill's bonus_amount of Earth or Loam; a drop's Loam goes through the budget) and the bonus.
func (o *objects) digHelp(ob *liveObj, digger int64, skill string) verdict {
	pkg, ok := o.w.rules.Dig[skill]
	if !ok {
		pkg = o.w.rules.Dig["no_skill"]
	}
	n, by := 0, []string{}
	for id := range ob.helpers[digger] {
		n++
		if cl := o.h.ClientLocked(id); cl != nil {
			by = append(by, cl.Name())
		}
	}
	sort.Strings(by)
	items, chance := 0, 0.0
	if n > 0 {
		items++
	}
	if n > 1 {
		chance += 0.05
	}
	if n > 2 {
		items++
	}
	if n > 5 {
		items++
		chance += 0.1
	}
	roll := o.roll()
	dropped := pkg.Drop > 0 && roll*100 < pkg.Drop
	loam := false
	if !dropped && chance > 0 {
		c := chance / (1 - pkg.Drop/100)
		loam = o.roll() < math.Min(1, c)
	}
	class := "earth"
	if dropped || loam {
		class = "loam"
	}
	ob.removeMiner(digger, false)
	extra := map[string]any{"roll": roll}
	var got tradeGoods
	if !dropped {
		yield := "earth"
		if loam {
			yield = "loam"
		}
		got = creditItem(yield, max(1, pkg.Bonus))
	}
	if n > 0 {
		extra["help"] = map[string]any{"n": n, "items": items, "class": class, "loam": loam, "by": by}
		if bonus := creditItem(class, items); !bonus.empty() {
			if got.empty() {
				got = bonus
			} else if got.Items[0].C == class {
				got.Items[0].N += items
			} else {
				got.Items = append(got.Items, bonus.Items[0])
			}
		}
	}
	return verdict{ok: true, extra: extra, got: got}
}

func (o *objects) regrowNode(street string, ob *liveObj) {
	rule := o.w.rules.Nodes[ob.spot.class]
	if ob.units < rule.Max {
		ob.units++
	}
	if ob.units < rule.Max {
		// resource-nodes.js tickResourceNode: each step from the last due time.
		ob.stop = nil
		o.schedule(street, ob, o.regrowDelay(rule), o.regrowNode)
	}
}

// tendPaper is paper_tree.js as js/resource-nodes.js has it: harvest takes up
// to 5 (8 with gardening_harvest_paper); water and pet once each, and both
// together add 9 paper (max 21) and clear the flags 6 minutes later.
func (o *objects) tendPaper(street string, ob *liveObj, verb string, perks map[string]bool, rule nodeRule) verdict {
	switch verb {
	case "harvest":
		if ob.units < 1 {
			return refuse("not_ready", notReady["paper_tree"])
		}
		want := 5
		if perks["paper8"] {
			want = 8
		}
		take := min(want, ob.units)
		ob.units -= take
		return verdict{ok: true, extra: map[string]any{"take": take}, got: creditItem("paper", take)}
	case "water", "pet":
		if verb == "water" && ob.watered {
			return refuse("not_wanted", "I don't want to be watered!") // paper_tree.js:358
		}
		if verb == "pet" && ob.petted {
			return refuse("not_wanted", "I don't want to be petted!") // paper_tree.js:382
		}
		if verb == "water" {
			ob.watered = true
		} else {
			ob.petted = true
		}
		if ob.watered && ob.petted && rule.Tend != nil {
			ob.units = min(rule.Max, ob.units+rule.Tend.Refill)
			o.armReset(street, ob, time.Duration(rule.Tend.ResetMs)*time.Millisecond)
		}
		return verdict{ok: true}
	}
	return refuse("bad_verb", "You can't do that here.")
}

// armReset clears a Paper Tree's tending flags d from now.
func (o *objects) armReset(street string, ob *liveObj, d time.Duration) {
	ob.resetAt = o.h.Now().Add(d)
	if ob.resetStop != nil {
		ob.resetStop()
	}
	ob.resetStop = o.h.After(d, func() {
		if o.streets[street][ob.spot.id] != ob || ob.resetStop == nil {
			return
		}
		ob.resetStop, ob.resetAt = nil, time.Time{}
		ob.petted, ob.watered = false, false
		o.broadcast(street, 0, ob)
		o.forget(street, ob)
		o.saveSoon()
	})
}

// ---------------------------------------------------------------------------
// Trees (js/trants.js, js/trant-care.js; items/include/trants.js)
// ---------------------------------------------------------------------------

func (o *objects) tendTree(ob *liveObj, verb, skill string) verdict {
	switch verb {
	case "harvest":
		return o.w.treeHarvest(&ob.treeCore, ob.spot.class, skill)
	case "pet", "water":
		return o.w.treeCare(&ob.treeCore, ob.spot.class, verb, skill, o.roll)
	}
	return refuse("bad_verb", "You can't do that to a tree.")
}

// pour is a Tree Poison or Antidote pour landing on a shared tree (the potions' onPourComplete:
// trant_base / wood_tree applyPoison 1268-1274 and cure). The bottle, the guilt, the mood and the
// Botany gate are the pourer's own (js/tree-poison.js). Poison starts the die timer for everyone;
// an antidote from anyone stops it.
func (o *objects) pour(street string, ob *liveObj, verb string, by int64, ms int64) verdict {
	if verb == "antidote" {
		if ob.poisonEnd.IsZero() {
			return refuse("not_poisoned", noPoisonedTree)
		}
		if ob.poisonStop != nil {
			ob.poisonStop()
		}
		ob.poisonEnd, ob.poisonBy, ob.poisonStop = time.Time{}, 0, nil
		o.saveNow()
		return verdict{ok: true}
	}
	if !ob.poisonEnd.IsZero() {
		return refuse("poisoned", alreadyPoison)
	}
	d := treePoison
	if o.dev && ms >= 1000 && ms < treePoison.Milliseconds() {
		d = time.Duration(ms) * time.Millisecond
	}
	ob.poisonBy = by
	o.armPoison(street, ob, d)
	o.saveNow()
	return verdict{ok: true, extra: map[string]any{"poison": d.Milliseconds()}}
}

// armPoison starts (or, after a restart, goes on with) a tree's die timer: d from now it dies.
func (o *objects) armPoison(street string, ob *liveObj, d time.Duration) {
	if ob.poisonStop != nil {
		ob.poisonStop()
	}
	ob.poisonEnd = o.h.Now().Add(d)
	ob.poisonStop = o.h.After(d, func() {
		if o.streets[street][ob.spot.id] != ob || ob.poisonStop == nil || ob.dead {
			return
		}
		o.killTree(street, ob, ob.poisonBy)
	})
}

// killTree is include/trants.js die 734-773 (and wood_tree.js onDie) on the shared tree: it is dead
// for everyone from now on, here or away. The poisoner of a Wood Tree is credited its 25 Planks
// (their client drops them beside the Patch). by 0: the Rook.
func (o *objects) killTree(street string, ob *liveObj, by int64) {
	if ob.poisonStop != nil {
		ob.poisonStop()
	}
	ob.poisonEnd, ob.poisonBy, ob.poisonStop = time.Time{}, 0, nil
	ob.dead, ob.deadBy = true, by
	ob.fruit, ob.petted, ob.watered = 0, false, false
	// On disk before the planks are credited: a restart must not find the clock still running and
	// kill (and credit) it again.
	o.saveNow()
	if ob.spot.kind == "wood" && by > 0 {
		creditLocked(o.h, by, "take", creditItem("plank", deadWoodPlanks))
	}
	o.broadcast(street, 0, ob)
	// G564: on a street whose Patches are shared, its plot is everyone's at once (patches.go adopt).
	if adopt, ok := o.h.ProvidedLocked("patches.adopt").(func(string, *objSpot)); ok {
		adopt(street, ob.spot)
	}
}

// fruitCycle is include/trants.js onFruitCycle 1043-1054 on the shared tree, as plusHealth runs it:
// the capacity for its maturity and health (updateFruitCap 1026-1041), then that fruitProdMap cell
// pushed back on, never past the capacity (pushFruit 466-473). The fruit it added.
func (o *objects) fruitCycle(ob *liveObj) int {
	return o.w.treeFruitCycle(&ob.treeCore, ob.spot.class)
}

// newDay is game.js resetAllTrantsForNewDay for one tree: the tending flags clear, a new window
// to pet and water it. The fruit stays as it is: GSJS never refills trees at the day turn (fruit
// comes back from tending and Fertilidust: fruitCycle).
func (o *objects) newDay(ob *liveObj) bool {
	if ob.dead {
		return false
	}
	return o.w.treeNewDay(&ob.treeCore, ob.spot.class, o.today())
}

// armDay sets the one timer that rolls every touched tree over at the next game day.
func (o *objects) armDay() {
	if o.dayStop != nil {
		return
	}
	now := o.h.Now().UnixMilli()
	next := gameEpochMs + (o.today()+1)*gameDayMs
	o.dayStop = o.h.After(time.Duration(next-now+50)*time.Millisecond, func() {
		o.dayStop = nil
		if o.rollDay() {
			o.armDay()
		}
	})
}

// rollDay runs newDay on every touched tree and tells each street what changed; false when no
// tree is held.
func (o *objects) rollDay() bool {
	trees := false
	for street, room := range o.streets {
		var changed []*liveObj
		for _, ob := range room {
			if ob.spot.kind != "tree" {
				continue
			}
			trees = true
			if o.newDay(ob) {
				changed = append(changed, ob)
			}
		}
		if len(changed) > 0 {
			o.broadcast(street, 0, changed...)
		}
		for _, ob := range changed {
			o.forget(street, ob)
		}
	}
	if o.pigsBack() {
		trees = true // a Piggy still away keeps the day timer armed (objects_pigs_gone.go)
	}
	o.saveSoon()
	return trees
}

// dayDev is GRODDLE_TREES_DEV=1 only, for QA: {"t":"obj_day_dev"} turns the game day for every
// shared tree now, as armDay's timer does at the real turn (and brings gone Piggies back, G605).
func (o *objects) dayDev(cl *Client, _ json.RawMessage) {
	if !o.dev || cl.Street() == "" {
		return
	}
	today := o.today()
	for _, room := range o.streets {
		for _, ob := range room {
			if ob.spot.kind == "tree" && ob.day >= today {
				ob.day = today - 1
			}
			if ob.pigGone() && ob.goneDay >= today {
				ob.goneDay = today - 1 // G605: a gone Piggy is due back at this dev turn too
			}
		}
	}
	log.Printf("objects: dev day turn for every shared tree (asked by %d)", cl.ID())
	o.rollDay()
}

// handleDust is Fertilidust landing on the street's shared trees (fertilidust.js onUse 266-345:
// every trant and Wood Tree of the location, the onFertilidust event 3 s on, which the client
// waits before it sends this; fertilidust_lite.js onUse 266-296: the closest tree, findCloseStack,
// so its id must be near). The jar, its charge and its lines are the player's own. Trees a client
// grew in its own Patches are not shared and never named here. On a street whose Patches are shared
// (G564 slice 2, patches_trees.go) the same jar reaches them: the full jar revives their dead trants
// as it is used and grows their living trees with the street's as it lands; Lite may name a Patch
// tree by its plot key (a tree:<i> plot's key is a dead street tree's id: the Patch tree grown there
// is the one meant).
func (o *objects) handleDust(cl *Client, frame json.RawMessage) {
	var m struct {
		Req int64  `json:"req"`
		ID  string `json:"id"`
		K   *int   `json:"k"` // the jar used: its place among the class's jars in the pack (objects_uses.go copyOf)
	}
	if json.Unmarshal(frame, &m) != nil || m.Req < 1 || m.Req > 1<<31 || len(m.ID) > 40 {
		o.h.ErrorLocked(cl, "bad_obj", "That object message did not check out.")
		return
	}
	no := func(code, message string) {
		o.h.SendLocked(cl, map[string]any{"t": "obj_no", "req": m.Req, "id": m.ID, "verb": "dust", "code": code, "message": message})
	}
	if !o.allow(cl) {
		no("slow_down", "Slow down a little.")
		return
	}
	street := cl.Street()
	spots := o.w.streets[street]
	var targets []*objSpot
	patchKey := ""
	if m.ID != "" {
		spot := spots[m.ID]
		tx, ty := 0.0, 0.0
		if spot != nil {
			tx, ty = spot.x, spot.y
		}
		if street != "" && (spot == nil || o.isDead(street, m.ID)) {
			if where, ok := o.h.ProvidedLocked("patches.dustSpot").(func(string, string) (float64, float64, bool)); ok {
				if x, y, ok := where(street, m.ID); ok {
					patchKey, tx, ty = m.ID, x, y
				}
			}
		}
		if patchKey == "" && (street == "" || spot == nil || (spot.kind != "tree" && spot.kind != "wood")) {
			no("unknown", "That is not something here.")
			return
		}
		if x, y := cl.Pos(); math.Abs(x-tx) > objReachX || math.Abs(y-ty) > objReachY {
			no("far", "You need to be closer.")
			return
		}
		if patchKey == "" {
			targets = append(targets, spot)
		}
	} else {
		for _, spot := range spots {
			if spot.kind == "tree" || spot.kind == "wood" {
				targets = append(targets, spot)
			}
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].id < targets[j].id })
	}
	// G335: the jar's charge is the ledger's to confirm and use (objects_uses.go); the dust lands
	// dustLand later, as the source's onFertilidust event does.
	class := useFertilidust
	if m.ID != "" {
		class = useFertilidustLite
	}
	o.askUseCopy(cl, class, copyOf(m.K), func(use map[string]any, code, message string) {
		if code != "" {
			if cl.Online() {
				no(code, message)
			}
			return
		}
		ans := map[string]any{"t": "obj_dust_used", "req": m.Req, "c": class}
		if use != nil {
			ans["use"] = use
		}
		// G564: the full jar's dead trants in shared Patches come back at once (fertilidust.js onUse).
		var revived []string
		if m.ID == "" {
			if revive, ok := o.h.ProvidedLocked("patches.revive").(func(string) []string); ok {
				if revived = revive(street); len(revived) > 0 {
					ans["revived"] = revived
				}
			}
		}
		if cl.Online() {
			o.h.SendLocked(cl, ans)
		}
		all := m.ID == ""
		o.h.After(dustLand, func() { o.dustTrees(cl, m.Req, street, targets, all, patchKey, revived) })
	})
}

// fertilize is one tree's onFertilidust: a trant (include/trants.js 1236-1244) grows one maturity
// below 10 (plusMaturity 785-790) and goes to health 9 and then plusHealth, which lifts it to 10 and
// regrows its fruit (fruitCycle); a Wood Tree (wood_tree.js onFertilidust 870-874) grows one below
// 6. A dead tree, or a Wood Tree already cleared (onDie: gone in the source), takes nothing. True
// when the tree changed.
// isDead is whether the shared tree an id names on a street died (its plot may hold a Patch tree now).
func (o *objects) isDead(street, id string) bool {
	ob := o.streets[street][id]
	return ob != nil && ob.dead
}

func (o *objects) fertilize(street string, ob *liveObj) bool {
	if ob.dead {
		return false
	}
	if ob.spot.kind == "wood" {
		return woodDust(&ob.treeCore)
	}
	changed := o.w.treeDust(&ob.treeCore, ob.spot.class)
	if o.rooked(street, ob.spot.id) {
		// Its capacity moved: the rooked tree's fruit push goes on from here.
		o.armRookFruit(street, ob, rookFruitEvery)
	}
	return changed
}
