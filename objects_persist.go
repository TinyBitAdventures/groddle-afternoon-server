package main

// The shared street objects' state file (objects.go, objects_pigs.go): what a
// street's objects are now outlives a restart of this server. A dead tree stays
// dead for everyone (a save that recorded the death keeps its Patch, so the
// world and the saves agree), a poison clock goes on running from where it
// was, a street Piggy keeps its mood, hunger and twice-daily clock (the ticks
// it missed while the server was down run when it is back, as the remake's
// replay of a tick nobody watched does), and rocks, resource nodes, trees and
// Wood Trees keep what was taken and when it comes back.
//
// -objects-file (default realtime-objects.json beside -secret-file; off:
// memory only) is the whole state, written through writeFileSynced within a
// second of a change, and at once when a tree is poisoned, cured or dies (a
// death credits a Wood Tree's planks, which must not happen twice). Who is
// swinging at a rock or digging a pile is not kept: those sessions end with
// the restart. An object whose street or id the layout (data/objects.json) no
// longer has, or whose kind changed, is dropped when the file is read.
//
// Timers are re-armed on the first connect after a start (resume), not when the
// module is built, so a death that came due while the server was down credits
// through the credits module, which is registered by then. Until someone
// connects nothing can see the objects, so nothing is different for anyone.

import (
	"encoding/json"
	"flag"
	"log"
	"sort"
	"time"
)

var objectsFileFlag = flag.String("objects-file", "", "file that keeps the shared street objects: dead and poisoned trees, street Piggies, what was taken from rocks, nodes and trees (default: realtime-objects.json beside -secret-file; off: memory only)")

// objectsFileForTest lets a test give the module a state file; under go test the flag is ignored.
var objectsFileForTest string

// objRec is one touched object as the file keeps it. Times are Unix ms (0: none).
type objRec struct {
	Kind      string          `json:"k"`
	Chunks    int             `json:"chunks,omitempty"`
	Mega      bool            `json:"mega,omitempty"`
	NextMega  bool            `json:"nextMega,omitempty"`
	Units     int             `json:"units,omitempty"`
	Petted    bool            `json:"petted,omitempty"`
	Watered   bool            `json:"watered,omitempty"`
	ResetAt   int64           `json:"resetAt,omitempty"`
	Fruit     int             `json:"fruit,omitempty"`
	Health    int             `json:"health,omitempty"`
	Maturity  int             `json:"maturity,omitempty"`
	Day       int64           `json:"day,omitempty"`
	WoodCount int             `json:"woodCount,omitempty"`
	WoodBy    []int64         `json:"woodBy,omitempty"`
	PoisonEnd int64           `json:"poisonEnd,omitempty"`
	PoisonBy  int64           `json:"poisonBy,omitempty"`
	Dead      bool            `json:"dead,omitempty"`
	DeadBy    int64           `json:"deadBy,omitempty"`
	Mood      int             `json:"mood,omitempty"`
	Hunger    int             `json:"hunger,omitempty"`
	Daily     bool            `json:"daily,omitempty"`
	Croak     bool            `json:"croak,omitempty"`
	TickAt    int64           `json:"tickAt,omitempty"`
	Petters   map[int64]int64 `json:"petters,omitempty"`
	Due       int64           `json:"due,omitempty"`
}

type objFile struct {
	V       int                           `json:"v"`
	Streets map[string]map[string]*objRec `json:"streets"`
}

func msOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func timeOf(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func (o *objects) record(ob *liveObj) *objRec {
	r := &objRec{Kind: ob.spot.kind, Chunks: ob.chunks, Mega: ob.mega, NextMega: ob.nextMega, Units: ob.units,
		Petted: ob.petted, Watered: ob.watered, ResetAt: msOf(ob.resetAt), Fruit: ob.fruit, Health: ob.health,
		Maturity: ob.maturity, Day: ob.day, WoodCount: ob.woodCount, PoisonEnd: msOf(ob.poisonEnd), PoisonBy: ob.poisonBy,
		Dead: ob.dead, DeadBy: ob.deadBy, Mood: ob.mood, Hunger: ob.hunger, Daily: ob.daily, Croak: ob.croak,
		TickAt: msOf(ob.tickAt), Due: msOf(ob.due)}
	for id := range ob.woodBy {
		r.WoodBy = append(r.WoodBy, id)
	}
	sort.Slice(r.WoodBy, func(i, j int) bool { return r.WoodBy[i] < r.WoodBy[j] })
	if len(ob.petters) > 0 {
		r.Petters = make(map[int64]int64, len(ob.petters))
		for id, day := range ob.petters {
			r.Petters[id] = day
		}
	}
	return r
}

// revive is a record back as a live object (no timers yet; resume arms them), or nil when it
// does not fit the spot any more.
func (o *objects) revive(spot *objSpot, r *objRec) *liveObj {
	if r == nil || r.Kind != spot.kind {
		return nil
	}
	clamp := func(v, lo, hi int) int { return min(hi, max(lo, v)) }
	ob := &liveObj{spot: spot, due: timeOf(r.Due)}
	switch spot.kind {
	case "rock":
		rule := o.w.rules.Rocks[spot.class]
		ob.chunks = clamp(r.Chunks, 0, max(rule.Chunks, o.w.rules.Mega.Chunks))
		ob.mega, ob.nextMega = r.Mega, r.NextMega
	case "node":
		rule := o.w.rules.Nodes[spot.class]
		ob.units = clamp(r.Units, rule.Min, rule.Max)
		ob.petted, ob.watered, ob.resetAt = r.Petted, r.Watered, timeOf(r.ResetAt)
	case "tree":
		ob.maturity = clamp(r.Maturity, 1, len(o.w.rules.Trants[spot.class]))
		ob.health = clamp(r.Health, 1, 10)
		ob.cap = o.w.capAt(spot.class, ob.maturity, ob.health)
		ob.fruit = clamp(r.Fruit, 0, ob.cap)
		ob.petted, ob.watered, ob.day = r.Petted, r.Watered, r.Day
	case "wood":
		ob.maturity = clamp(r.Maturity, 0, 6)
		ob.woodCount = clamp(r.WoodCount, 0, 1)
		ob.day = r.Day
		for _, id := range r.WoodBy {
			if ob.woodBy == nil {
				ob.woodBy = map[int64]bool{}
			}
			ob.woodBy[id] = true
		}
	case "pig":
		ob.mood = clamp(r.Mood, 0, pigMoodMax)
		ob.hunger = clamp(r.Hunger, 0, pigHungerMax)
		ob.petted, ob.daily, ob.croak = r.Petted, r.Daily, r.Croak
		ob.tickAt = timeOf(r.TickAt)
		if ob.tickAt.IsZero() {
			ob.tickAt = o.h.Now().Add(pigTick)
		}
		for id, day := range r.Petters {
			if ob.petters == nil {
				ob.petters = map[int64]int64{}
			}
			ob.petters[id] = day
		}
	default:
		return nil
	}
	if spot.kind == "tree" || spot.kind == "wood" {
		ob.dead, ob.deadBy = r.Dead, r.DeadBy
		if ob.dead {
			ob.fruit, ob.petted, ob.watered = 0, false, false
		} else {
			ob.poisonEnd, ob.poisonBy = timeOf(r.PoisonEnd), r.PoisonBy
		}
		ob.due = time.Time{}
	}
	if spot.kind == "pig" {
		ob.due = time.Time{}
	}
	return ob
}

// load reads the state file into o.streets; resume arms the timers later.
func (o *objects) load() error {
	var f objFile
	ok, err := readStateFile("objects", o.path, &f)
	if err != nil || !ok {
		return err
	}
	n, dead, dropped := 0, 0, 0
	for street, recs := range f.Streets {
		spots := o.w.streets[street]
		for id, r := range recs {
			spot := spots[id]
			if spot == nil {
				dropped++
				continue
			}
			ob := o.revive(spot, r)
			if ob == nil {
				dropped++
				continue
			}
			room := o.streets[street]
			if room == nil {
				room = map[string]*liveObj{}
				o.streets[street] = room
			}
			room[id] = ob
			n++
			if ob.dead {
				dead++
			}
		}
	}
	o.resumed = false
	log.Printf("objects %s: %d objects on %d streets (%d dead trees), %d dropped (not in the layout)", o.path, n, len(o.streets), dead, dropped)
	return nil
}

// resume re-arms what the file's objects were waiting for, once, on the first connect (or hook)
// after a start: a rock's respawn, a node's regrowth (the steps missed while the server was down
// are caught up), a Paper Tree's flag reset, a poison clock (a tree whose clock ran out dies now),
// the Piggies' tick (the missed ticks run) and the trees' new day.
func (o *objects) resume() {
	if o.resumed {
		return
	}
	o.resumed = true
	now := o.h.Now()
	streets := make([]string, 0, len(o.streets))
	for street := range o.streets {
		streets = append(streets, street)
	}
	sort.Strings(streets)
	trees, pigs := false, false
	for _, street := range streets {
		room := o.streets[street]
		ids := make([]string, 0, len(room))
		for id := range room {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			ob := room[id]
			switch ob.spot.kind {
			case "rock":
				if !ob.due.IsZero() {
					if d := ob.due.Sub(now); d > 0 {
						o.schedule(street, ob, d, o.respawnRock)
					} else {
						ob.due = time.Time{}
						o.respawnRock(street, ob)
					}
				}
			case "node":
				rule := o.w.rules.Nodes[ob.spot.class]
				if !ob.due.IsZero() {
					due := ob.due
					for !due.After(now) && ob.units < rule.Max {
						ob.units++
						due = due.Add(o.regrowDelay(rule))
					}
					ob.due = time.Time{}
					if ob.units < rule.Max {
						o.schedule(street, ob, due.Sub(now), o.regrowNode)
					}
				}
				if !ob.resetAt.IsZero() {
					if d := ob.resetAt.Sub(now); d > 0 {
						o.armReset(street, ob, d)
					} else {
						ob.resetAt, ob.petted, ob.watered = time.Time{}, false, false
					}
				}
			case "tree", "wood":
				trees = trees || ob.spot.kind == "tree"
				if !ob.dead && !ob.poisonEnd.IsZero() {
					if d := ob.poisonEnd.Sub(now); d > 0 {
						o.armPoison(street, ob, d)
					} else {
						o.killTree(street, ob, ob.poisonBy)
					}
				}
			case "pig":
				pigs = true
			}
		}
		for _, id := range ids {
			if ob := room[id]; ob != nil {
				o.forget(street, ob)
			}
		}
	}
	if trees {
		o.armDay()
	}
	if pigs {
		o.armPigs()
	}
}

// save writes the whole state now; false when it could not (it stays in memory).
func (o *objects) save() bool {
	if o.path == "" {
		return true
	}
	f := objFile{V: 1, Streets: map[string]map[string]*objRec{}}
	for street, room := range o.streets {
		if len(room) == 0 {
			continue
		}
		recs := make(map[string]*objRec, len(room))
		for id, ob := range room {
			recs[id] = o.record(ob)
		}
		f.Streets[street] = recs
	}
	b, err := json.Marshal(f)
	if err == nil {
		err = writeFileSynced(o.path, b)
	}
	if err != nil {
		log.Printf("objects %s: %v", o.path, err)
		return false
	}
	return true
}

// saveSoon writes within a second (a busy street changes often; one write a second is plenty).
func (o *objects) saveSoon() {
	if o.path == "" || o.saveAt {
		return
	}
	o.saveAt = true
	o.h.After(time.Second, func() {
		o.saveAt = false
		o.save()
	})
}

// saveNow writes at once (a poison, a cure, a death) and drops a write that was due.
func (o *objects) saveNow() {
	if o.path == "" {
		return
	}
	o.save()
}

// kept wraps a message handler: the file's timers are armed first, and a save follows.
func (o *objects) kept(fn func(cl *Client, frame json.RawMessage)) func(cl *Client, frame json.RawMessage) {
	return func(cl *Client, frame json.RawMessage) {
		o.resume()
		fn(cl, frame)
		o.saveSoon()
	}
}
