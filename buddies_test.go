package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var buddyTestSecret = []byte("buddies-test-secret-buddies-test-secret")

// buddyRig is a hub with only the buddies module, a manual clock and manual timers.
type buddyRig struct {
	t      *testing.T
	hub    *Hub
	m      *buddies
	now    time.Time
	timers []*rigTimer
	n      int
}

func newBuddyRig(t *testing.T) *buddyRig {
	r := &buddyRig{t: t, now: time.Unix(1_700_000_000, 0)}
	savedSecret := buddySecretForTest
	buddySecretForTest = buddyTestSecret
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.m = newBuddies(h).(*buddies); return r.m }}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	buddySecretForTest = savedSecret
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

func (r *buddyRig) fire(d time.Duration) int {
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

func (r *buddyRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

func (r *buddyRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *buddyRig) join(cl *Client, street string) {
	r.do(cl, map[string]any{"t": "join", "street": street})
}

// encodeIDs is the site's encoding (Buddies::ids): sorted, differences, base 36.
func encodeIDs(ids []int64) string {
	s := append([]int64{}, ids...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var parts []string
	var at int64
	for _, id := range s {
		parts = append(parts, strconv.FormatInt(id-at, 36))
		at = id
	}
	return strings.Join(parts, ",")
}

func signBuddy(secret []byte, payload map[string]any) string {
	raw, _ := json.Marshal(payload)
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (r *buddyRig) watchToken(sub int64, bud, rev, ign []int64) string {
	return signBuddy(buddyTestSecret, map[string]any{"k": "buddies", "sub": sub, "b": encodeIDs(bud), "r": encodeIDs(rev), "i": encodeIDs(ign), "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})
}

func (r *buddyRig) watch(cl *Client, bud, rev, ign []int64) {
	r.do(cl, map[string]any{"t": "buddies_watch", "token": r.watchToken(cl.ID(), bud, rev, ign)})
}

func (r *buddyRig) addProof(sub, to int64) string {
	r.n++
	return signBuddy(buddyTestSecret, map[string]any{"k": "buddy_added", "sub": sub, "to": to, "iat": r.now.Unix(), "exp": r.now.Add(time.Minute).Unix(), "n": "n" + strconv.Itoa(r.n)})
}

func buddyKinds(frames []map[string]any) string {
	var out []string
	for _, f := range frames {
		s, _ := f["t"].(string)
		if s == "error" {
			s += ":" + f["code"].(string)
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func only(t *testing.T, frames []map[string]any, kind string) map[string]any {
	t.Helper()
	if len(frames) != 1 || frames[0]["t"] != kind {
		t.Fatalf("want one %s, got %v", kind, frames)
	}
	return frames[0]
}

func TestBuddyIDsRoundTrip(t *testing.T) {
	ids := []int64{1, 7, 44, 45, 9000, 123456}
	got, ok := buddyIDs(encodeIDs(ids))
	if !ok || len(got) != len(ids) {
		t.Fatalf("decoded %v %v", got, ok)
	}
	for _, id := range ids {
		if !got[id] {
			t.Fatalf("lost %d", id)
		}
	}
	for _, bad := range []string{"x!", "-3", "3,0", "0", ",", "3,,4"} {
		if _, ok := buddyIDs(bad); ok {
			t.Fatalf("%q should not decode", bad)
		}
	}
	if m, ok := buddyIDs(""); !ok || len(m) != 0 {
		t.Fatal("an empty list is an empty list")
	}
}

func TestAWatchTellsWhoIsOnlineAndWhere(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	b := r.connect(2, "Bo")
	r.join(a, "GA1")
	r.join(b, "GA2")
	drain(a)
	drain(b)
	// Bo sends their list first: Ada has none yet, so nobody hears anything but Bo's status.
	r.watch(b, nil, []int64{1}, nil)
	st := only(t, drain(b), "buddies_status")
	if on := st["online"].([]any); len(on) != 0 {
		t.Fatalf("Ada has not sent a list, so she is not online for buddies yet: %v", on)
	}
	// Ada's list makes her online: Bo (who she added) hears it, with her street.
	r.watch(a, []int64{2}, nil, nil)
	got := only(t, drain(b), "buddy_online")
	if got["id"] != float64(1) || got["name"] != "Ada" || got["street"] != "GA1" {
		t.Fatalf("Bo heard %v", got)
	}
	st = only(t, drain(a), "buddies_status")
	on := st["online"].([]any)
	if len(on) != 1 || on[0].(map[string]any)["street"] != "GA2" {
		t.Fatalf("Ada's status %v", st)
	}
}

func TestStreetChangesAreToldOnce(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	b := r.connect(2, "Bo")
	r.join(a, "GA1")
	r.join(b, "GA2")
	r.watch(a, []int64{2}, nil, nil)
	r.watch(b, nil, []int64{1}, nil)
	drain(a)
	drain(b)
	// A street change is a leave then a join: one buddy_moved.
	r.join(b, "GA3")
	got := only(t, drain(a), "buddy_moved")
	if got["street"] != "GA3" {
		t.Fatalf("moved %v", got)
	}
	if n := r.fire(buddySettle); n != 0 {
		t.Fatalf("the join stopped the leave's settle timer: %d left", n)
	}
	if f := drain(a); len(f) != 0 {
		t.Fatalf("the settled leave said more: %v", f)
	}
	// A private place (leave, no join) shows as street "".
	r.do(b, map[string]any{"t": "leave"})
	if f := drain(a); len(f) != 0 {
		t.Fatalf("nothing before the settle: %v", f)
	}
	r.fire(buddySettle)
	if got := only(t, drain(a), "buddy_moved"); got["street"] != "" {
		t.Fatalf("private place %v", got)
	}
	// A race scene is private too.
	r.join(b, "RACE_ABC")
	if f := drain(a); len(f) != 0 {
		t.Fatalf("a race is still a secret place: %v", f)
	}
}

func TestOfflineAfterTheGraceAndBackWithin(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	b := r.connect(2, "Bo")
	r.join(a, "GA1")
	r.join(b, "GA2")
	r.watch(a, []int64{2}, nil, nil)
	r.watch(b, []int64{1}, nil, nil)
	drain(a)
	// A reload: Bo's socket drops and comes back inside the grace. Ada hears nothing.
	r.hub.Disconnect(b)
	b = r.connect(2, "Bo")
	r.join(b, "GA2")
	r.watch(b, []int64{1}, nil, nil)
	r.fire(buddyGrace)
	if f := drain(a); len(f) != 0 {
		t.Fatalf("a reload is not a logout: %v", f)
	}
	// Gone for good: after the grace Ada hears it.
	r.hub.Disconnect(b)
	if f := drain(a); len(f) != 0 {
		t.Fatalf("too soon: %v", f)
	}
	if n := r.fire(buddyGrace); n != 1 {
		t.Fatalf("grace timers %d", n)
	}
	got := only(t, drain(a), "buddy_offline")
	if got["id"] != float64(2) || got["name"] != "Bo" {
		t.Fatalf("offline %v", got)
	}
	if _, on := r.m.live[2]; on || r.m.watch[2] != nil {
		t.Fatal("an offline player's state is dropped")
	}
}

func TestOnlyWhoTheTokenNamesHears(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	b := r.connect(2, "Bo")
	c := r.connect(3, "Cy")
	r.watch(a, []int64{2}, nil, nil)
	r.watch(c, nil, nil, nil)
	drain(a)
	drain(c)
	r.watch(b, nil, nil, nil)
	if f := drain(c); len(f) != 0 {
		t.Fatalf("Cy holds nobody and heard %v", f)
	}
	only(t, drain(a), "buddy_online")
}

func TestForgedOrForeignTokensAreRefused(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	b := r.connect(2, "Bo")
	r.watch(b, nil, nil, nil)
	drain(b)
	cases := map[string]string{
		"someone else's":   r.watchToken(2, []int64{1}, nil, nil),
		"wrong secret":     signBuddy([]byte("not-the-secret-not-the-secret-not-the"), map[string]any{"k": "buddies", "sub": 1, "b": "2", "exp": r.now.Add(time.Hour).Unix()}),
		"expired":          signBuddy(buddyTestSecret, map[string]any{"k": "buddies", "sub": 1, "b": "2", "exp": r.now.Add(-time.Second).Unix()}),
		"a ticket":         signBuddy(buddyTestSecret, map[string]any{"sub": 1, "name": "Ada", "jti": "x", "exp": r.now.Add(time.Hour).Unix()}),
		"an add proof":     r.addProof(1, 2),
		"bad ids":          signBuddy(buddyTestSecret, map[string]any{"k": "buddies", "sub": 1, "b": "2,0", "exp": r.now.Add(time.Hour).Unix()}),
		"not even a token": "hello",
	}
	for name, token := range cases {
		r.now = r.now.Add(time.Minute) // past the watch rate limit
		r.do(a, map[string]any{"t": "buddies_watch", "token": token})
		if got := buddyKinds(drain(a)); got != "error:buddies" {
			t.Fatalf("%s: %s", name, got)
		}
	}
	if r.m.watch[1] != nil {
		t.Fatal("no refused token was kept")
	}
	if f := drain(b); len(f) != 0 {
		t.Fatalf("Bo heard of Ada from a refused token: %v", f)
	}
	// A buddy list never opens a socket as a ticket.
	if _, err := NewTickets(buddyTestSecret, func() time.Time { return r.now }).Check(r.watchToken(1, nil, nil, nil)); err == nil {
		t.Fatal("a buddy token must not pass as a realtime ticket")
	}
}

func TestNoSecretMeansNoBuddies(t *testing.T) {
	r := newBuddyRig(t)
	r.m.secret = nil
	a := r.connect(1, "Ada")
	r.watch(a, nil, nil, nil)
	if got := buddyKinds(drain(a)); got != "error:buddies" {
		t.Fatalf("got %s", got)
	}
}

func TestAddedYouReachesTheOtherOnce(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	b := r.connect(2, "Bo")
	proof := r.addProof(1, 2)
	r.do(a, map[string]any{"t": "buddy_tell", "token": proof})
	got := only(t, drain(b), "buddy_added")
	if got["id"] != float64(1) || got["name"] != "Ada" {
		t.Fatalf("added %v", got)
	}
	r.do(a, map[string]any{"t": "buddy_tell", "token": proof})
	if f := drain(b); len(f) != 0 {
		t.Fatalf("a proof works once: %v", f)
	}
	// Only the adder may send it.
	r.do(b, map[string]any{"t": "buddy_tell", "token": r.addProof(1, 2)})
	if got := buddyKinds(drain(b)); got != "error:buddies" {
		t.Fatalf("Bo sending Ada's proof: %s", got)
	}
	// A proof that claims a long life is refused.
	long := signBuddy(buddyTestSecret, map[string]any{"k": "buddy_added", "sub": 1, "to": 2, "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix(), "n": "long"})
	r.do(a, map[string]any{"t": "buddy_tell", "token": long})
	if got := buddyKinds(drain(a)); got != "error:buddies" {
		t.Fatalf("long proof: %s", got)
	}
	// Someone ignoring you never hears it.
	r.watch(b, nil, nil, []int64{1})
	drain(b)
	r.do(a, map[string]any{"t": "buddy_tell", "token": r.addProof(1, 2)})
	if f := drain(b); len(f) != 0 {
		t.Fatalf("Bo ignores Ada: %v", f)
	}
}

func TestIgnoringHidesBothWaysAndFeedsTheHook(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	c := r.connect(3, "Cy")
	r.join(a, "GA1")
	r.join(c, "GA2")
	r.watch(a, []int64{3}, nil, nil)
	r.watch(c, []int64{1}, nil, nil)
	drain(a)
	drain(c)
	ignoring, ok := r.hub.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	if !ok {
		t.Fatal("buddies.ignoring is provided")
	}
	if ignoring(1, 3) || ignoring(3, 1) {
		t.Fatal("nobody ignores anybody yet")
	}
	// Ada ignores Cy (the site drops Cy from her list): Cy is told Ada is gone.
	r.watch(a, nil, nil, []int64{3})
	if got := only(t, drain(c), "buddy_offline"); got["id"] != float64(1) {
		t.Fatalf("Cy heard %v", got)
	}
	if st := only(t, drain(a), "buddies_status"); len(st["online"].([]any)) != 0 {
		t.Fatalf("Ada's status still shows Cy: %v", st)
	}
	if !ignoring(1, 3) || ignoring(3, 1) {
		t.Fatal("the hook reads Ada's signed ignore list")
	}
	// Cy's stale token still names Ada: Cy hears nothing of her moves.
	r.join(a, "GA9")
	if f := drain(c); len(f) != 0 {
		t.Fatalf("an ignored player hears %v", f)
	}
	// Un-ignore: Cy hears Ada again.
	r.watch(a, []int64{3}, nil, nil)
	if got := only(t, drain(c), "buddy_online"); got["street"] != "GA9" {
		t.Fatalf("back %v", got)
	}
	if ignoring(1, 3) {
		t.Fatal("un-ignored")
	}
}

func TestWatchesAreRateLimited(t *testing.T) {
	r := newBuddyRig(t)
	a := r.connect(1, "Ada")
	for i := 0; i < buddyWatchBurst; i++ {
		r.watch(a, nil, nil, nil)
	}
	drain(a)
	r.watch(a, nil, nil, nil)
	if got := buddyKinds(drain(a)); got != "error:slow_down" {
		t.Fatalf("got %s", got)
	}
}

// With every module loaded, a player who never sends a list changes nothing
// for the others (the other modules' tests count frames).
func TestQuietWithoutAWatch(t *testing.T) {
	savedSecret := buddySecretForTest
	buddySecretForTest = buddyTestSecret
	defer func() { buddySecretForTest = savedSecret }()
	hub := NewHub(nil)
	a := hub.Connect(Claims{Sub: 1, Name: "Ada"})
	b := hub.Connect(Claims{Sub: 2, Name: "Bo"})
	drain(a)
	drain(b)
	hub.Handle(a, []byte(`{"t":"join","street":"GA1"}`))
	hub.Disconnect(a)
	for _, f := range drain(b) {
		if strings.HasPrefix(f["t"].(string), "buddy") {
			t.Fatalf("Bo heard %v", f)
		}
	}
}

func TestABlockRefusesPartyInvitesAndTradesBothWays(t *testing.T) {
	savedSecret := buddySecretForTest
	buddySecretForTest = buddyTestSecret
	defer func() { buddySecretForTest = savedSecret }()
	r := &buddyRig{t: t, now: time.Unix(1_700_000_000, 0)}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newBuddies(h).(*buddies); return r.m },
		newParties,
		func(h *Hub) Module {
			var table tradeTable
			if err := json.Unmarshal(tradeJSON, &table); err != nil {
				t.Fatal(err)
			}
			return newTradesWith(h, table, "off")
		},
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool { return func() bool { return true } }
	ada := r.connect(1, "Ada")
	cy := r.connect(3, "Cy")
	r.join(ada, "GA1")
	r.join(cy, "GA1")
	r.watch(ada, nil, nil, []int64{3}) // Ada blocks Cy
	drain(ada)
	drain(cy)
	for _, who := range []struct{ from, to *Client }{{cy, ada}, {ada, cy}} {
		r.do(who.from, map[string]any{"t": "party_invite", "id": who.to.ID()})
		if got := fmt.Sprint(drain(who.from)); !strings.Contains(got, "You are blocking or blocked by that player.") {
			t.Fatalf("%s's party invite answered %s", who.from.Name(), got)
		}
		r.do(who.from, map[string]any{"t": "trade_start", "id": who.to.ID()})
		if got := fmt.Sprint(drain(who.from)); !strings.Contains(got, "You are blocking or blocked by that player.") {
			t.Fatalf("%s's trade answered %s", who.from.Name(), got)
		}
		if got := drain(who.to); len(got) != 0 {
			t.Fatalf("%s heard nothing: %v", who.to.Name(), got)
		}
	}
}
