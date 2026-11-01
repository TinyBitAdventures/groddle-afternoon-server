package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTheStreetAnimalsAreLaidOut(t *testing.T) {
	counts := map[string]int{}
	fly := 0
	for _, spots := range sharedWorld.streets {
		for _, s := range spots {
			if isCreature(s.kind) {
				counts[s.kind]++
				if s.fly {
					fly++
				}
			}
		}
	}
	// The census (the game's street-ecology.json): 1,547 Chickens, 1,722 Butterflies, 139 Batterflies.
	if counts["chicken"] < 1000 || counts["butterfly"] < 1000 || counts["batterfly"] < 100 || fly == 0 || fly == counts["butterfly"] {
		t.Fatalf("animals laid out: %v, %d flying", counts, fly)
	}
	if b := sharedWorld.streets[defaultStreet]["butterfly:2"]; b == nil || !b.fly || sharedWorld.streets[defaultStreet]["butterfly:0"].fly {
		t.Fatalf("Hechey Track's Butterflies: the resting ones first, then the flyers: %v", b)
	}
	if rows := sharedWorld.rules.Ak["npc_chicken:squeeze"]; len(rows) != 8 || rows[0][0] != 1 || rows[0][1] != 60 || rows[1][0] != 2 {
		t.Fatalf("ak_chicken_squeeze: %v", rows)
	}
	if sharedWorld.rules.Incubate.Hatches["chicken_egg"] != "chick" || sharedWorld.rules.Incubate.Ms != 60000 {
		t.Fatalf("incubation rules: %+v", sharedWorld.rules.Incubate)
	}
	if pigFoods["grain"].Cost != 1 || pigFoods["meat"].Cost < 5 {
		t.Fatalf("food costs: %+v %+v", pigFoods["grain"], pigFoods["meat"])
	}
}

func TestASqueezedChickenIsEveryonesButTheLimitIsEachPlayers(t *testing.T) {
	r := newObjRig(t)
	got := r.credited()
	ch := sharedWorld.streets[defaultStreet]["chicken:0"]
	a := r.player(1, "Ada", defaultStreet, ch.x, ch.y)
	b := r.player(2, "Bo", defaultStreet, ch.x+80, ch.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	snap := r.only(a, "objs")
	if animals, _ := snap["animals"].([]any); snap["creatures"] != true || num(snap["now"]) != float64(r.now.UnixMilli()) || objOf(snap, "chicken:0") != nil || len(animals) != 7 || animals[0] != "butterfly:0" {
		t.Fatalf("a fresh street's snapshot: %v", snap)
	}
	// No skill: a 60% squeeze rolled 0.5 lands, held 2.5 s for everyone, one Grain credited.
	r.act(a, 1, "chicken:0", "squeeze", map[string]any{"skill": "no_skill"})
	ok := r.only(a, "obj_ok")
	c := objOf(ok, "chicken:0")
	if ok["success"] != true || num(ok["uses"]) != 1 || num(ok["limit"]) != 1 || num(c["sq"]) != 1 || num(c["lag"]) != 2500 || num(c["hold"]) != 2500 {
		t.Fatalf("Ada's squeeze answered %v", ok)
	}
	var seen, ev bool
	for _, m := range r.drain(b) {
		if m["t"] == "objs" && num(objOf(m, "chicken:0")["sq"]) == 1 && num(m["now"]) > 0 {
			seen = true
		}
		if m["t"] == "obj_ev" && m["ev"] == "squeeze" && num(m["by"]) == 1 && m["id"] == "chicken:0" {
			ev = true
		}
	}
	if !seen || !ev {
		t.Fatal("Bo did not see the squeeze")
	}
	if strings.Join(*got, ",") != "1:take:grainx1" {
		t.Fatalf("credited %v", *got)
	}
	// Ada's one squeeze a day is used; Bo still has his.
	r.now = r.now.Add(3 * time.Second)
	r.act(a, 2, "chicken:0", "squeeze", nil)
	if no := r.only(a, "obj_no"); no["code"] != "daily_limit" || no["message"] != "You can only squeeze this Chicken 1 time per game day." {
		t.Fatalf("Ada's second squeeze answered %v", no)
	}
	r.act(b, 1, "chicken:0", "squeeze", map[string]any{"skill": "animalkinship_2"})
	if ok := r.only(b, "obj_ok"); ok["success"] != true || num(ok["limit"]) != 3 || num(objOf(ok, "chicken:0")["sq"]) != 2 {
		t.Fatalf("Bo's squeeze answered %v", ok)
	}
	// A missed roll uses nothing (finishSkillPackage: the roll first).
	r.mod.roll = func() float64 { return 0.7 }
	r.now = r.now.Add(time.Second)
	c3 := r.player(3, "Cy", defaultStreet, ch.x, ch.y)
	r.act(c3, 1, "chicken:0", "squeeze", nil)
	if ok := r.only(c3, "obj_ok"); ok["success"] != false || ok["uses"] != nil {
		t.Fatalf("a missed squeeze answered %v", ok)
	}
	if len(*got) != 2 {
		t.Fatalf("a miss credited %v", *got)
	}
	// The next game day: everyone may squeeze again.
	r.mod.roll = func() float64 { return 0.1 }
	r.now = r.now.Add(time.Duration(gameDayMs) * time.Millisecond)
	r.act(a, 3, "chicken:0", "squeeze", nil)
	if ok := r.only(a, "obj_ok"); ok["success"] != true || num(objOf(ok, "chicken:0")["sq"]) != 1 {
		t.Fatalf("a new day's squeeze answered %v", ok)
	}
}

func TestMilkNeedsYourOwnMassageAndEveryoneSeesIt(t *testing.T) {
	r := newObjRig(t)
	got := r.credited()
	bf := sharedWorld.streets[defaultStreet]["butterfly:0"]
	a := r.player(1, "Ada", defaultStreet, bf.x, bf.y)
	b := r.player(2, "Bo", defaultStreet, bf.x, bf.y)
	r.act(a, 1, "butterfly:0", "milk", nil)
	if no := r.only(a, "obj_no"); no["code"] != "needs_massage" || no["message"] != butterflyNoWarmup {
		t.Fatalf("milk before a massage answered %v", no)
	}
	r.act(a, 2, "butterfly:0", "massage", nil)
	r.only(a, "obj_ok")
	r.now = r.now.Add(time.Second)
	r.act(a, 3, "butterfly:0", "milk", map[string]any{"skill": "animalkinship_2"})
	ok := r.only(a, "obj_ok")
	if ok["success"] != true || num(objOf(ok, "butterfly:0")["mk"]) != 1 || num(objOf(ok, "butterfly:0")["ms"]) != 1 {
		t.Fatalf("Ada's milk answered %v", ok)
	}
	var evs []string
	for _, m := range r.frames(b, "obj_ev") {
		evs = append(evs, m["ev"].(string))
	}
	if strings.Join(evs, ",") != "massage,milk" {
		t.Fatalf("Bo saw %v", evs)
	}
	// Ada's massage is not Bo's warmup; Animal Kinship VI needs none.
	r.now = r.now.Add(time.Second)
	r.act(b, 1, "butterfly:0", "milk", nil)
	if no := r.only(b, "obj_no"); no["code"] != "needs_massage" {
		t.Fatalf("Bo's milk on Ada's massage answered %v", no)
	}
	r.act(b, 2, "butterfly:0", "milk", map[string]any{"skill": "animalkinship_6"})
	if ok := r.only(b, "obj_ok"); ok["success"] != true || num(ok["limit"]) != 2 {
		t.Fatalf("Bo's AK VI milk answered %v", ok)
	}
	if strings.Join(*got, ",") != "1:take:milk_butterflyx2,2:take:milk_butterflyx4" {
		t.Fatalf("credited %v", *got)
	}
	// A flyer is milked from anywhere along the street; a resting one only near.
	far := r.player(3, "Cy", defaultStreet, bf.x+creatureReachX+10, bf.y)
	r.act(far, 1, "butterfly:0", "massage", nil)
	if no := r.only(far, "obj_no"); no["code"] != "far" {
		t.Fatalf("a far massage answered %v", no)
	}
	fl := sharedWorld.streets[defaultStreet]["butterfly:2"]
	far2 := r.player(4, "Di", defaultStreet, fl.x+creatureReachX+10, fl.y)
	r.act(far2, 1, "butterfly:2", "massage", nil)
	r.only(far2, "obj_ok")
}

func TestAChickenSitsOnOneEggForEveryoneAndHatchesItForItsOwner(t *testing.T) {
	r, l := usesRig(t)
	got := r.credited()
	ch := sharedWorld.streets[defaultStreet]["chicken:1"]
	a := r.player(1, "Ada", defaultStreet, ch.x, ch.y)
	b := r.player(2, "Bo", defaultStreet, ch.x, ch.y)
	r.act(a, 1, "chicken:1", "incubate", map[string]any{"c": "chicken_egg"})
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "no_item" || no["obj"].(map[string]any)["inc"] != nil {
		t.Fatalf("an egg Ada lacks answered %v", no)
	}
	l.give(1, "chicken_egg", 1)
	l.give(2, "piggy_egg", 1)
	r.act(a, 2, "chicken:1", "incubate", map[string]any{"c": "chicken_egg"})
	r.advance(0)
	ok := r.only(a, "obj_ok")
	inc, _ := objOf(ok, "chicken:1")["inc"].(map[string]any)
	if inc == nil || inc["egg"] != "chicken_egg" || num(inc["by"]) != 1 || num(inc["in"]) != 60000 || ok["use"] == nil || num(objOf(ok, "chicken:1")["hold"]) != 60000 {
		t.Fatalf("Ada's incubation answered %v", ok)
	}
	r.drain(b)
	r.act(b, 1, "chicken:1", "incubate", map[string]any{"c": "piggy_egg"})
	r.advance(0)
	if no := r.only(b, "obj_no"); no["code"] != "busy" || no["message"] != chickenBusy {
		t.Fatalf("a second egg answered %v", no)
	}
	r.act(b, 2, "chicken:1", "squeeze", nil)
	if no := r.only(b, "obj_no"); no["code"] != "sitting" || no["message"] != chickenSitting {
		t.Fatalf("a squeeze on a sitting Chicken answered %v", no)
	}
	if l.held[2]["piggy_egg"] != 1 {
		t.Fatal("Bo's egg was taken")
	}
	r.advance(60 * time.Second)
	var hatched bool
	for _, m := range r.frames(b, "obj_ev") {
		hatched = hatched || (m["ev"] == "hatched" && num(m["by"]) == 1)
	}
	if !hatched || strings.Join(*got, ",") != "1:take:chickx1" {
		t.Fatalf("the hatch: seen %v, credited %v", hatched, *got)
	}
	// Bo's egg: he walks off before it hatches, and it is lost.
	r.act(b, 3, "chicken:1", "incubate", map[string]any{"c": "piggy_egg"})
	r.advance(0)
	r.only(b, "obj_ok")
	r.send(b, map[string]any{"t": "join", "street": "GA5101HF7F429V5", "x": 100, "y": 100})
	r.advance(61 * time.Second)
	var lost bool
	for _, m := range r.frames(a, "obj_ev") {
		lost = lost || (m["ev"] == "lost" && num(m["by"]) == 2)
	}
	if !lost || len(*got) != 1 {
		t.Fatalf("a hatch with its owner gone: lost %v, credited %v", lost, *got)
	}
}

func TestABatterflysFoodCounterIsEveryones(t *testing.T) {
	r, l := usesRig(t)
	got := r.credited()
	var street string
	var spot *objSpot
	for tsid, spots := range sharedWorld.streets {
		if s := spots["batterfly:0"]; s != nil && (street == "" || tsid < street) {
			street, spot = tsid, s
		}
	}
	a := r.player(1, "Ada", street, spot.x, spot.y)
	b := r.player(2, "Bo", street, spot.x, spot.y)
	l.give(1, "grain", 10)
	l.give(2, "grain", 10)
	// Ten Grain (base cost 1 each): 10, no Guano yet.
	r.act(a, 1, "batterfly:0", "feed", map[string]any{"c": "grain", "n": 10})
	r.advance(0)
	ok := r.only(a, "obj_ok")
	if num(ok["guano"]) != 0 || num(objOf(ok, "batterfly:0")["food"]) != 10 {
		t.Fatalf("Ada's feed answered %v", ok)
	}
	// Bo's five more tip the shared counter to 15: one Guano, his.
	r.now = r.now.Add(5 * time.Second)
	r.act(b, 1, "batterfly:0", "feed", map[string]any{"c": "grain", "n": 5})
	r.advance(0)
	ok = r.only(b, "obj_ok")
	if num(ok["guano"]) != 1 || num(objOf(ok, "batterfly:0")["food"]) != 0 || strings.Join(*got, ",") != "2:take:guanox1" {
		t.Fatalf("Bo's feed answered %v, credited %v", ok, *got)
	}
	r.act(a, 2, "batterfly:0", "feed", map[string]any{"c": "pick", "n": 1})
	r.advance(0)
	if no := r.only(a, "obj_no"); no["code"] != "not_food" {
		t.Fatalf("a Pick answered %v", no)
	}
	if batterflyGuano(50, nil) != 2 || batterflyGuano(50, map[string]bool{"guano1": true}) != 3 || batterflyGuano(80, map[string]bool{"guano2": true}) != 5 || batterflyGuano(100, nil) != 3 || batterflyGuano(14, nil) != 0 {
		t.Fatal("the Guano thresholds")
	}
}

func TestAHoldStillsThePathForEveryone(t *testing.T) {
	r := newObjRig(t)
	ob := &liveObj{spot: &objSpot{id: "chicken:0", kind: "chicken"}}
	r.mod.hold(ob, 2*time.Second)
	if ob.lag != 2000 || !ob.holdEnd.Equal(r.now.Add(2*time.Second)) {
		t.Fatalf("a hold: lag %d", ob.lag)
	}
	// A hold within a hold extends it by what is new.
	r.now = r.now.Add(time.Second)
	r.mod.hold(ob, 3*time.Second)
	if ob.lag != 4000 {
		t.Fatalf("an extended hold: lag %d", ob.lag)
	}
	// Path time: frozen at (holdEnd - lag) the whole while.
	if ob.holdEnd.UnixMilli()-ob.lag != r.now.Add(-time.Second).UnixMilli() {
		t.Fatal("the path did not stand still from the first hold")
	}
	r.mod.hold(ob, time.Second)
	if ob.lag != 4000 {
		t.Fatal("a shorter hold inside a longer one changed the lag")
	}
}

func TestANibbleIsHeldToTheDailyLimitAndCreditsItsMeat(t *testing.T) {
	r := newObjRig(t)
	got := r.credited()
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	r.act(a, 1, "pig:2", "pet", nil)
	r.only(a, "obj_ok")
	r.act(a, 2, "pig:2", "nibble", map[string]any{"skill": "animalkinship_2"})
	if ok := r.only(a, "obj_ok"); ok["success"] != true {
		t.Fatalf("the nibble answered %v", ok)
	}
	r.now = r.now.Add(time.Second)
	r.act(a, 3, "pig:2", "nibble", map[string]any{"skill": "animalkinship_2"})
	if no := r.only(a, "obj_no"); no["code"] != "daily_limit" {
		t.Fatalf("a second nibble answered %v", no)
	}
	if strings.Join(*got, ",") != "1:take:meatx2" {
		t.Fatalf("credited %v", *got)
	}
}

func TestStreetAnimalsOutliveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.UnixMilli(gameEpochMs+1000*gameDayMs+3_600_000))
	ch := sharedWorld.streets[defaultStreet]["chicken:0"]
	a := r.player(1, "Ada", defaultStreet, ch.x, ch.y)
	r.mod.roll = func() float64 { return 0.1 }
	r.act(a, 1, "chicken:0", "squeeze", nil)
	r.only(a, "obj_ok")
	r.act(a, 2, "chicken:2", "incubate", map[string]any{"c": "butterfly_egg"})
	r.advance(0)
	r.only(a, "obj_ok")
	r.mod.save()
	r2 := r.restart(path, r.now.Add(10*time.Second))
	a2 := r2.player(1, "Ada", defaultStreet, ch.x, ch.y)
	c := syncOf(r2, a2, "chicken:0")
	if num(c["lag"]) != 2500 || num(c["sq"]) != 1 {
		t.Fatalf("after a restart chicken:0 reads %v", c)
	}
	inc, _ := syncOf(r2, a2, "chicken:2")["inc"].(map[string]any)
	if inc == nil || num(inc["in"]) != 50000 {
		t.Fatalf("after a restart the egg reads %v", inc)
	}
	r2.act(a2, 1, "chicken:0", "squeeze", nil)
	if no := r2.only(a2, "obj_no"); no["code"] != "daily_limit" {
		t.Fatalf("Ada's squeeze after a restart answered %v", no)
	}
	r2.advance(51 * time.Second)
	if syncOf(r2, a2, "chicken:2")["inc"] != nil {
		t.Fatal("the egg did not hatch after the restart")
	}
}

// The cost of the street animals: a street's snapshot with every animal on it touched, and the
// whole world's animals in memory and on disk (go test -run TestStreetAnimalCost -v).
func TestStreetAnimalCost(t *testing.T) {
	r := newObjRig(t)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	n := 0
	for street, spots := range sharedWorld.streets {
		for _, s := range spots {
			if !isCreature(s.kind) {
				continue
			}
			ob := r.mod.get(street, s)
			r.mod.hold(ob, creatureHold)
			ob.use(int64(n%50+1), "squeeze")
			ob.usesDay = r.mod.today()
			n++
		}
	}
	touch := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)
	start = time.Now()
	sync := 0
	cl := r.player(1, "Ada", defaultStreet, 0, 0)
	for i := 0; i < 100; i++ {
		r.send(cl, map[string]any{"t": "obj_sync"})
		sync += len(r.drain(cl))
		r.mod.limits = map[int64]*objBucket{}
	}
	snap := time.Since(start) / 100
	path := filepath.Join(t.TempDir(), "objects.json")
	r.mod.path = path
	start = time.Now()
	r.mod.save()
	saveT := time.Since(start)
	st, _ := os.Stat(path)
	t.Logf("%d street animals touched in %v, %.1f KB held; one street's snapshot %v; the state file %.0f KB written in %v",
		n, touch, float64(after.HeapAlloc-before.HeapAlloc)/1024, snap, float64(st.Size())/1024, saveT)
	if n < 3000 || snap > 50*time.Millisecond {
		t.Fatalf("%d animals, snapshot %v", n, snap)
	}
}
