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

// Old Main Street (Ix, hub 27), the pilot: four Patches and a Dead Spice Plant (data/patches.json).
const oldMainStreet = "GM4103L52ID23"

type patchRig struct {
	*objRig
	g       *patches
	ledger  *fakeUseLedger
	pending []func() // ledger calls waiting (deferLedger)
}

// newPatchRig is a hub with the patches module (and, with objects, the street objects module: tools
// through the ledger, a street tree's death), timers on the rig's clock, the fake ledger for beans and
// bottles, and a fixed roll.
func newPatchRig(t *testing.T, withObjects bool) *patchRig {
	t.Helper()
	r := &patchRig{objRig: &objRig{t: t, now: time.UnixMilli(gameEpochMs + 2000*gameDayMs + 3_600_000)}}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.g = newPatches(h).(*patches); return r.g }}
	if withObjects {
		modules = append(modules, func(h *Hub) Module { r.mod = newObjects(h).(*objects); return r.mod })
	}
	defer func() { modules = saved }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	if r.mod != nil {
		r.mod.roll = func() float64 { return 0.5 }
	}
	if r.g.uses != nil {
		r.g.roll = func() float64 { return 0.5 }
		r.ledger = newFakeUseLedger()
		r.g.uses.ledger = r.ledger
		r.g.uses.spawn = func(fn func()) { fn() }
		n := 0
		r.g.uses.newOp = func() string { n++; return fmt.Sprintf("use-%016x", n) }
	}
	return r
}

// deferLedger holds every ledger call until flush, so two acts can race.
func (r *patchRig) deferLedger() {
	r.g.uses.spawn = func(fn func()) { r.pending = append(r.pending, fn) }
}

func (r *patchRig) flush() {
	for len(r.pending) > 0 {
		fn := r.pending[0]
		r.pending = r.pending[1:]
		fn()
	}
	r.advance(0)
}

// shareAll shares every laid-out street (a test world without the pilot list).
func (r *patchRig) shareAll() {
	w := *r.g.w
	w.pilot = nil
	r.g.w = &w
}

func (r *patchRig) spot(street, key string) *patchSpot { return r.g.w.streets[street].spots[key] }

// gardener stands a player at a plot.
func (r *patchRig) at(id int64, name, street, key string) *Client {
	s := r.spot(street, key)
	return r.player(id, name, street, s.x+10*float64(id), s.y)
}

// walk moves a player to a plot on their street.
func (r *patchRig) walk(cl *Client, key string) {
	s := r.spot(cl.Street(), key)
	r.send(cl, map[string]any{"t": "move", "x": s.x, "y": s.y, "dir": 1})
	r.advance(0)
}

func (r *patchRig) pact(cl *Client, req int, id, verb string, extra map[string]any) {
	msg := map[string]any{"t": "patch_act", "req": req, "id": id, "verb": verb}
	for k, v := range extra {
		msg[k] = v
	}
	delete(r.g.limits, cl.ID()) // the rate limit has its own test
	r.send(cl, msg)
	r.advance(0)
}

// answer is the one patch_ok or patch_no frame queued for cl (the rest dropped).
func (r *patchRig) answer(cl *Client) map[string]any {
	r.t.Helper()
	var got []map[string]any
	for _, m := range r.drain(cl) {
		if m["t"] == "patch_ok" || m["t"] == "patch_no" {
			got = append(got, m)
		}
	}
	if len(got) != 1 {
		r.t.Fatalf("want one answer, got %v", got)
	}
	return got[0]
}

func (r *patchRig) ok(cl *Client) map[string]any {
	r.t.Helper()
	a := r.answer(cl)
	if a["t"] != "patch_ok" {
		r.t.Fatalf("refused: %v", a)
	}
	return a
}

func (r *patchRig) no(cl *Client, code string) map[string]any {
	r.t.Helper()
	a := r.answer(cl)
	if a["t"] != "patch_no" || a["code"] != code {
		r.t.Fatalf("want %s, got %v", code, a)
	}
	return a
}

func (r *patchRig) live(street, key string) *pPlot { return r.g.plots[street][key] }

// seedling plants a bean in a tended plot.
func (r *patchRig) seedling(cl *Client, key, bean string) {
	r.t.Helper()
	r.ledger.give(cl.ID(), bean, 1)
	r.pact(cl, 90, key, "tend", nil)
	r.ok(cl)
	r.pact(cl, 91, key, "plant", map[string]any{"bean": bean})
	r.ok(cl)
}

// tree grows a planted plot into its tree: three pettings, five minutes.
func (r *patchRig) tree(cl *Client, key, bean string) *pTree {
	r.t.Helper()
	r.seedling(cl, key, bean)
	for i := 0; i < 3; i++ {
		r.pact(cl, 92+i, key, "pet", nil)
		r.ok(cl)
	}
	r.advance(5 * time.Minute)
	p := r.live(oldMainStreet, key)
	if p == nil || p.stage != "tree" {
		r.t.Fatalf("no tree: %+v", p)
	}
	return p.tree
}

func TestPatchTheLayoutIsThePagesOwn(t *testing.T) {
	w := patchWorldData
	if len(w.streets) != 100 {
		t.Fatalf("%d streets with Patches, want the plan's 100", len(w.streets))
	}
	n := 0
	for _, st := range w.streets {
		n += len(st.spots)
	}
	if n != 188 {
		t.Fatalf("%d plots, want 175 Patches + 8 Dark Patches + 5 dead trees", n)
	}
	st := w.streets[oldMainStreet]
	if st.hub != 27 || len(st.spots) != 5 || st.spots["patch:3"] == nil || st.spots["trant_spice_dead:0"] == nil {
		t.Fatalf("Old Main Street is %+v", st)
	}
	if !w.shared(oldMainStreet) || w.shared("GLI11B92BD71UJA") || len(w.pilot) != 1 {
		t.Fatalf("the pilot is %v, want Old Main Street only", w.pilot)
	}
	if w.plantingHub(28) != 56 || w.plantingHub(52) != 56 || w.plantingHub(27) != 27 {
		t.Fatal("the archived Groddle hubs plant as Groddle Forest")
	}
	tm := w.rules.Timers
	if tm.Clean != 90_000 || tm.Dug != 600_000 || tm.Min != 300_000 || tm.Max != 14_400_000 || tm.Cycle1 != 600_000 || tm.Cycle != 1_200_000 || tm.Wood != 2000 || tm.Pets != 3 {
		t.Fatalf("timers %+v (patch.js setClean/setDug, patch_seedling.js setTimers, trants.js recalcCycles)", tm)
	}
	if w.rules.Grow["3"] != 180_000 || w.rules.Grow["4"] != 60_000 || w.rules.Clear != 25 || w.rules.Dig["soil_appreciation_4"] != 4 {
		t.Fatalf("rules %+v", w.rules)
	}
}

func TestPatchOffAnswersBadFrameAndOffThePilotNothingIsShared(t *testing.T) {
	patchesOffForTest = true
	r := newPatchRig(t, false)
	patchesOffForTest = false
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	r.send(a, map[string]any{"t": "patch_sync"})
	if m := r.only(a, "error"); m["code"] != "bad_frame" {
		t.Fatalf("off answered %v", m)
	}

	r = newPatchRig(t, false)
	b := r.player(2, "Bo", "GLI11B92BD71UJA", 3527, 922)
	r.send(b, map[string]any{"t": "patch_sync"})
	if snap := r.only(b, "patches"); snap["none"] != true {
		t.Fatalf("a street off the pilot answered %v", snap)
	}
	r.pact(b, 1, "patch:0", "tend", nil)
	r.no(b, "unknown")
	c := r.at(3, "Cy", oldMainStreet, "patch:0")
	r.send(c, map[string]any{"t": "patch_sync"})
	snap := r.only(c, "patches")
	if snap["snap"] != true || snap["none"] != nil || len(snap["plots"].(map[string]any)) != 0 || snap["timers"].(map[string]any)["clean"] != float64(90000) {
		t.Fatalf("the pilot's snapshot is %v", snap)
	}
}

func TestPatchTendLockIsOneTenderAndDigWaitsForIt(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	b := r.at(2, "Bo", oldMainStreet, "patch:0")
	r.pact(a, 1, "patch:0", "tend", map[string]any{"do": "start"})
	if ok := r.ok(a); ok["lock"] != float64(10000) {
		t.Fatalf("tend start answered %v", ok)
	}
	seen := r.drain(b)
	if len(seen) != 2 || seen[0]["t"] != "patches" || seen[1]["t"] != "patch_ev" || seen[1]["do"] != "start" {
		t.Fatalf("Bo saw %v", seen)
	}
	r.pact(b, 1, "patch:0", "tend", map[string]any{"do": "start"})
	if no := r.no(b, "busy"); no["message"] != patchBusy || no["plot"].(map[string]any)["tender"] != float64(1) {
		t.Fatalf("a second tender answered %v", no)
	}
	r.pact(b, 2, "patch:0", "dig", map[string]any{"do": "start"})
	r.no(b, "busy")
	r.pact(b, 3, "patch:0", "tend", nil)
	r.no(b, "busy")
	// The lock lapses after 10 s (onTendTimeout): Bo may tend.
	r.advance(10 * time.Second)
	r.drain(a)
	r.drain(b)
	r.pact(b, 4, "patch:0", "tend", nil)
	ok := r.ok(b)
	if p := ok["plot"].(map[string]any); p["stage"] != "clean" || p["due"] != float64(r.now.UnixMilli()+90000) {
		t.Fatalf("a tend answered %v", ok)
	}
	r.pact(a, 5, "patch:0", "tend", nil)
	if no := r.no(a, "not_messy"); no["message"] != patchNoTending {
		t.Fatal(no)
	}
	// Weeds back in 90 s for everyone.
	r.drain(b)
	r.advance(90 * time.Second)
	if fr := r.frames(a, "patches"); len(fr) != 1 {
		t.Fatalf("the weeds came back as %v", fr)
	}
	if r.live(oldMainStreet, "patch:0") != nil {
		t.Fatal("a weedy Patch again is forgotten")
	}
	// Someone digging stops a tend; leaving the street lets go.
	r.pact(a, 6, "patch:0", "dig", map[string]any{"do": "start"})
	r.ok(a)
	r.pact(b, 7, "patch:0", "tend", map[string]any{"do": "start"})
	r.no(b, "busy")
	r.send(a, map[string]any{"t": "join", "street": "GLI2V0UN7RD1T2J", "x": 100, "y": 100})
	r.advance(0)
	if p := r.live(oldMainStreet, "patch:0"); p != nil {
		t.Fatalf("leaving kept the dig: %+v", p)
	}
	r.drain(b)
	r.pact(b, 8, "patch:0", "tend", map[string]any{"do": "start"})
	r.ok(b)
	r.pact(b, 9, "patch:0", "tend", map[string]any{"do": "stop"})
	r.ok(b)
	if r.live(oldMainStreet, "patch:0") != nil {
		t.Fatal("a stopped tend keeps the lock")
	}
}

func (r *patchRig) frames(cl *Client, kind string) []map[string]any {
	var out []map[string]any
	for _, m := range r.drain(cl) {
		if m["t"] == kind {
			out = append(out, m)
		}
	}
	return out
}

func TestPatchDigMakesOneHoleAndGrantsEarth(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:1")
	b := r.at(2, "Bo", oldMainStreet, "patch:1")
	r.pact(a, 1, "patch:1", "dig", map[string]any{"do": "start"})
	r.ok(a)
	r.pact(b, 1, "patch:1", "dig", map[string]any{"do": "start"})
	if ok := r.ok(b); fmt.Sprint(ok["diggers"]) != "[1 2]" {
		t.Fatalf("several may dig: %v", ok)
	}
	r.pact(a, 2, "patch:1", "dig", map[string]any{"skill": "soil_appreciation_2"})
	ok := r.ok(a)
	gr := ok["grant"].(map[string]any)
	if gr["c"] != "earth" || gr["n"] != float64(3) || ok["plot"].(map[string]any)["stage"] != "dug" {
		t.Fatalf("the dig answered %v", ok)
	}
	// The first dig to land made the hole: Bo's lands on a hole.
	r.drain(b)
	r.pact(b, 2, "patch:1", "dig", nil)
	if no := r.no(b, "dug"); no["message"] != patchHole {
		t.Fatal(no)
	}
	// A Loam drop: no Earth receipt (the page's, through the budget).
	r.walk(a, "patch:2")
	r.pact(a, 3, "patch:2", "dig", map[string]any{"drop": true})
	if ok := r.ok(a); ok["grant"] != nil {
		t.Fatalf("a Loam dig granted %v", ok)
	}
	// The hole fills in 10 minutes.
	r.advance(10 * time.Minute)
	if r.live(oldMainStreet, "patch:1") != nil {
		t.Fatal("the hole never filled")
	}
	if len(r.g.grants) != 1 {
		t.Fatalf("grants %+v", r.g.grants)
	}
}

func TestPatchPlantRules(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	r.ledger.give(1, "bean_spice", 2)
	r.ledger.give(1, "bean_bean", 1)
	r.ledger.give(1, "bean_egg", 1)
	r.pact(a, 1, "patch:0", "plant", map[string]any{"bean": "bean_spice"})
	if no := r.no(a, "weeds"); no["message"] != patchWeeds {
		t.Fatal(no)
	}
	r.walk(a, "patch:1")
	r.pact(a, 2, "patch:1", "dig", nil)
	r.ok(a)
	r.pact(a, 3, "patch:1", "plant", map[string]any{"bean": "bean_spice"})
	r.no(a, "hole")
	r.walk(a, "patch:0")
	r.pact(a, 4, "patch:0", "tend", nil)
	r.ok(a)
	// Hub 27 grows Bubble, Gas and Spice; a Bean Tree Bean never, an Egg Plant Bean never on a Patch.
	r.pact(a, 5, "patch:0", "plant", map[string]any{"bean": "bean_bean"})
	if no := r.no(a, "wrong_bean"); no["message"] != patchWrongBean {
		t.Fatal(no)
	}
	r.pact(a, 6, "patch:0", "plant", map[string]any{"bean": "bean_egg"})
	r.no(a, "wrong_bean")
	r.pact(a, 7, "patch:0", "plant", map[string]any{"bean": "bean_plain"})
	r.no(a, "no_item")
	r.pact(a, 8, "patch:0", "plant", map[string]any{"bean": "bean_spice", "grow": 3})
	ok := r.ok(a)
	s := ok["plot"].(map[string]any)["seedling"].(map[string]any)
	now := float64(r.now.UnixMilli())
	if s["tree"] != "trant_spice" || s["planter"] != float64(1) || s["minAt"] != now+180000 || s["maxAt"] != now+14_400_000 || ok["use"] == nil {
		t.Fatalf("the plant answered %v", ok)
	}
	if r.ledger.held[1]["bean_spice"] != 1 {
		t.Fatalf("the ledger took %v", r.ledger.held[1])
	}
	// Planted: no plant, tend or dig.
	r.pact(a, 9, "patch:0", "plant", map[string]any{"bean": "bean_spice"})
	if no := r.no(a, "planted"); no["message"] != patchSeedPlant {
		t.Fatal(no)
	}
	r.pact(a, 10, "patch:0", "dig", nil)
	if no := r.no(a, "planted"); no["message"] != patchSeedDig {
		t.Fatal(no)
	}
	r.pact(a, 11, "patch:0", "tend", nil)
	r.no(a, "planted")
	// No bean in the ledger, and the site down.
	r.walk(a, "patch:2")
	r.pact(a, 12, "patch:2", "tend", nil)
	r.ok(a)
	r.pact(a, 13, "patch:2", "plant", map[string]any{"bean": "bean_gas"})
	if no := r.no(a, "no_item"); no["message"] != patchNoBean {
		t.Fatal(no)
	}
	r.ledger.give(1, "bean_gas", 1)
	r.ledger.down = true
	r.pact(a, 14, "patch:2", "plant", map[string]any{"bean": "bean_gas"})
	r.no(a, "no_answer")
	if p := r.live(oldMainStreet, "patch:2"); p.stage != "clean" {
		t.Fatalf("a refused plant changed the plot: %+v", p)
	}
}

func TestPatchDarkPatchTakesOnlyEggPlants(t *testing.T) {
	r := newPatchRig(t, false)
	r.shareAll()
	const wali = "GIF2AUKEAT72DU9" // Wali Hoppers, Salatu (93): one Dark Patch
	a := r.at(1, "Ada", wali, "patch_dark:0")
	r.ledger.give(1, "bean_bean", 1)
	r.ledger.give(1, "bean_egg", 1)
	r.pact(a, 1, "patch_dark:0", "tend", nil)
	r.ok(a)
	r.pact(a, 2, "patch_dark:0", "plant", map[string]any{"bean": "bean_bean"})
	r.no(a, "wrong_bean")
	r.pact(a, 3, "patch_dark:0", "plant", map[string]any{"bean": "bean_egg"})
	r.ok(a)
	// Forgotten for 4 hours, it withers back to a Dark Patch.
	r.advance(4 * time.Hour)
	if p := r.live(wali, "patch_dark:0"); p != nil {
		t.Fatalf("the Egg Plant Seedling left %+v", p)
	}
}

func TestPatchTwoPlantersOneSeedlingOneBean(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	b := r.at(2, "Bo", oldMainStreet, "patch:0")
	r.ledger.give(1, "bean_spice", 1)
	r.ledger.give(2, "bean_gas", 1)
	r.pact(a, 1, "patch:0", "tend", nil)
	r.ok(a)
	r.drain(b)
	r.deferLedger()
	r.pact(a, 2, "patch:0", "plant", map[string]any{"bean": "bean_spice"})
	r.pact(b, 2, "patch:0", "plant", map[string]any{"bean": "bean_gas"})
	r.no(b, "busy")
	r.flush()
	if ok := r.ok(a); ok["use"] == nil {
		t.Fatal(ok)
	}
	if r.ledger.held[1]["bean_spice"] != 0 || r.ledger.held[2]["bean_gas"] != 1 {
		t.Fatalf("the beans taken: %v", r.ledger.held)
	}
	if p := r.live(oldMainStreet, "patch:0"); p.seed.tree != "trant_spice" || p.seed.planter != 1 {
		t.Fatalf("the Seedling is %+v", p.seed)
	}
	r.drain(b)
	r.pact(b, 3, "patch:0", "plant", map[string]any{"bean": "bean_gas"})
	r.no(b, "planted")
}

func TestPatchSeedlingPettingsGrowthAndTheFourth(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	b := r.at(2, "Bo", oldMainStreet, "patch:0")
	c := r.at(3, "Cy", oldMainStreet, "patch:0")
	r.seedling(a, "patch:0", "bean_spice")
	r.drain(b)
	r.drain(c)
	r.pact(b, 1, "patch:0", "pet", nil)
	if ok := r.ok(b); ok["petted"] != float64(1) || ok["first"] != true || ok["third"] != nil {
		t.Fatal(ok)
	}
	r.pact(b, 2, "patch:0", "pet", nil)
	if ok := r.ok(b); ok["petted"] != float64(2) || ok["first"] != nil {
		t.Fatalf("a second petting by the same player is not a first: %v", ok)
	}
	r.drain(a)
	r.pact(a, 3, "patch:0", "pet", nil)
	ok := r.ok(a)
	if ok["third"] != true || ok["owner"] != true || ok["first"] != true || ok["grew"] != nil {
		t.Fatalf("the planter's third petting before 5 minutes answered %v", ok)
	}
	r.drain(c)
	r.pact(c, 4, "patch:0", "pet", nil)
	if no := r.no(c, "enough"); no["message"] != patchEnough {
		t.Fatal(no)
	}
	// It grows for everyone at the 5-minute mark: maturity 1, health 7, no fruit.
	r.drain(a)
	r.drain(b)
	r.advance(5 * time.Minute)
	for _, cl := range []*Client{a, b, c} {
		fr := r.frames(cl, "patches")
		if len(fr) != 1 {
			t.Fatalf("the growth was told as %v", fr)
		}
		tr := plotOf(t, fr[0], "patch:0")["tree"].(map[string]any)
		if tr["class"] != "trant_spice" || tr["maturity"] != float64(1) || tr["health"] != float64(7) || tr["fruit"] != float64(0) {
			t.Fatalf("the tree is %v", tr)
		}
	}
}

func TestPatchSeedlingThirdPettingAfterTheEarliestGrowsAtOnceAtTheGrowLevel(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	r.ledger.give(1, "bean_bubble", 1)
	r.pact(a, 1, "patch:0", "tend", nil)
	r.ok(a)
	r.pact(a, 2, "patch:0", "plant", map[string]any{"bean": "bean_bubble", "grow": 4})
	r.ok(a)
	r.advance(time.Minute)
	b := r.at(2, "Bo", oldMainStreet, "patch:0")
	for i := 0; i < 2; i++ {
		r.pact(b, 3+i, "patch:0", "pet", nil)
		r.ok(b)
	}
	r.pact(b, 5, "patch:0", "pet", nil)
	ok := r.ok(b)
	if ok["grew"] != true || ok["owner"] != nil || plotOf(t, map[string]any{"plots": map[string]any{"x": ok["plot"]}}, "x")["tree"].(map[string]any)["maturity"] != float64(4) {
		t.Fatalf("a third petting past the earliest time answered %v", ok)
	}
}

func TestPatchSeedlingWithersAfterFourHours(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	r.seedling(a, "patch:0", "bean_gas")
	r.pact(a, 1, "patch:0", "pet", nil)
	r.ok(a)
	r.advance(4*time.Hour - time.Second)
	if p := r.live(oldMainStreet, "patch:0"); p == nil || p.stage != "seedling" {
		t.Fatal("it withered early")
	}
	r.advance(time.Second)
	if p := r.live(oldMainStreet, "patch:0"); p != nil {
		t.Fatalf("after 4 h with one petting it is %+v", p)
	}
}

func TestPatchTreeCareOnceADayHarvestPerPlayer(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	b := r.at(2, "Bo", oldMainStreet, "patch:0")
	tr := r.tree(a, "patch:0", "bean_spice")
	r.pact(a, 1, "patch:0", "harvest", nil)
	if no := r.no(a, "small"); no["message"] != "This tree is too small to harvest." {
		t.Fatal(no)
	}
	tr.maturity, tr.health = 5, 7
	tr.cap = r.g.ow.capAt("trant_spice", 5, 7)
	tr.fruit = 0
	r.pact(a, 2, "patch:0", "harvest", nil)
	r.no(a, "empty")
	r.drain(b)
	r.g.roll = func() float64 { return 0.7 }
	r.pact(b, 3, "patch:0", "pet", nil)
	if ok := r.ok(b); ok["success"] != false || tr.petted {
		t.Fatalf("roll 0.7 against no skill's 60%% fails, and the window stays open: %v", ok)
	}
	r.pact(b, 4, "patch:0", "pet", map[string]any{"skill": "light_green_thumb_1"})
	r.ok(b)
	r.pact(a, 5, "patch:0", "pet", nil)
	if no := r.no(a, "not_wanted"); no["message"] != "This tree was already petted today." {
		t.Fatal(no)
	}
	r.drain(b)
	r.pact(a, 6, "patch:0", "water", nil)
	ok := r.ok(a)
	if ok["success"] != true || ok["grew"].(float64) <= 0 {
		t.Fatalf("pet and water together lift health and regrow fruit: %v", ok)
	}
	if tr.health != 8 || tr.fruit != min(r.g.ow.capAt("trant_spice", 5, 8), r.g.ow.prodAt("trant_spice", 5, 8)) {
		t.Fatalf("the tree is %+v", tr)
	}
	before := tr.fruit
	r.pact(a, 7, "patch:0", "harvest", nil)
	ok = r.ok(a)
	if ok["take"] != float64(3) || ok["harvests"] != float64(1) || ok["limit"] != float64(1) || ok["grant"].(map[string]any)["c"] != "all_spice" {
		t.Fatalf("a harvest answered %v", ok)
	}
	r.pact(a, 8, "patch:0", "harvest", nil)
	if no := r.no(a, "daily_limit"); no["message"] != "You can only harvest from each tree 1 time per game day." {
		t.Fatal(no)
	}
	// What Ada took is gone for Bo, and Bo has a harvest of his own.
	r.drain(b)
	r.pact(b, 9, "patch:0", "harvest", map[string]any{"skill": "gardening_3"})
	ok = r.ok(b)
	if want := min(12, before-3); ok["take"] != float64(want) || ok["limit"] != float64(2) {
		t.Fatalf("Bo's harvest answered %v (fruit was %d)", ok, before)
	}
	// A new game day: a new tending window, the harvests again.
	day := gameEpochMs + (r.g.today()+1)*gameDayMs
	r.advance(time.Duration(day-r.now.UnixMilli()+100) * time.Millisecond)
	if tr.petted || tr.watered || len(tr.harvests) != 0 {
		t.Fatalf("the day turn left %+v", tr)
	}
}

func TestPatchTreeGrowthFixtures(t *testing.T) {
	// testdata/patch-growth.json: maturity rolls from a fixed roll sequence, shared with the game's test.
	raw, err := os.ReadFile("testdata/patch-growth.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string    `json:"name"`
		Tree   string    `json:"tree"`
		Mat    int       `json:"maturity"`
		Health int       `json:"health"`
		At     int64     `json:"at"`
		Rolls  []float64 `json:"rolls"`
		Expect struct {
			Mat     int   `json:"maturity"`
			CycleAt int64 `json:"cycleAt"`
			Rolls   int   `json:"rolls"`
		} `json:"expect"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	r := newPatchRig(t, false)
	base := r.now.UnixMilli()
	for _, c := range cases {
		used := 0
		r.g.roll = func() float64 {
			if used >= len(c.Rolls) {
				return 0.99
			}
			used++
			return c.Rolls[used-1]
		}
		tr := &pTree{class: c.Tree, grownAt: base, cycleAt: base}
		tr.maturity, tr.health, tr.day = c.Mat, c.Health, r.g.dayOf(base)
		p := &pPlot{spot: r.spot(oldMainStreet, "patch:0"), kind: "patch", stage: "tree", tree: tr}
		r.g.advance(oldMainStreet, p, base+c.At)
		if tr.maturity != c.Expect.Mat || tr.cycleAt-base != c.Expect.CycleAt || used != c.Expect.Rolls {
			t.Errorf("%s: maturity %d cycleAt +%d rolls %d, want %+v", c.Name, tr.maturity, tr.cycleAt-base, used, c.Expect)
		}
	}
}

func TestPatchPoisonAntidoteDeathAndFirstClearWins(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	b := r.at(2, "Bo", oldMainStreet, "patch:0")
	r.tree(a, "patch:0", "bean_spice")
	r.drain(b)
	r.pact(b, 1, "patch:0", "poison", nil)
	r.no(b, "no_item")
	r.ledger.give(2, usePoison, 2)
	r.ledger.give(1, useAntidote, 1)
	r.pact(b, 2, "patch:0", "antidote", nil)
	r.no(b, "not_poisoned")
	r.pact(b, 3, "patch:0", "poison", nil)
	if ok := r.ok(b); ok["poison"] != float64(180000) || ok["use"] == nil {
		t.Fatal(ok)
	}
	r.pact(b, 4, "patch:0", "poison", nil)
	r.no(b, "poisoned")
	r.drain(a)
	r.advance(time.Minute)
	r.pact(a, 5, "patch:0", "antidote", nil)
	r.ok(a)
	r.advance(3 * time.Minute)
	if p := r.live(oldMainStreet, "patch:0"); p.stage != "tree" {
		t.Fatal("the antidote did not stop the clock")
	}
	r.pact(b, 6, "patch:0", "poison", map[string]any{"ms": 5000}) // not dev: the full 3 minutes
	if ok := r.ok(b); ok["poison"] != float64(180000) {
		t.Fatal(ok)
	}
	r.drain(a)
	r.advance(3 * time.Minute)
	fr := r.frames(a, "patches")
	if len(fr) != 1 || plotOf(t, fr[0], "patch:0")["dead"] != "trant_spice_dead" || plotOf(t, fr[0], "patch:0")["stage"] != "dead" {
		t.Fatalf("the death was told as %v", fr)
	}
	// Two clears: the first wins its 25 Planks and leaves a weedy Patch; the second hears nothing's left.
	r.drain(b)
	r.pact(a, 7, "patch:0", "clear", nil)
	ok := r.ok(a)
	if ok["grant"].(map[string]any)["n"] != float64(25) || ok["plot"].(map[string]any)["stage"] != "dirty" {
		t.Fatal(ok)
	}
	r.drain(b)
	r.pact(b, 8, "patch:0", "clear", nil)
	if no := r.no(b, "nothing"); no["message"] != patchNothingHere {
		t.Fatal(no)
	}
	// The street's own Dead Spice Plant starts dead for everyone.
	c := r.at(3, "Cy", oldMainStreet, "trant_spice_dead:0")
	r.pact(c, 9, "trant_spice_dead:0", "clear", nil)
	if ok := r.ok(c); ok["before"].(map[string]any)["stage"] != "dead" || ok["plot"].(map[string]any)["kind"] != "patch" {
		t.Fatal(ok)
	}
	r.pact(c, 10, "trant_spice_dead:0", "tend", nil)
	r.ok(c)
	d := r.at(4, "Di", oldMainStreet, "patch:3")
	r.pact(d, 11, "patch:3", "poison", nil)
	r.no(d, "not_tree")
}

func TestPatchWoodTreeCareHarvestAndBackToAPatch(t *testing.T) {
	r := newPatchRig(t, false)
	r.shareAll()
	const doon = "GA9S90EJ0G8241N" // a Bean street in hub 56 or near: any street whose hub grows Wood Trees
	st := r.g.w.streets[doon]
	if st == nil || !r.g.w.canPlant("wood_tree", "patch", st.hub) {
		t.Skip("the fixture street changed")
	}
	a := r.at(1, "Ada", doon, "patch:0")
	r.ledger.give(1, "bean_wood", 1)
	r.pact(a, 1, "patch:0", "tend", nil)
	r.ok(a)
	r.pact(a, 2, "patch:0", "plant", map[string]any{"bean": "bean_wood"})
	r.ok(a)
	for i := 0; i < 3; i++ {
		r.pact(a, 3+i, "patch:0", "pet", nil)
		r.ok(a)
	}
	r.advance(5 * time.Minute)
	p := r.live(doon, "patch:0")
	if p.stage != "tree" || !p.tree.wood() || p.tree.maturity != 1 {
		t.Fatalf("the Wood Tree is %+v", p)
	}
	tr := p.tree
	// maturityMap[0] = 2 pettings and waterings: two days of care lift it to maturity 2.
	for day := 0; day < 2; day++ {
		r.pact(a, 10+day*2, "patch:0", "pet", nil)
		r.ok(a)
		r.pact(a, 11+day*2, "patch:0", "water", nil)
		ok := r.ok(a)
		if day == 1 && ok["matured"] != true {
			t.Fatalf("the second day's care answered %v", ok)
		}
		next := gameEpochMs + (r.g.today()+1)*gameDayMs
		r.advance(time.Duration(next-r.now.UnixMilli()+100) * time.Millisecond)
	}
	if tr.maturity != 2 || tr.woodPetted != 0 {
		t.Fatalf("after two days of care: %+v", tr)
	}
	r.pact(a, 20, "patch:0", "harvest", nil)
	ok := r.ok(a)
	if g := ok["grant"].(map[string]any); g["c"] != "plank" || g["n"] != float64(30) {
		t.Fatal(ok)
	}
	r.pact(a, 21, "patch:0", "harvest", nil)
	r.no(a, "daily_limit")
	b := r.at(2, "Bo", doon, "patch:0")
	r.pact(b, 22, "patch:0", "harvest", nil)
	r.ok(b) // maturity 1 -> 0: cleared
	if tr.maturity != 0 || tr.clearedAt == 0 {
		t.Fatalf("a wee tree's last harvest left %+v", tr)
	}
	r.advance(2 * time.Second)
	if r.live(doon, "patch:0") != nil {
		t.Fatal("a Wood Tree harvested down to 0 is a weedy Patch 2 s later")
	}
}

func TestPatchADeadStreetTreeBecomesASharedPlot(t *testing.T) {
	r := newPatchRig(t, true)
	r.shareAll()
	const st = "GCR29V8S4Q12IF4" // a fruit tree (tree:0) and a Wood Tree (tree:1), and Patches
	tree0 := sharedWorld.streets[st]["tree:0"]
	a := r.player(1, "Ada", st, tree0.x, tree0.y)
	ob := r.mod.get(st, tree0)
	r.mod.killTree(st, ob, 0)
	p := r.live(st, "tree:0")
	if p == nil || !p.adopted || p.stage != "dead" || p.dead != "trant_fruit_dead" || p.spot.y != tree0.y-4 {
		t.Fatalf("the plot is %+v", p)
	}
	fr := r.frames(a, "patches")
	if len(fr) != 1 || plotOf(t, fr[0], "tree:0")["x"] != tree0.x {
		t.Fatalf("the street heard %v", fr)
	}
	r.pact(a, 1, "tree:0", "clear", nil)
	if ok := r.ok(a); ok["plot"].(map[string]any)["stage"] != "dirty" {
		t.Fatal(ok)
	}
	if r.live(st, "tree:0") == nil {
		t.Fatal("a tree:<i> plot is kept even weedy")
	}
	wood := sharedWorld.streets[st]["tree:1"]
	r.mod.killTree(st, r.mod.get(st, wood), 0)
	if p := r.live(st, "tree:1"); p == nil || p.stage != "dirty" || p.spot.class != "patch" {
		t.Fatalf("a dead Wood Tree's plot is %+v", p)
	}
	// On a street whose Patches are not shared, nothing is adopted.
	r.g.w = patchWorldData
	const other = "GA9KL58B1D82CDV"
	t0 := sharedWorld.streets[other]["tree:0"]
	r.mod.killTree(other, r.mod.get(other, t0), 0)
	if r.g.plots[other] != nil {
		t.Fatal("a street off the pilot adopted a plot")
	}
}

func TestPatchGrantsAreHeldUntilAcked(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "trant_spice_dead:0")
	r.pact(a, 1, "trant_spice_dead:0", "clear", nil)
	id := r.ok(a)["grant"].(map[string]any)["id"].(string)
	// A reconnect hears it again.
	b := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	if gr := r.only(b, "patch_grant"); gr["id"] != id || gr["c"] != "plank" || gr["n"] != float64(25) {
		t.Fatalf("the reconnect heard %v", gr)
	}
	r.send(b, map[string]any{"t": "patch_ack", "id": id})
	if len(r.g.grants) != 0 {
		t.Fatal("the ack did not clear the grant")
	}
}

func TestPatchSlice2StubsSayLater(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	r.tree(a, "patch:0", "bean_spice")
	r.pact(a, 1, "patch:0", "dust", nil)
	if no := r.no(a, "later"); no["message"] != patchLater {
		t.Fatal(no)
	}
	r.pact(a, 2, "patch:0", "trantsform", nil)
	r.no(a, "later")
	if fn := r.hub.ProvidedLocked("patches.rookTree").(func(string, string) bool); fn(oldMainStreet, "patch:0") {
		t.Fatal("the Rook strips no Patch tree in slice 1")
	}
	if fn := r.hub.ProvidedLocked("patches.dust").(func(string) []string); fn(oldMainStreet) != nil {
		t.Fatal("Fertilidust reaches no Patch tree in slice 1")
	}
}

func TestPatchRateLimit(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	for i := 0; i < 6; i++ {
		r.send(a, map[string]any{"t": "patch_act", "req": i + 1, "id": "patch:0", "verb": "dust"})
		r.no(a, "later")
	}
	r.send(a, map[string]any{"t": "patch_act", "req": 7, "id": "patch:0", "verb": "dust"})
	r.no(a, "slow_down")
}

func TestPatchDevTimersAndRoll(t *testing.T) {
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	r.send(a, map[string]any{"t": "patch_dev", "clean": 5000})
	if m := r.only(a, "error"); m["code"] != "bad_frame" {
		t.Fatalf("patch_dev without GRODDLE_PATCHES_DEV answered %v", m)
	}
	r.g.dev = true
	roll := 0.0
	r.send(a, map[string]any{"t": "patch_dev", "clean": 5000, "cycle": 2000, "roll": roll})
	if m := r.only(a, "patch_dev_ok"); m["timers"].(map[string]any)["clean"] != float64(5000) || m["roll"] != float64(0) {
		t.Fatal(m)
	}
	tr := r.tree(a, "patch:0", "bean_spice")
	r.advance(4 * time.Second)
	if tr.maturity != 3 {
		t.Fatalf("a forced roll and 2 s cycles: maturity %d", tr.maturity)
	}
}

func TestPatchRestartKeepsPlotsParksUnknownKeysAndGivesASeedlingAFreshHour(t *testing.T) {
	dir := t.TempDir()
	patchesFileForTest = filepath.Join(dir, "realtime-patches.json")
	defer func() { patchesFileForTest = "" }()
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	r.seedling(a, "patch:0", "bean_spice") // planted: written at once
	cy := r.at(3, "Cy", oldMainStreet, "patch:1")
	tr := r.tree(cy, "patch:1", "bean_gas")
	tr.maturity, tr.health = 5, 9
	r.ledger.give(3, usePoison, 1)
	r.pact(cy, 1, "patch:1", "poison", nil)
	r.ok(cy)
	r.g.save()
	raw, _ := os.ReadFile(patchesFileForTest)
	var f patchFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	// A key the layout no longer has (a placement change), kept by hand.
	f.Streets[oldMainStreet]["patch:7"] = &patchRec{Class: "patch", Kind: "patch", Stage: "tree", Tree: &patchTreeRec{Class: "trant_gas", Maturity: 9, Health: 9}}
	b, _ := json.Marshal(f)
	os.WriteFile(patchesFileForTest, b, 0o600)

	// Down 5 hours: the Seedling's 4 hours ran out meanwhile, the poison too, the tree grew.
	saved := r.now
	r2 := newPatchRig(t, false)
	r2.now = saved.Add(5 * time.Hour)
	if r2.g.parked[oldMainStreet]["patch:7"] == nil {
		t.Fatal("the unknown key was not parked")
	}
	c := r2.at(2, "Bo", oldMainStreet, "patch:0")
	r2.send(c, map[string]any{"t": "patch_sync"})
	snap := r2.only(c, "patches")
	s := plotOf(t, snap, "patch:0")
	if s["stage"] != "seedling" || s["seedling"].(map[string]any)["maxAt"] != float64(r2.now.UnixMilli()+3_600_000) {
		t.Fatalf("the Seedling after the downtime is %v", s)
	}
	if p := plotOf(t, snap, "patch:1"); p["stage"] != "dead" || p["dead"] != "trant_gas_dead" {
		t.Fatalf("the poisoned tree is %v", p)
	}
	r2.g.save()
	raw, _ = os.ReadFile(patchesFileForTest)
	if !strings.Contains(string(raw), `"parked"`) || !strings.Contains(string(raw), `"patch:7"`) {
		t.Fatalf("the parked plot was not written back: %s", raw)
	}

	// A torn file is moved aside and the module starts empty.
	os.WriteFile(patchesFileForTest, []byte(`{"v":1,"streets":{`), 0o600)
	r3 := newPatchRig(t, false)
	if len(r3.g.plots) != 0 || r3.g.path == "" {
		t.Fatal("a torn file did not start empty")
	}
	matches, _ := filepath.Glob(patchesFileForTest + ".bad-*")
	if len(matches) != 1 {
		t.Fatalf("aside: %v", matches)
	}
}

func TestPatchRestartMidGrowthCatchesUp(t *testing.T) {
	dir := t.TempDir()
	patchesFileForTest = filepath.Join(dir, "realtime-patches.json")
	defer func() { patchesFileForTest = "" }()
	r := newPatchRig(t, false)
	a := r.at(1, "Ada", oldMainStreet, "patch:0")
	tr := r.tree(a, "patch:0", "bean_bubble")
	tr.health = 10
	r.g.save()
	saved := r.now
	r2 := newPatchRig(t, false)
	r2.now = saved.Add(35 * time.Minute) // maturity 1 -> 2 at 10 min (chance 1.0), then 2 -> 3 at 30 min
	r2.g.roll = func() float64 { return 0.5 }
	c := r2.at(2, "Bo", oldMainStreet, "patch:0")
	r2.send(c, map[string]any{"t": "patch_sync"})
	if m := plotOf(t, r2.only(c, "patches"), "patch:0")["tree"].(map[string]any)["maturity"]; m != float64(3) {
		t.Fatalf("after 35 minutes down the tree is at %v", m)
	}
}
