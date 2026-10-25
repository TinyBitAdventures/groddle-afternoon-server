package main

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

type fakeTimer struct {
	d       time.Duration
	fn      func()
	stopped bool
}

// quoinHub is a hub with only the quoins module (on test data) and fake timers.
func quoinHub(t *testing.T, d *quoinData) (*Hub, *quoinsModule, *[]*fakeTimer) {
	t.Helper()
	var mod *quoinsModule
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { mod = newQuoinsWith(h, d); return mod }}
	t.Cleanup(func() { modules = saved })
	clock := time.Unix(1_800_000_000, 0)
	hub := NewHub(func() time.Time { return clock })
	timers := &[]*fakeTimer{}
	hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		ft := &fakeTimer{d: d, fn: fn}
		*timers = append(*timers, ft)
		return func() bool { ft.stopped = true; return true }
	}
	mod.randInt = func(lo, hi int) int { return hi }
	return hub, mod, timers
}

func intp(n int) *int { return &n }

func testQuoinData() *quoinData {
	return &quoinData{
		Types: map[string]int{"xp": 5, "mood": 10, "energy": 12, "currants": 7, "mystery": 0, "favor": 5, "time": 15},
		Classes: map[string]quoinClass{
			"small random xp":        {Type: "xp", Respawn: 300, Floor: 2, Ceil: 6},
			"slow medium-large xp":   {Type: "xp", Respawn: 1800, Benefit: intp(25)},
			"small random favor":     {Type: "favor", Respawn: 300, Floor: 1, Ceil: 3},
			"small random currants":  {Type: "currants", Respawn: 180, Floor: 2, Ceil: 6},
			"fast tiny currants":     {Type: "currants", Respawn: 40, Benefit: intp(1)},
			"slow medium-large mood": {Type: "mood", Respawn: 1800, Benefit: intp(25)},
		},
		streets: map[string][]quoinSpot{
			"GQTEST": {
				{X: 1000, Y: 900, Type: "xp", Class: "slow medium-large xp"},
				{X: 2000, Y: 900, Type: "mystery", Class: "small random xp"},
				{X: 3000, Y: 900, Type: "favor", Class: "small random favor"},
				{X: 4000, Y: 900, Type: "currants", Class: "fast tiny currants"},
				{X: 5000, Y: 900, Type: "xp", Class: "small random xp"},
			},
		},
	}
}

// frames drains what a client has been sent.
func frames(cl *Client) []map[string]any {
	var out []map[string]any
	for {
		select {
		case b := <-cl.send:
			var m map[string]any
			json.Unmarshal(b, &m)
			out = append(out, m)
		default:
			return out
		}
	}
}

func lastOf(t *testing.T, cl *Client, kind string) map[string]any {
	t.Helper()
	var got map[string]any
	for _, m := range frames(cl) {
		if m["t"] == kind {
			got = m
		}
	}
	if got == nil {
		t.Fatalf("no %s frame", kind)
	}
	return got
}

func noneOf(t *testing.T, cl *Client, kind string) {
	t.Helper()
	for _, m := range frames(cl) {
		if m["t"] == kind {
			t.Fatalf("unexpected %s: %v", kind, m)
		}
	}
}

func joinAt(hub *Hub, id int64, name, street string, x, y float64) *Client {
	cl := hub.Connect(Claims{Sub: id, Name: name})
	b, _ := json.Marshal(map[string]any{"t": "join", "street": street, "x": x, "y": y})
	hub.Handle(cl, b)
	return cl
}

func send(hub *Hub, cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	hub.Handle(cl, b)
}

// Feet 28 px under the quoin's centre: touching it as game.js does.
const touchY = 900 + quoinFeetLift

func TestQuoinSyncListsTheStreetAndWhatIsGone(t *testing.T) {
	hub, _, _ := quoinHub(t, testQuoinData())
	ada := joinAt(hub, 1, "Ada", "GQTEST", 1000, touchY)
	send(hub, ada, map[string]any{"t": "quoin_sync", "street": "GQTEST"})
	got := lastOf(t, ada, "quoins")
	if got["shared"] != true || len(got["quoins"].([]any)) != 5 || len(got["gone"].([]any)) != 0 {
		t.Fatalf("sync %v", got)
	}
	q0 := got["quoins"].([]any)[0].(map[string]any)
	if q0["i"] != 0.0 || q0["x"] != 1000.0 || q0["type"] != "xp" || q0["class"] != "slow medium-large xp" {
		t.Fatalf("first quoin %v", q0)
	}
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0})
	bo := joinAt(hub, 2, "Bo", "GQTEST", 1500, touchY)
	frames(bo)
	send(hub, bo, map[string]any{"t": "quoin_sync", "street": "GQTEST"})
	if gone := lastOf(t, bo, "quoins")["gone"].([]any); len(gone) != 1 || gone[0] != 0.0 {
		t.Fatalf("a joiner sees the taken quoin gone, got %v", gone)
	}
	// A street with no quoins data, and a sync for a street you are not on.
	cy := joinAt(hub, 3, "Cy", "GNOQUOINS", 0, 0)
	send(hub, cy, map[string]any{"t": "quoin_sync", "street": "GNOQUOINS"})
	if got := lastOf(t, cy, "quoins"); got["shared"] != false || got["quoins"] != nil {
		t.Fatalf("no data: %v", got)
	}
	send(hub, cy, map[string]any{"t": "quoin_sync", "street": "GQTEST"})
	noneOf(t, cy, "quoins")
}

func TestTheFirstToucherGetsTheQuoinAndItComesBack(t *testing.T) {
	hub, _, timers := quoinHub(t, testQuoinData())
	ada := joinAt(hub, 1, "Ada", "GQTEST", 1000, touchY)
	bo := joinAt(hub, 2, "Bo", "GQTEST", 1010, touchY)
	elsewhere := joinAt(hub, 3, "Cy", "GOTHER", 1000, touchY)
	frames(ada)
	frames(bo)
	frames(elsewhere)

	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0})
	send(hub, bo, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0})

	got := lastOf(t, ada, "quoin_got")
	if got["i"] != 0.0 || got["amount"] != 25.0 || got["type"] != "xp" {
		t.Fatalf("claimer got %v", got)
	}
	bf := frames(bo)
	var taken, refused map[string]any
	for _, m := range bf {
		switch m["t"] {
		case "quoin_taken":
			taken = m
		case "quoin_refused":
			refused = m
		}
	}
	if taken == nil || taken["id"] != 1.0 || taken["name"] != "Ada" || taken["i"] != 0.0 {
		t.Fatalf("the other player hears it vanish: %v", bf)
	}
	if refused == nil || refused["why"] != "gone" {
		t.Fatalf("the second toucher is refused: %v", bf)
	}
	noneOf(t, elsewhere, "quoin_taken")

	// quoin.js 305: the class's respawn_time (slow medium-large: 30 minutes).
	if len(*timers) != 1 || (*timers)[0].d != 30*time.Minute {
		t.Fatalf("timers %+v", *timers)
	}
	(*timers)[0].fn()
	for _, cl := range []*Client{ada, bo} {
		if back := lastOf(t, cl, "quoin_back"); back["i"] != 0.0 || back["street"] != "GQTEST" {
			t.Fatalf("respawn %v", back)
		}
	}
	send(hub, bo, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0})
	if got := lastOf(t, bo, "quoin_got"); got["amount"] != 25.0 {
		t.Fatalf("back and claimable: %v", got)
	}
}

func TestClaimsAreChecked(t *testing.T) {
	hub, _, _ := quoinHub(t, testQuoinData())
	ada := joinAt(hub, 1, "Ada", "GQTEST", 1000+quoinTouch+quoinTouchSlack+5, touchY)
	frames(ada)
	why := func(msg map[string]any) string {
		t.Helper()
		send(hub, ada, msg)
		return lastOf(t, ada, "quoin_refused")["why"].(string)
	}
	if w := why(map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0}); w != "far" {
		t.Fatalf("too far: %s", w)
	}
	if w := why(map[string]any{"t": "quoin_claim", "street": "GOTHER", "i": 0}); w != "street" {
		t.Fatalf("another street: %s", w)
	}
	if w := why(map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 99}); w != "unknown" {
		t.Fatalf("no such quoin: %s", w)
	}
	if w := why(map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": -1}); w != "unknown" {
		t.Fatalf("negative index: %s", w)
	}
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST"})
	if e := lastOf(t, ada, "error"); e["code"] != "bad_quoin" {
		t.Fatalf("no index: %v", e)
	}
	// Within the slack: the last move may trail the touch.
	send(hub, ada, map[string]any{"t": "move", "x": 1000 + quoinTouch + quoinTouchSlack - 5, "y": touchY})
	frames(ada)
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0})
	lastOf(t, ada, "quoin_got")
	// Off the street.
	send(hub, ada, map[string]any{"t": "leave"})
	if w := why(map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 4}); w != "street" {
		t.Fatalf("off every street: %s", w)
	}
}

func TestClaimsAreRateLimited(t *testing.T) {
	hub, _, _ := quoinHub(t, testQuoinData())
	ada := joinAt(hub, 1, "Ada", "GQTEST", 0, 0)
	frames(ada)
	slow := 0
	for i := 0; i < claimsPerSec+3; i++ {
		send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0})
		for _, m := range frames(ada) {
			if m["why"] == "slow" {
				slow++
			}
		}
	}
	if slow != 3 {
		t.Fatalf("slow refusals %d", slow)
	}
}

func TestShardsFollowTheSourceRule(t *testing.T) {
	hub, _, _ := quoinHub(t, testQuoinData())
	ada := joinAt(hub, 1, "Ada", "GQTEST", 1000, touchY)
	near := joinAt(hub, 2, "Bo", "GQTEST", 1010, touchY)   // 10 px: 50%
	mid := joinAt(hub, 3, "Cy", "GQTEST", 1162.5, touchY)  // 162.5 px: 25%
	edge := joinAt(hub, 4, "Di", "GQTEST", 1300, touchY)   // exactly 300 px: 5%
	afk := joinAt(hub, 5, "Ed", "GQTEST", 1020, touchY)    // AFK: counts, gets nothing
	follow := joinAt(hub, 6, "Fi", "GQTEST", 1030, touchY) // following: counts, gets nothing
	far := joinAt(hub, 7, "Gu", "GQTEST", 1301, touchY)    // outside
	send(hub, mid, map[string]any{"t": "quoin_status", "mult": 3})
	send(hub, afk, map[string]any{"t": "quoin_status", "afk": true})
	send(hub, follow, map[string]any{"t": "quoin_status", "following": true})
	for _, cl := range []*Client{ada, near, mid, edge, afk, follow, far} {
		frames(cl)
	}
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0})
	got := lastOf(t, ada, "quoin_got")
	// Everyone within 300 px, the grabber included: Ada, Bo, Cy, Di, Ed, Fi.
	if got["crowd"] != 6.0 {
		t.Fatalf("crowd %v", got["crowd"])
	}
	shards := got["shards"].([]any)
	want := []struct {
		id     float64
		amount float64
		pct    float64
	}{
		{2, 13, 0.5},  // round(25 * 1 * 0.5) = 12.5 -> 13
		{3, 19, 0.25}, // round(25 * 3 * 0.25) = 18.75 -> 19
		{4, 1, 0.05},  // round(25 * 0.05) = 1.25 -> 1
	}
	if len(shards) != len(want) {
		t.Fatalf("shards %v", shards)
	}
	for k, w := range want {
		s := shards[k].(map[string]any)
		if s["id"] != w.id || s["amount"] != w.amount || s["pct"] != w.pct {
			t.Fatalf("shard %d: %v want %+v", k, s, w)
		}
	}
	taken := lastOf(t, mid, "quoin_taken")
	if len(taken["shards"].([]any)) != 3 || taken["crowd"] != 6.0 {
		t.Fatalf("the others see the same shards: %v", taken)
	}
}

func TestShardPercentageAndMinimum(t *testing.T) {
	for _, c := range []struct{ d, want float64 }{{0, 0.5}, {25, 0.5}, {162.5, 0.25}, {300, 0.05}, {200, 0.5 - 0.5*(175.0/275)}, {290, 0.05}} {
		if got := shardPct(c.d); math.Abs(got-c.want) > 1e-9 {
			t.Fatalf("shardPct(%v) = %v, want %v", c.d, got, c.want)
		}
	}
	hub, mod, _ := quoinHub(t, testQuoinData())
	mod.randInt = func(lo, hi int) int { return lo }
	ada := joinAt(hub, 1, "Ada", "GQTEST", 4000, touchY)
	bo := joinAt(hub, 2, "Bo", "GQTEST", 4290, touchY)
	frames(ada)
	frames(bo)
	// fast tiny currants: 1 currant; 1 * 0.05 rounds to 0, but a shard is at least 1 (quoin.js 118).
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 3})
	s := lastOf(t, ada, "quoin_got")["shards"].([]any)
	if len(s) != 1 || s[0].(map[string]any)["amount"] != 1.0 {
		t.Fatalf("minimum shard: %v", s)
	}
}

func TestBenefitsAndNonSharingTypes(t *testing.T) {
	hub, mod, timers := quoinHub(t, testQuoinData())
	var asked [][2]int
	mod.randInt = func(lo, hi int) int { asked = append(asked, [2]int{lo, hi}); return lo + 1 }
	ada := joinAt(hub, 1, "Ada", "GQTEST", 2000, touchY)
	bo := joinAt(hub, 2, "Bo", "GQTEST", 2010, touchY)
	frames(ada)
	frames(bo)
	// Mystery spots carry the iMG pair but pay their own type's 0 (quoin.js has no mystery branch).
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 1})
	got := lastOf(t, ada, "quoin_got")
	if got["amount"] != 0.0 || got["type"] != "mystery" || len(got["shards"].([]any)) != 0 || got["crowd"] != 0.0 {
		t.Fatalf("mystery %v", got)
	}
	if len(asked) != 0 {
		t.Fatalf("mystery rolled %v", asked)
	}
	// Favor: rolled from its class, and favor never shards (quoin.js 84).
	send(hub, ada, map[string]any{"t": "move", "x": 3000, "y": touchY})
	send(hub, bo, map[string]any{"t": "move", "x": 3005, "y": touchY})
	frames(ada)
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 2})
	got = lastOf(t, ada, "quoin_got")
	if got["amount"] != 2.0 || len(got["shards"].([]any)) != 0 {
		t.Fatalf("favor %v", got)
	}
	if len(asked) != 1 || asked[0] != [2]int{1, 3} {
		t.Fatalf("favor roll %v", asked)
	}
	// The mystery spot's timer is its class's (small random xp: 5 minutes).
	if (*timers)[0].d != 5*time.Minute {
		t.Fatalf("mystery respawn %v", (*timers)[0].d)
	}
}

func TestStatusIsCleanedAndClearedOffline(t *testing.T) {
	hub, mod, _ := quoinHub(t, testQuoinData())
	ada := joinAt(hub, 1, "Ada", "GQTEST", 0, 0)
	for _, v := range []any{"x", -4, 1e9, nil} {
		send(hub, ada, map[string]any{"t": "quoin_status", "mult": v})
	}
	if m := mod.status[1].mult; m != 1 {
		// Four statuses in one second are allowed; the last, nil, means 1.
		t.Fatalf("mult %v", m)
	}
	mod.status[1].statusWindow = time.Time{}
	send(hub, ada, map[string]any{"t": "quoin_status", "mult": 1e9})
	if m := mod.status[1].mult; m != maxQuoinMult {
		t.Fatalf("capped mult %v", m)
	}
	hub.Disconnect(ada)
	if _, ok := mod.status[1]; ok {
		t.Fatal("status kept after going offline")
	}
}

func TestTheShippedQuoinData(t *testing.T) {
	d, err := parseQuoinData(quoinsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.streets) < 1000 {
		t.Fatalf("only %d streets", len(d.streets))
	}
	// Hechey Track, the fresh multiplayer start: the outdoor eight.
	if n := len(d.streets["GLI2V0UN7RD1T2J"]); n != 8 {
		t.Fatalf("Hechey Track has %d quoins", n)
	}
	for tsid, spots := range d.streets {
		for i, s := range spots {
			c, ok := d.Classes[s.Class]
			if !ok || c.Respawn <= 0 {
				t.Fatalf("%s #%d: class %q", tsid, i, s.Class)
			}
			if _, ok := d.Types[s.Type]; !ok || s.Type == "qurazy" {
				t.Fatalf("%s #%d: type %q", tsid, i, s.Type)
			}
			if math.IsNaN(s.X) || math.IsNaN(s.Y) {
				t.Fatalf("%s #%d: place", tsid, i)
			}
		}
	}
	// items/quoin.js 359-363 and 394-399, as the table carries them.
	if c := d.Classes["small random mood"]; c.Respawn != 420 || c.Floor != 2 || c.Ceil != 6 || c.Benefit != nil {
		t.Fatalf("small random mood %+v", c)
	}
	if c := d.Classes["slow medium-large energy"]; c.Respawn != 1800 || c.Benefit == nil || *c.Benefit != 25 {
		t.Fatalf("slow medium-large energy %+v", c)
	}
}
