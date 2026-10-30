package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newObjRigAt is newObjRig with a state file, its clock at now (zero: the rig's own).
func newObjRigAt(t *testing.T, path string, now time.Time) *objRig {
	t.Helper()
	saved := objectsFileForTest
	objectsFileForTest = path
	defer func() { objectsFileForTest = saved }()
	r := &objRig{t: t, now: time.UnixMilli(gameEpochMs + 1000*gameDayMs + 3_600_000)}
	if !now.IsZero() {
		r.now = now
	}
	savedMods := modules
	modules = []moduleFactory{func(h *Hub) Module { r.mod = newObjects(h).(*objects); return r.mod }}
	defer func() { modules = savedMods }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	r.mod.roll = func() float64 { return 0.5 }
	return r
}

// restart is the server stopping now and a new one starting from the same file, at.
func (r *objRig) restart(path string, at time.Time) *objRig {
	r.t.Helper()
	r.advance(time.Second) // a due save lands
	return newObjRigAt(r.t, path, at)
}

func syncOf(r *objRig, cl *Client, id string) map[string]any {
	r.t.Helper()
	r.drain(cl)
	r.send(cl, map[string]any{"t": "obj_sync"})
	return objOf(r.only(cl, "objs"), id)
}

func TestADeadTreeAndAPoisonClockOutliveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	got := r.credited()
	tree := sharedWorld.streets[defaultStreet]["tree:0"] // trant_fruit
	wood := sharedWorld.streets[defaultStreet]["tree:7"] // wood_tree
	a := r.player(1, "Ada", defaultStreet, tree.x, tree.y)
	b := r.player(2, "Bo", defaultStreet, wood.x, wood.y)
	r.act(a, 1, "tree:0", "poison", nil)
	r.drain(b)
	r.advance(treePoison)
	if dead := objOf(r.only(b, "objs"), "tree:0"); dead == nil || dead["dead"] != true {
		t.Fatalf("Bo saw %v", dead)
	}
	// Bo poisons the Wood Tree; a minute later the server restarts.
	r.act(b, 1, "tree:7", "poison", nil)
	r.only(b, "obj_ok")
	r.advance(time.Minute)
	start := r.now
	r = r.restart(path, start.Add(10*time.Second))
	got = r.credited()
	c := r.player(3, "Cy", defaultStreet, tree.x, tree.y)
	if dead := syncOf(r, c, "tree:0"); dead == nil || dead["dead"] != true || num(dead["by"]) != 1 || num(dead["fruit"]) != 0 {
		t.Fatalf("after the restart Cy sees the dead tree as %v", dead)
	}
	if p := syncOf(r, c, "tree:7"); p == nil || num(p["poison"]) != float64((2*time.Minute-10*time.Second).Milliseconds()) || num(p["by"]) != 2 {
		t.Fatalf("the poison clock reads %v", p)
	}
	r.act(c, 1, "tree:0", "harvest", map[string]any{"skill": "no_skill"})
	if no := r.only(c, "obj_no"); no["code"] != "dead" {
		t.Fatalf("a harvest on the dead tree answered %v", no)
	}
	// The clock runs out on the new server: dead for Cy, Bo credited his planks once.
	r.advance(2*time.Minute - 10*time.Second)
	if dead := objOf(r.only(c, "objs"), "tree:7"); dead == nil || dead["dead"] != true || num(dead["by"]) != 2 {
		t.Fatalf("Cy saw %v", dead)
	}
	if strings.Join(*got, ",") != "2:take:plankx25" {
		t.Fatalf("credited %v", *got)
	}
	// Another restart: both stay dead and nothing is credited again.
	r = r.restart(path, r.now.Add(time.Hour))
	got = r.credited()
	d := r.player(4, "Di", defaultStreet, 0, 0)
	for _, id := range []string{"tree:0", "tree:7"} {
		if dead := syncOf(r, d, id); dead == nil || dead["dead"] != true {
			t.Fatalf("%s after a second restart: %v", id, dead)
		}
	}
	r.advance(time.Hour)
	if len(*got) != 0 {
		t.Fatalf("credited again: %v", *got)
	}
}

func TestAPoisonClockThatRanOutWhileTheServerWasDownKillsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	wood := sharedWorld.streets[defaultStreet]["tree:7"]
	a := r.player(1, "Ada", defaultStreet, wood.x, wood.y)
	r.act(a, 1, "tree:7", "poison", nil)
	r.only(a, "obj_ok")
	// No save is waited for: the pour is on disk at once.
	r = newObjRigAt(t, path, r.now.Add(10*time.Minute))
	got := r.credited()
	b := r.player(2, "Bo", defaultStreet, wood.x, wood.y)
	if dead := syncOf(r, b, "tree:7"); dead == nil || dead["dead"] != true || num(dead["by"]) != 1 {
		t.Fatalf("the first player back sees %v", dead)
	}
	if strings.Join(*got, ",") != "1:take:plankx25" {
		t.Fatalf("credited %v", *got)
	}
	// The death went to disk before the credit: a crash right after does not kill it again.
	r = newObjRigAt(t, path, r.now.Add(time.Minute))
	got = r.credited()
	r.player(3, "Cy", defaultStreet, 0, 0)
	if len(*got) != 0 {
		t.Fatalf("credited again: %v", *got)
	}
}

func TestACuredTreeStaysCuredAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	tree := sharedWorld.streets[defaultStreet]["tree:0"]
	a := r.player(1, "Ada", defaultStreet, tree.x, tree.y)
	r.act(a, 1, "tree:0", "poison", nil)
	r.now = r.now.Add(time.Second)
	r.act(a, 2, "tree:0", "antidote", nil)
	r = newObjRigAt(t, path, r.now.Add(10*time.Minute))
	b := r.player(2, "Bo", defaultStreet, tree.x, tree.y)
	if p := syncOf(r, b, "tree:0"); p != nil {
		t.Fatalf("a cured, untouched tree is fresh again: %v", p)
	}
}

func TestAStreetPiggyKeepsItsMoodAndClockAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.act(a, 1, "pig:2", "pet", map[string]any{"skill": "no_skill"})
	if ok := r.only(a, "obj_ok"); ok["success"] != true {
		t.Fatalf("the pet answered %v", ok)
	}
	// An hour on, the server restarts; Ada comes back on the same game day.
	r = r.restart(path, r.now.Add(time.Hour))
	a = r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	p := syncOf(r, a, "pig:2")
	if p == nil || num(p["mood"]) != 2 || p["petted"] != true || p["daily"] != true {
		t.Fatalf("after the restart the Piggy reads %v", p)
	}
	if left := num(p["in"]); left > float64(time.Hour.Milliseconds()) || left < float64((time.Hour-3*time.Second).Milliseconds()) {
		t.Fatalf("its clock went on from where it was: %v ms to the tick", left)
	}
	// She petted it today: her nibble still lands.
	r.act(a, 2, "pig:2", "nibble", nil)
	if ok := r.only(a, "obj_ok"); ok["success"] != true {
		t.Fatalf("a nibble after the restart answered %v", ok)
	}
	// The tick comes on time.
	r.advance(time.Hour + 20*time.Millisecond)
	f := r.frames(a, "objs")
	if len(f) == 0 || num(objOf(f[len(f)-1], "pig:2")["hunger"]) != 1 {
		t.Fatalf("the tick after the restart sent %v", f)
	}
	// Down for five hours: the two ticks it missed (the daily half, then the other) run when the
	// server is back, and the next is an hour on, on its old clock.
	r = r.restart(path, r.now.Add(5*time.Hour))
	b := r.player(2, "Bo", defaultStreet, pig.x, pig.y)
	r.advance(20 * time.Millisecond)
	p = syncOf(r, b, "pig:2")
	if p == nil || num(p["in"]) > float64(time.Hour.Milliseconds()) || num(p["in"]) < float64((time.Hour-time.Second).Milliseconds()) || p["daily"] != false || num(p["mood"]) != 2 || p["petted"] != false {
		t.Fatalf("after five hours down: %v", p)
	}
}

func TestRocksAndNodesKeepWhatWasTakenAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	rock := sharedWorld.streets[defaultStreet]["rock:1"]
	a := r.player(1, "Ada", defaultStreet, rock.x, rock.y)
	r.act(a, 1, "rock:1", "mine", map[string]any{"perks": []string{"luck"}})
	r.only(a, "obj_ok")
	r = r.restart(path, r.now.Add(time.Minute))
	a = r.player(1, "Ada", defaultStreet, rock.x, rock.y)
	if got := syncOf(r, a, "rock:1"); got == nil || num(got["chunks"]) != 41 {
		t.Fatalf("the rock after a restart: %v", got)
	}
	// Emptied, then the server is down past its respawn: back to fresh on return.
	for req := 2; ; req++ {
		r.now = r.now.Add(time.Second)
		r.act(a, req, "rock:1", "mine", map[string]any{"perks": []string{"luck"}})
		if num(objOf(r.only(a, "obj_ok"), "rock:1")["chunks"]) == 0 {
			break
		}
	}
	r = r.restart(path, r.now.Add(rockRespawn/2))
	a = r.player(1, "Ada", defaultStreet, rock.x, rock.y)
	if got := syncOf(r, a, "rock:1"); got == nil || num(got["chunks"]) != 0 || num(got["in"]) < 20000 {
		t.Fatalf("an empty rock mid-respawn: %v", got)
	}
	r.advance(rockRespawn / 2)
	if got := syncOf(r, a, "rock:1"); got != nil {
		t.Fatalf("respawned on the new server, it is fresh: %v", got)
	}

	street, spot := dirtStreet(t)
	r = newObjRigAt(t, path, r.now)
	d := r.player(5, "Di", street, spot.x, spot.y)
	r.act(d, 1, spot.id, "dig", nil)
	before := objOf(r.only(d, "obj_ok"), spot.id)
	if before == nil || num(before["in"]) <= 0 {
		t.Fatalf("the dig answered %v", before)
	}
	units := num(before["units"])
	r = r.restart(path, r.now.Add(time.Second))
	d = r.player(5, "Di", street, spot.x, spot.y)
	if got := syncOf(r, d, spot.id); got == nil || num(got["units"]) != units {
		t.Fatalf("the Dirt Pile after a restart: %v (was %v units)", got, units)
	}
	// Down for a day: every regrowth step it missed is caught up, and it is fresh.
	r = r.restart(path, r.now.Add(24*time.Hour))
	d = r.player(5, "Di", street, spot.x, spot.y)
	if got := syncOf(r, d, spot.id); got != nil {
		t.Fatalf("a day later the Dirt Pile reads %v", got)
	}
}

func TestABadObjectsFileStartsEmptyAndIsReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "objects.json")
	if err := os.WriteFile(path, []byte(`{"v":1,"streets":{`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newObjRigAt(t, path, time.Time{})
	tree := sharedWorld.streets[defaultStreet]["tree:0"]
	a := r.player(1, "Ada", defaultStreet, tree.x, tree.y)
	if got := syncOf(r, a, "tree:0"); got != nil {
		t.Fatalf("a bad file still gave %v", got)
	}
	aside, _ := filepath.Glob(path + ".bad-*")
	if len(aside) != 1 {
		t.Fatalf("the bad file was not moved aside: %v", aside)
	}
	r.act(a, 1, "tree:0", "poison", nil)
	var f objFile
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &f) != nil || f.Streets[defaultStreet]["tree:0"] == nil || f.Streets[defaultStreet]["tree:0"].PoisonEnd == 0 {
		t.Fatalf("the next save wrote %s (%v)", b, err)
	}
}

func TestObjectsTheLayoutNoLongerHasAreDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	file := objFile{V: 1, Streets: map[string]map[string]*objRec{
		"GNOWHERE":    {"tree:0": {Kind: "tree", Dead: true}},
		defaultStreet: {"tree:99": {Kind: "tree", Dead: true}, "rock:1": {Kind: "tree", Dead: true}, "tree:0": {Kind: "tree", Dead: true, Maturity: 99, Health: -3}},
	}}
	b, _ := json.Marshal(file)
	os.WriteFile(path, b, 0o600)
	r := newObjRigAt(t, path, time.Time{})
	if len(r.mod.streets) != 1 || len(r.mod.streets[defaultStreet]) != 1 {
		t.Fatalf("kept %v", r.mod.streets)
	}
	ob := r.mod.streets[defaultStreet]["tree:0"]
	if !ob.dead || ob.maturity < 1 || ob.health != 1 {
		t.Fatalf("the dead tree came back as %+v", ob)
	}
}

func TestNoObjectsFileWithoutAPath(t *testing.T) {
	dir := t.TempDir()
	r := newObjRigAt(t, "", time.Time{})
	tree := sharedWorld.streets[defaultStreet]["tree:0"]
	a := r.player(1, "Ada", defaultStreet, tree.x, tree.y)
	r.act(a, 1, "tree:0", "poison", nil)
	r.advance(treePoison)
	if r.mod.path != "" || r.mod.saveAt {
		t.Fatal("memory only wrote a file")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files appeared: %v", entries)
	}
}

// The invented set every street without an ecology row used to get (trees, two
// rocks and a Piggy or two) is gone (game 2026-10-30): Gentle Island Exit's file
// holds only 4 quoins and a Qurazy marker. A state file from before still names
// its trees, rock and Piggy; they are dropped on load and nothing else is.
func TestTheOldFallbackObjectsOnGentleIslandExitAreDropped(t *testing.T) {
	const exit = "GA93VBH3H3D38HF"
	path := filepath.Join(t.TempDir(), "objects.json")
	file := objFile{V: 1, Streets: map[string]map[string]*objRec{
		exit: {
			"tree:1": {Kind: "tree", Fruit: 3, Maturity: 7, Health: 8},
			"tree:3": {Kind: "wood", WoodCount: 2},
			"rock:0": {Kind: "rock", Chunks: 10},
			"pig:0":  {Kind: "pig", Mood: 40},
		},
		defaultStreet: {"tree:0": {Kind: "tree", Dead: true}},
	}}
	b, _ := json.Marshal(file)
	os.WriteFile(path, b, 0o600)
	r := newObjRigAt(t, path, time.Time{})
	if n := len(r.mod.w.streets[exit]); n != 0 {
		t.Fatalf("the layout still lays out %d objects on Gentle Island Exit", n)
	}
	if room, kept := r.mod.streets[exit]; kept {
		t.Fatalf("kept %v on Gentle Island Exit", room)
	}
	if ob := r.mod.streets[defaultStreet]["tree:0"]; ob == nil || !ob.dead {
		t.Fatalf("Hechey Track's dead tree went too: %+v", ob)
	}
}
