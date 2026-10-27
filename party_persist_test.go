package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// psRig is a Party Spaces rig with a state file and timers kept by when they come due, so a
// test can run the clock forward and restart the server from the file.
type psRig struct {
	*spaceRig
	path string
	due  []*psTimer
}

type psTimer struct {
	at   time.Time
	fn   func()
	done bool
}

func newPsRig(t *testing.T, path string, now time.Time, ledger *fakeSpends, idBase int) *psRig {
	t.Helper()
	saved := partyFileForTest
	partyFileForTest = path
	defer func() { partyFileForTest = saved }()
	r := &psRig{spaceRig: &spaceRig{partyRig: &partyRig{t: t, now: now}}, path: path}
	d := testQuoinData()
	d.streets[moonStreet] = []quoinSpot{{X: 100, Y: 500, Type: "xp", Class: "slow medium-large xp"}}
	savedMods := modules
	modules = []moduleFactory{
		func(h *Hub) Module { r.m = newParties(h).(*parties); return r.m },
		func(h *Hub) Module { r.q = newQuoinsWith(h, d); return r.q },
	}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = savedMods
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &psTimer{at: r.now.Add(d), fn: fn}
		r.due = append(r.due, tm)
		return func() bool { was := !tm.done; tm.done = true; return was }
	}
	if ledger == nil {
		ledger = &fakeSpends{packs: map[int64]map[string]int{}, currants: map[int64]int64{}}
	}
	r.ledger = ledger
	r.m.spaces.ledger = ledger
	r.m.spaces.spawn = func(fn func()) {
		if r.hold {
			r.held = append(r.held, fn)
			return
		}
		fn()
	}
	n := idBase
	r.m.spaces.newID = func() string { n++; return fmt.Sprintf("%016x", n) }
	return r
}

// advance runs the clock d forward, firing what comes due on the way, in order.
func (r *psRig) advance(d time.Duration) {
	end := r.now.Add(d)
	for {
		var next *psTimer
		for _, tm := range r.due {
			if !tm.done && !tm.at.After(end) && (next == nil || tm.at.Before(next.at)) {
				next = tm
			}
		}
		if next == nil {
			break
		}
		next.done = true
		if next.at.After(r.now) {
			r.now = next.at
		}
		next.fn()
	}
	r.now = end
}

func (r *psRig) open(a *Client, pack string) string {
	r.t.Helper()
	r.give(a.ID(), pack, 0)
	r.do(a, map[string]any{"t": "party_space_start", "pack": pack})
	r.advance(0)
	opened := one2(r.t, drain(a), "party_space_opened")
	return opened["space"].(map[string]any)["street"].(string)
}

// restart is the server stopping (its shutdown hooks write the files) and a new one starting at at.
func (r *psRig) restart(at time.Time) *psRig {
	r.t.Helper()
	r.hub.Shutdown()
	return newPsRig(r.t, r.path, at, r.ledger, 1000)
}

func (r *psRig) file() partyFile {
	r.t.Helper()
	var f partyFile
	b, err := os.ReadFile(r.path)
	if err != nil || json.Unmarshal(b, &f) != nil {
		r.t.Fatalf("the party file: %s (%v)", b, err)
	}
	return f
}

func start(t *testing.T) *psRig {
	return newPsRig(t, filepath.Join(t.TempDir(), "party.json"), time.Unix(1_700_000_000, 0), nil, 0)
}

func TestAPartyAndItsSpaceOutliveARestart(t *testing.T) {
	r := start(t)
	a, b, c := r.threesome()
	street := r.open(a, "party_pack_toxic_moon")
	r.join(a, street)
	r.join(b, street)
	r.ledger.currants[2] = 5000
	r.advance(10 * time.Minute)
	r.do(b, map[string]any{"t": "party_space_extend", "minutes": 10}) // Bo pays 4500 for ten more
	r.advance(0)
	if one2(t, drain(b), "party_space_extended")["cost"] != float64(4500) {
		t.Fatal("the ATM did not take Bo's currants")
	}
	r.do(a, map[string]any{"t": "party_invite", "id": c.ID()}) // an invite still waiting
	r.advance(time.Second)
	f := r.file()
	if len(f.Parties) != 1 || f.Parties[0].Space == nil || len(f.Parties[0].Space.Spends) != 2 || len(f.Parties[0].Invites) != 1 {
		t.Fatalf("the file kept %+v", f)
	}

	// Down for 30 seconds (the invite has 90); the space had 60 - 10 + 10 = 60 minutes left at the save.
	r2 := r.restart(r.now.Add(30 * time.Second))
	left := time.Hour - time.Second - 30*time.Second
	a2 := r2.connect(1, "Ada")
	am := drain(a2)
	if ok, roster := lastRoster(t, am); !ok || roster != "Ada::leader,Bo::away" {
		t.Fatalf("Ada's party after the restart: %v %q", ok, roster)
	}
	s := spaceOf(t, am)
	if s["street"] != street || s["left"] != float64(left.Milliseconds()) || s["extensions"] != float64(1) {
		t.Fatalf("Ada's space after the restart: %v", s)
	}
	r2.join(a2, street)
	if a2.Street() != street {
		t.Fatalf("Ada could not go back into the space: %q", a2.Street())
	}
	b2 := r2.connect(2, "Bo")
	r2.join(b2, street)
	if b2.Street() != street || spaceOf(t, drain(b2))["street"] != street {
		t.Fatal("Bo is not back in the space")
	}
	// Back within the grace, as after a short drop: the roster shows Bo there again.
	if ok, roster := lastRoster(t, drain(a2)); !ok || roster != "Ada:"+street+":leader,Bo:"+street {
		t.Fatalf("Ada's roster with Bo back: %q", roster)
	}
	c2 := r2.connect(3, "Cy")
	cm := drain(c2)
	inv := one2(t, cm, "party_invited")
	if secs, _ := inv["timeout"].(float64); secs <= 0 || secs > partyPromptSeconds {
		t.Fatalf("Cy's invite came back without its time: %v", inv)
	}
	r2.join(c2, street)
	if c2.Street() == street {
		t.Fatal("Cy, not in the party, got into its space")
	}
	if r2.ledger.asks != 0 {
		t.Fatalf("a space that came back was refunded: %v", r2.ledger.unspent)
	}

	// The clock runs on from where it was: the warning at five minutes left, then the end.
	drain(a2)
	r2.advance(left - partySpaceWarning)
	// The restored invite ran out at its own time on the way.
	if got := texts(drain(a2)); got != "Cy has declined your invitation (expired) | "+partyTextWarning {
		t.Fatalf("Ada heard %q at five minutes left", got)
	}
	r2.advance(5 * time.Minute)
	am = drain(a2)
	if got := texts(am); got != partyTextExpired || one2(t, am, "party_space")["why"] != "expired" || a2.Street() == street {
		t.Fatalf("the end: %q %v %q", got, am, a2.Street())
	}
}

func TestASpaceWhoseClockRanOutWhileTheServerWasDownClosesAsItWould(t *testing.T) {
	r := start(t)
	a, b, _ := r.threesome()
	street := r.open(a, "party_pack_taster_toxic_moon") // ten minutes
	r.join(a, street)
	r2 := r.restart(r.now.Add(20 * time.Minute))
	a2 := r2.connect(1, "Ada")
	am := drain(a2)
	if got := texts(am); got != partyTextExpired {
		t.Fatalf("Ada heard %q", got)
	}
	if sp := one2(t, am, "party_space"); sp["space"] != nil || sp["why"] != "expired" {
		t.Fatalf("Ada's space: %v", sp)
	}
	r2.join(a2, street)
	if a2.Street() == street {
		t.Fatal("Ada got into a space that ended")
	}
	// Bo, back later, hears it too, once.
	r2.advance(30 * time.Second)
	b2 := r2.connect(2, "Bo")
	bm := drain(b2)
	if texts(bm) != partyTextExpired || one2(t, bm, "party_space")["why"] != "expired" {
		t.Fatalf("Bo heard %v", bm)
	}
	r2.do(b2, map[string]any{"t": "leave"})
	b3 := r2.connect(2, "Bo")
	if strings.Contains(texts(drain(b3)), partyTextExpired) {
		t.Fatal("Bo heard the end twice")
	}
	if r2.ledger.asks != 0 {
		t.Fatal("a space that ran its course was refunded")
	}
	_ = b
}

func TestASpaceThatCannotComeBackIsGivenBackOnce(t *testing.T) {
	r := start(t)
	a, b, _ := r.threesome()
	street := r.open(a, "party_pack_toxic_moon")
	r.ledger.currants[1] = 5000
	r.join(a, street)
	r.do(a, map[string]any{"t": "party_space_extend", "minutes": 10})
	r.advance(0)
	drain(a)
	r.hub.Shutdown()
	// This build no longer knows the template.
	b0, _ := os.ReadFile(r.path)
	os.WriteFile(r.path, []byte(strings.ReplaceAll(string(b0), `"template":"moon"`, `"template":"bash"`)), 0o600)

	r2 := newPsRig(t, r.path, r.now.Add(time.Minute), r.ledger, 1000)
	a2 := r2.connect(1, "Ada")
	r2.advance(0)
	am := drain(a2)
	if r2.ledger.packs[1]["party_pack_toxic_moon"] != 1 || r2.ledger.currants[1] != 5000 {
		t.Fatalf("the pack and the currants did not come back: %v %v", r2.ledger.packs[1], r2.ledger.currants[1])
	}
	refunds := kinds(am, "party_space_refunded")
	if len(refunds) != 2 {
		t.Fatalf("Ada was told of %d refunds: %v", len(refunds), am)
	}
	for _, rf := range refunds {
		if rf["refund"] != fmt.Sprint(rf["spend"])+"-r" || rf["text"] != partyTextRefunded {
			t.Fatalf("refund %v", rf)
		}
	}
	if ok, _ := lastRoster(t, am); !ok {
		t.Fatal("the party did not come back without its space")
	}
	if sp := one2(t, am, "party_space"); sp["space"] != nil || sp["why"] != "closed" {
		t.Fatalf("Ada's space: %v", sp)
	}
	b2 := r2.connect(2, "Bo")
	if sp := one2(t, drain(b2), "party_space"); sp["why"] != "closed" {
		t.Fatalf("Bo's space: %v", sp)
	}
	if f := r2.file(); len(f.Owed) != 0 || len(f.Notices) != 2 {
		t.Fatalf("the file after the refunds: %+v", f)
	}

	// Another restart: nothing is given twice, and Ada is told again (the page applies it once).
	r3 := r2.restart(r2.now.Add(time.Minute))
	a3 := r3.connect(1, "Ada")
	r3.advance(0)
	if len(kinds(drain(a3), "party_space_refunded")) != 2 || len(r3.ledger.unspent) != 2 || r3.ledger.asks != 2 {
		t.Fatalf("after a second restart: %v (asked %d)", r3.ledger.unspent, r3.ledger.asks)
	}
	// A day on, the notices are gone.
	r4 := r3.restart(r3.now.Add(25 * time.Hour))
	a4 := r4.connect(1, "Ada")
	if len(kinds(drain(a4), "party_space_refunded")) != 0 {
		t.Fatal("a day-old refund was told again")
	}
	_ = b
}

func TestABadPartyFileGivesBackWhatItNames(t *testing.T) {
	r := start(t)
	a, _, _ := r.threesome()
	r.open(a, "party_pack_toxic_moon")
	r.hub.Shutdown()
	b0, _ := os.ReadFile(r.path)
	cut := strings.Index(string(b0), `"spends"`)
	bad := string(b0)[:cut] + `"spends":[{"op":"party-0000000000000001","user":1,"why":"party_pack","until":` + fmt.Sprint(r.now.Add(time.Hour).UnixMilli()) + `},{"op":"party-00000000000000ff","user":1,"why":"party_atm","until":` + fmt.Sprint(r.now.Add(-time.Hour).UnixMilli()) + `}` // torn
	os.WriteFile(r.path, []byte(bad), 0o600)

	r2 := newPsRig(t, r.path, r.now.Add(time.Minute), r.ledger, 1000)
	if aside, _ := filepath.Glob(r.path + ".bad-*"); len(aside) != 1 {
		t.Fatalf("the bad file was not moved aside: %v", aside)
	}
	a2 := r2.connect(1, "Ada")
	r2.advance(0)
	if r2.ledger.packs[1]["party_pack_toxic_moon"] != 1 || len(kinds(drain(a2), "party_space_refunded")) != 1 {
		t.Fatalf("the pack named in the bad file did not come back: %v", r2.ledger.packs[1])
	}
	if r2.ledger.asks != 1 {
		t.Fatalf("a spend whose space had ended anyway was refunded too (%d asks)", r2.ledger.asks)
	}
	if f := r2.file(); len(f.Parties) != 0 || len(f.Owed) != 0 {
		t.Fatalf("the next save wrote %+v", f)
	}
}

func TestASpendTheLedgerHadWhenTheServerStoppedIsGivenBack(t *testing.T) {
	r := start(t)
	a, _, _ := r.threesome()
	r.give(1, "party_pack_toxic_moon", 0)
	r.hold = true
	r.do(a, map[string]any{"t": "party_space_start", "pack": "party_pack_toxic_moon"})
	if f := r.file(); len(f.Asked) != 1 {
		t.Fatalf("the spend with the ledger is not in the file: %+v", f)
	}
	// The site settles it, but the server stops before it hears.
	r.held[0]()
	r.held = nil
	r2 := r.restart(r.now.Add(time.Minute))
	if len(r2.ledger.spent) != 1 {
		t.Fatal("the spend did not settle")
	}
	a2 := r2.connect(1, "Ada")
	r2.advance(0)
	if r2.ledger.packs[1]["party_pack_toxic_moon"] != 1 || len(kinds(drain(a2), "party_space_refunded")) != 1 {
		t.Fatalf("the settled pack was not given back: %v", r2.ledger.packs[1])
	}

	// One that never reached the site is voided, so it can never settle late, and nothing is told.
	r3 := start(t)
	a3, _, _ := r3.threesome()
	r3.give(1, "party_pack_toxic_moon", 0)
	r3.hold = true
	r3.do(a3, map[string]any{"t": "party_space_start", "pack": "party_pack_toxic_moon"})
	late := r3.held[0]
	r4 := r3.restart(r3.now.Add(time.Minute))
	a4 := r4.connect(1, "Ada")
	r4.advance(0)
	if len(kinds(drain(a4), "party_space_refunded")) != 0 || r4.ledger.asks != 1 {
		t.Fatal("a spend that never came was told as given back")
	}
	late() // the old process's request arrives after all
	if len(r4.ledger.spent) != 0 || r4.ledger.packs[1]["party_pack_toxic_moon"] != 1 {
		t.Fatalf("a voided spend settled late: %v", r4.ledger.spent)
	}
}

func TestARefundIsAskedAgainUntilTheSiteAnswers(t *testing.T) {
	r := start(t)
	a, _, _ := r.threesome()
	r.open(a, "party_pack_toxic_moon")
	r.hub.Shutdown()
	b0, _ := os.ReadFile(r.path)
	os.WriteFile(r.path, []byte(strings.ReplaceAll(string(b0), `"pack":"party_pack_toxic_moon"`, `"pack":"party_pack_gone"`)), 0o600)
	r.ledger.silent = true
	r2 := newPsRig(t, r.path, r.now.Add(time.Minute), r.ledger, 1000)
	a2 := r2.connect(1, "Ada")
	r2.advance(0)
	r2.advance(partyRefundRetryMin)
	r2.advance(2 * partyRefundRetryMin)
	if r2.ledger.asks != 3 || len(r2.file().Owed) != 1 {
		t.Fatalf("asked %d times, owed %+v", r2.ledger.asks, r2.file().Owed)
	}
	r2.ledger.silent = false
	r2.advance(4 * partyRefundRetryMin)
	r2.advance(time.Hour)
	if len(r2.ledger.unspent) != 1 || len(r2.file().Owed) != 0 || len(kinds(drain(a2), "party_space_refunded")) != 1 {
		t.Fatalf("the refund: %v, owed %+v", r2.ledger.unspent, r2.file().Owed)
	}
}

func TestMembersWhoDoNotComeBackAfterARestartLogOut(t *testing.T) {
	r := start(t)
	a, _, _ := r.threesome()
	r.open(a, "party_pack_toxic_moon")
	r2 := r.restart(r.now.Add(time.Minute))
	a2 := r2.connect(1, "Ada")
	drain(a2)
	r2.advance(partyRestartGrace)
	if got := texts(drain(a2)); got != "Party member Bo has gone offline" {
		t.Fatalf("Ada heard %q", got)
	}
	// Ada goes too: with nobody online the party closes and its space with it (party.js player_logout).
	r2.do(a2, map[string]any{"t": "leave"})
	r2.hub.Disconnect(a2)
	r2.advance(partyGrace)
	if len(r2.m.all) != 0 || len(r2.m.spaces.byStreet) != 0 || r2.ledger.asks != 0 {
		t.Fatalf("the party is still open (%d) or was refunded (%d)", len(r2.m.all), r2.ledger.asks)
	}
}

func TestPartyQuoinsInASpaceOutliveARestart(t *testing.T) {
	dir := t.TempDir()
	saved := quoinsFileForTest
	quoinsFileForTest = filepath.Join(dir, "quoins.json")
	defer func() { quoinsFileForTest = saved }()
	r := newPsRig(t, filepath.Join(dir, "party.json"), time.Unix(1_700_000_000, 0), nil, 0)
	a, b, _ := r.threesome()
	street := r.open(a, "party_pack_toxic_moon")
	r.do(b, map[string]any{"t": "join", "street": street, "x": 100, "y": 528})
	r.do(b, map[string]any{"t": "quoin_claim", "street": street, "i": 0})
	if len(kinds(drain(b), "quoin_got")) != 1 {
		t.Fatal("Bo did not get the party quoin")
	}
	r2 := r.restart(r.now.Add(time.Minute))
	a2 := r2.connect(1, "Ada")
	r2.do(a2, map[string]any{"t": "join", "street": street, "x": 100, "y": 528})
	r2.do(a2, map[string]any{"t": "quoin_claim", "street": street, "i": 0})
	if one2(t, drain(a2), "quoin_refused")["why"] != "gone" {
		t.Fatal("the grabbed party quoin came back with the restart")
	}
}

func TestNoPartyFileWithoutAPath(t *testing.T) {
	r := newSpaceRig(t)
	a, _, _ := r.threesome()
	r.open(a, "party_pack_toxic_moon")
	if r.m.persist.path != "" || !r.m.persist.resumed || len(r.m.persist.asked) != 0 {
		t.Fatalf("a rig without a file keeps one: %+v", r.m.persist)
	}
}
