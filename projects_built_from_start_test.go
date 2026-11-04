package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Austin 2026-11-03: Hazy Gate 3 (job 524), the one road into Xalanga, is built in every world
// (data/projects.json "built", the game's tools/street-jobs-built-from-start.json); so are
// Konemaen Jaunt (206), Simo Steer (201) and Kala Close (240) in Jethimadh, the only walkable way to
// Xalanga for To Xalanga, And Beyond (the subway to Onto Parada). Every other project stays closed.
const hazyGate3 = "524"

var builtFromStart = map[string]bool{"201": true, "206": true, "240": true, hazyGate3: true}

func TestBuiltFromStartOnANewWorld(t *testing.T) {
	r := newProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	for id := range builtFromStart {
		if !r.m.t.Jobs[id].Built {
			t.Fatalf("the table marks %s built", id)
		}
		if row := r.m.st.Jobs[id]; row == nil || row.Done <= 0 {
			t.Fatalf("%s done on a new world: %+v", id, row)
		}
	}
	for id, job := range r.m.t.Jobs {
		if job.Built && !builtFromStart[id] {
			t.Fatalf("only the listed projects start built, not %s", id)
		}
	}
	a := r.player(1, "Ada", projBoard)
	built := kinds(drain(a), "project_world")
	if len(built) != 1 {
		t.Fatalf("one project_world: %v", built)
	}
	b := built[0]["built"].(map[string]any)
	for id := range builtFromStart {
		if _, ok := b[id]; !ok {
			t.Fatalf("the world says %s is built: %v", id, b)
		}
	}
	if len(b) != len(builtFromStart) {
		t.Fatalf("and nothing else: %v", b)
	}
	// Its board takes nothing more (projectTextDone).
	board := r.m.t.Jobs[hazyGate3].Offer[0]
	c := r.player(2, "Bo", board)
	drain(c)
	r.do(c, map[string]any{"t": "project_give", "job": hazyGate3, "req": "r1", "n": 1})
	if e := kinds(drain(c), "error"); len(e) != 1 || e[0]["message"] != projectTextDone {
		t.Fatalf("its board takes nothing more: %v", e)
	}
}

func TestBuiltFromStartOnASavedWorldKeepsWhatWasGiven(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	// A world saved before the list: Hazy Gate 3 under way, with something given.
	saved := `{"jobs":{"524":{"phase":0,"got":{"r9":3},"per":[{"mine":[{"id":7,"name":"Cy","cost":12}]}],"started":1700000000000},"206":{"phase":1,"got":{"r2":5},"per":[{"mine":[{"id":8,"name":"Di","cost":30}]}],"started":1700000000000}},"rest":{},"gives":{},"credits":{},"owed":{},"seq":1}`
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1_700_000_100, 0)
	r := newProjRig(t, path, newFakeProjectLedger(), start)
	row := r.m.st.Jobs[hazyGate3]
	if row.Done != start.UnixMilli() || row.Got["r9"] != 3 || len(row.Per) != 1 || row.Per[0].Mine[0].Cost != 12 {
		t.Fatalf("built on load, what was given kept: %+v", row)
	}
	if k := r.m.st.Jobs["206"]; k.Done != start.UnixMilli() || k.Phase != 1 || k.Got["r2"] != 5 || k.Per[0].Mine[0].Cost != 30 {
		t.Fatalf("Konemaen Jaunt under way: built on load, what was given kept: %+v", k)
	}
	for _, id := range []string{"201", "240"} {
		if row := r.m.st.Jobs[id]; row == nil || row.Done != start.UnixMilli() {
			t.Fatalf("%s built on load: %+v", id, row)
		}
	}
	if len(r.m.st.Credits) != 0 || len(r.m.st.Owed) != 0 {
		t.Fatalf("no rewards paid for it: %+v %+v", r.m.st.Credits, r.m.st.Owed)
	}
	// Again: nothing changes the second time (the same done time).
	r2 := newProjRig(t, path, newFakeProjectLedger(), start.Add(time.Hour))
	for id := range builtFromStart {
		if got := r2.m.st.Jobs[id].Done; got != start.UnixMilli() {
			t.Fatalf("idempotent: %s done %d, want %d", id, got, start.UnixMilli())
		}
	}
}
