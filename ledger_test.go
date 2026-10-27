package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeLedger answers settle and void from a script, and records the calls.
type fakeLedger struct {
	mu      sync.Mutex
	settles []tradeRecord
	voids   []string
	answer  func(kind string, trade string) ledgerAnswer
}

func (f *fakeLedger) settle(r *tradeRecord) ledgerAnswer {
	f.mu.Lock()
	f.settles = append(f.settles, *r)
	f.mu.Unlock()
	return f.answer("settle", r.Trade)
}

func (f *fakeLedger) void(r *tradeRecord) ledgerAnswer {
	f.mu.Lock()
	f.voids = append(f.voids, r.Trade)
	f.mu.Unlock()
	return f.answer("void", r.Trade)
}

// ledgerRig is the trade rig with a ledger whose calls run in place (their answers wait for fire(0)).
func ledgerRig(t *testing.T, journal string, answer func(kind, trade string) ledgerAnswer) (*tradeRig, *fakeLedger) {
	r := newTradeRig(t, journal)
	f := &fakeLedger{answer: answer}
	r.m.ledger = f
	r.m.spawn = func(fn func()) { fn() }
	return r, f
}

// accepted opens a trade, both offer (Ada 3 apples and 50 currants, Bo 7 cherries) and both accept.
func accepted(r *tradeRig, a, b *Client) string {
	id := r.open(a, b)
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 1, "items": []map[string]any{{"c": "apple", "n": 3}}, "currants": 50})
	r.do(b, map[string]any{"t": "trade_offer", "trade": id, "v": 1, "items": []map[string]any{{"c": "cherry", "n": 7}}, "currants": 0})
	r.do(a, map[string]any{"t": "trade_accept", "trade": id, "mine": 1, "seen": 1})
	drain(a)
	drain(b)
	r.do(b, map[string]any{"t": "trade_accept", "trade": id, "mine": 1, "seen": 1})
	return id
}

func status(s string) func(string, string) ledgerAnswer {
	return func(string, string) ledgerAnswer { return ledgerAnswer{Status: s} }
}

func TestLedgerSettlesBeforeTheDone(t *testing.T) {
	r, f := ledgerRig(t, "", status("settled"))
	a, b := r.two()
	id := accepted(r, a, b)
	ga, gb := drain(a), drain(b)
	if len(kinds(ga, "trade_done")) != 0 || len(kinds(gb, "trade_done")) != 0 {
		t.Fatal("no done before the ledger answers")
	}
	if s := lastState(t, ga); s["settling"] != true {
		t.Fatalf("both see it settling: %v", s)
	}
	if len(f.settles) != 1 || f.settles[0].Trade != id || f.settles[0].Gave[0].Currants != 50 || f.settles[0].Gave[1].Items[0].C != "cherry" {
		t.Fatalf("the site is asked to settle exactly what was accepted: %+v", f.settles)
	}
	// While it settles nobody can change, cancel or walk away from it.
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 2, "items": []map[string]any{}, "currants": 0})
	r.do(b, map[string]any{"t": "trade_cancel", "trade": id})
	if e := errorsOf(drain(a)); len(e) != 1 || e[0] != tradeTextSettling {
		t.Fatalf("an offer while settling: %q", e)
	}
	if e := errorsOf(drain(b)); len(e) != 1 || e[0] != tradeTextSettling {
		t.Fatalf("a cancel while settling: %q", e)
	}
	r.do(a, map[string]any{"t": "join", "street": "GOTHER"})
	r.fire(tradeSettle)
	r.hub.Disconnect(b)
	drain(a)
	if r.fire(0) != 1 {
		t.Fatal("the answer comes back under the hub lock")
	}
	if d := kinds(drain(a), "trade_done"); len(d) != 1 || d[0]["trade"] != id {
		t.Fatalf("settled: Ada hears the done: %v", d)
	}
	b2 := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	if d := kinds(drain(b2), "trade_done"); len(d) != 1 {
		t.Fatal("Bo, who went offline while it settled, hears it on connect")
	}
}

func TestLedgerRefusalCancelsWithTheSourceWords(t *testing.T) {
	r, _ := ledgerRig(t, "", func(string, string) ledgerAnswer {
		return ledgerAnswer{Status: "refused", Short: &ledgerShort{ID: 1, C: "apple"}}
	})
	a, b := r.two()
	id := accepted(r, a, b)
	drain(a)
	drain(b)
	r.fire(0)
	ga, gb := drain(a), drain(b)
	if len(kinds(ga, "trade_cancel")) != 1 || len(kinds(gb, "trade_cancel")) != 1 || len(kinds(ga, "trade_done")) != 0 {
		t.Fatalf("a refused trade is cancelled for both: %v / %v", ga, gb)
	}
	if e := errorsOf(ga); len(e) != 1 || e[0] != tradeTextTooMany {
		t.Fatalf("the short side hears inc_trading.js:456: %q", e)
	}
	if e := errorsOf(gb); len(e) != 1 || e[0] != tradeTextNotFinished {
		t.Fatalf("the other side hears nothing changed hands: %q", e)
	}
	r.now = r.now.Add(10 * time.Second)
	r.do(a, map[string]any{"t": "trade_check", "trade": id})
	if len(kinds(drain(a), "trade_void")) != 1 {
		t.Fatal("afterwards the trade is void, so the hold comes back")
	}
	r.do(a, map[string]any{"t": "trade_start", "id": 2})
	if len(kinds(drain(a), "trade_open")) != 1 {
		t.Fatal("and both may trade again")
	}
}

func TestLedgerSilenceVoidsAndAVoidCanFindItSettled(t *testing.T) {
	calls := 0
	r, f := ledgerRig(t, "", func(kind, _ string) ledgerAnswer {
		calls++
		switch {
		case kind == "settle":
			return ledgerAnswer{} // the answer was lost
		case calls == 2:
			return ledgerAnswer{} // the first void too
		default:
			return ledgerAnswer{Status: "settled"} // the settle had landed after all
		}
	})
	a, b := r.two()
	id := accepted(r, a, b)
	drain(a)
	drain(b)
	r.fire(0) // settle: no answer -> void
	r.fire(0) // void: no answer -> retry later
	if len(f.voids) != 1 {
		t.Fatalf("no answer to the settle: void it (%v)", f.voids)
	}
	r.do(a, map[string]any{"t": "trade_check", "trade": id})
	if m := drain(a); len(kinds(m, "trade_void")) != 0 || len(kinds(m, "trade_done")) != 0 {
		t.Fatalf("while the ledger has not said, a check says neither done nor void: %v", m)
	}
	if r.fire(2*time.Second) != 1 {
		t.Fatal("the void is asked again after a pause")
	}
	r.fire(0)
	if len(kinds(drain(a), "trade_done")) != 1 || len(kinds(drain(b), "trade_done")) != 1 {
		t.Fatal("the void found it settled: both hear the done")
	}
}

func TestLedgerSettlingSurvivesARestart(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "trades.jsonl")
	r, _ := ledgerRig(t, journal, func(string, string) ledgerAnswer { return ledgerAnswer{} })
	a, b := r.two()
	id := accepted(r, a, b)
	r.m.journal.f.Close() // the server stops before any answer

	r2, f2 := ledgerRig(t, journal, status("void"))
	if len(r2.m.settling) != 1 {
		t.Fatalf("the journal brings the settling trade back: %v", r2.m.settling)
	}
	a2 := r2.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r2.do(a2, map[string]any{"t": "join", "street": "GSTREET"})
	drain(a2)
	r2.now = r2.now.Add(10 * time.Second)
	r2.do(a2, map[string]any{"t": "trade_start", "id": 2})
	if e := errorsOf(drain(a2)); len(e) != 1 || e[0] != tradeTextUnsettled {
		t.Fatalf("unsettled until the ledger says: %q", e)
	}
	if len(f2.voids) != 1 || f2.voids[0] != id {
		t.Fatalf("connecting asks the site to void it: %v", f2.voids)
	}
	r2.fire(0)
	if v := kinds(drain(a2), "trade_void"); len(v) != 1 || v[0]["trade"] != id {
		t.Fatal("void: the hold comes back")
	}
	r3, _ := ledgerRig(t, journal, status("void"))
	if len(r3.m.settling) != 0 {
		t.Fatal("the journal remembers it was voided")
	}
}

func TestLedgerClampsToolPointsAndRefusesWithoutASite(t *testing.T) {
	r, _ := ledgerRig(t, "", status("settled"))
	a, b := r.two()
	id := r.open(a, b)
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 1, "items": []map[string]any{{"c": "pick", "n": 1, "r": 999999}}, "currants": 0})
	s := lastState(t, drain(b))
	if it := side(s, "theirs")["items"].([]any)[0].(map[string]any); it["r"] != float64(100) {
		t.Fatalf("a pick carries at most its 100 points (trade.json pts): %v", it)
	}
	r2 := newTradeRig(t, "")
	r2.m.noLedger = true
	a2, b2 := r2.two()
	r2.do(a2, map[string]any{"t": "trade_start", "id": b2.ID()})
	if e := errorsOf(drain(a2)); len(e) != 1 || e[0] != tradeTextNoLedger {
		t.Fatalf("no ledger set up: %q", e)
	}
}

func TestServiceSignatureMatchesTheSite(t *testing.T) {
	// The same vector as spike tests/ledger.test.js (ServiceCall::sign).
	got := signService([]byte(strings.Repeat("x", 40)), "POST", ledgerRouteTrade, 1700000000, "0123456789abcdef", []byte(`{"trade":"abc-1"}`))
	if got != "1700000000.0123456789abcdef.kpcQjQJ2Jv_-2M5av5r1Ed_EjG7VlizazcQYJxvE_1E" {
		t.Fatalf("signature %s", got)
	}
}

func TestHTTPLedgerSignsRetriesAndReadsAnswers(t *testing.T) {
	secret := []byte(strings.Repeat("s", 40))
	var mu sync.Mutex
	seen := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		seen++
		n := seen
		mu.Unlock()
		body, _ := io.ReadAll(req.Body)
		parts := strings.SplitN(req.Header.Get("X-Groddle-Service"), ".", 3)
		var at int64
		json.Unmarshal([]byte(parts[0]), &at)
		route := strings.TrimPrefix(req.URL.Path, "/wp-json")
		if len(parts) != 3 || signService(secret, "POST", route, at, parts[1], body) != req.Header.Get("X-Groddle-Service") {
			w.WriteHeader(401)
			return
		}
		switch {
		case n == 1:
			w.WriteHeader(502) // a hiccup: tried again
		case route == ledgerRouteTrade:
			w.WriteHeader(409)
			w.Write([]byte(`{"status":"refused","short":{"id":7,"c":"apple"}}`))
		default:
			w.Write([]byte(`{"status":"void"}`))
		}
	}))
	defer srv.Close()
	l := &httpLedger{base: srv.URL, secret: secret, client: srv.Client(), now: time.Now, pause: func(time.Duration) {}}
	a := l.settle(&tradeRecord{Trade: "t-1", IDs: [2]int64{7, 8}})
	if a.Status != "refused" || a.Short == nil || a.Short.ID != 7 || a.Short.C != "apple" {
		t.Fatalf("refused after one retry: %+v", a)
	}
	if v := l.void(&tradeRecord{Trade: "t-1", IDs: [2]int64{7, 8}}); v.Status != "void" {
		t.Fatalf("void: %+v", v)
	}
	bad := &httpLedger{base: srv.URL, secret: []byte("wrong"), client: srv.Client(), now: time.Now, pause: func(time.Duration) {}}
	if v := bad.void(&tradeRecord{Trade: "t-1"}); v.Status != "" {
		t.Fatalf("a refused signature is no answer: %+v", v)
	}
}
