package main

import (
	"encoding/json"
	"testing"
)

// G1018: the house and its back yard for the owner, key-holders and those let in (Austin
// 2026-11-25), as locations/inc_pols.js pols_canEnter 461-532 let them in.

// insideClaims reads a let-in proof the server handed out.
func insideClaims(t *testing.T, proof string) homeClaims {
	t.Helper()
	raw, err := signedBody(homesTestSecret, proof)
	if err != nil {
		t.Fatalf("a let-in proof signed with the shared secret: %v", err)
	}
	var c homeClaims
	if json.Unmarshal(raw, &c) != nil {
		t.Fatalf("proof: %s", raw)
	}
	return c
}

// The access matrix, on the realtime server: who joins the house (and so stands in its back yard).
func TestWhoMayStandInTheHouseAndBackYard(t *testing.T) {
	type row struct {
		name     string
		ownerOn  bool
		key      bool
		knockYes bool
		want     string // "in", or the error code
	}
	for _, c := range []row{
		{"owner", true, false, false, "in"},
		{"key holder, owner online", true, true, false, "in"},
		{"knocked and let in", true, false, true, "in"},
		{"plain visitor", true, false, false, "error:home_locked"},
		{"owner offline, key holder", false, true, false, "in"},
		{"owner offline, plain visitor", false, false, false, "error:home_locked"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newHomesRig(t)
			var ada *Client
			if c.ownerOn || c.name == "owner" {
				ada = r.connect(7, "Ada")
				r.join(ada, house(7))
				drain(ada)
			}
			if c.name == "owner" {
				r.join(ada, home(7))
				r.join(ada, house(7))
				if f := drain(ada); f[len(f)-1]["t"] != "street" {
					t.Fatalf("the owner walks in: %v", f)
				}
				return
			}
			bo := r.connect(9, "Bo")
			r.pass(bo, 7, "Ada", c.key)
			r.join(bo, home(7))
			drain(bo)
			if c.knockYes {
				r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
				drain(bo)
				r.do(ada, map[string]any{"t": "home_answer", "id": 9, "allow": true})
				f := only(t, drain(bo), "home_let_in")
				if cl := insideClaims(t, f["inside"].(string)); cl.K != "house_in" || cl.Sub != 9 || cl.O != 7 || cl.Exp != r.now.Add(homeInLife).Unix() {
					t.Fatalf("the one let in gets the site's proof: %+v", cl)
				}
			}
			r.join(bo, house(7))
			f := drain(bo)
			got := "in"
			if f[0]["t"] != "street" {
				got = buddyKinds(f)
			}
			if got != c.want {
				t.Fatalf("%s: %s, want %s", c.name, got, c.want)
			}
		})
	}
}

// Refused at the door with Glitch's words (houses_familiar_owner_offline, _not_at_home); a key-holder is told to the owner.
func TestTheDoorSaysGlitchsWords(t *testing.T) {
	r := newHomesRig(t)
	bo := r.connect(9, "Bo")
	r.pass(bo, 7, "Ada", false)
	drain(bo)
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	if f := only(t, drain(bo), "home_refused"); f["text"] != "The owner of this location (Ada) is currently offline" {
		t.Fatalf("offline: %v", f)
	}
	ada := r.connect(7, "Ada")
	cy := r.connect(11, "Cy")
	r.join(ada, house(7))
	drain(ada)
	r.pass(cy, 7, "Ada", true)
	r.join(cy, house(7))
	drain(cy)
	if f := drain(ada); buddyKinds(f) != "joined,home_entered" || f[1]["text"] != "Cy just entered your house." {
		t.Fatalf("inc_pols.js:466 the owner hears the key-holder come in: %v", f)
	}
	// A pass taken again while let in brings a fresh proof; never for one only visiting.
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	r.do(ada, map[string]any{"t": "home_answer", "id": 9, "allow": true})
	drain(bo)
	r.pass(bo, 7, "Ada", false)
	if f := only(t, drain(bo), "home_pass_ok"); f["inside"] == nil {
		t.Fatalf("a renewed pass carries the proof while let in: %v", f)
	}
	dan := r.connect(12, "Dan")
	r.pass(dan, 7, "Ada", false)
	if f := only(t, drain(dan), "home_pass_ok"); f["inside"] != nil {
		t.Fatalf("no proof for someone not let in: %v", f)
	}
	// The owner gone: being let in went with her (houses_logout), and so does the proof.
	r.hub.Disconnect(ada)
	drain(bo)
	r.pass(bo, 7, "Ada", false)
	if f := only(t, drain(bo), "home_pass_ok"); f["inside"] != nil {
		t.Fatalf("no proof once the owner is offline: %v", f)
	}
}

// backYardRig: Ada (7) at home in her house, Bo (9) let in, Cy (11) with a key, Dan (12) on the street only.
func backYardRig(t *testing.T) (r *yardRig, ada, bo, cy, dan *Client) {
	r = newYardRig(t)
	ada, bo, cy, dan = r.connect(7, "Ada"), r.connect(9, "Bo"), r.connect(11, "Cy"), r.connect(12, "Dan")
	r.join(ada, house(7))
	r.pass(bo, 7, "Ada", false)
	r.join(bo, home(7))
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	r.do(ada, map[string]any{"t": "home_answer", "id": 9, "allow": true})
	r.join(bo, house(7))
	r.pass(cy, 7, "Ada", true)
	r.join(cy, house(7))
	r.pass(dan, 7, "Ada", false)
	r.join(dan, home(7))
	for _, c := range []*Client{ada, bo, cy, dan} {
		drain(c)
	}
	return
}

// M28: a guest digs the back yard's Dirt Pile through the same site route; the owner and those inside are told, the street is not.
func TestAGuestDigsTheBackYardsDirtPile(t *testing.T) {
	r, ada, bo, cy, dan := backYardRig(t)
	r.site.answer = yardAnswer{Status: "settled", Added: 0, Act: json.RawMessage(`{"id":"yd-0000000000000001","kind":"node","target":"b:node:dirt_pile:0","verb":"dig","took":1,"left":3,"at":1}`)}
	r.do(bo, map[string]any{"t": "home_yard", "req": 1, "owner": 7, "kind": "node", "id": "b:node:dirt_pile:0", "verb": "dig"})
	r.settle()
	if len(r.site.calls) != 1 || r.site.calls[0].ID != "b:node:dirt_pile:0" || r.site.calls[0].Visitor != 9 {
		t.Fatalf("the site decides the back yard's dig: %+v", r.site.calls)
	}
	if f := only(t, drain(bo), "home_yard_done"); f["status"] != "settled" {
		t.Fatalf("Bo: %v", f)
	}
	for _, who := range []*Client{ada, cy} {
		if a := only(t, drain(who), "home_yard_acted"); a["act"].(map[string]any)["target"] != "b:node:dirt_pile:0" {
			t.Fatalf("those in the house hear it: %v", a)
		}
	}
	if got := drain(dan); len(got) != 0 {
		t.Fatalf("someone on the street hears nothing of the back yard: %v", got)
	}
	// The key-holder too, the owner offline.
	r.hub.Disconnect(ada)
	drain(bo)
	drain(cy)
	r.do(cy, map[string]any{"t": "home_yard", "req": 2, "owner": 7, "kind": "rock", "id": "b:rock:0", "verb": "mine"})
	r.settle()
	if len(r.site.calls) != 2 || r.site.calls[1].Visitor != 11 {
		t.Fatalf("a key-holder digs with the owner away: %+v", r.site.calls)
	}
}

// Only those standing in the house may act on the back yard; Wine of the Dead stays the owner's.
func TestTheBackYardIsForThoseInside(t *testing.T) {
	r, ada, _, cy, dan := backYardRig(t)
	r.do(dan, map[string]any{"t": "home_yard", "req": 1, "owner": 7, "kind": "node", "id": "b:node:dirt_pile:0", "verb": "dig"})
	if f := only(t, drain(dan), "home_yard_done"); f["why"] != "away" || f["text"] != yardTextAwayHouse {
		t.Fatalf("from the street: %v", f)
	}
	// From the house, the street's things are the street's.
	r.do(cy, map[string]any{"t": "home_yard", "req": 2, "owner": 7, "kind": "node", "id": "node:dirt_pile:0", "verb": "dig"})
	if f := only(t, drain(cy), "home_yard_done"); f["why"] != "away" {
		t.Fatalf("the street's pile from the house: %v", f)
	}
	// dirt_pile.js 67 pour_wine: container.owner == pc. No visitor pours, anywhere.
	for _, c := range []map[string]any{
		{"t": "home_yard", "req": 3, "owner": 7, "kind": "node", "id": "b:node:dirt_pile:0", "verb": "pour"},
		{"t": "home_yard", "req": 3, "owner": 7, "kind": "animal", "id": "b:a1", "verb": "pet"},
		{"t": "home_yard", "req": 3, "owner": 7, "kind": "node", "id": "b:b:node:dirt_pile:0", "verb": "dig"},
	} {
		r.do(cy, c)
		if f := only(t, drain(cy), "home_yard_done"); f["why"] != "bad" {
			t.Fatalf("%v: bad, got %v", c, f)
		}
	}
	r.do(ada, map[string]any{"t": "home_yard", "req": 4, "owner": 7, "kind": "node", "id": "b:node:dirt_pile:0", "verb": "dig"})
	if f := only(t, drain(ada), "home_yard_done"); f["why"] != "bad" {
		t.Fatalf("the owner digs in her own game: %v", f)
	}
	if len(r.site.calls) != 0 || len(r.pending) != 0 {
		t.Fatalf("no site calls for refused acts: %+v", r.site.calls)
	}
}

// G668: summoned into the house, the visitor is handed the site's proof for that arrival.
func TestASummonsIntoTheHouseCarriesTheProof(t *testing.T) {
	r := newHomesRig(t)
	r.connect(7, "Ada")
	bo := r.connect(9, "Bo")
	fn := r.hub.ProvidedLocked("homes.summoned").(func(int64, int64))
	fn(9, 7)
	f := only(t, drain(bo), "home_inside")
	if c := insideClaims(t, f["inside"].(string)); c.K != "house_in" || c.Sub != 9 || c.O != 7 {
		t.Fatalf("summoned in: %+v", c)
	}
}
