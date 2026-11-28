package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// startMeteorServer is startTeleportServer with a clock the test moves.
func startMeteorServer(t *testing.T) (ws, base string, advance func(time.Duration)) {
	t.Helper()
	var mu sync.Mutex
	at := time.Now()
	hub := NewHub(func() time.Time { mu.Lock(); defer mu.Unlock(); return at })
	srv := httptest.NewServer(NewServer(NewTickets(testSecret, nil), hub, nil))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws", srv.URL, func(d time.Duration) { mu.Lock(); at = at.Add(d); mu.Unlock() }
}

var meteorIDRe = regexp.MustCompile(`^mt-[0-9a-f]{16}$`)

func TestMeteorCallRefusesUnsignedBadAndReplayed(t *testing.T) {
	ws, base, _ := startMeteorServer(t)
	bob := signIn(t, ws, 2, "Bob")
	now := time.Now().Unix()
	body := map[string]any{"op": "shower", "lead": 0}
	raw, _ := json.Marshal(body)
	for name, header := range map[string]string{
		"no header":      "-",
		"garbage":        "garbage",
		"wrong secret":   signService([]byte(strings.Repeat("w", 40)), "POST", serviceRoutePrefix+"meteor", now, "00000000000000a1", raw),
		"other call":     signService(testSecret, "POST", serviceRoutePrefix+"online", now, "00000000000000a2", raw),
		"too old":        signService(testSecret, "POST", serviceRoutePrefix+"meteor", now-120, "00000000000000a3", raw),
		"other body":     signService(testSecret, "POST", serviceRoutePrefix+"meteor", now, "00000000000000a4", []byte(`{"op":"star"}`)),
		"teleport route": signService(testSecret, "POST", serviceRouteTeleport, now, "00000000000000a5", raw),
	} {
		if code, out := siteCall(t, base, "meteor", body, header); code != http.StatusUnauthorized || out["status"] != "refused" {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// Nothing reached Bob from a refused call.
	pong(t, bob, "meteor")
	for _, bad := range []map[string]any{{"op": "fireworks"}, {"op": "shower", "lead": -1}, {"op": "shower", "lead": meteorLeadMax + 1}, {"op": "star", "lead": 120}, {}} {
		if code, out := siteCall(t, base, "meteor", bad, ""); code != http.StatusBadRequest || out["status"] != "bad" {
			t.Errorf("%v: %d %v", bad, code, out)
		}
	}
	// A good call once; the same header again is a replay.
	good := signService(testSecret, "POST", serviceRoutePrefix+"meteor", now, "00000000000000b1", raw)
	code, out := siteCall(t, base, "meteor", body, good)
	mustStatus(t, code, out, 200, "started")
	code, out = siteCall(t, base, "meteor", body, good)
	mustStatus(t, code, out, 401, "refused")
	// Not socket kinds a player can send: only meteor_sync is.
	for _, kind := range []string{"meteor"} {
		bob.send(map[string]any{"t": kind, "op": "shower"})
		if e := bob.next("error"); e["code"] != "bad_frame" {
			t.Fatalf("%s: %v", kind, e)
		}
	}
}

func TestMeteorShowerReachesEveryGameOnlineWithOneSeed(t *testing.T) {
	ws, base, _ := startMeteorServer(t)
	ada := signIn(t, ws, 1, "Ada")
	bob := signIn(t, ws, 2, "Bob")
	code, out := siteCall(t, base, "meteor", map[string]any{"op": "shower", "lead": 120}, "")
	mustStatus(t, code, out, 200, "started")
	if out["players"] != 2.0 {
		t.Fatalf("players %v", out)
	}
	ev := out["event"].(map[string]any)
	a, b := ada.next("meteor"), bob.next("meteor")
	for _, f := range []map[string]any{a, b} {
		if f["id"] != ev["id"] || f["seed"] != ev["seed"] || f["start"] != ev["start"] || f["dur"] != ev["dur"] || f["kind"] != "shower" || f["hint"] != true {
			t.Fatalf("frame %v, event %v", f, ev)
		}
		if _, ok := f["now"].(float64); !ok {
			t.Fatalf("no clock: %v", f)
		}
	}
	if !meteorIDRe.MatchString(ev["id"].(string)) {
		t.Fatalf("id %v", ev["id"])
	}
	seed := ev["seed"].(float64)
	if seed < 1 || seed > meteorSeedMax {
		t.Fatalf("seed %v", seed)
	}
	dur := time.Duration(ev["dur"].(float64)) * time.Millisecond
	if dur < meteorShowerMin || dur > meteorShowerMax {
		t.Fatalf("dur %v", dur)
	}
	// Two minutes ahead, with the hint.
	if lead := ev["start"].(float64) - a["now"].(float64); lead < 119000 || lead > 121000 {
		t.Fatalf("lead %v", lead)
	}
	// One at a time.
	code, out = siteCall(t, base, "meteor", map[string]any{"op": "star"}, "")
	mustStatus(t, code, out, 200, "busy")
	pong(t, ada, "meteor")
}

func TestMeteorLateJoinerGetsTheRunningShowerAndStopEndsIt(t *testing.T) {
	ws, base, advance := startMeteorServer(t)
	ada := signIn(t, ws, 1, "Ada")
	code, out := siteCall(t, base, "meteor", map[string]any{"op": "shower"}, "")
	mustStatus(t, code, out, 200, "started")
	ev := out["event"].(map[string]any)
	if ev["hint"] != false {
		t.Fatalf("now: no hint %v", ev)
	}
	ada.next("meteor")
	advance(90 * time.Second)
	// Cy connects half way through: meteor_sync gives the same event (its start in the past) and the clock.
	cy := signIn(t, ws, 3, "Cy")
	cy.send(map[string]any{"t": "meteor_sync"})
	f := cy.next("meteor")
	if f["id"] != ev["id"] || f["seed"] != ev["seed"] || f["start"] != ev["start"] || f["dur"] != ev["dur"] {
		t.Fatalf("late frame %v, event %v", f, ev)
	}
	if f["now"].(float64)-f["start"].(float64) < 89000 {
		t.Fatalf("late joiner's clock %v", f)
	}
	code, out = siteCall(t, base, "meteor", map[string]any{"op": "status"}, "")
	mustStatus(t, code, out, 200, "ok")
	if out["event"].(map[string]any)["id"] != ev["id"] {
		t.Fatalf("status %v", out)
	}
	code, out = siteCall(t, base, "meteor", map[string]any{"op": "stop"}, "")
	mustStatus(t, code, out, 200, "stopped")
	for _, p := range []*peer{ada, cy} {
		if s := p.next("meteor"); s["kind"] != "stop" || s["id"] != ev["id"] {
			t.Fatalf("stop %v", s)
		}
	}
	code, out = siteCall(t, base, "meteor", map[string]any{"op": "stop"}, "")
	mustStatus(t, code, out, 200, "none")
	// Nothing on now: a sync sends nothing.
	cy.send(map[string]any{"t": "meteor_sync"})
	pong(t, cy, "meteor")
}

func TestMeteorEndsByItselfAndALoneStarIsShort(t *testing.T) {
	ws, base, advance := startMeteorServer(t)
	ada := signIn(t, ws, 1, "Ada")
	code, out := siteCall(t, base, "meteor", map[string]any{"op": "star"}, "")
	mustStatus(t, code, out, 200, "started")
	ev := out["event"].(map[string]any)
	if ev["kind"] != "star" || ev["dur"] != float64(meteorStarDur.Milliseconds()) || ev["hint"] != false {
		t.Fatalf("star %v", ev)
	}
	ada.next("meteor")
	advance(meteorStarDur + meteorTail)
	code, out = siteCall(t, base, "meteor", map[string]any{"op": "status"}, "")
	mustStatus(t, code, out, 200, "ok")
	if out["event"] != nil {
		t.Fatalf("over: %v", out)
	}
	ada.send(map[string]any{"t": "meteor_sync"})
	pong(t, ada, "meteor")
	// Over, so a new one may start.
	code, out = siteCall(t, base, "meteor", map[string]any{"op": "shower"}, "")
	mustStatus(t, code, out, 200, "started")
}
