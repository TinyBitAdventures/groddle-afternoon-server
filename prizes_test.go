package main

import (
	"testing"
	"time"
)

func TestAPairIsPaidForTwentyWinsOfAGameADay(t *testing.T) {
	now := time.Date(2026, 11, 14, 23, 0, 0, 0, time.UTC)
	h := NewHub(func() time.Time { return now })
	for i := 0; i < prizePairDaily; i++ {
		if !h.prizeLocked("race", 1, []int64{1, 2}) {
			t.Fatalf("win %d of the day is paid", i+1)
		}
	}
	if h.prizeLocked("race", 1, []int64{2}) || h.prizeLocked("race", 2, []int64{1}) {
		t.Fatal("past the count neither of the pair is paid for a race against the other")
	}
	if !h.prizeLocked("race", 1, []int64{3}) || !h.prizeLocked("charades", 1, []int64{2}) {
		t.Fatal("another player, or another game, counts apart")
	}
	// A game of four: one pair at the count stops the prize for the winner, and counts nothing.
	if h.prizeLocked("race", 3, []int64{1, 2, 3}) != true {
		t.Fatal("a pair under the count is paid")
	}
	if h.prizeLocked("race", 1, []int64{2, 3, 4}) || h.prizes.paid[prizePair("race", 1, 4)] != 0 {
		t.Fatal("one pair at the count stops the prize, and the others are not counted")
	}
	if !h.prizeLocked("race", 1, nil) {
		t.Fatal("a win over nobody pays")
	}
	now = now.Add(2 * time.Hour) // the next UTC day
	if !h.prizeLocked("race", 1, []int64{2}) {
		t.Fatal("a new day counts from zero")
	}
}

// prizesAt puts a pair at the day's count of paid wins of a game.
func prizesAt(h *Hub, game string, a, b int64) {
	for h.prizeLocked(game, a, []int64{b}) {
	}
}

func TestARaceWinPastThePairsCountStillEndsButPaysNothing(t *testing.T) {
	r := newRaceRig(t)
	got := caughtCredits(r.hub)
	ada, bo, match := r.pair("canyon_run")
	prizesAt(r.hub, "race", 1, 2)
	id, _ := r.run(ada, bo, match)
	r.move(bo, 905, 400)
	r.do(bo, map[string]any{"t": "race_finish", "match": id})
	end := one(t, drain(bo), "race_end")
	if end["why"] != "won" || end["winner"].(map[string]any)["name"] != "Bo" || end["prize"] != false {
		t.Fatalf("end %v", end)
	}
	if len(*got) != 0 {
		t.Fatalf("no credit past the count: %+v", *got)
	}
}

func TestACharadesGuessPastThePairsCountWinsNoPrize(t *testing.T) {
	r := newVerbsRig(t)
	ada, bo := r.pair()
	prizesAt(r.hub, "charades", 9, 7)
	r.do(ada, map[string]any{"t": "charades_start", "word": "Ginger Root"})
	drain(ada)
	drain(bo)
	r.do(bo, map[string]any{"t": "chat", "text": "ginger root"})
	won := verbOne(t, drain(bo), "charades_won")
	if won["prize"] != false || won["currants"].(float64) != 0 || won["xp"].(float64) != 0 || won["text"] != "Bo guessed correctly!" {
		t.Fatalf("won %v", won)
	}
	if len(r.credits) != 0 {
		t.Fatalf("no credit past the count: %+v", r.credits)
	}
}

func TestACrownsWinPastAPairsCountPaysNothing(t *testing.T) {
	r := newCrownRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	drain(ada)
	mt := &crownMatch{id: "00000000000000c1", round: 1, members: []*crownMember{{crownRef: crownRef{1, "Ada"}}, {crownRef: crownRef{2, "Bo"}}, {crownRef: crownRef{3, "Cy"}}}}
	prizesAt(r.hub, "crowns", 1, 3)
	r.hub.mu.Lock()
	r.m.reward(mt, crownRef{1, "Ada"})
	r.hub.mu.Unlock()
	rw := one(t, drain(ada), "crown_reward")
	if rw["prize"] != false || rw["xp"].(float64) != 0 || rw["currants"].(float64) != 0 || len(r.led.calls) != 0 {
		t.Fatalf("reward %v, ledger %v", rw, r.led.calls)
	}
}

func TestAMathMayhemWinPastAPairsCountPaysNothing(t *testing.T) {
	r := newMayhemRig(t, false)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	drain(ada)
	rm := &mayhemRoom{id: "00000000000000d1", round: 1, members: []*mayhemMember{
		{mayhemRef: mayhemRef{1, "Ada"}, playing: true}, {mayhemRef: mayhemRef{2, "Bo"}, playing: true}, {mayhemRef: mayhemRef{5, "Ed"}}}}
	prizesAt(r.hub, "mayhem", 1, 2)
	r.hub.mu.Lock()
	r.m.reward(rm, mayhemRef{1, "Ada"})
	r.hub.mu.Unlock()
	rw := one(t, drain(ada), "mayhem_reward")
	if rw["prize"] != false || rw["xp"].(float64) != 0 || rw["currants"].(float64) != 0 || len(r.led.calls) != 0 {
		t.Fatalf("reward %v, ledger %v", rw, r.led.calls)
	}
	// Ed, who did not play the round, is no pair: a win over him alone is paid.
	rm.members[1].playing = false
	r.hub.mu.Lock()
	rm.round = 2
	r.m.reward(rm, mayhemRef{1, "Ada"})
	r.hub.mu.Unlock()
	if len(r.led.calls) != 1 {
		t.Fatalf("a win over nobody at the count pays: %v", r.led.calls)
	}
}
