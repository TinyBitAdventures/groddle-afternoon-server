package main

// Street counts for the Encyclopedia's street pages (register G1277, report #686; Austin 2026-11-25:
// "Maybe the live count could say last check within last 24 hours, so a cached lookup rather then a
// realtime check"). A player saw no Wood Trees on Thicket where its Encyclopedia page lists 3: all
// three had been felled, and a felled Wood Tree is a Patch, as in Glitch (wood_tree.js endHarvest
// 785-788, onDie 857-866: replaceWith('patch')). The page now shows what stands on the street as of
// the last check beside what the street starts with. The site asks at most once an hour and keeps
// the answer (groddle-afternoon-site src/StreetLive.php); nothing here runs on a timer.
//
// A signed site call as in admin.go (POST /ws?call=street_counts, X-Groddle-Service over
// /realtime/v1/street_counts and the body, 60 s, each nonce once), never a socket message:
//
//	street_counts {}   200 {"status":"ok","at":ms,"streets":{"GLI…":{"trees":{"wood_tree":[0,3],"trant_bean":[1,0]},
//	                        "patches":{"empty":5,"seedlings":1,"dead":1,"grown":{"trant_spice":1}}}}}
//
// trees: each class of the street's own trees as laid out (data/objects.json, or its built street
// upgrade's layout), [standing, gone]. Gone is a tree that died: felled to maturity 0, poisoned, or a
// trant the Rook struck at health 1 (objects.go killTree). A Wood Tree leaves a Patch, a trant its
// dead tree.
// patches: on a street whose Patches are shared (patches.go, -patches on), every plot, the layout's
// and those a dead tree left (tree:<i>): empty (weedy, tended or dug), a Seedling, a grown tree by its
// class, a dead tree waiting to be cleared. Zero counts are left out, and a street with neither trees
// nor shared Patches is not listed.
//
// Object counts only: no player, name or position, so the site may show it to anyone.

import (
	"encoding/json"
	"net/http"
)

func init() {
	serviceHandlers["street_counts"] = streetCountsService
}

// treeTally is one tree class on a street: [standing, gone].
type treeTally [2]int

// patchTally is a street's shared Patch plots by what is in them.
type patchTally struct {
	Empty     int            `json:"empty,omitempty"`
	Seedlings int            `json:"seedlings,omitempty"`
	Dead      int            `json:"dead,omitempty"`
	Grown     map[string]int `json:"grown,omitempty"`
}

func (p *patchTally) none() bool {
	return p.Empty == 0 && p.Seedlings == 0 && p.Dead == 0 && len(p.Grown) == 0
}

// streetTally is one street's row in the answer.
type streetTally struct {
	Trees   map[string]treeTally `json:"trees,omitempty"`
	Patches *patchTally          `json:"patches,omitempty"`
}

func streetCountsService(h *Hub, body []byte) (int, any) {
	if !json.Valid(body) {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return http.StatusOK, map[string]any{"status": "ok", "at": h.now().UnixMilli(), "streets": streetCountsLocked(h)}
}

// streetCountsLocked is every street's tally (the hub lock held). It only reads: no layout moves, no
// timers armed, nothing saved.
func streetCountsLocked(h *Hub) map[string]*streetTally {
	out := map[string]*streetTally{}
	row := func(street string) *streetTally {
		if out[street] == nil {
			out[street] = &streetTally{}
		}
		return out[street]
	}
	if o, _ := h.provided["objects.self"].(*objects); o != nil {
		for street := range o.w.streets {
			if trees := o.treeCounts(street); len(trees) > 0 {
				row(street).Trees = trees
			}
		}
	}
	if g, _ := h.provided["patches.self"].(*patches); g != nil && !g.off {
		for street := range g.w.streets {
			if p := g.plotCounts(street); p != nil {
				row(street).Patches = p
			}
		}
	}
	return out
}

// treeCounts is the street's trees by class, standing and gone (nil when it has none).
func (o *objects) treeCounts(street string) map[string]treeTally {
	spots := o.w.streets[street]
	if v := o.variant(street); v != nil {
		spots = v.spots
	}
	var out map[string]treeTally
	for id, spot := range spots {
		if spot.kind != "tree" && spot.kind != "wood" {
			continue
		}
		if out == nil {
			out = map[string]treeTally{}
		}
		t := out[spot.class]
		if ob := o.streets[street][id]; ob != nil && ob.spot.kind == spot.kind && treeGone(ob) {
			t[1]++
		} else {
			t[0]++
		}
		out[spot.class] = t
	}
	return out
}

// treeGone: the tree died, or (a Wood Tree) was chopped to 0 and falls in its two seconds (armWoodDie).
func treeGone(ob *liveObj) bool {
	return ob.dead || (ob.spot.kind == "wood" && ob.maturity <= 0)
}

// plotCounts is the street's shared Patch plots by what is in them (nil when its Patches are not shared
// or it has none).
func (g *patches) plotCounts(street string) *patchTally {
	if !g.w.shared(street) {
		return nil
	}
	t := &patchTally{}
	count := func(p *pPlot) {
		switch {
		case p.stage == "seedling":
			t.Seedlings++
		case p.stage == "tree" && p.tree != nil:
			if t.Grown == nil {
				t.Grown = map[string]int{}
			}
			t.Grown[p.tree.class]++
		case p.stage == "dead":
			t.Dead++
		default:
			t.Empty++
		}
	}
	live := g.plots[street]
	for key, spot := range g.w.streets[street].spots {
		if p := live[key]; p != nil {
			count(p)
		} else {
			count(freshPatchPlot(spot))
		}
	}
	for key, p := range live {
		if g.w.streets[street].spots[key] == nil {
			count(p) // a plot a dead tree left (tree:<i>), its spot its own
		}
	}
	if t.none() {
		return nil
	}
	return t
}
