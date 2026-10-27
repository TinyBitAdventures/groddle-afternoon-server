package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

var homesTestSecret = []byte("homes-test-secret-homes-test-secret-homes")

// fakeIgnores stands in for buddies.go's buddies.ignoring hook.
type fakeIgnores struct{ ign map[[2]int64]bool }

func (f *fakeIgnores) Register(r *Registry) {
	r.Provide("buddies.ignoring", func(ignorer, other int64) bool { return f.ign[[2]int64{ignorer, other}] })
}

// homesRig is a hub with only the homes module (and a block list), a manual clock and timers.
type homesRig struct {
	t      *testing.T
	hub    *Hub
	m      *homes
	ign    *fakeIgnores
	now    time.Time
	timers []*rigTimer
	n      int
}

func newHomesRig(t *testing.T) *homesRig {
	r := &homesRig{t: t, now: time.Unix(1_700_000_000, 0), ign: &fakeIgnores{ign: map[[2]int64]bool{}}}
	savedSecret := homesSecretForTest
	homesSecretForTest = homesTestSecret
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newHomes(h).(*homes); return r.m },
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

func (r *homesRig) fire(d time.Duration) int {
	n := 0
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if tm.d == d && !tm.stopped {
			tm.stopped = true
			tm.fn()
			n++
		}
	}
	return n
}

func (r *homesRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

func (r *homesRig) do(cl *Client, msg map[string]any) {
	(&buddyRig{hub: r.hub}).do(cl, msg)
}

func (r *homesRig) join(cl *Client, street string) {
	r.do(cl, map[string]any{"t": "join", "street": street})
}

func (r *homesRig) passToken(visitor, owner int64, name string, key bool) string {
	k := 0
	if key {
		k = 1
	}
	return signBuddy(homesTestSecret, map[string]any{"k": "home", "sub": visitor, "o": owner, "name": name, "key": k, "iat": r.now.Unix(), "exp": r.now.Add(30 * time.Minute).Unix()})
}

func (r *homesRig) pass(cl *Client, owner int64, name string, key bool) {
	r.do(cl, map[string]any{"t": "home_pass", "token": r.passToken(cl.ID(), owner, name, key)})
}

func (r *homesRig) keyProof(owner, holder int64, on bool) string {
	r.n++
	o := 0
	if on {
		o = 1
	}
	return signBuddy(homesTestSecret, map[string]any{"k": "house_key", "sub": owner, "to": holder, "on": o, "iat": r.now.Unix(), "exp": r.now.Add(time.Minute).Unix(), "n": "n" + strconv.Itoa(r.n)})
}

func home(id int64) string  { return "HOME_" + strconv.FormatInt(id, 10) }
func house(id int64) string { return "HOUSE_" + strconv.FormatInt(id, 10) }

func TestHomeOwnerReadsOnlyHomeStreets(t *testing.T) {
	for street, want := range map[string]int64{"HOME_7": 7, "HOUSE_12": 12, "HOME_0": 0, "HOME_07": 0, "HOME_x": 0, "GA5101HF7F429V5": 0, "HOMES_7": 0, "HOUSE_-3": 0} {
		if got, _ := homeOwner(street); got != want {
			t.Errorf("%s: owner %d, want %d", street, got, want)
		}
	}
	if _, inside := homeOwner("HOUSE_3"); !inside {
		t.Fatal("HOUSE_ is the house")
	}
}

func TestOnlyAPassLetsAVisitorOntoAHomeStreet(t *testing.T) {
	r := newHomesRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.join(ada, home(7))
	if f := drain(ada); f[0]["t"] != "street" {
		t.Fatalf("an owner walks onto their own street: %v", f)
	}
	r.join(bo, home(7))
	if got := buddyKinds(drain(bo)); got != "error:home_private" {
		t.Fatalf("no pass: %s", got)
	}
	r.pass(bo, 7, "Ada", false)
	if f := only(t, drain(bo), "home_pass_ok"); f["owner"] != float64(7) || f["key"] != false {
		t.Fatalf("pass: %v", f)
	}
	r.join(bo, home(7))
	if f := drain(bo); f[0]["t"] != "street" || len(f[0]["players"].([]any)) != 1 {
		t.Fatalf("with a pass Bo stands with Ada: %v", f)
	}
	if got := buddyKinds(drain(ada)); got != "joined" {
		t.Fatalf("Ada sees Bo: %s", got)
	}
	// A pass for one home is no pass for another.
	r.join(bo, home(8))
	if got := buddyKinds(drain(bo)); got != "error:home_private" {
		t.Fatalf("another home: %s", got)
	}
}

func TestForgedForeignOrOldPassesAreRefused(t *testing.T) {
	r := newHomesRig(t)
	bo := r.connect(9, "Bo")
	bad := []string{
		"nope",
		r.passToken(10, 7, "Ada", true), // someone else's
		signBuddy([]byte("another-secret-another-secret-another"), map[string]any{"k": "home", "sub": 9, "o": 7, "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()}),
		signBuddy(homesTestSecret, map[string]any{"k": "buddies", "sub": 9, "o": 7, "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()}),
		signBuddy(homesTestSecret, map[string]any{"k": "home", "sub": 9, "o": 7, "iat": r.now.Unix(), "exp": r.now.Add(-time.Second).Unix()}),
	}
	for i, tok := range bad {
		r.do(bo, map[string]any{"t": "home_pass", "token": tok})
		if got := buddyKinds(drain(bo)); got != "error:homes" {
			t.Fatalf("token %d: %s", i, got)
		}
	}
	// A pass that says a year is good for an hour here.
	r.do(bo, map[string]any{"t": "home_pass", "token": signBuddy(homesTestSecret, map[string]any{"k": "home", "sub": 9, "o": 7, "iat": r.now.Unix(), "exp": r.now.Add(365 * 24 * time.Hour).Unix()})})
	drain(bo)
	r.now = r.now.Add(homePassLife + time.Second)
	r.join(bo, home(7))
	if got := buddyKinds(drain(bo)); got != "error:home_private" {
		t.Fatalf("an old pass: %s", got)
	}
}

func TestABlockKeepsTheVisitorOut(t *testing.T) {
	r := newHomesRig(t)
	bo := r.connect(9, "Bo")
	r.pass(bo, 7, "Ada", true)
	drain(bo)
	r.ign.ign[[2]int64{7, 9}] = true
	r.join(bo, home(7))
	if got := buddyKinds(drain(bo)); got != "error:home_blocked" {
		t.Fatalf("blocked since the pass: %s", got)
	}
}

func TestTheHouseNeedsAKeyOrTheOwnersWord(t *testing.T) {
	r := newHomesRig(t)
	ada, bo, cy := r.connect(7, "Ada"), r.connect(9, "Bo"), r.connect(11, "Cy")
	r.join(ada, house(7))
	drain(ada)
	r.pass(bo, 7, "Ada", false)
	r.pass(cy, 7, "Ada", true)
	drain(bo)
	drain(cy)
	r.join(bo, house(7))
	if got := buddyKinds(drain(bo)); got != "error:home_locked" {
		t.Fatalf("no key: %s", got)
	}
	r.join(cy, house(7))
	if f := drain(cy); f[0]["t"] != "street" {
		t.Fatalf("a key opens the door: %v", f)
	}
	frames := drain(ada)
	if got := buddyKinds(frames); got != "joined,home_entered" {
		t.Fatalf("Ada hears Cy come in: %s", got)
	}
	if frames[1]["text"] != "Cy just entered your house." {
		t.Fatalf("copy: %v", frames[1])
	}
}

func TestKnockingAnsweredYes(t *testing.T) {
	r := newHomesRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.pass(bo, 7, "Ada", false)
	drain(bo)
	// Ada is out in the world: not at home.
	r.join(ada, "GA5101HF7F429V5")
	drain(ada)
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	if f := only(t, drain(bo), "home_refused"); f["why"] != "away" || f["text"] != "The owner of this location (Ada) is not at home right now" {
		t.Fatalf("away: %v", f)
	}
	r.join(ada, home(7))
	drain(ada)
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	if f := only(t, drain(bo), "home_knock_sent"); f["text"] != "Ok! We'll let you know if they answer the door" {
		t.Fatalf("sent: %v", f)
	}
	if f := only(t, drain(ada), "home_knocked"); f["id"] != float64(9) || f["text"] != "Bo is knocking on your door and would like to enter your house..." {
		t.Fatalf("knocked: %v", f)
	}
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	if f := only(t, drain(bo), "home_refused"); f["why"] != "asked" || f["text"] != "You've already asked Ada if you can enter - be patient!" {
		t.Fatalf("asked twice: %v", f)
	}
	// Only someone knocking can be let in.
	r.do(ada, map[string]any{"t": "home_answer", "id": 11, "allow": true})
	r.do(ada, map[string]any{"t": "home_answer", "id": 9, "allow": true})
	if f := only(t, drain(bo), "home_let_in"); f["text"] != "Ada has let you into their house." {
		t.Fatalf("let in: %v", f)
	}
	if r.fire(homeKnockWait) != 0 {
		t.Fatal("an answered knock stops waiting")
	}
	r.join(bo, house(7))
	if f := drain(bo); f[0]["t"] != "street" {
		t.Fatalf("in: %v", f)
	}
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	if f := only(t, drain(bo), "home_open"); f["why"] != "let_in" {
		t.Fatalf("let in already: %v", f)
	}
	// Nobody else came in on Bo's word.
	cy := r.connect(11, "Cy")
	r.pass(cy, 7, "Ada", false)
	drain(cy)
	r.join(cy, house(7))
	if got := buddyKinds(drain(cy)); got != "error:home_locked" {
		t.Fatalf("Cy: %s", got)
	}
}

func TestAKnockNobodyAnswers(t *testing.T) {
	r := newHomesRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.pass(bo, 7, "Ada", false)
	r.join(ada, house(7))
	drain(ada)
	drain(bo)
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	drain(bo)
	drain(ada)
	if r.fire(homeKnockWait) != 1 {
		t.Fatal("the knock waits knockWait")
	}
	if f := only(t, drain(bo), "home_answered"); f["allow"] != false {
		t.Fatalf("no answer: %v", f)
	}
	only(t, drain(ada), "home_knock_over")
	// A late yes lets nobody in.
	r.do(ada, map[string]any{"t": "home_answer", "id": 9, "allow": true})
	if got := buddyKinds(drain(bo)); got != "" {
		t.Fatalf("late answer: %s", got)
	}
	r.now = r.now.Add(homeKnockEvery)
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	only(t, drain(bo), "home_knock_sent")
	r.do(ada, map[string]any{"t": "home_answer", "id": 9, "allow": false})
	if f := only(t, drain(bo), "home_answered"); f["allow"] != false {
		t.Fatalf("no: %v", f)
	}
}

func TestKnockingOnAnOfflineOrBlockingOwner(t *testing.T) {
	r := newHomesRig(t)
	bo := r.connect(9, "Bo")
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	if f := only(t, drain(bo), "home_refused"); f["why"] != "private" {
		t.Fatalf("no pass: %v", f)
	}
	r.pass(bo, 7, "Ada", false)
	drain(bo)
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	if f := only(t, drain(bo), "home_refused"); f["why"] != "offline" || f["text"] != "The owner of this location (Ada) is currently offline" {
		t.Fatalf("offline: %v", f)
	}
	r.ign.ign[[2]int64{7, 9}] = true
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	if f := only(t, drain(bo), "home_refused"); f["why"] != "blocked" || f["text"] != "Ada is blocking you." {
		t.Fatalf("blocked: %v", f)
	}
}

func TestAKeyGivenAndTakenBack(t *testing.T) {
	r := newHomesRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.pass(bo, 7, "Ada", false)
	drain(bo)
	proof := r.keyProof(7, 9, true)
	r.do(ada, map[string]any{"t": "home_key_tell", "token": proof})
	f := only(t, drain(bo), "home_key")
	if f["on"] != true || f["text"] != "Ada sent you a key to their house! (Keys can be found at your Magic Rock.)" || f["from"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("key: %v", f)
	}
	// Told once, and only by the owner it names.
	r.do(ada, map[string]any{"t": "home_key_tell", "token": proof})
	r.do(bo, map[string]any{"t": "home_key_tell", "token": r.keyProof(7, 9, true)})
	if got := buddyKinds(drain(bo)); got != "error:homes" {
		t.Fatalf("again or forged: %s", got)
	}
	// The pass Bo holds now opens the house.
	r.join(bo, house(7))
	if f := drain(bo); f[0]["t"] != "street" {
		t.Fatalf("in with the new key: %v", f)
	}
	r.do(ada, map[string]any{"t": "home_key_tell", "token": r.keyProof(7, 9, false)})
	frames := drain(bo)
	if got := buddyKinds(frames); got != "home_key,home_out" || frames[1]["why"] != "revoked" {
		t.Fatalf("revoked: %v", frames)
	}
	if bo.Street() != "" {
		t.Fatalf("Bo is out of the house: %q", bo.Street())
	}
	r.join(bo, house(7))
	if got := buddyKinds(drain(bo)); got != "error:home_locked" {
		t.Fatalf("no way back in: %s", got)
	}
	r.join(bo, home(7))
	if f := drain(bo); f[0]["t"] != "street" {
		t.Fatalf("the home street is still open: %v", f)
	}
}

func TestAnOwnerGoingOfflineEmptiesTheHouseButKeyHolders(t *testing.T) {
	r := newHomesRig(t)
	ada, bo, cy := r.connect(7, "Ada"), r.connect(9, "Bo"), r.connect(11, "Cy")
	r.join(ada, house(7))
	r.pass(bo, 7, "Ada", false)
	r.pass(cy, 7, "Ada", true)
	drain(bo)
	r.do(bo, map[string]any{"t": "home_knock", "owner": 7})
	r.do(ada, map[string]any{"t": "home_answer", "id": 9, "allow": true})
	r.join(bo, house(7))
	r.join(cy, house(7))
	drain(bo)
	drain(cy)
	r.hub.Disconnect(ada)
	frames := drain(bo)
	var kinds []string
	for _, f := range frames {
		kinds = append(kinds, f["t"].(string))
	}
	if !strings.Contains(strings.Join(kinds, ","), "home_out") || bo.Street() != "" {
		t.Fatalf("Bo goes out: %v (%q)", frames, bo.Street())
	}
	if cy.Street() != house(7) {
		t.Fatalf("Cy has a key and stays: %q", cy.Street())
	}
	// Ada back online: being let in went with her session (house_auth).
	ada = r.connect(7, "Ada")
	r.join(ada, house(7))
	drain(ada)
	r.join(bo, house(7))
	if got := buddyKinds(drain(bo)); got != "error:home_locked" {
		t.Fatalf("let in again only on a new knock: %s", got)
	}
}

func TestNoSecretMeansOnlyOwnersAtHome(t *testing.T) {
	r := newHomesRig(t)
	r.m.secret = nil
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.join(ada, home(7))
	if f := drain(ada); f[0]["t"] != "street" {
		t.Fatalf("owner: %v", f)
	}
	r.pass(bo, 7, "Ada", true)
	if got := buddyKinds(drain(bo)); got != "error:homes" {
		t.Fatalf("pass with no secret: %s", got)
	}
	r.join(bo, home(7))
	if got := buddyKinds(drain(bo)); got != "error:home_private" {
		t.Fatalf("visitor: %s", got)
	}
}

func TestHomePassesAreRateLimited(t *testing.T) {
	r := newHomesRig(t)
	bo := r.connect(9, "Bo")
	for i := 0; i < homePassBurst; i++ {
		r.pass(bo, 7, "Ada", false)
	}
	drain(bo)
	r.pass(bo, 7, "Ada", false)
	if got := buddyKinds(drain(bo)); got != "error:slow_down" {
		t.Fatalf("burst: %s", got)
	}
}
