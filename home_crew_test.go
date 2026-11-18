package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// crewOnHome puts Ada (7) on her own home street with Bo (9) and Cy (11) visiting it.
func (r *yardRig) crewOnHome() (ada, bo, cy *Client) {
	ada, bo, cy = r.visiting()
	r.join(ada, home(7))
	drain(ada)
	drain(bo)
	drain(cy)
	return ada, bo, cy
}

// frameOf is the one frame of kind among frames (others may come with it).
func crewFrame(t *testing.T, frames []map[string]any, kind string) map[string]any {
	t.Helper()
	var out map[string]any
	for _, f := range frames {
		if f["t"] == kind {
			if out != nil {
				t.Fatalf("two %s in %v", kind, frames)
			}
			out = f
		}
	}
	if out == nil {
		t.Fatalf("no %s in %v", kind, frames)
	}
	return out
}

func crewOf(t *testing.T, f map[string]any, id string) []float64 {
	t.Helper()
	crews, _ := f["crews"].(map[string]any)
	var out []float64
	list, _ := crews[id].([]any)
	for _, v := range list {
		out = append(out, v.(float64))
	}
	return out
}

// #464 (M3): a visitor who starts swinging at the owner's rock while she swings helps her, and
// everyone on the street sees the crew (mining.js addMiner 68-91, getVerbLabel 8-23).
func TestAHomeCrewAnswersHelpingAndShowsItsMiners(t *testing.T) {
	r := newYardRig(t)
	ada, bo, cy := r.crewOnHome()
	r.do(ada, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:0", "do": "start"})
	if ok := crewFrame(t, drain(ada), "home_crew_ok"); ok["helping"] != false || ok["id"] != "rock:0" {
		t.Fatalf("Ada swings alone: %v", ok)
	}
	drain(bo)
	drain(cy)
	r.do(bo, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:0", "do": "start"})
	frames := drain(bo)
	if ok := crewFrame(t, frames, "home_crew_ok"); ok["helping"] != true {
		t.Fatalf("Bo helps: %v", ok)
	}
	if got := crewOf(t, crewFrame(t, drain(cy), "home_crews"), "rock:0"); !reflect.DeepEqual(got, []float64{7, 9}) {
		t.Fatalf("Cy sees the crew: %v", got)
	}
	// Someone who arrives while the crew works is told who is at what.
	dee := r.connect(12, "Dee")
	r.pass(dee, 7, "Ada", false)
	r.join(dee, home(7))
	if got := crewOf(t, crewFrame(t, drain(dee), "home_crews"), "rock:0"); len(got) != 2 {
		t.Fatalf("Dee arrives to the crew: %v", got)
	}
}

// The owner's own swing lands in her game: she asks, and the bonus is one help's min(1, 4) = 1
// chunk, naming Bo (mining.js 327-342, 373). Bo's later swing has nobody's help: he joined her.
func TestTheOwnersSwingGetsItsCrewBonus(t *testing.T) {
	r := newYardRig(t)
	ada, bo, _ := r.crewOnHome()
	r.do(ada, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:0", "do": "start"})
	r.do(bo, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:0", "do": "start"})
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:0", "do": "land", "req": 4})
	f := crewFrame(t, drain(ada), "home_crew_help")
	help, _ := f["help"].(map[string]any)
	if f["req"] != float64(4) || help["bonus"] != float64(1) || !reflect.DeepEqual(help["by"], []any{"Bo"}) {
		t.Fatalf("Ada's landing: %v", f)
	}
	r.do(bo, map[string]any{"t": "home_yard", "req": 5, "owner": 7, "kind": "rock", "id": "rock:0", "verb": "mine"})
	r.settle()
	if c := r.site.calls[len(r.site.calls)-1]; c.Help != nil {
		t.Fatalf("nobody helped Bo: %+v", c.Help)
	}
	if done := crewFrame(t, drain(bo), "home_yard_done"); done["help"] != nil {
		t.Fatalf("no help line: %v", done)
	}
	// A visitor cannot claim a landing of his own here: his go through the site.
	r.do(bo, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:0", "do": "land", "req": 6})
	if got := drain(bo); len(got) != 0 {
		t.Fatalf("Bo's land is ignored: %v", got)
	}
}

// A visitor helped by the owner and another visitor: his landed swing takes the bonus to the site,
// which rolls it into what it takes and credits, and his answer names the helpers.
func TestAVisitorsHelpedSwingTakesTheBonusToTheSite(t *testing.T) {
	r := newYardRig(t)
	ada, bo, cy := r.crewOnHome()
	r.site.answer.Act = json.RawMessage(`{"id":"yd-0000000000000001","kind":"rock","target":"rock:0","verb":"mine","took":7,"at":1}`)
	r.do(bo, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:0", "do": "start"})
	r.do(ada, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:0", "do": "start"})
	r.do(cy, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:0", "do": "start"})
	drain(ada)
	drain(bo)
	drain(cy)
	r.do(bo, map[string]any{"t": "home_yard", "req": 8, "owner": 7, "kind": "rock", "id": "rock:0", "verb": "mine"})
	r.settle()
	c := r.site.calls[0]
	// Two helps: min(1,4) + min(2,4) = 3.
	if c.Help == nil || c.Help.Bonus != 3 {
		t.Fatalf("the site is told the bonus: %+v", c.Help)
	}
	frames := drain(bo)
	done := crewFrame(t, frames, "home_yard_done")
	help, _ := done["help"].(map[string]any)
	if help["bonus"] != float64(3) || !reflect.DeepEqual(help["by"], []any{"Ada", "Cy"}) {
		t.Fatalf("Bo's answer: %v", done)
	}
	if got := crewOf(t, crewFrame(t, frames, "home_crews"), "rock:0"); !reflect.DeepEqual(got, []float64{7, 11}) {
		t.Fatalf("Bo is off the rock, the others still at it: %v", got)
	}
}

// Help dig at the owner's Dirt Pile: three helpers give two more items and the 5% Loam chance
// (dirt_pile.js 345-358), the dig's own yield Loam too.
func TestAHomeDigCrewHelpsTheDigger(t *testing.T) {
	r := newYardRig(t)
	ada, bo, cy := r.crewOnHome()
	dee := r.connect(12, "Dee")
	r.pass(dee, 7, "Ada", false)
	r.join(dee, home(7))
	r.crew.roll = func() float64 { return 0.01 }
	id := "node:dirt_pile:0"
	for _, cl := range []*Client{bo, ada, cy, dee} {
		r.do(cl, map[string]any{"t": "home_crew", "owner": 7, "id": id, "do": "start"})
	}
	for _, cl := range []*Client{ada, bo, cy, dee} {
		drain(cl)
	}
	r.do(bo, map[string]any{"t": "home_yard", "req": 1, "owner": 7, "kind": "node", "id": id, "verb": "dig"})
	r.settle()
	if h := r.site.calls[0].Help; h == nil || h.Items != 2 || h.Class != "loam" || !h.Loam {
		t.Fatalf("the site is told: %+v", h)
	}
	if items, loam := homeDigHelp(1, 0.01); items != 1 || loam {
		t.Fatalf("one helper: one item, no Loam chance (%d %v)", items, loam)
	}
	if items, loam := homeDigHelp(6, 0.14); items != 3 || !loam {
		t.Fatalf("six helpers: three items and 15%% (%d %v)", items, loam)
	}
}

// Leaving the street takes a miner's helps back (miningSanityCheck 53-66): the owner then lands alone.
func TestLeavingTheHomeStreetTakesTheCrewHelpsBack(t *testing.T) {
	r := newYardRig(t)
	ada, bo, cy := r.crewOnHome()
	r.do(ada, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:1", "do": "start"})
	r.do(bo, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:1", "do": "start"})
	drain(ada)
	drain(cy)
	r.join(bo, "LA5101HF7F429V")
	drain(bo)
	if got := crewOf(t, crewFrame(t, drain(cy), "home_crews"), "rock:1"); !reflect.DeepEqual(got, []float64{7}) {
		t.Fatalf("Bo left the crew: %v", got)
	}
	drain(ada)
	r.do(ada, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:1", "do": "land"})
	if f := crewFrame(t, drain(ada), "home_crew_help"); f["help"] != nil {
		t.Fatalf("Bo's help went with him: %v", f)
	}
	// A crew only forms on that home street, from someone standing there, never someone blocked.
	r.do(bo, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:1", "do": "start"})
	if got := drain(bo); len(got) != 0 {
		t.Fatalf("Bo is not on the street: %v", got)
	}
	drain(cy)
	r.ign.ign[[2]int64{7, 11}] = true
	r.do(cy, map[string]any{"t": "home_crew", "owner": 7, "id": "rock:1", "do": "start"})
	if got := drain(cy); len(got) != 0 {
		t.Fatalf("Ada blocks Cy: %v", got)
	}
	r.do(ada, map[string]any{"t": "home_crew", "owner": 7, "id": "tree:1", "do": "start"})
	if got := drain(ada); len(got) != 0 {
		t.Fatalf("a tree has no crew: %v", got)
	}
}
