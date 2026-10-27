package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newRookTreeRigAt is newRookTreeRig with both modules on state files in dir, its clock at now
// (zero: the rig's own) from the start, so the files are read at that time.
func newRookTreeRigAt(t *testing.T, dir string, now time.Time) (*rookRig, *objects) {
	t.Helper()
	so, sr := objectsFileForTest, rookFileForTest
	objectsFileForTest, rookFileForTest = filepath.Join(dir, "objects.json"), filepath.Join(dir, "rook.json")
	defer func() { objectsFileForTest, rookFileForTest = so, sr }()
	r := &rookRig{t: t, now: time.UnixMilli(gameEpochMs + 1000*gameDayMs + 3_600_000), roll: 0.5}
	if !now.IsZero() {
		r.now = now
	}
	var objs *objects
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { objs = newObjects(h).(*objects); return objs },
		func(h *Hub) Module { r.mod = newRook(h).(*rook); return r.mod },
	}
	defer func() { modules = saved }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	r.mod.roll = func() float64 { return r.roll }
	r.mod.dev = true
	objs.roll = func() float64 { return 0.5 }
	return r, objs
}

func lastNo(frames []map[string]any) map[string]any {
	var no map[string]any
	for _, f := range frames {
		if f["t"] == "obj_no" {
			no = f
		}
	}
	return no
}

func TestARookedTreeStaysRookedAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	r, _ := newRookTreeRigAt(t, dir, time.Time{})
	spot := sharedWorld.streets[rookTestStreet]["tree:0"]
	if spot == nil {
		t.Skip("the Rook's test street has no shared tree")
	}
	a := r.player(1, "Ada")
	key := spot.class + ":0"
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{key, "npc_piggy:0"}, "objs": map[string]string{key: "tree:0"}})
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.roll = 0.4 // everything rooked
	r.advance(rookWingBeat)
	r.drain(a)
	// One dose of three before the restart.
	r.send(a, map[string]any{"t": "rook_revive", "key": key})
	if y := rookYous(r.drain(a))["revive"]; rnum(y["doses"]) != 1 {
		t.Fatalf("first dose: %v", y)
	}
	r.advance(time.Second) // the objects' save
	// The server restarts: the fight is over, the marks are not.
	r, objs := newRookTreeRigAt(t, dir, r.now.Add(time.Minute))
	b := r.player(2, "Bo")
	r.send(b, map[string]any{"t": "rook_sync", "keys": []string{key}})
	frames := r.drain(b)
	var rooked map[string]any
	for _, f := range frames {
		if f["t"] == "rook" {
			rooked, _ = f["rooked"].(map[string]any)
			if f["attack"] != nil {
				t.Fatalf("a fight outlived the restart: %v", f["attack"])
			}
		}
	}
	if rk, _ := rooked[key].(map[string]any); rk == nil || rnum(rk["doses"]) != 1 || rooked["npc_piggy:0"] == nil {
		t.Fatalf("after the restart the rooked things read %v", rooked)
	}
	if !objs.rooked(rookTestStreet, "tree:0") {
		t.Fatal("the tree is not rooked after the restart")
	}
	r.send(b, map[string]any{"t": "move", "x": spot.x, "y": spot.y})
	r.send(b, map[string]any{"t": "obj_act", "req": 1, "id": "tree:0", "verb": "water"})
	if no := lastNo(r.drain(b)); no == nil || no["code"] != "rooked" || num(objOf(no, "tree:0")["fruit"]) != 0 {
		t.Fatalf("watering it after the restart answered %v", no)
	}
	// G361: the rooked tree's fruit push the restart stopped goes on (objects.go get re-arms it).
	r.advance(rookFruitEvery)
	if tr := objs.streets[rookTestStreet]["tree:0"]; tr == nil || tr.fruit == 0 {
		t.Fatalf("the rooked tree grew no fruit after the restart: %+v", tr)
	}
	// Ada's dose counted: she cannot give a second; two more finish it, and that is kept too.
	a = r.player(1, "Ada")
	r.send(a, map[string]any{"t": "rook_revive", "key": key})
	if code := rookLastNo(r.drain(a)); code != "done" {
		t.Fatalf("Ada's second dose: %q", code)
	}
	r.send(b, map[string]any{"t": "rook_revive", "key": key})
	c := r.player(3, "Cy")
	r.send(c, map[string]any{"t": "rook_revive", "key": key})
	if y := rookYous(r.drain(c))["revive"]; y["done"] != true {
		t.Fatalf("the third dose: %v", y)
	}
	r, objs = newRookTreeRigAt(t, dir, r.now.Add(time.Minute))
	if objs.rooked(rookTestStreet, "tree:0") || r.mod.streets[rookTestStreet].rooked["npc_piggy:0"] == nil {
		t.Fatalf("after the revive and a restart: %v", r.mod.streets[rookTestStreet].rooked)
	}
}

func TestARestingStreetRestsAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	r, _ := newRookTreeRigAt(t, dir, time.Time{})
	s := r.mod.street(rookTestStreet)
	s.restUntil = r.now.Add(rookStreetRest)
	r.mod.save()
	r2, _ := newRookTreeRigAt(t, dir, r.now.Add(10*time.Minute))
	if got := r2.mod.streets[rookTestStreet]; got == nil || !got.restUntil.Equal(s.restUntil) {
		t.Fatalf("the rest after a restart: %+v", got)
	}
	// Past its rest, the street is not kept.
	r3, _ := newRookTreeRigAt(t, dir, r.now.Add(rookStreetRest+time.Minute))
	if got := r3.mod.streets[rookTestStreet]; got != nil {
		t.Fatalf("a rested street came back: %+v", got)
	}
	// A bad file starts the Rook empty.
	os.WriteFile(filepath.Join(dir, "rook.json"), []byte("nope"), 0o600)
	r4, _ := newRookTreeRigAt(t, dir, r.now)
	if len(r4.mod.streets) != 0 || r4.mod.path == "" {
		t.Fatalf("a bad file: %v (path %q)", r4.mod.streets, r4.mod.path)
	}
}
