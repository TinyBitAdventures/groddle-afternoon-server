package main

// The one-time hand-over of players' own street Patch trees (register G564 slice 3, the plan's section
// 10; Austin 2026-11-08: "decide by the count", and the live count picked option B, one winner per
// Patch). Before Patches were shared each player kept a private copy of every street Patch in their
// save. The site's payout (site src/PatchPayout.php, POST groddle/v1/admin/patches/payout) reads every
// save: where one player planted a Patch their Seedling or tree wins it, and where several did the most
// grown wins (the others get their bean and the fruit on it back by letter, option A). The winners
// must reach this server before players see the shared Patch, so the payout's answer carries them and
// the game's runner writes them as a seed file:
//
//	node tools/patches/payout.mjs --site=… --seed-out=realtime-patches-seed.json
//
// which goes beside the state file on the realtime host (the unit names it: -patches-seed). At the
// first connect after a start (resume), each entry not applied before lands in its plot:
//
//	{"v":1,"plots":[{"id":"GA9…|patch:0","street":"GA9…","key":"patch:0","user":500,"stage":"tree",
//	   "tree":"trant_fruit","bean":"bean_fruit","maturity":2,"health":7,"fruit":0,"grownAt":ms,"cycleAt":ms},
//	  {"id":…,"stage":"seedling","tree":"wood_tree","bean":"bean_wood","plantedAt":ms,"minAt":ms,
//	   "maxAt":ms,"petted":3,"grow":0,"thirdAt":ms}, …]}
//
//   - A Seedling keeps its times and pettings with its player as planter (the pettings count as theirs);
//     one that is ready grows at once by the usual rule (patch_seedling.js growUp), one whose 4 hours
//     ran out gets a fresh hour (Ours, as a restart's).
//   - A tree keeps its class, maturity, health and fruit (clamped to its capacity) and its last
//     maturity roll, so it goes on growing on real time as the page would have grown it (the 400-cycle
//     catch-up); today's tending is open; nobody owns a grown street tree (the source's rule).
//   - A plot that is not a fresh Patch any more (someone planted there first), a Patch the layout no
//     longer has or a street not shared, a tree that does not fit the soil (an Egg Plant only in a Dark
//     Patch) or a Wood Tree cut to 0: the entry is refunded to its planter as grants (the bean, and the
//     fruit or the Wood Tree's Planks on it: option A), credited to the ledger as receipts and resent
//     until acknowledged, as every Patch grant.
//   - Every entry is remembered by id in the state file ("seeded"), applied or refunded, so a restart or
//     the same file again never plants or pays twice. A server whose state file is off (lane and QA
//     servers) applies the file again at each start, onto fresh plots only.
//
// -patches-seed (default realtime-patches-seed.json beside -secret-file; off: none). A missing file is
// nothing to do; a file that does not parse is moved aside (persist.go).

import (
	"flag"
	"log"
	"strings"
)

var (
	patchesSeedFlag    = flag.String("patches-seed", "", "the payout's winners to plant once (patches_seed.go; default realtime-patches-seed.json beside -secret-file; off: none)")
	patchesSeedForTest string
)

// patchSeed is one winner: their Seedling or tree, as their save held it (the site copies the fields).
type patchSeed struct {
	ID     string `json:"id"`
	Street string `json:"street"`
	Key    string `json:"key"`
	User   int64  `json:"user"`
	Stage  string `json:"stage"`
	Tree   string `json:"tree"`
	Bean   string `json:"bean,omitempty"`
	// A Seedling.
	PlantedAt int64 `json:"plantedAt,omitempty"`
	MinAt     int64 `json:"minAt,omitempty"`
	MaxAt     int64 `json:"maxAt,omitempty"`
	Petted    int   `json:"petted,omitempty"`
	Grow      int   `json:"grow,omitempty"`
	ThirdAt   int64 `json:"thirdAt,omitempty"`
	// A tree.
	Maturity    int   `json:"maturity,omitempty"`
	Health      int   `json:"health,omitempty"`
	Fruit       int   `json:"fruit,omitempty"`
	GrownAt     int64 `json:"grownAt,omitempty"`
	CycleAt     int64 `json:"cycleAt,omitempty"`
	Count       int   `json:"count,omitempty"`
	WoodPetted  int   `json:"woodPetted,omitempty"`
	WoodWatered int   `json:"woodWatered,omitempty"`
}

type patchSeedFile struct {
	V     int         `json:"v"`
	Plots []patchSeed `json:"plots"`
}

// loadSeeds reads the seed file (newPatches, after the state file); the entries wait for resume.
func (g *patches) loadSeeds() {
	path := statePath(patchesSeedForTest, patchesSeedFlag, "realtime-patches-seed.json")
	if path == "" {
		return
	}
	var f patchSeedFile
	ok, err := readStateFile("patches seed", path, &f)
	if err != nil {
		log.Printf("patches seed %s: %v (nothing planted)", path, err)
		return
	}
	if !ok {
		return
	}
	n := 0
	for _, s := range f.Plots {
		if s.ID != "" && g.seeded[s.ID] == 0 {
			g.seeds = append(g.seeds, s)
			n++
		}
	}
	if n > 0 {
		g.resumed = false
	}
	log.Printf("patches seed %s: %d entries, %d to plant at the first connect", path, len(f.Plots), n)
}

// applySeeds plants the waiting winners (resume, under the hub lock, before the plots are advanced).
func (g *patches) applySeeds(now int64) {
	if len(g.seeds) == 0 {
		return
	}
	planted, refunded := 0, 0
	for _, s := range g.seeds {
		if g.seeded[s.ID] != 0 {
			continue
		}
		if s.User <= 0 || !patchTrees[s.Tree] || (s.Stage != "seedling" && s.Stage != "tree") {
			log.Printf("patches seed: %s is not a Seedling or tree of a player; skipped", s.ID)
			continue
		}
		g.seeded[s.ID] = s.User
		if why := g.plantSeed(s, now); why != "" {
			g.refundSeed(s, why)
			refunded++
			continue
		}
		planted++
		log.Printf("patches seed: %s %s is %d's %s %s, shared now", s.Street, s.Key, s.User, s.Tree, s.Stage)
	}
	g.seeds = nil
	log.Printf("patches seed: %d planted, %d refunded", planted, refunded)
	g.save()
}

// plantSeed puts one winner in its plot, or says why it can't.
func (g *patches) plantSeed(s patchSeed, now int64) string {
	st := g.w.streets[s.Street]
	var spot *patchSpot
	if st != nil {
		spot = st.spots[s.Key]
	}
	switch {
	case spot == nil:
		return "not in the layout"
	case spot.class != "patch" && spot.class != "patch_dark":
		return "not a Patch"
	case !g.w.shared(s.Street):
		return "not shared"
	case (s.Tree == "trant_egg") != (spot.class == "patch_dark"):
		return "does not fit the soil"
	case s.Stage == "tree" && s.Tree == "wood_tree" && s.Maturity < 1:
		return "a Wood Tree cut to 0"
	}
	if p := g.plots[s.Street][s.Key]; p != nil && !p.fresh() {
		return "taken"
	}
	clamp := func(t, lo, hi int64) int64 { return min(max(t, lo), hi) }
	p := freshPatchPlot(spot)
	if s.Stage == "seedling" {
		planted := clamp(s.PlantedAt, 1, now)
		if s.PlantedAt <= 0 {
			planted = now
		}
		grow := 0
		if _, ok := g.w.rules.Grow[itoa64(int64(s.Grow))]; ok {
			grow = s.Grow
		}
		minAt := s.MinAt
		if minAt < planted {
			minAt = planted + g.timers.min
			if ms, ok := g.w.rules.Grow[itoa64(int64(grow))]; ok {
				minAt = planted + ms
			}
		}
		maxAt := s.MaxAt
		if maxAt <= minAt {
			maxAt = planted + g.timers.max
		}
		seed := &pSeedling{tree: s.Tree, bean: s.Bean, plantedAt: planted, minAt: minAt, maxAt: maxAt, planter: s.User, grow: grow,
			petted: int(clamp(int64(s.Petted), 0, int64(g.w.rules.Timers.Pets)))}
		if seed.petted > 0 {
			seed.petters = []int64{s.User}
		}
		if seed.petted >= g.w.rules.Timers.Pets {
			seed.thirdAt = clamp(s.ThirdAt, planted, now)
		}
		if _, ok := g.seedReady(seed); !ok && seed.maxAt <= now {
			seed.maxAt = now + patchFreshHour // Ours: as a Seedling that ran out while the server was down
		}
		p.stage, p.since, p.seed = "seedling", planted, seed
	} else {
		grown := clamp(s.GrownAt, 1, now)
		if s.GrownAt <= 0 {
			grown = now
		}
		t := &pTree{class: s.Tree, grownAt: grown, cycleAt: clamp(s.CycleAt, grown, now)}
		t.day = g.today()
		if t.wood() {
			t.maturity = int(clamp(int64(s.Maturity), 1, 6))
			t.count, t.woodPetted, t.woodWatered = max(s.Count, 0), max(s.WoodPetted, 0), max(s.WoodWatered, 0)
		} else {
			t.maturity, t.health = int(clamp(int64(s.Maturity), 1, 10)), int(clamp(int64(s.Health), 1, 10))
			t.cap = g.ow.capAt(t.class, t.maturity, t.health)
			t.fruit = int(clamp(int64(s.Fruit), 0, int64(t.cap)))
		}
		p.stage, p.since, p.tree = "tree", grown, t
	}
	if g.plots[s.Street] == nil {
		g.plots[s.Street] = map[string]*pPlot{}
	}
	g.plots[s.Street][s.Key] = p
	return ""
}

// refundSeed is option A for a winner that can't land: the bean, and the fruit or Planks on the tree,
// as grants (resent until acknowledged) and ledger receipts.
func (g *patches) refundSeed(s patchSeed, why string) {
	bean := s.Bean
	if !strings.HasPrefix(bean, "bean_") || g.w.rules.Beans[bean] != s.Tree {
		bean = ""
		for b, tree := range g.w.rules.Beans {
			if tree == s.Tree && (bean == "" || b < bean) {
				bean = b
			}
		}
	}
	pay := []tradeItem{}
	if bean != "" {
		pay = append(pay, tradeItem{C: bean, N: 1})
	}
	if s.Stage == "tree" {
		if s.Tree == "wood_tree" {
			if s.Maturity >= 1 {
				pay = append(pay, tradeItem{C: "plank", N: woodPlanks[min(s.Maturity, 6)-1]})
			}
		} else if c := objFruit[s.Tree]; c != "" && s.Fruit > 0 {
			pay = append(pay, tradeItem{C: c, N: min(s.Fruit, 999)})
		}
	}
	if len(pay) == 0 {
		log.Printf("patches seed: %s %s (%d's %s) %s: nothing to refund", s.Street, s.Key, s.User, s.Tree, why)
		return
	}
	for _, it := range pay {
		g.grant(s.User, it.C, it.N)
	}
	creditLocked(g.h, s.User, "take", tradeGoods{Items: pay})
	log.Printf("patches seed: %s %s (%d's %s) %s: refunded %v", s.Street, s.Key, s.User, s.Tree, why, pay)
}
