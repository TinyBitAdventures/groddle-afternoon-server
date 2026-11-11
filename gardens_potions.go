package main

// Master Gardener potions on a shared public bed (register G368 slice 2): a pour works the whole bed
// at once (items/include/master_garden.js potionTendGarden; the game's js/master-garden.js):
//
//	Soak-All Solution     (potion_garden_water)     waters every plot (a wet one's watering starts again)
//	Liquid Super-Hoe      (potion_garden_clear)     hoes every dirty plot
//	Seed-Dibber Libation  (potion_garden_plant)     plants every clean plot with the chosen seed while it lasts
//	Manyharvest Cordial   (potion_garden_harvest)   picks every ripe plot, the planter's 60 seconds or not
//	                                                (only master_garden.js 423 touches the planter, and 477
//	                                                never checks it: Austin 2026-11-07, as the original)
//	Dung-Kicker Drops     (potion_garden_fertilize) fertilizes every seeded plot not yet fertilized, once one is wet
//
//	{"t":"garden_potion","req":3,"bed":"default","c":"potion_garden_plant","k":0,"seed":"seed_spinach","n":8}
//	   -> garden_ok {"verb":"potion","bed","c","count","before","plots","use":{…},"seeds":{…},"grants":[{id,c,n}]}
//	   or garden_no {"verb":"potion","code","message"}
//
// "n" is how many plots the page's energy covers (potionTendGarden pays the package's energy plot by
// plot and stops when the player is too tired; the energy stays the page's): the server works the
// first n plots that need it, in the bed's own order (createGarden: row by row), so the page's run of
// the same rules on the same plots lands where the server's did. The site's ledger takes one charge of
// the bottle poured first (LedgerUse.php, per copy: "k"), then for Seed-Dibber the seeds (whole items,
// as many as there are clean plots, at most n); when the seeds are refused the charge is given back.
// While the ledger answers the bed is held: no other act on it lands meanwhile. A Manyharvest's crops
// are the base produces_count of each plot (Master Gardener never halves, master_garden.js 497-503),
// credited as take receipts and granted until acknowledged, as a pick by hand is; its super harvest and
// the Master Gardener drop table are the page's, through the budget.
//
// The others see the Soak-All's watering can and the Super-Hoe's hoe over the pourer's head for 2 s
// (master_garden.js 171-173, 307-309, 868-881); the other three potions show nothing (their overlays
// are commented out in the source, 346, 473, 678, 767).

import (
	"encoding/json"
	"log"
	"math"
	"strconv"
)

type gardenPotion struct {
	action   string // water | hoe | plant | pick | fertilize
	noTarget string
}

var potionActions = map[string]bool{"water": true, "hoe": true, "plant": true, "pick": true, "fertilize": true}

const (
	gardenPotionMs    = 2000                                   // master_garden.js 173, 309
	gardenNoCropSeeds = "You need crop seeds for this garden." // js/master-garden.js canPour
	gardenNoHerbSeeds = "You need herb seeds for this garden."
	gardenOverlayMin  = 500
	gardenOverlayMax  = 6000
)

// overlayTool is the tool the others see over the tender's head, or "" (include/garden.js 179, 282,
// 651: the can or hoe used, Guano by hand; planting and picking by hand show none).
func overlayTool(m gardenAct) string {
	switch m.Verb {
	case "water", "water_all":
		if m.Tool == "irrigator_9000" {
			return m.Tool
		}
		return "watering_can"
	case "clear":
		if m.Tool == "high_class_hoe" {
			return m.Tool
		}
		return "hoe"
	case "fertilize":
		return gardenGuano
	case "potion":
		return potionTool(m.Seed)
	}
	return ""
}

// potionTool: the Soak-All's watering can and the Super-Hoe's hoe (m.Seed carries the action for a pour).
func potionTool(action string) string {
	switch action {
	case "water":
		return "watering_can"
	case "hoe":
		return "hoe"
	}
	return ""
}

func overlayMs(ms int64) int64 {
	if ms <= 0 {
		return gardenPotionMs
	}
	return min(max(ms, gardenOverlayMin), gardenOverlayMax)
}

func bedKey(street, bed string) string { return street + "|bed:" + bed }

// bedHeld: a pour on this act's bed waits for the ledger.
func (g *gardens) bedHeld(street string, m gardenAct) bool {
	bed := m.Bed
	if at := gardenPlotRe.FindStringSubmatch(m.ID); at != nil {
		bed = at[1]
	}
	return bed != "" && g.holding[bedKey(street, bed)]
}

// bedOrder is a bed's plot ids in its own order (createGarden: row by row, column by column).
func bedOrder(b *gardenBed) []string {
	out := make([]string, 0, b.cols*b.rows)
	for r := 1; r <= b.rows; r++ {
		for c := 1; c <= b.cols; c++ {
			out = append(out, b.key+":"+strconv.Itoa(c)+"-"+strconv.Itoa(r))
		}
	}
	return out
}

// potionTargets are the plots a pour works, in the bed's order (potionTendGarden's tests per action).
func (g *gardens) potionTargets(street string, b *gardenBed, action string, now int64) []string {
	var out []string
	for _, id := range bedOrder(b) {
		p := g.plot(street, id, now)
		_, known := g.w.seeds[p.seed]
		ok := false
		switch action {
		case "water":
			ok = true
		case "hoe":
			ok = p.state == "dirty"
		case "plant":
			ok = p.state == "clean"
		case "pick":
			ok = p.state == "crop" && known
		case "fertilize":
			// master_garden.js 692-694 (js/master-garden.js): every seeded plot not yet fertilized, wet or not.
			ok = known && !p.fert && p.growing()
		}
		if ok {
			out = append(out, id)
		}
	}
	return out
}

type gardenPour struct {
	Req  int64   `json:"req"`
	Bed  string  `json:"bed"`
	C    string  `json:"c"`
	K    *int    `json:"k"`
	Seed string  `json:"seed"`
	N    int     `json:"n"`
	Mod  float64 `json:"mod"` // Seed-Dibber: the pourer's grow modifier (Growth Spurt, Speedy Herbs), as a plant by hand
}

func (g *gardens) handlePotion(cl *Client, frame json.RawMessage) {
	var m gardenPour
	if json.Unmarshal(frame, &m) != nil || m.Req < 1 || m.Req > 1<<31 || len(m.C) > 64 || len(m.Seed) > 64 {
		g.h.ErrorLocked(cl, "bad_garden", "That garden message did not check out.")
		return
	}
	potion, known := g.w.potions[m.C]
	rp := &gardenReply{g: g, cl: cl, m: gardenAct{Req: m.Req, Bed: m.Bed, Verb: "potion", Seed: potion.action}}
	if !g.allow(cl) {
		rp.no("slow_down", "Slow down a little.")
		return
	}
	street := cl.Street()
	bed := g.w.streets[street][m.Bed]
	if street == "" || bed == nil || !known || m.N < 1 || m.N > bed.cols*bed.rows {
		rp.no("unknown", "That is not something here.")
		return
	}
	g.resume()
	rp.street = street
	rp.ids = bedOrder(bed)
	if x, y := cl.Pos(); math.Abs(x-bed.x) > objReachX || math.Abs(y-bed.y) > objReachY {
		rp.no("far", "You need to be closer.")
		return
	}
	now := g.nowMs()
	targets := g.potionTargets(street, bed, potion.action, now)
	if potion.action == "water" || potion.action == "fertilize" {
		// canPour (js/master-garden.js plotsFor, getValidTargets): a Soak-All needs a dry plot and a
		// Dung-Kicker a wet seeded one; then each works every plot its act takes.
		want := false
		for _, id := range targets {
			p := g.plots[street][id]
			want = want || (potion.action == "water" && !p.wet) || (potion.action == "fertilize" && p.wet)
		}
		if !want {
			targets = nil
		}
	}
	if len(targets) == 0 {
		rp.no("nothing", potion.noTarget)
		return
	}
	seeds := 0
	if potion.action == "plant" {
		seed, ok := g.w.seeds[m.Seed]
		if !ok || seed.bed != bed.bed {
			line := gardenNoCropSeeds
			if bed.bed == "herb" {
				line = gardenNoHerbSeeds
			}
			rp.no("wrong_seed", line)
			return
		}
		seeds = min(len(targets), m.N)
	}
	key := bedKey(street, m.Bed)
	for _, id := range rp.ids {
		if g.holding[street+"|"+id] {
			rp.no("busy", gardenBusy)
			return
		}
	}
	if g.holding[key] {
		rp.no("busy", gardenBusy)
		return
	}
	g.holding[key] = true
	g.uses.askCopy(cl, m.C, 0, copyOf(m.K), func(use map[string]any, code, message string) {
		if code != "" {
			delete(g.holding, key)
			rp.no(code, message)
			return
		}
		if seeds == 0 {
			delete(g.holding, key)
			g.pour(rp, bed, potion, m, use, nil, 0)
			return
		}
		g.uses.ask(cl, m.Seed, seeds, func(seedUse map[string]any, code, message string) {
			delete(g.holding, key)
			if code != "" {
				// The seeds were refused: the charge goes back, nothing is planted.
				if use != nil {
					g.uses.giveBack(use, cl.ID())
				}
				if code == "no_item" {
					message = gardenNoCropSeeds
					if bed.bed == "herb" {
						message = gardenNoHerbSeeds
					}
				}
				rp.no(code, message)
				return
			}
			g.pour(rp, bed, potion, m, use, seedUse, seeds)
		})
	})
}

// pour works the bed once the ledger took the charge (and the seeds): the first n plots that need it.
func (g *gardens) pour(rp *gardenReply, bed *gardenBed, potion gardenPotion, m gardenPour, use, seedUse map[string]any, seeds int) {
	now := g.nowMs()
	street := rp.street
	targets := g.potionTargets(street, bed, potion.action, now)
	rp.before = g.views(street, rp.ids, now)
	limit := m.N
	if potion.action == "plant" {
		limit = min(limit, seeds)
	}
	var changed []string
	crops := map[string]int{}
	for _, id := range targets {
		if len(changed) >= limit {
			break
		}
		p := g.plots[street][id]
		switch potion.action {
		case "water":
			// js/garden.js wetPlot: a dry plot's clock restarts; every plot's watering starts again.
			g.wetNow(p, now)
		case "hoe":
			p.state, p.seed, p.fert, p.stageAt, p.banked = "clean", "", false, now, 0
		case "plant":
			// The pourer is the planter: their 60 seconds hold for a hand pick (a Manyharvest ignores them).
			mod := m.Mod
			if !(mod >= 0.5 && mod <= 1) {
				mod = 1
			}
			g.sow(p, m.Seed, mod, rp.cl.ID(), now)
		case "pick":
			crops[g.w.seeds[p.seed].crop] += g.w.seeds[p.seed].produces
			p.state, p.seed, p.fert, p.planter, p.ripeAt, p.stageAt, p.banked, p.pace = "dirty", "", false, 0, 0, now, 0, 0
		case "fertilize":
			g.applyGuano(p, now)
		}
		changed = append(changed, id)
	}
	if len(changed) == 0 {
		// Only the Rook or a restart could change a held bed: nothing was done, so what was used goes back.
		log.Printf("gardens: a %s on %s %s by %d found nothing to do once paid for: given back", m.C, street, m.Bed, rp.cl.ID())
		if use != nil {
			g.uses.giveBack(use, rp.cl.ID())
		}
		if seedUse != nil {
			g.uses.giveBack(seedUse, rp.cl.ID())
		}
		rp.no("nothing", potion.noTarget)
		return
	}
	if potion.action == "plant" && len(changed) < seeds {
		log.Printf("gardens: a Seed-Dibber on %s %s by %d planted %d of the %d seeds the ledger took", street, m.Bed, rp.cl.ID(), len(changed), seeds)
	}
	extra := map[string]any{"c": m.C, "count": len(changed)}
	if use != nil {
		extra["use"] = use
	}
	if seedUse != nil {
		extra["seeds"] = seedUse
	}
	if len(crops) > 0 {
		var grants []map[string]any
		for _, crop := range sortedKeys(crops) {
			gr := g.grant(rp.cl.ID(), crop, crops[crop])
			creditLocked(g.h, rp.cl.ID(), "take", creditItem(crop, crops[crop]))
			grants = append(grants, map[string]any{"id": gr.ID, "c": gr.C, "n": gr.N})
		}
		extra["grants"] = grants
	}
	log.Printf("gardens: %d poured %s on %s %s: %d plots", rp.cl.ID(), m.C, street, m.Bed, len(changed))
	rp.ok(changed, extra, potion.action == "plant" || potion.action == "pick" || potion.action == "fertilize")
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
