package main

import (
	"path/filepath"
	"testing"
	"time"
)

// A street Piggy's name is everyone's (objects_pigs.go namePig; items/include/animal_naming.js).
func TestAStreetPiggysNameIsEveryones(t *testing.T) {
	r := newObjRig(t)
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	b := r.player(2, "Bo", defaultStreet, pig.x+100, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	ak5 := []string{"ak5"}

	// canName 6: Animal Kinship V first.
	r.act(a, 1, "pig:2", "name", map[string]any{"name": "Wilbur"})
	if no := r.only(a, "obj_no"); no["code"] != "skill" || no["message"] != pigNameSkill {
		t.Fatalf("a name without Animal Kinship V answered %v", no)
	}
	// onInputBoxResponse 41: 32 characters at most, only a-z, 0-9 and spaces.
	r.now = r.now.Add(time.Second)
	r.act(a, 2, "pig:2", "name", map[string]any{"name": "Wil-bur! the 2nd, of the Grand Mile and beyond", "perks": ak5})
	ok := r.only(a, "obj_ok")
	want := "Wilbur the 2nd of the Grand M" // cut at 32 first, then stripped (41)
	if ok["name"] != want || objOf(ok, "pig:2")["name"] != want || num(objOf(ok, "pig:2")["namer"]) != 1 {
		t.Fatalf("the name answered %v", ok)
	}
	// Bo sees the name in the Piggy's state and hears it named (sendBubble with no pc, 49).
	var ev, objs map[string]any
	for _, f := range r.drain(b) {
		switch f["t"] {
		case "obj_ev":
			ev = f
		case "objs":
			objs = f
		}
	}
	if ev == nil || ev["ev"] != "name" || ev["name"] != want || num(ev["by"]) != 1 || ev["id"] != "pig:2" {
		t.Fatalf("Bo heard %v", ev)
	}
	if p := objOf(objs, "pig:2"); p == nil || p["name"] != want {
		t.Fatalf("Bo sees %v", objs)
	}
	// canName 12-18: Bo may not rename it within the hour; Ada, who named it, may.
	r.now = r.now.Add(time.Minute)
	r.act(b, 1, "pig:2", "name", map[string]any{"name": "Babe", "perks": ak5})
	if no := r.only(b, "obj_no"); no["code"] != "named" || no["message"] != nameTaken("pig") {
		t.Fatalf("Bo's rename within the hour answered %v", no)
	}
	r.act(a, 3, "pig:2", "name", map[string]any{"name": "Wilbur", "perks": ak5})
	if ok := r.only(a, "obj_ok"); ok["name"] != "Wilbur" {
		t.Fatalf("Ada's rename answered %v", ok)
	}
	r.drain(b)
	r.now = r.now.Add(time.Second)
	r.act(a, 4, "pig:2", "name", map[string]any{"name": "Wilbur", "perks": ak5})
	if no := r.only(a, "obj_no"); no["code"] != "unchanged" {
		t.Fatalf("the same name again answered %v", no)
	}
	r.act(a, 5, "pig:2", "name", map[string]any{"name": "!!!", "perks": ak5})
	if no := r.only(a, "obj_no"); no["code"] != "empty" || no["message"] != pigNameEmpty {
		t.Fatalf("a name of no letters answered %v", no)
	}
	// An hour on, anyone may: Bo's name, through the word filter.
	r.now = r.now.Add(pigRenameAfter + time.Second)
	r.act(b, 2, "pig:2", "name", map[string]any{"name": "fuck face", "perks": ak5})
	ok = r.only(b, "obj_ok")
	if ok["name"] != "#!@$ face" || num(objOf(ok, "pig:2")["namer"]) != 2 {
		t.Fatalf("Bo's name an hour on answered %v", ok)
	}
	if p := syncOf(r, a, "pig:2"); p["name"] != "#!@$ face" || num(p["named"]) != 0 {
		t.Fatalf("Ada's snapshot reads %v", p)
	}
	r.now = r.now.Add(90 * time.Second)
	if p := syncOf(r, a, "pig:2"); num(p["named"]) != 90_000 {
		t.Fatalf("named reads %v, want 90000 ms ago", p["named"])
	}
}

// verbs.name 764-766: a sad Piggy refuses a first name; rename has no such check.
func TestASadPiggyRefusesAFirstName(t *testing.T) {
	r := newObjRig(t)
	street := ""
	for tsid := range sharedWorld.sad {
		if sharedWorld.streets[tsid]["pig:0"] != nil && (street == "" || tsid < street) {
			street = tsid
		}
	}
	if street == "" {
		t.Fatal("no sad street lays out a Piggy")
	}
	pig := sharedWorld.streets[street]["pig:0"]
	a := r.player(1, "Ada", street, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	r.act(a, 1, "pig:0", "name", map[string]any{"name": "Gloom", "perks": []string{"ak5"}})
	if no := r.only(a, "obj_no"); no["code"] != "sad" || no["message"] != pigNameSad {
		t.Fatalf("a sad Piggy's first name answered %v", no)
	}
	// Named before (a restart's record, say): a rename goes through.
	r.mod.streets[street]["pig:0"].userName = "Glum"
	r.mod.streets[street]["pig:0"].namer = 1
	r.act(a, 2, "pig:0", "name", map[string]any{"name": "Gloom", "perks": []string{"ak5"}})
	if ok := r.only(a, "obj_ok"); ok["name"] != "Gloom" {
		t.Fatalf("a sad Piggy's rename answered %v", ok)
	}
}

func TestAPiggysNameOutlivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	pig := sharedWorld.streets[defaultStreet]["pig:1"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	r.act(a, 1, "pig:1", "name", map[string]any{"name": "Truffle", "perks": []string{"ak5"}})
	if ok := r.only(a, "obj_ok"); ok["name"] != "Truffle" {
		t.Fatalf("the name answered %v", ok)
	}
	at := r.now.Add(10 * time.Minute)
	r2 := r.restart(path, at)
	b := r2.player(2, "Bo", defaultStreet, pig.x, pig.y)
	if p := syncOf(r2, b, "pig:1"); p["name"] != "Truffle" || num(p["namer"]) != 1 || num(p["named"]) < float64((10*time.Minute).Milliseconds()) {
		t.Fatalf("after a restart the Piggy reads %v", p)
	}
	// Still within the hour of Ada's naming: Bo waits.
	r2.act(b, 1, "pig:1", "name", map[string]any{"name": "Snout", "perks": []string{"ak5"}})
	if no := r2.only(b, "obj_no"); no["code"] != "named" {
		t.Fatalf("Bo's rename after the restart answered %v", no)
	}
}

func TestSanitizeAnimalName(t *testing.T) {
	for in, want := range map[string]string{
		"Wilbur":                               "Wilbur",
		"  Mr. Snuffles  ":                     "  Mr Snuffles  ",
		"Ünïcödé":                              "ncd",
		"abcdefghijklmnopqrstuvwxyz0123456789": "abcdefghijklmnopqrstuvwxyz012345",
	} {
		if got := sanitizeAnimalName(in); got != want {
			t.Errorf("sanitizeAnimalName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The street's Chickens and Butterflies take the same include (npc_chicken.js 677/707,
// npc_butterfly.js 471/506): named by the same rules, with no sad check; kept across a restart.
func TestAStreetChickensAndButterflysNameIsEveryones(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	ck := sharedWorld.streets[defaultStreet]["chicken:0"]
	bf := sharedWorld.streets[defaultStreet]["butterfly:0"]
	if ck == nil || bf == nil {
		t.Fatal("the default street lays out no Chicken or Butterfly")
	}
	a := r.player(1, "Ada", defaultStreet, ck.x, ck.y)
	b := r.player(2, "Bo", defaultStreet, ck.x+100, ck.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	ak5 := []string{"ak5"}
	r.act(a, 1, "chicken:0", "name", map[string]any{"name": "Henrietta"})
	if no := r.only(a, "obj_no"); no["code"] != "skill" {
		t.Fatalf("a name without Animal Kinship V answered %v", no)
	}
	r.act(a, 2, "chicken:0", "name", map[string]any{"name": "Henrietta", "perks": ak5})
	if ok := r.only(a, "obj_ok"); ok["name"] != "Henrietta" || objOf(ok, "chicken:0")["name"] != "Henrietta" {
		t.Fatalf("the Chicken's name answered %v", ok)
	}
	var ev map[string]any
	for _, f := range r.drain(b) {
		if f["t"] == "obj_ev" {
			ev = f
		}
	}
	if ev == nil || ev["ev"] != "name" || ev["name"] != "Henrietta" || ev["id"] != "chicken:0" {
		t.Fatalf("Bo heard %v", ev)
	}
	r.act(b, 1, "chicken:0", "name", map[string]any{"name": "Clucky", "perks": ak5})
	if no := r.only(b, "obj_no"); no["code"] != "named" || no["message"] != "Someone named this Chicken less than an hour ago." {
		t.Fatalf("Bo's rename answered %v", no)
	}
	r.act(a, 3, "butterfly:0", "name", map[string]any{"name": "Flutter", "perks": ak5})
	if ok := r.only(a, "obj_ok"); ok["name"] != "Flutter" {
		t.Fatalf("the Butterfly's name answered %v", ok)
	}
	r2 := r.restart(path, r.now.Add(time.Minute))
	c := r2.player(3, "Cy", defaultStreet, ck.x, ck.y)
	r2.send(c, map[string]any{"t": "obj_sync"})
	snap := r2.only(c, "objs")
	if p := objOf(snap, "chicken:0"); p == nil || p["name"] != "Henrietta" || num(p["namer"]) != 1 {
		t.Fatalf("after a restart the Chicken reads %v", p)
	}
	if p := objOf(snap, "butterfly:0"); p == nil || p["name"] != "Flutter" {
		t.Fatalf("after a restart the Butterfly reads %v", p)
	}
}
