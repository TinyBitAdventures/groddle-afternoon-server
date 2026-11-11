package main

// G656 (report #180): a run's goals scale with yesterday's active players.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFeatRunGoalsScaleWithYesterdaysPlayers(t *testing.T) {
	table := featTableForTest(t)
	a := table.Feats["animal_love"]
	if a.Share != 50 || table.PlayersMax != 500 {
		t.Fatalf("the share is one sitting (the personal goal), the cap 500 players: %+v %d", a, table.PlayersMax)
	}
	start := featRunStart(t, table, "animal_love")
	yday := featDay(start.Add(-24 * time.Hour).UnixMilli())
	m := newFeatsWith(NewHub(func() time.Time { return start }), table, "")
	goals := func(players int64, known bool) (featGoals, int64) {
		m.st.Days, m.st.DaysSince = map[string][]int64{}, 0
		if known {
			m.st.DaysSince = 1
			for i := int64(1); i <= players; i++ {
				m.st.Days[yday] = append(m.st.Days[yday], i)
			}
		}
		return m.runGoals(a, start.UnixMilli())
	}
	cases := []struct {
		players int64
		known   bool
		want    featGoals
		basis   int64
	}{
		{35, true, featGoals{1750, 3500, 5250}, 35}, // 11-10: 35 x 50
		{0, false, featGoals{150, 300, 450}, -1},    // unknown: the table's
		{0, true, featGoals{150, 300, 450}, 0},      // nobody: the floor
		{2, true, featGoals{150, 300, 450}, 2},      // 2 x 50 = 100 < 150: the floor
		{3, true, featGoals{150, 300, 450}, 3},      // 3 x 50 = the table's minimum
		{4, true, featGoals{200, 400, 600}, 4},
		{900, true, featGoals{25000, 50000, 75000}, 900}, // the cap: 500 players
	}
	for _, c := range cases {
		if g, basis := goals(c.players, c.known); g != c.want || basis != c.basis {
			t.Fatalf("%d players (known %v): %+v basis %d, want %+v basis %d", c.players, c.known, g, basis, c.want, c.basis)
		}
	}
	// A day only partly counted (the count began after its midnight) is unknown.
	m.st.Days = map[string][]int64{yday: {1, 2, 3, 4, 5}}
	m.st.DaysSince = start.Add(-20 * time.Hour).UnixMilli()
	if g, basis := m.runGoals(a, start.UnixMilli()); g.Goal != 150 || basis != -1 {
		t.Fatalf("a partial yesterday keeps the table's goals: %+v %d", g, basis)
	}
}

func TestFeatGoalsComeFromYesterdayAcrossRestarts(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "animal_love")
	path := filepath.Join(t.TempDir(), "feats.json")
	// The count begins two days before; player 1 is still signed in when yesterday begins.
	r := newFeatRig(t, path, nil, start.Add(-30*time.Hour), nil)
	first := r.player(1, "P1")
	r.fire(time.Second) // saveSoon
	r.now = start.Add(-20 * time.Hour)
	for i := int64(2); i <= 35; i++ {
		r.player(i, fmt.Sprintf("P%d", i))
	}
	r.player(2, "P2") // a reconnect counts once
	drain(first)
	r.fire(time.Second)
	b, _ := os.ReadFile(path)
	var st featState
	json.Unmarshal(b, &st)
	if got := len(st.Days[featDay(r.now.UnixMilli())]); got != 35 {
		t.Fatalf("yesterday's 35 distinct players are kept in the state file: %d (%v)", got, st.Days)
	}

	// A restart, and today's Feat starts: its goals are 35 sittings.
	r2 := newFeatRig(t, path, nil, start.Add(time.Hour), nil)
	a := r2.player(36, "Ada")
	st2 := kinds(drain(a), "feat_state")
	if len(st2) != 1 || st2[0]["feat"] != "animal_love" || num(st2[0]["goal"]) != 1750 || num(st2[0]["bonus"]) != 3500 || num(st2[0]["super"]) != 5250 || num(st2[0]["basis"]) != 35 {
		t.Fatalf("the run's goals from yesterday's 35 players: %v", st2)
	}
	r2.do(a, map[string]any{"t": "feat_add", "feat": "animal_love", "n": 10})
	if f := kinds(drain(a), "feat_first"); len(f) != 1 || num(f[0]["remaining"]) != 1740 {
		t.Fatalf("the vog_text's {remaining} is toward the run's minimum: %v", f)
	}

	// The run keeps its goals across a restart, whatever the day count says now.
	b, _ = os.ReadFile(path)
	var raw map[string]any
	json.Unmarshal(b, &raw)
	raw["days"] = map[string]any{}
	b, _ = json.Marshal(raw)
	os.WriteFile(path, b, 0o600)
	r3 := newFeatRig(t, path, nil, start.Add(2*time.Hour), nil)
	c := r3.player(37, "Bo")
	if s := kinds(drain(c), "feat_state"); len(s) != 1 || num(s[0]["goal"]) != 1750 || num(s[0]["total"]) != 10 {
		t.Fatalf("a restart mid-run keeps the run's goals: %v", s)
	}
}

func TestFeatScaledGoalsSetTiersAndTheRewardScale(t *testing.T) {
	table := featTableForTest(t)
	start := featRunStart(t, table, "tottlys_toys")
	led := newFakeFeatLedger()
	r := newFeatRig(t, filepath.Join(t.TempDir(), "feats.json"), led, start.Add(time.Hour), nil)
	// Yesterday: 4 players, so tottlys_toys (a sitting of 10) has 40, 80 and 120.
	r.m.st.DaysSince = 1
	r.m.st.Days = map[string][]int64{featDay(start.Add(-24 * time.Hour).UnixMilli()): {1, 2, 3, 4}}
	a := r.player(1, "Ada")
	if s := kinds(drain(a), "feat_state"); len(s) != 1 || num(s[0]["goal"]) != 40 || num(s[0]["super"]) != 120 {
		t.Fatalf("goals: %v", s)
	}
	var heard []map[string]any
	for i := 0; i < 13; i++ {
		r.do(a, map[string]any{"t": "feat_add", "feat": "tottlys_toys", "n": 10})
		heard = append(heard, drain(a)...)
		r.now = r.now.Add(30 * time.Second)
	}
	if g := kinds(heard, "feat_goal"); len(g) != 3 || num(g[0]["total"]) != 40 || num(g[2]["total"]) != 120 {
		t.Fatalf("each scaled goal met at its own number: %v", g)
	}
	r.now = start.Add(24*time.Hour + time.Second)
	r.do(a, map[string]any{"t": "feat_sync"})
	r.fire(0)
	ended := kinds(drain(a), "feat_ended")
	if len(ended) != 1 || num(ended[0]["goal"]) != 40 || num(ended[0]["tier"]) != 3 {
		t.Fatalf("ended at the supermegabonus of the run's goals: %v", ended)
	}
	// Scale min(130, 120) / 40 = 3: 170 currants -> 510.
	if led.currants[1] != 510 {
		t.Fatalf("the pool's scale is capped at the supermegabonus over the run's minimum: %v", led.currants)
	}
}
