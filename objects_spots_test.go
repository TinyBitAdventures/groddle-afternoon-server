package main

import "testing"

// R43 (#587): the snapshot gives every rock's, tree's and node's spot, so an online page draws each
// where this server checks reach (street-objects-feature), not where its own planner put that id.
// On Indri Slope the page's planner put npc_salmon:2 where this server has npc_salmon:3, beside the
// Asura Spire signpost: Pocket on it was refused as far while the player stood on it.
func TestSnapshotGivesEveryRockTreeAndNodeSpot(t *testing.T) {
	r := newObjRig(t)
	for i, street := range []string{"GUV2SSTOU8A30RS", "GCR131F8U1K1PF9"} {
		a := r.player(int64(i+1), "Ada", street, 2472, 1103)
		r.send(a, map[string]any{"t": "obj_sync"})
		spots, _ := r.only(a, "objs")["spots"].(map[string]any)
		kinds := map[string]int{}
		for id, s := range sharedWorld.streets[street] {
			if !placedBySnapshot(s.kind) {
				continue
			}
			kinds[s.kind]++
			at, _ := spots[id].([]any)
			if len(at) != 2 || num(at[0]) != s.x || num(at[1]) != s.y {
				t.Fatalf("%s: the snapshot gives %s at %v, the layout has %v,%v", street, id, spots[id], s.x, s.y)
			}
		}
		if street == "GCR131F8U1K1PF9" && (kinds["rock"] == 0 || kinds["tree"] == 0 || kinds["wood"] == 0 || kinds["node"] == 0) {
			t.Fatalf("%s should have rocks, trees, a Wood Tree and a node: %v", street, kinds)
		}
		if street == "GUV2SSTOU8A30RS" {
			// The fish beside the Asura Spire signpost is npc_salmon:3, in reach of a player there.
			if at, _ := spots["node:npc_salmon:3"].([]any); len(at) != 2 || num(at[0]) != 2480 || num(at[1]) != 1104.5 {
				t.Fatalf("Indri Slope's npc_salmon:3: %v", spots["node:npc_salmon:3"])
			}
		}
	}
}
