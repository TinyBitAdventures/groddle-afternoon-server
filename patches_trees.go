package main

// Shared street Patches, slice 2 (register G564): the Rook, Fertilidust and Trantsformation Fluid on
// the trees grown in shared Patches. The rules and their sources are in patches.go's header; the
// wire is objects.go's obj_dust for Fertilidust, rook.go's for the Rook, and patch_act "trantsform".

import (
	"log"
	"slices"
	"sort"
	"strings"
)

// rookKeyOf is the Rook's key for the tree in a Patch: "<class>:p<plot key>" (the page's
// js/rook-attack.js rookKeyFor, the same on every page).
func rookKeyOf(class, key string) string { return class + ":p" + key }

// livingTree is a plot's grown tree a Rook, a dust or a pour can reach: a trant, or a Wood Tree not
// yet cleared (onDie: gone in the source).
func livingTree(p *pPlot) bool {
	return p != nil && p.stage == "tree" && p.tree != nil && (!p.tree.wood() || p.tree.maturity > 0)
}

// rooked asks the Rook whether the tree in a plot is rooked now (rook.go thingRooked).
func (g *patches) rooked(street string, p *pPlot) bool {
	if p == nil || p.tree == nil {
		return false
	}
	fn, ok := g.h.ProvidedLocked("rook.thingRooked").(func(string, string) bool)
	return ok && fn(street, rookKeyOf(p.tree.class, p.spot.key))
}

// unrook tells the Rook the tree in a plot is gone (it died), so a tree grown there later is not
// rooked from the start.
func (g *patches) unrook(street, class, key string) {
	if fn, ok := g.h.ProvidedLocked("rook.unrook").(func(string, string)); ok {
		fn(street, rookKeyOf(class, key))
	}
}

// rookFruit is the fruit a rooked trant grows back while it waits (objects.go armRookFruit,
// include/trants.js 1170-1232): from step 6 (12 s after the strike) every 4 s, its fruit trimmed to
// the capacity a health lower and the fruitProdMap cell of its health pushed back on, until the fruit
// stops changing. A revival ends it (and leaves the fruit). True when the fruit changed.
func (g *patches) rookFruit(street string, p *pPlot, now int64) bool {
	t := p.tree
	if t.rookNext <= 0 || t.rookNext > now {
		return false
	}
	if !g.rooked(street, p) {
		t.rookNext = 0
		return false
	}
	changed := false
	for guard := 0; guard < 64 && t.rookNext > 0 && t.rookNext <= now; guard++ {
		before := t.fruit
		low := g.ow.capAt(t.class, t.maturity, t.health-1)
		t.cap = g.ow.capAt(t.class, t.maturity, t.health)
		t.fruit = min(t.cap, min(t.fruit, low)+g.ow.prodAt(t.class, t.maturity, t.health))
		if t.fruit == before {
			t.rookNext = 0
			break
		}
		changed = true
		t.rookNext += rookFruitEvery.Milliseconds()
	}
	return changed
}

// rookable is the street's living Patch trees by their Rook keys (rook.go wingBeat), each with its
// class; none on a street whose Patches are not shared.
func (g *patches) rookable(street string) map[string]string {
	if !g.w.shared(street) {
		return nil
	}
	g.resume()
	now := g.nowMs()
	out := map[string]string{}
	var changed []string
	for key, p := range g.plots[street] {
		if g.advance(street, p, now) {
			changed = append(changed, key)
		}
		if livingTree(p) {
			out[rookKeyOf(p.tree.class, key)] = p.tree.class
		}
	}
	if len(changed) > 0 {
		sort.Strings(changed)
		g.broadcast(street, changed, 0)
	}
	return out
}

// rookTree is a wing beat rooking the tree in a Patch (rook.go): a trant loses every fruit and two
// health (trees.go treeRook, include/trants.js onRooked 1140-1151) and grows fruit back while it
// waits; one already at health 1 dies to its dead class, as a street tree does online (objects.go
// rookTree). A Wood Tree is only rooked. Everyone on the street sees it. False when the plot holds no
// living tree.
func (g *patches) rookTree(street, key string) bool {
	if !g.w.shared(street) {
		return false
	}
	p := g.plots[street][key]
	now := g.nowMs()
	if p != nil {
		g.advance(street, p, now)
	}
	if !livingTree(p) {
		return false
	}
	t := p.tree
	if !t.wood() {
		if g.ow.treeRook(&t.treeCore, t.class) {
			log.Printf("patches: the Rook killed the %s in %s %s (health 1)", t.class, street, key)
			g.killTree(street, p, now)
		} else {
			t.rookNext = now + rookFruitFirst.Milliseconds()
		}
	}
	g.broadcast(street, []string{key}, 0)
	g.forget(street)
	g.saveSoon()
	g.arm()
	return true
}

// dustSpot is where the Patch tree a Fertilidust Lite names stands (objects.go handleDust checks
// the reach); ok false when the key names no living tree in a shared Patch.
func (g *patches) dustSpot(street, key string) (x, y float64, ok bool) {
	if !g.w.shared(street) || !patchKeyRe.MatchString(key) {
		return 0, 0, false
	}
	spot := g.spotOf(street, key)
	if spot == nil {
		return 0, 0, false
	}
	g.resume()
	p := g.plots[street][key]
	if p != nil {
		g.advance(street, p, g.nowMs())
	}
	if !livingTree(p) {
		return 0, 0, false
	}
	return spot.x, spot.y, true
}

// dust is the onFertilidust of the street's living Patch trees as the dust lands (objects.go
// dustTrees, 3 s after the jar was used), or with key the one tree a Fertilidust Lite named: a trant
// one maturity below 10, health 10 and its fruit regrown (trees.go treeDust), a Wood Tree one
// maturity below 6 (woodDust). skip: the plots this jar revived (not trees when it was used).
// Everyone hears the trees that changed; their keys.
func (g *patches) dust(street, key string, skip []string) []string {
	if !g.w.shared(street) {
		return nil
	}
	g.resume()
	now := g.nowMs()
	var changed []string
	for k, p := range g.plots[street] {
		if (key != "" && k != key) || slices.Contains(skip, k) {
			continue
		}
		g.advance(street, p, now)
		if !livingTree(p) {
			continue
		}
		t := p.tree
		grew := false
		if t.wood() {
			grew = woodDust(&t.treeCore)
		} else {
			grew = g.ow.treeDust(&t.treeCore, t.class)
			if g.rooked(street, p) {
				// Its capacity moved: the rooked tree's fruit push goes on from here.
				t.rookNext = now + rookFruitEvery.Milliseconds()
			}
		}
		if grew {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	if len(changed) > 0 {
		log.Printf("patches: Fertilidust grew %d Patch trees on %s", len(changed), street)
		g.broadcast(street, changed, 0)
		g.saveSoon()
		g.arm()
	}
	return changed
}

// revive is a full Fertilidust's dead trants in the street's shared Patches, as the jar is used
// (fertilidust.js onUse 266-345: replaceWith the living class, health 4 then plusHealth, maturity 3
// then plusMaturity; the new trant has no fruit, js/patches.js REVIVED_TRANT): a laid-out dead tree,
// a dead tree grown in a Patch, and a dead street tree's tree:<i> plot. Everyone sees them at once;
// their keys.
func (g *patches) revive(street string) []string {
	st := g.w.streets[street]
	if st == nil || !g.w.shared(street) {
		return nil
	}
	g.resume()
	now := g.nowMs()
	today := g.dayOf(now)
	keys := map[string]*patchSpot{}
	for k, spot := range st.spots {
		keys[k] = spot
	}
	for k, p := range g.plots[street] {
		keys[k] = p.spot
	}
	var revived []string
	for k, spot := range keys {
		if g.holding[street+"|"+k] {
			continue
		}
		p := g.plot(street, spot, now)
		if p.stage != "dead" || !deadTrants[p.dead] {
			continue
		}
		class := strings.TrimSuffix(p.dead, "_dead")
		t := &pTree{class: class, grownAt: now, cycleAt: now}
		t.day = today
		t.maturity, t.health = 4, 5
		t.cap, t.fruit = g.ow.capAt(class, t.maturity, t.health), 0
		p.kind = patchKindFor(p.dead)
		p.stage, p.since, p.due, p.seed, p.tree, p.dead = "tree", now, 0, nil, t, ""
		revived = append(revived, k)
	}
	g.forget(street)
	sort.Strings(revived)
	if len(revived) > 0 {
		log.Printf("patches: Fertilidust revived %d dead trees on %s: %v", len(revived), street, revived)
		g.broadcast(street, revived, 0)
		g.save()
		g.arm()
	}
	return revived
}

// trantsformRefusal is a Trantsformation Fluid pour's refusal before the bottle is used, and again
// after (canPour 512-527, getValidTargets 529-534: never a poisoned tree, never an Egg Plant off a
// home street; maturity and health 4 or more; a Wood Tree has no health, so only its maturity counts).
func trantsformRefusal(p *pPlot) (string, string) {
	switch {
	case p.stage != "tree" || p.tree == nil || p.tree.class == "trant_egg":
		return "not_tree", patchNoTrantsform
	case p.tree.poisonEnd != 0:
		return "poisoned", patchNoTrantsform
	case p.tree.maturity < 4:
		return "small", patchTrantSmall
	case !p.tree.wood() && p.tree.health < 4:
		return "weak", patchTrantWeak
	}
	return "", ""
}

// trantsform is a Trantsformation Fluid pour landing on a Patch tree (onPour 536-592): the bottle is
// the ledger's to use first; then the tree becomes a different random one (never an Egg Plant on a
// street) at health 4 and maturity max(4, maturity - 3), for everyone. The new tree's tending
// window, harvests and Rook state start fresh (replaceWithPoofIfOnGround makes a new item).
func (g *patches) trantsform(rp *patchReply, p *pPlot) {
	if code, line := trantsformRefusal(p); code != "" {
		rp.no(code, line)
		return
	}
	if g.rooked(rp.street, p) {
		rp.no("rooked", patchRooked)
		return
	}
	hold := rp.street + "|" + rp.spot.key
	g.holding[hold] = true
	g.uses.askCopy(rp.cl, useTrantsform, 0, copyOf(rp.m.K), func(use map[string]any, code, message string) {
		delete(g.holding, hold)
		if code != "" {
			rp.no(code, message)
			return
		}
		p := rp.live()
		code, line := trantsformRefusal(p)
		if code == "" && g.rooked(rp.street, p) {
			code, line = "rooked", patchRooked
		}
		if code != "" {
			if use != nil {
				g.uses.giveBack(use, rp.cl.ID())
			}
			log.Printf("patches: a trantsform on %s %s by %d: the tree changed first (%s), the bottle given back", rp.street, rp.spot.key, rp.cl.ID(), code)
			rp.no(code, line)
			return
		}
		from := p.tree.class
		pool := make([]string, 0, len(patchTrantsformInto))
		for _, c := range patchTrantsformInto {
			if c != from && c != "trant_egg" {
				pool = append(pool, c)
			}
		}
		to := pool[min(len(pool)-1, int(g.rnd()*float64(len(pool))))]
		now := g.nowMs()
		m := max(4, p.tree.maturity-3)
		t := &pTree{class: to, grownAt: now, cycleAt: now}
		t.day = g.dayOf(now)
		if t.wood() {
			t.maturity = min(6, m)
		} else {
			t.maturity, t.health = min(10, m), 4
			t.cap, t.fruit = g.ow.capAt(to, t.maturity, t.health), 0
		}
		p.tree, p.since = t, now
		log.Printf("patches: %d trantsformed the %s in %s %s into a %s", rp.cl.ID(), from, rp.street, rp.spot.key, to)
		extra := map[string]any{"from": from, "to": to}
		if use != nil {
			extra["use"] = use
		}
		rp.ok(extra, true)
	})
}
