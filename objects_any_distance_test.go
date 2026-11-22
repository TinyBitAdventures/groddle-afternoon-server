package main

import (
	"sort"
	"testing"
)

// firstSpotOf is the first street (by tsid) with a spot of this kind, and that spot.
func firstSpotOf(t *testing.T, kind string) (string, *objSpot) {
	t.Helper()
	streets := make([]string, 0, len(sharedWorld.streets))
	for s := range sharedWorld.streets {
		streets = append(streets, s)
	}
	sort.Strings(streets)
	for _, s := range streets {
		ids := make([]string, 0)
		for id := range sharedWorld.streets[s] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if sp := sharedWorld.streets[s][id]; sp.kind == kind {
				return s, sp
			}
		}
	}
	t.Fatalf("no %s on any street", kind)
	return "", nil
}

// farAnswer is the "far" refusal among a client's frames, or nil.
func farAnswer(frames []map[string]any) map[string]any {
	for _, f := range frames {
		if f["t"] == "obj_no" && f["code"] == "far" {
			return f
		}
	}
	return nil
}

// #604: the source let a player name or rename a street animal from anywhere on the street, and
// massage or milk a Butterfly and feed a Batterfly ("disable_proximity": true on those verbs,
// npc_piggy.js 750-816, npc_chicken.js, npc_butterfly.js, npc_batterfly.js). Each page walks a
// Piggy its own way, so a player beside the one she had named was told to come closer. A Piggy's
// pet still wants the player near (proximity_override 800).
func TestNamingAStreetAnimalTakesNoReachCheck(t *testing.T) {
	r := newObjRig(t)
	cases := []struct {
		kind, verb string
		extra      map[string]any
	}{
		{"pig", "name", map[string]any{"name": "Dr Oink", "perks": []string{"ak5"}}},
		{"chicken", "name", map[string]any{"name": "Henny", "perks": []string{"ak5"}}},
		{"butterfly", "name", map[string]any{"name": "Flutter", "perks": []string{"ak5"}}},
		{"batterfly", "name", map[string]any{"name": "Bats", "perks": []string{"ak5"}}},
		{"butterfly", "massage", nil},
		{"butterfly", "milk", nil},
		{"batterfly", "feed", map[string]any{"c": "cherry", "n": 1}},
	}
	for i, c := range cases {
		street, spot := firstSpotOf(t, c.kind)
		// (No shared Butterfly rests yet: every one flies, and a flyer is measured from nowhere already.)
		// Well past every reach the server keeps (a Piggy's 1600 x 1000), across and down.
		cl := r.player(int64(10+i), "Ada", street, spot.x+3000, spot.y+1500)
		r.act(cl, 1, spot.id, c.verb, c.extra)
		if far := farAnswer(r.drain(cl)); far != nil {
			t.Fatalf("%s %s on %s from across the street was refused: %v", c.verb, spot.id, street, far)
		}
	}
	street, spot := firstSpotOf(t, "pig")
	cl := r.player(30, "Bo", street, spot.x+3000, spot.y)
	r.act(cl, 1, spot.id, "pet", nil)
	if far := farAnswer(r.drain(cl)); far == nil || far["message"] != objTextFarWalker {
		t.Fatalf("a pet from across the street should still be refused as far: %v", far)
	}
}
