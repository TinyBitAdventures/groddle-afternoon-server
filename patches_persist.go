package main

// The shared street Patches' state file (patches.go): every touched plot, every tree:<i> plot a dead
// street tree left, and every grant not yet acknowledged outlive a restart. A file of its own, not the
// objects file: an older build drops what it does not know from the objects file (objects_persist.go),
// so a rollback would wipe planted trees; an older build never reads this one, and a later build
// picks it up where it was.
//
// Times are Unix ms; growth, poison and timers run on real time, so a tree keeps rolling maturity while
// the server is down (up to the page's 400-cycle catch-up) and a poison clock that ran out kills the
// tree at the first connect. "saved" is when the file was written, for a Seedling's fresh hour
// (patches.go resume). The tend lock and the diggers are not kept: they lapse with the sockets.
//
// A plot whose street or key the layout (data/patches.json) no longer has is not dropped: it is
// parked, logged, and written back as it was, so a grown tree is never lost to a placement change and
// a later build can move it by key. A tree:<i> plot carries its own spot and needs no layout row. A
// file that does not parse is moved aside (persist.go).

import (
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"time"
)

type patchSeedRec struct {
	Tree      string  `json:"tree"`
	Bean      string  `json:"bean,omitempty"`
	PlantedAt int64   `json:"plantedAt"`
	MinAt     int64   `json:"minAt"`
	MaxAt     int64   `json:"maxAt"`
	Petted    int     `json:"petted,omitempty"`
	Petters   []int64 `json:"petters,omitempty"`
	Planter   int64   `json:"planter,omitempty"`
	Grow      int     `json:"grow,omitempty"`
	ThirdAt   int64   `json:"thirdAt,omitempty"`
}

type patchTreeRec struct {
	Class       string `json:"class"`
	Maturity    int    `json:"maturity"`
	Health      int    `json:"health,omitempty"`
	Fruit       int    `json:"fruit,omitempty"`
	Cap         int    `json:"cap,omitempty"`
	Petted      bool   `json:"petted,omitempty"`
	Watered     bool   `json:"watered,omitempty"`
	Day         int64  `json:"day"`
	GrownAt     int64  `json:"grownAt"`
	CycleAt     int64  `json:"cycleAt,omitempty"`
	Count       int    `json:"count,omitempty"`
	WoodPetted  int    `json:"woodPetted,omitempty"`
	WoodWatered int    `json:"woodWatered,omitempty"`
	// A Wood Tree's care beyond its counts (trees.go woodCare): when its marks clear, each player's clocks.
	WoodReset   int64           `json:"woodReset,omitempty"`
	WoodPetAt   map[int64]int64 `json:"woodPetAt,omitempty"`
	WoodWaterAt map[int64]int64 `json:"woodWaterAt,omitempty"`
	ClearedAt   int64           `json:"clearedAt,omitempty"`
	WoodBy      []int64         `json:"woodBy,omitempty"`
	Harvests    map[string]int  `json:"harvests,omitempty"`
	HDay        int64           `json:"hday,omitempty"`
	PoisonEnd   int64           `json:"poisonEnd,omitempty"`
	PoisonBy    int64           `json:"poisonBy,omitempty"`
	RookNext    int64           `json:"rookNext,omitempty"`
}

type patchRec struct {
	Class   string        `json:"class"`
	Adopted bool          `json:"adopted,omitempty"`
	X       float64       `json:"x,omitempty"`
	Y       float64       `json:"y,omitempty"`
	Kind    string        `json:"kind"`
	Stage   string        `json:"stage"`
	Since   int64         `json:"since,omitempty"`
	Due     int64         `json:"due,omitempty"`
	Seed    *patchSeedRec `json:"seed,omitempty"`
	Tree    *patchTreeRec `json:"tree,omitempty"`
	Dead    string        `json:"dead,omitempty"`
}

type patchFile struct {
	V       int                             `json:"v"`
	Saved   int64                           `json:"saved"`
	Seq     uint64                          `json:"seq,omitempty"`
	Streets map[string]map[string]*patchRec `json:"streets"`
	Parked  map[string]map[string]*patchRec `json:"parked,omitempty"`
	Grants  []patchGrant                    `json:"grants"`
	// Seeded: the payout's seed ids applied or refunded, and whose (patches_seed.go).
	Seeded map[string]int64 `json:"seeded,omitempty"`
}

var patchTrees = map[string]bool{"trant_bean": true, "trant_bubble": true, "trant_egg": true, "trant_fruit": true, "trant_gas": true, "trant_spice": true, "wood_tree": true}

func (g *patches) record(p *pPlot) *patchRec {
	r := &patchRec{Class: p.spot.class, Adopted: p.adopted, Kind: p.kind, Stage: p.stage, Since: p.since, Due: p.due, Dead: p.dead}
	if p.adopted {
		r.X, r.Y = p.spot.x, p.spot.y
	}
	if s := p.seed; s != nil {
		r.Seed = &patchSeedRec{Tree: s.tree, Bean: s.bean, PlantedAt: s.plantedAt, MinAt: s.minAt, MaxAt: s.maxAt, Petted: s.petted,
			Petters: append([]int64(nil), s.petters...), Planter: s.planter, Grow: s.grow, ThirdAt: s.thirdAt}
	}
	if t := p.tree; t != nil {
		tr := &patchTreeRec{Class: t.class, Maturity: t.maturity, Health: t.health, Fruit: t.fruit, Cap: t.cap, Petted: t.petted, Watered: t.watered,
			Day: t.day, GrownAt: t.grownAt, CycleAt: t.cycleAt, Count: t.count, WoodPetted: t.woodCare.pets, WoodWatered: t.woodCare.waters,
			WoodReset: t.woodCare.resetAt, WoodPetAt: copyClocks(t.woodCare.petAt), WoodWaterAt: copyClocks(t.woodCare.waterAt),
			ClearedAt: t.clearedAt, HDay: t.hday, PoisonEnd: t.poisonEnd, PoisonBy: t.poisonBy, RookNext: t.rookNext}
		for id := range t.woodBy {
			tr.WoodBy = append(tr.WoodBy, id)
		}
		sort.Slice(tr.WoodBy, func(i, j int) bool { return tr.WoodBy[i] < tr.WoodBy[j] })
		if len(t.harvests) > 0 {
			tr.Harvests = map[string]int{}
			for id, n := range t.harvests {
				tr.Harvests[itoa64(id)] = n
			}
		}
		r.Tree = tr
	}
	return r
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// fromRecord is a plot read back, or nil when the record does not hold together.
func (g *patches) fromRecord(spot *patchSpot, r *patchRec) *pPlot {
	if r == nil || !patchStages[r.Stage] || (r.Kind != "patch" && r.Kind != "patch_dark") {
		return nil
	}
	p := &pPlot{spot: spot, adopted: r.Adopted, kind: r.Kind, stage: r.Stage, since: r.Since, due: r.Due, dead: r.Dead}
	switch r.Stage {
	case "seedling":
		s := r.Seed
		if s == nil || !patchTrees[s.Tree] {
			return nil
		}
		p.seed = &pSeedling{tree: s.Tree, bean: s.Bean, plantedAt: s.PlantedAt, minAt: s.MinAt, maxAt: s.MaxAt, petted: s.Petted,
			petters: append([]int64(nil), s.Petters...), planter: s.Planter, grow: s.Grow, thirdAt: s.ThirdAt}
	case "tree":
		tr := r.Tree
		if tr == nil || !patchTrees[tr.Class] {
			return nil
		}
		t := &pTree{class: tr.Class, grownAt: tr.GrownAt, cycleAt: tr.CycleAt, count: tr.Count,
			woodCare:  woodCare{pets: max(0, tr.WoodPetted), waters: max(0, tr.WoodWatered), resetAt: max(0, tr.WoodReset), petAt: copyClocks(tr.WoodPetAt), waterAt: copyClocks(tr.WoodWaterAt)},
			clearedAt: tr.ClearedAt, hday: tr.HDay, poisonEnd: tr.PoisonEnd, poisonBy: tr.PoisonBy, rookNext: tr.RookNext}
		t.maturity, t.health, t.fruit, t.cap, t.petted, t.watered, t.day = tr.Maturity, tr.Health, tr.Fruit, tr.Cap, tr.Petted, tr.Watered, tr.Day
		if t.wood() && t.petted && t.watered && t.woodCare.resetAt == 0 {
			// A file from the daily model: those marks were the day's, long since reset.
			t.petted, t.watered = false, false
		}
		if !t.wood() && (t.maturity < 1 || t.maturity > 10 || t.health < 1 || t.health > 10) {
			return nil
		}
		for _, id := range tr.WoodBy {
			if t.woodBy == nil {
				t.woodBy = map[int64]bool{}
			}
			t.woodBy[id] = true
		}
		for k, n := range tr.Harvests {
			if id, err := strconv.ParseInt(k, 10, 64); err == nil && id > 0 {
				if t.harvests == nil {
					t.harvests = map[int64]int{}
				}
				t.harvests[id] = n
			}
		}
		p.tree = t
	case "dead":
		if !deadTrants[r.Dead] {
			return nil
		}
	}
	return p
}

func (g *patches) load() error {
	var f patchFile
	ok, err := readStateFile("patches", g.path, &f)
	if err != nil || !ok {
		return err
	}
	n, parked, bad := 0, 0, 0
	park := func(street, key string, r *patchRec) {
		if g.parked[street] == nil {
			g.parked[street] = map[string]*patchRec{}
		}
		g.parked[street][key] = r
		parked++
		log.Printf("patches %s: %s %s is not in the layout; parked (kept as it was)", g.path, street, key)
	}
	for street, recs := range f.Streets {
		for key, r := range recs {
			if r == nil {
				continue
			}
			var spot *patchSpot
			if r.Adopted {
				if !patchKeyRe.MatchString(key) || (r.Class != "patch" && !deadTrants[r.Class]) {
					bad++
					continue
				}
				spot = &patchSpot{key: key, class: r.Class, x: r.X, y: r.Y}
			} else if st := g.w.streets[street]; st != nil && st.spots[key] != nil && st.spots[key].class == r.Class {
				spot = st.spots[key]
			} else {
				park(street, key, r)
				continue
			}
			p := g.fromRecord(spot, r)
			if p == nil {
				bad++
				log.Printf("patches %s: %s %s did not hold together; dropped", g.path, street, key)
				continue
			}
			if g.plots[street] == nil {
				g.plots[street] = map[string]*pPlot{}
			}
			g.plots[street][key] = p
			n++
		}
	}
	for street, recs := range f.Parked {
		for key, r := range recs {
			if r != nil {
				park(street, key, r)
			}
		}
	}
	for _, gr := range f.Grants {
		if gr.ID != "" && gr.User > 0 && gr.C != "" && gr.N > 0 {
			g.grants = append(g.grants, gr)
		}
	}
	for id, user := range f.Seeded {
		if id != "" && user > 0 {
			g.seeded[id] = user
		}
	}
	g.seq, g.savedAt, g.resumed = f.Seq, f.Saved, false
	log.Printf("patches %s: %d plots on %d streets, %d parked, %d dropped, %d grants not yet saved", g.path, n, len(g.plots), parked, bad, len(g.grants))
	return nil
}

func (g *patches) save() {
	if g.path == "" {
		return
	}
	g.trimGrants()
	f := patchFile{V: 1, Saved: g.nowMs(), Seq: g.seq, Streets: map[string]map[string]*patchRec{}, Grants: g.grants}
	if f.Grants == nil {
		f.Grants = []patchGrant{}
	}
	for street, room := range g.plots {
		for key, p := range room {
			if p.fresh() {
				continue
			}
			if f.Streets[street] == nil {
				f.Streets[street] = map[string]*patchRec{}
			}
			f.Streets[street][key] = g.record(p)
		}
	}
	if len(g.parked) > 0 {
		f.Parked = g.parked
	}
	if len(g.seeded) > 0 {
		f.Seeded = g.seeded
	}
	b, _ := json.Marshal(f)
	if err := writeFileSynced(g.path, b); err != nil {
		log.Printf("patches %s: %v", g.path, err)
	}
}

// saveSoon writes the file within a second (a tend, a dig, a pet, a maturity step).
func (g *patches) saveSoon() {
	if g.path == "" || g.saveDue {
		return
	}
	g.saveDue = true
	g.h.After(time.Second, func() {
		g.saveDue = false
		g.save()
	})
}
