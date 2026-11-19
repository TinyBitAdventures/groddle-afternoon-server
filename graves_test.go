package main

import (
	"testing"
	"time"
)

// G1022 (#505): a croak's grave is the street's, and another player may mourn or celebrate it once.

type graveRig struct {
	t      *testing.T
	hub    *Hub
	now    time.Time
	timers []*toyTimer
}

func newGraveRig(t *testing.T) *graveRig {
	t.Helper()
	r := &graveRig{t: t, now: time.Unix(1_800_000_000, 0)}
	saved := modules
	modules = []moduleFactory{newGraves}
	t.Cleanup(func() { modules = saved })
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		ft := &toyTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	return r
}

func (r *graveRig) advance(d time.Duration) {
	end := r.now.Add(d)
	for _, ft := range r.timers {
		if !ft.stopped && !ft.at.After(end) {
			ft.stopped = true
			r.now = ft.at
			ft.fn()
		}
	}
	r.now = end
}

func (r *graveRig) croak(cl *Client, street string) {
	send(r.hub, cl, map[string]any{"t": "grave_set", "croak": 1, "street": street, "x": 120, "y": -80, "giant": "alph", "age": 0, "state": "none"})
	send(r.hub, cl, map[string]any{"t": "join", "street": hellOne, "x": 0, "y": 0})
}

func TestGraveIsSeenAndMournedOnceByAnotherPlayer(t *testing.T) {
	r := newGraveRig(t)
	ada := joinAt(r.hub, 1, "Ada", "GA1", 120, 0)
	bo := joinAt(r.hub, 2, "Bo", "GA1", 300, 0)
	cy := joinAt(r.hub, 3, "Cy", "GA1", 400, 0)
	drain(ada)
	drain(bo)
	drain(cy)
	r.croak(ada, "GA1")
	if g := one(t, drain(bo), "grave")["grave"].(map[string]any); g["name"] != "Ada" || g["state"] != "none" || g["giant"] != "alph" || g["owner"] != float64(1) {
		t.Fatalf("Bo sees Ada's grave appear: %v", g)
	}
	drain(ada)
	drain(cy)

	// A late arrival is sent the street's graves.
	dee := joinAt(r.hub, 4, "Dee", "GA1", 0, 0)
	if list := one(t, drain(dee), "graves")["graves"].([]any); len(list) != 1 {
		t.Fatalf("Dee: %v", list)
	}

	send(r.hub, bo, map[string]any{"t": "grave_act", "owner": 1, "verb": "mourn"})
	done := one(t, drain(bo), "grave_done")
	if done["ok"] != true || done["favor"] != float64(graveFavor) || done["name"] != "Ada" || done["giant"] != "alph" {
		t.Fatalf("Bo mourned: %v", done)
	}
	if m := one(t, drain(ada), "grave_mourned"); m["by"] != "Bo" || m["verb"] != "mourn" || m["croak"] != float64(1) {
		t.Fatalf("Ada is told (recognizeDeath): %v", m)
	}
	if g := one(t, drain(cy), "grave")["grave"].(map[string]any); g["state"] != "mourn" || g["by"] != "Bo" {
		t.Fatalf("the street sees it mourned: %v", g)
	}

	// The first wins: the source's two refusals.
	send(r.hub, cy, map[string]any{"t": "grave_act", "owner": 1, "verb": "celebrate"})
	if d := one(t, drain(cy), "grave_done"); d["ok"] != false || d["text"] != graveTextOther {
		t.Fatalf("Cy: %v", d)
	}
	send(r.hub, bo, map[string]any{"t": "grave_act", "owner": 1, "verb": "mourn"})
	if d := one(t, drain(bo), "grave_done"); d["ok"] != false || d["text"] != graveTextSelf {
		t.Fatalf("Bo again: %v", d)
	}
	if len(kinds(drain(ada), "grave_mourned")) != 0 {
		t.Fatal("one recognition a grave")
	}
}

func TestGraveRules(t *testing.T) {
	r := newGraveRig(t)
	ada := joinAt(r.hub, 1, "Ada", "GA1", 120, 0)
	bo := joinAt(r.hub, 2, "Bo", "GA2", 0, 0)
	r.croak(ada, "GA1")
	drain(bo)

	// Only from the grave's street.
	send(r.hub, bo, map[string]any{"t": "grave_act", "owner": 1, "verb": "mourn"})
	if d := one(t, drain(bo), "grave_done"); d["ok"] != false {
		t.Fatalf("from another street: %v", d)
	}
	// A living player elsewhere places no grave; nor one in Naraka or a private place.
	send(r.hub, bo, map[string]any{"t": "grave_set", "croak": 1, "street": "GA1", "x": 0, "y": 0})
	send(r.hub, bo, map[string]any{"t": "grave_set", "croak": 1, "street": "GA2", "x": 0, "y": 0})
	dead := joinAt(r.hub, 3, "Cy", hellOne, 0, 0)
	send(r.hub, dead, map[string]any{"t": "grave_set", "croak": 1, "street": hellOne, "x": 0, "y": 0})
	send(r.hub, dead, map[string]any{"t": "grave_set", "croak": 1, "street": "HOME_3", "x": 0, "y": 0})
	cy := joinAt(r.hub, 4, "Dee", "GA2", 0, 0)
	if list := one(t, drain(cy), "graves")["graves"].([]any); len(list) != 1 || list[0].(map[string]any)["name"] != "Bo" {
		t.Fatalf("GA2 has only the grave Bo set standing there: %v", list)
	}
	ga1 := joinAt(r.hub, 6, "Fay", "GA1", 0, 0)
	if list := one(t, drain(ga1), "graves")["graves"].([]any); len(list) != 1 || list[0].(map[string]any)["name"] != "Ada" {
		t.Fatalf("GA1 has Ada's only: %v", list)
	}

	// A generic Giant's grave gives no favor (graveside_marker.js 68).
	send(r.hub, bo, map[string]any{"t": "join", "street": "GA1", "x": 0, "y": 0})
	eve := joinAt(r.hub, 5, "Eve", "GA3", 0, 0)
	send(r.hub, eve, map[string]any{"t": "grave_set", "croak": 2, "street": "GA3", "x": 0, "y": 0, "giant": "nobody"})
	send(r.hub, eve, map[string]any{"t": "join", "street": hellOne})
	send(r.hub, cy, map[string]any{"t": "join", "street": "GA3"})
	drain(cy)
	send(r.hub, cy, map[string]any{"t": "grave_act", "owner": 5, "verb": "celebrate"})
	if d := one(t, drain(cy), "grave_done"); d["ok"] != true || d["favor"] != float64(0) || d["giant"] != "generic" {
		t.Fatalf("generic: %v", d)
	}
}

func TestGraveOwnerAliveOfflineAndTime(t *testing.T) {
	r := newGraveRig(t)
	ada := joinAt(r.hub, 1, "Ada", "GA1", 120, 0)
	bo := joinAt(r.hub, 2, "Bo", "GA1", 0, 0)
	r.croak(ada, "GA1")
	drain(bo)

	// Offline: the recognition waits for the next connect (player.js 200-204).
	r.hub.Disconnect(ada)
	send(r.hub, bo, map[string]any{"t": "grave_act", "owner": 1, "verb": "celebrate"})
	if d := one(t, drain(bo), "grave_done"); d["ok"] != true {
		t.Fatalf("offline owner's grave: %v", d)
	}
	ada = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	if m := one(t, drain(ada), "grave_mourned"); m["by"] != "Bo" || m["verb"] != "celebrate" {
		t.Fatalf("at login: %v", m)
	}
	r.hub.Disconnect(ada)
	ada = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	if len(kinds(drain(ada), "grave_mourned")) != 0 {
		t.Fatal("once")
	}

	// A reconnect while dead keeps the croak's grave and its state.
	send(r.hub, ada, map[string]any{"t": "grave_set", "croak": 1, "street": "GA1", "x": 120, "y": -80, "giant": "alph", "age": 60000, "state": "none"})
	send(r.hub, bo, map[string]any{"t": "grave_act", "owner": 1, "verb": "mourn"})
	if d := one(t, drain(bo), "grave_done"); d["text"] != graveTextSelf {
		t.Fatalf("still recognized: %v", d)
	}

	// Alive again: walking onto a street outside Naraka takes the grave away.
	send(r.hub, ada, map[string]any{"t": "join", "street": "GA9"})
	if g := one(t, drain(bo), "grave_gone"); g["owner"] != float64(1) {
		t.Fatalf("gone: %v", g)
	}

	// A new croak: the grave goes 60 minutes after it (onFade + onDisappear), and the owner's own
	// state (a solo companion mourned first) closes it.
	send(r.hub, ada, map[string]any{"t": "join", "street": "GA1"})
	r.croak(ada, "GA1")
	drain(bo)
	send(r.hub, ada, map[string]any{"t": "grave_set", "croak": 1, "street": "GA1", "x": 120, "y": -80, "state": "mourn", "by": "Amp"})
	if g := one(t, drain(bo), "grave")["grave"].(map[string]any); g["state"] != "mourn" || g["by"] != "Amp" {
		t.Fatalf("closed by the owner's game: %v", g)
	}
	r.advance(graveLife)
	one(t, drain(bo), "grave_gone")
	send(r.hub, bo, map[string]any{"t": "join", "street": "GA1"})
	if len(kinds(drain(bo), "graves")) != 0 {
		t.Fatal("expired: no graves to list")
	}

	// Resurrected (grave_clear), and a grave whose owner is alive though the clear never came.
	send(r.hub, ada, map[string]any{"t": "join", "street": "GA1"})
	r.croak(ada, "GA1")
	drain(bo)
	send(r.hub, ada, map[string]any{"t": "grave_clear"})
	one(t, drain(bo), "grave_gone")
}
