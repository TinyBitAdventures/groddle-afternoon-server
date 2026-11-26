package main

import (
	"testing"
	"time"
)

const pianoParty = "GA96OS42D6F3M9P"

// soundRig is a hub with only the street sounds module and a clock the test moves.
type soundRig struct {
	hub *Hub
	now time.Time
}

func newSoundRig(t *testing.T) *soundRig {
	t.Helper()
	r := &soundRig{now: time.Unix(1_800_000_000, 0)}
	saved := modules
	modules = []moduleFactory{newStreetSounds}
	t.Cleanup(func() { modules = saved })
	r.hub = NewHub(func() time.Time { return r.now })
	return r
}

func (r *soundRig) play(cl *Client, event string) {
	send(r.hub, cl, map[string]any{"t": "street_sound", "event": event})
}

func soundsOf(cl *Client) []map[string]any {
	var out []map[string]any
	for _, m := range frames(cl) {
		if m["t"] == "street_sound" {
			out = append(out, m)
		}
	}
	return out
}

// #715: a key one player bumps on Piano Party is heard by the others there, not twice by the player.
func TestStreetSoundReachesTheOthersOnTheStreet(t *testing.T) {
	r := newSoundRig(t)
	ada := joinAt(r.hub, 1, "Ada", pianoParty, 1000, 500)
	bo := joinAt(r.hub, 2, "Bo", pianoParty, 1400, 500)
	cy := joinAt(r.hub, 3, "Cy", "GA5101HF7F429V5", 0, 0)
	frames(ada)
	frames(bo)
	frames(cy)
	r.play(ada, "key1")
	got := soundsOf(bo)
	if len(got) != 1 || got[0]["event"] != "key1" || got[0]["street"] != pianoParty || got[0]["by"] != 1.0 {
		t.Fatalf("Bo heard %v", got)
	}
	if own := soundsOf(ada); len(own) != 0 {
		t.Fatalf("the player heard their own note again: %v", own)
	}
	if other := soundsOf(cy); len(other) != 0 {
		t.Fatalf("another street heard it: %v", other)
	}
	// The street's other spelling (L…) is the same street.
	if n := len(streetSoundTable["A96OS42D6F3M9P"]); n != 66 {
		t.Fatalf("Piano Party lists %d events", n)
	}
}

// Only an event the street lists, from a player standing on it, once a player per 200 ms.
func TestStreetSoundIsCheckedAndLimited(t *testing.T) {
	r := newSoundRig(t)
	ada := joinAt(r.hub, 1, "Ada", pianoParty, 1000, 500)
	bo := joinAt(r.hub, 2, "Bo", pianoParty, 1400, 500)
	frames(ada)
	frames(bo)
	// Not one of the street's shared sounds: a flag-only event, an arrival, another street's, junk.
	for _, ev := range []string{"key1_off", "first_player_enter", "note_1", "", "../x", "key1 "} {
		r.now = r.now.Add(time.Second)
		r.play(ada, ev)
	}
	if got := soundsOf(bo); len(got) != 0 {
		t.Fatalf("relayed %v", got)
	}
	// Too soon after the last one: dropped. A refused one does not hold up the next.
	r.now = r.now.Add(time.Second)
	r.play(ada, "key2")
	r.now = r.now.Add(100 * time.Millisecond)
	r.play(ada, "key3")
	r.now = r.now.Add(100 * time.Millisecond)
	r.play(ada, "key4")
	got := soundsOf(bo)
	if len(got) != 2 || got[0]["event"] != "key2" || got[1]["event"] != "key4" {
		t.Fatalf("the limit let through %v", got)
	}
	// Each player has their own limit.
	r.play(bo, "key5")
	if got := soundsOf(ada); len(got) != 1 || got[0]["event"] != "key5" {
		t.Fatalf("Ada heard %v", got)
	}
	// A toy street's notes go through toy_click (streettoys.go), never this way.
	di := joinAt(r.hub, 4, "Di", "GHV88RQU60B3Q0G", 5400, 900)
	ed := joinAt(r.hub, 5, "Ed", "GHV88RQU60B3Q0G", 5500, 900)
	frames(di)
	frames(ed)
	r.now = r.now.Add(time.Second)
	r.play(di, "first_note")
	if got := soundsOf(ed); len(got) != 0 {
		t.Fatalf("a toy street's note came this way: %v", got)
	}
}
