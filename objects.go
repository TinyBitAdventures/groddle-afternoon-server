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
//
// Server to client:
//
//	{"t":"objs","street":"GA5…","snap":true,"objs":[State…]}  the answer to obj_sync: every
//	                                                       object that differs from fresh ("none":true:
//	                                                       nothing on this street is shared)
//	{"t":"objs","street":"GA5…","objs":[State…]}           a change (to the others on the street)
//	                                                       or a regrowth, respawn or new day (to everyone)
//	{"t":"obj_ok","req":7,"id":"rock:2","verb":"mine","before":State,"obj":State,"chunks":7,"roll":0.31}
//	{"t":"obj_no","req":7,"id":"rock:2","verb":"mine","code":"empty","message":"…","obj":State}
//	{"t":"obj_mine","id":"rock:2","helping":true}             your swing joined someone else's
//
// A Wood Tree is tree:<i> too: {"id":"tree:3","maturity":5,"count":1}.
//
// State: {"id":"rock:2","chunks":43,"mega":false,"in":0,"miners":[3,7]} (in: ms to the
// respawn; miners: who is swinging at it now, so the verb reads "Help mine"),
// {"id":"node:peat_1:0","units":3,"in":120000} (in: ms to the next +1; a Paper
// Tree adds "petted","watered","reset"), {"id":"tree:1","fruit":40,"cap":98,
// "health":8,"maturity":7,"petted":true,"watered":false}.
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
// until it is revived.
//
// What a player takes is theirs to apply, and the site's ledger is told what
// the take was (credits.go, objCredit), so the save can show it outside the
// earning budget: a rock's whole take, a tree's or Paper Tree's fruit, a Wood
// Tree's planks, and a resource node's yield with no skill bonus (bonuses,
// super harvests and drops are the client's and go through the budget).
//
// The server forgets an object once it is fresh again, and everything when it
// restarts (accepted for now).

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
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

	objBurst = 6.0 // actions and syncs in a burst
	objEvery = 4.0 // then this many a second

	// js/game-day.js: Glitch's game day is four real hours from GAME_EPOCH_MS.
	gameEpochMs = 1_238_562_000_000
	gameDayMs   = 14_400_000
)

var (
	objIDRe  = regexp.MustCompile(`^(?:(rock|tree):(\d{1,2})|node:([a-z0-9_]{1,24}):(\d{1,2}))$`)
	objVerbs = map[string]bool{"mine": true, "harvest": true, "pet": true, "water": true, "scrape": true, "scoop": true, "dig": true}
	objPerks = map[string]bool{"luck": true, "expert": true, "whisperer": true, "paper8": true}
)

// The source's refusals (items/*.js), with the remake's copy where the source
// only hides the verb.
var notReady = map[string]string{
	"mortar_barnacle": "This barnacle is not ready yet!",           // mortar_barnacle.js:64
	"ice_knob":        "This Ice Nubbin is not ready yet!",         // ice_knob.js:63
	"jellisac":        "This Jellisac is not ready yet!",           // jellisac.js:54
	"dirt_pile":       "There isn't enough dirt left to dig.",      // dirt_pile.js:113
	"peat_1":          "No more peat here!",                        // peat_1.js:143
	"paper_tree":      "There's nothing to pick, cowboy!",          // paper_tree.js:252
	"rock":            "This is not the rock you are looking for.", // include/mining.js:216
	"tree":            "There's nothing to pick, cowboy.",          // include/trants.js:235
}

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
}

// objSpot is one object as laid out: its kind, class, place and (trees) its fresh maturity and health.
type objSpot struct {
	id       string
	kind     string // rock | node | tree | wood (a Wood Tree: tree:<i> like the trants)
	class    string
	x, y     float64
	maturity int
	health   int
}

type objWorld struct {
	rules   objRules
	streets map[string]map[string]*objSpot
}

func loadObjWorld(raw []byte) (*objWorld, error) {
	var file struct {
		Rules   objRules `json:"rules"`
		Streets map[string]struct {
			Rocks [][]any `json:"rocks"`
			Trees [][]any `json:"trees"`
			Nodes [][]any `json:"nodes"`
		} `json:"streets"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	w := &objWorld{rules: file.Rules, streets: map[string]map[string]*objSpot{}}
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

	chunks     int  // rock
	mega       bool // rock: came back at 150
	nextMega   bool // rock: will come back at 150
	units      int  // node
	petted     bool // node (Paper Tree), tree
	watered    bool
	resetAt    time.Time // Paper Tree: tending flags clear
	fruit, cap int       // tree
	health     int
	maturity   int
	day        int64          // tree: the game day its flags and fruit belong to; wood: its harvesters' day
	woodCount  int            // wood: harvest_count (wood_tree.js 732-782)
	woodBy     map[int64]bool // wood: who harvested it this game day (the source's intervals[today][pc])

	// rock: who is mining it now, in the order they started, and each miner's helpers with how
	// many times each helped (mining.js addMiner 68-91, removeMiner 93-122).
	miners  []int64
	helpers map[int64]map[int64]int

	due  time.Time   // rock respawn or node's next +1
	stop func() bool // that timer
	// resetStop is the Paper Tree's flag reset timer.
	resetStop func() bool
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
	limits  map[int64]*objBucket
	dayStop func() bool
}

func newObjects(h *Hub) Module {
	return &objects{h: h, w: sharedWorld, roll: rand.Float64, streets: map[string]map[string]*liveObj{}, limits: map[int64]*objBucket{}}
}

func (o *objects) Register(r *Registry) {
	r.Handle("obj_sync", o.handleSync)
	r.Handle("obj_act", o.handleAct)
	r.OnDisconnect(func(cl *Client) { delete(o.limits, cl.ID()) })
	r.Handle("obj_mine", o.handleMine)
	r.OnLeave(o.leftStreet)
	r.Provide("objects.treeClass", o.treeClass)
	r.Provide("objects.rookTree", o.rookTree)
}

// treeClass is the class of the tree an id names on a street ("" when it names no tree there), so the
// Rook can check the tree a client pairs with one of its keys (rook_sync "objs").
func (o *objects) treeClass(street, id string) string {
	spot := o.w.streets[street][id]
	if spot == nil || (spot.kind != "tree" && spot.kind != "wood") {
		return ""
	}
	return spot.class
}

// rookTree is items/include/trants.js onRooked 1140-1151 on the shared tree, as the client does on
// its copy (js/rook-attack.js rookTrantState): every fruit gone, health down 2 (1 at health 2), never
// below 1 (a tree at health 1 dies in the source; tree death is not shared yet). Everyone on the
// street is told. False when the id names no tree there.
func (o *objects) rookTree(street, id string) bool {
	spot := o.w.streets[street][id]
	if spot == nil || spot.kind != "tree" {
		return false
	}
	ob := o.get(street, spot)
	ob.fruit = 0
	loss := 2
	if ob.health == 2 {
		loss = 1
	}
	ob.health = max(1, ob.health-loss)
	o.broadcast(street, 0, ob)
	return true
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
	}
	return ob
}

func (o *objects) isFresh(ob *liveObj) bool {
	s := ob.spot
	switch s.kind {
	case "rock":
		return ob.chunks == o.w.rules.Rocks[s.class].Chunks && !ob.mega && ob.stop == nil && len(ob.miners) == 0
	case "node":
		return ob.units == o.w.rules.Nodes[s.class].Initial && !ob.petted && !ob.watered && ob.stop == nil && ob.resetStop == nil
	case "tree":
		return ob.fruit == ob.cap && ob.maturity == s.maturity && ob.health == s.health && !ob.petted && !ob.watered
	case "wood":
		return ob.maturity == s.maturity && ob.woodCount == 0 && len(ob.woodBy) == 0
	}
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
	}
	if spot.kind == "tree" {
		o.newDay(ob)
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
		return m
	case "wood":
		return map[string]any{"id": s.id, "maturity": ob.maturity, "count": ob.woodCount}
	default:
		return map[string]any{"id": s.id, "fruit": ob.fruit, "cap": ob.cap, "health": ob.health, "maturity": ob.maturity, "petted": ob.petted, "watered": ob.watered}
	}
}

func (o *objects) broadcast(street string, except int64, obs ...*liveObj) {
	list := make([]map[string]any, 0, len(obs))
	for _, ob := range obs {
		list = append(list, o.state(ob))
	}
	o.h.BroadcastStreetLocked(street, map[string]any{"t": "objs", "street": street, "objs": list}, except)
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
	o.h.SendLocked(cl, map[string]any{"t": "objs", "street": street, "snap": true, "objs": list})
}

type objAct struct {
	Req   int64    `json:"req"`
	ID    string   `json:"id"`
	Verb  string   `json:"verb"`
	Perks []string `json:"perks"`
	Skill string   `json:"skill"`
}

// verdict is what one verb decided: ok, or a refusal code and line; extra rides on obj_ok.
type verdict struct {
	ok      bool
	code    string
	message string
	extra   map[string]any
	got     tradeGoods // what the player takes, credited to the site's ledger
}

// objFruit is each trant's fruit (js/trants.js:115-120; items/trant_*.js).
var objFruit = map[string]string{"trant_fruit": "cherry", "trant_egg": "egg_plain", "trant_bean": "bean_plain", "trant_bubble": "plain_bubble", "trant_spice": "all_spice", "trant_gas": "general_vapour"}

// objNodeYield is a resource node's yield with no skill package bonus
// (js/resource-nodes.js:1377-1420: mortar_barnacle 1, ice_knob at least 1,
// jellisac 1, dirt_pile 2 earth unless a drop rolls, peat_1 3). More than
// this, and a dirt pile's drops, go through the budget.
var objNodeYield = map[string]struct {
	c string
	n int
}{"mortar_barnacle": {"barnacle", 1}, "ice_knob": {"ice", 1}, "jellisac": {"jellisac_clump", 1}, "dirt_pile": {"earth", 2}, "peat_1": {"peat", 3}}

// woodPlanks is items/wood_tree.js:880 plankProdMap, the planks by maturity (endHarvest 766).
var woodPlanks = [6]int{25, 30, 40, 50, 60, 80}

func refuse(code, message string) verdict { return verdict{code: code, message: message} }

func (o *objects) handleAct(cl *Client, frame json.RawMessage) {
	var m objAct
	if json.Unmarshal(frame, &m) != nil || m.Req < 1 || m.Req > 1<<31 || len(m.Perks) > 8 || len(m.Skill) > 32 {
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
	if x, y := cl.Pos(); math.Abs(x-spot.x) > objReachX || math.Abs(y-spot.y) > objReachY {
		no("far", "You need to be closer.", nil)
		return
	}
	perks := map[string]bool{}
	for _, p := range m.Perks {
		if objPerks[p] {
			perks[p] = true
		}
	}
	ob := o.get(street, spot)
	before := o.state(ob)
	var v verdict
	switch spot.kind {
	case "rock":
		v = o.mine(street, ob, m.Verb, perks, cl.ID())
	case "node":
		v = o.useNode(street, ob, m.Verb, perks)
	case "wood":
		if o.rooked(street, spot.id) {
			v = refuse("rooked", "It's rooked. Revive it first.")
		} else {
			v = o.chopWood(ob, m.Verb, cl.ID())
		}
	case "tree":
		if o.rooked(street, spot.id) {
			v = refuse("rooked", "It's rooked. Revive it first.")
		} else {
			v = o.tendTree(ob, m.Verb, m.Skill)
		}
	}
	if !v.ok && spot.kind == "rock" && ob.mining(cl.ID()) {
		// A refused swing is over (cancelMining): off the rock, its helps taken back.
		ob.removeMiner(cl.ID(), true)
		o.broadcast(street, cl.ID(), ob)
	}
	after := o.state(ob)
	if !v.ok {
		no(v.code, v.message, after)
		o.forget(street, ob)
		return
	}
	msg := map[string]any{"t": "obj_ok", "req": m.Req, "id": m.ID, "verb": m.Verb, "before": before, "obj": after}
	for k, val := range v.extra {
		msg[k] = val
	}
	o.h.SendLocked(cl, msg)
	if !v.got.empty() {
		creditLocked(o.h, cl.ID(), "take", v.got)
	}
	o.broadcast(street, cl.ID(), ob)
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
	if ob.maturity <= 0 {
		return refuse("cleared", "This Wood Tree has been cleared.")
	}
	if ob.woodBy[harvester] {
		return refuse("daily_limit", "You can only harvest from this tree once per game day.")
	}
	if ob.woodBy == nil {
		ob.woodBy = map[int64]bool{}
	}
	ob.woodBy[harvester] = true
	got := creditItem("plank", woodPlanks[min(max(ob.maturity, 1), 6)-1])
	if ob.maturity <= 2 || ob.woodCount != 0 {
		ob.maturity--
		ob.woodCount = 0
	} else {
		ob.woodCount = 1
	}
	return verdict{ok: true, got: got}
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

// leftStreet is miningSanityCheck 53-66: a player who leaves is off every rock, their helps taken back.
func (o *objects) leftStreet(cl *Client, street string) {
	for _, ob := range o.streets[street] {
		if ob.spot.kind == "rock" && ob.mining(cl.ID()) {
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

func (o *objects) useNode(street string, ob *liveObj, verb string, perks map[string]bool) verdict {
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
	if rule.After != nil {
		// mortar_barnacle.js / ice_knob.js / jellisac.js: back to the first state.
		ob.units = *rule.After
	} else {
		// dirt_pile.js / peat_1.js: one use comes off.
		ob.units = max(rule.Min, ob.units-max(1, rule.Consume))
	}
	// The regrow timer restarts on every use (resource-nodes.js harvestResourceNode).
	o.schedule(street, ob, o.regrowDelay(rule), o.regrowNode)
	y := objNodeYield[class]
	return verdict{ok: true, got: creditItem(y.c, y.n)}
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
			d := time.Duration(rule.Tend.ResetMs) * time.Millisecond
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
			})
		}
		return verdict{ok: true}
	}
	return refuse("bad_verb", "You can't do that here.")
}

// ---------------------------------------------------------------------------
// Trees (js/trants.js, js/trant-care.js; items/include/trants.js)
// ---------------------------------------------------------------------------

func (o *objects) tendTree(ob *liveObj, verb, skill string) verdict {
	switch verb {
	case "harvest":
		// trants.js canHarvestTrant: too small below maturity 3.
		if ob.maturity < 3 {
			return refuse("small", "This tree is too small to harvest.")
		}
		// include/trants.js performHarvest 234-237.
		if ob.fruit <= 0 {
			return refuse("empty", notReady["tree"])
		}
		// catalog_skill_packages.js gardening_harvest bonus_amount by Arborology
		// rank (removeFruit 447-461 takes at most what is there). A super harvest's
		// bonus never comes off the tree (trants.js harvestTrant), so it stays local.
		amount, ok := o.w.rules.Harvest[skill]
		if !ok {
			amount = o.w.rules.Harvest["no_skill"]
		}
		take := min(amount, ob.fruit)
		ob.fruit -= take
		return verdict{ok: true, extra: map[string]any{"take": take}, got: creditItem(objFruit[ob.spot.class], take)}
	case "pet", "water":
		if verb == "pet" && ob.petted {
			return refuse("not_wanted", "This tree was already petted today.")
		}
		if verb == "water" && ob.watered {
			return refuse("not_wanted", "This tree was already watered today.")
		}
		// trant-care.js performTrantCare: the success roll comes first
		// (light_green_thumb_pet / _water success_rate by rank).
		rates := o.w.rules.Care[verb]
		rate, ok := rates[skill]
		if !ok {
			rate = rates["no_skill"]
		}
		roll := o.roll()
		success := roll*100 < rate
		if success {
			if verb == "pet" {
				ob.petted = true
			} else {
				ob.watered = true
			}
			// include/trants.js performTending 670-684: both together lift health once.
			if ob.petted && ob.watered {
				ob.health = min(10, max(1, ob.health+1))
			}
			ob.cap = o.w.capAt(ob.spot.class, ob.maturity, ob.health)
			ob.fruit = min(ob.fruit, ob.cap)
		}
		return verdict{ok: true, extra: map[string]any{"roll": roll, "success": success}}
	}
	return refuse("bad_verb", "You can't do that to a tree.")
}

// newDay is game.js resetAllTrantsForNewDay for one tree: the tending flags
// clear and the fruit refills to 55-80% of what it can hold (never less than
// it has).
func (o *objects) newDay(ob *liveObj) bool {
	today := o.today()
	if ob.day >= today {
		return false
	}
	ob.day = today
	ob.petted, ob.watered = false, false
	ob.cap = o.w.capAt(ob.spot.class, ob.maturity, ob.health)
	target := int(math.Floor(float64(ob.cap) * (0.55 + o.roll()*0.25)))
	ob.fruit = min(ob.cap, max(ob.fruit, target))
	return true
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
		if trees {
			o.armDay()
		}
	})
}
