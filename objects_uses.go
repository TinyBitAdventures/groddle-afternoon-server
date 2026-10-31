package main

// Shared-object acts that use up something the player carries (G335 slice 4).
// Fertilidust grows every tree on a street for everyone, its Lite the closest
// one, Tree Poison kills a street tree for everyone and its Antidote saves
// one, so the jar or bottle behind each is not the client's word: before this
// server grows, poisons or cures a shared tree, the site's ledger confirms the
// player holds the thing and uses one charge of it.
//
//	POST <site>/wp-json/groddle/v1/ledger/use  {"op":"use-<16 hex>","id":7,"c":"fertilidust"}
//	     200 {"status":"settled","left":4,"spent":false}   one charge used; left in the open jar
//	     409 {"status":"refused","short":{"id":7,"c":"fertilidust"}}   the ledger holds none
//
// Idempotent per op (the site keeps each once: groddle-afternoon-site
// src/LedgerUse.php), so the retries ledger.go makes with the same body are
// safe. A jar or bottle has five charges: Fertilidust and Fertilidust Lite
// maxCharges 5 (items/fertilidust.js:23, fertilidust_lite.js:23; powder_base
// use() takes one, 529-534, and the jar is deleted at 0, 491-492), Tree Poison
// and its Antidote points_remaining 5 (potion_tree_poison.js:36,
// potion_tree_poison_antidote.js; tool_base use(1), the fifth pour consumes
// the bottle). The site counts the open one's charges; "spent" means this use
// emptied it and one left the ledger (recorded like a Party Pack's spend, so
// the save is held to it once it lists the op as applied).
//
// The player's answer carries the site's word, "use":{"op","c","left","spent"}:
// on obj_ok for a pour, on obj_dust_used for a dust (sent as soon as the
// ledger agrees; the trees grow dustLand later, as the source's onFertilidust
// event does, fertilidust.js:296 and fertilidust_lite.js:292 events_add 3), so
// the page uses the same charge the ledger did. A refusal is obj_no with code
// "no_item" and the source's line: "You don't have any antidote!" (trant_fruit.js:265,
// wood_tree.js:257); for the poison and the dust, whose verbs the source simply
// hides without the item, the ledger's own words for a giver who lacks
// something (inc_trading.js:456, as trades refuse).
//
// While a pour waits for the site the tree is held: another pour on it is
// refused ("busy"), so a charge is never used on a tree someone else poisoned
// in the meantime. A player has one use waiting at a time. No answer from the
// site (down, after ledger.go's retries) refuses the act and changes nothing
// here, and the op is given back (below).
//
// A lost answer (G335 slice 5): the site keeps each op's answer, so the
// retries ledger.go makes with the same body settle once. When no answer comes
// at all, the act is refused here and the op is given back: the httpLedger
// journals each op before it asks (-uses-file, realtime-uses.json beside
// -secret-file; off: memory only) and, for one that got no answer, calls
// POST ledger/use/void {"op","id"} with a backoff until the site answers.
// The site puts the charge (or the emptied jar) back unless a load already
// applied it, and a late copy of the use answers void, so the player keeps
// what the act never spent. A restart voids whatever the journal still holds.
//
// The same asking serves the other shared acts that use something up (slice 5):
// a street Piggy sated by food (objects_pigs.go askFeed: whole items, "n"), a
// Rook Balm's full revive (rook.go), and Sneezing and Sparkle Powder spread to
// the street (hi.go). Each module holds its own useAsker.
//
// Without the site's ledger (-trade-ledger off, tests) the jar is the
// client's word as before and "use" is left out.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
	"regexp"
	"sync"
	"testing"
	"time"
)

const (
	ledgerRouteUse     = "/groddle/v1/ledger/use"
	ledgerRouteUseVoid = "/groddle/v1/ledger/use/void"
	ledgerRouteHas     = "/groddle/v1/ledger/has"
	// fertilidust.js:296 / fertilidust_lite.js:292: item.events_add({callback: 'onFertilidust'}, 3).
	dustLand = 3 * time.Second

	useTextLacks      = "You don't have enough of that item." // inc_trading.js:456
	useTextNoAntidote = "You don't have any antidote!"        // trant_fruit.js:265, wood_tree.js:257
	useTextNoAnswer   = "Your pack could not be checked just now. Try again in a moment."
	useTextBusy       = "Someone is already pouring something on that tree."
)

// The classes a shared act uses, by act (js/tree-poison.js TREE_POISON / TREE_ANTIDOTE; js/powders.js;
// js/rook-attack.js BALM_CLASS).
const (
	useFertilidust     = "fertilidust"
	useFertilidustLite = "fertilidust_lite"
	usePoison          = "potion_tree_poison"
	useAntidote        = "potion_tree_poison_antidote"
	useBalm            = "potion_rook_balm"
	useSneezing        = "sneezing_powder"
	useSparkle         = "sparkle_powder"
)

// useOpRe is newUseOp's shape (the site's LedgerUse::isUse).
var useOpRe = regexp.MustCompile(`^use-[0-9a-f]{16}$`)

var usesFileFlag = flag.String("uses-file", "", "file that keeps ledger uses not yet answered, so a lost answer is given back after a restart (default: realtime-uses.json beside -secret-file; off: memory only)")

// usesFileForTest lets a test give the journal a file; under go test the flag is ignored.
var usesFileForTest string

// useLedger is the site's ledger as these acts use it (httpLedger).
type useLedger interface {
	// use takes one charge of class, or n whole items of it when n > 0.
	use(op string, id int64, class string, n int) ledgerAnswer
	// has says which of classes the player holds one of at least (ok false: no answer).
	has(id int64, classes []string) (held map[string]bool, ok bool)
}

func (l *httpLedger) use(op string, id int64, class string, n int) ledgerAnswer {
	body := map[string]any{"op": op, "id": id, "c": class}
	if n > 0 {
		body["n"] = n
	}
	j := l.useJournal()
	j.asking(op, id)
	a := l.call(ledgerRouteUse, body)
	if a.Status == "" {
		j.lost(op, id)
	} else {
		j.answered(op)
	}
	return a
}

func (l *httpLedger) has(id int64, classes []string) (map[string]bool, bool) {
	a := l.call(ledgerRouteHas, map[string]any{"id": id, "c": classes})
	if a.Status != "settled" {
		return nil, false
	}
	held := map[string]bool{}
	for _, c := range a.Has {
		held[c] = true
	}
	return held, true
}

// useJournal is the process's one journal of ledger uses (opened with the first ledger that asks).
func (l *httpLedger) useJournal() *useVoids {
	theUseVoidsOnce.Do(func() {
		theUseVoids = openUseVoids(statePath(usesFileForTest, usesFileFlag, "realtime-uses.json"), l.call, time.Sleep)
	})
	return theUseVoids
}

var (
	theUseVoidsOnce sync.Once
	theUseVoids     *useVoids
)

// useVoids journals each use op from before it is asked until the site answers it, and gives back
// the ones that got no answer: POST ledger/use/void, again after a backoff (creditBackoffMin doubling
// to creditBackoffMax) until the site answers. Its own lock; writes happen off the hub lock.
type useVoids struct {
	mu    sync.Mutex
	path  string           // "": memory only
	ops   map[string]int64 // op -> player: asked and not answered, or being given back
	call  func(route string, payload any) ledgerAnswer
	pause func(time.Duration)
}

func openUseVoids(path string, call func(route string, payload any) ledgerAnswer, pause func(time.Duration)) *useVoids {
	v := &useVoids{path: path, ops: map[string]int64{}, call: call, pause: pause}
	if path != "" {
		var kept map[string]int64
		if ok, err := readStateFile("uses", path, &kept); err != nil {
			log.Printf("uses %s: %v (kept in memory only)", path, err)
			v.path = ""
		} else if ok {
			for op, id := range kept {
				if useOpRe.MatchString(op) && id > 0 {
					v.ops[op] = id
				}
			}
		}
	}
	for op, id := range v.ops {
		log.Printf("uses: %s for %d had no answer before the restart: giving it back", op, id)
		go v.giveBack(op, id)
	}
	return v
}

func (v *useVoids) asking(op string, id int64) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.ops[op] = id
	v.saveLocked()
}

func (v *useVoids) answered(op string) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.ops[op]; ok {
		delete(v.ops, op)
		v.saveLocked()
	}
}

// lost: the use got no answer, so the act did not happen here; the site gives back what it used.
func (v *useVoids) lost(op string, id int64) {
	if v == nil {
		return
	}
	go v.giveBack(op, id)
}

func (v *useVoids) giveBack(op string, id int64) {
	wait := creditBackoffMin
	for {
		a := v.call(ledgerRouteUseVoid, map[string]any{"op": op, "id": id})
		if a.Status != "" {
			log.Printf("uses: %s for %d given back (the site says %s)", op, id, a.Status)
			v.answered(op)
			return
		}
		v.pause(wait)
		wait = min(wait*2, creditBackoffMax)
	}
}

func (v *useVoids) saveLocked() {
	if v.path == "" {
		return
	}
	b, _ := json.Marshal(v.ops)
	if err := writeFileSynced(v.path, b); err != nil {
		log.Printf("uses %s: %v", v.path, err)
	}
}

// useAsker asks the site's ledger before one module's shared acts that use something up.
type useAsker struct {
	h      *Hub
	ledger useLedger    // nil: the client's word
	spawn  func(func()) // runs a ledger call off the hub lock (tests run it in place)
	newOp  func() string
	asking map[int64]bool // a player's use waiting for the site
}

func newUseAsker(h *Hub, what string) *useAsker {
	a := &useAsker{h: h, spawn: func(fn func()) { go fn() }, asking: map[int64]bool{}, newOp: newUseOp}
	if !testing.Testing() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				a.ledger = l
				l.useJournal()
				log.Printf("%s: used through the ledger at %s", what, l.base)
			}
		}
	}
	return a
}

func newUseOp() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "use-" + hex.EncodeToString(b)
}

// objUses is the objects module's side of it: the asker, and the trees held while a pour waits.
type objUses struct {
	*useAsker
	pouring map[string]bool // street + "|" + tree id: a pour waiting for the site
	// The tools behind shared takes (objects_tools.go): each player's last "has one" answer.
	tools map[int64]*toolCheck
}

func (o *objects) initUses() {
	o.uses = objUses{
		useAsker: newUseAsker(o.h, "objects: Fertilidust, Tree Poison, Antidote, Piggy food and tools"),
		pouring:  map[string]bool{},
		tools:    map[int64]*toolCheck{},
	}
}

// useView is what the player hears of the site's word.
func useView(op, class string, n int, a ledgerAnswer) map[string]any {
	left := 0
	if a.Left != nil {
		left = *a.Left
	}
	v := map[string]any{"op": op, "c": class, "left": left, "spent": a.Spent}
	if n > 0 {
		v["n"] = n
	}
	return v
}

// useLine is the refusal a player hears when the ledger holds none of class.
func useLine(class string) string {
	if class == useAntidote {
		return useTextNoAntidote
	}
	return useTextLacks
}

// ask uses one charge of class (or n whole items, n > 0) for cl through the ledger, off the hub
// lock, then runs then under it: the use (nil without a ledger) or a refusal (code and line).
func (a *useAsker) ask(cl *Client, class string, n int, then func(use map[string]any, code, message string)) {
	site := a.ledger
	if site == nil {
		then(nil, "", "")
		return
	}
	id := cl.ID()
	if a.asking[id] {
		then(nil, "slow_down", "Slow down a little.")
		return
	}
	a.asking[id] = true
	op := a.newOp()
	a.spawn(func() {
		ans := site.use(op, id, class, n)
		a.h.After(0, func() {
			delete(a.asking, id)
			switch ans.Status {
			case "settled":
				left := -1
				if ans.Left != nil {
					left = *ans.Left
				}
				log.Printf("uses: %s x%d for %d used through the ledger (%s, %d left, spent %v)", class, max(n, 1), id, op, left, ans.Spent)
				then(useView(op, class, n, ans), "", "")
			case "refused":
				log.Printf("uses: %s for %d refused by the ledger (%s): it holds none", class, id, op)
				then(nil, "no_item", useLine(class))
			default:
				log.Printf("uses: %s for %d: no answer from the ledger (%s), given back", class, id, op)
				then(nil, "no_answer", useTextNoAnswer)
			}
		})
	})
}

// askUse is the objects module's ask for one charge.
func (o *objects) askUse(cl *Client, class string, then func(use map[string]any, code, message string)) {
	o.uses.ask(cl, class, 0, then)
}

// pourRefusal is the pour's own refusal before anything is used (pour decides the rest).
func pourRefusal(ob *liveObj, verb string) (verdict, bool) {
	switch {
	case ob.dead:
		return refuse("dead", treeIsDead), true
	case verb == "antidote" && ob.poisonEnd.IsZero():
		return refuse("not_poisoned", noPoisonedTree), true
	case verb == "poison" && !ob.poisonEnd.IsZero():
		return refuse("poisoned", alreadyPoison), true
	}
	return verdict{}, false
}

// askPour is a Tree Poison or Antidote pour on a shared tree when the ledger holds the bottles:
// the bottle's charge first, then the pour (handleAct's own path, finishAct, with "use" on obj_ok).
func (o *objects) askPour(cl *Client, m objAct, street string, spot *objSpot) {
	ob := o.get(street, spot)
	if v, no := pourRefusal(ob, m.Verb); no {
		o.finishAct(cl, m, street, ob, o.state(ob), v)
		return
	}
	key := street + "|" + spot.id
	if o.uses.pouring[key] {
		o.finishAct(cl, m, street, ob, o.state(ob), refuse("busy", useTextBusy))
		return
	}
	class := usePoison
	if m.Verb == "antidote" {
		class = useAntidote
	}
	o.uses.pouring[key] = true
	o.askUse(cl, class, func(use map[string]any, code, message string) {
		delete(o.uses.pouring, key)
		ob := o.get(street, spot)
		before := o.state(ob)
		if code != "" {
			o.finishAct(cl, m, street, ob, before, refuse(code, message))
			return
		}
		v, no := pourRefusal(ob, m.Verb)
		if !no {
			v = o.pour(street, ob, m.Verb, cl.ID(), m.Ms)
		} else {
			// The tree was held, so only the Rook can have changed it: the charge is used all the same.
			log.Printf("objects: a %s on %s %s by %d was paid for, but the tree changed first (%s)", m.Verb, street, spot.id, cl.ID(), v.code)
		}
		if v.ok && use != nil {
			if v.extra == nil {
				v.extra = map[string]any{}
			}
			v.extra["use"] = use
		}
		o.finishAct(cl, m, street, ob, before, v)
	})
}

// dustTrees is Fertilidust on targets: the trees it grew, everyone told, the user answered.
func (o *objects) dustTrees(cl *Client, req int64, street string, targets []*objSpot) {
	ids := []string{}
	var touched, changed []*liveObj
	for _, spot := range targets {
		ob := o.get(street, spot)
		touched = append(touched, ob)
		if o.fertilize(street, ob) {
			ids = append(ids, spot.id)
			changed = append(changed, ob)
		}
	}
	log.Printf("objects: Fertilidust on %s by %d grew %d of %d trees", street, cl.ID(), len(ids), len(targets))
	if cl.Online() && cl.Street() == street {
		o.h.SendLocked(cl, map[string]any{"t": "obj_dusted", "req": req, "ids": ids})
	}
	if len(changed) > 0 {
		o.broadcast(street, 0, changed...)
	}
	for _, ob := range touched {
		o.forget(street, ob)
	}
}
