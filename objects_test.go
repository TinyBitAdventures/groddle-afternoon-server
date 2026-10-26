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

// objRig is a hub with only the objects module, a hand-wound clock and timers
// that fire when the test says.
type objRig struct {
	t      *testing.T
	hub    *Hub
	mod    *objects
	now    time.Time
	timers []*objTimer
}

type objTimer struct {
	at   time.Time
	fn   func()
	done bool
}

func newObjRig(t *testing.T) *objRig {
	t.Helper()
	r := &objRig{t: t, now: time.UnixMilli(gameEpochMs + 1000*gameDayMs + 3_600_000)}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.mod = newObjects(h).(*objects); return r.mod }}
	defer func() { modules = saved }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	r.mod.roll = func() float64 { return 0.5 }
	return r
}

// advance moves the clock and fires every timer now due, in order.
func (r *objRig) advance(d time.Duration) {
	r.now = r.now.Add(d)
	for {
		var next *objTimer
		for _, tm := range r.timers {
			if !tm.done && !tm.at.After(r.now) && (next == nil || tm.at.Before(next.at)) {
				next = tm
			}
		}
		if next == nil {
			return
		}
		next.done = true
		next.fn()
	}
}

func (r *objRig) player(id int64, name, street string, x, y float64) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.send(cl, map[string]any{"t": "join", "street": street, "x": x, "y": y})
	r.drain(cl)
	return cl
}

func (r *objRig) send(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

// drain returns every frame queued for cl, decoded.
func (r *objRig) drain(cl *Client) []map[string]any {
	var out []map[string]any
	for {
		select {
		case b := <-cl.send:
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				r.t.Fatal(err)
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

func (r *objRig) only(cl *Client, kind string) map[string]any {
	r.t.Helper()
	var got []map[string]any
	for _, m := range r.drain(cl) {
		if m["t"] == kind {
			got = append(got, m)
		}
	}
	if len(got) != 1 {
		r.t.Fatalf("want one %s frame, got %v", kind, got)
	}
	return got[0]
}

func (r *objRig) act(cl *Client, req int, id, verb string, extra map[string]any) {
	msg := map[string]any{"t": "obj_act", "req": req, "id": id, "verb": verb}
	for k, v := range extra {
		msg[k] = v
	}
	r.send(cl, msg)
}

func num(v any) float64 { f, _ := v.(float64); return f }

func objOf(m map[string]any, id string) map[string]any {
	if o, ok := m["obj"].(map[string]any); ok && o["id"] == id {
		return o
	}
	list, _ := m["objs"].([]any)
	for _, raw := range list {
		if o, ok := raw.(map[string]any); ok && o["id"] == id {
			return o
		}
	}
	return nil
}

const defaultStreet = "GLI2V0UN7RD1T2J"

func TestTheLayoutLoads(t *testing.T) {
	w := sharedWorld
	spots := w.streets[defaultStreet]
	if spots["rock:1"] == nil || spots["rock:1"].class != "rock_dullite_1" || spots["tree:0"] == nil {
		t.Fatalf("the default street's rocks and trees are missing: %v", spots)
	}
	if len(w.streets) < 1000 {
		t.Fatalf("only %d streets laid out", len(w.streets))
	}
	// The caps are the game repo's public/assets/content/trants.json fruitCapMap.
	game := os.Getenv("GRODDLE_GAME_DIR")
	if game == "" {
		game = filepath.Join("..", "groddle-afternoon")
	}
	raw, err := os.ReadFile(filepath.Join(game, "public", "assets", "content", "trants.json"))
	if err != nil {
		t.Skipf("the fruit caps are checked against the game repo's trants.json: none at %s (set GRODDLE_GAME_DIR)", game)
	}
	var trants map[string]struct {
		FruitCapMap [][]int `json:"fruitCapMap"`
	}
	if err := json.Unmarshal(raw, &trants); err != nil {
		t.Fatal(err)
	}
	for class, row := range trants {
		for m := 1; m <= 10; m++ {
			for h := 1; h <= 10; h++ {
				if got, want := w.capAt(class, m, h), row.FruitCapMap[m-1][h-1]; got != want {
					t.Fatalf("%s cap(%d,%d) = %d, trants.json has %d", class, m, h, got, want)
				}
			}
		}
	}
}

func TestARockEmptiesForEveryoneAndComesBack(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["rock:1"]
	a := r.player(1, "Ada", defaultStreet, spot.x+40, spot.y)
	b := r.player(2, "Bo", defaultStreet, spot.x-60, spot.y)
	r.drain(a)

	// max_luck: Dullite's top roll, 9 (include/mining.js:298), until 50 is gone.
	left := 50.0
	for req := 1; left > 0; req++ {
		r.act(a, req, "rock:1", "mine", map[string]any{"perks": []string{"luck"}})
		ok := r.only(a, "obj_ok")
		if num(ok["chunks"]) != 9 || num(ok["req"]) != float64(req) {
			t.Fatalf("swing %d answered %v", req, ok)
		}
		left -= 9
		seen := objOf(r.only(b, "objs"), "rock:1")
		if want := max(0, left); num(seen["chunks"]) != want {
			t.Fatalf("Bo sees %v chunks, want %v", seen["chunks"], want)
		}
	}
	// Bo cannot take more: the source's line.
	r.act(b, 1, "rock:1", "mine", nil)
	no := r.only(b, "obj_no")
	if no["code"] != "empty" || no["message"] != "This is not the rock you are looking for." || num(objOf(no, "rock:1")["in"]) < 44000 {
		t.Fatalf("Bo's swing answered %v", no)
	}
	// 45 s later it reforms for both, and the server forgets it (fresh again).
	r.advance(rockRespawn)
	for _, cl := range []*Client{a, b} {
		if got := objOf(r.only(cl, "objs"), "rock:1"); num(got["chunks"]) != 50 || got["mega"] != false {
			t.Fatalf("the respawn reads %v", got)
		}
	}
	if len(r.mod.streets) != 0 {
		t.Fatalf("a fresh rock is still held: %v", r.mod.streets)
	}
}

func TestAMegaRockAndTheExpertTake(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["rock:3"] // rock_sparkly_1: 20% mega, 30% with Rock Whisperer
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	r.mod.roll = func() float64 { return 0.25 }
	// randInt(3, 7) at 0.25 is 4; Sparkly Expert takes round(0.6) = 1 more off the rock.
	r.act(a, 1, "rock:3", "mine", map[string]any{"perks": []string{"expert"}})
	if ok := r.only(a, "obj_ok"); num(ok["chunks"]) != 4 || num(objOf(ok, "rock:3")["chunks"]) != 45 {
		t.Fatalf("the expert swing answered %v", ok)
	}
	r.mod.get(defaultStreet, spot).chunks = 1
	r.act(a, 2, "rock:3", "mine", map[string]any{"perks": []string{"whisperer"}})
	r.drain(a)
	r.advance(rockRespawn)
	if got := objOf(r.only(a, "objs"), "rock:3"); num(got["chunks"]) != 150 || got["mega"] != true {
		t.Fatalf("a whispered sparkly rock at 0.25 should come back huge: %v", got)
	}
}

func TestTheServerChecksWhereAndWhat(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["rock:1"]
	a := r.player(1, "Ada", defaultStreet, spot.x+objReachX+10, spot.y)
	r.act(a, 1, "rock:1", "mine", nil)
	if no := r.only(a, "obj_no"); no["code"] != "far" {
		t.Fatalf("a swing from across the street answered %v", no)
	}
	for _, id := range []string{"rock:99", "tree:x", "node:peat_1:9", "rock:1;drop"} {
		r.now = r.now.Add(time.Second)
		r.act(a, 2, id, "mine", nil)
		if no := r.only(a, "obj_no"); no["code"] != "unknown" {
			t.Fatalf("%s answered %v", id, no)
		}
	}
	r.act(a, 3, "rock:1", "eat", nil)
	if no := r.only(a, "obj_no"); no["code"] != "unknown" {
		t.Fatalf("an unknown verb answered %v", no)
	}
	r.send(a, map[string]any{"t": "obj_act", "req": -1, "id": "rock:1", "verb": "mine"})
	if e := r.only(a, "error"); e["code"] != "bad_obj" {
		t.Fatalf("a bad req answered %v", e)
	}
	// Off the street, nothing.
	r.send(a, map[string]any{"t": "leave"})
	r.drain(a)
	r.now = r.now.Add(time.Second)
	r.act(a, 4, "rock:1", "mine", nil)
	if no := r.only(a, "obj_no"); no["code"] != "unknown" {
		t.Fatalf("off the street answered %v", no)
	}
}

func TestActionsAreRateLimited(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["rock:1"]
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	slow := 0
	for i := 1; i <= 20; i++ {
		r.act(a, i, "rock:99", "mine", nil)
		for _, m := range r.drain(a) {
			if m["code"] == "slow_down" {
				slow++
			}
		}
	}
	if slow < 10 {
		t.Fatalf("20 actions at once, only %d slowed", slow)
	}
}

func TestSyncSendsWhatDiffersFromFresh(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["rock:1"]
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	if snap := r.only(a, "objs"); snap["snap"] != true || len(snap["objs"].([]any)) != 0 || snap["none"] != nil {
		t.Fatalf("an untouched street syncs as %v", snap)
	}
	r.act(a, 1, "rock:1", "mine", nil)
	r.drain(a)
	b := r.player(2, "Bo", defaultStreet, 0, 0)
	r.send(b, map[string]any{"t": "obj_sync"})
	snap := r.only(b, "objs")
	if got := objOf(snap, "rock:1"); got == nil || num(got["chunks"]) != 43 { // randInt(5, 9) at 0.5 is 7
		t.Fatalf("Bo's sync reads %v", snap)
	}
	c := r.player(3, "Cy", "NOWHERE", 0, 0)
	r.send(c, map[string]any{"t": "obj_sync"})
	if snap := r.only(c, "objs"); snap["none"] != true {
		t.Fatalf("a street with no layout syncs as %v", snap)
	}
}

func TestATreeIsSharedButHarvestLimitsAreNot(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["tree:0"] // trant_fruit, maturity 10, health 7
	capFresh := sharedWorld.capAt("trant_fruit", 10, 7)
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	b := r.player(2, "Bo", defaultStreet, spot.x, spot.y)
	r.drain(a)

	r.act(a, 1, "tree:0", "harvest", map[string]any{"skill": "gardening_3"})
	ok := r.only(a, "obj_ok")
	if num(ok["take"]) != 12 || num(objOf(ok, "tree:0")["fruit"]) != float64(capFresh-12) || num(ok["before"].(map[string]any)["fruit"]) != float64(capFresh) {
		t.Fatalf("Gardening III harvest answered %v", ok)
	}
	if got := objOf(r.only(b, "objs"), "tree:0"); num(got["fruit"]) != float64(capFresh-12) {
		t.Fatalf("Bo sees %v", got)
	}

	// No skill: pet succeeds 60% (0.5 * 100 < 60), water 90%; together +1 health.
	r.act(a, 2, "tree:0", "pet", nil)
	if ok := r.only(a, "obj_ok"); ok["success"] != true || num(ok["roll"]) != 0.5 || objOf(ok, "tree:0")["petted"] != true {
		t.Fatalf("pet answered %v", ok)
	}
	r.drain(b)
	r.act(b, 1, "tree:0", "pet", nil)
	if no := r.only(b, "obj_no"); no["code"] != "not_wanted" {
		t.Fatalf("a second pet answered %v", no)
	}
	r.act(b, 2, "tree:0", "water", map[string]any{"skill": "light_green_thumb_1"})
	ok = r.only(b, "obj_ok")
	tree := objOf(ok, "tree:0")
	if tree["watered"] != true || num(tree["health"]) != 8 || num(tree["cap"]) != float64(sharedWorld.capAt("trant_fruit", 10, 8)) {
		t.Fatalf("water after pet answered %v", ok)
	}
	// A failed pet sets nothing (0.7 * 100 >= 60).
	spot1 := sharedWorld.streets[defaultStreet]["tree:1"]
	c := r.player(3, "Cy", defaultStreet, spot1.x, spot1.y)
	r.mod.roll = func() float64 { return 0.7 }
	r.act(c, 1, "tree:1", "pet", nil)
	if ok := r.only(c, "obj_ok"); ok["success"] != false || objOf(ok, "tree:1")["petted"] != false {
		t.Fatalf("a failed pet answered %v", ok)
	}

	// The new game day: flags clear and fruit refills to 55-80% of the cap.
	r.drain(a)
	r.drain(b)
	r.mod.roll = func() float64 { return 0 }
	r.advance(time.Duration(gameDayMs) * time.Millisecond)
	got := objOf(r.only(a, "objs"), "tree:0")
	capNow := sharedWorld.capAt("trant_fruit", 10, 8)
	want := max(capFresh-12, int(float64(capNow)*0.55))
	if got["petted"] != false || got["watered"] != false || num(got["fruit"]) != float64(want) {
		t.Fatalf("the new day reads %v, want fruit %d", got, want)
	}
}

func TestResourceNodesRunOutAndRegrow(t *testing.T) {
	r := newObjRig(t)
	w := *sharedWorld
	w.streets = map[string]map[string]*objSpot{"NODES": {
		"node:peat_1:0":          {id: "node:peat_1:0", kind: "node", class: "peat_1", x: 100, y: 500},
		"node:mortar_barnacle:0": {id: "node:mortar_barnacle:0", kind: "node", class: "mortar_barnacle", x: 200, y: 500},
		"node:paper_tree:0":      {id: "node:paper_tree:0", kind: "node", class: "paper_tree", x: 300, y: 500},
	}}
	r.mod.w = &w
	a := r.player(1, "Ada", "NODES", 150, 500)
	b := r.player(2, "Bo", "NODES", 250, 500)
	r.drain(a)

	for i := 1; i <= 4; i++ {
		r.act(a, i, "node:peat_1:0", "harvest", nil)
		if got := objOf(r.only(a, "obj_ok"), "node:peat_1:0"); num(got["units"]) != float64(4-i) {
			t.Fatalf("peat use %d left %v", i, got)
		}
	}
	r.drain(b)
	r.act(b, 1, "node:peat_1:0", "harvest", nil)
	if no := r.only(b, "obj_no"); no["message"] != "No more peat here!" {
		t.Fatalf("an empty bog answered %v", no)
	}
	// peat_1.js:228: randInt(2, 6) minutes; 0.5 rolls 4.
	r.advance(4 * time.Minute)
	if got := objOf(r.only(b, "objs"), "node:peat_1:0"); num(got["units"]) != 1 || num(got["in"]) != 240000 {
		t.Fatalf("the first regrow reads %v", got)
	}
	for i := 0; i < 3; i++ {
		r.advance(4 * time.Minute)
	}
	r.drain(a)
	if len(r.mod.streets["NODES"]) != 0 {
		t.Fatalf("a regrown bog is still held")
	}

	r.act(a, 9, "node:mortar_barnacle:0", "scrape", nil)
	if got := objOf(r.only(a, "obj_ok"), "node:mortar_barnacle:0"); num(got["units"]) != 1 {
		t.Fatalf("a scraped barnacle reads %v", got)
	}
	r.act(b, 2, "node:mortar_barnacle:0", "dig", nil)
	if no := r.only(b, "obj_no"); no["code"] != "bad_verb" {
		t.Fatalf("digging a barnacle answered %v", no)
	}
	r.act(b, 3, "node:mortar_barnacle:0", "scrape", nil)
	if no := r.only(b, "obj_no"); no["message"] != "This barnacle is not ready yet!" {
		t.Fatalf("a scraped barnacle answered %v", no)
	}

	// Paper Tree: one waters, the other pets, and 9 paper grow for both.
	r.act(a, 10, "node:paper_tree:0", "harvest", map[string]any{"perks": []string{"paper8"}})
	if ok := r.only(a, "obj_ok"); num(ok["take"]) != 8 || num(objOf(ok, "node:paper_tree:0")["units"]) != 13 {
		t.Fatalf("paper harvest answered %v", ok)
	}
	r.act(a, 11, "node:paper_tree:0", "water", nil)
	r.drain(a)
	r.drain(b)
	r.act(b, 4, "node:paper_tree:0", "pet", nil)
	if got := objOf(r.only(b, "obj_ok"), "node:paper_tree:0"); num(got["units"]) != 21 || got["petted"] != true || num(got["reset"]) != 360000 {
		t.Fatalf("pet after water reads %v", got)
	}
	r.act(a, 12, "node:paper_tree:0", "water", nil)
	if no := r.only(a, "obj_no"); no["message"] != "I don't want to be watered!" {
		t.Fatalf("a second water answered %v", no)
	}
	r.advance(6 * time.Minute)
	var reset map[string]any
	for _, m := range r.drain(a) {
		if got := objOf(m, "node:paper_tree:0"); got != nil {
			reset = got
		}
	}
	if reset == nil || reset["petted"] != false || reset["watered"] != false {
		t.Fatalf("the paper tree's reset reads %v", reset)
	}
}

func TestTheHeaderListsTheKinds(t *testing.T) {
	src, err := os.ReadFile("objects.go")
	if err != nil {
		t.Skip()
	}
	for _, kind := range []string{`"t":"obj_sync"`, `"t":"obj_act"`, `"t":"objs"`, `"t":"obj_ok"`, `"t":"obj_no"`} {
		if !strings.Contains(string(src), kind) {
			t.Fatalf("the header does not list %s", kind)
		}
	}
}

func TestHelpMineAddsTheHelpersChunks(t *testing.T) {
	if got := []int{helpMineBonus(0), helpMineBonus(1), helpMineBonus(2), helpMineBonus(4), helpMineBonus(9), helpMineBonus(12)}; fmt.Sprint(got) != "[0 1 3 10 30 30]" {
		t.Fatalf("mining.js 335-337 bonuses %v", got)
	}
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["rock:1"]
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	b := r.player(2, "Bo", defaultStreet, spot.x+30, spot.y)
	c := r.player(3, "Cy", defaultStreet, spot.x-30, spot.y)
	r.drain(a)
	r.drain(b)
	mine := func(cl *Client, do string) { r.send(cl, map[string]any{"t": "obj_mine", "id": "rock:1", "do": do}) }

	mine(a, "start")
	if got := r.only(a, "obj_mine"); got["helping"] != false {
		t.Fatalf("Ada starts alone: %v", got)
	}
	if got := objOf(r.only(b, "objs"), "rock:1"); fmt.Sprint(got["miners"]) != "[1]" {
		t.Fatalf("Bo sees Ada swinging (Help mine): %v", got)
	}
	mine(b, "start")
	if got := r.only(b, "obj_mine"); got["helping"] != true {
		t.Fatalf("Bo helps: %v", got)
	}
	mine(c, "start")
	r.drain(a)
	r.drain(b)
	r.drain(c)
	// Cy gives up: Ada keeps only Bo's help.
	mine(c, "stop")
	r.act(a, 1, "rock:1", "mine", map[string]any{"perks": []string{"luck"}})
	ok := r.only(a, "obj_ok")
	help, _ := ok["help"].(map[string]any)
	if num(ok["chunks"]) != 9 || help == nil || num(help["bonus"]) != 1 || fmt.Sprint(help["by"]) != "[Bo]" {
		t.Fatalf("Ada's swing with one help answered %v", ok)
	}
	if got := objOf(ok, "rock:1"); num(got["chunks"]) != 40 || fmt.Sprint(got["miners"]) != "[2]" {
		t.Fatalf("the rock gives up the bonus too, and Ada is off it: %v", got)
	}
	// Bo started after Ada: nobody helped Bo.
	r.act(b, 1, "rock:1", "mine", map[string]any{"perks": []string{"luck"}})
	if ok := r.only(b, "obj_ok"); ok["help"] != nil || objOf(ok, "rock:1")["miners"] != nil {
		t.Fatalf("Bo's swing had no helpers: %v", ok)
	}
	// Leaving the street takes a player off the rock.
	mine(c, "start")
	r.drain(a)
	r.send(c, map[string]any{"t": "join", "street": "GA9", "x": 0, "y": 0})
	if got := objOf(r.only(a, "objs"), "rock:1"); got["miners"] != nil {
		t.Fatalf("Cy left: %v", got)
	}
}
