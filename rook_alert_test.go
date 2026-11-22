package main

import (
	"testing"
	"time"
)

// alertFrames picks the rook_attacks frames out of what a client was sent.
func alertFrames(frames []map[string]any) [][]any {
	var out [][]any
	for _, f := range frames {
		if f["t"] == "rook_attacks" {
			list, _ := f["attacks"].([]any)
			out = append(out, list)
		}
	}
	return out
}

// G799: the server is Glitch's rook_attack_tracker. A player anywhere (at home, nowhere near the
// street) hears of an attack when the Rook arrives, not during the herald, and hears it is over
// when the Rook is beaten; a client that asks is told the list.
func TestRookAlertsTellEveryoneWhileTheRookIsOut(t *testing.T) {
	r := newRookRig(t, true)
	fighter := r.player(1, "Ada")
	home := r.hub.Connect(Claims{Sub: 2, Name: "Bo"}) // signed in, on no street of the Rook's
	r.drain(home)

	r.send(home, map[string]any{"t": "rook_attacks"})
	got := alertFrames(r.drain(home))
	if len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("ask with no attack: %v", got)
	}

	r.send(fighter, map[string]any{"t": "rook_dev", "do": "start"})
	if got := alertFrames(r.drain(home)); len(got) != 0 {
		t.Fatalf("the herald is not an attack yet: %v", got)
	}

	r.advance(rookHerald)
	got = alertFrames(r.drain(home))
	if len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("the Rook arrived: want one alert with one attack, got %v", got)
	}
	a := got[0][0].(map[string]any)
	if a["street"] != rookTestStreet || a["hub"] != r.shrine().hub || int64(a["since"].(float64)) != r.now.UnixMilli() {
		t.Fatalf("attack row: %v", a)
	}

	// A wing beat or a stun changes the fight, not the list: nothing more is sent.
	r.send(fighter, map[string]any{"t": "rook_dev", "do": "wingbeat"})
	r.send(fighter, map[string]any{"t": "rook_dev", "do": "stun"})
	if got := alertFrames(r.drain(home)); len(got) != 0 {
		t.Fatalf("same attack, no new alert: %v", got)
	}

	// The one who asks again is told it.
	r.send(home, map[string]any{"t": "rook_attacks"})
	if got := alertFrames(r.drain(home)); len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("ask during the attack: %v", got)
	}

	// Beaten: the attack is over at once (a dead Rook needs no help), and its clear sends nothing new.
	r.mod.defeat(r.mod.streets[rookTestStreet].attack)
	got = alertFrames(r.drain(home))
	if len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("defeated: want an empty list, got %v", got)
	}
	r.advance(time.Minute)
	if got := alertFrames(r.drain(home)); len(got) != 0 {
		t.Fatalf("the clear repeats nothing: %v", got)
	}
	if r.mod.streets[rookTestStreet] != nil && r.mod.streets[rookTestStreet].attack != nil {
		t.Fatal("the attack should be cleared")
	}
}

// A Rook that flies off after its wing beats is over too.
func TestRookAlertsEndWhenTheRookFliesOff(t *testing.T) {
	r := newRookRig(t, true)
	fighter := r.player(1, "Ada")
	home := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.send(fighter, map[string]any{"t": "rook_dev", "do": "attack"})
	if got := alertFrames(r.drain(home)); len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("attack without a herald: %v", got)
	}
	for i := 0; i < 3; i++ {
		r.send(fighter, map[string]any{"t": "rook_dev", "do": "wingbeat"})
	}
	got := alertFrames(r.drain(home))
	if len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("flew off: want an empty list, got %v (phase %s)", got, r.mod.streets[rookTestStreet].attack.phase)
	}
}
