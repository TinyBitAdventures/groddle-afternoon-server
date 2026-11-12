package main

import (
	"encoding/json"
	"testing"
	"time"
)

func newLevelsRig(t *testing.T) (*Hub, *fakeIgnores, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	ign := &fakeIgnores{ign: map[[2]int64]bool{}}
	saved := modules
	modules = []moduleFactory{newLevels, func(h *Hub) Module { return ign }}
	h := NewHub(func() time.Time { return now })
	modules = saved
	return h, ign, &now
}

func TestLevelUpIsToldToTheStreet(t *testing.T) {
	h, ign, now := newLevelsRig(t)
	send := func(cl *Client, msg map[string]any) {
		*now = now.Add(2 * time.Second)
		b, _ := json.Marshal(msg)
		h.Handle(cl, b)
	}
	ada := h.Connect(Claims{Sub: 1, Name: "Ada"})
	bo := h.Connect(Claims{Sub: 2, Name: "Bo"})
	cy := h.Connect(Claims{Sub: 3, Name: "Cy"})
	elsewhere := h.Connect(Claims{Sub: 4, Name: "Di"})
	for _, cl := range []*Client{ada, bo, cy} {
		send(cl, map[string]any{"t": "join", "street": "GA1", "x": 0, "y": -100})
	}
	send(elsewhere, map[string]any{"t": "join", "street": "GA2", "x": 0, "y": -100})
	ign.ign[[2]int64{1, 3}] = true // Ada ignores Cy (inc_stats.js 374 buddies_get_ignoring_tsids)
	for _, cl := range []*Client{ada, bo, cy, elsewhere} {
		drain(cl)
	}

	send(ada, map[string]any{"t": "level_up", "level": 7})
	got := kinds(drain(bo), "level_up")
	if len(got) != 1 || got[0]["name"] != "Ada" || got[0]["level"] != float64(7) || got[0]["id"] != float64(1) {
		t.Fatalf("Bo heard %v", got)
	}
	if n := len(kinds(drain(ada), "level_up")); n != 0 {
		t.Fatalf("Ada heard her own level-up %d times", n)
	}
	if n := len(kinds(drain(cy), "level_up")); n != 0 {
		t.Fatalf("Cy, whom Ada ignores, heard it")
	}
	if n := len(kinds(drain(elsewhere), "level_up")); n != 0 {
		t.Fatalf("another street heard it")
	}

	// The same level again, a lower one, or one out of range: nothing.
	for _, lvl := range []int{7, 5, 61, 1} {
		send(ada, map[string]any{"t": "level_up", "level": lvl})
	}
	if n := len(kinds(drain(bo), "level_up")); n != 0 {
		t.Fatalf("a repeat or bad level was told %d times", n)
	}
	send(ada, map[string]any{"t": "level_up", "level": 8})
	if n := len(kinds(drain(bo), "level_up")); n != 1 {
		t.Fatalf("level 8 was told %d times", n)
	}
}
