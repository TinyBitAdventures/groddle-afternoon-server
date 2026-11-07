package main

// The tools behind shared takes (G335 slice 5). Mining a rock, chopping a Wood
// Tree, scraping a Mortar Barnacle or an Ice Nubbin, digging a Dirt Pile,
// cutting Peat and watering a tree each change something everyone on the
// street sees, and the take is credited to the site's ledger as an exact
// receipt (credits.go). The source refused each without the tool, so before
// this server lets one land it asks the site's ledger whether the player holds
// one: POST ledger/has {"id":7,"c":[every tool class below]} answers
// {"status":"settled","has":["pick","watering_can"]} (groddle-afternoon-site
// src/LedgerUse.php). Nothing is used: the tool's wear stays the client's, as
// in solo (tool_base use() lowers points_remaining in the player's own pack;
// the ledger counts tools, not their points).
//
// Off the hot path: one call asks for every tool group at once, the first
// time a player does a tool-gated act on a street. A "has one" answer is kept
// for that street visit, at most toolFresh; a "has none" only toolNoFresh, so a
// player who buys one from a vendor on the same street is asked again almost
// at once (the ledger learns of a purchase with the next save write, a few
// seconds later). While the call is out, that player's tool-gated acts wait
// for it (at most toolWaitMax). No answer from the site lets the act land
// without its credit (the take then goes through the earning budget, as before
// slice 3) and asks again next time: an honest player is never stopped by the
// site being slow.
//
// Refusals use the source's lines, obj_no code "no_tool":
//   - a rock: "You'll need a pick first." (rock_*.js conditions, is_pick)
//   - a Wood Tree: wood_tree.js:678 (is_hatchet 673: hatchet or class_axe)
//   - a Mortar Barnacle or Ice Nubbin: "You'll need a working scraper first." (mortar_barnacle.js:332, ice_knob.js:356)
//   - a Dirt Pile: "You need a working shovel." (dirt_pile.js:109)
//   - Peat: "You'll need a working shovel first." (peat_1.js:354)
//   - watering a tree or a Paper Tree: "That's going to be tough without a watering can :(" (include/trants.js:126, paper_tree.js:660)
//
// The classes are the game's own (js/mining.js findWorkingPick, js/woodcutting.js
// WOOD_TOOLS, js/resource-nodes.js SCRAPERS, SHOVELS, WATERING_CANS).
// Without the site's ledger (-trade-ledger off, tests) the tool is the client's word.

import (
	"log"
	"time"
)

const (
	toolFresh   = 10 * time.Minute
	toolNoFresh = 5 * time.Second
	toolWaitMax = 4
)

type toolNeed struct {
	group   string
	classes []string
	line    string
}

var (
	toolPick    = toolNeed{"pick", []string{"pick", "fancy_pick"}, "You'll need a pick first."}
	toolHatchet = toolNeed{"hatchet", []string{"hatchet", "class_axe"}, "You can't just pluck planks from a tree! You need something to gently extract them with. A hatchet, maybe?"}
	toolScraper = toolNeed{"scraper", []string{"scraper", "super_scraper"}, "You'll need a working scraper first."}
	toolShovel  = toolNeed{"shovel", []string{"shovel", "ace_of_spades"}, "You need a working shovel."}
	toolPeat    = toolNeed{"shovel", []string{"shovel", "ace_of_spades"}, "You'll need a working shovel first."}
	toolCan     = toolNeed{"can", []string{"watering_can", "irrigator_9000"}, "That's going to be tough without a watering can :("}
	// A shared public garden (gardens.go): the plots' own lines (js/garden.js waterPlot, clearPlot), as
	// the source answers only with a talkback (include/garden.js 217-228 broken_watering_can, 326-330 broken_hoe).
	toolGardenCan = toolNeed{"can", []string{"watering_can", "irrigator_9000"}, "You need a Watering Can to water plots."}
	toolHoe       = toolNeed{"hoe", []string{"hoe", "high_class_hoe"}, "You need a Hoe to clear dirty plots."}

	// toolClasses is every class asked about, once.
	toolClasses = []string{"pick", "fancy_pick", "hatchet", "class_axe", "scraper", "super_scraper", "shovel", "ace_of_spades", "watering_can", "irrigator_9000", "hoe", "high_class_hoe"}
)

// toolFor is the tool a verb on spot needs, or nil.
func toolFor(spot *objSpot, verb string) *toolNeed {
	switch {
	case spot.kind == "rock" && verb == "mine":
		return &toolPick
	case spot.kind == "wood" && verb == "harvest":
		return &toolHatchet
	case spot.kind == "tree" && verb == "water":
		return &toolCan
	case spot.kind != "node":
		return nil
	}
	switch {
	case (spot.class == "mortar_barnacle" || spot.class == "ice_knob") && verb == "scrape":
		return &toolScraper
	case spot.class == "dirt_pile" && verb == "dig":
		return &toolShovel
	case spot.class == "peat_1" && verb == "harvest":
		return &toolPeat
	case spot.class == "paper_tree" && verb == "water":
		return &toolCan
	}
	return nil
}

// toolCheck is one player's last answer, for one street visit.
type toolCheck struct {
	street  string
	at      time.Time
	held    map[string]bool // group -> holds one
	asking  bool
	waiting []func(held map[string]bool, ok bool)
}

// withTool runs act once the ledger says cl holds need (credit true), or without the credit when the
// site gave no answer; refuses with the source's line when it holds none.
func (o *objects) withTool(cl *Client, street string, need *toolNeed, refuse func(code, message string), act func(credit bool)) {
	site := o.uses.ledger
	if site == nil {
		act(true)
		return
	}
	id := cl.ID()
	now := o.h.Now()
	c := o.uses.tools[id]
	if c != nil && !c.asking && c.street == street {
		age := now.Sub(c.at)
		if c.held[need.group] && age < toolFresh {
			act(true)
			return
		}
		if !c.held[need.group] && age < toolNoFresh {
			refuse("no_tool", need.line)
			return
		}
	}
	then := func(held map[string]bool, ok bool) {
		switch {
		case !cl.Online() || cl.Street() != street:
		case !ok:
			act(false)
		case held[need.group]:
			act(true)
		default:
			refuse("no_tool", need.line)
		}
	}
	if c != nil && c.asking {
		if len(c.waiting) >= toolWaitMax {
			refuse("slow_down", "Slow down a little.")
			return
		}
		c.waiting = append(c.waiting, then)
		return
	}
	c = &toolCheck{street: street, asking: true, waiting: []func(map[string]bool, bool){then}}
	o.uses.tools[id] = c
	o.uses.spawn(func() {
		classes, ok := site.has(id, toolClasses)
		o.h.After(0, func() {
			if o.uses.tools[id] != c {
				return
			}
			held := map[string]bool{}
			for _, n := range []toolNeed{toolPick, toolHatchet, toolScraper, toolShovel, toolCan, toolHoe} {
				for _, cls := range n.classes {
					held[n.group] = held[n.group] || classes[cls]
				}
			}
			waiting := c.waiting
			c.asking, c.waiting = false, nil
			if ok {
				c.at, c.held = o.h.Now(), held
				log.Printf("objects: tools for %d on %s: %v", id, street, held)
			} else {
				delete(o.uses.tools, id)
				log.Printf("objects: tools for %d: no answer from the ledger (the take goes uncredited)", id)
			}
			for _, fn := range waiting {
				fn(held, ok)
			}
		})
	})
}

// withGardenTool is withTool for a shared public garden's verbs (gardens.go, through the hook
// "objects.withTool"): group "can" or "hoe", the same answers kept for the same street visit.
func (o *objects) withGardenTool(cl *Client, street, group string, refuse func(code, message string), act func(credit bool)) {
	need := &toolGardenCan
	if group == "hoe" {
		need = &toolHoe
	}
	o.withTool(cl, street, need, refuse, act)
}
