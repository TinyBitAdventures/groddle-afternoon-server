package main

// The shared public gardens' state file (gardens.go): every touched plot and every pick's crop not
// yet acknowledged outlive a restart. A file of its own, not the objects file: an older build drops
// what it does not know from the objects file (objects_persist.go), so a rollback would wipe planted
// plots; an older build never reads this one, and a later build picks it up where it was.
//
// Times are Unix ms. "saved" is when the file was written, for a restart's fresh 60 seconds
// (gardens.go resume). A plot whose street or bed the layout (data/gardens.json) no longer has is
// dropped when the file is read, what grows in it owed to its planter by mail first ("payouts",
// gardens_payout.go, G940); a file that does not parse is moved aside (persist.go).

import (
	"encoding/json"
	"log"
	"time"
)

type gardenRec struct {
	State   string  `json:"s"`
	Seed    string  `json:"seed,omitempty"`
	Wet     bool    `json:"wet,omitempty"`
	WetAt   int64   `json:"wetAt,omitempty"`
	StageAt int64   `json:"stageAt,omitempty"`
	Banked  int64   `json:"banked,omitempty"`
	Mod     float64 `json:"mod,omitempty"`
	Fert    bool    `json:"fert,omitempty"`
	Planter int64   `json:"planter,omitempty"`
	RipeAt  int64   `json:"ripeAt,omitempty"`
	// The clock the crop and the watering run on (gardens.go need, waterOf). A file from before the
	// garden's speed changed has neither, so its crops finish and its waterings last as they were.
	Pace    int `json:"pace,omitempty"`
	WetPace int `json:"wetPace,omitempty"`
}

type gardenFile struct {
	V       int                              `json:"v"`
	Saved   int64                            `json:"saved"`
	Seq     uint64                           `json:"seq,omitempty"`
	Streets map[string]map[string]*gardenRec `json:"streets"`
	Grants  []gardenGrant                    `json:"grants"`
	Payouts []gardenPayout                   `json:"payouts,omitempty"`
}

var gardenStates = map[string]bool{"clean": true, "seeds": true, "shoots": true, "crop": true, "dirty": true}

func (g *gardens) load() error {
	var f gardenFile
	ok, err := readStateFile("gardens", g.path, &f)
	if err != nil || !ok {
		return err
	}
	n, dropped, ownerless := 0, 0, 0
	var owed []gardenOwed
	for street, recs := range f.Streets {
		for id, r := range recs {
			bed, _ := g.w.bedOf(street, id)
			if bed == nil || r == nil || !gardenStates[r.State] {
				dropped++
				// G940: what grows on a bed the layout no longer has goes to its planter by mail.
				if o, ok := g.w.owedOf(street, id, r); bed == nil && ok {
					owed = append(owed, o)
				} else if bed == nil && r != nil && r.State == "crop" {
					ownerless++
				}
				continue
			}
			p := &gPlot{state: r.State, seed: r.Seed, wet: r.Wet, wetAt: r.WetAt, stageAt: r.StageAt, banked: r.Banked, mod: r.Mod, fert: r.Fert, planter: r.Planter, ripeAt: r.RipeAt, pace: r.Pace, wetPace: r.WetPace}
			if p.mod <= 0 || p.mod > 1 {
				p.mod = 1
			}
			if _, known := g.w.seeds[p.seed]; (p.growing() || p.state == "crop") && !known {
				dropped++
				continue
			}
			if g.plots[street] == nil {
				g.plots[street] = map[string]*gPlot{}
			}
			g.plots[street][id] = p
			n++
		}
	}
	for _, gr := range f.Grants {
		if gr.ID != "" && gr.User > 0 && gr.C != "" && gr.N > 0 {
			g.grants = append(g.grants, gr)
		}
	}
	g.addPayouts(f.Payouts)
	if paid := gardenPayouts(owed); len(paid) > 0 || ownerless > 0 {
		g.addPayouts(paid)
		log.Printf("gardens %s: plots on beds the layout no longer has: %s", g.path, payoutSummary(paid, ownerless))
	}
	g.seq, g.savedAt, g.resumed = f.Seq, f.Saved, false
	log.Printf("gardens %s: %d plots on %d streets, %d picks not yet saved, %d dropped (not in the layout), %d payouts waiting", g.path, n, len(g.plots), len(g.grants), dropped, len(g.payouts))
	return nil
}

func (g *gardens) save() {
	if g.path == "" {
		return
	}
	g.trimGrants()
	f := gardenFile{V: 1, Saved: g.nowMs(), Seq: g.seq, Streets: map[string]map[string]*gardenRec{}, Grants: g.grants, Payouts: g.payouts}
	if f.Grants == nil {
		f.Grants = []gardenGrant{}
	}
	for street, room := range g.plots {
		for id, p := range room {
			if p.fresh() {
				continue
			}
			if f.Streets[street] == nil {
				f.Streets[street] = map[string]*gardenRec{}
			}
			f.Streets[street][id] = &gardenRec{State: p.state, Seed: p.seed, Wet: p.wet, WetAt: p.wetAt, StageAt: p.stageAt, Banked: p.banked, Mod: p.mod, Fert: p.fert, Planter: p.planter, RipeAt: p.ripeAt, Pace: p.pace, WetPace: p.wetPace}
		}
	}
	b, _ := json.Marshal(f)
	if err := writeStateSoon(g.path, b); err != nil {
		log.Printf("gardens %s: %v", g.path, err)
	}
}

// saveSoon writes the file within a second (a water, a hoe, a stage).
func (g *gardens) saveSoon() {
	if g.path == "" || g.saveDue {
		return
	}
	g.saveDue = true
	g.h.After(time.Second, func() {
		g.saveDue = false
		g.save()
	})
}
