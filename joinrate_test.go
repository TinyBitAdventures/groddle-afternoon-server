package main

import (
	"testing"
	"time"
)

type noModule struct{}

func (noModule) Register(*Registry) {}

func TestStreetJoinsPastTwoASecondWaitAndTheLastOneLands(t *testing.T) {
	r := newClockRig(t, func(h *Hub) Module { return noModule{} })
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.join(bo, "GB", 0)
	drain(bo)
	// The burst lands at once.
	for _, s := range []string{"GA1", "GA2", "GA3", "GB"} {
		r.join(ada, s, 0)
	}
	if ada.Street() != "GB" || len(kinds(drain(bo), "joined")) != 1 {
		t.Fatalf("the burst: on %s", ada.Street())
	}
	// Past it: held, a newer one replaces it, nothing broadcast meanwhile.
	drain(ada)
	r.join(ada, "GA1", 0)
	r.join(ada, "GA2", 0)
	r.join(ada, "GB", 5)
	if ada.Street() != "GB" || len(kinds(drain(bo), "left")) != 0 {
		t.Fatalf("held joins moved Ada to %s", ada.Street())
	}
	r.advance(joinEvery)
	if ada.Street() != "GB" || ada.player.X != 5 {
		t.Fatalf("the last join lands: %s %v", ada.Street(), ada.player.X)
	}
	if st := last(t, drain(ada), "street"); st["street"] != "GB" {
		t.Fatalf("street %v", st)
	}
	// A leave forgets a join still waiting.
	for i := 0; i < joinBurst+1; i++ {
		r.join(ada, "GA9", 0)
	}
	r.do(ada, map[string]any{"t": "leave"})
	r.advance(5 * time.Second)
	if ada.Street() != "" {
		t.Fatalf("after a leave, a waiting join put Ada on %s", ada.Street())
	}
	if ada.joinNext != nil || ada.joinWait != nil {
		t.Fatal("the waiting join was not forgotten")
	}
	// An honest pace (a street every second) never waits.
	r.advance(10 * time.Second)
	for i := 0; i < 20; i++ {
		r.advance(time.Second)
		street := []string{"GA1", "GA2"}[i%2]
		r.join(ada, street, 0)
		if ada.Street() != street {
			t.Fatalf("join %d waited", i)
		}
	}
}
