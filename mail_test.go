package main

import (
	"testing"
	"time"
)

// mailRig is a hub with only the mail nudge and a block list, a manual clock and timers.
type mailRig struct {
	*homesRig
	mail *mailNudge
}

func newMailRig(t *testing.T) *mailRig {
	r := &mailRig{homesRig: &homesRig{t: t, now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}}}
	savedSecret := homesSecretForTest
	homesSecretForTest = homesTestSecret
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.mail = newMailNudge(h).(*mailNudge); return r.mail },
		func(h *Hub) Module { return r.ign },
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	homesSecretForTest = savedSecret
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

func (r *mailRig) proof(from, to int64, id string, at time.Time) string {
	return signBuddy(homesTestSecret, map[string]any{"k": "mail", "sub": from, "to": to, "n": id, "at": at.Unix(), "iat": r.now.Unix(), "exp": r.now.Add(2 * time.Minute).Unix()})
}

func TestMailNudgeArrivesWhenTheLetterDoes(t *testing.T) {
	r := newMailRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.do(ada, map[string]any{"t": "mail_tell", "token": r.proof(7, 9, "00000000000000a1", r.now.Add(420*time.Second))})
	if f := drain(bo); len(f) != 0 {
		t.Fatalf("a parcel is not told before it arrives: %v", f)
	}
	if n := r.fire(420 * time.Second); n != 1 {
		t.Fatalf("one arrival scheduled at 420 s, fired %d", n)
	}
	f := drain(bo)
	if len(f) != 1 || f[0]["t"] != "mail_arrived" || f[0]["id"] != "00000000000000a1" {
		t.Fatalf("the receiver hears: %v", f)
	}
	if from, _ := f[0]["from"].(map[string]any); from["name"] != "Ada" {
		t.Fatalf("from Ada: %v", f[0])
	}
	if f := drain(ada); len(f) != 0 {
		t.Fatalf("the sender hears nothing back: %v", f)
	}
}

func TestMailNudgeWordsAloneArriveAtOnceAndOnlyOnce(t *testing.T) {
	r := newMailRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	tok := r.proof(7, 9, "00000000000000b2", r.now)
	r.do(ada, map[string]any{"t": "mail_tell", "token": tok})
	if got := buddyKinds(drain(bo)); got != "mail_arrived" {
		t.Fatalf("a letter of words arrives at once: %s", got)
	}
	r.do(ada, map[string]any{"t": "mail_tell", "token": tok})
	if f := drain(bo); len(f) != 0 {
		t.Fatalf("a proof is good once: %v", f)
	}
}

func TestMailNudgeRefusesAnotherSendersProofAndBlocks(t *testing.T) {
	r := newMailRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	cy := r.connect(11, "Cy")
	r.do(cy, map[string]any{"t": "mail_tell", "token": r.proof(7, 9, "00000000000000c3", r.now)})
	if got := buddyKinds(drain(cy)); got != "error:mail" {
		t.Fatalf("only the sender's socket hands it over: %s", got)
	}
	if f := drain(bo); len(f) != 0 {
		t.Fatalf("nothing reaches Bo: %v", f)
	}
	r.do(ada, map[string]any{"t": "mail_tell", "token": "nonsense"})
	if got := buddyKinds(drain(ada)); got != "error:mail" {
		t.Fatalf("a bad proof: %s", got)
	}
	r.ign.ign[[2]int64{9, 7}] = true
	r.do(ada, map[string]any{"t": "mail_tell", "token": r.proof(7, 9, "00000000000000d4", r.now)})
	if f := drain(bo); len(f) != 0 {
		t.Fatalf("a player who blocks the sender hears nothing: %v", f)
	}
	r.do(ada, map[string]any{"t": "mail_tell", "token": r.proof(7, 9, "00000000000000e5", r.now.Add(time.Hour))})
	if got := buddyKinds(drain(ada)); got != "error:mail" {
		t.Fatalf("an arrival an hour ahead is not a letter the site sent: %s", got)
	}
}

func TestMailNudgeToSomeoneOfflineIsDropped(t *testing.T) {
	r := newMailRig(t)
	ada := r.connect(7, "Ada")
	r.do(ada, map[string]any{"t": "mail_tell", "token": r.proof(7, 9, "00000000000000f6", r.now.Add(420*time.Second))})
	if n := r.fire(420 * time.Second); n != 1 {
		t.Fatalf("still scheduled: %d", n)
	}
	if f := drain(ada); len(f) != 0 {
		t.Fatalf("nothing to tell anyone: %v", f)
	}
}
