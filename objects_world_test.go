package main

import (
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// notPigs is a snapshot's objects other than the street's Piggies.
func notPigs(snap map[string]any) []any {
	var out []any
	list, _ := snap["objs"].([]any)
	for _, raw := range list {
		if o, ok := raw.(map[string]any); ok && !strings.HasPrefix(o["id"].(string), "pig:") {
			out = append(out, raw)
		}
	}
	return out
}

// frames returns every frame of one kind queued for cl.
func (r *objRig) frames(cl *Client, kind string) []map[string]any {
	var got []map[string]any
	for _, m := range r.drain(cl) {
		if m["t"] == kind {
			got = append(got, m)
		}
	}
	return got
}

// credited records what the objects module hands the credits module.
func (r *objRig) credited() *[]string {
	var got []string
	r.hub.provided["credits.add"] = func(user int64, why string, g tradeGoods) string {
		parts := []string{}
		for _, it := range g.Items {
			parts = append(parts, it.C+"x"+strconv.Itoa(it.N))
		}
		got = append(got, strconv.FormatInt(user, 10)+":"+why+":"+strings.Join(parts, "+"))
		return "cr-test"
	}
	return &got
}

// ---------------------------------------------------------------------------
// Tree death
// ---------------------------------------------------------------------------

func TestPoisonKillsAStreetTreeForEveryone(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["tree:0"] // trant_fruit
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	b := r.player(2, "Bo", defaultStreet, spot.x+50, spot.y)
	r.act(a, 1, "tree:0", "poison", nil)
	ok := r.only(a, "obj_ok")
	if num(ok["poison"]) != 180000 || num(objOf(ok, "tree:0")["poison"]) != 180000 || num(objOf(ok, "tree:0")["by"]) != 1 {
		t.Fatalf("the pour answered %v", ok)
	}
	if got := objOf(r.only(b, "objs"), "tree:0"); got == nil || num(got["poison"]) != 180000 {
		t.Fatalf("Bo heard %v", got)
	}
	r.now = r.now.Add(time.Second)
	r.act(b, 1, "tree:0", "poison", nil)
	if no := r.only(b, "obj_no"); no["code"] != "poisoned" {
		t.Fatalf("a second poison answered %v", no)
	}
	// Bo's antidote saves it for everyone.
	r.act(b, 2, "tree:0", "antidote", nil)
	r.only(b, "obj_ok")
	if got := objOf(r.only(a, "objs"), "tree:0"); got == nil || got["poison"] != nil {
		t.Fatalf("Ada heard the cure as %v", got)
	}
	r.advance(4 * time.Minute)
	if f := r.frames(a, "objs"); len(f) != 0 {
		t.Fatalf("a cured tree still died: %v", f)
	}
	r.act(a, 2, "tree:0", "antidote", nil)
	if no := r.only(a, "obj_no"); no["code"] != "not_poisoned" || no["message"] != noPoisonedTree {
		t.Fatalf("an antidote on a healthy tree answered %v", no)
	}
	// Poisoned again, nobody cures it: three minutes on it is dead, for Ada, Bo and anyone later.
	r.now = r.now.Add(time.Second)
	r.act(a, 3, "tree:0", "poison", nil)
	r.drain(a)
	r.drain(b)
	r.advance(2 * time.Minute)
	if f := r.frames(b, "objs"); len(f) != 0 {
		t.Fatalf("it died early: %v", f)
	}
	r.advance(time.Minute + time.Second)
	dead := objOf(r.only(b, "objs"), "tree:0")
	if dead == nil || dead["dead"] != true || num(dead["by"]) != 1 || num(dead["fruit"]) != 0 {
		t.Fatalf("Bo saw the death as %v", dead)
	}
	r.drain(a)
	for i, verb := range []string{"harvest", "pet", "water", "poison", "antidote"} {
		r.now = r.now.Add(time.Second)
		r.act(a, 10+i, "tree:0", verb, map[string]any{"skill": "no_skill"})
		if no := r.only(a, "obj_no"); no["code"] != "dead" {
			t.Fatalf("%s on a dead tree answered %v", verb, no)
		}
	}
	c := r.player(3, "Cy", defaultStreet, 0, 0)
	r.send(c, map[string]any{"t": "obj_sync"})
	if got := objOf(r.only(c, "objs"), "tree:0"); got == nil || got["dead"] != true {
		t.Fatalf("Cy, arriving later, synced %v", got)
	}
	// A new game day does not bring it back.
	r.advance(5 * time.Hour)
	r.drain(c)
	r.send(c, map[string]any{"t": "obj_sync"})
	if got := objOf(r.only(c, "objs"), "tree:0"); got == nil || got["dead"] != true || num(got["fruit"]) != 0 {
		t.Fatalf("after a day it reads %v", got)
	}
	if fn, _ := r.hub.ProvidedLocked("objects.treeClass").(func(string, string) string); fn(defaultStreet, "tree:0") != "" {
		t.Fatal("the Rook can still pair a dead tree")
	}
}

func TestAPoisonedWoodTreeLeavesItsPoisonerThePlanks(t *testing.T) {
	r := newObjRig(t)
	got := r.credited()
	spot := sharedWorld.streets[defaultStreet]["tree:7"] // wood_tree
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	b := r.player(2, "Bo", defaultStreet, spot.x, spot.y)
	r.act(a, 1, "tree:7", "poison", nil)
	r.drain(a)
	r.drain(b)
	r.advance(treePoison)
	if dead := objOf(r.only(b, "objs"), "tree:7"); dead == nil || dead["dead"] != true || num(dead["by"]) != 1 {
		t.Fatalf("Bo saw %v", dead)
	}
	if strings.Join(*got, ",") != "1:take:plankx25" {
		t.Fatalf("credited %v", *got)
	}
	r.act(b, 1, "tree:7", "harvest", nil)
	if no := r.only(b, "obj_no"); no["code"] != "dead" {
		t.Fatalf("a dead Wood Tree's harvest answered %v", no)
	}
}

func TestDevPoisonTimeOnlyWithTheFlag(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["tree:1"]
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	r.act(a, 1, "tree:1", "poison", map[string]any{"ms": 5000})
	if ok := r.only(a, "obj_ok"); num(ok["poison"]) != 180000 {
		t.Fatalf("without the flag the pour lasts %v", ok["poison"])
	}
	r2 := newObjRig(t)
	r2.mod.dev = true
	a2 := r2.player(1, "Ada", defaultStreet, spot.x, spot.y)
	r2.act(a2, 1, "tree:1", "poison", map[string]any{"ms": 5000})
	if ok := r2.only(a2, "obj_ok"); num(ok["poison"]) != 5000 {
		t.Fatalf("with the flag the pour lasts %v", ok["poison"])
	}
	r2.advance(5 * time.Second)
	if dead := objOf(r2.only(a2, "objs"), "tree:1"); dead == nil || dead["dead"] != true {
		t.Fatalf("the dev pour died as %v", dead)
	}
}

func TestTheRookKillsATreeAtHealthOne(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["tree:3"] // maturity 7, health 7
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	strip := r.mod.rookTree
	for _, want := range []int{5, 3, 1} {
		strip(defaultStreet, "tree:3")
		if got := objOf(r.only(a, "objs"), "tree:3"); num(got["health"]) != float64(want) || got["dead"] != nil {
			t.Fatalf("after a rooking want health %d, got %v", want, got)
		}
	}
	if !strip(defaultStreet, "tree:3") {
		t.Fatal("the last rooking did not land")
	}
	if got := objOf(r.only(a, "objs"), "tree:3"); got["dead"] != true || got["by"] != nil {
		t.Fatalf("a rooked tree at health 1 reads %v", got)
	}
	if strip(defaultStreet, "tree:3") {
		t.Fatal("a dead tree was rooked again")
	}
}

// ---------------------------------------------------------------------------
// Help digging
// ---------------------------------------------------------------------------

func dirtStreet(t *testing.T) (string, *objSpot) {
	t.Helper()
	streets := make([]string, 0, len(sharedWorld.streets))
	for tsid := range sharedWorld.streets {
		streets = append(streets, tsid)
	}
	sort.Strings(streets)
	for _, tsid := range streets {
		for _, spot := range sharedWorld.streets[tsid] {
			if spot.class == "dirt_pile" && spot.id == "node:dirt_pile:0" {
				return tsid, spot
			}
		}
	}
	t.Fatal("no Dirt Pile in data/objects.json")
	return "", nil
}

func TestDiggersHelpEachOtherAtADirtPile(t *testing.T) {
	r := newObjRig(t)
	got := r.credited()
	street, spot := dirtStreet(t)
	id := spot.id
	a := r.player(1, "Ada", street, spot.x, spot.y)
	b := r.player(2, "Bo", street, spot.x+40, spot.y)
	c := r.player(3, "Cy", street, spot.x-40, spot.y)
	r.send(a, map[string]any{"t": "obj_dig", "id": id, "do": "start"})
	if m := r.only(a, "obj_dig"); m["helping"] != false {
		t.Fatalf("the first digger is helping: %v", m)
	}
	if diggers := objOf(r.only(b, "objs"), id)["diggers"]; len(diggers.([]any)) != 1 {
		t.Fatalf("Bo sees diggers %v", diggers)
	}
	r.send(b, map[string]any{"t": "obj_dig", "id": id, "do": "start"})
	if m := r.only(b, "obj_dig"); m["helping"] != true {
		t.Fatalf("Bo joining is not helping: %v", m)
	}
	r.drain(a)
	r.drain(c)
	// Ada's dig lands with one helper: one more item, no Loam chance yet (dirt_pile.js 413-424).
	r.act(a, 1, id, "dig", map[string]any{"skill": "soil_appreciation_1"})
	ok := r.only(a, "obj_ok")
	help, _ := ok["help"].(map[string]any)
	if help == nil || num(help["n"]) != 1 || num(help["items"]) != 1 || help["class"] != "earth" || help["loam"] != false {
		t.Fatalf("Ada's dig answered %v", ok)
	}
	if by := help["by"].([]any); len(by) != 1 || by[0] != "Bo" {
		t.Fatalf("the helpers named %v", by)
	}
	if num(ok["roll"]) != 0.5 {
		t.Fatalf("the drop roll %v", ok["roll"])
	}
	// SA1: 2 Earth (bonus_amount) and the helper's 1 (roll 0.5 is past the 3% drop).
	if strings.Join(*got, ",") != "1:take:earthx3" {
		t.Fatalf("credited %v", *got)
	}
	if diggers := objOf(ok, id)["diggers"].([]any); len(diggers) != 1 || num(diggers[0]) != 2 {
		t.Fatalf("after Ada's dig the diggers are %v", diggers)
	}
	// Bo's dig had no helper of its own (Ada was digging first): no bonus.
	r.act(b, 1, id, "dig", nil)
	if ok := r.only(b, "obj_ok"); ok["help"] != nil {
		t.Fatalf("Bo's dig answered %v", ok)
	}
	// Three helpers: two items and the 5% Loam chance, normalised (0.05/0.97 at a roll of 0.01 lands).
	*got = nil
	r.drain(a)
	r.drain(b)
	r.send(a, map[string]any{"t": "obj_dig", "id": id, "do": "start"})
	for i, cl := range []*Client{b, c, r.player(4, "Di", street, spot.x, spot.y)} {
		r.now = r.now.Add(time.Second)
		r.send(cl, map[string]any{"t": "obj_dig", "id": id, "do": "start"})
		_ = i
	}
	rolls := []float64{0.5, 0.01}
	r.mod.roll = func() float64 { v := rolls[0]; rolls = rolls[1:]; return v }
	r.drain(a)
	r.now = r.now.Add(time.Second)
	r.act(a, 2, id, "dig", map[string]any{"skill": "soil_appreciation_2"})
	ok = r.only(a, "obj_ok")
	help = ok["help"].(map[string]any)
	if num(help["n"]) != 3 || num(help["items"]) != 2 || help["class"] != "loam" || help["loam"] != true {
		t.Fatalf("a dig with three helpers answered %v", ok)
	}
	if strings.Join(*got, ",") != "1:take:loamx5" { // SA2's 3 Loam and the 2 bonus Loam
		t.Fatalf("credited %v", *got)
	}
	// A cancel takes the helps back; leaving the street takes the digger off.
	r.mod.roll = func() float64 { return 0.5 }
	r.send(c, map[string]any{"t": "obj_dig", "id": id, "do": "stop"})
	r.send(b, map[string]any{"t": "leave"})
	r.drain(a)
	r.send(a, map[string]any{"t": "obj_sync"})
	if diggers := objOf(r.only(a, "objs"), id)["diggers"].([]any); len(diggers) != 1 || num(diggers[0]) != 4 {
		t.Fatalf("after Cy's cancel and Bo's leaving the diggers are %v", diggers)
	}
}

func TestNoDigCrewOnARockOrAPeatBog(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["rock:1"]
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	r.send(a, map[string]any{"t": "obj_dig", "id": "rock:1", "do": "start"})
	if f := r.drain(a); len(f) != 0 {
		t.Fatalf("a dig at a rock answered %v", f)
	}
}

// ---------------------------------------------------------------------------
// Piggies
// ---------------------------------------------------------------------------

func TestAStreetPiggyIsEveryones(t *testing.T) {
	r := newObjRig(t)
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	b := r.player(2, "Bo", defaultStreet, pig.x+100, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	snap := r.only(a, "objs")
	if p := objOf(snap, "pig:2"); p == nil || num(p["mood"]) != 1 || num(p["hunger"]) != 0 || p["daily"] != true || num(p["in"]) != float64(pigTick.Milliseconds()) {
		t.Fatalf("a fresh Piggy reads %v", p)
	}
	// Nibble before a pet: the source's line.
	r.act(a, 1, "pig:2", "nibble", nil)
	if no := r.only(a, "obj_no"); no["code"] != "needs_pet" || no["message"] != pigNeedsPet {
		t.Fatalf("an early nibble answered %v", no)
	}
	// No skill: a 90% pet, rolled 0.5, lands; mood 2 for Bo too.
	r.now = r.now.Add(time.Second)
	r.act(a, 2, "pig:2", "pet", map[string]any{"skill": "no_skill"})
	ok := r.only(a, "obj_ok")
	if ok["success"] != true || num(objOf(ok, "pig:2")["mood"]) != 2 || objOf(ok, "pig:2")["petted"] != true {
		t.Fatalf("the pet answered %v", ok)
	}
	if p := objOf(r.only(b, "objs"), "pig:2"); num(p["mood"]) != 2 {
		t.Fatalf("Bo sees %v", p)
	}
	r.act(a, 3, "pig:2", "nibble", nil)
	if ok := r.only(a, "obj_ok"); ok["success"] != true {
		t.Fatalf("Ada's nibble after her pet answered %v", ok)
	}
	// Bo has not petted it today: his nibble waits, unless Animal Kinship VI.
	r.now = r.now.Add(time.Second)
	r.act(b, 1, "pig:2", "nibble", nil)
	if no := r.only(b, "obj_no"); no["code"] != "needs_pet" {
		t.Fatalf("Bo's nibble answered %v", no)
	}
	r.act(b, 2, "pig:2", "nibble", map[string]any{"perks": []string{"ak6"}})
	r.only(b, "obj_ok")
	// A failed pet roll (0.95 at 90%) changes nothing.
	r.mod.roll = func() float64 { return 0.95 }
	r.now = r.now.Add(time.Second)
	r.act(b, 3, "pig:2", "pet", nil)
	if ok := r.only(b, "obj_ok"); ok["success"] != false {
		t.Fatalf("a missed pet answered %v", ok)
	}
	r.drain(a)
	// Far off: refused.
	far := r.player(3, "Cy", defaultStreet, pig.x+pigReachX+10, pig.y)
	r.act(far, 1, "pig:2", "pet", nil)
	if no := r.only(far, "obj_no"); no["code"] != "far" {
		t.Fatalf("a pet from far off answered %v", no)
	}
}

func TestAPiggysTwiceDailyTick(t *testing.T) {
	r := newObjRig(t)
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	// Half a day: hunger 1, no mood change (is_daily flips to 0 first).
	r.advance(pigTick + 20*time.Millisecond)
	f := r.frames(a, "objs")
	if len(f) != 1 {
		t.Fatalf("the first tick sent %v", f)
	}
	if p := objOf(f[0], "pig:2"); num(p["mood"]) != 1 || num(p["hunger"]) != 1 || p["daily"] != false {
		t.Fatalf("after half a day %v", p)
	}
	// The whole street's Piggies ticked together (their clocks started at the same sync).
	if n := len(f[0]["objs"].([]any)); n < 6 {
		t.Fatalf("only %d objects moved", n)
	}
	// Ada pets it: the next daily half keeps its mood (PIGGY_PET_PROTECTS_MOOD); hungry Piggies eat fruit.
	r.act(a, 1, "pig:2", "pet", nil)
	r.drain(a)
	r.advance(pigTick + 20*time.Millisecond)
	var p map[string]any
	ate := []string{}
	for _, m := range r.drain(a) {
		if m["t"] == "objs" {
			if got := objOf(m, "pig:2"); got != nil {
				p = got
			}
		}
		if m["t"] == "obj_ev" && m["ev"] == "ate" {
			ate = append(ate, m["id"].(string)+">"+m["on"].(string))
		}
	}
	if num(p["mood"]) != 2 || p["petted"] != false || p["daily"] != true {
		t.Fatalf("after the daily half %v", p)
	}
	// Hunger 2 on its own street's trees: it ate a fruit, as did the others near a tree on their level.
	if num(p["hunger"]) != 0 || len(ate) == 0 {
		t.Fatalf("a hungry Piggy did not eat: %v, ate %v", p, ate)
	}
}

func TestAPiggyEatsTheNearestFruitOnItsLevel(t *testing.T) {
	r := newObjRig(t)
	street := defaultStreet
	r.mod.wakePigs(street)
	ob := r.mod.streets[street]["pig:2"]
	ob.hunger = 3
	tree := r.mod.fruitNear(street, ob.spot)
	if tree == nil {
		t.Fatal("no tree near pig:2")
	}
	before := tree.fruit
	evs := r.mod.pigTwiceDaily(street, ob, []*liveObj{ob})
	if len(evs) != 1 || evs[0]["ev"] != "ate" || evs[0]["on"] != tree.spot.id || tree.fruit != before-1 || ob.hunger != 0 {
		t.Fatalf("events %v, fruit %d -> %d, hunger %d", evs, before, tree.fruit, ob.hunger)
	}
	// Mood 0 with happy neighbours comes back to 1 on the daily half; at 10 it croaks and lives.
	ob.mood, ob.daily, ob.hunger = 1, false, 10
	other := r.mod.streets[street]["pig:3"]
	other.mood = 2
	for id, t2 := range r.mod.streets[street] {
		if strings.HasPrefix(id, "tree:") {
			t2.fruit = 0
		}
	}
	evs = r.mod.pigTwiceDaily(street, ob, []*liveObj{ob, other})
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, e["ev"].(string))
	}
	if ob.mood != 1 || strings.Join(kinds, ",") != "happy,croak" || !ob.croak || ob.hunger != 10 {
		t.Fatalf("mood %d, events %v, croak %v, hunger %d", ob.mood, kinds, ob.croak, ob.hunger)
	}
}

func TestFeedingAndEatingAPiggy(t *testing.T) {
	r := newObjRig(t)
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	ob := r.mod.streets[defaultStreet]["pig:2"]
	ob.hunger = 5
	r.act(a, 1, "pig:2", "nibble", map[string]any{"perks": []string{"ak6"}})
	if no := r.only(a, "obj_no"); no["code"] != "not_in_mood" || no["message"] != pigNotMood {
		t.Fatalf("a hungry Piggy's nibble answered %v", no)
	}
	r.act(a, 2, "pig:2", "feed", nil) // a food under base cost 5: still hungry
	if p := objOf(r.only(a, "obj_ok"), "pig:2"); num(p["hunger"]) != 5 {
		t.Fatalf("cheap food left %v", p)
	}
	r.now = r.now.Add(time.Second)
	r.act(a, 3, "pig:2", "feed", map[string]any{"perks": []string{"sate"}})
	if p := objOf(r.only(a, "obj_ok"), "pig:2"); num(p["hunger"]) != 0 {
		t.Fatalf("sating food left %v", p)
	}
	r.act(a, 4, "pig:2", "eat", nil)
	if no := r.only(a, "obj_no"); no["code"] != "not_hungry" {
		t.Fatalf("a full Piggy eating answered %v", no)
	}
	// It ate tree:4's fruit where Ada sees it: one fruit off the shared tree for everyone.
	ob.hunger = 4
	b := r.player(2, "Bo", defaultStreet, pig.x, pig.y)
	r.now = r.now.Add(time.Second)
	r.act(a, 5, "pig:2", "eat", map[string]any{"on": "tree:4"})
	r.only(a, "obj_ok")
	f := r.frames(b, "objs")
	var tree, p map[string]any
	for _, m := range f {
		if got := objOf(m, "tree:4"); got != nil {
			tree = got
		}
		if got := objOf(m, "pig:2"); got != nil {
			p = got
		}
	}
	if tree == nil || num(tree["fruit"]) != num(tree["cap"])-1 || p == nil || num(p["hunger"]) != 0 {
		t.Fatalf("Bo heard the tree %v and the Piggy %v", tree, p)
	}
	ob.hunger = 4
	r.now = r.now.Add(time.Second)
	r.act(a, 6, "pig:2", "eat", map[string]any{"on": "rock:1"})
	if no := r.only(a, "obj_no"); no["code"] != "unknown" {
		t.Fatalf("eating a rock answered %v", no)
	}
}

func TestASadStreetsPiggiesRefuseAndWait(t *testing.T) {
	r := newObjRig(t)
	// Every sad street's Piggies were the game's invented fallback set, gone since
	// 2026-10-30 (the archive puts no Piggy on a sad street), so this rig's world
	// makes Hechey Track sad: a layout that gains one still meets the rule.
	for tsid := range sharedWorld.sad {
		if sharedWorld.streets[tsid]["pig:0"] != nil {
			t.Fatalf("%s is sad and lays out a Piggy: drop this stand-in and test that street", tsid)
		}
	}
	street := defaultStreet
	pig := sharedWorld.streets[street]["pig:0"]
	if pig == nil {
		t.Fatal("Hechey Track lays out no Piggy")
	}
	r.mod.w = &objWorld{rules: sharedWorld.rules, streets: sharedWorld.streets, sad: map[string]bool{street: true}}
	a := r.player(1, "Ada", street, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	r.act(a, 1, "pig:0", "pet", nil)
	if no := r.only(a, "obj_no"); no["code"] != "sad" || no["message"] != pigSad {
		t.Fatalf("a sad Piggy's pet answered %v", no)
	}
	r.advance(pigTick + 20*time.Millisecond)
	if p := objOf(r.only(a, "objs"), "pig:0"); num(p["hunger"]) != 0 || p["daily"] != true {
		t.Fatalf("a sad Piggy ticked: %v", p)
	}
}

func TestPigDevOnlyWithTheFlag(t *testing.T) {
	r := newObjRig(t)
	pig := sharedWorld.streets[defaultStreet]["pig:4"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	r.send(a, map[string]any{"t": "obj_dev", "id": "pig:4", "hunger": 5})
	if f := r.drain(a); len(f) != 0 || r.mod.streets[defaultStreet]["pig:4"].hunger != 0 {
		t.Fatalf("without the flag obj_dev did %v", f)
	}
	r.mod.dev = true
	r.send(a, map[string]any{"t": "obj_dev", "id": "pig:4", "hunger": 5})
	if p := objOf(r.only(a, "objs"), "pig:4"); num(p["hunger"]) != 5 {
		t.Fatalf("obj_dev set %v", p)
	}
	r.send(a, map[string]any{"t": "obj_dev", "id": "pig:4", "hunger": 2, "tick": true})
	var ate bool
	for _, m := range r.drain(a) {
		if m["t"] == "obj_ev" && m["id"] == "pig:4" && m["ev"] == "ate" && m["on"] == "tree:3" {
			ate = true
		}
	}
	if !ate || r.mod.streets[defaultStreet]["pig:4"].hunger != 0 {
		t.Fatalf("a dev tick at hunger 2 did not eat tree:3's fruit (hunger %d)", r.mod.streets[defaultStreet]["pig:4"].hunger)
	}
}
