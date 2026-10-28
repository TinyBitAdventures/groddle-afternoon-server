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
// here; the site may have used the charge all the same (a lost answer), which
// costs the player that one charge.
//
// Without the site's ledger (-trade-ledger off, tests) the jar is the
// client's word as before and "use" is left out.

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"testing"
	"time"
)

const (
	ledgerRouteUse = "/groddle/v1/ledger/use"
	// fertilidust.js:296 / fertilidust_lite.js:292: item.events_add({callback: 'onFertilidust'}, 3).
	dustLand = 3 * time.Second

	useTextLacks      = "You don't have enough of that item." // inc_trading.js:456
	useTextNoAntidote = "You don't have any antidote!"        // trant_fruit.js:265, wood_tree.js:257
	useTextNoAnswer   = "Your pack could not be checked just now. Try again in a moment."
	useTextBusy       = "Someone is already pouring something on that tree."
)

// The classes a shared act uses, by act (js/tree-poison.js TREE_POISON / TREE_ANTIDOTE; js/powders.js).
const (
	useFertilidust     = "fertilidust"
	useFertilidustLite = "fertilidust_lite"
	usePoison          = "potion_tree_poison"
	useAntidote        = "potion_tree_poison_antidote"
)

// useLedger is the site's ledger as these acts use it (httpLedger).
type useLedger interface {
	use(op string, id int64, class string) ledgerAnswer
}

func (l *httpLedger) use(op string, id int64, class string) ledgerAnswer {
	return l.call(ledgerRouteUse, map[string]any{"op": op, "id": id, "c": class})
}

// objUses is the objects module's side of it.
type objUses struct {
	ledger  useLedger    // nil: the client's word
	spawn   func(func()) // runs a ledger call off the hub lock (tests run it in place)
	newOp   func() string
	asking  map[int64]bool  // a player's use waiting for the site
	pouring map[string]bool // street + "|" + tree id: a pour waiting for the site
}

func (o *objects) initUses() {
	o.uses = objUses{
		spawn:   func(fn func()) { go fn() },
		asking:  map[int64]bool{},
		pouring: map[string]bool{},
		newOp: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return "use-" + hex.EncodeToString(b)
		},
	}
	if !testing.Testing() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				o.uses.ledger = l
				log.Printf("objects: Fertilidust, Tree Poison and Antidote are used through the ledger at %s", l.base)
			}
		}
	}
}

// useView is what the player hears of the site's word.
func useView(op, class string, a ledgerAnswer) map[string]any {
	left := 0
	if a.Left != nil {
		left = *a.Left
	}
	return map[string]any{"op": op, "c": class, "left": left, "spent": a.Spent}
}

// askUse uses one charge of class for cl through the ledger, off the hub lock, then runs then
// under it: the use (nil without a ledger) or a refusal (code and line) already sent nowhere.
func (o *objects) askUse(cl *Client, class string, then func(use map[string]any, code, message string)) {
	site := o.uses.ledger
	if site == nil {
		then(nil, "", "")
		return
	}
	id := cl.ID()
	if o.uses.asking[id] {
		then(nil, "slow_down", "Slow down a little.")
		return
	}
	o.uses.asking[id] = true
	op := o.uses.newOp()
	o.uses.spawn(func() {
		a := site.use(op, id, class)
		o.h.After(0, func() {
			delete(o.uses.asking, id)
			switch a.Status {
			case "settled":
				left := -1
				if a.Left != nil {
					left = *a.Left
				}
				log.Printf("objects: %s for %d used through the ledger (%s, %d left, spent %v)", class, id, op, left, a.Spent)
				then(useView(op, class, a), "", "")
			case "refused":
				log.Printf("objects: %s for %d refused by the ledger (%s): it holds none", class, id, op)
				line := useTextLacks
				if class == useAntidote {
					line = useTextNoAntidote
				}
				then(nil, "no_item", line)
			default:
				log.Printf("objects: %s for %d: no answer from the ledger (%s)", class, id, op)
				then(nil, "no_answer", useTextNoAnswer)
			}
		})
	})
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
