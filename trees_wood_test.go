package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testdata/wood-care.json: Wood Tree care steps shared with the game's tests/wood-tree-care.test.js,
// so a street Wood Tree and a shared Patch's (woodTend) count care exactly as solo play does
// (js/trant-care.js performTrantCare, js/woodcutting.js noteWoodCare).
func TestWoodCareMatchesTheGame(t *testing.T) {
	raw, err := os.ReadFile("testdata/wood-care.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string `json:"name"`
		Maturity int    `json:"maturity"`
		Steps    []struct {
			At     int64   `json:"at"`
			Who    int64   `json:"who"`
			Verb   string  `json:"verb"`
			Roll   float64 `json:"roll"`
			Expect string  `json:"expect"`
		} `json:"steps"`
		End struct {
			Maturity int  `json:"maturity"`
			Pets     int  `json:"pets"`
			Waters   int  `json:"waters"`
			Petted   bool `json:"petted"`
			Watered  bool `json:"watered"`
		} `json:"end"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 5 {
		t.Fatalf("%d cases", len(cases))
	}
	rules := sharedWorld.rules
	const base = int64(1_800_000_000_000)
	for _, c := range cases {
		tr := treeCore{maturity: c.Maturity}
		var care woodCare
		count := 1
		for i, s := range c.Steps {
			v := woodTend(&tr, &care, &count, s.Verb, s.Who, base+s.At, rules.WoodCare[s.Verb]["no_skill"], s.Roll, rules.WoodMap)
			got := "ok"
			switch {
			case !v.ok:
				got = v.code
				want := "I don't want to be petted right now!"
				if s.Verb == "water" {
					want = "I don't want to be watered right now!"
				}
				if v.code == "not_wanted" && v.message != want {
					t.Fatalf("%s step %d: %q", c.Name, i, v.message)
				}
			case v.extra["success"] != true:
				got = "failed"
			case v.extra["matured"] == true:
				got = "grew"
				if v.extra["maturity"] != tr.maturity || count != 0 {
					t.Fatalf("%s step %d: matured %v, count %d", c.Name, i, v.extra, count)
				}
			}
			if got != s.Expect {
				t.Fatalf("%s step %d (%s by %d at %d): got %s, want %s", c.Name, i, s.Verb, s.Who, s.At, got, s.Expect)
			}
		}
		if tr.maturity != c.End.Maturity || care.pets != c.End.Pets || care.waters != c.End.Waters || tr.petted != c.End.Petted || tr.watered != c.End.Watered {
			t.Fatalf("%s: ends %+v %+v, want %+v", c.Name, tr, care, c.End)
		}
	}
}

func TestWoodCareRefusesAClearedTreeAndHarvestStartsTheCountsOver(t *testing.T) {
	tr := treeCore{maturity: 0}
	var care woodCare
	count := 0
	if v := woodTend(&tr, &care, &count, "pet", 1, 1000, 100, 0, sharedWorld.rules.WoodMap); v.ok || v.code != "cleared" {
		t.Fatalf("a cleared tree: %+v", v)
	}
	tr.maturity = 5
	woodTend(&tr, &care, &count, "pet", 1, 1000, 100, 0, sharedWorld.rules.WoodMap)
	woodTend(&tr, &care, &count, "water", 1, 1001, 100, 0, sharedWorld.rules.WoodMap)
	if care.pets != 1 || care.waters != 1 {
		t.Fatalf("care %+v", care)
	}
	var by map[int64]bool
	if v := woodChop(&tr, &care, &count, &by, 1); !v.ok || tr.maturity != 5 || care.pets != 0 || care.waters != 0 {
		t.Fatalf("a harvest the tree survives (endHarvest 783-786): %+v %+v", tr, care)
	}
	// Each player's clocks are their own and outlive the harvest.
	if woodCareWait(&care, "pet", 1, 1002) <= 0 || woodCareWait(&care, "pet", 2, 1002) != 0 {
		t.Fatal("the clocks are per player")
	}
}

// streetWood is the first street Wood Tree in the layout.
func streetWood(t *testing.T) (string, string, *objSpot) {
	var street, id string
	var spot *objSpot
	for tsid, spots := range sharedWorld.streets {
		for sid, sp := range spots {
			if sp.kind == "wood" && (street == "" || tsid < street || (tsid == street && sid < id)) {
				street, id, spot = tsid, sid, sp
			}
		}
	}
	if spot == nil {
		t.Fatal("no street Wood Tree in the layout")
	}
	return street, id, spot
}

func TestAStreetWoodTreeGrowsByEveryonesCare(t *testing.T) {
	street, id, spot := streetWood(t)
	r := newObjRig(t)
	r.mod.roll = func() float64 { return 0 }
	r.mod.get(street, spot).maturity = 1
	a := r.player(1, "Ada", street, spot.x, spot.y)
	b := r.player(2, "Bo", street, spot.x, spot.y)
	r.drain(a)
	r.drain(b)
	tree := func(m map[string]any) map[string]any { return objOf(m, id) }
	r.act(a, 1, id, "pet", nil)
	if got := tree(r.only(a, "obj_ok")); num(got["pets"]) != 1 || got["petted"] != true {
		t.Fatalf("Ada's pet: %v", got)
	}
	if got := tree(r.only(b, "objs")); num(got["pets"]) != 1 {
		t.Fatalf("Bo sees %v", got)
	}
	r.act(a, 2, id, "pet", nil)
	if no := r.only(a, "obj_no"); no["code"] != "not_wanted" || no["message"] != "I don't want to be petted right now!" {
		t.Fatalf("Ada's second pet inside 5 minutes: %v", no)
	}
	// Bo's clock is his own, and his care counts toward the same tree.
	r.drain(b)
	r.act(b, 1, id, "pet", nil)
	r.only(b, "obj_ok")
	r.act(b, 2, id, "water", nil)
	r.only(b, "obj_ok")
	r.drain(a)
	r.act(a, 3, id, "water", nil)
	ok := r.only(a, "obj_ok")
	if ok["matured"] != true || num(ok["maturity"]) != 2 {
		t.Fatalf("two pets and two waters grow a maturity-1 tree (maturityMap[0] = 2): %v", ok)
	}
	if got := tree(ok); num(got["maturity"]) != 2 || got["pets"] != nil && num(got["pets"]) != 0 {
		t.Fatalf("the tree after growing: %v", got)
	}
	// Five minutes on, Ada may pet again; no game day needed.
	r.advance(5 * time.Minute)
	r.drain(a)
	r.act(a, 4, id, "pet", nil)
	if got := tree(r.only(a, "obj_ok")); num(got["pets"]) != 1 || num(got["maturity"]) != 2 {
		t.Fatalf("Ada five minutes on: %v", got)
	}
	// A failed roll changes nothing and starts no clock.
	r.mod.roll = func() float64 { return 0.95 }
	r.drain(b)
	r.act(b, 3, id, "pet", nil)
	if ok := r.only(b, "obj_ok"); ok["success"] != false || num(tree(ok)["pets"]) != 1 {
		t.Fatalf("a failed pet: %v", ok)
	}
}

func TestAStreetWoodTreeChoppedToZeroDiesForEveryone(t *testing.T) {
	street, id, spot := streetWood(t)
	r := newObjRig(t)
	got := r.credited()
	ob := r.mod.get(street, spot)
	ob.maturity = 1
	a := r.player(1, "Ada", street, spot.x, spot.y)
	b := r.player(2, "Bo", street, spot.x, spot.y)
	r.act(a, 1, id, "harvest", nil)
	if s := objOf(r.only(a, "obj_ok"), id); num(s["maturity"]) != 0 || s["dead"] == true {
		t.Fatalf("the wee tree's harvest: %v", s)
	}
	r.drain(b)
	r.advance(time.Second)
	if fr := r.drain(b); len(fr) != 0 {
		t.Fatalf("onDie waits two seconds: %v", fr)
	}
	r.advance(time.Second)
	if dead := objOf(r.only(b, "objs"), id); dead == nil || dead["dead"] != true || num(dead["by"]) != 0 {
		t.Fatalf("Bo saw %v", dead)
	}
	// The harvest's own 25 Planks only: a chopped tree's death leaves none (only a poisoned one does).
	if len(*got) != 1 || (*got)[0] != "1:take:plankx25" {
		t.Fatalf("credited %v", *got)
	}
}

func TestWoodCareOutlivesARestart(t *testing.T) {
	street, id, spot := streetWood(t)
	path := filepath.Join(t.TempDir(), "objects.json")
	r := newObjRigAt(t, path, time.Time{})
	r.mod.roll = func() float64 { return 0 }
	r.mod.get(street, spot).maturity = 3
	a := r.player(1, "Ada", street, spot.x, spot.y)
	r.act(a, 1, id, "pet", nil)
	r.only(a, "obj_ok")
	r.act(a, 2, id, "water", nil)
	r.only(a, "obj_ok")
	r.mod.saveNow()
	r = r.restart(path, r.now.Add(time.Minute))
	r.mod.roll = func() float64 { return 0 }
	a = r.player(1, "Ada", street, spot.x, spot.y)
	if s := syncOf(r, a, id); num(s["pets"]) != 1 || num(s["waters"]) != 1 || s["petted"] != true || num(s["maturity"]) != 3 {
		t.Fatalf("after the restart: %v", s)
	}
	r.act(a, 3, id, "pet", nil)
	if no := r.only(a, "obj_no"); no["code"] != "not_wanted" {
		t.Fatalf("Ada's clock outlived the restart: %v", no)
	}
	// A file from the daily model: both marks and no reset time block nothing and count nothing.
	ob := r.mod.revive(spot, &objRec{Kind: "wood", Maturity: 4, Petted: true, Watered: true})
	if ob == nil || ob.petted || ob.watered || !woodCareFresh(&ob.treeCore, &ob.woodCare) {
		t.Fatalf("an old file's wood: %+v", ob)
	}
}

func TestAPatchWoodTreeTakesCareEveryFiveMinutes(t *testing.T) {
	r := newPatchRig(t, false)
	r.shareAll()
	const doon = "GA9S90EJ0G8241N"
	st := r.g.w.streets[doon]
	if st == nil || !r.g.w.canPlant("wood_tree", "patch", st.hub) {
		t.Skip("the fixture street changed")
	}
	a := r.at(1, "Ada", doon, "patch:0")
	b := r.at(2, "Bo", doon, "patch:0")
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
	tr := r.live(doon, "patch:0").tree
	if tr == nil || !tr.wood() || tr.maturity != 1 {
		t.Fatalf("the Wood Tree is %+v", tr)
	}
	r.g.roll = func() float64 { return 0 }
	r.drain(b)
	r.pact(a, 10, "patch:0", "pet", nil)
	r.ok(a)
	r.pact(a, 11, "patch:0", "pet", nil)
	if no := r.no(a, "not_wanted"); no["message"] != "I don't want to be petted right now!" {
		t.Fatal(no)
	}
	r.drain(b)
	r.pact(b, 12, "patch:0", "pet", nil)
	r.ok(b)
	r.pact(a, 13, "patch:0", "water", nil)
	r.ok(a)
	r.pact(b, 14, "patch:0", "water", nil)
	if ok := r.ok(b); ok["matured"] != true || ok["maturity"] != float64(2) {
		t.Fatalf("Ada's and Bo's care together grow it: %v", ok)
	}
	// A new game day clears nothing: the marks run on their own 10 minutes.
	tr.woodCare.pets = 1
	next := gameEpochMs + (r.g.today()+1)*gameDayMs
	r.advance(time.Duration(next-r.now.UnixMilli()+100) * time.Millisecond)
	if tr.woodCare.pets != 1 {
		t.Fatalf("the day turn kept the counts: %+v", tr.woodCare)
	}
}
