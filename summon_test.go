package main

import (
	"fmt"
	"testing"
	"time"
)

// summonRig is a hub with the summons, the parties (with Party Spaces), following and a block list.
type summonRig struct {
	*spaceRig
	s   *summons
	f   *follows
	ign *fakeIgnores
}

func newSummonRig(t *testing.T) *summonRig {
	sp := &spaceRig{partyRig: &partyRig{t: t, now: time.Unix(1_700_000_000, 0)}}
	r := &summonRig{spaceRig: sp, ign: &fakeIgnores{ign: map[[2]int64]bool{}}}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { sp.m = newParties(h).(*parties); return sp.m },
		func(h *Hub) Module { r.s = newSummons(h).(*summons); return r.s },
		func(h *Hub) Module { r.f = newFollows(h).(*follows); return r.f },
		func(h *Hub) Module { return r.ign },
	}
	sp.hub = NewHub(func() time.Time { return sp.now })
	modules = saved
	sp.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		sp.timers = append(sp.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	sp.ledger = &fakeSpends{packs: map[int64]map[string]int{}, currants: map[int64]int64{}}
	sp.m.spaces.ledger = sp.ledger
	sp.m.spaces.spawn = func(fn func()) { fn() }
	n := 0
	sp.m.spaces.newID = func() string { n++; return fmt.Sprintf("%016x", n) }
	return r
}

// at puts cl on street at x, y.
func (r *summonRig) at(cl *Client, street string, x, y float64) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": x, "y": y})
}

// tick moves the clock past the request rate.
func (r *summonRig) tick() { r.now = r.now.Add(10 * time.Second) }

func summonOne(t *testing.T, msgs []map[string]any, kind string) map[string]any {
	t.Helper()
	got := kinds(msgs, kind)
	if len(got) != 1 {
		t.Fatalf("want one %s, got %d in %v", kind, len(got), msgs)
	}
	return got[0]
}

func TestSummonYesPaysThenMovesThemBesideYou(t *testing.T) {
	r := newSummonRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.at(ada, "GSTREET1", 400, -120)
	r.at(bo, "GSTREET2", 50, -60)
	drain(ada)
	drain(bo)

	r.do(ada, map[string]any{"t": "summon", "id": 2})
	if w := summonOne(t, drain(ada), "summon_waiting"); w["name"] != "Bo" || w["timeout"] != float64(30) {
		t.Fatalf("waiting %v", w)
	}
	asked := summonOne(t, drain(bo), "summon_asked")
	if asked["street"] != "GSTREET1" || asked["from"].(map[string]any)["name"] != "Ada" || asked["party"] != nil {
		t.Fatalf("asked %v", asked)
	}
	r.do(bo, map[string]any{"t": "summon_answer", "from": 1, "accept": true})
	if p := summonOne(t, drain(ada), "summon_pay"); p["id"] != float64(2) || p["name"] != "Bo" {
		t.Fatalf("pay %v", p)
	}
	if len(drain(bo)) != 0 {
		t.Fatal("Bo moved before Ada paid")
	}
	// The ask timer was stopped: it cannot decline a summons being paid for.
	r.fire(summonAskWait + summonAskGrace)
	if len(drain(ada)) != 0 {
		t.Fatal("a stale ask timer spoke")
	}
	r.do(ada, map[string]any{"t": "summon_paid", "id": 2, "ok": true})
	g := summonOne(t, drain(bo), "summon_go")
	if g["street"] != "GSTREET1" || g["x"] != float64(420) || g["y"] != float64(-120) || g["party"] != false {
		t.Fatalf("go %v", g)
	}
	if r.s.by[1] != nil || len(r.s.held[2]) != 0 {
		t.Fatal("the summons stayed open")
	}
	// Free again: a second summons may go out.
	r.tick()
	r.do(ada, map[string]any{"t": "summon", "id": 2})
	summonOne(t, drain(ada), "summon_waiting")
}

func TestSummonNoAndTimeoutTellTheSummoner(t *testing.T) {
	r := newSummonRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.at(ada, "GSTREET1", 0, 0)
	r.at(bo, "GSTREET2", 0, 0)
	drain(ada)
	drain(bo)

	r.do(ada, map[string]any{"t": "summon", "id": 2})
	drain(bo)
	drain(ada)
	r.do(bo, map[string]any{"t": "summon_answer", "from": 1, "accept": false})
	if d := summonOne(t, drain(ada), "summon_declined"); d["text"] != "Bo did not accept your summons." {
		t.Fatalf("declined %v", d)
	}

	r.tick()
	r.do(ada, map[string]any{"t": "summon", "id": 2})
	drain(ada)
	drain(bo)
	r.fire(summonAskWait + summonAskGrace)
	summonOne(t, drain(ada), "summon_declined")
	summonOne(t, drain(bo), "summon_gone")
	// A late answer to a summons that is over.
	r.do(bo, map[string]any{"t": "summon_answer", "from": 1, "accept": true})
	summonOne(t, drain(bo), "summon_gone")
	if len(drain(ada)) != 0 {
		t.Fatal("a late yes reached Ada")
	}
}

func TestSummonRefusals(t *testing.T) {
	r := newSummonRig(t)
	ada, bo, cy := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	r.at(ada, "GSTREET1", 0, 0)
	r.at(bo, "GSTREET2", 0, 0)
	r.at(cy, "GSTREET2", 0, 0)
	drain(ada)
	drain(bo)
	drain(cy)
	refused := func(id int64, text string) {
		t.Helper()
		r.tick()
		r.do(ada, map[string]any{"t": "summon", "id": id})
		if m := summonOne(t, drain(ada), "summon_refused"); m["text"] != text {
			t.Fatalf("summon %d: %v, want %q", id, m, text)
		}
		if len(drain(bo)) != 0 || len(drain(cy)) != 0 {
			t.Fatal("a refused summons reached a target")
		}
	}
	refused(1, summonTextSelf)
	refused(9, summonTextOffline)
	r.ign.ign[[2]int64{2, 1}] = true
	refused(2, summonTextBlocked)
	delete(r.ign.ign, [2]int64{2, 1})

	// Where the summoner stands (inc_teleportation.js 776-806).
	// Someone else's house sends the target to its door (TestSummonFromSomeoneElsesHouseToItsDoor).
	for street, text := range map[string]string{"HOUSE_x": summonTextHere, "HOME_x": summonTextHere, "RACE_5": summonTextHere, "TRAIN_3": summonTextHere} {
		r.at(ada, street, 0, 0)
		drain(ada)
		refused(2, text)
	}
	r.do(ada, map[string]any{"t": "leave"})
	drain(ada)
	refused(2, summonTextHere)
	r.at(ada, "GSTREET1", 0, 0)
	drain(ada)

	// A target off every street: in a quest room or a scene (754-759).
	r.do(bo, map[string]any{"t": "leave"})
	drain(cy)
	refused(2, summonTextCant)
	r.at(bo, "GSTREET2", 0, 0)
	drain(bo)
	drain(cy)

	// One at a time (733-738).
	r.tick()
	r.do(ada, map[string]any{"t": "summon", "id": 2})
	drain(ada)
	drain(bo)
	refused(3, summonTextOne)
}

func TestSummonTargetsOwnRefusalsAndTooBusy(t *testing.T) {
	r := newSummonRig(t)
	bo := r.connect(2, "Bo")
	r.at(bo, "GSTREET2", 0, 0)
	var callers []*Client
	for i := int64(10); i < 10+summonMaxHeld+1; i++ {
		cl := r.connect(i, fmt.Sprintf("P%d", i))
		r.at(cl, "GSTREET1", 0, 0)
		drain(cl)
		callers = append(callers, cl)
	}
	drain(bo)
	for _, cl := range callers[:summonMaxHeld] {
		r.do(cl, map[string]any{"t": "summon", "id": 2})
		summonOne(t, drain(cl), "summon_waiting")
	}
	last := callers[summonMaxHeld]
	r.do(last, map[string]any{"t": "summon", "id": 2})
	if m := summonOne(t, drain(last), "summon_refused"); m["text"] != summonTextBusy {
		t.Fatalf("busy %v", m)
	}
	drain(bo)
	// The target's game refuses for its own reasons, without a prompt.
	for i, why := range []string{"dead", "busy", "contraband"} {
		cl := callers[i]
		r.do(bo, map[string]any{"t": "summon_answer", "from": cl.ID(), "accept": false, "why": why})
		if m := summonOne(t, drain(cl), "summon_refused"); m["text"] != summonWhy[why] {
			t.Fatalf("%s: %v", why, m)
		}
	}
}

func TestSummonPaymentFailsOrNeverComes(t *testing.T) {
	r := newSummonRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.at(ada, "GSTREET1", 0, 0)
	r.at(bo, "GSTREET2", 0, 0)
	ask := func() {
		r.tick()
		r.do(ada, map[string]any{"t": "summon", "id": 2})
		r.do(bo, map[string]any{"t": "summon_answer", "from": 1, "accept": true})
		drain(ada)
		drain(bo)
	}
	ask()
	r.do(ada, map[string]any{"t": "summon_paid", "id": 2, "ok": false})
	if m := summonOne(t, drain(bo), "summon_failed"); m["text"] != summonTextOops {
		t.Fatalf("failed %v", m)
	}
	ask()
	r.fire(summonPayWait)
	summonOne(t, drain(bo), "summon_failed")
	if r.s.by[1] != nil {
		t.Fatal("an unpaid summons stayed open")
	}
	// The summoner walked into a race between the Yes and paying: refused, Oops for the target.
	ask()
	r.at(ada, "RACE_1", 0, 0)
	drain(ada)
	r.do(ada, map[string]any{"t": "summon_paid", "id": 2, "ok": true})
	if m := summonOne(t, drain(ada), "summon_refused"); m["text"] != summonTextHere {
		t.Fatalf("refused %v", m)
	}
	summonOne(t, drain(bo), "summon_failed")
}

func TestSummonGoingOfflineEndsIt(t *testing.T) {
	r := newSummonRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.at(ada, "GSTREET1", 0, 0)
	r.at(bo, "GSTREET2", 0, 0)
	r.do(ada, map[string]any{"t": "summon", "id": 2})
	drain(ada)
	drain(bo)
	r.hub.Disconnect(ada)
	summonOne(t, drain(bo), "summon_gone")

	ada = r.connect(1, "Ada")
	r.at(ada, "GSTREET1", 0, 0)
	r.tick()
	r.do(ada, map[string]any{"t": "summon", "id": 2})
	drain(ada)
	r.hub.Disconnect(bo)
	summonOne(t, drain(ada), "summon_declined")
	if r.s.by[1] != nil || len(r.s.held) != 0 {
		t.Fatal("the summons outlived the target")
	}
}

func TestSummonLetsTheTargetsFollowersGo(t *testing.T) {
	r := newSummonRig(t)
	ada, bo, cy := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	r.at(ada, "GSTREET1", 0, 0)
	r.at(bo, "GSTREET2", 0, 0)
	r.at(cy, "GSTREET2", 100, 0)
	r.do(cy, map[string]any{"t": "follow_start", "id": 2})
	r.do(bo, map[string]any{"t": "follow_answer", "id": 3, "accept": true})
	if r.f.leaderOf[3] != 2 {
		t.Fatal("Cy is not following Bo")
	}
	drain(ada)
	drain(bo)
	drain(cy)
	r.do(ada, map[string]any{"t": "summon", "id": 2})
	r.do(bo, map[string]any{"t": "summon_answer", "from": 1, "accept": true})
	r.do(ada, map[string]any{"t": "summon_paid", "id": 2, "ok": true})
	if r.f.leaderOf[3] != 0 {
		t.Fatal("Cy still follows a summoned Bo (inc_teleportation.js:673)")
	}
	summonOne(t, drain(cy), "follow_ended")
	summonOne(t, drain(bo), "summon_go")
}

func TestSummonIntoAPartySpaceJoinsTheParty(t *testing.T) {
	r := newSummonRig(t)
	a, b, c := r.threesome()
	street := r.open(a, "party_pack_toxic_moon")
	r.at(a, street, 300, -100)
	drain(a)
	drain(b)
	drain(c)

	r.do(a, map[string]any{"t": "summon", "id": 3})
	drain(a)
	asked := summonOne(t, drain(c), "summon_asked")
	if asked["party"] != "Toxic Moon" || asked["street"] != street {
		t.Fatalf("asked %v", asked)
	}
	r.do(c, map[string]any{"t": "summon_answer", "from": 1, "accept": true})
	summonOne(t, drain(a), "summon_pay")
	r.do(a, map[string]any{"t": "summon_paid", "id": 3, "ok": true})
	cm := drain(c)
	g := summonOne(t, cm, "summon_go")
	if g["party"] != true || g["street"] != street {
		t.Fatalf("go %v", g)
	}
	if s := spaceOf(t, cm); s["street"] != street {
		t.Fatalf("Cy was not sent the space: %v", cm)
	}
	if len(kinds(cm, "party_space_prompt")) != 0 {
		t.Fatal("a summoned player was offered a teleporter as well")
	}
	if r.m.of[3] == nil || r.m.of[3] != r.m.of[1] {
		t.Fatal("Cy did not join Ada's party")
	}
	// Cy may now stand in the space (the space's gate lets members in).
	r.join(c, street)
	if len(kinds(drain(c), "error")) != 0 {
		t.Fatal("the space refused a summoned member")
	}
}

func TestSummonIntoAPartySpaceRefusalsAndDecline(t *testing.T) {
	r := newSummonRig(t)
	a, b, c := r.threesome()
	d, e := r.connect(4, "Dee"), r.connect(5, "Eve")
	r.join(d, "GSTREET1")
	r.join(e, "GSTREET1")
	r.party(d, e)
	street := r.open(a, "party_pack_toxic_moon")
	r.at(a, street, 0, 0)
	for _, cl := range []*Client{a, b, c, d, e} {
		drain(cl)
	}
	// Dee is in another party (inc_teleportation.js:781).
	r.do(a, map[string]any{"t": "summon", "id": 4})
	if m := summonOne(t, drain(a), "summon_refused"); m["text"] != summonTextOtherParty {
		t.Fatalf("other party %v", m)
	}
	// Cy says no: the summoner hears both lines (715, 719).
	r.tick()
	r.do(a, map[string]any{"t": "summon", "id": 3})
	drain(a)
	drain(c)
	r.do(c, map[string]any{"t": "summon_answer", "from": 1, "accept": false})
	am := drain(a)
	if got := texts(am); got != "Cy has declined your invitation" {
		t.Fatalf("party line %q", got)
	}
	summonOne(t, am, "summon_declined")
	if r.m.of[3] != nil {
		t.Fatal("a No joined the party")
	}
	// Bo, already in the party, is summoned in with no party lines at all.
	r.tick()
	r.do(a, map[string]any{"t": "summon", "id": 2})
	r.do(b, map[string]any{"t": "summon_answer", "from": 1, "accept": true})
	r.do(a, map[string]any{"t": "summon_paid", "id": 2, "ok": true})
	bm := drain(b)
	if g := summonOne(t, bm, "summon_go"); g["party"] != true {
		t.Fatalf("go %v", g)
	}
	if texts(bm) != "" {
		t.Fatalf("Bo heard %q", texts(bm))
	}
}

// newSummonHomesRig is newSummonRig with the homes module (and its test secret) as well.
func newSummonHomesRig(t *testing.T) (*summonRig, *homes) {
	var hm *homes
	sp := &spaceRig{partyRig: &partyRig{t: t, now: time.Unix(1_700_000_000, 0)}}
	r := &summonRig{spaceRig: sp, ign: &fakeIgnores{ign: map[[2]int64]bool{}}}
	savedSecret := homesSecretForTest
	homesSecretForTest = homesTestSecret
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { sp.m = newParties(h).(*parties); return sp.m },
		func(h *Hub) Module { r.s = newSummons(h).(*summons); return r.s },
		func(h *Hub) Module { hm = newHomes(h).(*homes); return hm },
		func(h *Hub) Module { return r.ign },
	}
	sp.hub = NewHub(func() time.Time { return sp.now })
	modules = saved
	homesSecretForTest = savedSecret
	sp.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		sp.timers = append(sp.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r, hm
}

// homePassFor is the site's visit pass (Neighbours.php) for visitor to owner's home.
func (r *summonRig) homePassFor(cl *Client, owner int64, key bool) {
	k := 0
	if key {
		k = 1
	}
	tok := signBuddy(homesTestSecret, map[string]any{"k": "home", "sub": cl.ID(), "o": owner, "name": "Owner", "key": k, "iat": r.now.Unix(), "exp": r.now.Add(30 * time.Minute).Unix()})
	r.do(cl, map[string]any{"t": "home_pass", "token": tok})
	drain(cl)
}

// summonAll runs one summons from ada to bo through to summon_go, returning it.
func (r *summonRig) summonAll(t *testing.T, ada, bo *Client) map[string]any {
	t.Helper()
	r.tick()
	r.do(ada, map[string]any{"t": "summon", "id": bo.ID()})
	summonOne(t, drain(ada), "summon_waiting")
	summonOne(t, drain(bo), "summon_asked")
	r.do(bo, map[string]any{"t": "summon_answer", "from": ada.ID(), "accept": true})
	summonOne(t, drain(ada), "summon_pay")
	r.do(ada, map[string]any{"t": "summon_paid", "id": bo.ID(), "ok": true})
	return summonOne(t, drain(bo), "summon_go")
}

// G668: inc_teleportation.js 799-806 in a home.
func TestSummonIntoHomes(t *testing.T) {
	r, hm := newSummonHomesRig(t)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.at(bo, "GSTREET2", 0, 0)
	drain(bo)

	// The owner summons anyone into their own home street, house or Tower floor.
	for _, street := range []string{"HOME_1", "TOWER_1_2", "HOUSE_1"} {
		r.at(ada, street, 300, -50)
		drain(ada)
		g := r.summonAll(t, ada, bo)
		if g["street"] != street || g["x"] != float64(320) {
			t.Fatalf("%s: go %v", street, g)
		}
	}

	// Into the house: the site's pass as for any visit, then in for that one arrival.
	r.join(bo, "HOUSE_1")
	if got := buddyKinds(drain(bo)); got != "error:home_private" {
		t.Fatalf("no pass, no house: %s", got)
	}
	r.homePassFor(bo, 1, false)
	r.join(bo, "HOUSE_1")
	if f := drain(bo); f[0]["t"] != "street" {
		t.Fatalf("summoned in: %v", f)
	}
	drain(ada)
	r.join(bo, "HOME_1")
	r.join(bo, "HOUSE_1")
	if got := kinds(drain(bo), "error"); len(got) != 1 || got[0]["code"] != "home_locked" {
		t.Fatalf("a second arrival is no key: %v", got)
	}

	// The arrival waits homeSummonLife at most, and only while the owner is online.
	r.summonAll(t, ada, bo)
	r.now = r.now.Add(homeSummonLife + time.Second)
	r.join(bo, "HOUSE_1")
	if got := kinds(drain(bo), "error"); len(got) != 1 || got[0]["code"] != "home_locked" {
		t.Fatalf("a late arrival: %v", got)
	}
	r.homePassFor(bo, 1, false)
	r.summonAll(t, ada, bo)
	if hm.called[2][1].IsZero() {
		t.Fatal("no arrival kept")
	}
	r.do(bo, map[string]any{"t": "leave"})
	r.hub.Disconnect(bo)
	if hm.called[2] != nil {
		t.Fatal("a disconnect keeps the arrival")
	}
}

func TestSummonFromSomeoneElsesHome(t *testing.T) {
	r, _ := newSummonHomesRig(t)
	ada, bo, cy := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(5, "Cy")
	r.at(bo, "GSTREET2", 0, 0)
	r.at(cy, "HOME_5", 0, 0)
	drain(bo)
	drain(cy)
	r.homePassFor(ada, 5, false)
	// A visitor on someone's public home street or Tower floor (inc_pols.js 448-458).
	for _, street := range []string{"HOME_5", "TOWER_5_0"} {
		r.at(ada, street, 100, -10)
		drain(ada)
		drain(cy)
		if g := r.summonAll(t, ada, bo); g["street"] != street {
			t.Fatalf("%s: go %v", street, g)
		}
	}
	// Not someone the owner blocks, here or from inside their house.
	r.homePassFor(ada, 5, true)
	for _, street := range []string{"HOME_5", "HOUSE_5"} {
		r.at(ada, street, 0, 0)
		drain(ada)
		r.ign.ign[[2]int64{5, 2}] = true
		r.tick()
		r.do(ada, map[string]any{"t": "summon", "id": 2})
		if m := summonOne(t, drain(ada), "summon_refused"); m["text"] != summonTextThem {
			t.Fatalf("%s, blocked by the owner: %v", street, m)
		}
		delete(r.ign.ign, [2]int64{5, 2})
	}
}

// Austin 2026-11-13, inc_teleportation.js 663-665: from inside someone else's house the target
// goes to its front door on the owner's home street, never inside, and with or without a key.
func TestSummonFromSomeoneElsesHouseToItsDoor(t *testing.T) {
	r, hm := newSummonHomesRig(t)
	ada, bo, cy := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(5, "Cy")
	r.at(bo, "GSTREET2", 0, 0)
	r.at(cy, "HOUSE_5", 0, 0)
	drain(bo)
	drain(cy)
	r.homePassFor(ada, 5, true)
	r.at(ada, "HOUSE_5", 300, -40)
	drain(ada)
	drain(cy)

	r.tick()
	r.do(ada, map[string]any{"t": "summon", "id": 2})
	summonOne(t, drain(ada), "summon_waiting")
	asked := summonOne(t, drain(bo), "summon_asked")
	door, _ := asked["door"].(map[string]any)
	if asked["street"] != "HOUSE_5" || door["owner"] != float64(5) || door["name"] != "Cy" {
		t.Fatalf("asked %v", asked)
	}
	r.do(bo, map[string]any{"t": "summon_answer", "from": 1, "accept": true})
	if p := summonOne(t, drain(ada), "summon_pay"); p["door"].(map[string]any)["name"] != "Cy" {
		t.Fatalf("pay %v", p)
	}
	r.do(ada, map[string]any{"t": "summon_paid", "id": 2, "ok": true})
	g := summonOne(t, drain(bo), "summon_go")
	if g["street"] != "HOME_5" || g["door"].(map[string]any)["owner"] != float64(5) || g["x"] != nil || g["party"] != false {
		t.Fatalf("go %v", g)
	}
	if hm.called[2] != nil {
		t.Fatal("a summons to the door let them in")
	}
	// At the door, as any visitor: the street with the site's pass; the house still wants a key or a knock.
	r.join(bo, "HOME_5")
	if got := buddyKinds(drain(bo)); got != "error:home_private" {
		t.Fatalf("no pass, no home street: %s", got)
	}
	r.homePassFor(bo, 5, false)
	r.join(bo, "HOME_5")
	if f := drain(bo); f[0]["t"] != "street" {
		t.Fatalf("at the door: %v", f)
	}
	r.join(bo, "HOUSE_5")
	if got := kinds(drain(bo), "error"); len(got) != 1 || got[0]["code"] != "home_locked" {
		t.Fatalf("no key, no way in: %v", got)
	}
	r.at(bo, "GSTREET2", 0, 0)
	drain(bo)
	drain(cy)

	// The owner summoned into their own house goes in (663: pols_is_owner(pc)), beside the summoner.
	r.at(cy, "GSTREET2", 0, 0)
	drain(cy)
	drain(ada)
	if g := r.summonAll(t, ada, cy); g["street"] != "HOUSE_5" || g["x"] != float64(320) || g["door"] != nil {
		t.Fatalf("the owner: %v", g)
	}

	// The owner offline: the name the site signed in the summoner's pass ("Owner" in homePassFor).
	r.hub.Disconnect(cy)
	drain(ada)
	if g := r.summonAll(t, ada, bo); g["street"] != "HOME_5" || g["door"].(map[string]any)["name"] != "Owner" {
		t.Fatalf("owner offline: %v", g)
	}

	// The summoner's own house is still inside (TestSummonIntoHomes), with no door.
	r.at(ada, "HOUSE_1", 10, 0)
	drain(ada)
	if g := r.summonAll(t, ada, bo); g["street"] != "HOUSE_1" || g["door"] != nil {
		t.Fatalf("own house: %v", g)
	}
}
