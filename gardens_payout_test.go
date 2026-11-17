package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// G940 (report #469): plots on a bed the layout no longer has (Community Gardens' made-up herb bed)
// are owed to their planters by mail, once, before they are dropped.

const payoutStreet = "GCR101NQ4BU1SKS"

type putCall struct {
	id   string
	user int64
	why  string
	got  tradeGoods
}

func payoutRig(t *testing.T, path string, take bool) (*gardenRig, *[]putCall) {
	t.Helper()
	gardensFileForTest = path
	r := newGardenRig(t, false)
	calls := &[]putCall{}
	r.hub.provided["credits.put"] = func(id string, user int64, why string, got tradeGoods) bool {
		if take {
			*calls = append(*calls, putCall{id, user, why, got})
		}
		return take
	}
	return r, calls
}

func (r *gardenRig) visit(id int64) {
	b := gardenWorldData.streets[payoutStreet]["default"]
	r.player(id, "P", payoutStreet, b.x, b.y)
	r.advance(time.Second)
}

func TestGardenPayoutMailsPlantersOfDroppedBedsOnce(t *testing.T) {
	if gardenWorldData.streets[payoutStreet]["herb"] != nil {
		t.Fatal("Community Gardens still has the made-up herb bed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "realtime-gardens.json")
	defer func() { gardensFileForTest = "" }()
	old := `{"v":1,"saved":1,"streets":{"GCR101NQ4BU1SKS":{` +
		`"herb:1-1":{"s":"seeds","seed":"herb_seed_rubeweed","planter":7},` +
		`"herb:2-1":{"s":"shoots","seed":"herb_seed_rubeweed","planter":7},` +
		`"herb:1-2":{"s":"crop","seed":"herb_seed_purple_flower","planter":8},` +
		`"herb:3-1":{"s":"crop","seed":"herb_seed_rubeweed"},` +
		`"herb:2-2":{"s":"dirty"},` +
		`"default:1-1":{"s":"seeds","seed":"seed_spinach","planter":7}}},"grants":[]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	rube := gardenWorldData.seeds["herb_seed_rubeweed"]
	purple := gardenWorldData.seeds["herb_seed_purple_flower"]

	// No ledger yet: nothing is taken, the payouts wait (and a restart makes the same two, not four).
	r0, _ := payoutRig(t, path, false)
	if len(r0.g.payouts) != 2 {
		t.Fatalf("payouts %v", r0.g.payouts)
	}
	ids := []string{r0.g.payouts[0].ID, r0.g.payouts[1].ID}
	r0.visit(1)
	if len(r0.g.payouts) != 2 {
		t.Fatalf("payouts went with no ledger: %v", r0.g.payouts)
	}
	r0.g.save()
	r0.advance(2 * time.Second)
	r0.hub.Shutdown()
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"herb:`) || !strings.Contains(string(raw), `"payouts"`) {
		t.Fatalf("the file after the first start %s", raw)
	}

	// The ledger takes them on the first connect: one credit per planter, the full produces_count.
	r, calls := payoutRig(t, path, true)
	if len(r.g.payouts) != 2 || r.g.payouts[0].ID != ids[0] || r.g.payouts[1].ID != ids[1] {
		t.Fatalf("a restart made other payouts: %v (was %v)", r.g.payouts, ids)
	}
	if r.g.plots[payoutStreet]["default:1-1"] == nil || len(r.g.plots[payoutStreet]) != 1 {
		t.Fatalf("plots kept %v", r.g.plots[payoutStreet])
	}
	r.visit(2)
	if len(*calls) != 2 || len(r.g.payouts) != 0 {
		t.Fatalf("credits %v, waiting %v", *calls, r.g.payouts)
	}
	byUser := map[int64]putCall{}
	for _, c := range *calls {
		if c.why != "garden" || !strings.HasPrefix(c.id, "cr-g-") || !creditIDRe.MatchString(c.id) {
			t.Fatalf("credit %v", c)
		}
		byUser[c.user] = c
	}
	if g := byUser[7].got.Items; len(g) != 1 || g[0].C != rube.crop || g[0].N != 2*rube.produces {
		t.Fatalf("the planter of two rubeweed plots got %v", g)
	}
	if g := byUser[8].got.Items; len(g) != 1 || g[0].C != purple.crop || g[0].N != purple.produces {
		t.Fatalf("the planter of a ripe purple flower got %v", g)
	}
	r.advance(2 * time.Second)
	r.hub.Shutdown()

	// Paid: the next start has nothing to pay.
	r2, calls2 := payoutRig(t, path, true)
	r2.visit(3)
	if len(*calls2) != 0 || len(r2.g.payouts) != 0 {
		t.Fatalf("paid twice: %v %v", *calls2, r2.g.payouts)
	}
	b, _ := json.Marshal(r2.g.payouts)
	if string(b) != "null" && string(b) != "[]" {
		t.Fatalf("payouts %s", b)
	}
}

func TestGardenPayoutIDsFollowThePlots(t *testing.T) {
	a := gardenPayouts([]gardenOwed{{7, "S", "herb:1-1", "rubeweed", 3}, {7, "S", "herb:2-1", "rubeweed", 3}, {8, "S", "herb:1-2", "purple_flower", 2}})
	b := gardenPayouts([]gardenOwed{{8, "S", "herb:1-2", "purple_flower", 2}, {7, "S", "herb:2-1", "rubeweed", 3}, {7, "S", "herb:1-1", "rubeweed", 3}})
	if len(a) != 2 || a[0].ID != b[0].ID || a[1].ID != b[1].ID || a[0].User != 7 || a[0].Items["rubeweed"] != 6 {
		t.Fatalf("%v vs %v", a, b)
	}
	c := gardenPayouts([]gardenOwed{{7, "S", "herb:1-1", "rubeweed", 3}})
	if c[0].ID == a[0].ID {
		t.Fatal("other plots, the same id")
	}
	if s := payoutSummary(a, 2); !strings.Contains(s, "2 planters owed 8 crops") || !strings.Contains(s, "2 ripe crops") {
		t.Fatalf("summary %q", s)
	}
}

func TestCreditsPutKeepsTheCallersID(t *testing.T) {
	m := &credits{h: NewHub(time.Now), out: newCreditOutbox(nil)}
	got := creditItem("rubeweed", 6)
	if !m.put("cr-g-0123abcd", 7, "garden", got) || m.out.pending() != 1 || m.out.queue[0].ID != "cr-g-0123abcd" || m.out.queue[0].Why != "garden" {
		t.Fatalf("queue %v", m.out.queue)
	}
	for _, bad := range []struct {
		id, why string
		user    int64
	}{{"cr-q-0123", "garden", 7}, {"gg-1", "garden", 7}, {"cr-g-ABC", "garden", 7}, {"cr-g-1", "garden", 0}, {"cr-o-1", "online", 7}, {"cr-g-1", "nope", 7}} {
		if m.put(bad.id, bad.user, bad.why, got) {
			t.Fatalf("took %v", bad)
		}
	}
	if (&credits{h: m.h}).put("cr-g-1", 7, "garden", got) {
		t.Fatal("took a credit with no ledger")
	}
}
