package main

import (
	"strings"
	"testing"
	"time"
)

const salmonTestStreet = "GA9QTSIOV4F31QH"

// Slice 3 of research/plans/street-animals-shared.md: a Salmon pocketed by one player is gone for
// everyone until the spawner puts another in the water 10 s later (npc_salmon.js verbs.pocket
// apiDelete; spawner.js spawn_interval), and the Pocket Salmon is credited to who pocketed it.
func TestAPocketedSalmonIsGoneForEveryoneUntilItRespawns(t *testing.T) {
	spot := sharedWorld.streets[salmonTestStreet]["node:npc_salmon:0"]
	if spot == nil || spot.kind != "node" || spot.class != "npc_salmon" {
		t.Fatalf("no shared Salmon on %s: %+v", salmonTestStreet, spot)
	}
	if rule := sharedWorld.rules.Nodes["npc_salmon"]; rule.Model != "respawn" || rule.Max != 1 || len(rule.Regrow) != 3 || rule.Regrow[0] != 10000 {
		t.Fatalf("the Salmon's rule: %+v", rule)
	}
	r := newObjRig(t)
	got := r.credited()
	a := r.player(1, "Ada", salmonTestStreet, spot.x+200, spot.y)
	b := r.player(2, "Bo", salmonTestStreet, spot.x-200, spot.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	r.drain(b)
	r.act(a, 1, "node:npc_salmon:0", "pocket", nil)
	ok := r.only(a, "obj_ok")
	if st := objOf(ok, "node:npc_salmon:0"); num(st["units"]) != 0 || num(st["in"]) != 10000 {
		t.Fatalf("Ada pockets it: %v", ok)
	}
	if strings.Join(*got, ",") != "1:take:salmonx1" {
		t.Fatalf("credited %v", *got)
	}
	_, seen := lastObj(r.drain(b), "node:npc_salmon:0")
	if seen == nil || num(seen["units"]) != 0 {
		t.Fatalf("Bo sees it gone: %v", seen)
	}
	r.act(b, 1, "node:npc_salmon:0", "pocket", nil)
	if no := r.only(b, "obj_no"); no["code"] != "not_ready" || no["message"] != notReady["npc_salmon"] {
		t.Fatalf("Bo's pocket of a gone Salmon: %v", no)
	}
	r.advance(10 * time.Second)
	if st := syncOf(r, b, "node:npc_salmon:0"); st != nil {
		t.Fatalf("10 s on it swims again (fresh, so forgotten): %v", st)
	}
	r.act(b, 2, "node:npc_salmon:0", "pocket", nil)
	if ok := r.only(b, "obj_ok"); ok["verb"] != "pocket" {
		t.Fatalf("Bo pockets the new one: %v", ok)
	}
}
