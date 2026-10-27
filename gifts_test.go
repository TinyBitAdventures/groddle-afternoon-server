package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fakeGiftSite stands in for the site's groddle/v1/ledger/gift.
type fakeGiftSite struct {
	calls  []giftCall
	answer giftAnswer
}

func (f *fakeGiftSite) gift(c giftCall) giftAnswer {
	f.calls = append(f.calls, c)
	return f.answer
}

type giftRig struct {
	t       *testing.T
	hub     *Hub
	m       *gifts
	ign     *fakeIgnores
	site    *fakeGiftSite
	now     time.Time
	timers  []*rigTimer
	pending []func()
}

func newGiftRig(t *testing.T) *giftRig {
	r := &giftRig{t: t, now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}, site: &fakeGiftSite{answer: giftAnswer{Status: "settled", Level: 4}}}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newGifts(h).(*gifts); return r.m },
		func(h *Hub) Module { return r.ign },
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	r.m.site = r.site
	r.m.spawn = func(fn func()) { r.pending = append(r.pending, fn) }
	n := 0
	r.m.newID = func() string { n++; return strings.Repeat("0", 15) + string(rune('0'+n)) }
	return r
}

func (r *giftRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

// do sends one frame a second later than the last (the rate limit is its own test).
func (r *giftRig) do(cl *Client, msg map[string]any) {
	r.now = r.now.Add(time.Second)
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *giftRig) join(cl *Client, street string) {
	r.do(cl, map[string]any{"t": "join", "street": street})
}

// settle runs the site calls waiting, then their answers under the hub lock.
func (r *giftRig) settle() {
	for len(r.pending) > 0 {
		fn := r.pending[0]
		r.pending = r.pending[1:]
		fn()
	}
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if tm.d == 0 && !tm.stopped {
			tm.stopped = true
			tm.fn()
		}
	}
}

// together puts Ada (7) and Bo (9) on one street.
func (r *giftRig) together() (ada, bo *Client) {
	ada, bo = r.connect(7, "Ada"), r.connect(9, "Bo")
	r.join(ada, "LA5101HF7F429V")
	r.join(bo, "LA5101HF7F429V")
	drain(ada)
	drain(bo)
	return ada, bo
}

func TestAGiveSettlesAtTheSiteThenBothPlayersHear(t *testing.T) {
	r := newGiftRig(t)
	ada, bo := r.together()
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "ref": "s1", "item": map[string]any{"c": "cherry", "n": 3}})
	if got := append(drain(ada), drain(bo)...); len(got) != 0 {
		t.Fatalf("nothing until the site answers: %v", got)
	}
	r.settle()
	if len(r.site.calls) != 1 {
		t.Fatalf("one call: %v", r.site.calls)
	}
	c := r.site.calls[0]
	if c.Op != "gift-0000000000000001" || c.From != 7 || c.To != 9 || c.Box || c.Note != nil || len(c.Gave.Items) != 1 || c.Gave.Items[0].C != "cherry" || c.Gave.Items[0].N != 3 || c.Gave.Currants != 0 {
		t.Fatalf("the call: %+v", c)
	}
	g := only(t, drain(ada), "gift_given")
	if g["status"] != "settled" || g["op"] != "gift-0000000000000001" || g["ref"] != "s1" || g["level"] != float64(4) || g["to"].(map[string]any)["name"] != "Bo" || g["item"].(map[string]any)["c"] != "cherry" {
		t.Fatalf("Ada is told: %v", g)
	}
	got := only(t, drain(bo), "gift_got")
	if got["op"] != "gift-0000000000000001" || got["from"].(map[string]any)["name"] != "Ada" || got["item"].(map[string]any)["n"] != float64(3) || got["box"] != nil || got["note"] != nil {
		t.Fatalf("Bo is told: %v", got)
	}
}

func TestGiveFollowsTheSourcesRulesForWhoMayGetIt(t *testing.T) {
	r := newGiftRig(t)
	ada, bo := r.together()
	cy := r.connect(11, "Cy")
	r.join(cy, "LIF102FDNU11314")
	drain(cy)
	cases := []struct {
		to   int64
		why  string
		text string
	}{
		{7, "self", "You can't do that to yourself."},
		{12, "offline", "That player has gone offline."},
		{11, "away", "You can't do that to a player that isn't there!"},
	}
	for _, c := range cases {
		r.do(ada, map[string]any{"t": "gift_give", "to": c.to, "item": map[string]any{"c": "cherry", "n": 1}})
		f := only(t, drain(ada), "gift_given")
		if f["status"] != "refused" || f["why"] != c.why || f["text"] != c.text {
			t.Fatalf("to %d: %v", c.to, f)
		}
	}
	// takeable.js 258-264: blocked either way.
	r.ign.ign[[2]int64{9, 7}] = true
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "cherry", "n": 1}})
	if f := only(t, drain(ada), "gift_given"); f["why"] != "blocked" || f["text"] != "You are blocked by that player and cannot give them anything." {
		t.Fatalf("blocked by Bo: %v", f)
	}
	r.ign.ign = map[[2]int64]bool{{7, 9}: true}
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "cherry", "n": 1}})
	if f := only(t, drain(ada), "gift_given"); f["why"] != "blocking" || f["text"] != "You are blocking that player and cannot give them anything." {
		t.Fatalf("blocking Bo: %v", f)
	}
	// A player off every street (a leave) gives nothing.
	r.ign.ign = map[[2]int64]bool{}
	r.do(bo, map[string]any{"t": "leave"})
	drain(ada)
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "cherry", "n": 1}})
	if f := only(t, drain(ada), "gift_given"); f["why"] != "away" {
		t.Fatalf("Bo left the street: %v", f)
	}
	if len(r.site.calls) != 0 || len(r.pending) != 0 {
		t.Fatalf("the site is never asked: %v", r.site.calls)
	}
}

func TestGiveOnlyWhatTheLedgerCarries(t *testing.T) {
	r := newGiftRig(t)
	ada, _ := r.together()
	for _, c := range []struct {
		item map[string]any
		text string
	}{
		{map[string]any{"c": "bag_gift_box_wrapped", "n": 1}, "That can't be given to another player."},
		{map[string]any{"c": "no_such_thing", "n": 1}, "That can't be given to another player."},
		{map[string]any{"c": "cherry", "n": 0}, "You don't have enough of that item."},
		{map[string]any{"c": "cherry", "n": 100000}, "You don't have enough of that item."},
	} {
		r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": c.item})
		if f := only(t, drain(ada), "gift_given"); f["status"] != "refused" || f["text"] != c.text {
			t.Fatalf("%v: %v", c.item, f)
		}
	}
	// Soulbound classes say so (takeable.js:222).
	for c, why := range r.m.table.Untradeable {
		if why != "soulbound" {
			continue
		}
		r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": c, "n": 1}})
		if f := only(t, drain(ada), "gift_given"); f["text"] != "This item is locked to you and can't be given to another player." {
			t.Fatalf("soulbound %s: %v", c, f)
		}
		break
	}
	// Neither both nor none of item and box.
	r.do(ada, map[string]any{"t": "gift_give", "to": 9})
	if f := only(t, drain(ada), "gift_given"); f["why"] != "bad" {
		t.Fatalf("nothing given: %v", f)
	}
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "cherry", "n": 1}, "box": []any{map[string]any{"c": "cherry", "n": 1}}})
	if f := only(t, drain(ada), "gift_given"); f["why"] != "bad" {
		t.Fatalf("both: %v", f)
	}
	if len(r.site.calls) != 0 {
		t.Fatalf("never asked: %v", r.site.calls)
	}
}

func TestANoteTravelsWithItsWriting(t *testing.T) {
	r := newGiftRig(t)
	ada, bo := r.together()
	long := strings.Repeat("ab", 600)
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "note", "n": 1, "r": 3}, "note": map[string]any{"title": " Hi\n<b>Bo</b> ", "body": "line one\nline\u0000 two\n" + long}})
	r.settle()
	c := r.site.calls[0]
	if c.Gave.Items[0].N != 1 || c.Gave.Items[0].R != nil || c.Note == nil || c.Note.Title != "Hi bBo/b" {
		t.Fatalf("one note, its title one line with no tags: %+v %+v", c.Gave, c.Note)
	}
	if !strings.HasPrefix(c.Note.Body, "line one\nline two\n") || len([]rune(c.Note.Body)) != 1000 {
		t.Fatalf("the body keeps its lines, at most 1000 characters (note.js:247): %q", c.Note.Body[:30])
	}
	if g := only(t, drain(ada), "gift_given"); g["note"] != true {
		t.Fatalf("Ada: %v", g)
	}
	got := only(t, drain(bo), "gift_got")
	if got["note"].(map[string]any)["title"] != "Hi bBo/b" {
		t.Fatalf("Bo reads it: %v", got)
	}
	// A note field on anything but a note is refused.
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "cherry", "n": 1}, "note": map[string]any{"title": "x"}})
	if f := only(t, drain(ada), "gift_given"); f["why"] != "bad" {
		t.Fatalf("a note on cherries: %v", f)
	}
}

func TestAWrappedGiftBoxGoesWholeWithTheBox(t *testing.T) {
	r := newGiftRig(t)
	ada, bo := r.together()
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "ref": "b1", "box": []any{map[string]any{"c": "cherry", "n": 3}, map[string]any{"c": "pick", "n": 1, "r": 999999}}})
	r.settle()
	c := r.site.calls[0]
	if !c.Box || c.Ref != "b1" || len(c.Gave.Items) != 3 || c.Gave.Items[0].C != "bag_gift_box" || c.Gave.Items[0].N != 1 || c.Gave.Items[1].C != "cherry" || c.Gave.Items[2].C != "pick" {
		t.Fatalf("the box and what is in it: %+v", c)
	}
	if p := c.Gave.Items[2].R; p == nil || *p != r.m.table.Items["pick"].Pts {
		t.Fatalf("a tool's points are clamped to its capacity: %v", c.Gave.Items[2].R)
	}
	g := only(t, drain(ada), "gift_given")
	if box := g["box"].([]any); len(box) != 2 || g["item"] != nil {
		t.Fatalf("Ada hears the box went: %v", g)
	}
	got := only(t, drain(bo), "gift_got")
	if box := got["box"].([]any); len(box) != 2 || box[0].(map[string]any)["c"] != "cherry" {
		t.Fatalf("Bo gets the box: %v", got)
	}
	// bag_gift_box.js canContain 666-672: three stacks at most, no bags, elements or furniture, one stack per class.
	for _, box := range [][]any{
		{},
		{map[string]any{"c": "cherry", "n": 1}, map[string]any{"c": "apple", "n": 1}, map[string]any{"c": "plank", "n": 1}, map[string]any{"c": "grain", "n": 1}},
		{map[string]any{"c": "bag_gift_box", "n": 1}},
		{map[string]any{"c": "element_red", "n": 1}},
		{map[string]any{"c": "furniture_chair", "n": 1}},
		{map[string]any{"c": "cherry", "n": 1}, map[string]any{"c": "cherry", "n": 2}},
	} {
		r.do(ada, map[string]any{"t": "gift_give", "to": 9, "box": box})
		if f := only(t, drain(ada), "gift_given"); f["status"] != "refused" || f["why"] != "box" {
			t.Fatalf("%v: %v", box, f)
		}
	}
	if len(r.site.calls) != 1 {
		t.Fatalf("only the good box was asked: %d", len(r.site.calls))
	}
}

func TestTheSitesRefusalAndSilenceComeBackAsTheGiversWords(t *testing.T) {
	r := newGiftRig(t)
	ada, bo := r.together()
	r.site.answer = giftAnswer{Status: "refused", Why: "short", Text: "You don't have enough of that item."}
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "cherry", "n": 5}})
	// One at a time: a second give waits for the first.
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "apple", "n": 1}})
	if f := only(t, drain(ada), "gift_given"); f["why"] != "busy" {
		t.Fatalf("busy: %v", f)
	}
	r.settle()
	if f := only(t, drain(ada), "gift_given"); f["status"] != "refused" || f["text"] != "You don't have enough of that item." || f["op"] != "gift-0000000000000001" {
		t.Fatalf("refused: %v", f)
	}
	if got := drain(bo); len(got) != 0 {
		t.Fatalf("Bo hears nothing of a refused give: %v", got)
	}
	r.site.answer = giftAnswer{}
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "apple", "n": 1}})
	r.settle()
	if f := only(t, drain(ada), "gift_given"); f["status"] != "refused" || f["why"] != "off" {
		t.Fatalf("no answer: %v", f)
	}
	if len(drain(bo)) != 0 {
		t.Fatal("Bo hears nothing")
	}
	// Without a ledger nothing can be given.
	r.m.site = nil
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "apple", "n": 1}})
	if f := only(t, drain(ada), "gift_given"); f["why"] != "off" || f["text"] != "You can't give things right now." {
		t.Fatalf("off: %v", f)
	}
}

func TestAReceiverWhoLeftStillGetsAGiftTheSiteSettled(t *testing.T) {
	r := newGiftRig(t)
	ada, bo := r.together()
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "cherry", "n": 1}})
	r.join(bo, "LIF102FDNU11314")
	drain(bo)
	drain(ada)
	r.settle()
	only(t, drain(ada), "gift_given")
	only(t, drain(bo), "gift_got")
	// The giver gone before the answer: the receiver still hears it; the giver's game applies it on connect.
	r.join(bo, "LA5101HF7F429V")
	drain(bo)
	drain(ada)
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "cherry", "n": 1}})
	r.hub.Disconnect(ada)
	drain(bo)
	r.settle()
	only(t, drain(bo), "gift_got")
	if _, busy := r.m.busy[7]; busy {
		t.Fatal("the giver's slot is free")
	}
}

func TestGivingIsRateLimited(t *testing.T) {
	r := newGiftRig(t)
	ada, _ := r.together()
	r.m.site = nil
	frame := []byte(`{"t":"gift_give","to":9,"item":{"c":"cherry","n":1}}`)
	for i := 0; i < giftBurst; i++ {
		r.hub.Handle(ada, frame)
	}
	drain(ada)
	r.hub.Handle(ada, frame)
	if f := only(t, drain(ada), "error"); f["code"] != "slow_down" {
		t.Fatalf("slow down: %v", f)
	}
}

func TestBadRefsAreDropped(t *testing.T) {
	r := newGiftRig(t)
	ada, _ := r.together()
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "ref": "<script>", "item": map[string]any{"c": "cherry", "n": 1}})
	r.settle()
	if r.site.calls[0].Ref != "" {
		t.Fatalf("ref: %q", r.site.calls[0].Ref)
	}
	if g := only(t, drain(ada), "gift_given"); g["ref"] != "" {
		t.Fatalf("ref back: %v", g)
	}
}

func TestCleanNoteText(t *testing.T) {
	if got := cleanNoteText("a\tb\r\nc\x07d", 100, false); got != "a b  cd" {
		t.Fatalf("title: %q", got)
	}
	if got := cleanNoteText("a\nb", 100, true); got != "a\nb" {
		t.Fatalf("body: %q", got)
	}
	if got := cleanNoteText("\xffé"+strings.Repeat("x", 10), 5, false); got != "éxxxx" {
		t.Fatalf("cut: %q", got)
	}
}
