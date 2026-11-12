package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// nodeStreet finds the first street (by tsid) with node:<class>:0.
func nodeStreet(t *testing.T, class string) (string, *objSpot) {
	t.Helper()
	streets := make([]string, 0, len(sharedWorld.streets))
	for tsid := range sharedWorld.streets {
		streets = append(streets, tsid)
	}
	sort.Strings(streets)
	for _, tsid := range streets {
		if spot := sharedWorld.streets[tsid]["node:"+class+":0"]; spot != nil {
			return tsid, spot
		}
	}
	t.Skipf("no %s in data/objects.json", class)
	return "", nil
}

// Two players at one barnacle: both scrapes started while it was ready, so both land and both are
// paid (mortar_barnacle.js onScrapingComplete 232-311 never checks isUseable again).
func TestEveryNodeUseThatStartedIsPaid(t *testing.T) {
	r := newObjRig(t)
	got := r.credited()
	street, spot := nodeStreet(t, "mortar_barnacle")
	id := spot.id
	a := r.player(1, "Ada", street, spot.x, spot.y)
	b := r.player(2, "Bo", street, spot.x+30, spot.y)
	r.send(a, map[string]any{"t": "obj_dig", "id": id, "do": "start"})
	r.send(b, map[string]any{"t": "obj_dig", "id": id, "do": "start"})
	if f := r.drain(a); len(f) == 0 || f[0]["t"] == "obj_dig" {
		t.Fatalf("a barnacle answers no Help label: %v", f)
	}
	r.drain(b)
	r.act(a, 1, id, "scrape", nil)
	if ok := r.only(a, "obj_ok"); num(objOf(ok, id)["units"]) != 1 {
		t.Fatalf("Ada's scrape answered %v", ok)
	}
	r.drain(b)
	r.act(b, 1, id, "scrape", nil)
	ok := r.only(b, "obj_ok")
	if before, _ := ok["before"].(map[string]any); num(before["units"]) != 4 {
		t.Fatalf("Bo's late scrape runs on the ready barnacle: %v", ok)
	}
	if strings.Join(*got, ",") != "1:take:barnaclex1,2:take:barnaclex1" {
		t.Fatalf("credited %v", *got)
	}
	// A third who never started is refused: the barnacle is not ready.
	c := r.player(3, "Cy", street, spot.x, spot.y)
	r.drain(c)
	r.act(c, 1, id, "scrape", nil)
	if no := r.only(c, "obj_no"); no["message"] != notReady["mortar_barnacle"] {
		t.Fatalf("Cy answered %v", no)
	}
	// Bo is off the barnacle once his scrape landed: a second claim is refused.
	r.act(b, 2, id, "scrape", nil)
	if no := r.only(b, "obj_no"); no["code"] != "not_ready" {
		t.Fatalf("Bo again answered %v", no)
	}
}

// include/scraping.js: each other scraper adds one Ice, and the line names them (ice_knob.js 270-317).
func TestIceScrapersHelpEachOther(t *testing.T) {
	r := newObjRig(t)
	got := r.credited()
	street, spot := nodeStreet(t, "ice_knob")
	id := spot.id
	a := r.player(1, "Ada", street, spot.x, spot.y)
	b := r.player(2, "Bo", street, spot.x+30, spot.y)
	c := r.player(3, "Cy", street, spot.x-30, spot.y)
	r.send(a, map[string]any{"t": "obj_dig", "id": id, "do": "start"})
	if m := r.only(a, "obj_dig"); m["helping"] != false {
		t.Fatalf("Ada starts alone: %v", m)
	}
	r.send(b, map[string]any{"t": "obj_dig", "id": id, "do": "start"})
	if m := r.only(b, "obj_dig"); m["helping"] != true {
		t.Fatalf("Bo helps (Help scrape): %v", m)
	}
	r.send(c, map[string]any{"t": "obj_dig", "id": id, "do": "start"})
	r.drain(a)
	r.drain(b)
	r.drain(c)
	r.act(a, 1, id, "scrape", nil)
	ok := r.only(a, "obj_ok")
	help, _ := ok["help"].(map[string]any)
	if help == nil || num(help["n"]) != 2 || num(help["items"]) != 2 || help["class"] != "ice" || fmt.Sprint(help["by"]) != "[Bo Cy]" {
		t.Fatalf("Ada's scrape with two helpers answered %v", ok)
	}
	// Bo's own scrape: Ada's start came before his, so she helps him too (addScraper), and Cy.
	r.drain(b)
	r.act(b, 1, id, "scrape", nil)
	ok = r.only(b, "obj_ok")
	if help, _ := ok["help"].(map[string]any); help == nil || num(help["n"]) != 2 {
		t.Fatalf("Bo's late scrape answered %v", ok)
	}
	if strings.Join(*got, ",") != "1:take:icex3,2:take:icex3" {
		t.Fatalf("credited %v", *got)
	}
}

// A swing that started on a rock another player then emptied still lands (onMiningComplete).
func TestALateSwingAtAnEmptiedRockLands(t *testing.T) {
	r := newObjRig(t)
	spot := sharedWorld.streets[defaultStreet]["rock:1"]
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	b := r.player(2, "Bo", defaultStreet, spot.x+30, spot.y)
	mine := func(cl *Client, do string) { r.send(cl, map[string]any{"t": "obj_mine", "id": "rock:1", "do": do}) }
	ob := r.mod.get(defaultStreet, spot)
	ob.chunks = 3
	mine(a, "start")
	mine(b, "start")
	r.drain(a)
	r.drain(b)
	r.act(a, 1, "rock:1", "mine", map[string]any{"perks": []string{"luck"}})
	if ok := r.only(a, "obj_ok"); num(objOf(ok, "rock:1")["chunks"]) != 0 {
		t.Fatalf("Ada's swing answered %v", ok)
	}
	r.drain(b)
	r.act(b, 1, "rock:1", "mine", map[string]any{"perks": []string{"luck"}})
	ok := r.only(b, "obj_ok")
	if before, _ := ok["before"].(map[string]any); num(before["chunks"]) < 9 || num(objOf(ok, "rock:1")["chunks"]) != 0 {
		t.Fatalf("Bo's late swing answered %v", ok)
	}
	c := r.player(3, "Cy", defaultStreet, spot.x, spot.y)
	r.drain(c)
	r.act(c, 1, "rock:1", "mine", nil)
	if no := r.only(c, "obj_no"); no["code"] != "empty" {
		t.Fatalf("a swing that never started answered %v", no)
	}
}
