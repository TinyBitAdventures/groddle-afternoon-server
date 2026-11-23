package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeQuarterSite stands in for the site's groddle/v1/ledger/quarter routes: one row per lot,
// one lot per player (groddle_quarter_lots' keys), and a player with no home street refused.
type fakeQuarterSite struct {
	mu      sync.Mutex
	rows    map[string]quarterRow // "street#lot"
	names   map[int64]string
	noHome  map[int64]bool
	claims  []quarterCall
	frees   []quarterCall
	loads   int
	silent  bool
	loadErr error
}

func newFakeQuarterSite() *fakeQuarterSite {
	return &fakeQuarterSite{rows: map[string]quarterRow{}, names: map[int64]string{7: "Ada", 9: "Bo", 11: "Cy"}, noHome: map[int64]bool{}}
}

func (f *fakeQuarterSite) table() []quarterRow {
	out := []quarterRow{}
	for _, r := range f.rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Lot < out[j].Lot })
	return out
}

func (f *fakeQuarterSite) quarterClaim(c quarterCall) quarterAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims = append(f.claims, c)
	if f.silent {
		return quarterAnswer{}
	}
	key := fmt.Sprintf("%s#%d", c.Street, c.Lot)
	if r, ok := f.rows[key]; ok {
		if r.Owner == c.ID {
			return quarterAnswer{Status: "settled", Lot: c.Lot, Lots: f.table()}
		}
		return quarterAnswer{Status: "refused", Why: "taken", Lots: f.table()}
	}
	for _, r := range f.rows {
		if r.Owner == c.ID {
			return quarterAnswer{Status: "refused", Why: "have", Lots: f.table()}
		}
	}
	if f.noHome[c.ID] {
		return quarterAnswer{Status: "refused", Why: "no_home", Lots: f.table()}
	}
	f.rows[key] = quarterRow{Street: c.Street, Lot: c.Lot, Owner: c.ID, Name: f.names[c.ID]}
	return quarterAnswer{Status: "settled", Lot: c.Lot, Lots: f.table()}
}

func (f *fakeQuarterSite) quarterRelease(c quarterCall) quarterAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frees = append(f.frees, c)
	if f.silent {
		return quarterAnswer{}
	}
	for k, r := range f.rows {
		if r.Owner == c.ID {
			delete(f.rows, k)
			return quarterAnswer{Status: "settled", Street: r.Street, Lot: r.Lot, Lots: f.table()}
		}
	}
	return quarterAnswer{Status: "refused", Why: "none", Lots: f.table()}
}

func (f *fakeQuarterSite) quarterLoad() ([]quarterRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.table(), nil
}

type quarterRig struct {
	t       *testing.T
	hub     *Hub
	m       *quarter
	site    *fakeQuarterSite
	now     time.Time
	timers  []*rigTimer
	pending []func()
}

const (
	quarterBlock = "GQRMM1100BLOCK1"
	quarterOther = "GLI32HBOUTD1GQV" // Marylpole Mount, the parent street
)

func newQuarterRig(t *testing.T, site *fakeQuarterSite) *quarterRig {
	if site == nil {
		site = newFakeQuarterSite()
	}
	r := &quarterRig{t: t, now: time.Unix(1_700_000_000, 0), site: site}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.m = newQuarter(h).(*quarter); return r.m }}
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
	r.m.newID = func() string { n++; return fmt.Sprintf("%016x", n) }
	r.m.load()
	r.settle()
	return r
}

func (r *quarterRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

func (r *quarterRig) do(cl *Client, msg map[string]any) {
	r.now = r.now.Add(3 * time.Second)
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *quarterRig) at(cl *Client, street string) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": 100, "y": -100})
}

func (r *quarterRig) settle() {
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

func (r *quarterRig) claim(cl *Client, lot int) {
	r.do(cl, map[string]any{"t": "quarter_claim", "lot": lot})
	r.settle()
}

func quarterOwners(t *testing.T, msg map[string]any) map[int]float64 {
	t.Helper()
	out := map[int]float64{}
	for _, row := range msg["lots"].([]any) {
		m := row.(map[string]any)
		out[int(m["lot"].(float64))] = m["owner"].(map[string]any)["id"].(float64)
	}
	return out
}

func TestTheQuarterDataNamesTheBlockAndItsTenLots(t *testing.T) {
	d := parseQuarterData(quartersJSON)
	b, ok := d.Streets[quarterBlock]
	if !ok || b.Label != "Marylpole Mount Quarter, 1100 block" {
		t.Fatalf("the first block (generate_label '<Name> Quarter, <x><y>00 block'): %+v", d)
	}
	if len(b.Lots) != 10 || b.Lots[0] != 1105 || b.Lots[9] != 1195 {
		t.Fatalf("ten lots 1105..1195: %v", b.Lots)
	}
}

func TestAClaimIsEveryonesAndASecondLotOrAHeldLotIsRefused(t *testing.T) {
	r := newQuarterRig(t, nil)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.at(ada, quarterBlock)
	r.at(bo, quarterBlock)
	if st := pick(t, drain(bo), "quarter_lots"); len(st["lots"].([]any)) != 0 {
		t.Fatalf("all for sale at first: %v", st)
	}
	drain(ada)

	r.claim(ada, 1105)
	got := pick(t, drain(ada), "quarter_claimed")
	if got["status"] != "settled" || got["lot"] != float64(1105) {
		t.Fatalf("Ada has 1105: %v", got)
	}
	if o := quarterOwners(t, pick(t, drain(bo), "quarter_lots")); o[1105] != 7 {
		t.Fatalf("Bo sees Ada's lot at once: %v", o)
	}

	// Bo asks for Ada's lot: refused here, the site never asked.
	asked := len(r.site.claims)
	r.claim(bo, 1105)
	if got := pick(t, drain(bo), "quarter_claimed"); got["status"] != "refused" || got["why"] != "taken" || got["text"] != quarterTextTaken {
		t.Fatalf("a held lot is refused: %v", got)
	}
	// Ada asks for a second: has_other.
	r.claim(ada, 1115)
	if got := pick(t, drain(ada), "quarter_claimed"); got["why"] != "have" || got["text"] != quarterTextHave {
		t.Fatalf("one lot per player (inc_pols.js has_other): %v", got)
	}
	if len(r.site.claims) != asked {
		t.Fatalf("refused here without asking the site: %d", len(r.site.claims))
	}
	if r.hub.provided["quarter.owner"].(func(string, int) int64)(quarterBlock, 1105) != 7 {
		t.Fatalf("the table holds Ada's lot")
	}
}

func TestTheSiteIsTheTruthWhenTheTableHereIsBehind(t *testing.T) {
	site := newFakeQuarterSite()
	r := newQuarterRig(t, site)
	bo := r.connect(9, "Bo")
	r.at(bo, quarterBlock)
	drain(bo)
	// Cy claimed 1125 on another server's watch: the site has it, this table does not.
	site.rows[quarterBlock+"#1125"] = quarterRow{Street: quarterBlock, Lot: 1125, Owner: 11, Name: "Cy"}
	r.claim(bo, 1125)
	frames := drain(bo)
	if got := pick(t, frames, "quarter_claimed"); got["status"] != "refused" || got["why"] != "taken" {
		t.Fatalf("the site refuses: %v", got)
	}
	if o := quarterOwners(t, pick(t, frames, "quarter_lots")); o[1125] != 11 {
		t.Fatalf("and its table reaches the block: %v", o)
	}
}

func TestGivingALotUpPutsItBackOnSale(t *testing.T) {
	r := newQuarterRig(t, nil)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.at(ada, quarterBlock)
	r.at(bo, quarterOther)
	r.claim(ada, 1135)
	drain(ada)
	// Bo, away on Marylpole Mount, hears nothing of the block.
	if frames := of(drain(bo), "quarter_lots"); len(frames) != 0 {
		t.Fatalf("only the block hears: %v", frames)
	}
	// Ada gives it up from anywhere.
	r.at(ada, quarterOther)
	drain(ada)
	r.do(ada, map[string]any{"t": "quarter_release"})
	r.settle()
	if got := pick(t, drain(ada), "quarter_released"); got["status"] != "settled" || got["lot"] != float64(1135) {
		t.Fatalf("given up: %v", got)
	}
	r.at(bo, quarterBlock)
	if st := pick(t, drain(bo), "quarter_lots"); len(st["lots"].([]any)) != 0 {
		t.Fatalf("for sale again: %v", st)
	}
	r.claim(bo, 1135)
	if got := pick(t, drain(bo), "quarter_claimed"); got["status"] != "settled" {
		t.Fatalf("Bo may claim it now: %v", got)
	}
	// Nothing to give up.
	r.do(ada, map[string]any{"t": "quarter_release"})
	r.settle()
	if got := pick(t, drain(ada), "quarter_released"); got["why"] != "none" {
		t.Fatalf("no lot: %v", got)
	}
}

func TestAClaimNeedsTheBlockALotAndAnAnswer(t *testing.T) {
	site := newFakeQuarterSite()
	site.noHome[11] = true
	r := newQuarterRig(t, site)
	ada, cy := r.connect(7, "Ada"), r.connect(11, "Cy")
	r.at(ada, quarterOther)
	r.claim(ada, 1105)
	if got := pick(t, drain(ada), "quarter_claimed"); got["why"] != "lot" {
		t.Fatalf("only on the block: %v", got)
	}
	r.at(ada, quarterBlock)
	r.claim(ada, 1100)
	if got := pick(t, drain(ada), "quarter_claimed"); got["why"] != "lot" {
		t.Fatalf("a lot the block has: %v", got)
	}
	r.at(cy, quarterBlock)
	r.claim(cy, 1145)
	if got := pick(t, drain(cy), "quarter_claimed"); got["why"] != "no_home" || got["text"] != quarterTextHome {
		t.Fatalf("the site's no_home: %v", got)
	}
	site.silent = true
	r.claim(ada, 1145)
	if got := pick(t, drain(ada), "quarter_claimed"); got["why"] != "off" {
		t.Fatalf("no answer is off: %v", got)
	}
	if len(r.m.asking) != 0 || len(r.m.claiming) != 0 {
		t.Fatalf("nothing left waiting: %v %v", r.m.asking, r.m.claiming)
	}
}

func TestALotTheSiteIsWeighingIsTakenForEveryoneElse(t *testing.T) {
	r := newQuarterRig(t, nil)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.at(ada, quarterBlock)
	r.at(bo, quarterBlock)
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "quarter_claim", "lot": 1155})
	r.do(bo, map[string]any{"t": "quarter_claim", "lot": 1155})
	if got := pick(t, drain(bo), "quarter_claimed"); got["why"] != "taken" {
		t.Fatalf("Bo is refused while Ada's claim is weighed: %v", got)
	}
	r.do(ada, map[string]any{"t": "quarter_claim", "lot": 1165})
	if got := pick(t, drain(ada), "quarter_claimed"); got["why"] != "busy" {
		t.Fatalf("one at a time: %v", got)
	}
	r.settle()
	if got := pick(t, drain(ada), "quarter_claimed"); got["status"] != "settled" || got["lot"] != float64(1155) {
		t.Fatalf("Ada's first claim settles: %v", got)
	}
	if len(r.site.claims) != 1 {
		t.Fatalf("the site was asked once: %d", len(r.site.claims))
	}
}

func TestQuarterLoadsAgainAfterAFailureAndSurvivesARestart(t *testing.T) {
	site := newFakeQuarterSite()
	site.loadErr = fmt.Errorf("down")
	r := newQuarterRig(t, site)
	if r.m.loaded {
		t.Fatalf("not loaded while the site is down")
	}
	ada := r.connect(7, "Ada")
	r.at(ada, quarterBlock)
	r.claim(ada, 1105)
	if got := pick(t, drain(ada), "quarter_claimed"); got["why"] != "off" {
		t.Fatalf("claims wait for the load: %v", got)
	}
	site.loadErr = nil
	for _, tm := range r.timers {
		if tm.d == quarterLoadRetry && !tm.stopped {
			tm.stopped = true
			tm.fn()
		}
	}
	r.settle()
	if !r.m.loaded {
		t.Fatalf("loaded on the retry")
	}
	r.claim(ada, 1105)
	drain(ada)
	r2 := newQuarterRig(t, site)
	if r2.hub.provided["quarter.owner"].(func(string, int) int64)(quarterBlock, 1105) != 7 {
		t.Fatalf("a restart loads Ada's lot from the site")
	}
}
