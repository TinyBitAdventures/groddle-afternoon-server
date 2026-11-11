package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Community Gardens, the plan's pilot street, and its crop bed (data/gardens.json).
const gardenStreet = "GCR101NQ4BU1SKS"

type gardenRig struct {
	*objRig
	g      *gardens
	ledger *fakeUseLedger
}

// newGardenRig is a hub with the gardens module alone (no objects module: tools are the client's word),
// timers on the rig's clock, and, with ledger, seeds and Guano from the fake ledger.
func newGardenRig(t *testing.T, ledger bool) *gardenRig {
	t.Helper()
	r := &gardenRig{objRig: &objRig{t: t, now: time.UnixMilli(1_800_000_000_000)}}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.g = newGardens(h).(*gardens); return r.g }}
	defer func() { modules = saved }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	if ledger && r.g.uses != nil {
		r.ledger = newFakeUseLedger()
		r.g.uses.ledger = r.ledger
		r.g.uses.spawn = func(fn func()) { fn() }
		n := 0
		r.g.uses.newOp = func() string { n++; return fmt.Sprintf("use-%016x", n) }
	}
	return r
}

func (r *gardenRig) bed(name string) *gardenBed { return gardenWorldData.streets[gardenStreet][name] }

func (r *gardenRig) gardener(id int64, name string) *Client {
	b := r.bed("default")
	return r.player(id, name, gardenStreet, b.x+20*float64(id), b.y)
}

func (r *gardenRig) gact(cl *Client, req int, id, verb string, extra map[string]any) {
	msg := map[string]any{"t": "garden_act", "req": req, "id": id, "verb": verb}
	for k, v := range extra {
		msg[k] = v
	}
	r.send(cl, msg)
	r.advance(0)
}

// plotOf reads one plot out of a frame's "plots".
func plotOf(t *testing.T, frame map[string]any, id string) map[string]any {
	t.Helper()
	plots, _ := frame["plots"].(map[string]any)
	p, _ := plots[id].(map[string]any)
	if p == nil {
		t.Fatalf("no %s in %v", id, frame)
	}
	return p
}

func (r *gardenRig) frames(cl *Client, kind string) []map[string]any {
	var out []map[string]any
	for _, m := range r.drain(cl) {
		if m["t"] == kind {
			out = append(out, m)
		}
	}
	return out
}

// spinachRipe is how long watered spinach takes from seed to crop (data/gardens.json: the source's
// 0.5 h and 0.25 h at the garden's pace, 3 minutes and 90 seconds at 10 times).
func spinachRipe() time.Duration {
	s := gardenWorldData.seeds["seed_spinach"]
	return time.Duration(s.g1+s.g2) * time.Millisecond
}

// grow takes a plot planted with spinach and watered to a crop.
func (r *gardenRig) grow(a *Client, id string) {
	r.t.Helper()
	r.gact(a, 1, id, "plant", map[string]any{"seed": "seed_spinach"})
	if ok := r.only(a, "garden_ok"); plotOf(r.t, ok, id)["state"] != "seeds" {
		r.t.Fatalf("plant answered %v", ok)
	}
	if p := r.g.plots[gardenStreet][id]; !p.wet {
		r.gact(a, 2, id, "water", nil)
		r.only(a, "garden_ok")
	}
	r.advance(spinachRipe())
}

func TestGardenSnapshotOnlyOnTheTenPublicStreets(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	r.send(a, map[string]any{"t": "garden_sync"})
	snap := r.only(a, "gardens")
	if snap["snap"] != true || snap["none"] != nil || snap["lock"] != float64(60000) {
		t.Fatalf("snapshot %v", snap)
	}
	beds := snap["beds"].(map[string]any)
	if fmt.Sprint(beds["default"]) != "[4 2]" || fmt.Sprint(beds["herb"]) != "[3 2]" {
		t.Fatalf("beds %v", beds)
	}
	if len(gardenWorldData.streets) != 10 {
		t.Fatalf("%d shared streets, want the ten public gardens", len(gardenWorldData.streets))
	}
	// Doonboggle Trail (the tutorial's spinach) and any home are not shared.
	for _, street := range []string{"GA94GCF7H3D30IM", "HOME_7", "LNOWHERE"} {
		b := r.player(9, "Bo", street, 100, 100)
		r.send(b, map[string]any{"t": "garden_sync"})
		if snap := r.only(b, "gardens"); snap["none"] != true {
			t.Fatalf("%s answered %v", street, snap)
		}
		r.gact(b, 1, "default:1-1", "plant", map[string]any{"seed": "seed_spinach"})
		if no := r.only(b, "garden_no"); no["code"] != "unknown" {
			t.Fatalf("a plant on %s answered %v", street, no)
		}
	}
}

func TestGardenPlantIsSeenByEveryoneAndOnlyOnACleanPlot(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	b := r.gardener(2, "Bo")
	r.gact(a, 1, "default:1-1", "plant", map[string]any{"seed": "seed_spinach", "mod": 0.9})
	ok := r.only(a, "garden_ok")
	p := plotOf(t, ok, "default:1-1")
	if p["state"] != "seeds" || p["seed"] != "seed_spinach" || p["mod"] != 0.9 {
		t.Fatalf("plant answered %v", ok)
	}
	if before := plotOf(t, ok, "default:1-1"); before == nil || plotOf(t, map[string]any{"plots": ok["before"]}, "default:1-1")["state"] != "clean" {
		t.Fatalf("before %v", ok["before"])
	}
	seen := r.drain(b)
	if len(seen) != 2 || seen[0]["t"] != "gardens" || plotOf(t, seen[0], "default:1-1")["state"] != "seeds" || seen[1]["t"] != "garden_ev" {
		t.Fatalf("Bo saw %v", seen)
	}
	// Bo can't plant over it (include/garden.js 441-446).
	r.gact(b, 1, "default:1-1", "plant", map[string]any{"seed": "seed_spinach"})
	if no := r.only(b, "garden_no"); no["code"] != "not_clean" || no["message"] != gardenNotClean {
		t.Fatalf("a plant over seeds answered %v", no)
	}
	// An herb seed in the crop bed, and a crop seed in the herb bed, are refused with the plot's line.
	r.gact(b, 2, "default:2-1", "plant", map[string]any{"seed": "herb_seed_rubeweed"})
	if no := r.only(b, "garden_no"); no["message"] != gardenCropOnly {
		t.Fatalf("an herb seed in a crop bed answered %v", no)
	}
	// No such plot: the herb bed is 3 by 2.
	r.gact(b, 3, "herb:4-1", "plant", map[string]any{"seed": "herb_seed_rubeweed"})
	if no := r.only(b, "garden_no"); no["code"] != "unknown" {
		t.Fatalf("herb:4-1 answered %v", no)
	}
	if r.g.plots[gardenStreet]["default:1-1"].planter != 1 {
		t.Fatal("the planter is not kept")
	}
}

func TestGardenGrowsOnlyWhileWetAndDryingKeepsProgress(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	r.gact(a, 1, "default:1-1", "plant", map[string]any{"seed": "seed_spinach"})
	r.drain(a)
	r.advance(30 * time.Second)
	if p := r.g.plots[gardenStreet]["default:1-1"]; p.state != "seeds" {
		t.Fatalf("a dry plot grew: %+v", p)
	}
	r.g.waterMs = 3000 // a watering that lasts 3 s
	r.gact(a, 2, "default:1-1", "water", nil)
	r.drain(a)
	r.advance(3 * time.Second)
	p := r.g.plots[gardenStreet]["default:1-1"]
	if p.wet || p.state != "seeds" || p.banked != 3000 {
		t.Fatalf("after 3 s wet the plot is %+v", p)
	}
	frames := r.frames(a, "gardens")
	if len(frames) != 1 || plotOf(t, frames[0], "default:1-1")["wet"] != nil {
		t.Fatalf("the drying was told as %v", frames)
	}
	r.g.waterMs = gardenWorldData.waterMs
	r.gact(a, 3, "default:1-1", "water", nil)
	r.drain(a)
	// The stage needs its whole time less the 3 s it kept.
	g1 := time.Duration(gardenWorldData.seeds["seed_spinach"].g1) * time.Millisecond
	r.advance(g1 - 3*time.Second - time.Millisecond)
	if p.state != "seeds" {
		t.Fatalf("the stage ended early: %+v", p)
	}
	r.advance(time.Millisecond)
	if p.state != "shoots" || p.banked != 0 {
		t.Fatalf("the banked growth was not kept: %+v", p)
	}
}

func TestGardenStagesAfterALongGapAreAllTaken(t *testing.T) {
	// G561: the page's tickGarden takes one stage a call; the server takes every stage the time passed.
	r := newGardenRig(t, false)
	p := &gPlot{state: "seeds", seed: "seed_spinach", wet: true, wetAt: 0, stageAt: 0, mod: 1, pace: 10, wetPace: 10}
	if !r.g.advance(p, 600_000) || p.state != "crop" || p.ripeAt != 270_000 || p.stageAt != 270_000 {
		t.Fatalf("after 10 min the plot is %+v", p)
	}
	// A plot from before the garden's speed changed (no pace) takes its stages on the old clock.
	old := &gPlot{state: "seeds", seed: "seed_spinach", wet: true, wetAt: 0, stageAt: 0, mod: 1}
	if !r.g.advance(old, 20_000) || old.state != "crop" || old.ripeAt != 8000 || old.stageAt != 8000 {
		t.Fatalf("after 20 s the old plot is %+v", old)
	}
}

func TestGardenPlantersSixtySeconds(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	b := r.gardener(2, "Bo")
	r.grow(a, "default:1-1")
	r.drain(a)
	frames := r.frames(b, "gardens")
	last := plotOf(t, frames[len(frames)-1], "default:1-1")
	if last["state"] != "crop" || last["planter"] != float64(1) || last["lock"] != float64(60000) {
		t.Fatalf("Bo saw the crop as %v", last)
	}
	// 59 s on: Bo is refused with the original's line, and nothing changes.
	r.advance(59 * time.Second)
	r.gact(b, 1, "default:1-1", "harvest", map[string]any{"perks": []string{"full"}})
	no := r.only(b, "garden_no")
	if no["code"] != "locked" || no["message"] != gardenLockLine || plotOf(t, no, "default:1-1")["state"] != "crop" {
		t.Fatalf("Bo's pick at 59 s answered %v", no)
	}
	// The planter's own pick during the lock: the crop, halved without Croppery I.
	r.gact(a, 3, "default:1-1", "harvest", nil)
	ok := r.only(a, "garden_ok")
	gr := ok["grant"].(map[string]any)
	if gr["c"] != "spinach" || gr["n"] != float64(6) || plotOf(t, ok, "default:1-1")["state"] != "dirty" {
		t.Fatalf("Ada's pick answered %v", ok)
	}

	// A second round: after 61 s anyone may pick, and the planter then hears "nothing there".
	r.gact(a, 4, "default:1-1", "clear", nil)
	r.drain(a)
	r.grow(a, "default:1-1")
	r.drain(a)
	r.drain(b)
	r.advance(61 * time.Second)
	if frames := r.frames(b, "gardens"); len(frames) != 1 || plotOf(t, frames[0], "default:1-1")["planter"] != nil {
		t.Fatalf("the lock's end was told as %v", frames)
	}
	r.gact(b, 5, "default:1-1", "harvest", map[string]any{"perks": []string{"full"}})
	if ok := r.only(b, "garden_ok"); ok["grant"].(map[string]any)["n"] != float64(12) {
		t.Fatalf("Bo's pick after 61 s answered %v", ok)
	}
	r.gact(a, 6, "default:1-1", "harvest", nil)
	if no := r.only(a, "garden_no"); no["code"] != "nothing" || no["message"] != gardenNothingPick {
		t.Fatalf("Ada's late pick answered %v", no)
	}
}

func TestGardenTwoPickersOnlyOneGetsTheCrop(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	b := r.gardener(2, "Bo")
	c := r.gardener(3, "Cy")
	r.grow(a, "default:2-2")
	r.advance(61 * time.Second)
	r.drain(b)
	r.drain(c)
	r.send(b, map[string]any{"t": "garden_act", "req": 1, "id": "default:2-2", "verb": "harvest"})
	r.send(c, map[string]any{"t": "garden_act", "req": 1, "id": "default:2-2", "verb": "harvest"})
	r.advance(0)
	if len(r.frames(b, "garden_ok")) != 1 || len(r.frames(c, "garden_no")) != 1 || len(r.g.grants) != 1 {
		t.Fatalf("two pickers: grants %v", r.g.grants)
	}
}

func TestGardenSeedAndGuanoGoThroughTheLedger(t *testing.T) {
	r := newGardenRig(t, true)
	a := r.gardener(1, "Ada")
	b := r.gardener(2, "Bo")
	r.ledger.give(1, "seed_spinach", 1)
	// Bo holds no seed: the original's line, nothing planted.
	r.gact(b, 1, "default:1-1", "plant", map[string]any{"seed": "seed_spinach"})
	if no := r.only(b, "garden_no"); no["code"] != "no_item" || no["message"] != gardenNoSeed || plotOf(t, no, "default:1-1")["state"] != "clean" {
		t.Fatalf("a plant without the seed answered %v", no)
	}
	r.gact(a, 1, "default:1-1", "plant", map[string]any{"seed": "seed_spinach"})
	ok := r.only(a, "garden_ok")
	use := ok["use"].(map[string]any)
	if use["c"] != "seed_spinach" || use["n"] != float64(1) || use["spent"] != true {
		t.Fatalf("the plant's use is %v", use)
	}
	if got := strings.Join(r.ledger.calls, ","); !strings.Contains(got, "1 seed_spinach x1") {
		t.Fatalf("ledger calls %s", got)
	}
	// The site gives no answer: the plant is refused and nothing changes.
	r.ledger.down = true
	r.ledger.give(1, "seed_spinach", 1)
	r.gact(a, 2, "default:2-1", "plant", map[string]any{"seed": "seed_spinach"})
	if no := r.only(a, "garden_no"); no["code"] != "no_answer" || plotOf(t, no, "default:2-1")["state"] != "clean" {
		t.Fatalf("a plant with the site down answered %v", no)
	}
	r.ledger.down = false
	// Guano: watered enough, taken by the ledger, seeds straight to shoots.
	r.gact(a, 3, "default:1-1", "fertilize", nil)
	if no := r.only(a, "garden_no"); no["message"] != gardenFertDry {
		t.Fatalf("Guano on a dry plot answered %v", no)
	}
	r.gact(a, 4, "default:1-1", "water", nil)
	r.drain(a)
	r.gact(a, 5, "default:1-1", "fertilize", nil)
	if no := r.only(a, "garden_no"); no["message"] != gardenFertNoGuano {
		t.Fatalf("Guano without any answered %v", no)
	}
	r.ledger.give(1, gardenGuano, 1)
	r.gact(a, 6, "default:1-1", "fertilize", nil)
	if ok := r.only(a, "garden_ok"); plotOf(t, ok, "default:1-1")["state"] != "shoots" || ok["use"].(map[string]any)["c"] != gardenGuano {
		t.Fatalf("Guano answered %v", ok)
	}
	// Shoots take Guano once: a tenth of what is left (include/garden.js 625-639).
	r.ledger.give(1, gardenGuano, 2)
	r.advance(time.Second)
	r.gact(a, 7, "default:1-1", "fertilize", nil)
	p := r.g.plots[gardenStreet]["default:1-1"]
	// A second into the shoots: a tenth of the rest of grow 2.
	want := (gardenWorldData.seeds["seed_spinach"].g2 - 1000) / 10
	if !p.fert || r.g.due(p) != r.now.UnixMilli()+want {
		t.Fatalf("Guano on shoots left %+v, due in %d", p, r.g.due(p)-r.now.UnixMilli())
	}
	r.drain(a)
	r.gact(a, 8, "default:1-1", "fertilize", nil)
	if no := r.only(a, "garden_no"); no["message"] != gardenFertNothing {
		t.Fatalf("a second Guano answered %v", no)
	}
}

func TestGardenWaterAllIsOneMessage(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	r.send(a, map[string]any{"t": "garden_act", "req": 1, "bed": "default", "verb": "water_all", "ids": []string{"default:1-1", "default:2-1", "default:3-1", "default:4-1", "default:1-2", "default:2-2", "default:3-2", "default:4-2"}})
	r.advance(0)
	ok := r.only(a, "garden_ok")
	if ok["watered"] != float64(8) || len(ok["plots"].(map[string]any)) != 8 {
		t.Fatalf("water all answered %v", ok)
	}
	r.send(a, map[string]any{"t": "garden_act", "req": 2, "bed": "default", "verb": "water_all", "ids": []string{"default:1-1"}})
	r.advance(0)
	if no := r.only(a, "garden_no"); no["message"] != gardenNoDry {
		t.Fatalf("water all with none dry answered %v", no)
	}
	// A plot of the other bed is not this bed's.
	r.send(a, map[string]any{"t": "garden_act", "req": 3, "bed": "default", "verb": "water_all", "ids": []string{"herb:1-1"}})
	r.advance(0)
	if no := r.only(a, "garden_no"); no["code"] != "unknown" {
		t.Fatalf("water all over two beds answered %v", no)
	}
}

func TestGardenHoeOnlyADirtyPlot(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	r.gact(a, 1, "default:1-1", "clear", nil)
	if no := r.only(a, "garden_no"); no["message"] != gardenNoHoeing {
		t.Fatalf("hoeing a clean plot answered %v", no)
	}
	r.grow(a, "default:1-1")
	r.gact(a, 2, "default:1-1", "harvest", nil)
	r.drain(a)
	r.gact(a, 3, "default:1-1", "clear", nil)
	if ok := r.only(a, "garden_ok"); plotOf(t, ok, "default:1-1")["state"] != "clean" || plotOf(t, ok, "default:1-1")["wet"] != true {
		t.Fatalf("a hoe answered %v (wetness carries over)", ok)
	}
}

func TestGardenGrantsAreResentUntilAcknowledged(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	r.grow(a, "default:1-1")
	r.gact(a, 2, "default:1-1", "harvest", map[string]any{"perks": []string{"full"}})
	id := r.only(a, "garden_ok")["grant"].(map[string]any)["id"].(string)
	// The page never heard (a reload): the next connect hears the grant again, and again, until acked.
	for i := 0; i < 2; i++ {
		a = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
		got := r.frames(a, "garden_grant")
		if len(got) != 1 || got[0]["id"] != id || got[0]["c"] != "spinach" || got[0]["n"] != float64(12) {
			t.Fatalf("connect %d heard %v", i, got)
		}
	}
	// Someone else's ack changes nothing; the owner's ends it.
	b := r.gardener(2, "Bo")
	r.send(b, map[string]any{"t": "garden_ack", "id": id})
	if len(r.g.grants) != 1 {
		t.Fatal("another player's ack took the grant")
	}
	r.send(a, map[string]any{"t": "garden_ack", "id": id})
	a = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	if got := r.frames(a, "garden_grant"); len(got) != 0 || len(r.g.grants) != 0 {
		t.Fatalf("after the ack: %v", got)
	}
}

func TestGardenLimitsAndReach(t *testing.T) {
	r := newGardenRig(t, false)
	b := r.bed("default")
	far := r.player(1, "Ada", gardenStreet, b.x+objReachX+10, b.y)
	r.gact(far, 1, "default:1-1", "water", nil)
	if no := r.only(far, "garden_no"); no["code"] != "far" {
		t.Fatalf("a water from afar answered %v", no)
	}
	a := r.gardener(2, "Bo")
	codes := []string{}
	for i := 1; i <= 8; i++ {
		r.send(a, map[string]any{"t": "garden_act", "req": i, "id": "default:1-1", "verb": "clear"})
		for _, m := range r.frames(a, "garden_no") {
			codes = append(codes, m["code"].(string))
		}
	}
	if strings.Count(strings.Join(codes, ","), "slow_down") != 2 {
		t.Fatalf("eight acts at once answered %v", codes)
	}
	r.send(a, map[string]any{"t": "garden_act", "req": 0, "id": "default:1-1", "verb": "clear"})
	if errs := r.frames(a, "error"); len(errs) != 1 {
		t.Fatalf("a bad frame answered %v", errs)
	}
}

func TestGardenRestartKeepsPlotsGrantsAndGivesTheLockAgain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "realtime-gardens.json")
	gardensFileForTest = path
	defer func() { gardensFileForTest = "" }()
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	// Mid-growth on one plot, a crop in its lock on another, a pick not acknowledged.
	r.grow(a, "default:1-1")
	r.advance(70 * time.Second)
	r.gact(a, 3, "default:2-1", "plant", map[string]any{"seed": "seed_spinach"})
	r.gact(a, 4, "default:2-1", "water", nil)
	r.gact(a, 5, "default:3-1", "plant", map[string]any{"seed": "seed_spinach"})
	r.gact(a, 6, "default:3-1", "water", nil)
	r.advance(spinachRipe())
	r.gact(a, 7, "default:3-1", "harvest", nil)
	r.gact(a, 8, "default:2-1", "plant", map[string]any{"seed": "seed_potato"}) // refused: not clean
	r.advance(time.Second)                                                      // the soon save
	r.hub.Shutdown()
	// Down for 10 minutes: 2-1's lock was running at the stop, 1-1's ran out before it.
	later := r.now.Add(10 * time.Minute)
	r2 := newGardenRig(t, false)
	r2.now = later
	if len(r2.g.grants) != 1 || r2.g.grants[0].User != 1 {
		t.Fatalf("grants after the restart %v", r2.g.grants)
	}
	a2 := r2.gardener(1, "Ada")
	if got := r2.frames(a2, "garden_grant"); len(got) != 0 {
		// gardener drains the join; the grant came with the connect.
		t.Fatalf("unexpected %v", got)
	}
	b2 := r2.gardener(2, "Bo")
	r2.send(b2, map[string]any{"t": "garden_sync"})
	snap := r2.only(b2, "gardens")
	p11, p21 := plotOf(t, snap, "default:1-1"), plotOf(t, snap, "default:2-1")
	if p11["state"] != "crop" || p11["planter"] != nil {
		t.Fatalf("1-1 after the restart %v", p11)
	}
	if p21["state"] != "crop" || p21["planter"] != float64(1) || p21["lock"] != float64(60000) {
		t.Fatalf("2-1's lock ran into the downtime: the planter gets a fresh 60 s: %v", p21)
	}
	if p := plotOf(t, snap, "default:3-1"); p["state"] != "dirty" {
		t.Fatalf("3-1 after the restart %v", p)
	}
	// A crash mid-write leaves a torn file: moved aside, the module starts empty and goes on saving.
	if err := os.WriteFile(path, []byte(`{"v":1,"streets":{"GCR`), 0o600); err != nil {
		t.Fatal(err)
	}
	r3 := newGardenRig(t, false)
	if len(r3.g.plots) != 0 || r3.g.path != path {
		t.Fatalf("a torn file left %v (path %q)", r3.g.plots, r3.g.path)
	}
	if m, _ := filepath.Glob(path + ".bad-*"); len(m) != 1 {
		t.Fatalf("the torn file was not moved aside: %v", m)
	}
	// An old file naming a plot the layout no longer has drops it.
	old := `{"v":1,"saved":1,"streets":{"GCR101NQ4BU1SKS":{"default:9-9":{"s":"crop","seed":"seed_spinach"},"herb:1-1":{"s":"dirty"}},"NOWHERE":{"default:1-1":{"s":"dirty"}}},"grants":[]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r4 := newGardenRig(t, false)
	if len(r4.g.plots[gardenStreet]) != 1 || r4.g.plots[gardenStreet]["herb:1-1"] == nil || len(r4.g.plots) != 1 {
		t.Fatalf("an old file kept %v", r4.g.plots)
	}
}

func TestGardenOffLeavesPagesOnTheirOwnPlots(t *testing.T) {
	gardensOffForTest = true
	defer func() { gardensOffForTest = false }()
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	r.send(a, map[string]any{"t": "garden_sync"})
	errs := r.frames(a, "error")
	if len(errs) != 1 || errs[0]["code"] != "bad_frame" {
		t.Fatalf("-gardens off answered %v", errs)
	}
}

func TestGardenDevHookOnlyWithTheSwitch(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	r.send(a, map[string]any{"t": "garden_dev", "lock": 2000})
	if r.g.lockMs != 60000 || len(r.frames(a, "error")) != 1 {
		t.Fatal("garden_dev worked without GRODDLE_GARDENS_DEV")
	}
	r.g.dev = true
	r.send(a, map[string]any{"t": "garden_dev", "lock": 2000, "water": 5000})
	if r.g.lockMs != 2000 || r.g.waterMs != 5000 {
		t.Fatalf("garden_dev left lock %d water %d", r.g.lockMs, r.g.waterMs)
	}
}

// The QA clock: "skip" lets garden time pass for every plot at once, by the rules.
func TestGardenDevSkipLetsGardenTimePass(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	b := r.gardener(2, "Bea")
	r.gact(a, 1, "default:1-1", "water", nil)
	r.gact(a, 2, "default:1-1", "plant", map[string]any{"seed": "seed_potato"})
	r.drain(a)
	r.drain(b)
	p := r.g.plots[gardenStreet]["default:1-1"]
	r.send(a, map[string]any{"t": "garden_dev", "skip": 3_600_000})
	if p.state != "seeds" || len(r.frames(a, "error")) != 1 {
		t.Fatalf("garden_dev skip worked without GRODDLE_GARDENS_DEV: %+v", p)
	}
	r.g.dev = true
	// 36 minutes: the potato's seeds stage ends, and everyone on the street hears it.
	r.send(a, map[string]any{"t": "garden_dev", "skip": 36 * 60_000})
	if p.state != "shoots" || !p.wet {
		t.Fatalf("36 minutes on: %+v", p)
	}
	if f := r.frames(b, "gardens"); len(f) != 1 || plotOf(t, f[0], "default:1-1")["state"] != "shoots" {
		t.Fatalf("the other gardener heard %v", f)
	}
	// 33 more: a crop at 69 minutes, still wet; 3 more and the watering is over.
	r.send(a, map[string]any{"t": "garden_dev", "skip": 33 * 60_000})
	if p.state != "crop" || !p.wet || p.planter != 1 {
		t.Fatalf("69 minutes on: %+v", p)
	}
	r.send(a, map[string]any{"t": "garden_dev", "skip": 3 * 60_000})
	if p.state != "crop" || p.wet || p.planter != 0 {
		t.Fatalf("72 minutes on: %+v", p)
	}
}

// The growth fixtures the game's tests run through js/garden.js too (tests/shared-gardens.test.js):
// both copies of the rules agree on when a plot changes.
func TestGardenGrowthFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/garden-growth.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string         `json:"name"`
		Plot   gardenRec      `json:"plot"`
		At     int64          `json:"at"`
		Expect map[string]any `json:"expect"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	r := newGardenRig(t, false)
	for _, c := range cases {
		p := &gPlot{state: c.Plot.State, seed: c.Plot.Seed, wet: c.Plot.Wet, wetAt: c.Plot.WetAt, stageAt: c.Plot.StageAt, banked: c.Plot.Banked, mod: c.Plot.Mod, fert: c.Plot.Fert, pace: c.Plot.Pace, wetPace: c.Plot.WetPace}
		if p.mod == 0 {
			p.mod = 1
		}
		r.g.advance(p, c.At)
		got := map[string]any{"state": p.state, "wet": p.wet, "banked": float64(p.banked)}
		for k, want := range c.Expect {
			if fmt.Sprint(got[k]) != fmt.Sprint(want) {
				t.Errorf("%s: %s = %v, want %v (%+v)", c.Name, k, got[k], want, p)
			}
		}
	}
}

func TestGardenToolsAreTheLedgersWithTheObjectsModule(t *testing.T) {
	r := &gardenRig{objRig: &objRig{t: t, now: time.UnixMilli(1_800_000_000_000)}}
	saved := modules
	var objs *objects
	modules = []moduleFactory{
		func(h *Hub) Module { objs = newObjects(h).(*objects); return objs },
		func(h *Hub) Module { r.g = newGardens(h).(*gardens); return r.g },
	}
	defer func() { modules = saved }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	l := newFakeUseLedger()
	objs.uses.ledger = l
	objs.uses.spawn = func(fn func()) { fn() }
	a := r.gardener(1, "Ada")
	r.gact(a, 1, "default:1-1", "water", nil)
	if no := r.only(a, "garden_no"); no["code"] != "no_tool" || no["message"] != toolGardenCan.line {
		t.Fatalf("a water without a can answered %v", no)
	}
	r.advance(toolNoFresh)
	l.give(1, "irrigator_9000", 1)
	r.gact(a, 2, "default:1-1", "water", nil)
	r.only(a, "garden_ok")
	r.g.plots[gardenStreet]["default:1-1"].state = "dirty"
	r.gact(a, 3, "default:1-1", "clear", nil)
	if no := r.only(a, "garden_no"); no["code"] != "no_tool" || no["message"] != toolHoe.line {
		t.Fatalf("a hoe without one answered %v", no)
	}
	if !strings.Contains(strings.Join(toolClasses, ","), "high_class_hoe") {
		t.Fatal("the High-Class Hoe is not asked about")
	}
}

// The garden's speed (Austin 2026-11-10): data/gardens.json is written from the game's one number.
func TestGardenTimesAreTheSourcesHoursAtThePace(t *testing.T) {
	w := gardenWorldData
	if w.pace != 10 {
		t.Fatalf("pace %d", w.pace)
	}
	hour := int64(3_600_000 / w.pace)
	// seed_*.js classProps time_grow1, time_grow2 (hours).
	for seed, h := range map[string][2]float64{"seed_spinach": {0.5, 0.25}, "seed_potato": {6, 5.5}, "seed_onion": {1, 0.5}, "herb_seed_purple_flower": {0.5, 0.5}, "herb_seed_yellow_crumb_flower": {4.5, 4.5}} {
		s := w.seeds[seed]
		if s.g1 != int64(h[0]*float64(hour)) || s.g2 != int64(h[1]*float64(hour)) {
			t.Errorf("%s grows in %d + %d ms, want %v h at %d ms an hour", seed, s.g1, s.g2, h, hour)
		}
		// The clock before: 8 s an hour, never under 4 s a stage.
		if s.l1 != max(4000, int64(h[0]*8000)) || s.l2 != max(4000, int64(h[1]*8000)) {
			t.Errorf("%s old times %d + %d", seed, s.l1, s.l2)
		}
	}
	// include/garden.js timings.water 12 hours, at the same pace; before it, 12 real hours.
	if w.waterMs != 12*hour || w.legacyWaterMs != 12*3_600_000 {
		t.Errorf("a watering lasts %d ms (old %d)", w.waterMs, w.legacyWaterMs)
	}
	// The planter's lock is the source's own real 60 seconds (pushEvent 0.0167 h), not scaled.
	if w.lockMs != 60_000 {
		t.Errorf("lock %d", w.lockMs)
	}
}

// A state file written before the speed changed: its growing crops finish on the old clock, its
// waterings last their 12 hours, nothing is lost, and whatever is planted or watered next runs at
// the new pace. The marks survive the next save and restart.
func TestGardenFileFromBeforeThePaceFinishesOnItsOldClock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "realtime-gardens.json")
	gardensFileForTest = path
	defer func() { gardensFileForTest = "" }()
	t0 := int64(1_800_000_000_000)
	// Saved 5 s before the restart: potato seeds planted 10 s before the save in a plot watered 3 hours
	// before (old: 48 s + 44 s to a crop), dry spinach shoots with 1 s kept, a ripe crop, a wet clean plot.
	old := fmt.Sprintf(`{"v":1,"saved":%d,"streets":{"GCR101NQ4BU1SKS":{`+
		`"default:1-1":{"s":"seeds","seed":"seed_potato","wet":true,"wetAt":%d,"stageAt":%d,"mod":1,"planter":1},`+
		`"default:2-1":{"s":"shoots","seed":"seed_spinach","stageAt":%d,"banked":1000,"mod":1,"planter":1},`+
		`"default:3-1":{"s":"crop","seed":"seed_spinach","wet":true,"wetAt":%d,"stageAt":%d,"mod":1,"ripeAt":%d},`+
		`"default:4-1":{"s":"clean","wet":true,"wetAt":%d,"stageAt":%d,"mod":1}}},"grants":[]}`,
		t0-5000, t0-3*3_600_000, t0-15_000, t0-60_000, t0-3*3_600_000, t0-600_000, t0-600_000, t0-11*3_600_000, t0-11*3_600_000)
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	r.send(a, map[string]any{"t": "garden_sync"})
	snap := r.only(a, "gardens")
	p11, p21, p31, p41 := plotOf(t, snap, "default:1-1"), plotOf(t, snap, "default:2-1"), plotOf(t, snap, "default:3-1"), plotOf(t, snap, "default:4-1")
	if p11["state"] != "seeds" || p11["wet"] != true || p11["pace"] != nil || p11["wetPace"] != nil {
		t.Fatalf("the old potato after the restart: %v", p11)
	}
	if p21["state"] != "shoots" || p21["banked"] != float64(1000) || p31["state"] != "crop" || p41["state"] != "clean" || p41["wet"] != true {
		t.Fatalf("the other old plots: %v %v %v", p21, p31, p41)
	}
	// The potato was 15 s into its old 48 s of seeds: shoots 33 s on, a crop 44 s after that.
	r.advance(33 * time.Second)
	if p := r.g.plots[gardenStreet]["default:1-1"]; p.state != "shoots" {
		t.Fatalf("33 s on the old potato is %+v", p)
	}
	r.advance(44 * time.Second)
	if p := r.g.plots[gardenStreet]["default:1-1"]; p.state != "crop" || !p.wet {
		t.Fatalf("77 s on the old potato is %+v", p)
	}
	// The dry old shoots, watered now: 3 s of their old 4 s are left, and the water is today's.
	r.gact(a, 1, "default:2-1", "water", nil)
	r.drain(a)
	r.advance(3 * time.Second)
	if p := r.g.plots[gardenStreet]["default:2-1"]; p.state != "crop" || p.wetPace != 10 || p.pace != 0 {
		t.Fatalf("the old spinach, watered: %+v", p)
	}
	// The wet clean plot keeps its old 12 hours (an hour is left); spinach planted there is on the new clock.
	r.gact(a, 2, "default:4-1", "plant", map[string]any{"seed": "seed_spinach"})
	ok := r.only(a, "garden_ok")
	if p := plotOf(t, ok, "default:4-1"); p["pace"] != float64(10) || p["wet"] != true || p["wetPace"] != nil {
		t.Fatalf("spinach planted in the old wet plot: %v", p)
	}
	r.advance(30 * time.Second)
	if p := r.g.plots[gardenStreet]["default:4-1"]; p.state != "seeds" {
		t.Fatalf("new spinach grew on the old clock: %+v", p)
	}
	r.advance(spinachRipe() - 30*time.Second)
	if p := r.g.plots[gardenStreet]["default:4-1"]; p.state != "crop" || !p.wet {
		t.Fatalf("new spinach after its 4.5 minutes: %+v", p)
	}
	// Picked and hoed, the old potato plot is an ordinary plot: the next potato takes its 69 minutes.
	r.gact(a, 3, "default:1-1", "harvest", nil)
	r.gact(a, 4, "default:1-1", "clear", nil)
	r.gact(a, 5, "default:1-1", "plant", map[string]any{"seed": "seed_potato"})
	r.drain(a)
	r.advance(2 * time.Second) // the save
	r.hub.Shutdown()
	later := r.now
	r2 := newGardenRig(t, false)
	r2.now = later
	p := r2.g.plots[gardenStreet]["default:1-1"]
	if p == nil || p.state != "seeds" || p.seed != "seed_potato" || p.pace != 10 {
		t.Fatalf("the new potato after a restart: %+v", p)
	}
	if need := r2.g.need(p); need != 36*60*1000 {
		t.Fatalf("its seeds stage is %d ms", need)
	}
}

// One watering covers about one long crop, as in Glitch: potato (69 min) planted on a fresh watering
// (72 min) ripens wet; planted late it dries, keeps its growth, and goes on when watered.
func TestGardenAWateringCoversOneLongCrop(t *testing.T) {
	r := newGardenRig(t, false)
	a := r.gardener(1, "Ada")
	r.gact(a, 1, "default:1-1", "water", nil)
	r.gact(a, 2, "default:1-1", "plant", map[string]any{"seed": "seed_potato"})
	r.drain(a)
	r.advance(69 * time.Minute)
	p := r.g.plots[gardenStreet]["default:1-1"]
	if p.state != "crop" || !p.wet {
		t.Fatalf("potato after 69 minutes: %+v", p)
	}
	r.advance(3 * time.Minute)
	if p.wet {
		t.Fatalf("the watering outlasted 72 minutes: %+v", p)
	}
	// A second potato in the same plot, watered again: dry at 72 minutes only if planted late.
	r.gact(a, 3, "default:1-1", "harvest", nil)
	r.gact(a, 4, "default:1-1", "clear", nil)
	r.gact(a, 5, "default:1-1", "water", nil)
	r.advance(10 * time.Minute)
	r.gact(a, 6, "default:1-1", "plant", map[string]any{"seed": "seed_potato"})
	r.drain(a)
	r.advance(62 * time.Minute)
	p = r.g.plots[gardenStreet]["default:1-1"] // hoed dry it was forgotten: the watering made it anew
	if p.wet || p.state != "shoots" || p.banked != 26*60*1000 {
		t.Fatalf("potato planted 10 minutes late, at the drying: %+v", p)
	}
	r.advance(5 * time.Hour)
	if p.state != "shoots" || p.banked != 26*60*1000 {
		t.Fatalf("a dry plot grew or lost growth: %+v", p)
	}
	r.gact(a, 7, "default:1-1", "water", nil)
	r.advance(7 * time.Minute)
	if p.state != "crop" {
		t.Fatalf("watered again, the last 7 minutes: %+v", p)
	}
}

// Report 188: the Herb Gardens streets have two herb beds, the first keyed herb2 (js/garden.js
// streetGardenBed); herb2's plots take herb seeds only and there is no crop bed.
func TestGardenHerbGardensStreetsHaveTwoHerbBeds(t *testing.T) {
	const chakra = "GA95U14319524O9"
	beds := gardenWorldData.streets[chakra]
	if beds["herb2"] == nil || beds["herb2"].bed != "herb" || beds["herb2"].key != "herb2" || beds["herb"] == nil || beds["default"] != nil {
		t.Fatalf("Chakra Phool Herb Gardens beds %v", beds)
	}
	if bed, key := gardenWorldData.bedOf(chakra, "herb2:4-2"); bed != beds["herb2"] || key != "4-2" {
		t.Fatalf("herb2:4-2 is %v %q", bed, key)
	}
	if got := bedOrder(beds["herb2"]); got[0] != "herb2:1-1" || len(got) != 8 {
		t.Fatalf("herb2 order %v", got)
	}
	r := newGardenRig(t, false)
	b := beds["herb2"]
	a := r.player(1, "Ada", chakra, b.x+20, b.y)
	r.gact(a, 1, "herb2:1-1", "plant", map[string]any{"seed": "herb_seed_rubeweed"})
	if ok := r.only(a, "garden_ok"); plotOf(t, ok, "herb2:1-1")["state"] != "seeds" {
		t.Fatalf("an herb seed in herb2 answered %v", ok)
	}
	r.gact(a, 2, "herb2:2-1", "plant", map[string]any{"seed": "seed_spinach"})
	if no := r.only(a, "garden_no"); no["message"] != gardenHerbOnly {
		t.Fatalf("a crop seed in herb2 answered %v", no)
	}
}
