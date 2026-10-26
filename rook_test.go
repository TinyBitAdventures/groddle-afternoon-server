package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Hechey Track (the fresh start): a rookable street with a Spriggan shrine.
const rookTestStreet = "GLI2V0UN7RD1T2J"

type rookRig struct {
	t      *testing.T
	hub    *Hub
	mod    *rook
	now    time.Time
	timers []*objTimer
	roll   float64
}

func newRookRig(t *testing.T, dev bool) *rookRig {
	t.Helper()
	r := &rookRig{t: t, now: time.UnixMilli(1_760_000_000_000), roll: 0.5}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.mod = newRook(h).(*rook); return r.mod }}
	defer func() { modules = saved }()
	r.hub = NewHub(func() time.Time { return r.now })
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &objTimer{at: r.now.Add(d), fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	r.mod.roll = func() float64 { return r.roll }
	r.mod.dev = dev
	return r
}

func (r *rookRig) advance(d time.Duration) {
	end := r.now.Add(d)
	for {
		var next *objTimer
		for _, tm := range r.timers {
			if !tm.done && !tm.at.After(end) && (next == nil || tm.at.Before(next.at)) {
				next = tm
			}
		}
		if next == nil {
			r.now = end
			return
		}
		if next.at.After(r.now) {
			r.now = next.at
		}
		next.done = true
		next.fn()
	}
}

func (r *rookRig) shrine() rookSpot { return r.mod.w.streets[rookTestStreet] }

// player joins the test street standing at the shrine.
func (r *rookRig) player(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	sp := r.shrine()
	r.send(cl, map[string]any{"t": "join", "street": rookTestStreet, "x": sp.x, "y": sp.y})
	r.drain(cl)
	return cl
}

func (r *rookRig) send(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *rookRig) drain(cl *Client) []map[string]any {
	var out []map[string]any
	for {
		select {
		case b := <-cl.send:
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				r.t.Fatal(err)
			}
			out = append(out, m)
		default:
			return out
		}
	}
}

// evs lists the rook events in frames, "type" only.
func rookEvs(frames []map[string]any) []string {
	var out []string
	for _, f := range frames {
		if f["t"] == "rook" {
			if ev, ok := f["ev"].(map[string]any); ok {
				out = append(out, ev["type"].(string))
			}
		}
	}
	return out
}

func rookYous(frames []map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, f := range frames {
		if f["t"] == "rook_you" {
			out[f["why"].(string)] = f
		}
	}
	return out
}

func rookLastAttack(t *testing.T, frames []map[string]any) map[string]any {
	t.Helper()
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i]["t"] == "rook" {
			a, _ := frames[i]["attack"].(map[string]any)
			return a
		}
	}
	t.Fatalf("no rook frame in %v", frames)
	return nil
}

func rookLastNo(frames []map[string]any) string {
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i]["t"] == "rook_no" {
			return frames[i]["code"].(string)
		}
	}
	return ""
}

func rnum(v any) int { f, _ := v.(float64); return int(f) }

func TestRookDataIsLoaded(t *testing.T) {
	sp, ok := rookData.streets[rookTestStreet]
	if !ok || sp.giant == "" || sp.hub == "" {
		t.Fatalf("Hechey Track should be rookable with a shrine: %+v", sp)
	}
	if len(rookData.streets) < 100 || rookData.values["plank"] == 0 || !rookData.refused["pick"] {
		t.Fatalf("data looks thin: %d streets, plank %d", len(rookData.streets), rookData.values["plank"])
	}
	raw, err := os.ReadFile("data/rook.json")
	if err != nil || !strings.Contains(string(raw), "build-realtime-rook.mjs") {
		t.Fatal("data/rook.json names the tool that writes it")
	}
}

func TestRookScaleFollowsTheCrowd(t *testing.T) {
	for _, c := range []struct{ n, health, stun int }{{1, 100, 125}, {2, 200, 125}, {3, 300, 200}, {5, 500, 425}, {7, 700, 1000}, {10, 1000, 1000}, {40, 1000, 1000}} {
		h, s := rookScale(c.n)
		if h != c.health || s != c.stun {
			t.Errorf("crowd %d: health %d stun %d, want %d %d", c.n, h, s, c.health, c.stun)
		}
	}
}

func TestRookDevNeedsTheFlag(t *testing.T) {
	r := newRookRig(t, false)
	a := r.player(1, "Ada")
	r.send(a, map[string]any{"t": "rook_dev", "do": "start"})
	got := r.drain(a)
	if len(got) != 1 || got[0]["code"] != "bad_frame" {
		t.Fatalf("rook_dev without GRODDLE_ROOK_DEV is unknown: %v", got)
	}
	if r.mod.streets[rookTestStreet] != nil && r.mod.streets[rookTestStreet].attack != nil {
		t.Fatal("no attack started")
	}
}

func TestRookScheduleHeraldAndArrival(t *testing.T) {
	r := newRookRig(t, false)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.drain(a)
	r.advance(rookScheduleMin - time.Second)
	if got := rookEvs(r.drain(a)); len(got) != 0 {
		t.Fatalf("nothing before the hour: %v", got)
	}
	r.advance(time.Second)
	fa, fb := r.drain(a), r.drain(b)
	if strings.Join(rookEvs(fa), ",") != "herald_warning" || strings.Join(rookEvs(fb), ",") != "herald_warning" {
		t.Fatalf("both hear the herald: %v / %v", rookEvs(fa), rookEvs(fb))
	}
	if at := rookLastAttack(t, fa); at["phase"] != "herald" || rnum(at["clock"]) != 30000 {
		t.Fatalf("herald state %v", at)
	}
	r.advance(rookHeraldWarn)
	if got := rookEvs(r.drain(a)); strings.Join(got, ",") != "herald_warning" {
		t.Fatalf("second warning at 15 s: %v", got)
	}
	r.advance(rookHerald - rookHeraldWarn)
	fa = r.drain(a)
	at := rookLastAttack(t, fa)
	if strings.Join(rookEvs(fa), ",") != "attack_start" || at["phase"] != "build_up" || rnum(at["health"]) != 200 || rnum(at["maxStun"]) != 125 || rnum(at["crowd"]) != 2 {
		t.Fatalf("the Rook arrives scaled to two: %v %v", rookEvs(fa), at)
	}
	if rnum(at["clock"]) != int(rookWingBeat/time.Millisecond) {
		t.Fatalf("wing beat in 10 min: %v", at["clock"])
	}
	// A third arrival makes it stronger; the late joiner's sync has the whole state.
	c := r.player(3, "Cy")
	r.send(c, map[string]any{"t": "rook_sync", "keys": []string{}})
	fc := r.drain(c)
	snap := fc[len(fc)-1]
	sa := snap["attack"].(map[string]any)
	if snap["snap"] != true || rnum(sa["health"]) != 300 || rnum(sa["maxHealth"]) != 300 || rnum(sa["maxStun"]) != 200 {
		t.Fatalf("late joiner snapshot %v", snap)
	}
	if got := rookEvs(r.drain(a)); strings.Join(got, ",") != "crowd" {
		t.Fatalf("the street hears the Rook grow: %v", got)
	}
}

func TestRookOrbsStunAndAnger(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.drain(a)
	r.drain(b)

	// A lone orb fails without angering the Rook (inc_rook.js 787-811).
	r.send(a, map[string]any{"t": "rook_orb"})
	if y := rookYous(r.drain(a)); rnum(y["orb_start"]["energy"]) != -50 {
		t.Fatalf("orb start costs 50 energy: %v", y)
	}
	r.drain(b)
	r.advance(rookOrbWindow)
	fa := r.drain(a)
	y := rookYous(fa)
	if rnum(y["stun_fail"]["xp"]) != 5 || rnum(y["stun_fail"]["mood"]) != -5 || rookLastAttack(t, fa)["phase"] != "build_up" {
		t.Fatalf("lone orb: %v phase %v", y, rookLastAttack(t, fa)["phase"])
	}
	if got := strings.Join(rookEvs(fa), ","); got != "stun_failed" {
		t.Fatalf("events %s", got)
	}

	// A with B joined: 25 stun, the first attempt, angry.
	r.send(a, map[string]any{"t": "rook_orb"})
	r.send(b, map[string]any{"t": "rook_orb"})
	if rookLastNo(r.drain(b)) != "" {
		// B may start an orb of their own (three at once), but then cannot join A's.
	}
	r.send(b, map[string]any{"t": "rook_orb_join", "by": 1})
	if code := rookLastNo(r.drain(b)); code != "busy" {
		t.Fatalf("in your own orb you cannot join another: %q", code)
	}
	r.advance(rookOrbWindow) // both orbs fire alone
	r.drain(a)
	r.drain(b)
	r.send(a, map[string]any{"t": "rook_orb"})
	r.drain(b)
	r.send(b, map[string]any{"t": "rook_orb_join", "by": 1})
	yb := rookYous(r.drain(b))
	if rnum(yb["orb_join"]["energy"]) != -25 || rnum(yb["orb_join"]["xp"]) != 10 {
		t.Fatalf("joining: %v", yb)
	}
	r.drain(a)
	r.advance(rookOrbWindow)
	fa, fb := r.drain(a), r.drain(b)
	ya, yb := rookYous(fa), rookYous(fb)
	if rnum(ya["stun_lead"]["xp"]) != 10 || rnum(ya["stun_assist"]["xp"]) != 25 || rnum(yb["stun_assist"]["xp"]) != 25 || yb["stun_lead"] != nil {
		t.Fatalf("stun rewards: A %v B %v", ya, yb)
	}
	at := rookLastAttack(t, fb)
	if at["phase"] != "angry" || at["angry"] != "angry1" || rnum(at["stun"]) != 25 || rnum(at["clock"]) != 120000 {
		t.Fatalf("after the first stun: %v", at)
	}
	var stun map[string]any
	for _, f := range fb {
		if ev, _ := f["ev"].(map[string]any); ev != nil && ev["type"] == "stun" {
			stun = ev
		}
	}
	if stun == nil || stun["first"] != true || stun["text"] != "Ada and 1 other led an attack against the Rook, stunning it for 25 points. 100 remaining to stun the Rook." {
		t.Fatalf("stun event %v", stun)
	}
	// Four more orbs within the two minutes: STUNNED! at 125, angry2 on the way.
	seen := []string{}
	for i := 0; i < 4; i++ {
		r.send(a, map[string]any{"t": "rook_orb"})
		r.send(b, map[string]any{"t": "rook_orb_join", "by": 1})
		r.advance(rookOrbWindow)
		seen = append(seen, rookEvs(r.drain(a))...)
	}
	r.drain(b)
	if !strings.Contains(strings.Join(seen, ","), "angry2") {
		t.Fatalf("angry2 a minute in: %v", seen)
	}
	at = rookLastAttack(t, []map[string]any{r.mod.message(rookTestStreet, nil)})
	if at["phase"] != "stunned" || at["stun"] != 125 || at["clock"].(int64) != 120000 {
		t.Fatalf("stunned: %v (%v)", at, seen)
	}
}

func TestRookAngryClockEndsInAWingBeat(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.send(a, map[string]any{"t": "rook_orb"})
	r.send(b, map[string]any{"t": "rook_orb_join", "by": 1})
	r.advance(rookOrbWindow)
	r.drain(a)
	r.advance(rookAngry2After)
	if got := rookEvs(r.drain(a)); strings.Join(got, ",") != "angry2" {
		t.Fatalf("angry2: %v", got)
	}
	r.advance(rookAngry3After)
	if got := rookEvs(r.drain(a)); strings.Join(got, ",") != "angry3" {
		t.Fatalf("angry3: %v", got)
	}
	r.advance(rookAngryWingBeat - rookAngry2After - rookAngry3After)
	fa := r.drain(a)
	at := rookLastAttack(t, fa)
	if strings.Join(rookEvs(fa), ",") != "wing_beat" || at["phase"] != "build_up" || rnum(at["stun"]) != 0 || rnum(at["clock"]) != 600000 {
		t.Fatalf("the angry Rook strikes at two minutes and its stun resets: %v %v", rookEvs(fa), at)
	}
}

func rookStunNow(r *rookRig, cl *Client) {
	r.send(cl, map[string]any{"t": "rook_dev", "do": "stun"})
}

func TestRookPrimeStrikeDefeatAndRewards(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	// Not stunned: no priming.
	r.send(a, map[string]any{"t": "rook_prime", "bonus": 0})
	if code := rookLastNo(r.drain(a)); code != "not_stunned" {
		t.Fatalf("prime before a stun: %q", code)
	}
	rookStunNow(r, a)
	r.drain(a)
	r.drain(b)
	// B strikes before anyone primes.
	r.send(b, map[string]any{"t": "rook_strike", "item": "plank", "count": 10})
	if code := rookLastNo(r.drain(b)); code != "not_primed" {
		t.Fatalf("strike unprimed: %q", code)
	}
	// Too far from the shrine to prime.
	sp := r.shrine()
	r.send(a, map[string]any{"t": "move", "x": sp.x + 2000, "y": sp.y})
	r.send(a, map[string]any{"t": "rook_prime", "bonus": 0})
	if code := rookLastNo(r.drain(a)); code != "far" {
		t.Fatalf("prime from afar: %q", code)
	}
	r.send(a, map[string]any{"t": "move", "x": sp.x - 150, "y": sp.y})
	r.send(a, map[string]any{"t": "rook_prime", "bonus": 1})
	if y := rookYous(r.drain(a)); rnum(y["prime"]["energy"]) != -10 {
		t.Fatalf("prime costs 10: %v", y)
	}
	r.send(b, map[string]any{"t": "rook_prime", "bonus": 0})
	if code := rookLastNo(r.drain(b)); code != "primed" {
		t.Fatalf("one primer at a time: %q", code)
	}
	// B offers 100 planks: 10% of 5 x 100 = 50, +25% from A's Shrine On card = 63 (rounded).
	r.send(b, map[string]any{"t": "rook_strike", "item": "plank", "count": 100})
	fb := r.drain(b)
	yb := rookYous(fb)
	if rnum(yb["strike"]["damage"]) != 63 || rnum(yb["strike"]["xp"]) != 63 || yb["strike"]["item"] != "plank" {
		t.Fatalf("strike: %v", yb)
	}
	if at := rookLastAttack(t, fb); rnum(at["health"]) != 200-63 {
		t.Fatalf("health for everyone: %v", at)
	}
	r.drain(a)
	// Too soon again; a refused item; a tiny offering.
	r.send(b, map[string]any{"t": "rook_strike", "item": "plank", "count": 1})
	if code := rookLastNo(r.drain(b)); code != "recharging" {
		t.Fatalf("3 s between strikes: %q", code)
	}
	r.advance(rookStrikeEvery)
	for _, c := range []struct {
		item  string
		count int
		code  string
	}{{"pick", 1, "refused"}, {"emblem_lem", 1, "refused"}, {"plank", 1, "refused"}, {"plank", 0, "bad"}, {"Plank!", 1, "bad"}} {
		r.send(b, map[string]any{"t": "rook_strike", "item": c.item, "count": c.count})
		if code := rookLastNo(r.drain(b)); code != c.code {
			t.Fatalf("%s x%d: %q, want %q", c.item, c.count, code, c.code)
		}
	}
	// A strikes with the shrine's own emblem: 250 (+63), capped at what is left: defeated.
	r.send(a, map[string]any{"t": "rook_strike", "item": "emblem_" + sp.giant, "count": 1})
	fa := r.drain(a)
	fb = r.drain(b)
	ya, yb := rookYous(fa), rookYous(fb)
	if rnum(ya["strike"]["damage"]) != 137 {
		t.Fatalf("capped strike: %v", ya)
	}
	// Contribution: A dealt 137, B 63 (inc_rook.js 197-202); A primed for 200 total (shrines.js 937).
	if rnum(ya["defeat_help"]["xp"]) != 137*2+50 || rnum(yb["defeat_help"]["xp"]) != 63*2+50 || rnum(ya["prime_done"]["xp"]) != 100 {
		t.Fatalf("rewards A %v B %v", ya, yb)
	}
	if got := strings.Join(rookEvs(fb), ","); got != "strike,defeated" {
		t.Fatalf("events %s", got)
	}
	// A witness who arrived and never struck gets 50: start again on a fresh rig below.
	r.advance(rookClearAfter)
	if got := rookEvs(r.drain(a)); strings.Join(got, ",") != "clear" {
		t.Fatalf("clear after 15 s: %v", got)
	}
	if s := r.mod.streets[rookTestStreet]; s == nil || s.attack != nil || !s.restUntil.After(r.now) {
		t.Fatal("the street rests after the attack")
	}
}

func TestRookWitnessGetsFifty(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	c := r.player(3, "Cy")
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	rookStunNow(r, a)
	r.send(a, map[string]any{"t": "rook_prime", "bonus": 0})
	r.send(a, map[string]any{"t": "rook_strike", "item": "emblem_" + r.shrine().giant, "count": 1})
	if y := rookYous(r.drain(c)); rnum(y["defeat_witness"]["xp"]) != 50 || y["defeat_help"] != nil {
		t.Fatalf("witness: %v", y)
	}
}

func TestRookWingBeatsRookTheStreetAndTheRookLeaves(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	keys := []string{"npc_piggy:0", "npc_piggy:1", "trant_bean:0", "npc_butterfly:3", "pc:1", "npc_piggy:x", "rock_beryl_1:0"}
	r.send(a, map[string]any{"t": "rook_sync", "keys": keys})
	r.send(b, map[string]any{"t": "rook_sync", "keys": []string{"npc_chicken:0"}})
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.drain(a)
	r.drain(b)
	r.roll = 0.4 // every roll under 50%: all rooked
	r.advance(rookWingBeat)
	fa, fb := r.drain(a), r.drain(b)
	if y := rookYous(fb); rnum(y["wing_beat"]["energy"]) != -20 || rnum(y["wing_beat"]["mood"]) != -20 {
		t.Fatalf("a wing beat costs everyone: %v", y)
	}
	var hit []any
	for _, f := range fa {
		if ev, _ := f["ev"].(map[string]any); ev != nil && ev["type"] == "wing_beat" {
			hit = ev["hit"].([]any)
		}
	}
	if len(hit) != 5 {
		t.Fatalf("the five real rookable keys of both players are rooked: %v", hit)
	}
	r.roll = 0.9
	r.advance(rookWingBeat)
	r.drain(b)
	if at := rookLastAttack(t, r.drain(a)); rnum(at["wingBeats"]) != 2 || at["phase"] != "build_up" {
		t.Fatalf("second beat %v", at)
	}
	r.advance(rookWingBeat)
	fa = r.drain(a)
	if got := strings.Join(rookEvs(fa), ","); got != "wing_beat,depart" {
		t.Fatalf("the third beat sends the Rook off: %s", got)
	}
	r.advance(rookDepartClear)
	fa = r.drain(a)
	if got := strings.Join(rookEvs(fa), ","); got != "clear" {
		t.Fatalf("clear %s", got)
	}
	rooked := fa[len(fa)-1]["rooked"].(map[string]any)
	if len(rooked) != 5 || fa[len(fa)-1]["attack"] != nil {
		t.Fatalf("the aftermath stays: %v", fa[len(fa)-1])
	}
}

func TestRookReviveNeedsThreeHealersOrABalm(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{"npc_piggy:0", "npc_piggy:1"}})
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.roll = 0
	r.send(a, map[string]any{"t": "rook_dev", "do": "wingbeat"})
	r.drain(a)
	r.drain(b)
	r.send(a, map[string]any{"t": "rook_revive", "key": "npc_piggy:0"})
	ya := rookYous(r.drain(a))
	if y := ya["revive"]; rnum(y["xp"]) != 25 || rnum(y["energy"]) != -25 || rnum(y["mood"]) != -25 || rnum(y["doses"]) != 1 || y["done"] != false {
		t.Fatalf("first dose: %v", y)
	}
	r.send(a, map[string]any{"t": "rook_revive", "key": "npc_piggy:0"})
	if code := rookLastNo(r.drain(a)); code != "done" {
		t.Fatalf("one dose per player: %q", code)
	}
	r.send(b, map[string]any{"t": "rook_revive", "key": "npc_piggy:0"})
	fb := r.drain(b)
	if y := rookYous(fb)["revive"]; rnum(y["doses"]) != 2 {
		t.Fatalf("second dose: %v", y)
	}
	if rk := fb[len(fb)-1]["rooked"].(map[string]any)["npc_piggy:0"].(map[string]any); rnum(rk["doses"]) != 2 {
		t.Fatalf("everyone sees two doses: %v", rk)
	}
	// A pours a balm: a full revive (+200), the final bonus and favor.
	r.send(a, map[string]any{"t": "rook_revive", "key": "npc_piggy:0", "balm": true})
	fa := r.drain(a)
	if y := rookYous(fa)["revive"]; rnum(y["xp"]) != 25+200+25 || rnum(y["favor"]) != 10 || y["done"] != true {
		t.Fatalf("balm: %v", y)
	}
	if _, still := fa[len(fa)-1]["rooked"].(map[string]any)["npc_piggy:0"]; still {
		t.Fatal("revived for everyone")
	}
	r.send(b, map[string]any{"t": "rook_revive", "key": "npc_piggy:0"})
	if code := rookLastNo(r.drain(b)); code != "not_rooked" {
		t.Fatalf("gone: %q", code)
	}
}

func TestRookOrbLimitsAndBreak(t *testing.T) {
	r := newRookRig(t, true)
	var cls []*Client
	for i := int64(1); i <= 12; i++ {
		cls = append(cls, r.player(i, "P"+string(rune('a'+i))))
	}
	r.send(cls[0], map[string]any{"t": "rook_dev", "do": "attack"})
	for _, i := range []int{0, 1, 2} {
		r.send(cls[i], map[string]any{"t": "rook_orb"})
	}
	r.send(cls[3], map[string]any{"t": "rook_orb"})
	if code := rookLastNo(r.drain(cls[3])); code != "full" {
		t.Fatalf("three orbs at most: %q", code)
	}
	r.send(cls[3], map[string]any{"t": "rook_orb_join", "by": 99})
	if code := rookLastNo(r.drain(cls[3])); code != "gone" {
		t.Fatalf("no such orb: %q", code)
	}
	// Nine join player 1's orb: it breaks at once, a failure that angers the Rook.
	for i := 3; i < 12; i++ {
		r.send(cls[i], map[string]any{"t": "rook_orb_join", "by": 1})
	}
	f := r.drain(cls[0])
	if y := rookYous(f); y["stun_fail"]["broke"] != true {
		t.Fatalf("broken orb: %v", y)
	}
	if at := rookLastAttack(t, f); at["phase"] != "angry" || rnum(at["stun"]) != 0 {
		t.Fatalf("a broken orb angers: %v", at)
	}
}

func TestRookHubSkipsTheLastAndRestingStreets(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	rookStunNow(r, a)
	r.send(a, map[string]any{"t": "rook_prime", "bonus": 0})
	r.send(a, map[string]any{"t": "rook_strike", "item": "emblem_" + r.shrine().giant, "count": 1})
	r.advance(rookClearAfter)
	r.drain(a)
	// The hub fires an hour later with only the last, resting street occupied: nothing starts.
	r.advance(rookScheduleMin)
	if s := r.mod.streets[rookTestStreet]; s != nil && s.attack != nil {
		t.Fatal("the last location is never picked again")
	}
	hub := r.mod.w.streets[rookTestStreet].hub
	if r.mod.hubs[hub].timer == nil {
		t.Fatal("the hub waits for its next turn while someone is there")
	}
	// A dev start still works on a resting street (main.js /rook_attack has no such check).
	r.send(a, map[string]any{"t": "rook_dev", "do": "start"})
	if got := rookEvs(r.drain(a)); strings.Join(got, ",") != "herald_warning" {
		t.Fatalf("dev start: %v", got)
	}
	r.send(a, map[string]any{"t": "rook_dev", "do": "start"})
	if code := rookLastNo(r.drain(a)); code != "rooked" {
		t.Fatalf("already rooked: %q", code)
	}
}

func TestRookLeavingFoldsYourOrbAndPrime(t *testing.T) {
	r := newRookRig(t, true)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.send(a, map[string]any{"t": "rook_orb"})
	r.drain(a)
	r.drain(b)
	r.send(a, map[string]any{"t": "join", "street": "GA5101HF7F429V5"})
	r.drain(a)
	if got := rookEvs(r.drain(b)); strings.Join(got, ",") != "orb_cancel" {
		t.Fatalf("A's orb leaves with A: %v", got)
	}
	r.advance(rookOrbWindow)
	if len(rookYous(r.drain(a))) != 0 {
		t.Fatal("a folded orb never fires")
	}
	// Off a non-rookable street, sync answers none.
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{"npc_piggy:0"}})
	f := r.drain(a)
	if len(f) != 1 || f[0]["none"] != true || f[0]["attack"] != nil {
		t.Fatalf("not rookable: %v", f)
	}
	if r.mod.streets["GA5101HF7F429V5"] != nil {
		t.Fatal("nothing kept for a street the Rook never visits")
	}
}
