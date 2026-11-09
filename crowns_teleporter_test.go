package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTeleLedger is the site's ledger/spend and ledger/spend/refund for the teleporter.
type fakeTeleLedger struct {
	mu      sync.Mutex
	spends  []string
	refunds []string
	answer  string // spend: settled | refused | "" (no answer)
	back    string // refund: void | refused | "" (no answer)
}

func (f *fakeTeleLedger) spend(op string, id int64, gave tradeGoods, why string) ledgerAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spends = append(f.spends, fmt.Sprintf("%s %d %d %d %s", op, id, gave.Currants, len(gave.Items), why))
	return ledgerAnswer{Status: f.answer}
}

func (f *fakeTeleLedger) refundSpend(op string, id int64) ledgerAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunds = append(f.refunds, fmt.Sprintf("%s %d", op, id))
	if f.back == "void" {
		return ledgerAnswer{Status: "void", Gave: []tradeGoods{{Items: []tradeItem{}, Currants: crownsTeleporterPrice}}}
	}
	return ledgerAnswer{Status: f.back}
}

func newTeleRig(t *testing.T) (*crownRig, *fakeTeleLedger) {
	r := newCrownRig(t)
	led := &fakeTeleLedger{answer: "settled", back: "void"}
	r.m.spender = led
	return r, led
}

// atTeleporter connects a player on The forgotten floor.
func (r *crownRig) atTeleporter(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.at(cl, crownsTeleporterStreet, 1060, 600)
	drain(cl)
	return cl
}

// teleport pays and waits out the second; the crown_match it came into.
func (r *crownRig) teleport(cl *Client) map[string]any {
	r.t.Helper()
	r.do(cl, map[string]any{"t": "crown_teleport"})
	r.fire(0)
	p := one(r.t, drain(cl), "crown_paid")
	if p["cost"].(float64) != 50 || p["text"] != "You are transported to a mystical land of fun and adventure!" || p["id"] != p["spend"] {
		r.t.Fatalf("paid %v", p)
	}
	r.do(cl, map[string]any{"t": "crown_ack", "id": p["id"]})
	if r.fire(crownsTeleportJoin) != 1 {
		r.t.Fatal("the join comes a second after paying (apiSetTimerX 1000)")
	}
	mm := one(r.t, drain(cl), "crown_match")
	if mm["open"] != true {
		r.t.Fatalf("a teleporter game is open %v", mm)
	}
	return mm
}

func TestCrownsTeleporterPaysThroughTheLedgerAndJoinsANewGame(t *testing.T) {
	r, led := newTeleRig(t)
	ada := r.atTeleporter(1, "Ada")
	mm := r.teleport(ada)
	if len(led.spends) != 1 || !strings.HasSuffix(led.spends[0], " 1 50 0 game_teleporter") || !strings.HasPrefix(led.spends[0], "gtele-") {
		t.Fatalf("spend %v", led.spends)
	}
	if players := mm["players"].([]any); len(players) != 1 || mm["slot"].(float64) != 0 {
		t.Fatalf("alone in a new game %v", mm)
	}
	if len(r.m.owed[1]) != 0 {
		t.Fatalf("the ack clears crown_paid %v", r.m.owed[1])
	}
	// Alone and clicked: you must wait for more (dismissOverlaysAndStart 259-266).
	r.at(ada, mm["street"].(string), 1180, 243)
	drain(ada)
	r.do(ada, map[string]any{"t": "crown_ready", "match": mm["match"]})
	if rs := one(t, drain(ada), "crown_ready_state"); rs["text"] != "Ada is ready to play. Need 2 more." {
		t.Fatalf("ready %v", rs)
	}
}

func TestCrownsTeleporterFillsTheFullestGameThenOpensAnother(t *testing.T) {
	r, _ := newTeleRig(t)
	names := []string{"Ada", "Bo", "Cy", "Di", "Ed"}
	var cls []*Client
	var first map[string]any
	for i, n := range names {
		cl := r.atTeleporter(int64(i+1), n)
		cls = append(cls, cl)
		mm := r.teleport(cl)
		if i == 0 {
			first = mm
		}
		if i < 4 {
			if mm["match"] != first["match"] || int(mm["slot"].(float64)) != i || len(mm["players"].([]any)) != i+1 {
				t.Fatalf("%s joins the first game in slot %d: %v", n, i, mm)
			}
			if i > 0 {
				j := one(t, drain(cls[0]), "crown_joined")
				if j["name"] != n || len(j["players"].([]any)) != i+1 {
					t.Fatalf("joined %v", j)
				}
			}
		} else if mm["match"] == first["match"] || len(mm["players"].([]any)) != 1 {
			t.Fatalf("four starts, so the fifth opens a new game %v", mm)
		}
	}
}

func TestCrownsTeleporterGameStartsWithThreeReadyAndNobodyUnready(t *testing.T) {
	r, _ := newTeleRig(t)
	var cls []*Client
	var mm map[string]any
	for i, n := range []string{"Ada", "Bo", "Cy", "Di"} {
		cl := r.atTeleporter(int64(i+1), n)
		cls = append(cls, cl)
		mm = r.teleport(cl)
		r.at(cl, mm["street"].(string), r.m.a.Starts[i][0], r.m.a.Starts[i][1])
	}
	for _, cl := range cls {
		drain(cl)
	}
	id := mm["match"]
	for _, cl := range cls[:3] {
		r.do(cl, map[string]any{"t": "crown_ready", "match": id})
	}
	// Three are ready, but Di has not clicked (can_start_game 593-597).
	msgs := drain(cls[0])
	if len(kinds(msgs, "crown_soon")) != 0 {
		t.Fatalf("Di holds the start %v", msgs)
	}
	if rs := kinds(msgs, "crown_ready_state"); rs[2]["text"] != "Cy is ready to play. Just waiting on a few more slackers to HIT THE START BUTTON." {
		t.Fatalf("slackers %v", rs)
	}
	r.do(cls[3], map[string]any{"t": "crown_ready", "match": id})
	one(t, drain(cls[0]), "crown_soon")
	r.fire(crownsSoon)
	one(t, drain(cls[0]), "crown_countdown")
	r.fire(4000 * time.Millisecond)
	if g := one(t, drain(cls[2]), "crown_go"); len(g["scores"].([]any)) != 4 {
		t.Fatalf("four play %v", g)
	}
	// A game under way is not open to the teleporter.
	ed := r.atTeleporter(5, "Ed")
	if e := r.teleport(ed); e["match"] == id {
		t.Fatal("a started game takes nobody new")
	}
}

func TestCrownsTeleporterGameWaitsWhenPlayersLeave(t *testing.T) {
	r, led := newTeleRig(t)
	var cls []*Client
	var mm map[string]any
	for i, n := range []string{"Ada", "Bo", "Cy"} {
		cl := r.atTeleporter(int64(i+1), n)
		cls = append(cls, cl)
		mm = r.teleport(cl)
		r.at(cl, mm["street"].(string), r.m.a.Starts[i][0], r.m.a.Starts[i][1])
	}
	id := mm["match"].(string)
	r.do(cls[0], map[string]any{"t": "crown_ready", "match": id})
	r.do(cls[1], map[string]any{"t": "crown_ready", "match": id})
	r.do(cls[2], map[string]any{"t": "crown_leave", "match": id})
	for _, cl := range cls {
		drain(cl)
	}
	mt := r.m.matches[id]
	if mt == nil || mt.status != "waiting" || mt.closed {
		t.Fatalf("a teleporter game under three waits for more (ticket games end short) %+v", mt)
	}
	// The 50 currants stay spent for leaving (the source never gave them back).
	if len(led.refunds) != 0 {
		t.Fatalf("no refund for leaving %v", led.refunds)
	}
	// Di comes and clicks: three ready, nobody unready.
	di := r.atTeleporter(4, "Di")
	if dm := r.teleport(di); dm["match"] != id || dm["slot"].(float64) != 2 {
		t.Fatalf("Di takes Cy's start %v", dm)
	}
	r.at(di, mt.street, r.m.a.Starts[2][0], r.m.a.Starts[2][1])
	drain(di)
	r.do(di, map[string]any{"t": "crown_ready", "match": id})
	one(t, drain(cls[0]), "crown_soon")
	// Everyone leaves: the game is forgotten.
	r.do(cls[0], map[string]any{"t": "crown_leave", "match": id})
	r.do(cls[1], map[string]any{"t": "crown_leave", "match": id})
	r.do(di, map[string]any{"t": "crown_leave", "match": id})
	if r.m.matches[id] != nil {
		t.Fatal("the last one out ends it")
	}
}

func TestCrownsTeleporterRefusals(t *testing.T) {
	r, led := newTeleRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.at(ada, "GA1", 0, 0)
	drain(ada)
	r.do(ada, map[string]any{"t": "crown_teleport"})
	if f := one(t, drain(ada), "crown_refused"); f["why"] != "street" {
		t.Fatalf("away from the teleporter %v", f)
	}
	r.at(ada, crownsTeleporterStreet, 1060, 600)
	drain(ada)
	led.answer = "refused"
	r.do(ada, map[string]any{"t": "crown_teleport"})
	r.fire(0)
	if f := one(t, drain(ada), "crown_refused"); f["why"] != "poor" || f["text"] != "Oh dear, you don't have enough currants to play!" {
		t.Fatalf("poor %v", f)
	}
	if r.m.busy(1) || len(led.refunds) != 0 {
		t.Fatal("a refused spend leaves nothing behind")
	}
	// Paying while a spend is out: busy.
	led.answer = "settled"
	r.do(ada, map[string]any{"t": "crown_teleport"})
	r.do(ada, map[string]any{"t": "crown_teleport"})
	if f := one(t, drain(ada), "crown_refused"); f["why"] != "busy" {
		t.Fatalf("busy %v", f)
	}
	r.fire(0)
	r.fire(crownsTeleportJoin)
	drain(ada)
	// In a game: busy too.
	r.do(ada, map[string]any{"t": "crown_teleport"})
	if f := one(t, drain(ada), "crown_refused"); f["why"] != "busy" {
		t.Fatalf("busy in a game %v", f)
	}
}

func TestCrownsTeleporterGivesBackASpendThatBoughtNothing(t *testing.T) {
	r, led := newTeleRig(t)
	// No answer from the ledger: it may have settled, so it is given back.
	led.answer = ""
	ada := r.atTeleporter(1, "Ada")
	r.do(ada, map[string]any{"t": "crown_teleport"})
	r.fire(0)
	r.fire(0)
	msgs := drain(ada)
	if f := one(t, msgs, "crown_refused"); f["why"] != "ledger" {
		t.Fatalf("ledger %v", f)
	}
	back := one(t, msgs, "crown_paid_back")
	op := strings.Fields(led.spends[0])[0]
	if back["spend"] != op || back["refund"] != op+"-r" || back["id"] != op+"-r" || back["currants"].(float64) != 50 {
		t.Fatalf("paid back %v", back)
	}
	if len(led.refunds) != 1 || len(r.m.refunds) != 0 {
		t.Fatalf("refund asked once and settled %v %v", led.refunds, r.m.refunds)
	}
	// Owed until acked: sent again on connect.
	r.hub.Disconnect(ada)
	ada = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	if again := one(t, drain(ada), "crown_paid_back"); again["id"] != op+"-r" {
		t.Fatalf("again %v", again)
	}
	r.do(ada, map[string]any{"t": "crown_ack", "id": op + "-r"})
	if len(r.m.owed[1]) != 0 {
		t.Fatal("acked")
	}

	// Paid, then gone before the second was up: the 50 come back.
	led.answer = "settled"
	bo := r.atTeleporter(2, "Bo")
	r.do(bo, map[string]any{"t": "crown_teleport"})
	r.fire(0)
	one(t, drain(bo), "crown_paid")
	r.hub.Disconnect(bo)
	r.fire(crownsTeleportJoin)
	r.fire(0)
	if len(led.refunds) != 2 || len(r.m.matches) != 0 {
		t.Fatalf("Bo's spend given back, no game made %v %v", led.refunds, r.m.matches)
	}
	bo = r.hub.Connect(Claims{Sub: 2, Name: "Bo"})
	msgs = drain(bo)
	if len(kinds(msgs, "crown_paid")) != 1 || len(kinds(msgs, "crown_paid_back")) != 1 || kinds(msgs, "crown_paid_back")[0]["text"] != crownsTeleportBack {
		t.Fatalf("Bo hears the spend (unacked) and its refund %v", msgs)
	}
}

func TestCrownsTeleporterRefundSurvivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crowns.json")
	r, led := newTeleRig(t)
	r.m.useFile(path)
	led.answer, led.back = "", ""
	ada := r.atTeleporter(1, "Ada")
	r.do(ada, map[string]any{"t": "crown_teleport"})
	r.fire(0)
	r.fire(0)
	if len(r.m.refunds) != 1 {
		t.Fatalf("an unanswered refund is owed %v", r.m.refunds)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"refunds"`) {
		t.Fatalf("kept in the file %s", b)
	}
	r2, led2 := newTeleRig(t)
	r2.m.useFile(path)
	if len(r2.m.refunds) != 1 {
		t.Fatal("loaded")
	}
	led2.back = "void"
	ada2 := r2.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r2.fire(0)
	if len(led2.refunds) != 1 || len(r2.m.refunds) != 0 {
		t.Fatalf("asked again on connect %v", led2.refunds)
	}
	if back := one(t, drain(ada2), "crown_paid_back"); back["currants"].(float64) != 50 {
		t.Fatalf("back %v", back)
	}
}
