package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rookCall runs one of the console's Rook calls on a rig's hub, as the signed service would.
func rookCall(t *testing.T, r *rookRig, call string, body string) (int, map[string]any) {
	t.Helper()
	code, out := serviceHandlers[call](r.hub, []byte(body))
	raw, _ := json.Marshal(out)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return code, m
}

// noRewards fails on any rook_you in frames: an administrator's end pays and costs nothing.
func noRewards(t *testing.T, who string, frames []map[string]any) {
	t.Helper()
	if y := rookYous(frames); len(y) != 0 {
		t.Fatalf("%s got %v", who, y)
	}
	for _, f := range frames {
		if ev, _ := f["ev"].(map[string]any); ev != nil && (ev["type"] == "defeated" || ev["type"] == "wing_beat" || ev["type"] == "stun") {
			t.Fatalf("%s saw %v", who, ev)
		}
	}
}

func TestAdminRookCallsRefuseUnsignedBadAndReplayed(t *testing.T) {
	ws, base := startTeleportServer(t)
	signIn(t, ws, 2, "Bob")
	now := time.Now().Unix()
	calls := map[string]any{"rooks": map[string]any{}, "rook_end": map[string]any{"street": rookTestStreet}}
	for call, body := range calls {
		raw, _ := json.Marshal(body)
		for name, header := range map[string]string{
			"no header":      "-",
			"garbage":        "garbage",
			"wrong secret":   signService([]byte(strings.Repeat("w", 40)), "POST", serviceRoutePrefix+call, now, "00000000000000e1", raw),
			"other call":     signService(testSecret, "POST", serviceRoutePrefix+"online", now, "00000000000000e2", raw),
			"too old":        signService(testSecret, "POST", serviceRoutePrefix+call, now-120, "00000000000000e3", raw),
			"other body":     signService(testSecret, "POST", serviceRoutePrefix+call, now, "00000000000000e4", []byte(`{"street":"GOTHER"}`)),
			"teleport route": signService(testSecret, "POST", serviceRouteTeleport, now, "00000000000000e5", raw),
		} {
			if code, out := siteCall(t, base, call, body, header); code != http.StatusUnauthorized || out["status"] != "refused" {
				t.Errorf("%s %s: %d %v", call, name, code, out)
			}
		}
	}
	raw, _ := json.Marshal(calls["rook_end"])
	good := signService(testSecret, "POST", serviceRoutePrefix+"rook_end", now, "00000000000000f1", raw)
	code, out := siteCall(t, base, "rook_end", calls["rook_end"], good)
	mustStatus(t, code, out, 200, "none")
	code, out = siteCall(t, base, "rook_end", calls["rook_end"], good)
	mustStatus(t, code, out, 401, "refused")
	code, out = siteCall(t, base, "rooks", map[string]any{}, "")
	mustStatus(t, code, out, 200, "ok")
	if a, ok := out["attacks"].([]any); !ok || len(a) != 0 {
		t.Fatalf("no attacks: %v", out)
	}
	code, out = siteCall(t, base, "rook_end", map[string]any{"street": "../etc"}, "")
	mustStatus(t, code, out, 400, "bad")
	// Not socket kinds.
	bob := signIn(t, ws, 3, "Cy")
	for _, kind := range []string{"rooks", "rook_end"} {
		bob.send(map[string]any{"t": kind, "street": rookTestStreet})
		if e := bob.next("error"); e["code"] != "bad_frame" {
			t.Fatalf("%s: %v", kind, e)
		}
	}
}

func TestAdminRookEndOnAStreetWithNoRook(t *testing.T) {
	r := newRookRig(t, true)
	r.player(1, "Ada")
	for _, street := range []string{"GNOTASTREET", rookTestStreet} {
		if code, out := rookCall(t, r, "rook_end", `{"street":"`+street+`"}`); code != 200 || out["status"] != "none" {
			t.Fatalf("%s: %d %v", street, code, out)
		}
	}
	for _, body := range []string{`{}`, `{"street":""}`, `{"street":7}`, `nope`} {
		if code, out := rookCall(t, r, "rook_end", body); code != 400 || out["status"] != "bad" {
			t.Fatalf("%s: %d %v", body, code, out)
		}
	}
}

func TestAdminRooksListsAndRookEndSendsTheRookOffWithNoRewards(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{"npc_piggy:0", "npc_piggy:1", "trant_bean:0"}})
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.roll = 0.4 // the first wing beat rooks all three
	r.advance(rookWingBeat)
	r.roll = 0.5
	r.send(b, map[string]any{"t": "rook_orb"}) // an orb under way when the end comes
	r.drain(a)
	r.drain(b)

	code, out := rookCall(t, r, "rooks", `{}`)
	list, _ := out["attacks"].([]any)
	if code != 200 || out["status"] != "ok" || len(list) != 1 {
		t.Fatalf("rooks: %d %v", code, out)
	}
	at := list[0].(map[string]any)
	players, _ := at["players"].([]any)
	if at["street"] != rookTestStreet || at["hub"] != r.shrine().hub || at["phase"] != "build_up" || rnum(at["wingBeats"]) != 1 || rnum(at["health"]) != 200 || rnum(at["maxHealth"]) != 200 ||
		rnum(at["orbs"]) != 1 || at["primed"] != false || at["scheduled"] != false || rnum(at["clock"]) != int(rookWingBeat/time.Millisecond) || len(players) != 2 ||
		players[0].(map[string]any)["name"] != "Ada" || rnum(players[1].(map[string]any)["id"]) != 2 {
		t.Fatalf("the attack as the console sees it: %v", at)
	}

	code, out = rookCall(t, r, "rook_end", `{"street":"`+rookTestStreet+`"}`)
	if code != 200 || out["status"] != "ended" || out["phase"] != "build_up" || rnum(out["players"]) != 2 {
		t.Fatalf("rook_end: %d %v", code, out)
	}
	for _, cl := range []*Client{a, b} {
		f := r.drain(cl)
		noRewards(t, cl.Name(), f)
		if got := strings.Join(rookEvs(f), ","); got != "depart" {
			t.Fatalf("%s sees the Rook fly off: %s", cl.Name(), got)
		}
		st := rookLastAttack(t, f)
		if st["phase"] != "departed" || rnum(st["clock"]) != int(rookDepartClear/time.Millisecond) || len(st["orbs"].([]any)) != 0 {
			t.Fatalf("%s: departing %v", cl.Name(), st)
		}
	}
	// While it flies off: listed as departed, and a second end says so.
	if _, out := rookCall(t, r, "rooks", `{}`); out["attacks"].([]any)[0].(map[string]any)["phase"] != "departed" {
		t.Fatalf("departing in the list: %v", out)
	}
	if _, out := rookCall(t, r, "rook_end", `{"street":"`+rookTestStreet+`"}`); out["status"] != "ending" {
		t.Fatalf("a second end: %v", out)
	}
	// The orb never fires, the clear comes as after a departure, and what it rooked stays rooked.
	r.advance(rookDepartClear)
	for _, cl := range []*Client{a, b} {
		f := r.drain(cl)
		noRewards(t, cl.Name(), f)
		if got := strings.Join(rookEvs(f), ","); got != "clear" {
			t.Fatalf("%s: %s", cl.Name(), got)
		}
		last := f[len(f)-1]
		if ev := last["ev"].(map[string]any); ev["defeated"] != false || last["attack"] != nil || len(last["rooked"].(map[string]any)) != 3 {
			t.Fatalf("%s: the clear %v", cl.Name(), last)
		}
	}
	s := r.mod.streets[rookTestStreet]
	if s == nil || s.attack != nil || len(s.rooked) != 3 || !s.restUntil.Equal(r.now.Add(rookStreetRest)) {
		t.Fatalf("the street after: %+v", s)
	}
	if _, out := rookCall(t, r, "rooks", `{}`); len(out["attacks"].([]any)) != 0 {
		t.Fatalf("nothing listed after the clear: %v", out)
	}
	// Nothing else comes: no wing beat, no orb, no stun recovery.
	r.advance(2 * rookWingBeat)
	for _, cl := range []*Client{a, b} {
		if f := r.drain(cl); len(rookEvs(f)) != 0 || len(rookYous(f)) != 0 {
			t.Fatalf("%s after the end: %v", cl.Name(), f)
		}
	}
	if _, out := rookCall(t, r, "rook_end", `{"street":"`+rookTestStreet+`"}`); out["status"] != "none" {
		t.Fatalf("after the clear: %v", out)
	}
}

func TestAdminRookEndWhileStunnedAndPrimedPaysNothing(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	rookStunNow(r, a)
	r.send(a, map[string]any{"t": "rook_prime", "bonus": 0})
	r.send(b, map[string]any{"t": "rook_strike", "item": "plank", "count": 40})
	fb := r.drain(b)
	if y := rookYous(fb); y["strike"] == nil {
		t.Fatalf("the strike lands first: %v", fb)
	}
	r.drain(a)
	health := r.mod.streets[rookTestStreet].attack.health
	if _, out := rookCall(t, r, "rook_end", `{"street":"`+rookTestStreet+`"}`); out["status"] != "ended" || out["phase"] != "stunned" {
		t.Fatalf("rook_end: %v", out)
	}
	fa := r.drain(a)
	noRewards(t, "Ada", fa)
	if st := rookLastAttack(t, fa); st["primed"] != nil || st["phase"] != "departed" || rnum(st["health"]) != health {
		t.Fatalf("the shrine closes unpaid, the Rook leaves as it was: %v", st)
	}
	r.advance(rookPrimeTime + rookStunRecovery + rookDepartClear)
	noRewards(t, "Ada", r.drain(a))
	noRewards(t, "Bo", r.drain(b))
}

func TestAdminRookEndDuringTheHeraldMeansItNeverArrives(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	r.send(a, map[string]any{"t": "rook_dev", "do": "start"})
	if st := rookLastAttack(t, r.drain(a)); st["phase"] != "herald" {
		t.Fatalf("herald %v", st)
	}
	if _, out := rookCall(t, r, "rook_end", `{"street":"`+rookTestStreet+`"}`); out["status"] != "ended" || out["phase"] != "herald" {
		t.Fatalf("rook_end: %v", out)
	}
	r.advance(rookHerald + rookDepartClear)
	f := r.drain(a)
	noRewards(t, "Ada", f)
	if got := strings.Join(rookEvs(f), ","); got != "depart,clear" {
		t.Fatalf("no herald warning or arrival after the end: %s", got)
	}
}

func TestAdminRookEndIsKeptAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	r, _ := newRookTreeRigAt(t, dir, time.Time{})
	a := r.player(1, "Ada")
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{"npc_piggy:0", "npc_chicken:1"}})
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.roll = 0.4
	r.advance(rookWingBeat)
	if _, out := rookCall(t, r, "rook_end", `{"street":"`+rookTestStreet+`"}`); out["status"] != "ended" {
		t.Fatalf("rook_end: %v", out)
	}
	r.advance(rookDepartClear)
	var f rookFile
	raw, err := os.ReadFile(filepath.Join(dir, "rook.json"))
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatalf("state file: %v %s", err, raw)
	}
	rec := f.Streets[rookTestStreet]
	if rec == nil || len(rec.Rooked) != 2 || rec.Rest != msOf(r.now.Add(rookStreetRest)) {
		t.Fatalf("the file keeps the rooked things and the street's rest: %s", raw)
	}
	r2, _ := newRookTreeRigAt(t, dir, r.now.Add(10*time.Minute))
	s := r2.mod.streets[rookTestStreet]
	if s == nil || s.attack != nil || len(s.rooked) != 2 || !s.restUntil.Equal(r.now.Add(rookStreetRest)) {
		t.Fatalf("after a restart: %+v", s)
	}
}
