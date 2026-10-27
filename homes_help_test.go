package main

import (
	"strings"
	"testing"
	"time"
)

// fakeButler stands in for the site's groddle/v1/ledger/butler.
type fakeButler struct {
	calls  []map[string]any
	answer butlerAnswer
	hold   func()
}

func (f *fakeButler) butler(op string, from, to int64, gave tradeGoods, text string) butlerAnswer {
	f.calls = append(f.calls, map[string]any{"op": op, "from": from, "to": to, "gave": gave, "text": text})
	if f.hold != nil {
		f.hold()
	}
	return f.answer
}

// helpRig is the homes rig with the help module too, the site faked and its calls run in place.
type helpRig struct {
	*homesRig
	help    *homeHelp
	site    *fakeButler
	pending []func()
}

func newHelpRig(t *testing.T) *helpRig {
	r := &helpRig{homesRig: &homesRig{t: t, now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}}, site: &fakeButler{answer: butlerAnswer{Status: "settled"}}}
	savedSecret, savedHelp := homesSecretForTest, homeHelpSecretForTest
	homesSecretForTest, homeHelpSecretForTest = homesTestSecret, homesTestSecret
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newHomes(h).(*homes); return r.m },
		func(h *Hub) Module { r.help = newHomeHelp(h).(*homeHelp); return r.help },
		func(h *Hub) Module { return r.ign },
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	homesSecretForTest, homeHelpSecretForTest = savedSecret, savedHelp
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	r.help.site = r.site
	r.help.spawn = func(fn func()) { r.pending = append(r.pending, fn) }
	n := 0
	r.help.newID = func() string { n++; return strings.Repeat("0", 15) + string(rune('0'+n)) }
	return r
}

// settle runs the site calls waiting, then their answers under the hub lock.
func (r *helpRig) settle() {
	for len(r.pending) > 0 {
		fn := r.pending[0]
		r.pending = r.pending[1:]
		fn()
	}
	r.fire(0)
}

// visiting puts Bo (9) on Ada's (7) home street with Ada there.
func (r *helpRig) visiting() (ada, bo *Client) {
	ada, bo = r.connect(7, "Ada"), r.connect(9, "Bo")
	r.join(ada, home(7))
	r.pass(bo, 7, "Ada", false)
	r.join(bo, home(7))
	drain(ada)
	drain(bo)
	return ada, bo
}

func TestAVisitorLeavesAPackageWithTheButler(t *testing.T) {
	r := newHelpRig(t)
	ada, bo := r.visiting()
	r.do(bo, map[string]any{"t": "butler_leave", "owner": 7, "item": map[string]any{"c": "cherry", "n": 3}, "text": "  For <b>you</b>,\n Ada!  "})
	if got := drain(bo); len(got) != 0 {
		t.Fatalf("nothing until the site answers: %v", got)
	}
	r.settle()
	if len(r.site.calls) != 1 {
		t.Fatalf("one call: %v", r.site.calls)
	}
	c := r.site.calls[0]
	g := c["gave"].(tradeGoods)
	if c["op"] != "butler-0000000000000001" || c["from"] != int64(9) || c["to"] != int64(7) || c["text"] != "For you, Ada!" || len(g.Items) != 1 || g.Items[0].C != "cherry" || g.Items[0].N != 3 || g.Currants != 0 {
		t.Fatalf("the call: %+v", c)
	}
	f := only(t, drain(bo), "butler_left")
	if f["status"] != "settled" || f["op"] != "butler-0000000000000001" || f["owner"] != float64(7) || f["message"] != true || f["item"].(map[string]any)["c"] != "cherry" {
		t.Fatalf("Bo is told: %v", f)
	}
	p := only(t, drain(ada), "butler_parcel")
	if p["from"].(map[string]any)["name"] != "Bo" || p["item"] != true || p["message"] != true {
		t.Fatalf("Ada, online, is told: %v", p)
	}
}

func TestTheButlerTakesOnlyFromItsHomeStreetAndOneVisitorAtATime(t *testing.T) {
	r := newHelpRig(t)
	_, bo := r.visiting()
	cy := r.connect(11, "Cy")
	r.pass(cy, 7, "Ada", false)
	drain(cy)
	// Cy is not on Ada's street.
	r.do(cy, map[string]any{"t": "butler_leave", "owner": 7, "text": "hi"})
	if f := only(t, drain(cy), "butler_left"); f["status"] != "refused" || f["why"] != "away" {
		t.Fatalf("from afar: %v", f)
	}
	r.join(cy, home(7))
	drain(cy)
	drain(bo)
	r.do(bo, map[string]any{"t": "butler_leave", "owner": 7, "text": "first"})
	r.do(cy, map[string]any{"t": "butler_leave", "owner": 7, "text": "second"})
	if f := only(t, drain(cy), "butler_left"); f["why"] != "busy" || f["text"] != "The Butler is talking to somebody else right now." {
		t.Fatalf("busy: %v", f)
	}
	r.settle()
	if f := only(t, drain(bo), "butler_left"); f["status"] != "settled" {
		t.Fatalf("Bo's: %v", f)
	}
	r.now = r.now.Add(10 * time.Second)
	r.do(cy, map[string]any{"t": "butler_leave", "owner": 7, "text": "second"})
	r.settle()
	if f := only(t, drain(cy), "butler_left"); f["status"] != "settled" {
		t.Fatalf("Cy, after: %v", f)
	}
	if len(r.site.calls) != 2 {
		t.Fatalf("two calls: %d", len(r.site.calls))
	}
}

func TestTheButlerRefusesBadParcelsBlockedVisitorsAndWhatTheSiteRefuses(t *testing.T) {
	r := newHelpRig(t)
	_, bo := r.visiting()
	for i, msg := range []map[string]any{
		{"t": "butler_leave", "owner": 7},
		{"t": "butler_leave", "owner": 7, "text": "   "},
		{"t": "butler_leave", "owner": 7, "item": map[string]any{"c": "Cherry!", "n": 1}},
		{"t": "butler_leave", "owner": 7, "item": map[string]any{"c": "cherry", "n": 0}},
		{"t": "butler_leave", "owner": 7, "item": map[string]any{"c": "cherry", "n": 10000}},
		{"t": "butler_leave", "owner": 9, "text": "to myself"},
	} {
		r.now = r.now.Add(10 * time.Second)
		r.do(bo, msg)
		if f := only(t, drain(bo), "butler_left"); f["status"] != "refused" {
			t.Fatalf("%d: %v", i, f)
		}
	}
	r.ign.ign[[2]int64{7, 9}] = true
	r.now = r.now.Add(10 * time.Second)
	r.do(bo, map[string]any{"t": "butler_leave", "owner": 7, "text": "hello"})
	if f := only(t, drain(bo), "butler_left"); f["why"] != "blocked" {
		t.Fatalf("blocked: %v", f)
	}
	delete(r.ign.ign, [2]int64{7, 9})
	if len(r.site.calls) != 0 {
		t.Fatalf("no call for any of those: %v", r.site.calls)
	}
	r.site.answer = butlerAnswer{Status: "refused", Why: "full", Text: "Sorry, I can't carry any more packages."}
	r.now = r.now.Add(10 * time.Second)
	r.do(bo, map[string]any{"t": "butler_leave", "owner": 7, "item": map[string]any{"c": "cherry", "n": 1}})
	r.settle()
	if f := only(t, drain(bo), "butler_left"); f["status"] != "refused" || f["text"] != "Sorry, I can't carry any more packages." {
		t.Fatalf("the site's no: %v", f)
	}
	// No answer at all is a no (the site's op is idempotent, but nobody asks again).
	r.site.answer = butlerAnswer{}
	r.now = r.now.Add(10 * time.Second)
	r.do(bo, map[string]any{"t": "butler_leave", "owner": 7, "text": "anyone?"})
	r.settle()
	if f := only(t, drain(bo), "butler_left"); f["status"] != "refused" || f["why"] != "off" {
		t.Fatalf("no answer: %v", f)
	}
	// Without a site the Butler takes nothing.
	r.help.site = nil
	r.now = r.now.Add(10 * time.Second)
	r.do(bo, map[string]any{"t": "butler_leave", "owner": 7, "text": "anyone?"})
	if f := only(t, drain(bo), "butler_left"); f["why"] != "off" {
		t.Fatalf("no site: %v", f)
	}
}

func TestAButlerMessageIsOneLineOf431(t *testing.T) {
	long := strings.Repeat("é", 500)
	if got := cleanButlerText(long); len([]rune(got)) != 431 {
		t.Fatalf("431 characters: %d", len([]rune(got)))
	}
	if got := cleanButlerText("a\tb\n\nc <script>x</script> d"); got != "a b c x d" {
		t.Fatalf("cleaned: %q", got)
	}
}

func TestAVisitorLeavingFreesTheButler(t *testing.T) {
	r := newHelpRig(t)
	_, bo := r.visiting()
	r.do(bo, map[string]any{"t": "butler_leave", "owner": 7, "text": "brb"})
	r.hub.Disconnect(bo)
	if len(r.help.busy) != 0 {
		t.Fatalf("busy after a disconnect: %v", r.help.busy)
	}
	r.settle() // the answer finds nobody, and nothing breaks
}

func (r *helpRig) tendToken(visitor, owner int64, n string, exp time.Duration) string {
	return signBuddy(homesTestSecret, map[string]any{"k": "home_tend", "sub": visitor, "o": owner, "acts": []map[string]any{{"y": "street", "g": "c1", "p": "1-1", "a": "water", "s": "", "at": 5}}, "iat": r.now.Unix(), "exp": r.now.Add(exp).Unix(), "n": n})
}

func TestTendingReachesTheOwnerAndEveryoneThereOnce(t *testing.T) {
	r := newHelpRig(t)
	ada, bo := r.visiting()
	cy := r.connect(11, "Cy")
	r.pass(cy, 7, "Ada", false)
	r.join(cy, home(7))
	drain(cy)
	drain(ada)
	drain(bo)
	for i, tok := range []string{"nope", r.tendToken(11, 7, "x", time.Minute), r.tendToken(9, 7, "y", -time.Second),
		signBuddy([]byte("another-secret-another-secret-another"), map[string]any{"k": "home_tend", "sub": 9, "o": 7, "acts": []any{map[string]any{"a": "water"}}, "exp": r.now.Add(time.Minute).Unix(), "n": "z"})} {
		r.do(bo, map[string]any{"t": "home_tend_tell", "token": tok})
		if got := buddyKinds(drain(bo)); got != "error:homes" {
			t.Fatalf("token %d: %s", i, got)
		}
	}
	tok := r.tendToken(9, 7, "n1", time.Minute)
	r.do(bo, map[string]any{"t": "home_tend_tell", "token": tok})
	if got := drain(bo); len(got) != 0 {
		t.Fatalf("the tender is not told: %v", got)
	}
	a := only(t, drain(ada), "home_tended")
	acts := a["acts"].([]any)
	if a["owner"] != float64(7) || a["from"].(map[string]any)["name"] != "Bo" || len(acts) != 1 || acts[0].(map[string]any)["a"] != "water" {
		t.Fatalf("Ada: %v", a)
	}
	only(t, drain(cy), "home_tended")
	r.do(bo, map[string]any{"t": "home_tend_tell", "token": tok})
	if got := drain(ada); len(got) != 0 {
		t.Fatalf("a proof works once: %v", got)
	}
}

func TestAHouseWithFourPlayersAndItsOwnerCountsAHostedParty(t *testing.T) {
	r := newHelpRig(t)
	ada := r.connect(7, "Ada")
	r.join(ada, house(7))
	drain(ada)
	var guests []*Client
	for i := int64(0); i < 17; i++ {
		g := r.connect(20+i, "G")
		r.pass(g, 7, "Ada", true)
		drain(g)
		guests = append(guests, g)
	}
	kinds := func() string {
		var out []string
		for _, f := range drain(ada) {
			if f["t"] == "home_hosted" {
				out = append(out, strings.Repeat("3", 1)+map[bool]string{true: "+17", false: ""}[f["seventeen"] == true])
			}
		}
		return strings.Join(out, ",")
	}
	for i := 0; i < 2; i++ {
		r.join(guests[i], house(7))
	}
	if got := kinds(); got != "" {
		t.Fatalf("three players is no party: %s", got)
	}
	r.join(guests[2], house(7))
	if got := kinds(); got != "3" {
		t.Fatalf("four players: %s", got)
	}
	for i := 3; i < 17; i++ {
		r.join(guests[i], house(7))
	}
	if got := kinds(); !strings.HasSuffix(got, "3+17") || strings.Count(got, "+17") != 1 {
		t.Fatalf("eighteen players: %s", got)
	}
	// Without the owner in the house nobody counts anything.
	r.join(ada, home(7))
	drain(ada)
	r.do(guests[0], map[string]any{"t": "leave"})
	r.join(guests[0], house(7))
	if got := kinds(); got != "" {
		t.Fatalf("owner out: %s", got)
	}
}
