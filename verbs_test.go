package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type verbsCredit struct {
	user int64
	why  string
	got  tradeGoods
}

type verbsRig struct {
	t       *testing.T
	hub     *Hub
	m       *verbs
	ign     *fakeIgnores
	now     time.Time
	timers  []*rigTimer
	credits []verbsCredit
}

func newVerbsRig(t *testing.T) *verbsRig {
	r := &verbsRig{t: t, now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newVerbs(h).(*verbs); return r.m },
		func(h *Hub) Module { return r.ign },
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	r.hub.provided["credits.add"] = func(user int64, why string, g tradeGoods) string {
		r.credits = append(r.credits, verbsCredit{user, why, g})
		return "cr-c-test"
	}
	return r
}

func (r *verbsRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

func (r *verbsRig) do(cl *Client, msg map[string]any) {
	r.now = r.now.Add(time.Second)
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *verbsRig) joinAt(cl *Client, street string, x float64) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": x, "y": 0})
}

// fire runs the timers due by d (the charades clock).
func (r *verbsRig) fire(d time.Duration) {
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if !tm.stopped && tm.d <= d {
			tm.stopped = true
			tm.fn() // h.After wraps it in the hub lock
		}
	}
}

const verbsStreet = "LA5101HF7F429V"

func (r *verbsRig) pair() (ada, bo *Client) {
	ada, bo = r.connect(7, "Ada"), r.connect(9, "Bo")
	r.joinAt(ada, verbsStreet, 100)
	r.joinAt(bo, verbsStreet, 300)
	drain(ada)
	drain(bo)
	return ada, bo
}

func verbOne(t *testing.T, msgs []map[string]any, kind string) map[string]any {
	t.Helper()
	got := kinds(msgs, kind)
	if len(got) != 1 {
		t.Fatalf("want one %s, got %d in %v", kind, len(got), msgs)
	}
	return got[0]
}

func TestAHugReachesTheOtherPlayerFromTheServer(t *testing.T) {
	r := newVerbsRig(t)
	ada, bo := r.pair()
	r.do(ada, map[string]any{"t": "verb_do", "verb": "hug", "to": 9, "ref": "r1"})
	done := verbOne(t, drain(ada), "verb_done")
	if done["status"] != "ok" || done["ref"] != "r1" || done["verb"] != "hug" {
		t.Fatalf("done = %v", done)
	}
	to := done["to"].(map[string]any)
	if to["id"].(float64) != 9 || to["name"] != "Bo" {
		t.Fatalf("to = %v", to)
	}
	got := verbOne(t, drain(bo), "verb_got")
	if got["verb"] != "hug" || got["id"] != done["id"] || got["from"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("got = %v (done %v)", got, done)
	}
	// Each act has its own id.
	r.do(ada, map[string]any{"t": "verb_do", "verb": "high_five", "to": 9})
	if again := verbOne(t, drain(ada), "verb_done"); again["id"] == done["id"] {
		t.Fatalf("same id twice: %v", again)
	}
}

func TestVerbsKeepTheSourceRefusals(t *testing.T) {
	r := newVerbsRig(t)
	ada, bo := r.pair()
	refused := func(msg map[string]any, want string) {
		t.Helper()
		r.do(ada, msg)
		done := verbOne(t, drain(ada), "verb_done")
		if done["status"] != "refused" || done["text"] != want {
			t.Fatalf("%v: got %v, want %q", msg, done, want)
		}
		if got := kinds(drain(bo), "verb_got"); len(got) != 0 {
			t.Fatalf("%v reached Bo: %v", msg, got)
		}
	}
	refused(map[string]any{"t": "verb_do", "verb": "hug", "to": 7}, "You can't do that to yourself.")
	refused(map[string]any{"t": "verb_do", "verb": "soak", "to": 7}, "You can't soak yourself!")
	refused(map[string]any{"t": "verb_do", "verb": "tickle", "to": 9}, verbTextBad)
	refused(map[string]any{"t": "verb_do", "verb": "play_for", "to": 9, "block": "apple"}, verbTextBad)
	refused(map[string]any{"t": "verb_do", "verb": "hug", "to": 44}, "That player has gone offline.")
	refused(map[string]any{"t": "verb_do", "verb": "play_for", "to": 44, "block": "musicblock_d_green_01"}, "Hey! Where'd they go?")

	// Too far: verb_radius 800.
	r.do(bo, map[string]any{"t": "move", "x": 1000, "y": 0, "dir": 1, "anim": "walk"})
	refused(map[string]any{"t": "verb_do", "verb": "splank", "to": 9}, "That player is too far away. Try moving a little closer.")
	r.do(bo, map[string]any{"t": "move", "x": 850, "y": 0, "dir": 1, "anim": "walk"})
	r.do(ada, map[string]any{"t": "verb_do", "verb": "splank", "to": 9})
	if done := verbOne(t, drain(ada), "verb_done"); done["status"] != "ok" {
		t.Fatalf("750 px away: %v", done)
	}
	drain(bo)

	// Blocks either way.
	r.ign.ign[[2]int64{9, 7}] = true
	refused(map[string]any{"t": "verb_do", "verb": "kiss", "to": 9}, "That player has blocked you.")
	r.ign.ign = map[[2]int64]bool{{7, 9}: true}
	refused(map[string]any{"t": "verb_do", "verb": "kiss", "to": 9}, "You are blocking Bo.")
	r.ign.ign = map[[2]int64]bool{}

	// Another street.
	r.joinAt(bo, "LA5101HF7F429W", 100)
	drain(ada)
	refused(map[string]any{"t": "verb_do", "verb": "moon", "to": 9}, "You can't do that to a player that isn't there!")
}

func TestNoEmotesInARaceButASoakIsFine(t *testing.T) {
	r := newVerbsRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.joinAt(ada, "RACE_ABC", 0)
	r.joinAt(bo, "RACE_ABC", 10)
	drain(ada)
	drain(bo)
	for verb, want := range map[string]string{"hug": verbTextBearRace, "moon": verbTextBearRace, "splank": verbTextPlankRace} {
		r.do(ada, map[string]any{"t": "verb_do", "verb": verb, "to": 9})
		if done := verbOne(t, drain(ada), "verb_done"); done["text"] != want {
			t.Fatalf("%s in a race: %v", verb, done)
		}
	}
	r.do(ada, map[string]any{"t": "verb_do", "verb": "soak", "to": 9})
	if done := verbOne(t, drain(ada), "verb_done"); done["status"] != "ok" {
		t.Fatalf("soak in a race: %v", done)
	}
	if got := verbOne(t, drain(bo), "verb_got"); got["verb"] != "soak" {
		t.Fatalf("got %v", got)
	}
}

func TestPlayForCarriesTheBlockAndKissTheGarlic(t *testing.T) {
	r := newVerbsRig(t)
	ada, bo := r.pair()
	r.do(ada, map[string]any{"t": "verb_do", "verb": "play_for", "to": 9, "block": "musicblock_d_green_01", "garlic": true})
	done := verbOne(t, drain(ada), "verb_done")
	got := verbOne(t, drain(bo), "verb_got")
	if done["block"] != "musicblock_d_green_01" || got["block"] != "musicblock_d_green_01" || got["garlic"] != nil {
		t.Fatalf("play for: %v / %v", done, got)
	}
	r.do(ada, map[string]any{"t": "verb_do", "verb": "kiss", "to": 9, "garlic": true, "block": "musicblock_d_green_01"})
	got = verbOne(t, drain(bo), "verb_got")
	if got["garlic"] != true || got["block"] != nil {
		t.Fatalf("kiss: %v", got)
	}
	drain(ada)
}

func TestVerbsAreRateLimited(t *testing.T) {
	r := newVerbsRig(t)
	ada, _ := r.pair()
	b, _ := json.Marshal(map[string]any{"t": "verb_do", "verb": "hug", "to": 9})
	for i := 0; i < verbBurst+2; i++ {
		r.hub.Handle(ada, b) // all in the same instant
	}
	msgs := kinds(drain(ada), "verb_done")
	ok, slow := 0, 0
	for _, m := range msgs {
		if m["status"] == "ok" {
			ok++
		} else if m["text"] == verbTextSlow {
			slow++
		}
	}
	if ok != verbBurst || slow != 2 {
		t.Fatalf("ok %d slow %d", ok, slow)
	}
}

func TestCharadesWordsAreCleanedAsTheSourceDid(t *testing.T) {
	for in, want := range map[string]string{
		"  Big Apple! ":         "big apple",
		"sn@ke-Pit 9":           "snke-pit 9",
		"!!!":                   "",
		strings.Repeat("a", 40): strings.Repeat("a", 32),
		// substr(0, 32) comes before the strip.
		strings.Repeat("!", 31) + "ab": "a",
	} {
		if got := cleanCharadesWord(in); got != want {
			t.Fatalf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCharadesAGuessWinsTheGameAndTheCurrants(t *testing.T) {
	r := newVerbsRig(t)
	ada, bo := r.pair()

	// Alone first.
	cy := r.connect(11, "Cy")
	r.joinAt(cy, "LA5101HF7F429W", 0)
	drain(cy)
	r.do(cy, map[string]any{"t": "charades_start", "word": "apple"})
	if s := verbOne(t, drain(cy), "charades_started"); s["status"] != "refused" || s["text"] != charadesTextAlone {
		t.Fatalf("alone: %v", s)
	}
	// An empty word keeps the potion.
	r.do(ada, map[string]any{"t": "charades_start", "word": "!!"})
	if s := verbOne(t, drain(ada), "charades_started"); s["status"] != "refused" || s["why"] != "empty" {
		t.Fatalf("empty: %v", s)
	}

	r.do(ada, map[string]any{"t": "charades_start", "word": "Ginger Root"})
	s := verbOne(t, drain(ada), "charades_started")
	if s["status"] != "ok" || s["word"] != "ginger root" {
		t.Fatalf("start: %v", s)
	}
	on := verbOne(t, drain(bo), "charades_on")
	raw, _ := json.Marshal(on)
	if strings.Contains(string(raw), "ginger") || on["from"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("the word leaked or the drinker is wrong: %s", raw)
	}
	r.do(ada, map[string]any{"t": "charades_start", "word": "other"})
	if s := verbOne(t, drain(ada), "charades_started"); s["text"] != charadesTextAgain {
		t.Fatalf("again: %v", s)
	}

	// Cy joins and hears of it.
	r.joinAt(cy, verbsStreet, 200)
	if on := verbOne(t, drain(cy), "charades_on"); on["from"].(map[string]any)["id"].(float64) != 7 {
		t.Fatalf("joiner: %v", on)
	}
	drain(ada)
	drain(bo)

	r.do(bo, map[string]any{"t": "chat", "text": "is it ginger?"})
	if len(kinds(drain(bo), "charades_won")) != 0 {
		t.Fatal("ginger alone won")
	}
	r.do(bo, map[string]any{"t": "chat", "text": "GINGER ROOT!"})
	won := verbOne(t, drain(bo), "charades_won")
	if won["currants"].(float64) != 250 || won["xp"].(float64) != 50 {
		t.Fatalf("won: %v", won)
	}
	end := verbOne(t, drain(ada), "charades_end")
	if end["why"] != "guessed" || end["winner"].(map[string]any)["name"] != "Bo" {
		t.Fatalf("end: %v", end)
	}
	over := verbOne(t, drain(cy), "charades_over")
	if over["text"] != "Bo guessed correctly! +50 iMG, +250 currants" {
		t.Fatalf("over: %v", over)
	}
	if len(r.credits) != 1 || r.credits[0].user != 9 || r.credits[0].why != "charades" || r.credits[0].got.Currants != 250 {
		t.Fatalf("credits: %v", r.credits)
	}
	// Over: saying it again wins nothing.
	r.do(cy, map[string]any{"t": "chat", "text": "ginger root"})
	if len(kinds(drain(cy), "charades_won")) != 0 || len(r.credits) != 1 {
		t.Fatal("a second guess won")
	}
}

func TestCharadesRuinedTimedOutAndLeft(t *testing.T) {
	r := newVerbsRig(t)
	ada, bo := r.pair()

	r.do(ada, map[string]any{"t": "charades_start", "word": "moon"})
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "chat", "text": "the MOON is up"})
	if end := verbOne(t, drain(ada), "charades_end"); end["why"] != "ruined" {
		t.Fatalf("ruin: %v", end)
	}
	if over := verbOne(t, drain(bo), "charades_over"); over["text"] != "Ada ruined their own game of charades by saying the answer." {
		t.Fatalf("ruin over: %v", over)
	}

	r.do(ada, map[string]any{"t": "charades_start", "word": "piggy"})
	drain(ada)
	drain(bo)
	r.fire(charadesFor)
	if end := verbOne(t, drain(ada), "charades_end"); end["why"] != "timeout" || end["word"] != "piggy" {
		t.Fatalf("timeout: %v", end)
	}
	if over := verbOne(t, drain(bo), "charades_over"); over["text"] != "Ada's game of charades is over. Nobody guessed 'piggy'." {
		t.Fatalf("timeout over: %v", over)
	}
	r.do(bo, map[string]any{"t": "chat", "text": "piggy"})
	if len(r.credits) != 0 {
		t.Fatal("a guess after the end won")
	}

	r.do(ada, map[string]any{"t": "charades_start", "word": "crab"})
	drain(ada)
	drain(bo)
	r.joinAt(ada, "LA5101HF7F429W", 0)
	if end := verbOne(t, drain(ada), "charades_end"); end["why"] != "left" {
		t.Fatalf("left: %v", end)
	}
	if over := verbOne(t, drain(bo), "charades_over"); over["why"] != "left" {
		t.Fatalf("left over: %v", over)
	}
	if len(r.m.games) != 0 {
		t.Fatalf("games left: %v", r.m.games)
	}
	r.do(bo, map[string]any{"t": "chat", "text": "crab"})
	if len(r.credits) != 0 {
		t.Fatal("a guess after leaving won")
	}
}

func TestEpicBlowoutNeedsTwelvePlayersElevenOfThemInfluenced(t *testing.T) {
	r := newVerbsRig(t)
	house := "HOUSE_7"
	owner := r.connect(7, "Ada")
	r.joinAt(owner, house, 0)
	var guests []*Client
	for i := 0; i < 11; i++ {
		g := r.connect(int64(100+i), fmt.Sprintf("Guest %d", i))
		guests = append(guests, g)
		r.do(g, map[string]any{"t": "verbs_influence", "on": i < 10}) // influenced before coming in
		r.joinAt(g, house, float64(i*10))
	}
	// The owner's buzz does not count.
	r.do(owner, map[string]any{"t": "verbs_influence", "on": true})
	for _, c := range append([]*Client{owner}, guests...) {
		if got := kinds(drain(c), "party_blowout"); len(got) != 0 {
			t.Fatalf("10 influenced guests made a blowout: %v", got)
		}
	}
	// The eleventh guest gets buzzed: everyone there was at it, once.
	r.do(guests[10], map[string]any{"t": "verbs_influence", "on": true})
	for _, c := range append([]*Client{owner}, guests...) {
		got := kinds(drain(c), "party_blowout")
		if len(got) != 1 || got[0]["owner"].(float64) != 7 {
			t.Fatalf("player %d: %v", c.ID(), got)
		}
	}
	r.do(guests[3], map[string]any{"t": "verbs_influence", "on": false})
	r.do(guests[3], map[string]any{"t": "verbs_influence", "on": true})
	if got := kinds(drain(guests[3]), "party_blowout"); len(got) != 0 {
		t.Fatalf("told twice: %v", got)
	}
}

func TestEpicBlowoutOnlyInAHome(t *testing.T) {
	r := newVerbsRig(t)
	var all []*Client
	for i := 0; i < 12; i++ {
		c := r.connect(int64(200+i), fmt.Sprintf("P%d", i))
		r.do(c, map[string]any{"t": "verbs_influence", "on": true})
		r.joinAt(c, "PARTY_ABC", 0)
		all = append(all, c)
	}
	r.do(all[0], map[string]any{"t": "verbs_influence", "on": false})
	r.do(all[0], map[string]any{"t": "verbs_influence", "on": true})
	for _, c := range all {
		if got := kinds(drain(c), "party_blowout"); len(got) != 0 {
			t.Fatalf("a Party Space is no house party: %v", got)
		}
	}
}

func TestEpicBlowoutDevThresholds(t *testing.T) {
	r := newVerbsRig(t)
	r.m.dev = true
	owner := r.connect(7, "Ada")
	r.joinAt(owner, "HOME_7", 0)
	for i := 0; i < 3; i++ {
		g := r.connect(int64(300+i), "G")
		r.do(g, map[string]any{"t": "verbs_influence", "on": true})
		r.joinAt(g, "HOME_7", 0)
	}
	if got := kinds(drain(owner), "party_blowout"); len(got) != 1 {
		t.Fatalf("dev blowout: %v", got)
	}
}

// G432 random_kindness.js 274-328: Bestow is a point of the item through the ledger, then the
// target's own game says which landed and the giver hears it.
func TestBestowRandomKindness(t *testing.T) {
	r := newVerbsRig(t)
	led := newFakeUseLedger()
	r.m.uses.ledger = led
	r.m.uses.spawn = func(fn func()) { fn() }
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.joinAt(ada, "GA5", 100)
	r.joinAt(bo, "GA5", 5000) // disable_proximity: across the street is fine
	drain(ada)
	drain(bo)
	bestow := func(ref string) {
		r.do(ada, map[string]any{"t": "verb_do", "verb": "bestow", "to": 9, "ref": ref, "k": 0})
		r.fire(0) // the ledger's answer comes back under the hub lock
	}
	// Without the item the ledger refuses and nothing reaches the other player.
	bestow("a")
	done := kinds(drain(ada), "verb_done")
	if len(done) != 1 || done[0]["status"] != "refused" || done[0]["text"] != useTextLacks {
		t.Fatalf("no item: %v", done)
	}
	if got := kinds(drain(bo), "verb_got"); len(got) != 0 {
		t.Fatalf("a bestow with no item arrived: %v", got)
	}
	// With it: the target hears, the giver's answer carries the ledger's word.
	led.give(7, useKindness, 1)
	bestow("b")
	done = kinds(drain(ada), "verb_done")
	if len(done) != 1 || done[0]["status"] != "ok" || done[0]["use"] == nil {
		t.Fatalf("bestow: %v", done)
	}
	if use := done[0]["use"].(map[string]any); use["c"] != useKindness {
		t.Fatalf("use: %v", use)
	}
	got := kinds(drain(bo), "verb_got")
	if len(got) != 1 || got[0]["verb"] != "bestow" || got[0]["from"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("got: %v", got)
	}
	id := got[0]["id"].(string)
	// Someone else can't answer, nor can the answer be more than a bestow gives.
	r.do(ada, map[string]any{"t": "verb_landed", "id": id, "stat": "mood", "n": 50})
	if l := kinds(drain(ada), "verb_landed"); len(l) != 0 {
		t.Fatalf("the giver answered for the target: %v", l)
	}
	r.do(bo, map[string]any{"t": "verb_landed", "id": id, "stat": "mood", "n": 50})
	l := kinds(drain(ada), "verb_landed")
	if len(l) != 1 || l[0]["stat"] != "mood" || l[0]["n"] != float64(50) || l[0]["to"].(map[string]any)["name"] != "Bo" {
		t.Fatalf("landed: %v", l)
	}
	// Once only.
	r.do(bo, map[string]any{"t": "verb_landed", "id": id, "stat": "energy", "n": 50})
	if l := kinds(drain(ada), "verb_landed"); len(l) != 0 {
		t.Fatalf("answered twice: %v", l)
	}
	// An answer past 50, or none in kindnessWait: the giver hears only that it was bestowed.
	bestow("c")
	drain(ada)
	id = kinds(drain(bo), "verb_got")[0]["id"].(string)
	r.do(bo, map[string]any{"t": "verb_landed", "id": id, "stat": "mood", "n": 5000})
	if l := kinds(drain(ada), "verb_landed"); len(l) != 1 || l[0]["stat"] != nil {
		t.Fatalf("a claim past 50: %v", l)
	}
	bestow("d")
	drain(ada)
	drain(bo)
	r.fire(kindnessWait)
	if l := kinds(drain(ada), "verb_landed"); len(l) != 1 || l[0]["stat"] != nil {
		t.Fatalf("no answer: %v", l)
	}
	// Not yourself; not someone gone.
	bestow0 := map[string]any{"t": "verb_do", "verb": "bestow", "to": 7, "ref": "e"}
	r.do(ada, bestow0)
	if d := kinds(drain(ada), "verb_done"); len(d) != 1 || d[0]["text"] != verbTextSelf {
		t.Fatalf("yourself: %v", d)
	}
	calls := len(led.calls)
	r.joinAt(bo, "GIQ", 100)
	r.do(ada, map[string]any{"t": "verb_do", "verb": "bestow", "to": 9, "ref": "f"})
	r.fire(0)
	if d := kinds(drain(ada), "verb_done"); len(d) != 1 || d[0]["text"] != verbTextNotThere {
		t.Fatalf("not there: %v", d)
	}
	if len(led.calls) != calls {
		t.Fatal("a point was asked for a player who isn't there")
	}
	// The rate brake holds a spammer to about one a second.
	r.joinAt(bo, "GA5", 100)
	drain(ada)
	drain(bo)
	led.give(7, useKindness, 40)
	sent := 0
	for i := 0; i < 20; i++ {
		b, _ := json.Marshal(map[string]any{"t": "verb_do", "verb": "bestow", "to": 9, "ref": fmt.Sprintf("s%d", i)})
		r.hub.Handle(ada, b)
		r.fire(0)
	}
	sent = len(kinds(drain(bo), "verb_got"))
	if sent > verbBurst {
		t.Fatalf("spam: %d got through at once", sent)
	}
}
