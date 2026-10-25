package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// partyRig is a hub with a manual clock and manual timers.
type partyRig struct {
	t      *testing.T
	hub    *Hub
	m      *parties
	now    time.Time
	timers []*rigTimer
}

type rigTimer struct {
	d       time.Duration
	fn      func()
	stopped bool
}

func newPartyRig(t *testing.T) *partyRig {
	r := &partyRig{t: t, now: time.Unix(1_700_000_000, 0)}
	// Only the party module, so the test sees its own frames alone.
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.m = newParties(h).(*parties); return r.m }}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

// fire runs every live timer of duration d (once each).
func (r *partyRig) fire(d time.Duration) int {
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

func (r *partyRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	return cl
}

func (r *partyRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *partyRig) join(cl *Client, street string) {
	r.do(cl, map[string]any{"t": "join", "street": street})
}

// drain takes every queued frame for cl.
func drain(cl *Client) []map[string]any {
	var out []map[string]any
	for {
		select {
		case b := <-cl.send:
			var m map[string]any
			json.Unmarshal(b, &m)
			out = append(out, m)
		default:
			return out
		}
	}
}

func kinds(msgs []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, m := range msgs {
		if m["t"] == kind {
			out = append(out, m)
		}
	}
	return out
}

func texts(msgs []map[string]any) string {
	var out []string
	for _, m := range kinds(msgs, "party_activity") {
		out = append(out, fmt.Sprint(m["text"]))
	}
	return strings.Join(out, " | ")
}

// lastRoster is the newest party message: nil party, or its members as "name:street:flags".
func lastRoster(t *testing.T, msgs []map[string]any) (bool, string) {
	t.Helper()
	rs := kinds(msgs, "party")
	if len(rs) == 0 {
		t.Fatalf("no party message in %v", msgs)
	}
	p, _ := rs[len(rs)-1]["party"].(map[string]any)
	if p == nil {
		return false, ""
	}
	var parts []string
	for _, x := range p["members"].([]any) {
		mem := x.(map[string]any)
		s := fmt.Sprintf("%v:%v", mem["name"], mem["street"])
		if mem["leader"] == true {
			s += ":leader"
		}
		if mem["online"] != true {
			s += ":offline"
		}
		if mem["away"] == true {
			s += ":away"
		}
		parts = append(parts, s)
	}
	return true, strings.Join(parts, ",")
}

func (r *partyRig) party(a, b *Client) {
	r.t.Helper()
	r.do(a, map[string]any{"t": "party_invite", "id": b.ID()})
	if len(kinds(drain(b), "party_invited")) != 1 {
		r.t.Fatal("no invite reached the invitee")
	}
	r.do(b, map[string]any{"t": "party_accept", "from": a.ID()})
}

func TestAPartyFormsChatsAndFollowsItsMembers(t *testing.T) {
	r := newPartyRig(t)
	a, b, c := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	for _, cl := range []*Client{a, b, c} {
		r.join(cl, "GSTREET1")
	}
	drain(a)
	drain(b)
	drain(c)

	r.do(a, map[string]any{"t": "party_invite", "id": 2})
	if got := texts(drain(a)); got != "You have invited Bo to join your party." {
		t.Fatalf("inviter heard %q", got)
	}
	inv := kinds(drain(b), "party_invited")
	if len(inv) != 1 || inv[0]["text"] != "Ada has invited you to join their Party Chat." || inv[0]["timeout"] != float64(89) {
		t.Fatalf("invitee got %v", inv)
	}
	r.do(b, map[string]any{"t": "party_accept", "from": 1})
	am, bm := drain(a), drain(b)
	if got := texts(am); got != "Bo accepted your party invite." {
		t.Fatalf("leader heard %q", got)
	}
	if got := texts(bm); got != "You joined a party with Ada" {
		t.Fatalf("joiner heard %q", got)
	}
	for _, msgs := range [][]map[string]any{am, bm} {
		if _, got := lastRoster(t, msgs); got != "Ada:GSTREET1:leader,Bo:GSTREET1" {
			t.Fatalf("roster %q", got)
		}
	}
	if len(kinds(drain(c), "party")) != 0 {
		t.Fatal("a non-member saw the roster")
	}

	// Party chat: members only, sender included; street chat stays separate.
	r.do(b, map[string]any{"t": "party_chat", "text": "  meet  at the   tree "})
	for _, cl := range []*Client{a, b} {
		said := kinds(drain(cl), "party_said")
		if len(said) != 1 || said[0]["text"] != "meet at the tree" || said[0]["name"] != "Bo" {
			t.Fatalf("member %d got %v", cl.ID(), said)
		}
	}
	if got := drain(c); len(got) != 0 {
		t.Fatalf("a non-member on the same street heard %v", got)
	}

	// A street change: leave then join, one roster with the new street.
	r.join(b, "GSTREET2")
	if _, got := lastRoster(t, drain(a)); got != "Ada:GSTREET1:leader,Bo:GSTREET2" {
		t.Fatalf("after the move %q", got)
	}
	r.fire(partySettle)
	if got := kinds(drain(a), "party"); len(got) != 0 {
		t.Fatalf("the settled push still went out after the join answered: %v", got)
	}
	// A private place: off the street, still in the party.
	r.do(b, map[string]any{"t": "leave"})
	r.fire(partySettle)
	if _, got := lastRoster(t, drain(a)); got != "Ada:GSTREET1:leader,Bo:" {
		t.Fatalf("in a private place %q", got)
	}

	r.do(b, map[string]any{"t": "party_leave"})
	am, bm = drain(a), drain(b)
	if in, _ := lastRoster(t, bm); in || texts(bm) != "You have left the party" {
		t.Fatalf("leaver got %v", bm)
	}
	if in, _ := lastRoster(t, am); in || texts(am) != "Your party has been disbanded" {
		t.Fatalf("the one left got %v", am)
	}
}

func TestInvitesFailForTheSourceReasons(t *testing.T) {
	r := newPartyRig(t)
	a, b, c, d := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy"), r.connect(4, "Dee")
	r.party(c, d)
	drain(a)
	cases := []struct {
		msg  map[string]any
		want string
	}{
		{map[string]any{"id": 1}, "You can't invite yourself."},
		{map[string]any{"id": 99}, "That player is currently offline."},
		{map[string]any{"name": "Nobody"}, "Nobody is currently offline."},
		{map[string]any{"id": 3}, "Cy is already in a party."},
		{map[string]any{"name": "bo"}, "You have invited Bo to join your party."},
		{map[string]any{"name": "BO"}, "Bo has already been invited to join your party."},
	}
	for _, tc := range cases {
		tc.msg["t"] = "party_invite"
		r.now = r.now.Add(partyInviteEvery) // each attempt spends from the invite rate
		r.do(a, tc.msg)
		if got := texts(drain(a)); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.msg, got, tc.want)
		}
	}
	r.do(a, map[string]any{"t": "party_invite"})
	if e := kinds(drain(a), "error"); len(e) != 1 || e[0]["code"] != "bad_party" {
		t.Fatalf("no target: %v", e)
	}
	r.do(b, map[string]any{"t": "party_accept", "from": 1})
	drain(a)
	r.now = r.now.Add(partyInviteEvery)
	r.do(a, map[string]any{"t": "party_invite", "id": 2})
	if got := texts(drain(a)); !strings.HasSuffix(got, "Bo is already in your party.") {
		t.Fatalf("member: %q", got)
	}
	// Two players with one name: by name is ambiguous, by id works.
	e1, e2 := r.connect(5, "Echo"), r.connect(6, "echo")
	_, _ = e1, e2
	r.now = r.now.Add(partyInviteEvery)
	r.do(a, map[string]any{"t": "party_invite", "name": "Echo"})
	if got := texts(drain(a)); !strings.HasPrefix(got, "More than one player online is called Echo.") {
		t.Fatalf("ambiguous: %q", got)
	}
}

func TestInvitesExpireAndDeclineAndTheLonePartyCloses(t *testing.T) {
	r := newPartyRig(t)
	a, b, c := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	m := partiesOf(t, r)
	r.do(a, map[string]any{"t": "party_invite", "id": 2})
	r.do(a, map[string]any{"t": "party_invite", "id": 3})
	drain(a)
	drain(b)
	if got := m.partyIDs(); len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("an inviting party of one: %v", got)
	}
	r.do(b, map[string]any{"t": "party_decline", "from": 1})
	if got := texts(drain(a)); got != "Bo has declined your invitation" {
		t.Fatalf("decline: %q", got)
	}
	if len(m.partyIDs()) != 1 {
		t.Fatal("the party closed with an invite still out")
	}
	if n := r.fire(partyInviteTimeout); n != 1 {
		t.Fatalf("%d live invite timers, want 1 (the declined one is stopped)", n)
	}
	if got := texts(drain(a)); got != "Cy has declined your invitation (expired)" {
		t.Fatalf("expiry: %q", got)
	}
	if u := kinds(drain(c), "party_uninvited"); len(u) != 1 || u[0]["from"] != float64(1) {
		t.Fatalf("the invitee's prompt was not withdrawn: %v", u)
	}
	if len(m.partyIDs()) != 0 {
		t.Fatalf("the lone party stayed open: %v", m.partyIDs())
	}
	r.do(c, map[string]any{"t": "party_accept", "from": 1})
	if got := texts(drain(c)); got != "Your invitation to join this party has expired - sorry!" {
		t.Fatalf("late accept: %q", got)
	}
}

func TestALeavingLeaderPassesTheLead(t *testing.T) {
	r := newPartyRig(t)
	a, b, c := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	r.party(a, b)
	r.do(a, map[string]any{"t": "party_invite", "id": 3})
	drain(c)
	r.do(c, map[string]any{"t": "party_accept", "from": 1})
	am, bm := drain(a), drain(b)
	if !strings.Contains(texts(am), "Cy accepted your party invite.") || !strings.Contains(texts(bm), "Cy has joined the party") {
		t.Fatalf("third member: inviter %q, other %q", texts(am), texts(bm))
	}
	if got := texts(drain(c)); got != "You joined a party with Ada and Bo" {
		t.Fatalf("joiner: %q", got)
	}
	r.do(a, map[string]any{"t": "party_leave"})
	bm, cm := drain(b), drain(c)
	if got := texts(bm); got != "You are now leader of the party | Ada has left the party" {
		t.Fatalf("new leader heard %q", got)
	}
	if got := texts(cm); got != "Ada has left the party" {
		t.Fatalf("member heard %q", got)
	}
	if _, got := lastRoster(t, cm); got != "Bo::leader,Cy:" {
		t.Fatalf("roster %q", got)
	}
}

func TestPartiesOutlastADropButNotEveryoneLeaving(t *testing.T) {
	r := newPartyRig(t)
	a, b := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.join(a, "GSTREET1")
	r.join(b, "GSTREET1")
	r.party(a, b)
	drain(a)
	m := partiesOf(t, r)

	// A replaced socket keeps the party, and hears the roster again.
	b2 := r.connect(2, "Bo")
	if in, _ := lastRoster(t, drain(b2)); !in {
		t.Fatal("the new socket was not told its party")
	}
	// A short drop: away, then back, nobody told they went offline.
	r.hub.Disconnect(b2)
	if _, got := lastRoster(t, drain(a)); got != "Ada:GSTREET1:leader,Bo::away" {
		t.Fatalf("dropped: %q", got)
	}
	b3 := r.connect(2, "Bo")
	r.join(b3, "GSTREET1")
	if n := r.fire(partyGrace); n != 0 {
		t.Fatalf("%d grace timers still live after the return", n)
	}
	if got := texts(drain(a)); got != "" {
		t.Fatalf("a short drop told the party %q", got)
	}
	// A long one: offline, still a member; back again later.
	r.hub.Disconnect(b3)
	r.fire(partyGrace)
	am := drain(a)
	if got := texts(am); got != "Party member Bo has gone offline" {
		t.Fatalf("offline: %q", got)
	}
	if _, got := lastRoster(t, am); got != "Ada:GSTREET1:leader,Bo::offline" {
		t.Fatalf("offline roster %q", got)
	}
	b4 := r.connect(2, "Bo")
	if got := texts(drain(a)); got != "Party member Bo has come online" {
		t.Fatalf("online: %q", got)
	}
	if in, _ := lastRoster(t, drain(b4)); !in {
		t.Fatal("back online without the roster")
	}
	// Everyone gone: the party closes.
	r.hub.Disconnect(a)
	r.hub.Disconnect(b4)
	r.fire(partyGrace)
	if got := m.partyIDs(); len(got) != 0 {
		t.Fatalf("a party with nobody online: %v", got)
	}
}

func TestAcceptingAnotherPartyLeavesTheFirstAndBringsYourInvites(t *testing.T) {
	r := newPartyRig(t)
	a, b, c, d := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy"), r.connect(4, "Dee")
	m := partiesOf(t, r)
	// Someone in a party cannot be invited (send_invite other_party), so Cy's
	// invite is out before Bo joins Ada.
	r.do(c, map[string]any{"t": "party_invite", "id": 2})
	drain(b)
	r.party(a, b)
	drain(a)
	r.do(b, map[string]any{"t": "party_invite", "id": 4}) // Bo invites Dee into Ada's party
	drain(b)
	r.do(b, map[string]any{"t": "party_accept", "from": 3})
	if got := texts(drain(a)); got != "Your party has been disbanded" {
		t.Fatalf("the old party: %q", got)
	}
	drain(d)
	r.do(d, map[string]any{"t": "party_accept", "from": 2})
	if got := m.partyIDs(); len(got) != 1 || fmt.Sprint(got[0]) != "[3 2 4]" {
		t.Fatalf("parties %v", got)
	}
}

func TestPartyChatRules(t *testing.T) {
	r := newPartyRig(t)
	a, b := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.do(a, map[string]any{"t": "party_chat", "text": "hello?"})
	if got := texts(drain(a)); got != "You're not in a party!" {
		t.Fatalf("no party: %q", got)
	}
	r.party(a, b)
	drain(a)
	r.do(a, map[string]any{"t": "party_chat", "text": "/who"})
	if e := kinds(drain(a), "error"); len(e) != 1 || e[0]["message"] != "Slash commands are for global/local chat only" {
		t.Fatalf("slash: %v", e)
	}
	for i := 0; i < chatBurst; i++ {
		r.do(a, map[string]any{"t": "party_chat", "text": "spam"})
	}
	r.do(a, map[string]any{"t": "party_chat", "text": "spam"})
	am := drain(a)
	if len(kinds(am, "party_said")) != chatBurst || len(kinds(am, "error")) != 1 {
		t.Fatalf("rate: %d said, %v", len(kinds(am, "party_said")), kinds(am, "error"))
	}
	r.now = r.now.Add(chatEvery)
	r.do(a, map[string]any{"t": "party_chat", "text": strings.Repeat("x", 500)})
	said := kinds(drain(b), "party_said")
	if last := said[len(said)-1]; len([]rune(last["text"].(string))) != maxChat {
		t.Fatalf("long line kept %d", len([]rune(last["text"].(string))))
	}
}

func TestInviteLimits(t *testing.T) {
	r := newPartyRig(t)
	target := r.connect(100, "Popular")
	for i := int64(1); i <= partyMaxHeld+1; i++ {
		inviter := r.connect(i, fmt.Sprintf("P%d", i))
		r.do(inviter, map[string]any{"t": "party_invite", "id": 100})
		got := texts(drain(inviter))
		if i <= partyMaxHeld && got != "You have invited Popular to join your party." {
			t.Fatalf("invite %d: %q", i, got)
		}
		if i == partyMaxHeld+1 && got != "Popular has a lot of invites waiting right now." {
			t.Fatalf("over the cap: %q", got)
		}
	}
	// A reconnect brings the waiting invites back, with their time left.
	r.now = r.now.Add(30 * time.Second)
	drain(target)
	t2 := r.connect(100, "Popular")
	inv := kinds(drain(t2), "party_invited")
	if len(inv) != partyMaxHeld || inv[0]["timeout"] != float64(59) {
		t.Fatalf("resent %d invites, first %v", len(inv), inv)
	}
	// The invite rate.
	spam := r.connect(200, "Spammer")
	for i := 0; i < partyInviteBurst+1; i++ {
		r.do(spam, map[string]any{"t": "party_invite", "id": int64(300 + i)})
	}
	if e := kinds(drain(spam), "error"); len(e) != 1 || e[0]["code"] != "slow_down" {
		t.Fatalf("invite rate: %v", e)
	}
}

func partiesOf(t *testing.T, r *partyRig) *parties { return r.m }

// The hub drops a client that stopped reading without the disconnect hook;
// the party still sees them go.
func TestASlowClientDroppedByTheHubStillGoesOffline(t *testing.T) {
	r := newPartyRig(t)
	a, b := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.join(b, "GSTREET1")
	r.party(a, b)
	drain(a)
	for len(b.send) < cap(b.send) {
		b.send <- []byte(`{}`)
	}
	r.join(a, "GSTREET1") // the "joined" frame finds Bo's buffer full
	if r.hub.ClientLocked(2) != nil {
		t.Fatal("the hub kept the slow client")
	}
	r.fire(partySettle)
	if _, got := lastRoster(t, drain(a)); got != "Ada:GSTREET1:leader,Bo::away" {
		t.Fatalf("after the drop %q", got)
	}
	r.fire(partyGrace)
	if got := texts(drain(a)); got != "Party member Bo has gone offline" {
		t.Fatalf("offline: %q", got)
	}
}
