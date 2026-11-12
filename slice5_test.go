package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// G335 slice 5: the shared acts that used to believe the client about an item or a tool.

func fakeAsker(h *Hub, l *fakeUseLedger) *useAsker {
	n := 0
	return &useAsker{h: h, ledger: l, spawn: func(fn func()) { fn() }, asking: map[int64]bool{},
		newOp: func() string { n++; return fmt.Sprintf("use-%016x", n) }}
}

func TestFeedingAStreetPiggyTakesTheFoodThroughTheLedger(t *testing.T) {
	r, l := usesRig(t)
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	b := r.player(2, "Bo", defaultStreet, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	ob := r.mod.streets[defaultStreet]["pig:2"]
	ob.hunger = 5

	// Bo claims two Apples the ledger never saw: refused, and the Piggy stays hungry for everyone.
	r.act(b, 1, "pig:2", "feed", map[string]any{"c": "apple", "n": 2, "perks": []string{"sate"}})
	r.advance(0)
	if no := r.only(b, "obj_no"); no["code"] != "no_item" || no["message"] != useTextLacks {
		t.Fatalf("food the ledger lacks answered %v", no)
	}
	if ob.hunger != 5 {
		t.Fatalf("hunger %d", ob.hunger)
	}
	// A Cherry (base cost 1) sates nobody: nothing shared changes, so the ledger is not asked.
	r.now = r.now.Add(time.Second)
	r.act(a, 2, "pig:2", "feed", map[string]any{"c": "cherry", "n": 3})
	if ok := r.only(a, "obj_ok"); ok["use"] != nil || ob.hunger != 5 {
		t.Fatalf("a cheap food answered %v, hunger %d", ok, ob.hunger)
	}
	// Ada holds three Apples: two leave her ledger, then the Piggy is sated for everyone.
	l.give(1, "apple", 3)
	r.now = r.now.Add(time.Second)
	r.drain(b)
	r.act(a, 3, "pig:2", "feed", map[string]any{"c": "apple", "n": 2})
	r.advance(0)
	ok := r.only(a, "obj_ok")
	use, _ := ok["use"].(map[string]any)
	if use == nil || use["c"] != "apple" || num(use["n"]) != 2 || use["spent"] != true || num(objOf(ok, "pig:2")["hunger"]) != 0 {
		t.Fatalf("the feed answered %v", ok)
	}
	if got := objOf(r.only(b, "objs"), "pig:2"); got == nil || num(got["hunger"]) != 0 {
		t.Fatalf("Bo heard %v", got)
	}
	if l.held[1]["apple"] != 1 {
		t.Fatalf("the ledger kept %d apples", l.held[1]["apple"])
	}
	// Fed when full: nothing shared changes, so nothing is asked; a non-food or too many is refused.
	calls := len(l.calls)
	r.now = r.now.Add(time.Second)
	r.act(a, 4, "pig:2", "feed", map[string]any{"c": "apple", "n": 1})
	r.only(a, "obj_ok")
	r.now = r.now.Add(time.Second)
	r.act(a, 5, "pig:2", "feed", map[string]any{"c": "pick", "n": 1})
	if no := r.only(a, "obj_no"); no["code"] != "not_food" {
		t.Fatalf("a pick as food answered %v", no)
	}
	r.now = r.now.Add(time.Second)
	r.act(a, 6, "pig:2", "feed", map[string]any{"c": "apple", "n": 101})
	if no := r.only(a, "obj_no"); no["code"] != "not_food" {
		t.Fatalf("more than a stack answered %v", no)
	}
	if len(l.calls) != calls {
		t.Fatalf("asked the ledger: %v", l.calls[calls:])
	}
}

func TestAStreetPiggyNoLongerEatsFoodOnlyOnePlayerSees(t *testing.T) {
	r := newObjRig(t)
	pig := sharedWorld.streets[defaultStreet]["pig:2"]
	a := r.player(1, "Ada", defaultStreet, pig.x, pig.y)
	r.send(a, map[string]any{"t": "obj_sync"})
	r.drain(a)
	ob := r.mod.streets[defaultStreet]["pig:2"]
	ob.hunger = 4
	r.act(a, 1, "pig:2", "eat", nil)
	if no := r.only(a, "obj_no"); no["code"] != "unseen" || ob.hunger != 4 {
		t.Fatalf("an unseen stack answered %v, hunger %d", no, ob.hunger)
	}
}

func TestToolGatedTakesNeedAToolTheLedgerHolds(t *testing.T) {
	r, l := usesRig(t)
	rock := sharedWorld.streets[defaultStreet]["rock:0"]
	if rock == nil {
		t.Skip("no rock:0 on the test street")
	}
	a := r.player(1, "Ada", defaultStreet, rock.x, rock.y)
	b := r.player(2, "Bo", defaultStreet, rock.x+20, rock.y)
	r.drain(b)

	// Bo's page claims a pick the ledger never saw: the source's line, and the rock is untouched.
	// A Metal Rock asks for a Fancy Pick (rock_metal_1.js conditions 198-202).
	want, pick := "You'll need a pick first.", "pick"
	if rock.class == "rock_metal_1" {
		want, pick = "You'll need a Fancy Pick first.", "fancy_pick"
	}
	r.act(b, 1, "rock:0", "mine", nil)
	r.advance(0)
	if no := r.only(b, "obj_no"); no["code"] != "no_tool" || no["message"] != want {
		t.Fatalf("a swing without a pick answered %v", no)
	}
	if r.mod.streets[defaultStreet]["rock:0"] != nil {
		t.Fatal("the rock changed")
	}
	// Asked again within toolNoFresh: refused without a call; after it, asked again.
	r.now = r.now.Add(time.Second)
	r.act(b, 2, "rock:0", "mine", nil)
	r.only(b, "obj_no")
	if n := strings.Count(strings.Join(l.calls, ","), "has 2"); n != 1 {
		t.Fatalf("has calls for Bo: %d", n)
	}
	l.give(2, pick, 1)
	r.now = r.now.Add(toolNoFresh)
	r.act(b, 3, "rock:0", "mine", nil)
	r.advance(0)
	r.only(b, "obj_ok")

	// Ada holds a Fancy Pick: one call, then every swing on this street needs none.
	l.give(1, "fancy_pick", 1)
	for i := 0; i < 3; i++ {
		r.now = r.now.Add(time.Second)
		r.act(a, 10+i, "rock:0", "mine", nil)
		r.advance(0)
		for _, f := range r.drain(a) {
			if f["t"] == "obj_no" && f["code"] != "empty" {
				t.Fatalf("swing %d answered %v", i, f)
			}
		}
	}
	if n := strings.Count(strings.Join(l.calls, ","), "has 1"); n != 1 {
		t.Fatalf("has calls for Ada: %d (%v)", n, l.calls)
	}
	// A Wood Tree needs a hatchet, which the same answer already said she lacks.
	if wood := sharedWorld.streets[defaultStreet]; wood != nil {
		for id, spot := range wood {
			if spot.kind != "wood" {
				continue
			}
			r.send(a, map[string]any{"t": "move", "x": spot.x, "y": spot.y})
			r.drain(a)
			r.now = r.now.Add(time.Second)
			r.act(a, 20, id, "harvest", nil)
			r.advance(0)
			if no := r.only(a, "obj_no"); no["code"] != "no_tool" || !strings.Contains(no["message"].(string), "A hatchet, maybe?") {
				t.Fatalf("a Wood Tree without a hatchet answered %v", no)
			}
			break
		}
	}
	// The site gives no answer: the swing lands (uncredited) and the next one asks again.
	l.down = true
	c := r.player(3, "Cy", defaultStreet, rock.x, rock.y)
	r.mod.streets[defaultStreet]["rock:0"].chunks = 50
	r.act(c, 1, "rock:0", "mine", nil)
	r.advance(0)
	r.only(c, "obj_ok")
	if r.mod.uses.tools[3] != nil {
		t.Fatal("no answer is not kept")
	}
}

func TestARookBalmIsUsedThroughTheLedgerFirst(t *testing.T) {
	r := newRookRig(t, true)
	l := newFakeUseLedger()
	r.mod.uses = fakeAsker(r.hub, l)
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{"npc_piggy:0", "npc_piggy:1"}})
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.roll = 0
	r.send(a, map[string]any{"t": "rook_dev", "do": "wingbeat"})
	r.drain(a)
	r.drain(b)
	// Bo's page claims a balm the ledger never saw: refused, still rooked for everyone.
	r.send(b, map[string]any{"t": "rook_revive", "key": "npc_piggy:0", "balm": true})
	r.advance(0)
	fb := r.drain(b)
	if code := rookLastNo(fb); code != "no_item" {
		t.Fatalf("a balm without one: %q %v", code, fb)
	}
	if r.mod.streets[rookTestStreet].rooked["npc_piggy:0"] == nil {
		t.Fatal("revived without a balm")
	}
	// Ada holds one: a charge used (4 left), then the full revive for everyone.
	l.give(1, useBalm, 1)
	r.send(a, map[string]any{"t": "rook_revive", "key": "npc_piggy:0", "balm": true})
	r.advance(0)
	y := rookYous(r.drain(a))["revive"]
	use, _ := y["use"].(map[string]any)
	if y["done"] != true || use == nil || use["c"] != useBalm || rnum(use["left"]) != 4 {
		t.Fatalf("the balm answered %v", y)
	}
	if r.mod.streets[rookTestStreet].rooked["npc_piggy:0"] != nil {
		t.Fatal("still rooked")
	}
	// A dose needs no item and never asks.
	calls := len(l.calls)
	r.send(b, map[string]any{"t": "rook_revive", "key": "npc_piggy:1"})
	if y := rookYous(r.drain(b))["revive"]; rnum(y["doses"]) != 1 || len(l.calls) != calls {
		t.Fatalf("a dose: %v, calls %v", y, l.calls)
	}
}

func TestPowderSpreadsOnlyFromAJarTheLedgerHolds(t *testing.T) {
	r := newHiRig(t, "")
	l := newFakeUseLedger()
	r.mod.uses = fakeAsker(r.hub, l)
	ada := r.join(1, "Ada", 0, 1)
	bo := r.join(2, "Bo", 500, 1)
	frames(bo)
	send(r.hub, ada, map[string]any{"t": "powder_spread", "cls": "sparkle_powder"})
	r.advance(0)
	if got := lastOf(t, ada, "powder_spread"); got["ok"] != false || got["why"] != "no_item" || got["message"] != useTextLacks {
		t.Fatalf("a jar the ledger lacks: %v", got)
	}
	if len(kinds(frames(bo), "powder_caught")) != 0 {
		t.Fatal("Bo caught it anyway")
	}
	l.give(1, useSparkle, 1)
	r.now = r.now.Add(3 * time.Second)
	send(r.hub, ada, map[string]any{"t": "powder_spread", "cls": "sparkle_powder"})
	r.advance(0)
	got := lastOf(t, ada, "powder_spread")
	use, _ := got["use"].(map[string]any)
	if got["ok"] != true || use == nil || use["c"] != useSparkle || use["left"].(float64) != 4 {
		t.Fatalf("the jar answered %v", got)
	}
	if c := lastOf(t, bo, "powder_caught"); c["cls"] != "sparkle_powder" {
		t.Fatalf("Bo caught %v", c)
	}
}

// fakeVoidSite stands in for the site's use and use/void routes for the journal.
type fakeVoidSite struct {
	mu     sync.Mutex
	down   int // calls that get no answer first
	routes []string
}

func (f *fakeVoidSite) call(route string, payload any) ledgerAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = append(f.routes, route)
	if f.down > 0 {
		f.down--
		return ledgerAnswer{}
	}
	return ledgerAnswer{Status: "void"}
}

func TestALostUseIsGivenBackAndSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uses.json")
	site := &fakeVoidSite{down: 2}
	v := &useVoids{path: path, ops: map[string]int64{}, call: site.call, pause: func(time.Duration) {}}
	v.asking("use-00000000000000aa", 7)
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "use-00000000000000aa") {
		t.Fatalf("not journalled before the call: %s", b)
	}
	v.asking("use-00000000000000bb", 8)
	v.answered("use-00000000000000bb")
	// No answer: given back, again until the site answers (two misses, then void).
	v.giveBack("use-00000000000000aa", 7)
	if len(site.routes) != 3 || site.routes[2] != ledgerRouteUseVoid {
		t.Fatalf("void calls %v", site.routes)
	}
	if b, _ := os.ReadFile(path); string(b) != "{}" {
		t.Fatalf("the journal kept %s", b)
	}
	// A restart with an op the site never answered gives it back (a bad line is left out).
	os.WriteFile(path, []byte(`{"use-00000000000000cc":9,"bad":1}`), 0o600)
	site2 := &fakeVoidSite{}
	w := openUseVoids(path, site2.call, func(time.Duration) {})
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.mu.Lock()
		n := len(w.ops)
		w.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still waiting: %v", w.ops)
		}
		time.Sleep(5 * time.Millisecond)
	}
	site2.mu.Lock()
	defer site2.mu.Unlock()
	if len(site2.routes) != 1 || site2.routes[0] != ledgerRouteUseVoid {
		t.Fatalf("after the restart: %v", site2.routes)
	}
}

func TestOneUseProofsStaySpentAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proofs.jsonl")
	now := int64(1_800_000_000)
	p := newProofJournal(path, func() time.Time { return time.Unix(now, 0) })
	if !p.spend("ticket", "j1", now+60, now) || p.spend("ticket", "j1", now+60, now) {
		t.Fatal("a ticket is good once")
	}
	if !p.spend("buddy_add", "j1", now+120, now) {
		t.Fatal("kinds are apart")
	}
	p.spend("home_key", "old", now+5, now)
	// The restart: the same proofs are still spent; one that expired meanwhile is forgotten.
	q := newProofJournal(path, func() time.Time { return time.Unix(now+10, 0) })
	if q.spend("ticket", "j1", now+60, now+10) || q.spend("buddy_add", "j1", now+120, now+10) {
		t.Fatal("a restart forgot a spent proof")
	}
	if len(q.spent) != 2 {
		t.Fatalf("kept %v", q.spent)
	}
	// Tickets use the journal: a ticket spent before the restart is refused after it.
	tk := NewTickets([]byte(strings.Repeat("s", 32)), func() time.Time { return time.Unix(now+10, 0) })
	tk.used = q
	ticket := signTicket(t, Claims{Sub: 5, Name: "Ada", Jti: "j1", Exp: now + 60}, tk.secret)
	if _, err := tk.Check(ticket); err != errTicketReused {
		t.Fatalf("a replayed ticket after a restart: %v", err)
	}
	// Compaction keeps only the live ones.
	for i := 0; i < proofsCompactAt; i++ {
		q.spend("ticket", fmt.Sprintf("n%d", i), now+11, now+10)
	}
	r := newProofJournal(path, func() time.Time { return time.Unix(now+12, 0) })
	if len(r.spent) != 2 {
		t.Fatalf("after compaction and expiry: %d live", len(r.spent))
	}
}

func TestTheLedgerIsAskedForWholeItemsAndToolsOverHTTP(t *testing.T) {
	secret := []byte(strings.Repeat("k", 32))
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		mu.Lock()
		bodies = append(bodies, req.URL.Path+" "+string(b))
		mu.Unlock()
		switch strings.TrimPrefix(req.URL.Path, "/wp-json") {
		case ledgerRouteHas:
			w.Write([]byte(`{"status":"settled","has":["pick","watering_can"]}`))
		case ledgerRouteUse:
			w.Write([]byte(`{"status":"settled","left":0,"spent":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	l := &httpLedger{base: srv.URL, secret: secret, client: srv.Client(), now: time.Now, pause: func(time.Duration) {}}
	held, ok := l.has(7, toolClasses)
	if !ok || !held["pick"] || !held["watering_can"] || held["hatchet"] {
		t.Fatalf("has: %v %v", held, ok)
	}
	theUseVoidsOnce.Do(func() { theUseVoids = &useVoids{ops: map[string]int64{}, call: l.call, pause: func(time.Duration) {}} })
	if a := l.use("use-0000000000000001", 7, "apple", 2); a.Status != "settled" || !a.Spent {
		t.Fatalf("use: %v", a)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !strings.Contains(bodies[1], `"n":2`) || !strings.Contains(bodies[0], `"c":["pick","fancy_pick"`) {
		t.Fatalf("bodies %v", bodies)
	}
}

// Each Rook Balm keeps its own pours (2026-11-04): the tube poured goes to the ledger as k.
func TestARookBalmNamesTheTubePouredToTheLedger(t *testing.T) {
	r := newRookRig(t, true)
	l := newFakeUseLedger()
	r.mod.uses = fakeAsker(r.hub, l)
	a := r.player(1, "Ada")
	r.send(a, map[string]any{"t": "rook_sync", "keys": []string{"npc_piggy:0", "npc_piggy:1", "npc_piggy:2"}})
	r.send(a, map[string]any{"t": "rook_dev", "do": "attack"})
	r.roll = 0
	r.send(a, map[string]any{"t": "rook_dev", "do": "wingbeat"})
	r.drain(a)
	l.give(1, useBalm, 2)
	r.send(a, map[string]any{"t": "rook_revive", "key": "npc_piggy:0", "balm": true, "k": 1})
	r.advance(0)
	if y := rookYous(r.drain(a))["revive"]; y["done"] != true {
		t.Fatalf("the second tube's pour answered %v", y)
	}
	// None, or one out of range, names no tube: the one in use.
	r.send(a, map[string]any{"t": "rook_revive", "key": "npc_piggy:1", "balm": true, "k": 999})
	r.advance(0)
	r.drain(a)
	if len(l.copies) != 1 || l.copies[0] != useBalm+" #1" {
		t.Fatalf("the tubes named were %v (want only the first pour's tube 1)", l.copies)
	}
}
