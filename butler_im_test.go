package main

import (
	"testing"
	"time"
)

// butlerRig is the homes rig with the Butler's IMs and a block list.
type butlerRig struct {
	*homesRig
	im *butlerIM
}

func newButlerRig(t *testing.T) *butlerRig {
	r := &butlerRig{homesRig: &homesRig{t: t, now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}}}
	savedSecret := homesSecretForTest
	homesSecretForTest = homesTestSecret
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newHomes(h).(*homes); return r.m },
		func(h *Hub) Module { r.im = newButlerIM(h).(*butlerIM); return r.im },
		func(h *Hub) Module { return r.ign },
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	homesSecretForTest = savedSecret
	return r
}

// awayWithVisitor: Ada (7) has a Butler and stands on another street; Bo (9) comes onto her home street.
func (r *butlerRig) awayWithVisitor() (ada, bo *Client) {
	ada, bo = r.connect(7, "Ada"), r.connect(9, "Bo")
	r.do(ada, map[string]any{"t": "butler_watch", "on": true, "notify": true, "listen": true})
	r.join(ada, "GA5101HF7F429V5")
	r.pass(bo, 7, "Ada", false)
	r.join(bo, home(7))
	return ada, bo
}

func TestButlerTellsAnAwayOwnerWhoCameAndWhatTheySaid(t *testing.T) {
	r := newButlerRig(t)
	ada, bo := r.awayWithVisitor()
	got := kinds(drain(ada), "butler_im")
	if len(got) != 1 || got[0]["kind"] != "visitor" || got[0]["from"].(map[string]any)["name"] != "Bo" {
		t.Fatalf("visitorNotifyOwnerHere: %v", got)
	}
	r.do(bo, map[string]any{"t": "chat", "text": "anyone home?"})
	got = kinds(drain(ada), "butler_im")
	if len(got) != 1 || got[0]["kind"] != "speech" || got[0]["text"] != "anyone home?" {
		t.Fatalf("visitorSpeechReport: %v", got)
	}
	if s := kinds(drain(bo), "butler_im"); len(s) != 0 {
		t.Fatalf("the visitor hears no IM: %v", s)
	}
}

func TestButlerKeepsQuietWhenTheOwnerIsHomeOrHasNoButler(t *testing.T) {
	r := newButlerRig(t)
	ada, bo := r.awayWithVisitor()
	drain(ada)
	// The owner comes home: they hear it themselves.
	r.join(ada, home(7))
	drain(ada)
	r.do(bo, map[string]any{"t": "chat", "text": "oh hi"})
	if got := kinds(drain(ada), "butler_im"); len(got) != 0 {
		t.Fatalf("owner on the home street: %v", got)
	}
	// In the house counts as away (ownerInsideHouse).
	r.join(ada, house(7))
	drain(ada)
	r.do(bo, map[string]any{"t": "chat", "text": "knock knock"})
	if got := kinds(drain(ada), "butler_im"); len(got) != 1 || got[0]["kind"] != "speech" {
		t.Fatalf("owner in the house: %v", got)
	}
	// No Butler: nothing.
	r.do(ada, map[string]any{"t": "butler_watch", "on": false})
	r.do(bo, map[string]any{"t": "chat", "text": "hello?"})
	if got := kinds(drain(ada), "butler_im"); len(got) != 0 {
		t.Fatalf("no Butler: %v", got)
	}
}

func TestButlerQuitAndStopEavesdropping(t *testing.T) {
	r := newButlerRig(t)
	ada, bo := r.awayWithVisitor()
	drain(ada)
	// Stop telling me things visitors say: arrivals still come, speech does not.
	r.do(ada, map[string]any{"t": "butler_watch", "on": true, "notify": true, "listen": false})
	r.do(bo, map[string]any{"t": "chat", "text": "secret"})
	if got := kinds(drain(ada), "butler_im"); len(got) != 0 {
		t.Fatalf("eavesdropping off: %v", got)
	}
	// quit: nothing at all.
	r.do(ada, map[string]any{"t": "butler_watch", "on": true, "notify": false, "listen": true})
	r.join(bo, "GA5101HF7F429V5")
	r.join(bo, home(7))
	r.do(bo, map[string]any{"t": "chat", "text": "back"})
	if got := kinds(drain(ada), "butler_im"); len(got) != 0 {
		t.Fatalf("notifications off: %v", got)
	}
}

func TestButlerIgnoresABlockedVisitor(t *testing.T) {
	r := newButlerRig(t)
	ada, bo := r.awayWithVisitor()
	drain(ada)
	r.ign.ign[[2]int64{9, 7}] = true // Bo blocks Ada
	r.do(bo, map[string]any{"t": "chat", "text": "hi"})
	if got := kinds(drain(ada), "butler_im"); len(got) != 0 {
		t.Fatalf("blocked: %v", got)
	}
	r.do(ada, map[string]any{"t": "butler_announce", "text": "hello all"})
	if got := kinds(drain(bo), "butler_said"); len(got) != 0 {
		t.Fatalf("a visitor blocking the owner hears no Butler: %v", got)
	}
	r.do(ada, map[string]any{"t": "butler_tell", "text": "hi"})
	if got := kinds(drain(ada), "butler_im"); len(got) < 2 || got[len(got)-1]["kind"] != "tell_fail" {
		t.Fatalf("tell to a blocked visitor fails: %v", got)
	}
}

func TestButlerAnnouncesTellsAndSaysWho(t *testing.T) {
	r := newButlerRig(t)
	ada, bo := r.awayWithVisitor()
	cy := r.connect(11, "Cy")
	r.pass(cy, 7, "Ada", false)
	r.join(cy, home(7))
	drain(ada)
	drain(bo)
	drain(cy)

	r.do(ada, map[string]any{"t": "butler_announce", "text": "  Back   in five!  "})
	for _, cl := range []*Client{bo, cy} {
		got := kinds(drain(cl), "butler_said")
		if len(got) != 1 || got[0]["text"] != "Back in five!" || got[0]["owner"] != float64(7) {
			t.Fatalf("announceMessage to %s: %v", cl.Name(), got)
		}
	}
	got := kinds(drain(ada), "butler_im")
	if len(got) != 1 || got[0]["kind"] != "announced" || got[0]["text"] != "Back in five!" {
		t.Fatalf("confirmAnnounce: %v", got)
	}

	// tell: Cy came last, so the Butler tells Cy.
	r.do(ada, map[string]any{"t": "butler_tell", "text": "the door is open"})
	said := kinds(drain(bo), "butler_said")
	want := "Cy, Ada just told me to say 'the door is open' to you."
	if len(said) != 1 || said[0]["text"] != want || said[0]["to"].(map[string]any)["name"] != "Cy" {
		t.Fatalf("tellMessage: %v", said)
	}
	if got := kinds(drain(ada), "butler_im"); len(got) != 1 || got[0]["kind"] != "told" || got[0]["text"] != want {
		t.Fatalf("confirmTell: %v", got)
	}

	// who
	r.do(ada, map[string]any{"t": "butler_who"})
	got = kinds(drain(ada), "butler_im")
	if len(got) != 1 || got[0]["kind"] != "who" {
		t.Fatalf("who: %v", got)
	}
	if names := got[0]["names"].([]any); len(names) != 2 || names[0] != "Bo" || names[1] != "Cy" {
		t.Fatalf("who names: %v", names)
	}

	// Cy leaves: the most recent visitor is forgotten (onPlayerExit), so tell fails.
	r.join(cy, "GA5101HF7F429V5")
	r.do(ada, map[string]any{"t": "butler_tell", "text": "bye"})
	if got := kinds(drain(ada), "butler_im"); len(got) != 1 || got[0]["kind"] != "tell_fail" {
		t.Fatalf("farCommandTellFail: %v", got)
	}
	// Empty: nothing said.
	r.do(ada, map[string]any{"t": "butler_announce", "text": "   "})
	if got := drain(bo); len(kinds(got, "butler_said")) != 0 {
		t.Fatalf("empty announce: %v", got)
	}
}

func TestButlerCommandsNeedAButler(t *testing.T) {
	r := newButlerRig(t)
	ada := r.connect(7, "Ada")
	r.do(ada, map[string]any{"t": "butler_announce", "text": "hi"})
	r.do(ada, map[string]any{"t": "butler_who"})
	got := kinds(drain(ada), "butler_im")
	if len(got) != 2 || got[0]["kind"] != "no_butler" || got[1]["kind"] != "no_butler" {
		t.Fatalf("no Butler: %v", got)
	}
}

func TestButlerCommandsAreRateLimited(t *testing.T) {
	r := newButlerRig(t)
	ada, bo := r.awayWithVisitor()
	drain(ada)
	drain(bo)
	for i := 0; i < butlerSayBurst+2; i++ {
		r.do(ada, map[string]any{"t": "butler_announce", "text": "spam"})
	}
	if got := kinds(drain(bo), "butler_said"); len(got) != butlerSayBurst {
		t.Fatalf("burst %d, got %d", butlerSayBurst, len(got))
	}
	errs := kinds(drain(ada), "error")
	if len(errs) != 2 || errs[0]["code"] != "slow_down" {
		t.Fatalf("slow down: %v", errs)
	}
}

func TestTypingMarkReachesTheStreetButNotTheTyper(t *testing.T) {
	r := newButlerRig(t)
	ada, bo, cy := r.connect(7, "Ada"), r.connect(9, "Bo"), r.connect(11, "Cy")
	for _, cl := range []*Client{ada, bo} {
		r.join(cl, "GA5101HF7F429V5")
	}
	r.join(cy, "GIQ1234567890AB")
	drain(ada)
	drain(bo)
	drain(cy)
	r.do(ada, map[string]any{"t": "typing", "text": "never sent"})
	got := kinds(drain(bo), "typing")
	if len(got) != 1 || got[0]["id"] != float64(7) || len(got[0]) != 2 {
		t.Fatalf("typing to the street, no text: %v", got)
	}
	if len(kinds(drain(ada), "typing")) != 0 || len(kinds(drain(cy), "typing")) != 0 {
		t.Fatal("not the typer, not another street")
	}
	// At most one each typingEvery.
	r.do(ada, map[string]any{"t": "typing"})
	if len(kinds(drain(bo), "typing")) != 0 {
		t.Fatal("throttled")
	}
	r.now = r.now.Add(typingEvery)
	r.do(ada, map[string]any{"t": "typing"})
	if len(kinds(drain(bo), "typing")) != 1 {
		t.Fatal("again after typingEvery")
	}
	// A block either way: no mark.
	r.ign.ign[[2]int64{9, 7}] = true
	r.now = r.now.Add(typingEvery)
	r.do(ada, map[string]any{"t": "typing"})
	if len(kinds(drain(bo), "typing")) != 0 {
		t.Fatal("blocked")
	}
	// Off a street: nothing, and no error.
	r.do(cy, map[string]any{"t": "leave"})
	drain(cy)
	r.do(cy, map[string]any{"t": "typing"})
	if got := drain(cy); len(got) != 0 {
		t.Fatalf("no street: %v", got)
	}
}
