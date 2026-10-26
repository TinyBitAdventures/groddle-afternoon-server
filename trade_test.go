package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tradeRig is a hub with only the trade module, a manual clock and manual timers.
type tradeRig struct {
	t      *testing.T
	hub    *Hub
	m      *trades
	now    time.Time
	timers []*rigTimer
}

func newTradeRig(t *testing.T, journal string) *tradeRig {
	r := &tradeRig{t: t, now: time.Unix(1_700_000_000, 0)}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module {
		var table tradeTable
		if err := json.Unmarshal(tradeJSON, &table); err != nil {
			t.Fatal(err)
		}
		r.m = newTradesWith(h, table, journal)
		return r.m
	}}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

func (r *tradeRig) fire(d time.Duration) int {
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

func (r *tradeRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

// two connects Ada (1) and Bo (2) on one street, frames drained.
func (r *tradeRig) two() (*Client, *Client) {
	a := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	b := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.do(a, map[string]any{"t": "join", "street": "GSTREET"})
	r.do(b, map[string]any{"t": "join", "street": "GSTREET"})
	drain(a)
	drain(b)
	return a, b
}

// open starts a trade between a and b and returns its id.
func (r *tradeRig) open(a, b *Client) string {
	r.do(a, map[string]any{"t": "trade_start", "id": b.ID()})
	ga, gb := drain(a), drain(b)
	oa, ob := kinds(ga, "trade_open"), kinds(gb, "trade_open")
	if len(oa) != 1 || len(ob) != 1 {
		r.t.Fatalf("both sides open the window: %v / %v", ga, gb)
	}
	if oa[0]["trade"] != ob[0]["trade"] || ob[0]["with"].(map[string]any)["name"] != "Ada" || oa[0]["with"].(map[string]any)["name"] != "Bo" {
		r.t.Fatalf("the open names the trade and the other player: %v %v", oa[0], ob[0])
	}
	return oa[0]["trade"].(string)
}

func errorsOf(msgs []map[string]any) []string {
	var out []string
	for _, m := range kinds(msgs, "error") {
		out = append(out, m["message"].(string))
	}
	return out
}

func lastState(t *testing.T, msgs []map[string]any) map[string]any {
	t.Helper()
	s := kinds(msgs, "trade_state")
	if len(s) == 0 {
		t.Fatalf("no trade_state in %v", msgs)
	}
	return s[len(s)-1]
}

func side(m map[string]any, which string) map[string]any { return m[which].(map[string]any) }

func TestTradeTableComesFromTheSource(t *testing.T) {
	var table tradeTable
	if err := json.Unmarshal(tradeJSON, &table); err != nil {
		t.Fatal(err)
	}
	if table.Slots != 6 {
		t.Fatalf("six slots (TradeDialog.as:50), got %d", table.Slots)
	}
	if table.Items["apple"].Max != 100 || table.Items["pick"].Max != 1 {
		t.Fatalf("slot limits are stackmax: apple %d pick %d", table.Items["apple"].Max, table.Items["pick"].Max)
	}
	if table.Untradeable["play_cube"] != "soulbound" || table.Untradeable["artifact_bee_key"] != "no_trade" {
		t.Fatalf("untradeable classes: %v %v", table.Untradeable["play_cube"], table.Untradeable["artifact_bee_key"])
	}
}

func TestTradeStartRules(t *testing.T) {
	r := newTradeRig(t, "")
	a, b := r.two()
	c := r.hub.Connect(Claims{Sub: 3, Name: "Cy"})
	r.do(c, map[string]any{"t": "join", "street": "GOTHER"})
	drain(c)

	r.do(a, map[string]any{"t": "trade_start", "id": 1})
	r.do(a, map[string]any{"t": "trade_start", "id": 99})
	r.do(a, map[string]any{"t": "trade_start", "id": 3})
	got := errorsOf(drain(a))
	want := []string{tradeTextSelf, tradeTextOffline, tradeTextElsewhere}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("refusals %q, want %q", got, want)
	}
	r.now = r.now.Add(10 * time.Second)
	r.do(a, map[string]any{"t": "trade_start", "name": "nobody"})
	r.do(a, map[string]any{"t": "trade_start", "name": "ada"})
	if got := errorsOf(drain(a)); strings.Join(got, "|") != tradeTextNoPlayer+"|"+tradeTextSelf {
		t.Fatalf("by name: %q", got)
	}
	// By name finds someone on your street.
	r.now = r.now.Add(10 * time.Second)
	r.do(a, map[string]any{"t": "trade_start", "name": "BO"})
	if len(kinds(drain(b), "trade_open")) != 1 {
		t.Fatal("trade_start by name opens the trade")
	}
	drain(a)
	// Busy: both the requester and the busy player hear why.
	r.do(c, map[string]any{"t": "join", "street": "GSTREET"})
	drain(a)
	drain(b)
	drain(c)
	r.do(c, map[string]any{"t": "trade_start", "id": 1})
	if got := errorsOf(drain(c)); len(got) != 1 || got[0] != tradeTextTheyBusy {
		t.Fatalf("busy: %q", got)
	}
	if act := kinds(drain(a), "trade_activity"); len(act) != 1 || act[0]["text"] != "Cy wanted to trade with you, but you are busy." {
		t.Fatalf("the busy player hears it: %v", act)
	}
	r.now = r.now.Add(10 * time.Second)
	r.do(a, map[string]any{"t": "trade_start", "id": 3})
	if got := errorsOf(drain(a)); len(got) != 1 || got[0] != tradeTextBusySelf {
		t.Fatalf("already trading: %q", got)
	}
}

func TestTradeStartIsRateLimited(t *testing.T) {
	r := newTradeRig(t, "")
	a, _ := r.two()
	for i := 0; i < tradeStartBurst+2; i++ {
		r.do(a, map[string]any{"t": "trade_start", "id": 1})
	}
	slow := 0
	for _, m := range kinds(drain(a), "error") {
		if m["code"] == "slow_down" {
			slow++
		}
	}
	if slow != 2 {
		t.Fatalf("%d slow_down after the burst", slow)
	}
}

func TestTradeOffersValidateAndChangesResetAcceptance(t *testing.T) {
	r := newTradeRig(t, "")
	a, b := r.two()
	id := r.open(a, b)

	// Ten apples, twenty currants.
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 1, "items": []map[string]any{{"c": "apple", "n": 10}}, "currants": 20})
	sb := lastState(t, drain(b))
	theirs := side(sb, "theirs")
	if theirs["v"].(float64) != 1 || theirs["currants"].(float64) != 20 || len(theirs["items"].([]any)) != 1 {
		t.Fatalf("Bo sees Ada's offer: %v", sb)
	}
	drain(a)

	// Refused: no_trade, soulbound, unknown, over the stack, seven slots, negative currants.
	bad := []map[string]any{
		{"items": []map[string]any{{"c": "artifact_bee_key", "n": 1}}},
		{"items": []map[string]any{{"c": "play_cube", "n": 1}}},
		{"items": []map[string]any{{"c": "not_a_thing", "n": 1}}},
		{"items": []map[string]any{{"c": "apple", "n": 101}}},
		{"items": []map[string]any{{"c": "apple", "n": 0}}},
		{"items": []map[string]any{{"c": "apple", "n": 1}, {"c": "apple", "n": 1}, {"c": "apple", "n": 1}, {"c": "apple", "n": 1}, {"c": "apple", "n": 1}, {"c": "apple", "n": 1}, {"c": "apple", "n": 1}}},
		{"items": []map[string]any{}, "currants": -5},
	}
	for i, extra := range bad {
		msg := map[string]any{"t": "trade_offer", "trade": id, "v": 10 + i}
		for k, v := range extra {
			msg[k] = v
		}
		r.do(a, msg)
		r.now = r.now.Add(time.Second)
	}
	got := drain(a)
	wantErr := []string{tradeTextCantTrade, tradeTextSoulbound, tradeTextCantTrade, tradeTextSlot, tradeTextTooMany, tradeTextSlot, tradeTextCurrants}
	if e := errorsOf(got); strings.Join(e, "|") != strings.Join(wantErr, "|") {
		t.Fatalf("refusals %q", e)
	}
	states := kinds(got, "trade_state")
	if len(states) != len(bad) || states[0]["rejected"].(float64) != 10 || side(states[0], "mine")["v"].(float64) != 1 {
		t.Fatalf("each refusal names the refused version and keeps the last good offer: %v", states)
	}
	if len(kinds(drain(b), "trade_state")) != 0 {
		t.Fatal("a refused offer changes nothing for the other side")
	}

	// Two picks take two slots (stackmax 1 each).
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 30, "items": []map[string]any{{"c": "pick", "n": 1, "r": 40}, {"c": "pick", "n": 1}}, "currants": 0})
	if e := errorsOf(drain(a)); len(e) != 0 {
		t.Fatalf("two pick slots: %q", e)
	}
	drain(b)

	// Bo accepts what he saw; Ada changes her offer; both acceptances go.
	r.do(b, map[string]any{"t": "trade_accept", "trade": id, "mine": 0, "seen": 30})
	if !side(lastState(t, drain(a)), "theirs")["accepted"].(bool) {
		t.Fatal("Ada sees Bo's lock")
	}
	drain(b)
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 31, "items": []map[string]any{{"c": "pick", "n": 1, "r": 40}}, "currants": 0})
	sb = lastState(t, drain(b))
	if side(sb, "mine")["accepted"].(bool) {
		t.Fatal("a change clears the other side's acceptance (inc_trading.js 389-390)")
	}
	drain(a)
	// The same offer again only bumps the version: acceptance stays.
	r.do(b, map[string]any{"t": "trade_accept", "trade": id, "mine": 0, "seen": 31})
	drain(a)
	drain(b)
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 32, "items": []map[string]any{{"c": "pick", "n": 1, "r": 40}}, "currants": 0})
	if !side(lastState(t, drain(b)), "mine")["accepted"].(bool) {
		t.Fatal("an unchanged offer keeps acceptances")
	}
	drain(a)
	// A stale version is ignored.
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 5, "items": []map[string]any{}, "currants": 0})
	if len(drain(b)) != 0 {
		t.Fatal("a stale version is dropped")
	}
	// Accepting an offer you have not seen is refused.
	r.do(a, map[string]any{"t": "trade_accept", "trade": id, "mine": 32, "seen": 99})
	if e := errorsOf(drain(a)); len(e) != 1 || e[0] != tradeTextSeenStale {
		t.Fatalf("stale accept: %q", e)
	}
	// Unlock clears only your own acceptance (inc_trading.js 948): Bo stays accepted.
	drain(b)
	r.do(a, map[string]any{"t": "trade_unlock", "trade": id})
	sa := lastState(t, drain(a))
	if side(sa, "mine")["accepted"].(bool) || !side(sa, "theirs")["accepted"].(bool) {
		t.Fatalf("unlock: %v", sa)
	}
	r.do(b, map[string]any{"t": "trade_unlock", "trade": id})
	if side(lastState(t, drain(a)), "theirs")["accepted"].(bool) {
		t.Fatal("Bo's unlock shows on Ada's side")
	}
}

func TestTradeCompletesOnceAndWaitsForBothAcks(t *testing.T) {
	r := newTradeRig(t, "")
	a, b := r.two()
	id := r.open(a, b)
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 1, "items": []map[string]any{{"c": "apple", "n": 3}}, "currants": 50})
	r.do(b, map[string]any{"t": "trade_offer", "trade": id, "v": 1, "items": []map[string]any{{"c": "cherry", "n": 7}}, "currants": 0})
	drain(a)
	drain(b)
	r.do(a, map[string]any{"t": "trade_accept", "trade": id, "mine": 1, "seen": 1})
	r.do(b, map[string]any{"t": "trade_accept", "trade": id, "mine": 1, "seen": 1})
	da, db := kinds(drain(a), "trade_done"), kinds(drain(b), "trade_done")
	if len(da) != 1 || len(db) != 1 {
		t.Fatalf("both hear the trade is done: %v %v", da, db)
	}
	ja, _ := json.Marshal([]any{da[0]["gave"], da[0]["got"]})
	if string(ja) != `[{"currants":50,"items":[{"c":"apple","n":3}]},{"currants":0,"items":[{"c":"cherry","n":7}]}]` {
		t.Fatalf("Ada gave and got: %s", ja)
	}
	if db[0]["with"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("Bo's done names Ada: %v", db[0])
	}
	// No longer trading; unsettled until acked.
	r.do(a, map[string]any{"t": "trade_accept", "trade": id, "mine": 1, "seen": 1})
	if e := errorsOf(drain(a)); len(e) != 1 || e[0] != tradeTextNotTrading {
		t.Fatalf("after done: %q", e)
	}
	r.now = r.now.Add(10 * time.Second)
	r.do(a, map[string]any{"t": "trade_start", "id": 2})
	if e := errorsOf(drain(a)); len(e) != 1 || e[0] != tradeTextUnsettled {
		t.Fatalf("unsettled: %q", e)
	}
	// A check and a reconnect both bring the done back.
	r.do(a, map[string]any{"t": "trade_check", "trade": id})
	if len(kinds(drain(a), "trade_done")) != 1 {
		t.Fatal("a check answers with the done")
	}
	a2 := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	if len(kinds(drain(a2), "trade_done")) != 1 {
		t.Fatal("a new socket hears the done")
	}
	r.do(a2, map[string]any{"t": "trade_ack", "trade": id})
	r.do(a2, map[string]any{"t": "trade_check", "trade": id})
	if v := kinds(drain(a2), "trade_void"); len(v) != 1 {
		t.Fatal("after the ack a check is void for Ada")
	}
	r.do(b, map[string]any{"t": "trade_check", "trade": id})
	if len(kinds(drain(b), "trade_done")) != 1 {
		t.Fatal("Bo still has it until he acks")
	}
	r.do(b, map[string]any{"t": "trade_ack", "trade": id})
	if len(r.m.done) != 0 {
		t.Fatal("both acks drop the record")
	}
}

func TestTradeCancelLeaveAndDisconnect(t *testing.T) {
	r := newTradeRig(t, "")
	a, b := r.two()
	id := r.open(a, b)
	r.do(b, map[string]any{"t": "trade_cancel", "trade": id})
	ca, cb := kinds(drain(a), "trade_cancel"), kinds(drain(b), "trade_cancel")
	if len(ca) != 1 || len(cb) != 1 || ca[0]["by"].(map[string]any)["name"] != "Bo" || cb[0]["by"] != nil {
		t.Fatalf("cancel: %v %v", ca, cb)
	}
	r.do(a, map[string]any{"t": "trade_check", "trade": id})
	if len(kinds(drain(a), "trade_void")) != 1 {
		t.Fatal("a cancelled trade checks void")
	}

	// A re-join of the same street inside tradeSettle keeps it; leaving ends it.
	r.now = r.now.Add(10 * time.Second)
	id = r.open(a, b)
	r.do(a, map[string]any{"t": "join", "street": "GSTREET"})
	drain(a)
	drain(b)
	r.fire(tradeSettle)
	if r.m.of[1] == nil {
		t.Fatal("a re-join (new outfit) keeps the trade")
	}
	r.do(a, map[string]any{"t": "leave"})
	r.fire(tradeSettle)
	if r.m.of[1] != nil || len(kinds(drain(b), "trade_cancel")) != 1 {
		t.Fatal("leaving the street ends it (player.js:611)")
	}
	drain(a)
	r.do(a, map[string]any{"t": "join", "street": "GSTREET"})
	drain(a)
	drain(b)

	// Joining another street ends it at once.
	r.now = r.now.Add(10 * time.Second)
	r.open(a, b)
	r.do(b, map[string]any{"t": "join", "street": "GOTHER"})
	if r.m.of[1] != nil || len(kinds(drain(a), "trade_cancel")) != 1 {
		t.Fatal("another street ends it")
	}
	r.do(b, map[string]any{"t": "join", "street": "GSTREET"})
	drain(a)
	drain(b)

	// Going offline ends it (player.js:242).
	r.now = r.now.Add(10 * time.Second)
	r.open(a, b)
	r.hub.Disconnect(b)
	if r.m.of[1] != nil || len(kinds(drain(a), "trade_cancel")) != 1 {
		t.Fatal("offline ends it")
	}

	// A new socket for a character ends its open trade.
	b2 := r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	r.do(b2, map[string]any{"t": "join", "street": "GSTREET"})
	drain(a)
	drain(b2)
	r.now = r.now.Add(10 * time.Second)
	r.open(a, b2)
	r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	if r.m.of[2] != nil || len(kinds(drain(b2), "trade_cancel")) != 1 {
		t.Fatal("a replaced socket ends the trade")
	}
}

func TestTradeJournalSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trades.jsonl")
	r := newTradeRig(t, path)
	a, b := r.two()
	id := r.open(a, b)
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 1, "items": []map[string]any{{"c": "apple", "n": 2}}, "currants": 0})
	r.do(a, map[string]any{"t": "trade_accept", "trade": id, "mine": 1, "seen": 0})
	r.do(b, map[string]any{"t": "trade_accept", "trade": id, "mine": 0, "seen": 1})
	drain(a)
	drain(b)
	r.do(a, map[string]any{"t": "trade_ack", "trade": id})
	// A torn line from a crash is skipped.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"done":{"trade":"x`)
	f.Close()

	r2 := newTradeRig(t, path)
	a2 := r2.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	b2 := r2.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	if len(kinds(drain(a2), "trade_done")) != 0 {
		t.Fatal("Ada acked before the restart")
	}
	db := kinds(drain(b2), "trade_done")
	if len(db) != 1 || db[0]["trade"] != id {
		t.Fatalf("Bo hears the done after a restart: %v", db)
	}
	r2.do(b2, map[string]any{"t": "trade_ack", "trade": id})
	r3 := newTradeRig(t, path)
	if len(r3.m.done) != 0 {
		t.Fatal("both acks leave nothing to replay")
	}
}

func TestTradeJournalFailureCancelsInsteadOfCompleting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trades.jsonl")
	r := newTradeRig(t, path)
	a, b := r.two()
	id := r.open(a, b)
	r.m.journal.f.Close() // writes now fail
	r.do(a, map[string]any{"t": "trade_accept", "trade": id, "mine": 0, "seen": 0})
	r.do(b, map[string]any{"t": "trade_accept", "trade": id, "mine": 0, "seen": 0})
	ga := drain(a)
	if len(kinds(ga, "trade_done")) != 0 || len(kinds(ga, "trade_cancel")) != 1 || len(r.m.done) != 0 {
		t.Fatalf("an unjournalled trade must not complete: %v", ga)
	}
}
