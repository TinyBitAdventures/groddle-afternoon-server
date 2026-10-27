package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCreditSite answers credit batches from a script, and records them.
type fakeCreditSite struct {
	mu      sync.Mutex
	batches [][]creditEvent
	answer  func(n int, batch []creditEvent) ([]string, error) // n: 1 for the first call
}

func (f *fakeCreditSite) credits(batch []creditEvent) ([]string, error) {
	f.mu.Lock()
	f.batches = append(f.batches, append([]creditEvent(nil), batch...))
	n := len(f.batches)
	f.mu.Unlock()
	return f.answer(n, batch)
}

func allDone(_ int, batch []creditEvent) ([]string, error) {
	ids := make([]string, len(batch))
	for i, ev := range batch {
		ids[i] = ev.ID
	}
	return ids, nil
}

// caughtCredits stands in for the credits module on a test hub: what each hook credited.
func caughtCredits(h *Hub) *[]creditEvent {
	got := &[]creditEvent{}
	h.provided["credits.add"] = func(user int64, why string, g tradeGoods) string {
		*got = append(*got, creditEvent{User: user, Why: why, Got: g})
		return "cr-test"
	}
	return got
}

// sameCredit compares who, why and what (ids and times aside).
func sameCredit(a, b creditEvent) bool {
	if a.User != b.User || a.Why != b.Why || a.Got.Currants != b.Got.Currants || len(a.Got.Items) != len(b.Got.Items) {
		return false
	}
	for i := range a.Got.Items {
		if a.Got.Items[i].C != b.Got.Items[i].C || a.Got.Items[i].N != b.Got.Items[i].N {
			return false
		}
	}
	return true
}

func event(n int) creditEvent {
	return creditEvent{ID: "cr-q-test-" + string(rune('a'+n%26)) + strings.Repeat("x", n/26), User: 7, Why: "quoin", Got: tradeGoods{Currants: int64(n + 1)}}
}

func TestCreditsGoInBatchesAndLeaveOnlyWhenDone(t *testing.T) {
	site := &fakeCreditSite{answer: allDone}
	o := newCreditOutbox(site)
	for i := 0; i < 120; i++ {
		o.add(event(i))
	}
	for want := 70; want >= 0; want -= 50 {
		left, err := o.flushOnce()
		if err != nil || left != max(want, 0) {
			t.Fatalf("after a batch: %d left, %v (want %d)", left, err, max(want, 0))
		}
	}
	if left, _ := o.flushOnce(); left != 0 || len(site.batches) != 3 {
		t.Fatalf("120 credits go in 3 calls: %d calls, %d left", len(site.batches), left)
	}
	if len(site.batches[0]) != creditBatch || len(site.batches[2]) != 20 || site.batches[0][0].ID != event(0).ID || site.batches[2][19].ID != event(119).ID {
		t.Fatalf("oldest first, %d a call: %d %d", creditBatch, len(site.batches[0]), len(site.batches[2]))
	}
}

func TestACreditTheSiteDidNotTakeIsSentAgainWithTheSameID(t *testing.T) {
	// The site answers for the first two only: the third stays and goes again, unchanged.
	site := &fakeCreditSite{answer: func(n int, batch []creditEvent) ([]string, error) {
		if n == 1 {
			return []string{batch[0].ID, batch[1].ID}, nil
		}
		return allDone(n, batch)
	}}
	o := newCreditOutbox(site)
	for i := 0; i < 3; i++ {
		o.add(event(i))
	}
	if left, err := o.flushOnce(); left != 1 || err != nil {
		t.Fatalf("one left after a partial answer: %d %v", left, err)
	}
	if left, err := o.flushOnce(); left != 0 || err != nil {
		t.Fatalf("then none: %d %v", left, err)
	}
	if again := site.batches[1]; len(again) != 1 || again[0].ID != site.batches[0][2].ID || !sameCredit(again[0], site.batches[0][2]) {
		t.Fatalf("the resend is the same event, id and all: %+v", again)
	}
}

func TestCreditsWaitOutASiteOutage(t *testing.T) {
	// At least once: no answer twice (down, then a 5xx), then the site takes the lot.
	site := &fakeCreditSite{answer: func(n int, batch []creditEvent) ([]string, error) {
		if n <= 2 {
			return nil, errors.New("HTTP 503")
		}
		return allDone(n, batch)
	}}
	o := newCreditOutbox(site)
	var mu sync.Mutex
	var slept []time.Duration
	o.sleep = func(d time.Duration) {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
	}
	go func() {
		o.run()
	}()
	for i := 0; i < 5; i++ {
		o.add(event(i))
	}
	deadline := time.Now().Add(5 * time.Second)
	for o.pending() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("still %d waiting after the outage", o.pending())
		}
		time.Sleep(5 * time.Millisecond)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	if len(site.batches) != 3 {
		t.Fatalf("two failed calls and one good one: %d", len(site.batches))
	}
	for _, b := range site.batches {
		if len(b) != 5 || b[0].ID != event(0).ID {
			t.Fatalf("every try carries the same five: %+v", b)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(slept) < 3 || slept[0] != creditGather || slept[1] != creditBackoffMin || slept[2] != 2*creditBackoffMin {
		t.Fatalf("gather, then a backoff that doubles: %v", slept)
	}
}

func TestAFullOutboxDropsTheOldest(t *testing.T) {
	o := newCreditOutbox(&fakeCreditSite{answer: allDone})
	for i := 0; i < creditQueueMax+3; i++ {
		o.add(creditEvent{ID: "cr-q-x-" + string(rune('a'+i%26)), User: int64(i + 1)})
	}
	if o.pending() != creditQueueMax || o.dropped != 3 || o.queue[0].User != 4 {
		t.Fatalf("kept %d, dropped %d, oldest user %d", o.pending(), o.dropped, o.queue[0].User)
	}
}

func TestHTTPCreditsAreSignedAndReadTheDoneList(t *testing.T) {
	secret := []byte("credits-secret")
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		body, _ := io.ReadAll(req.Body)
		parts := strings.SplitN(req.Header.Get("X-Groddle-Service"), ".", 3)
		var at int64
		json.Unmarshal([]byte(parts[0]), &at)
		route := strings.TrimPrefix(req.URL.Path, "/wp-json")
		if route != ledgerRouteCredits || len(parts) != 3 || signService(secret, "POST", route, at, parts[1], body) != req.Header.Get("X-Groddle-Service") {
			w.WriteHeader(401)
			return
		}
		if calls == 1 {
			w.WriteHeader(502)
			return
		}
		var in struct {
			Credits []creditEvent `json:"credits"`
		}
		json.Unmarshal(body, &in)
		ids := []string{}
		for _, c := range in.Credits {
			if c.Got.Items == nil {
				w.WriteHeader(400) // items is always a list
				return
			}
			ids = append(ids, c.ID)
		}
		json.NewEncoder(w).Encode(map[string]any{"done": ids})
	}))
	defer srv.Close()
	l := &httpLedger{base: srv.URL, secret: secret, client: srv.Client(), now: time.Now, pause: func(time.Duration) {}}
	batch := []creditEvent{{ID: "cr-r-a-1", User: 7, Why: "race", Got: tradeGoods{Items: []tradeItem{}, Currants: 100}}}
	if _, err := l.credits(batch); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("a 502 is no answer: %v", err)
	}
	done, err := l.credits(batch)
	if err != nil || len(done) != 1 || done[0] != "cr-r-a-1" {
		t.Fatalf("signed and taken: %v %v", done, err)
	}
}

func TestTheModuleMakesOneIDPerEventAndSkipsNothingOfValue(t *testing.T) {
	h := NewHub(nil)
	m := &credits{h: h, boot: "0a1b2c3d", out: newCreditOutbox(&fakeCreditSite{answer: allDone})}
	a := m.add(7, "quoin", tradeGoods{Currants: 3})
	b := m.add(7, "take", creditItem("beryl", 9))
	if a != "cr-q-0a1b2c3d-1" || b != "cr-t-0a1b2c3d-2" || len(a) > 40 {
		t.Fatalf("ids %q %q", a, b)
	}
	if m.add(7, "quoin", tradeGoods{}) != "" || m.add(0, "race", tradeGoods{Currants: 100}) != "" || m.add(7, "gift", tradeGoods{Currants: 1}) != "" || m.add(7, "take", creditItem("beryl", 0)) != "" {
		t.Fatal("nothing, nobody or an unknown why sends nothing")
	}
	if m.out.pending() != 2 || !sameCredit(m.out.queue[1], creditEvent{User: 7, Why: "take", Got: creditItem("beryl", 9)}) || m.out.queue[0].Got.Items == nil {
		t.Fatalf("queued %+v", m.out.queue)
	}
	off := &credits{h: h, boot: "x"}
	if off.add(7, "race", tradeGoods{Currants: 100}) != "" {
		t.Fatal("without the site's ledger nothing is queued")
	}
}

func TestACurrantsQuoinCreditsTheGrabAndTheShards(t *testing.T) {
	hub, mod, _ := quoinHub(t, testQuoinData())
	got := caughtCredits(hub)
	ada := joinAt(hub, 1, "Ada", "GQTEST", 4000, touchY)
	bo := joinAt(hub, 2, "Bo", "GQTEST", 4010, touchY)
	send(hub, ada, map[string]any{"t": "quoin_status", "mult": 3.4})
	send(hub, bo, map[string]any{"t": "quoin_status", "mult": 2})
	frames(ada)
	frames(bo)
	// fast tiny currants: benefit 1, x3.4 rounds to 3; Bo 10 px off gets half of 1 x 2 = 1.
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 3})
	if g := lastOf(t, ada, "quoin_got"); g["amount"].(float64) != 1 {
		t.Fatalf("got %v", g)
	}
	if len(*got) != 2 || !sameCredit((*got)[0], creditEvent{User: 1, Why: "quoin", Got: tradeGoods{Currants: 3}}) || !sameCredit((*got)[1], creditEvent{User: 2, Why: "shard", Got: tradeGoods{Currants: 1}}) {
		t.Fatalf("credits %+v", *got)
	}
	// An xp quoin is not the ledger's.
	*got = nil
	send(hub, ada, map[string]any{"t": "move", "x": 1000, "y": touchY})
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 0})
	lastOf(t, ada, "quoin_got")
	if len(*got) != 0 {
		t.Fatalf("an xp quoin credits nothing: %+v", *got)
	}
	// Past quoinCreditDaily grabs in a game day the grab is not credited (its shards still are).
	mod.grabs[1] = quoinDay{day: mod.grabs[1].day, n: quoinCreditDaily}
	mod.gone = map[string]map[int]func() bool{}
	send(hub, ada, map[string]any{"t": "move", "x": 4000, "y": touchY})
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 3})
	lastOf(t, ada, "quoin_got")
	if len(*got) != 1 || (*got)[0].Why != "shard" {
		t.Fatalf("the 151st grab of the day: %+v", *got)
	}
	// A new game day counts from zero.
	*got = nil
	mod.grabs[1] = quoinDay{day: mod.grabs[1].day - 1, n: 500}
	mod.gone = map[string]map[int]func() bool{}
	send(hub, ada, map[string]any{"t": "quoin_claim", "street": "GQTEST", "i": 3})
	if len(*got) != 2 || (*got)[0].Why != "quoin" || mod.grabs[1].n != 1 {
		t.Fatalf("a new day: %+v %+v", *got, mod.grabs[1])
	}
}

func TestARaceWinCreditsTheWinnerOnly(t *testing.T) {
	r := newRaceRig(t)
	got := caughtCredits(r.hub)
	ada, bo, match := r.pair("canyon_run")
	id, _ := r.run(ada, bo, match)
	r.move(bo, 905, 400)
	r.do(bo, map[string]any{"t": "race_finish", "match": id})
	if len(*got) != 1 || !sameCredit((*got)[0], creditEvent{User: 2, Why: "race", Got: tradeGoods{Currants: raceWinCurrants}}) {
		t.Fatalf("the winner's 100 currants: %+v", *got)
	}
	// A game called off pays nobody.
	r2 := newRaceRig(t)
	got2 := caughtCredits(r2.hub)
	a2, b2, m2 := r2.pair("grab_em_good")
	id2, _ := r2.run(a2, b2, m2)
	r2.do(a2, map[string]any{"t": "race_leave", "match": id2})
	if len(*got2) != 0 {
		t.Fatalf("called off: %+v", *got2)
	}
}

func TestSharedTakesAreCreditedAsItemCounts(t *testing.T) {
	r := newObjRig(t)
	got := caughtCredits(r.hub)
	spot := sharedWorld.streets[defaultStreet]["rock:3"] // rock_sparkly_1
	a := r.player(1, "Ada", defaultStreet, spot.x, spot.y)
	r.mod.roll = func() float64 { return 0.25 }
	// randInt(3, 7) at 0.25 is 4, and Sparkly Expert's round(0.6) = 1 more: 5 chunks of Sparkly.
	r.act(a, 1, "rock:3", "mine", map[string]any{"perks": []string{"expert"}})
	r.only(a, "obj_ok")
	if len(*got) != 1 || (*got)[0].User != 1 || (*got)[0].Why != "take" || !sameCredit((*got)[0], creditEvent{User: 1, Why: "take", Got: creditItem("sparkly", 5)}) {
		t.Fatalf("a rock's take: %+v", *got)
	}
	// A tree's fruit: Gardening III takes 12 cherries.
	*got = nil
	tree := sharedWorld.streets[defaultStreet]["tree:0"]
	r.send(a, map[string]any{"t": "move", "x": tree.x, "y": tree.y})
	r.act(a, 2, "tree:0", "harvest", map[string]any{"skill": "gardening_3"})
	r.only(a, "obj_ok")
	if len(*got) != 1 || !sameCredit((*got)[0], creditEvent{User: 1, Why: "take", Got: creditItem("cherry", 12)}) {
		t.Fatalf("a tree's take: %+v", *got)
	}
	// A refusal and a pet credit nothing.
	*got = nil
	r.act(a, 3, "tree:0", "pet", nil)
	r.drain(a)
	if len(*got) != 0 {
		t.Fatalf("a pet: %+v", *got)
	}
}

func TestNodeAndWoodTakesAreCredited(t *testing.T) {
	r := newObjRig(t)
	got := caughtCredits(r.hub)
	w := *sharedWorld
	w.streets = map[string]map[string]*objSpot{"NODES": {
		"node:peat_1:0":     {id: "node:peat_1:0", kind: "node", class: "peat_1", x: 100, y: 500},
		"node:paper_tree:0": {id: "node:paper_tree:0", kind: "node", class: "paper_tree", x: 300, y: 500},
		"tree:4":            {id: "tree:4", kind: "wood", class: "wood_tree", x: 200, y: 500, maturity: 4},
	}}
	r.mod.w = &w
	a := r.player(1, "Ada", "NODES", 200, 500)
	r.act(a, 1, "node:peat_1:0", "harvest", nil)
	r.act(a, 2, "node:paper_tree:0", "harvest", map[string]any{"perks": []string{"paper8"}})
	r.act(a, 3, "tree:4", "harvest", nil)
	r.drain(a)
	want := []tradeGoods{creditItem("peat", 3), creditItem("paper", 8), creditItem("plank", 50)}
	if len(*got) != 3 {
		t.Fatalf("three takes: %+v", *got)
	}
	for i, ev := range *got {
		if !sameCredit(ev, creditEvent{User: 1, Why: "take", Got: want[i]}) {
			t.Fatalf("take %d: %+v, want %+v", i, ev, want[i])
		}
	}
}
