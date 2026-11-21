package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #576 (G1082): a Party Space's instance (PARTY_<id>) has its template street's rocks, trees and
// nodes, shared by everyone in it (Help mine), each party its own, gone when the space ends.

func newPartyObjRig(t *testing.T) (*spaceRig, *objects) {
	var objs *objects
	r := newSpaceRig(t, func(h *Hub) Module { objs = newObjects(h).(*objects); return objs })
	objs.roll = func() float64 { return 0.5 }
	return r, objs
}

// pairInSpace: two players in a party, standing in its Toxic Moon space by rock:0.
func pairInSpace(r *spaceRig, a, b *Client) string {
	r.t.Helper()
	r.join(a, "GSTREET1")
	r.join(b, "GSTREET1")
	r.party(a, b)
	street := r.open(a, "party_pack_toxic_moon")
	rock := sharedWorld.streets[moonStreet]["rock:0"]
	for _, cl := range []*Client{a, b} {
		r.do(cl, map[string]any{"t": "join", "street": street, "x": rock.x, "y": rock.y})
	}
	drain(a)
	drain(b)
	return street
}

func objsIn(msgs []map[string]any) []map[string]any {
	var out []map[string]any
	for _, m := range kinds(msgs, "objs") {
		list, _ := m["objs"].([]any)
		for _, raw := range list {
			if o, ok := raw.(map[string]any); ok {
				out = append(out, o)
			}
		}
	}
	return out
}

func TestAPartySpaceSharesItsTemplatesRocksWithinThePartyOnly(t *testing.T) {
	if sharedWorld.streets[moonStreet]["rock:0"] == nil {
		t.Fatal("data/objects.json has no rock:0 on Toxic Moon's street")
	}
	r, objs := newPartyObjRig(t)
	a, b := r.connect(1, "Ada"), r.connect(2, "Bo")
	c, d := r.connect(3, "Cy"), r.connect(4, "Di")
	one := pairInSpace(r, a, b)
	two := pairInSpace(r, c, d)
	if one == two || !strings.HasPrefix(one, partySpaceStreet) || !strings.HasPrefix(two, partySpaceStreet) {
		t.Fatalf("two parties, two spaces: %s %s", one, two)
	}

	// The space's street has its template's layout: not "none".
	r.do(a, map[string]any{"t": "obj_sync"})
	if snap := one2(t, drain(a), "objs"); snap["none"] == true || snap["street"] != one {
		t.Fatalf("the party space answered no shared objects: %v", snap)
	}

	// Ada swings at rock:0: Bo, in the same space, sees her on it and helps (Help mine).
	r.do(a, map[string]any{"t": "obj_mine", "id": "rock:0", "do": "start"})
	if got := one2(t, drain(a), "obj_mine"); got["helping"] != false {
		t.Fatalf("Ada starts alone: %v", got)
	}
	seen := objsIn(drain(b))
	if len(seen) != 1 || fmt.Sprint(seen[0]["miners"]) != "[1]" {
		t.Fatalf("Bo does not see Ada mining the party's rock: %v", seen)
	}
	r.do(b, map[string]any{"t": "obj_mine", "id": "rock:0", "do": "start"})
	if got := one2(t, drain(b), "obj_mine"); got["helping"] != true {
		t.Fatalf("Bo's swing is not a help: %v", got)
	}
	if got := objsIn(drain(a)); len(got) != 1 || fmt.Sprint(got[0]["miners"]) != "[1 2]" {
		t.Fatalf("Ada does not see Bo join her on the rock: %v", got)
	}

	// The other party, on the same template, sees none of it and has its own rock:0.
	if got := objsIn(drain(c)); len(got) != 0 {
		t.Fatalf("the other party heard this party's rock: %v", got)
	}
	r.do(c, map[string]any{"t": "obj_sync"})
	snap := one2(t, drain(c), "objs")
	if snap["none"] == true || snap["street"] != two || len(objsIn([]map[string]any{snap})) != 0 {
		t.Fatalf("the other party's rocks are not fresh and its own: %v", snap)
	}
	r.do(c, map[string]any{"t": "obj_mine", "id": "rock:0", "do": "start"})
	if got := one2(t, drain(c), "obj_mine"); got["helping"] != false {
		t.Fatalf("Cy helps a swing from another party: %v", got)
	}
	if got := objsIn(drain(a)); len(got) != 0 {
		t.Fatalf("this party heard the other party's rock: %v", got)
	}

	// Ada's swing lands with Bo's help, and the chunks are gone for the party.
	r.do(a, map[string]any{"t": "obj_act", "req": 1, "id": "rock:0", "verb": "mine"})
	ok := one2(t, drain(a), "obj_ok")
	if help, _ := ok["help"].(map[string]any); help == nil || fmt.Sprint(help["by"]) != "[Bo]" {
		t.Fatalf("Ada's swing in the party had no help: %v", ok)
	}
	left := objOf(ok, "rock:0")["chunks"]
	if got := objsIn(drain(b)); len(got) == 0 || got[len(got)-1]["chunks"] != left {
		t.Fatalf("Bo does not see the chunks Ada took: %v", got)
	}
	if objs.streets[two]["rock:0"] == nil || objs.streets[two]["rock:0"].chunks == objs.streets[one]["rock:0"].chunks {
		t.Fatal("the two parties share one rock:0")
	}

	// The template street itself is untouched.
	if objs.streets[moonStreet] != nil {
		t.Fatalf("the template street got live objects: %v", objs.streets[moonStreet])
	}

	// The space ends (Bo leaves, so the party is gone): its objects go with it, the other's stay.
	r.do(b, map[string]any{"t": "party_leave"})
	if r.m.spaces.byStreet[one] != nil {
		t.Fatal("the space outlived its party")
	}
	if objs.streets[one] != nil {
		t.Fatalf("the ended space's objects were kept: %v", objs.streets[one])
	}
	if objs.streets[two]["rock:0"] == nil {
		t.Fatal("the other party lost its rock when this one ended")
	}
}

func TestAPartySpacesObjectsAreNeverWrittenToTheStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "objects.json")
	saved := objectsFileForTest
	objectsFileForTest = path
	defer func() { objectsFileForTest = saved }()
	r, objs := newPartyObjRig(t)
	a, b := r.connect(1, "Ada"), r.connect(2, "Bo")
	street := pairInSpace(r, a, b)
	r.do(a, map[string]any{"t": "obj_act", "req": 1, "id": "rock:0", "verb": "mine"})
	if one2(t, drain(a), "obj_ok") == nil || objs.streets[street]["rock:0"] == nil {
		t.Fatal("Ada could not mine the party's rock")
	}
	if !objs.save() {
		t.Fatal("save failed")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Streets map[string]any `json:"streets"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for s := range f.Streets {
		if strings.HasPrefix(s, partySpaceStreet) {
			t.Fatalf("a Party Space's objects were saved: %s", s)
		}
	}
}

func TestOnlyAPartySpaceTakesItsTemplatesLayout(t *testing.T) {
	r, objs := newPartyObjRig(t)
	a, b := r.connect(1, "Ada"), r.connect(2, "Bo")
	street := pairInSpace(r, a, b)
	if objs.layoutOf(street) != moonStreet {
		t.Fatalf("%s lays out as %s", street, objs.layoutOf(street))
	}
	for _, s := range []string{"PARTY_NOSUCHSPACE", "HOME_1", defaultStreet, "RACE_1"} {
		if got := objs.layoutOf(s); got != s {
			t.Fatalf("%s lays out as %s", s, got)
		}
	}
	if objs.spots("PARTY_NOSUCHSPACE") != nil {
		t.Fatal("a space that does not exist has objects")
	}
}
