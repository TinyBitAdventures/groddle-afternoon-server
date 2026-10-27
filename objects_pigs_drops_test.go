package main

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// pigDropRig is the objects rig with the drops module too: a street Piggy and the stacks
// dropped for everyone on its street (register G332).
type pigDropRig struct {
	*objRig
	drops   *drops
	site    *fakeDropSite
	pending []func()
}

func newPigDropRig(t *testing.T) *pigDropRig {
	t.Helper()
	r := &pigDropRig{objRig: &objRig{t: t, now: time.UnixMilli(gameEpochMs + 1000*gameDayMs + 3_600_000)},
		site: &fakeDropSite{answer: dropAnswer{Status: "settled"}, took: map[string]bool{}}}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.mod = newObjects(h).(*objects); return r.mod },
		func(h *Hub) Module { r.drops = newDrops(h).(*drops); return r.drops },
	}
	defer func() { modules = saved }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	r.mod.roll = func() float64 { return 0.5 }
	r.drops.site = r.site
	r.drops.loaded = true
	r.drops.spawn = func(fn func()) { r.pending = append(r.pending, fn) }
	n := 0
	r.drops.newID = func() string { n++; return fmt.Sprintf("%016x", n) }
	return r
}

// settle runs the site calls waiting, then whatever they set due now.
func (r *pigDropRig) settle() {
	for len(r.pending) > 0 {
		fn := r.pending[0]
		r.pending = r.pending[1:]
		fn()
	}
	r.advance(0)
}

func (r *pigDropRig) dropAt(cl *Client, c string, n int, x, y float64) string {
	r.send(cl, map[string]any{"t": "ground_drop", "ref": "d1", "item": map[string]any{"c": c, "n": n}, "x": x, "y": y})
	r.settle()
	return r.site.drops[len(r.site.drops)-1].Op
}

func (r *pigDropRig) frames(cl *Client, kind string) []map[string]any {
	var out []map[string]any
	for _, m := range r.drain(cl) {
		if m["t"] == kind {
			out = append(out, m)
		}
	}
	return out
}

func TestAHungryStreetPiggyWalksToSharedFoodAndEatsOne(t *testing.T) {
	r := newPigDropRig(t)
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x+150, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	ob := r.mod.streets[defaultStreet]["pig:2"]
	ob.hunger = 3
	for id, other := range r.mod.streets[defaultStreet] {
		if other.spot.kind == "pig" && id != "pig:2" {
			other.hunger = 0
		}
	}
	op := r.dropAt(a, "apple", 3, pig.x+150, pig.y-20)
	r.drain(a)
	if ob.eatingFood != op {
		t.Fatalf("the hungry Piggy goes for the apples (onContainerItemAdded): %q", ob.eatingFood)
	}
	// It walks 150 px at 42 px/s: nothing is eaten before it gets there.
	r.advance(3 * time.Second)
	r.settle()
	if len(r.site.eats) != 0 {
		t.Fatalf("eaten before it arrived: %v", r.site.eats)
	}
	r.advance(time.Second)
	r.settle()
	if len(r.site.eats) != 1 || r.site.eats[0] != op+":3" {
		t.Fatalf("one bite asked of the site: %v", r.site.eats)
	}
	ate := r.frames(a, "ground_eaten")
	if len(ate) != 1 || num(ate[0]["n"]) != 2 || ate[0]["pig"] != "pig:2" || ate[0]["id"] != op {
		t.Fatalf("the street hears the bite: %v", ate)
	}
	if ob.hunger != 0 || ob.eatingFood != "" || r.drops.stacks[op].N != 2 {
		t.Fatalf("sated, the stack one less: hunger %d, eating %q, n %d", ob.hunger, ob.eatingFood, r.drops.stacks[op].N)
	}
	// A sated Piggy leaves the rest (hunger 0: onContainerItemAdded needs hunger > 0).
	r.dropAt(a, "cherry", 2, pig.x+120, pig.y-20)
	if ob.eatingFood != "" {
		t.Fatalf("a sated Piggy went for food")
	}
}

func TestAPiggyEatsTheLastOfAStackAndNotWhatIsNotFood(t *testing.T) {
	r := newPigDropRig(t)
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	for _, other := range r.mod.streets[defaultStreet] {
		if other.spot.kind == "pig" {
			other.hunger = 0
		}
	}
	ob := r.mod.streets[defaultStreet]["pig:2"]
	ob.hunger = 2
	// A pick is no food (no include/food.js).
	r.dropAt(a, "pick", 1, pig.x, pig.y-20)
	if ob.eatingFood != "" {
		t.Fatal("a Piggy went for a pick")
	}
	op := r.dropAt(a, "apple", 1, pig.x+40, pig.y-20)
	r.drain(a)
	r.advance(pigWalkMin)
	r.settle()
	got := []string{}
	for _, m := range r.drain(a) {
		if m["t"] == "ground_eaten" || m["t"] == "ground_gone" {
			got = append(got, fmt.Sprint(m["t"], ":", m["why"], ":", m["n"]))
		}
	}
	if fmt.Sprint(got) != "[ground_eaten:<nil>:0 ground_gone:eaten:<nil>]" || r.drops.stacks[op] != nil {
		t.Fatalf("the last apple eaten, the stack gone: %v", got)
	}
}

func TestAStackPickedUpBeforeThePiggyArrivesIsNotEaten(t *testing.T) {
	r := newPigDropRig(t)
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x+600, pig.y)
	b := r.player(2, "Bo", defaultStreet, pig.x+900, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	for _, other := range r.mod.streets[defaultStreet] {
		if other.spot.kind == "pig" {
			other.hunger = 0
		}
	}
	ob := r.mod.streets[defaultStreet]["pig:2"]
	ob.hunger = 4
	op := r.dropAt(a, "apple", 2, pig.x+600, pig.y-20)
	// Ada walks off; Bo takes the apples while the Piggy is on its way (about 14 s).
	r.send(a, map[string]any{"t": "move", "x": pig.x + 1400, "y": pig.y})
	r.send(b, map[string]any{"t": "ground_pickup", "id": op})
	r.settle()
	r.advance(pigWalkMax)
	r.settle()
	if len(r.site.eats) != 0 || ob.hunger != 4 || ob.eatingFood != "" {
		t.Fatalf("nothing left to eat: eats %v, hunger %d, eating %q", r.site.eats, ob.hunger, ob.eatingFood)
	}
}

func TestATwiceDailyTickPrefersDroppedFoodToFruit(t *testing.T) {
	r := newPigDropRig(t)
	street := defaultStreet
	pig := sharedWorld.streets[street]["pig:2"]
	a := r.player(1, "Ada", street, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	ob := r.mod.streets[street]["pig:2"]
	tree := r.mod.fruitNear(street, ob.spot)
	if tree == nil {
		t.Fatal("no tree near pig:2")
	}
	// A sated Piggy: the drop is left alone until the tick makes it hungry.
	for _, other := range r.mod.streets[street] {
		if other.spot.kind == "pig" {
			other.hunger = 0
		}
	}
	op := r.dropAt(a, "apple", 2, pig.x+math.Min(300, pigReachX), pig.y-20)
	if ob.eatingFood != "" {
		t.Fatal("not hungry yet")
	}
	fruit := tree.fruit
	ob.hunger = 1 // hungry before the tick, so it scans (is_daily || hunger > 0)
	evs := r.mod.pigTwiceDaily(street, ob, []*liveObj{ob})
	if ob.eatingFood != op || tree.fruit != fruit || len(evs) != 0 {
		t.Fatalf("the tick goes for the food first (food_nearby): eating %q, fruit %d -> %d, events %v", ob.eatingFood, fruit, tree.fruit, evs)
	}
	r.advance(pigWalkMax)
	r.settle()
	if ob.hunger != 0 || r.drops.stacks[op].N != 1 {
		t.Fatalf("it ate one: hunger %d, n %d", ob.hunger, r.drops.stacks[op].N)
	}
}
