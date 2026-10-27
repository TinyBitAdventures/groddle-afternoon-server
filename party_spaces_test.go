package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeSpends is the site's ledger for Party Spaces: what each player holds.
type fakeSpends struct {
	packs    map[int64]map[string]int
	currants map[int64]int64
	spent    []string // "op:id:what"
	unspent  []string
	silent   bool
}

func (f *fakeSpends) spend(op string, id int64, gave tradeGoods, why string) ledgerAnswer {
	if f.silent {
		return ledgerAnswer{}
	}
	for _, it := range gave.Items {
		if f.packs[id][it.C] < it.N {
			return ledgerAnswer{Status: "refused", Short: &ledgerShort{ID: id, C: it.C}}
		}
	}
	if f.currants[id] < gave.Currants {
		return ledgerAnswer{Status: "refused", Short: &ledgerShort{ID: id, Currants: true}}
	}
	for _, it := range gave.Items {
		f.packs[id][it.C] -= it.N
		f.spent = append(f.spent, fmt.Sprintf("%s:%d:%s", op, id, it.C))
	}
	if gave.Currants > 0 {
		f.currants[id] -= gave.Currants
		f.spent = append(f.spent, fmt.Sprintf("%s:%d:%dc", op, id, gave.Currants))
	}
	return ledgerAnswer{Status: "settled"}
}

func (f *fakeSpends) unspend(op string, id int64) ledgerAnswer {
	f.unspent = append(f.unspent, fmt.Sprintf("%s:%d", op, id))
	return ledgerAnswer{Status: "void"}
}

type spaceRig struct {
	*partyRig
	q      *quoinsModule
	ledger *fakeSpends
	held   []func() // spends waiting, when hold is set
	hold   bool
}

const moonStreet = "GDOBUP8IR8E2IPH"

func newSpaceRig(t *testing.T) *spaceRig {
	r := &spaceRig{partyRig: &partyRig{t: t, now: time.Unix(1_700_000_000, 0)}}
	d := testQuoinData()
	d.streets[moonStreet] = []quoinSpot{{X: 100, Y: 500, Type: "xp", Class: "slow medium-large xp"}}
	saved := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newParties(h).(*parties); return r.m },
		func(h *Hub) Module { r.q = newQuoinsWith(h, d); return r.q },
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	r.ledger = &fakeSpends{packs: map[int64]map[string]int{}, currants: map[int64]int64{}}
	r.m.spaces.ledger = r.ledger
	r.m.spaces.spawn = func(fn func()) {
		if r.hold {
			r.held = append(r.held, fn)
			return
		}
		fn()
	}
	n := 0
	r.m.spaces.newID = func() string { n++; return fmt.Sprintf("%016x", n) }
	return r
}

// settle runs the ledger's answers (h.After(0)).
func (r *spaceRig) settle() { r.fire(0) }

func (r *spaceRig) give(id int64, pack string, currants int64) {
	if r.ledger.packs[id] == nil {
		r.ledger.packs[id] = map[string]int{}
	}
	r.ledger.packs[id][pack]++
	r.ledger.currants[id] += currants
}

func one2(t *testing.T, msgs []map[string]any, kind string) map[string]any {
	t.Helper()
	ks := kinds(msgs, kind)
	if len(ks) == 0 {
		t.Fatalf("no %s in %v", kind, msgs)
	}
	return ks[len(ks)-1]
}

func spaceOf(t *testing.T, msgs []map[string]any) map[string]any {
	t.Helper()
	s, _ := one2(t, msgs, "party_space")["space"].(map[string]any)
	return s
}

// threesome: Ada and Bo in a party on GSTREET1, Cy there too but not in it.
func (r *spaceRig) threesome() (a, b, c *Client) {
	a, b, c = r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	for _, cl := range []*Client{a, b, c} {
		r.join(cl, "GSTREET1")
	}
	r.party(a, b)
	drain(a)
	drain(b)
	drain(c)
	return
}

func (r *spaceRig) open(a *Client, pack string) string {
	r.t.Helper()
	r.give(a.ID(), pack, 0)
	r.do(a, map[string]any{"t": "party_space_start", "pack": pack})
	r.settle()
	opened := one2(r.t, drain(a), "party_space_opened")
	return opened["space"].(map[string]any)["street"].(string)
}

func TestAPackOpensOneSpaceForTheWholeParty(t *testing.T) {
	r := newSpaceRig(t)
	a, b, c := r.threesome()
	r.give(1, "party_pack_toxic_moon", 0)
	r.do(a, map[string]any{"t": "party_space_start", "pack": "party_pack_toxic_moon"})
	r.settle()
	am, bm := drain(a), drain(b)
	opened := one2(t, am, "party_space_opened")
	if opened["pack"] != "party_pack_toxic_moon" || !strings.HasPrefix(fmt.Sprint(opened["spend"]), "party-") {
		t.Fatalf("opened %v", opened)
	}
	if len(r.ledger.spent) != 1 || !strings.HasSuffix(r.ledger.spent[0], ":1:party_pack_toxic_moon") {
		t.Fatalf("the ledger spent %v", r.ledger.spent)
	}
	sa, sb := spaceOf(t, am), spaceOf(t, bm)
	street := fmt.Sprint(sa["street"])
	if !strings.HasPrefix(street, "PARTY_") || sb["street"] != street || sa["template"] != "moon" || sa["label"] != "Toxic Moon" || sa["left"] != float64(time.Hour.Milliseconds()) {
		t.Fatalf("spaces %v / %v", sa, sb)
	}
	if got := texts(am); got != "Ada made us a new Party Space!" {
		t.Fatalf("Ada heard %q", got)
	}
	if got := texts(bm); got != "Ada made us a new Party Space!" {
		t.Fatalf("Bo heard %q", got)
	}
	if len(kinds(bm, "party_space_prompt")) != 1 || len(kinds(am, "party_space_prompt")) != 0 {
		t.Fatalf("only the others get the teleport offer: %v %v", am, bm)
	}
	if len(drain(c)) != 0 {
		t.Fatal("Cy heard about a party he is not in")
	}

	// Both in; Cy cannot follow.
	r.join(a, street)
	r.join(b, street)
	if a.Street() != street || b.Street() != street {
		t.Fatalf("the members are not in: %q %q", a.Street(), b.Street())
	}
	r.join(c, street)
	if c.Street() != "GSTREET1" || one2(t, drain(c), "error")["code"] != "private_street" {
		t.Fatalf("Cy got into the space: %q", c.Street())
	}
	// The same clock for both.
	r.now = r.now.Add(10 * time.Minute)
	r.do(b, map[string]any{"t": "party_space_extend", "minutes": 10})
	r.settle()
	if one2(t, drain(b), "party_space_refused")["text"] != partyTextAtmBroke {
		t.Fatal("Bo extended without currants")
	}
}

func TestStartingIsRefusedForTheSourceReasons(t *testing.T) {
	r := newSpaceRig(t)
	a, b, c := r.threesome()
	r.give(3, "party_pack_toxic_moon", 0)
	r.do(c, map[string]any{"t": "party_space_start", "pack": "party_pack_toxic_moon"})
	if one2(t, drain(c), "party_space_refused")["text"] != partyTextNoParty {
		t.Fatal("a pack worked outside a party")
	}
	r.do(a, map[string]any{"t": "party_space_start", "pack": "party_pack_halloween"})
	if one2(t, drain(a), "party_space_refused")["text"] != partyTextTemplate {
		t.Fatal("an unknown pack was taken")
	}
	// A forged pack: the ledger does not hold it.
	r.do(a, map[string]any{"t": "party_space_start", "pack": "party_pack_val_holla"})
	r.settle()
	am := drain(a)
	if one2(t, am, "party_space_refused")["text"] != partyTextNoPack || len(kinds(am, "party_space_opened")) != 0 || r.m.of[1].space != nil {
		t.Fatalf("a pack the ledger lacks opened a space: %v", am)
	}
	r.open(a, "party_pack_taster_val_holla")
	r.give(2, "party_pack_toxic_moon", 0)
	r.do(b, map[string]any{"t": "party_space_start", "pack": "party_pack_toxic_moon"})
	if one2(t, drain(b), "party_space_refused")["text"] != partyTextGoing {
		t.Fatal("a second space opened")
	}
	if r.ledger.packs[2]["party_pack_toxic_moon"] != 1 {
		t.Fatal("the refused pack was spent")
	}
	// No answer from the ledger: nothing opens and nothing is spent.
	r2 := newSpaceRig(t)
	a2, _, _ := r2.threesome()
	r2.give(1, "party_pack_toxic_moon", 0)
	r2.ledger.silent = true
	r2.do(a2, map[string]any{"t": "party_space_start", "pack": "party_pack_toxic_moon"})
	r2.settle()
	if one2(t, drain(a2), "party_space_refused")["why"] != "ledger" || r2.m.of[1].space != nil {
		t.Fatal("a space opened without the ledger's word")
	}
}

func TestTheAtmExtendsTheSharedClockFromThePayersCurrants(t *testing.T) {
	r := newSpaceRig(t)
	a, b, _ := r.threesome()
	street := r.open(a, "party_pack_taster_toxic_moon")
	r.join(a, street)
	r.join(b, street)
	drain(a)
	drain(b)
	r.give(2, "", 10000)
	r.do(b, map[string]any{"t": "party_space_extend", "minutes": 10})
	if len(kinds(drain(b), "error")) != 1 {
		t.Fatal("a taster's space took the full pack's offer")
	}
	r.now = r.now.Add(2 * time.Minute)
	r.do(b, map[string]any{"t": "party_space_extend", "minutes": 15})
	r.settle()
	am, bm := drain(a), drain(b)
	ext := one2(t, bm, "party_space_extended")
	if ext["cost"] != float64(8500) || ext["text"] != partyTextAtmOK || r.ledger.currants[2] != 1500 {
		t.Fatalf("extended %v, Bo has %d", ext, r.ledger.currants[2])
	}
	want := float64((8*time.Minute + 15*time.Minute).Milliseconds())
	if spaceOf(t, am)["left"] != want || spaceOf(t, bm)["left"] != want || spaceOf(t, am)["extensions"] != float64(1) {
		t.Fatalf("clocks %v / %v", spaceOf(t, am), spaceOf(t, bm))
	}
	if got := texts(am); got != "Bo extended the party time!" {
		t.Fatalf("Ada heard %q", got)
	}
	r.do(b, map[string]any{"t": "party_space_extend", "minutes": 15})
	r.settle()
	if one2(t, drain(b), "party_space_refused")["text"] != partyTextAtmBroke {
		t.Fatal("Bo extended past his currants")
	}
	// The warning comes 5 minutes before the new end, the end at it.
	if r.fire(18*time.Minute) != 1 {
		t.Fatal("no warning timer at the new end - 5 min")
	}
	if got := texts(drain(a)); got != partyTextWarning {
		t.Fatalf("the warning was %q", got)
	}
	if r.fire(23*time.Minute) != 1 {
		t.Fatal("no end timer")
	}
	am, bm = drain(a), drain(b)
	if texts(am) != partyTextExpired || spaceOf(t, am) != nil || one2(t, bm, "party_space")["why"] != "expired" {
		t.Fatalf("the end: %v", am)
	}
	if a.Street() != "" || b.Street() != "" {
		t.Fatal("the members are still in an expired space")
	}
	r.join(a, street)
	if a.Street() == street {
		t.Fatal("an expired space can be entered")
	}
	r.do(b, map[string]any{"t": "party_space_extend", "minutes": 5})
	if one2(t, drain(b), "party_space_refused")["text"] != partyTextAtmLate {
		t.Fatal("the ATM worked after the end")
	}
}

func TestTheCreatorKicksAndOnlyTheCreatorInvitesBack(t *testing.T) {
	r := newSpaceRig(t)
	a, b, c := r.threesome()
	street := r.open(a, "party_pack_toxic_moon")
	// Cy joins the party while the space lives: the invite names it, and he is offered a way in.
	r.do(a, map[string]any{"t": "party_invite", "id": 3})
	if inv := one2(t, drain(c), "party_invited"); inv["text"] != "Ada has invited you to join their Party in Toxic Moon." {
		t.Fatalf("invite %v", inv)
	}
	r.do(c, map[string]any{"t": "party_accept", "from": 1})
	if cm := drain(c); spaceOf(t, cm)["street"] != street || len(kinds(cm, "party_space_prompt")) != 1 {
		t.Fatalf("Cy was not offered the space: %v", cm)
	}
	for _, cl := range []*Client{a, b, c} {
		r.join(cl, street)
		drain(cl)
	}
	// Only the creator may kick.
	r.do(b, map[string]any{"t": "party_space_kick", "id": 3})
	if one2(t, drain(b), "error")["message"] != partyTextCant {
		t.Fatal("Bo kicked someone")
	}
	r.do(a, map[string]any{"t": "party_space_kick", "id": 2})
	am, bm := drain(a), drain(b)
	if one2(t, bm, "party_space_kicked")["text"] != "Ada removed you from their Party Space." || b.Street() != "" {
		t.Fatalf("Bo was not put out: %v on %q", bm, b.Street())
	}
	if !strings.Contains(texts(am), "Bo was removed from your Party Space.") || r.m.of[2] != nil {
		t.Fatalf("Ada heard %q", texts(am))
	}
	r.join(b, street)
	if b.Street() == street {
		t.Fatal("Bo walked back in")
	}
	r.join(b, "GSTREET1")
	drain(c)
	r.do(c, map[string]any{"t": "party_invite", "id": 2})
	if got := texts(drain(c)); got != "Bo has been removed from your party - only the party creator can re-invite them." {
		t.Fatalf("Cy's invite: %q", got)
	}
	r.do(a, map[string]any{"t": "party_invite", "id": 2})
	if got := texts(drain(a)); got != "You have invited Bo to join your party." {
		t.Fatalf("Ada's invite: %q", got)
	}
}

func TestLeavingThePartyLeavesTheSpaceAndADisbandedPartyLosesIt(t *testing.T) {
	r := newSpaceRig(t)
	a, b, _ := r.threesome()
	street := r.open(a, "party_pack_toxic_moon")
	r.join(a, street)
	r.join(b, street)
	drain(a)
	drain(b)
	r.do(b, map[string]any{"t": "party_leave"})
	if b.Street() != "" || one2(t, drain(b), "party_space")["why"] != "left" {
		t.Fatal("Bo stayed in the space after leaving the party")
	}
	am := drain(a)
	if one2(t, am, "party_space")["why"] != "closed" || a.Street() != "" || r.m.spaces.byStreet[street] != nil {
		t.Fatalf("the space outlived its party: %v", am)
	}
}

func TestPartyQuoinsAreSharedInTheSpace(t *testing.T) {
	r := newSpaceRig(t)
	a, b, _ := r.threesome()
	street := r.open(a, "party_pack_toxic_moon")
	r.do(a, map[string]any{"t": "join", "street": street, "x": 100, "y": 528})
	r.do(b, map[string]any{"t": "join", "street": street, "x": 100, "y": 528})
	drain(a)
	drain(b)
	r.do(a, map[string]any{"t": "quoin_sync", "street": street})
	if q := one2(t, drain(a), "quoins"); q["shared"] != true || len(q["quoins"].([]any)) != 1 {
		t.Fatalf("the space has no shared quoins: %v", q)
	}
	r.do(b, map[string]any{"t": "quoin_claim", "street": street, "i": 0})
	r.do(a, map[string]any{"t": "quoin_claim", "street": street, "i": 0})
	if len(kinds(drain(b), "quoin_got")) != 1 || one2(t, drain(a), "quoin_refused")["why"] != "gone" {
		t.Fatal("the first toucher did not win the party quoin")
	}
	r.do(a, map[string]any{"t": "quoin_sync", "street": moonStreet})
	_ = drain(a)
}

func TestAPackSpentAsThePartyEndsIsGivenBack(t *testing.T) {
	r := newSpaceRig(t)
	a, b, _ := r.threesome()
	r.give(1, "party_pack_toxic_moon", 0)
	r.hold = true
	r.do(a, map[string]any{"t": "party_space_start", "pack": "party_pack_toxic_moon"})
	r.do(b, map[string]any{"t": "party_leave"}) // the party disbands meanwhile
	r.hold = false
	for _, fn := range r.held {
		fn()
	}
	r.settle()
	r.settle()
	if len(r.ledger.unspent) != 1 || len(kinds(drain(a), "party_space_opened")) != 0 {
		t.Fatalf("the pack was not given back: %v", r.ledger.unspent)
	}
}

func TestPartyPackClasses(t *testing.T) {
	for class, want := range map[string][2]any{
		"party_pack_toxic_moon":        {"moon", false},
		"party_pack_taster_toxic_moon": {"moon", true},
		"party_pack_winter_wingding":   {"glitchmas", false},
		"party_pack_taster_val_holla":  {"val_holla", true},
		"party_pack_monster_bash":      {"", false},
		"party_pack":                   {"", false},
		"party_pack_taster_":           {"", false},
		"race_ticket":                  {"", false},
	} {
		tpl, taster, ok := partyPackTemplate(class)
		if tpl != want[0] || (ok && taster != want[1]) || ok != (want[0] != "") {
			t.Errorf("%s: %q %v %v", class, tpl, taster, ok)
		}
	}
	for id, tpl := range partyTemplates {
		if len(tpl.prices) != 4 || !strings.HasPrefix(tpl.street, "G") || tpl.label == "" {
			t.Errorf("template %s: %+v", id, tpl)
		}
	}
}
