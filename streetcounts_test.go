package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// thicket is Thicket in Groddle Forest (report #686): a Bean Tree, a Bubble Tree, a Gas Plant and
// three Wood Trees as laid out; its Patches are not shared.
const thicket = "GLI23Q4NDHD11EK"

// streetCounts runs the site's call on a hub and decodes the answer.
func streetCounts(t *testing.T, h *Hub) (int, map[string]streetTally, float64) {
	t.Helper()
	code, out := serviceHandlers["street_counts"](h, []byte(`{}`))
	raw, _ := json.Marshal(out)
	var m struct {
		Status  string                 `json:"status"`
		At      float64                `json:"at"`
		Streets map[string]streetTally `json:"streets"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Status != "ok" {
		t.Fatalf("answer %d %s (%v)", code, raw, err)
	}
	return code, m.Streets, m.At
}

func TestStreetCountsTellStandingTreesFromFelledOnes(t *testing.T) {
	r := newObjRig(t)
	code, streets, at := streetCounts(t, r.hub)
	if code != http.StatusOK || int64(at) != r.now.UnixMilli() {
		t.Fatalf("answer %d at %v", code, at)
	}
	want := map[string]treeTally{"trant_bean": {1, 0}, "trant_bubble": {1, 0}, "trant_gas": {1, 0}, "wood_tree": {3, 0}}
	if got := streets[thicket]; !sameTrees(got.Trees, want) || got.Patches != nil {
		t.Fatalf("a fresh Thicket: %+v", got)
	}
	// Every street with trees is listed, with each tree as laid out.
	for street, spots := range sharedWorld.streets {
		n := 0
		for _, s := range spots {
			if s.kind == "tree" || s.kind == "wood" {
				n++
			}
		}
		got := 0
		for _, c := range streets[street].Trees {
			got += c[0] + c[1]
		}
		if got != n {
			t.Fatalf("%s: %d trees counted, %d laid out", street, got, n)
		}
	}
	// All three Wood Trees felled, as on the live server: one dead, one chopped to 0 and falling in
	// its two seconds, one dead after a restart's resume.
	var woods []*objSpot
	for _, s := range sharedWorld.streets[thicket] {
		if s.kind == "wood" {
			woods = append(woods, s)
		}
	}
	if len(woods) != 3 {
		t.Fatalf("Thicket has %d Wood Trees", len(woods))
	}
	r.mod.killTree(thicket, r.mod.get(thicket, woods[0]), 0)
	r.mod.get(thicket, woods[1]).maturity = 0
	r.mod.killTree(thicket, r.mod.get(thicket, woods[2]), 7)
	_, streets, _ = streetCounts(t, r.hub)
	want["wood_tree"] = treeTally{0, 3}
	if got := streets[thicket]; !sameTrees(got.Trees, want) {
		t.Fatalf("Thicket felled: %+v", got)
	}
	// A tree only touched (a harvest, a pet) still stands.
	for _, s := range sharedWorld.streets[thicket] {
		if s.class == "trant_bean" {
			r.mod.get(thicket, s).fruit = 0
		}
	}
	if _, streets, _ = streetCounts(t, r.hub); streets[thicket].Trees["trant_bean"] != (treeTally{1, 0}) {
		t.Fatalf("a touched Bean Tree: %+v", streets[thicket])
	}
}

func TestStreetCountsCountSharedPatchesAndWhatGrowsInThem(t *testing.T) {
	r := newPatchRig(t, true)
	r.shareAll()
	const st = "GCR29V8S4Q12IF4" // a fruit tree (tree:0), a Wood Tree (tree:1), and Patches
	layout := len(r.g.w.streets[st].spots)
	_, streets, _ := streetCounts(t, r.hub)
	if p := streets[st].Patches; p == nil || p.Empty+p.Dead != layout || p.Seedlings != 0 || p.Grown != nil {
		t.Fatalf("fresh Patches: %+v (layout %d)", p, layout)
	}
	// Both street trees die: the fruit tree leaves its dead tree, the Wood Tree a Patch.
	fresh := *streets[st].Patches
	r.mod.killTree(st, r.mod.get(st, sharedWorld.streets[st]["tree:0"]), 0)
	r.mod.killTree(st, r.mod.get(st, sharedWorld.streets[st]["tree:1"]), 0)
	_, streets, _ = streetCounts(t, r.hub)
	got := streets[st]
	if got.Trees["trant_fruit"][1] != 1 || got.Trees["wood_tree"][1] != 1 {
		t.Fatalf("trees: %+v", got.Trees)
	}
	if got.Patches.Dead != fresh.Dead+1 || got.Patches.Empty != fresh.Empty+1 {
		t.Fatalf("patches after the deaths: %+v (fresh %+v)", got.Patches, fresh)
	}

	// On Old Main Street, a Seedling in one Patch and a grown tree in another.
	keys := []string{}
	for k, s := range r.g.w.streets[oldMainStreet].spots {
		if s.class == "patch" {
			keys = append(keys, k)
		}
	}
	if len(keys) < 2 {
		t.Skip("Old Main Street has fewer than two Patches")
	}
	a := r.at(1, "Ada", oldMainStreet, keys[0])
	tree := r.tree(a, keys[0], "bean_spice")
	r.walk(a, keys[1])
	r.seedling(a, keys[1], "bean_gas")
	_, streets, _ = streetCounts(t, r.hub)
	p := streets[oldMainStreet].Patches
	if p.Seedlings != 1 || p.Grown[tree.class] != 1 || p.Empty+p.Dead != len(r.g.w.streets[oldMainStreet].spots)-2 {
		t.Fatalf("Old Main Street: %+v", p)
	}
}

func TestStreetCountsSkipPatchesWhenTheyAreNotShared(t *testing.T) {
	r := newPatchRig(t, true)
	r.pilotOnly(oldMainStreet)
	_, streets, _ := streetCounts(t, r.hub)
	if streets[oldMainStreet].Patches == nil {
		t.Fatal("the pilot street's Patches are missing")
	}
	for street, row := range streets {
		if street != oldMainStreet && row.Patches != nil {
			t.Fatalf("%s is off the pilot list but counted %+v", street, row.Patches)
		}
	}
}

func TestStreetCountsAreASignedCallWithNoPlayerInThem(t *testing.T) {
	ws, base := startTeleportServer(t)
	signIn(t, ws, 2, "Bob")
	if code, out := siteCall(t, base, "street_counts", map[string]any{}, "garbage"); code != http.StatusUnauthorized || out["status"] != "refused" {
		t.Fatalf("unsigned: %d %v", code, out)
	}
	code, out := siteCall(t, base, "street_counts", map[string]any{}, "")
	mustStatus(t, code, out, 200, "ok")
	at, _ := out["at"].(float64)
	if d := time.Since(time.UnixMilli(int64(at))); d < 0 || d > time.Minute {
		t.Fatalf("at %v", at)
	}
	raw, _ := json.Marshal(out)
	var m struct {
		Streets map[string]map[string]json.RawMessage `json:"streets"`
	}
	json.Unmarshal(raw, &m)
	if len(m.Streets) < 700 {
		t.Fatalf("%d streets", len(m.Streets))
	}
	for street, row := range m.Streets {
		for k := range row {
			if k != "trees" && k != "patches" {
				t.Fatalf("%s carries %q", street, k)
			}
		}
	}
	if len(raw) > 256*1024 {
		t.Fatalf("the answer is %d bytes", len(raw))
	}
}

func sameTrees(a, b map[string]treeTally) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
