package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var radTestSecret = []byte("radiance-test-secret-radiance-test-secret")

type radRig struct {
	t      *testing.T
	hub    *Hub
	m      *radianceModule
	now    time.Time
	timers []*fakeTimer
	rolls  []int
}

func newRadRig(t *testing.T) *radRig {
	t.Helper()
	r := &radRig{t: t, now: time.Unix(1_800_000_000, 0)}
	savedSecret := radSecretForTest
	radSecretForTest = radTestSecret
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.m = newRadiance(h).(*radianceModule); return r.m }}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	radSecretForTest = savedSecret
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		ft := &fakeTimer{d: r.now.Sub(time.Unix(0, 0)) + d, fn: fn}
		r.timers = append(r.timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	// randInt: the queued rolls, then always the low end.
	r.m.roll = func(lo, hi int) int {
		if len(r.rolls) > 0 {
			v := r.rolls[0]
			r.rolls = r.rolls[1:]
			return v
		}
		return lo
	}
	return r
}

func (r *radRig) advance(d time.Duration) {
	r.now = r.now.Add(d)
	for {
		ran := false
		for _, ft := range r.timers {
			if !ft.stopped && ft.d <= r.now.Sub(time.Unix(0, 0)) {
				ft.stopped = true
				ft.fn()
				ran = true
			}
		}
		if !ran {
			return
		}
	}
}

// seconds advances one second at a time, so a test can read between ticks.
func (r *radRig) seconds(n int) {
	for i := 0; i < n; i++ {
		r.advance(time.Second)
	}
}

func (r *radRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *radRig) player(id int64, name, street string, x float64) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.do(cl, map[string]any{"t": "join", "street": street, "x": x, "y": 500})
	drain(cl)
	return cl
}

func (r *radRig) token(sub int64, rank int, cards string, exp int64) string {
	return chanSign(radTestSecret, map[string]any{"k": "radiance", "sub": sub, "r": rank, "u": cards, "iat": r.now.Unix(), "exp": exp})
}

func (r *radRig) watch(cl *Client, rank int, cards string) map[string]any {
	r.do(cl, map[string]any{"t": "radiance_watch", "token": r.token(cl.ID(), rank, cards, r.now.Unix()+600)})
	return radOne(r.t, drain(cl), "radiance_status")
}

func radKind(frames []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, f := range frames {
		if f["t"] == kind {
			out = append(out, f)
		}
	}
	return out
}

// radOne is the one frame of kind among frames (fatal unless exactly one).
func radOne(t *testing.T, frames []map[string]any, kind string) map[string]any {
	t.Helper()
	got := radKind(frames, kind)
	if len(got) != 1 {
		t.Fatalf("want one %s, got %v", kind, frames)
	}
	return got[0]
}

func radError(frames []map[string]any) string {
	var out []string
	for _, f := range radKind(frames, "error") {
		out = append(out, f["message"].(string))
	}
	return strings.Join(out, " | ")
}

func TestRadianceDistanceFollowsRankAndRadiusCards(t *testing.T) {
	cases := []struct {
		rank  int
		cards string
		want  float64
	}{
		{1, "", 400}, {2, "", 800}, {3, "", 1500},
		{1, "transcendental_radius_1", 600}, {2, "transcendental_radius_1,transcendental_radius_2", 1600},
		{1, "transcendental_radius_1,transcendental_radius_2,transcendental_radius_3", radWholeStreet},
	}
	for _, c := range cases {
		w := &radWatch{rank: c.rank, cards: map[string]bool{}}
		for _, k := range strings.Split(c.cards, ",") {
			if k != "" {
				w.cards[k] = true
			}
		}
		if got := radDistance(w); got != c.want {
			t.Errorf("rank %d %q: %v, want %v", c.rank, c.cards, got, c.want)
		}
	}
}

func TestRadianceDurationIsAMultipleOfThreeStretchedByProlonger(t *testing.T) {
	fixed := func(v int) func(lo, hi int) int { return func(lo, hi int) int { return v } }
	none := map[string]bool{}
	one := map[string]bool{"transcendental_benefits_1": true}
	two := map[string]bool{"transcendental_benefits_1": true, "transcendental_benefits_2": true}
	cases := []struct {
		roll  int
		cards map[string]bool
		want  int
	}{
		{6, none, 6000}, {7, none, 6000}, {8, none, 9000}, {18, none, 18000},
		{6, one, 6000},   // 6 x 1.2 = 7.2 -> 7 -> 6
		{9, one, 12000},  // 10.8 -> 11 -> 12
		{18, two, 27000}, // 27
		{17, two, 27000}, // 17 -> 18 -> 27
	}
	for _, c := range cases {
		if got := radDuration(1, c.cards, fixed(c.roll)); got != c.want {
			t.Errorf("roll %d %v: %d, want %d", c.roll, c.cards, got, c.want)
		}
	}
	var lo, hi int
	radDuration(3, none, func(a, b int) int { lo, hi = a, b; return a })
	if lo != 12 || hi != 30 {
		t.Fatalf("rank 3 rolls %d..%d, want 12..30", lo, hi)
	}
}

func TestRadianceRefusesWithoutSkillTokenOrSomeoneNear(t *testing.T) {
	r := newRadRig(t)
	ada := r.player(1, "Ada", "GRAD", 1000)
	r.do(ada, map[string]any{"t": "radiate_start"})
	if got := radError(drain(ada)); got != radNoWatch {
		t.Fatalf("no watch: %q", got)
	}
	// Someone else's token, a forged one, an expired one.
	r.do(ada, map[string]any{"t": "radiance_watch", "token": r.token(2, 1, "", r.now.Unix()+60)})
	r.do(ada, map[string]any{"t": "radiance_watch", "token": chanSign([]byte("not-the-secret-not-the-secret-not-the"), map[string]any{"k": "radiance", "sub": 1, "r": 3, "exp": r.now.Unix() + 60})})
	r.do(ada, map[string]any{"t": "radiance_watch", "token": r.token(1, 1, "", r.now.Unix()-1)})
	r.do(ada, map[string]any{"t": "radiance_watch", "token": chanSign(radTestSecret, map[string]any{"k": "groups", "sub": 1, "r": 1, "exp": r.now.Unix() + 60})})
	if got := radKind(drain(ada), "error"); len(got) != 4 {
		t.Fatalf("bad tokens: %v", got)
	}
	st := r.watch(ada, 0, "")
	if st["rank"].(float64) != 0 {
		t.Fatalf("status %v", st)
	}
	r.do(ada, map[string]any{"t": "radiate_start"})
	if got := radError(drain(ada)); got != radNoSkill {
		t.Fatalf("rank 0: %q", got)
	}
	st = r.watch(ada, 1, "transcendental_radius_1,bogus_card")
	if st["dist"].(float64) != 600 {
		t.Fatalf("status %v", st)
	}
	r.do(ada, map[string]any{"t": "radiate_start"})
	if got := radError(drain(ada)); got != radNobody {
		t.Fatalf("alone: %q", got)
	}
	far := r.player(2, "Bo", "GRAD", 1601)
	r.advance(2 * time.Second) // radiate_start is paced
	r.do(ada, map[string]any{"t": "radiate_start"})
	if got := radError(drain(ada)); got != radNobody {
		t.Fatalf("Bo 601 px off: %q", got)
	}
	drain(far)
}

func TestRadianceRadiatesInPulsesToPlayersInRangeAndEnds(t *testing.T) {
	r := newRadRig(t)
	ada := r.player(1, "Ada", "GRAD", 1000)
	bo := r.player(2, "Bo", "GRAD", 1300)
	cy := r.player(3, "Cy", "GRAD", 1500) // 500 px: out of range at rank 1
	r.watch(ada, 1, "")
	r.rolls = []int{7} // 7 s -> 6 s
	r.do(ada, map[string]any{"t": "radiate_start"})
	af := drain(ada)
	started := radOne(t, af, "radiate_started")
	if started["duration"].(float64) != 6000 || started["recipients"].(float64) != 1 || started["dist"].(float64) != 400 || started["durationMax"].(float64) != 18000 {
		t.Fatalf("started %v", started)
	}
	rad := started["rad"].(string)
	bf := drain(bo)
	felt := radOne(t, bf, "radiate_felt")
	if felt["rad"] != rad || felt["from"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("felt %v", felt)
	}
	if m := radOne(t, bf, "meditating"); m["kind"] != "transcendental" || m["id"].(float64) != 1 {
		t.Fatalf("bubble %v", m)
	}
	cf := drain(cy)
	if len(radKind(cf, "radiate_felt")) != 0 || len(radKind(cf, "meditating")) != 1 {
		t.Fatalf("Cy %v", cf)
	}
	// Ticks at 1, 2, 3 s carry ticks 0, 1, 2: no pulse until the fourth second (ticks == 3).
	r.seconds(3)
	if len(radKind(drain(bo), "radiate_pulse")) != 0 {
		t.Fatal("pulse before the third tick")
	}
	r.seconds(1)
	p := radOne(t, drain(bo), "radiate_pulse")
	if p["n"].(float64) != 1 || p["rank"].(float64) != 1 || p["mood"] != true {
		t.Fatalf("pulse %v", p)
	}
	if b := radOne(t, drain(ada), "radiate_burst"); b["n"].(float64) != 1 || b["recipients"].(float64) != 1 {
		t.Fatalf("burst %v", b)
	}
	if len(radKind(drain(cy), "radiate_pulse")) != 0 {
		t.Fatal("Cy out of range got a pulse")
	}
	// Mood at 0: no mood for anyone from the next pulse.
	r.do(ada, map[string]any{"t": "radiate_mood", "moodless": true})
	r.seconds(3) // ticks 4, 5, 6: the second pulse, then the end (6 x 1000 >= 6000)
	bf = drain(bo)
	p = radOne(t, bf, "radiate_pulse")
	if p["n"].(float64) != 2 || p["mood"] != false {
		t.Fatalf("pulse 2 %v", p)
	}
	if e := radOne(t, bf, "radiate_ended"); e["complete"] != true || e["rad"] != rad {
		t.Fatalf("ended %v", e)
	}
	if m := radOne(t, bf, "meditating"); m["kind"] != "" {
		t.Fatalf("Bo after the end: %v", m)
	}
	af = drain(ada)
	done := radOne(t, af, "radiate_done")
	if done["why"] != "timer" || done["seconds"].(float64) != 7 || done["full"] != true || done["effectSeconds"].(float64) != 6 ||
		done["pulses"].(float64) != 2 || done["recipients"].(float64) != 1 || done["crowd"].(float64) != 3 || done["maxRelax"] != false {
		t.Fatalf("done %v", done)
	}
	if fl := done["flags"].([]any); len(fl) != 0 {
		t.Fatalf("flags with 3 on the street: %v", fl)
	}
	// meditation_cooldown 120 s.
	r.do(ada, map[string]any{"t": "radiate_start"})
	if got := radError(drain(ada)); got != radScattered {
		t.Fatalf("cooldown: %q", got)
	}
	r.advance(119 * time.Second)
	r.do(ada, map[string]any{"t": "radiate_start"})
	if got := radError(drain(ada)); got != radScattered {
		t.Fatalf("cooldown at 119 s: %q", got)
	}
	r.advance(time.Second)
	r.watch(ada, 1, "")
	r.do(ada, map[string]any{"t": "radiate_start"})
	if got := radKind(drain(ada), "radiate_started"); len(got) != 1 {
		t.Fatal("no second radiation after the cooldown")
	}
}

func TestRadianceMeetsNewArrivalsAndTellsLeaversTheyWereSomewhatTranscended(t *testing.T) {
	r := newRadRig(t)
	ada := r.player(1, "Ada", "GRAD", 1000)
	bo := r.player(2, "Bo", "GRAD", 1100)
	r.watch(ada, 2, "") // 800 px
	r.rolls = []int{21}
	r.do(ada, map[string]any{"t": "radiate_start"})
	drain(ada)
	drain(bo)
	cy := r.hub.Connect(Claims{Sub: 3, Name: "Cy"})
	r.do(cy, map[string]any{"t": "join", "street": "GRAD", "x": 1700, "y": 500})
	if m := radOne(t, drain(cy), "meditators"); len(m["list"].([]any)) != 1 {
		t.Fatalf("Cy was told %v", m)
	}
	r.seconds(4) // first pulse: Cy is new in range
	cf := drain(cy)
	if len(radKind(cf, "radiate_felt")) != 1 || len(radKind(cf, "radiate_pulse")) != 1 {
		t.Fatalf("Cy %v", cf)
	}
	// Bo walks to another street: told at the next pulse.
	r.do(bo, map[string]any{"t": "join", "street": "GELSEWHERE", "x": 0, "y": 0})
	drain(bo)
	r.seconds(3)
	if e := radOne(t, drain(bo), "radiate_ended"); e["complete"] != false {
		t.Fatalf("Bo left: %v", e)
	}
	// Cy walks out of range: nothing at the end for Cy, and Ada is alone at the next pulse.
	r.do(cy, map[string]any{"t": "move", "x": 1900, "y": 500})
	drain(cy)
	r.seconds(3)
	done := radOne(t, drain(ada), "radiate_done")
	if done["why"] != "alone" || done["full"] != false || done["recipients"].(float64) != 2 || done["effectSeconds"].(float64) != 9 {
		t.Fatalf("done %v", done)
	}
	if len(radKind(drain(cy), "radiate_ended")) != 0 {
		t.Fatal("Cy out of range was told it ended")
	}
}

func TestRadianceEndsWhenTheRadiatorMovesStopsOrLeaves(t *testing.T) {
	r := newRadRig(t)
	ada := r.player(1, "Ada", "GRAD", 1000)
	bo := r.player(2, "Bo", "GRAD", 1100)
	start := func() {
		r.cooldownReset(1)
		r.watch(ada, 1, "")
		r.do(ada, map[string]any{"t": "radiate_start"})
		if len(radKind(drain(ada), "radiate_started")) != 1 {
			t.Fatal("did not start")
		}
		drain(bo)
	}
	start()
	r.do(ada, map[string]any{"t": "move", "x": 1080, "y": 500})
	r.seconds(1)
	if d := radOne(t, drain(ada), "radiate_done"); d["why"] != "moved" {
		t.Fatalf("moved: %v", d)
	}
	if e := radOne(t, drain(bo), "radiate_ended"); e["complete"] != true {
		t.Fatalf("Bo after Ada moved: %v", e)
	}
	r.do(ada, map[string]any{"t": "move", "x": 1000, "y": 500})
	start()
	r.do(ada, map[string]any{"t": "radiate_stop", "why": "chat"})
	if d := radOne(t, drain(ada), "radiate_done"); d["why"] != "chat" || d["seconds"].(float64) != 0 {
		t.Fatalf("stop: %v", d)
	}
	start()
	r.hub.Disconnect(ada)
	if e := radOne(t, drain(bo), "radiate_ended"); e["complete"] != true {
		t.Fatalf("Bo after Ada went offline: %v", e)
	}
	if len(r.m.active) != 0 || r.m.medit[1] != "" {
		t.Fatalf("left behind: %v %v", r.m.active, r.m.medit)
	}
	for _, ft := range r.timers {
		if !ft.stopped {
			ft.fn() // a stale tick does nothing
		}
	}
}

func (r *radRig) cooldownReset(id int64) { delete(r.m.cooldown, id) }

func TestRadianceWholeStreetAndTheSharingTheOnenessFlag(t *testing.T) {
	r := newRadRig(t)
	ada := r.player(1, "Ada", "GRAD", 0)
	var others []*Client
	for i := int64(2); i <= 6; i++ {
		others = append(others, r.player(i, "P", "GRAD", float64(i)*3000))
	}
	r.watch(ada, 1, "transcendental_radius_1,transcendental_radius_2,transcendental_radius_3")
	r.do(ada, map[string]any{"t": "radiate_start"})
	st := radOne(t, drain(ada), "radiate_started")
	if st["recipients"].(float64) != 5 || st["dist"].(float64) != -1 {
		t.Fatalf("started %v", st)
	}
	r.seconds(7)
	done := radOne(t, drain(ada), "radiate_done")
	if fl := done["flags"].([]any); len(fl) != 1 || fl[0] != "transcendence_5players" {
		t.Fatalf("flags %v", done)
	}
	for _, o := range others {
		if len(radKind(drain(o), "radiate_pulse")) != 2 {
			t.Fatal("a player on the street missed a pulse")
		}
	}
}

func TestMeditatingTogetherIsCountedAndShown(t *testing.T) {
	r := newRadRig(t)
	ada := r.player(1, "Ada", "GMED", 0)
	bo := r.player(2, "Bo", "GMED", 100)
	cy := r.player(3, "Cy", "GMED", 200)
	r.do(ada, map[string]any{"t": "meditate_start", "kind": "standing"})
	if tg := radOne(t, drain(ada), "meditate_together"); tg["n"].(float64) != 0 {
		t.Fatalf("Ada alone %v", tg)
	}
	if m := radOne(t, drain(bo), "meditating"); m["kind"] != "standing" {
		t.Fatalf("Bo sees %v", m)
	}
	r.do(bo, map[string]any{"t": "meditate_start", "kind": "focused"})
	r.do(bo, map[string]any{"t": "meditate_start", "kind": "levitating"}) // not a meditation
	if tg := radOne(t, drain(ada), "meditate_together"); tg["n"].(float64) != 1 {
		t.Fatalf("Ada with Bo %v", tg)
	}
	if tg := radOne(t, drain(bo), "meditate_together"); tg["n"].(float64) != 1 || tg["kind"] != "focused" {
		t.Fatalf("Bo %v", tg)
	}
	drain(cy)
	dee := r.hub.Connect(Claims{Sub: 4, Name: "Dee"})
	r.do(dee, map[string]any{"t": "join", "street": "GMED", "x": 300, "y": 500})
	ml := radOne(t, drain(dee), "meditators")
	if list := ml["list"].([]any); len(list) != 2 {
		t.Fatalf("meditators %v", ml)
	}
	if r.m.h.ProvidedLocked("radiance.meditating").(func(int64) string)(2) != "focused" {
		t.Fatal("the provided hook")
	}
	// Bo stops: the 30 s focused floor keeps Bo from radiating at once.
	r.do(bo, map[string]any{"t": "meditate_stop"})
	if tg := radOne(t, drain(ada), "meditate_together"); tg["n"].(float64) != 0 {
		t.Fatalf("Ada after Bo %v", tg)
	}
	r.watch(bo, 1, "")
	r.do(bo, map[string]any{"t": "radiate_start"})
	if got := radError(drain(bo)); got != radScattered {
		t.Fatalf("Bo after focusing: %q", got)
	}
	// Meditating already: no radiating on top.
	r.watch(ada, 1, "")
	r.do(ada, map[string]any{"t": "radiate_start"})
	if got := radError(drain(ada)); got != radBusy {
		t.Fatalf("Ada meditating: %q", got)
	}
	// Ada walks off the street: the others see the bubble go.
	drain(cy)
	r.do(ada, map[string]any{"t": "join", "street": "GOTHER", "x": 0, "y": 0})
	if m := radOne(t, drain(cy), "meditating"); m["kind"] != "" || m["id"].(float64) != 1 {
		t.Fatalf("Cy after Ada left %v", m)
	}
	if r.m.medit[1] != "" {
		t.Fatal("Ada still meditating")
	}
}
