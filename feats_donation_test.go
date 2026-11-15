package main

import (
	"sync"
	"testing"
	"time"
)

// fakeDonationLedger is the site's ledger/spend for a donation Feat: what each player holds of each class.
type fakeDonationLedger struct {
	mu    sync.Mutex
	held  map[int64]map[string]int
	seen  map[string]string
	calls []string
}

func (f *fakeDonationLedger) spend(op string, id int64, gave tradeGoods, why string) ledgerAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, op+":"+why)
	if s, ok := f.seen[op]; ok {
		return ledgerAnswer{Status: s}
	}
	for _, it := range gave.Items {
		if f.held[id][it.C] < it.N {
			f.seen[op] = "void"
			return ledgerAnswer{Status: "refused", Short: &ledgerShort{ID: id, C: it.C}}
		}
	}
	for _, it := range gave.Items {
		f.held[id][it.C] -= it.N
	}
	f.seen[op] = "settled"
	return ledgerAnswer{Status: "settled"}
}

// donate sends a donation report and returns the feat_counted that answers it (after the ledger's answer).
func donate(r *featRig, cl *Client, n int64, extra ...any) map[string]any {
	msg := map[string]any{"t": "feat_add", "feat": "donate_sparkly", "n": n}
	for i := 0; i+1 < len(extra); i += 2 {
		msg[extra[i].(string)] = extra[i+1]
	}
	r.do(cl, msg)
	r.fire(0)
	got := kinds(drain(cl), "feat_counted")
	if len(got) != 1 {
		r.t.Fatalf("one feat_counted for %v: %v", msg, got)
	}
	return got[0]
}

// G786 (#361): Glitch counted every chunk donated (groups/feat.js increment adds the full amount, the clamp at
// line 29 commented out; inc_feats.js feats_increment passes it through). A donation the ledger confirms counts
// in full at no pace; what it cannot confirm counts at the pace and the rest is carried over, never dropped.
func TestFeatDonationsCountEveryChunkTheLedgerConfirms(t *testing.T) {
	table := featTableForTest(t)
	if table.Feats["donate_sparkly"].Donate != "sparkly" {
		t.Fatalf("the game's table names the class donate_sparkly counts: %+v", table.Feats["donate_sparkly"])
	}
	start := featRunStart(t, table, "donate_sparkly")
	r := newFeatRig(t, "", newFakeFeatLedger(), start.Add(10*time.Minute), nil)
	led := &fakeDonationLedger{held: map[int64]map[string]int{1: {"sparkly": 200}, 2: {"sparkly": 10}, 3: {}}, seen: map[string]string{}}
	r.m.spender = led
	a := r.player(1, "Ada")
	b := r.player(2, "Bo")
	c := r.player(3, "Cy")
	drain(a)
	drain(b)
	drain(c)

	// #361: 189 chunks given, 189 left the ledger: all 189 count, at once.
	op := "fdon-00000000000000a1"
	got := donate(r, a, 189, "op", op, "c", "sparkly")
	if num(got["n"]) != 189 || num(got["mine"]) != 189 || got["reason"] != nil {
		t.Fatalf("a 189-chunk donation counts 189: %v", got)
	}
	if led.held[1]["sparkly"] != 11 || len(led.calls) != 1 || led.calls[0] != op+":feat_donation" {
		t.Fatalf("the chunks left the ledger once: %v %v", led.held[1], led.calls)
	}
	// The same spend again counts nothing and asks the ledger nothing.
	if got = donate(r, a, 189, "op", op, "c", "sparkly"); num(got["n"]) != 0 || got["reason"] != "already" || len(led.calls) != 1 {
		t.Fatalf("a spend id counts once: %v %v", got, led.calls)
	}
	// Another class is no donation for this Feat: the pace.
	if got = donate(r, a, 5, "op", "fdon-00000000000000a2", "c", "beryl"); num(got["n"]) != 5 || len(led.calls) != 1 {
		t.Fatalf("a class the Feat does not name is the client's word, at the pace: %v", got)
	}

	// Bo holds 10 but says 120: the ledger refuses, so the pace counts a stack (50) and carries 70 over.
	if got = donate(r, b, 120, "op", "fdon-00000000000000b1", "c", "sparkly"); num(got["n"]) != 50 {
		t.Fatalf("unconfirmed: the burst now: %v", got)
	}
	if r.m.st.Run.Carry[2] != 70 {
		t.Fatalf("the rest is carried over, not dropped: %v", r.m.st.Run.Carry)
	}
	// Six seconds on, the pace lets two more through.
	r.now = r.now.Add(6 * time.Second)
	r.fire(3 * time.Second)
	counted := kinds(drain(b), "feat_counted")
	sum := int64(0)
	for _, m := range counted {
		sum += int64(num(m["n"]))
	}
	if sum != 2 || r.m.st.Run.Carry[2] != 68 || r.m.st.Run.idx[2].N != 52 {
		t.Fatalf("the carry drains at the pace: %v carry %v", counted, r.m.st.Run.Carry)
	}

	// An older page (no spend id): a stack now, the rest carried over.
	if got = donate(r, c, 189); num(got["n"]) != 50 || r.m.st.Run.Carry[3] != 139 {
		t.Fatalf("a report the ledger cannot check: the pace, the rest carried: %v %v", got, r.m.st.Run.Carry)
	}
	if r.m.st.Run.Total != 189+5+52+50 {
		t.Fatalf("the total: %d", r.m.st.Run.Total)
	}

	// An offline player's carry waits for them.
	r.hub.Disconnect(c)
	r.now = r.now.Add(time.Minute)
	r.fire(3 * time.Second)
	if r.m.st.Run.Carry[3] != 139 {
		t.Fatalf("offline: the carry waits: %v", r.m.st.Run.Carry)
	}
}
