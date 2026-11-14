package main

import (
	"testing"
	"time"
)

func TestASocketIsPacedToSixtyFramesASecondAfterABurstOf120(t *testing.T) {
	var g frameGate
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < frameBurst; i++ {
		if d := g.wait(now); d != 0 {
			t.Fatalf("frame %d of the burst waited %v", i, d)
		}
	}
	// Past the burst each frame waits its turn: a flood read as fast as the reader allows runs at 60 a second.
	start := now
	for i := 0; i < 600; i++ {
		now = now.Add(g.wait(now))
	}
	if got := now.Sub(start); got < 9900*time.Millisecond || got > 10100*time.Millisecond {
		t.Fatalf("600 frames past the burst took %v, want about 10 s", got)
	}
	if g.paced != 600 {
		t.Fatalf("paced %d", g.paced)
	}
	// An honest page (a move every 100 ms and a few other frames) never waits.
	var honest frameGate
	for i := 0; i < 600; i++ {
		now = now.Add(50 * time.Millisecond) // 20 frames a second for 30 s
		if d := honest.wait(now); d != 0 {
			t.Fatalf("frame %d of an honest page waited %v", i, d)
		}
	}
	// A burst of 200 acks after a reconnect: nothing lost, the last 80 spread over about 1.3 s.
	var acks frameGate
	start = now
	for i := 0; i < 200; i++ {
		now = now.Add(acks.wait(now))
	}
	if got := now.Sub(start); got < 1300*time.Millisecond || got > 1400*time.Millisecond {
		t.Fatalf("200 acks took %v", got)
	}
}
