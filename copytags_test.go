package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// The game's shared cases (its tests/fixtures/copy-tag-vectors.json, copied here; the game's
// tests/item-copies-own-state-3.test.js checks the copy is the same file).
func TestCopyTagsPassTheSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/copy-tag-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Now     int64 `json:"now"`
		Vectors []struct {
			Kind string          `json:"kind"`
			In   json.RawMessage `json:"in"`
			Out  json.RawMessage `json:"out"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	table := loadTradeTable(t)
	now := time.UnixMilli(v.Now)
	for _, c := range v.Vectors {
		got := table.Tags.clean(c.Kind, c.In, now)
		var want, have any
		_ = json.Unmarshal(c.Out, &want)
		if got != nil {
			_ = json.Unmarshal(got, &have)
		}
		wj, _ := json.Marshal(want)
		hj, _ := json.Marshal(have)
		if !bytes.Equal(wj, hj) {
			t.Errorf("%s %s: got %s, want %s", c.Kind, c.In, hj, wj)
		}
		// Checked words check the same again.
		if got != nil {
			if again := table.Tags.clean(c.Kind, got, now); !bytes.Equal(again, got) {
				t.Errorf("%s %s: again %s", c.Kind, got, again)
			}
		}
	}
}

func loadTradeTable(t *testing.T) tradeTable {
	t.Helper()
	var table tradeTable
	if err := json.Unmarshal(tradeJSON, &table); err != nil {
		t.Fatal(err)
	}
	if table.Tags.Name.Max != 32 || table.Tags.Phrases.Max != 3 || table.Items["npc_cubimal_chick"].Tag != "name" || table.Items["potion_favor"].Tag != "giant" || table.Items["still"].Tag != "still" || table.Items["pumpkin_lit_1"].Tag != "phrases" || table.Items["teleportation_script"].Tag != "script" || table.Tags.Script.ID != 32 {
		t.Fatalf("the trade table's tag rules: %+v", table.Tags)
	}
	return table
}

func TestTagsOnlyRideOnOneCopyOfAClassThatKeepsThem(t *testing.T) {
	table := loadTradeTable(t)
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		it   tradeItem
		want string
	}{
		{tradeItem{C: "npc_cubimal_chick", N: 1, T: json.RawMessage(`"Bob"`)}, `"Bob"`},
		{tradeItem{C: "npc_cubimal_chick", N: 1, T: json.RawMessage(`"fuck"`)}, `"#!@$"`},
		{tradeItem{C: "npc_cubimal_chick", N: 2, T: json.RawMessage(`"Bob"`)}, ``},
		{tradeItem{C: "apple", N: 1, T: json.RawMessage(`"Bob"`)}, ``},
		{tradeItem{C: "potion_favor", N: 1, T: json.RawMessage(`"Bob"`)}, ``},
		{tradeItem{C: "potion_favor", N: 1, T: json.RawMessage(`"lem"`)}, `"lem"`},
		{tradeItem{C: "pumpkin_lit_2", N: 1, T: json.RawMessage(`["Boo", "<i>x</i>"]`)}, `["Boo","ix/i"]`},
		{tradeItem{C: "still", N: 1, T: json.RawMessage(`{"hopper":700,"hooch":4}`)}, `{"hopper":600,"hooch":4,"workingSince":0}`},
		{tradeItem{C: "still", N: 1, T: json.RawMessage(`"` + strings.Repeat("x", 5000) + `"`)}, ``},
	}
	for _, c := range cases {
		it := c.it
		table.cleanTag(&it, now)
		if string(it.T) != c.want {
			t.Errorf("%s x%d %s: %q, want %q", c.it.C, c.it.N, c.it.T, it.T, c.want)
		}
	}
}

// A trade carries each one-copy slot's words to the other side, checked; the other side's own
// offer is theirs alone.
func TestATradeCarriesACopysWords(t *testing.T) {
	r := newTradeRig(t, "")
	a, b := r.two()
	id := r.open(a, b)
	r.do(a, map[string]any{"t": "trade_offer", "trade": id, "v": 1, "items": []map[string]any{{"c": "npc_cubimal_chick", "n": 1, "r": 0, "t": "Shit Bob"}, {"c": "apple", "n": 2, "t": "x"}}, "currants": 0})
	sb := lastState(t, drain(b))
	items := side(sb, "theirs")["items"].([]any)
	if items[0].(map[string]any)["t"] != "#!@$ Bob" || items[1].(map[string]any)["t"] != nil {
		t.Fatalf("Bo sees Ada's slots: %v", items)
	}
	r.do(b, map[string]any{"t": "trade_offer", "trade": id, "v": 1, "items": []map[string]any{{"c": "potion_favor", "n": 1, "r": 7, "t": "humbaba"}}, "currants": 0})
	drain(a)
	drain(b)
	r.do(a, map[string]any{"t": "trade_accept", "trade": id, "mine": 1, "seen": 1})
	r.do(b, map[string]any{"t": "trade_accept", "trade": id, "mine": 1, "seen": 1})
	da := kinds(drain(a), "trade_done")
	db := kinds(drain(b), "trade_done")
	if len(da) != 1 || len(db) != 1 {
		t.Fatalf("done: %v %v", da, db)
	}
	ja, _ := json.Marshal(da[0]["got"])
	jb, _ := json.Marshal(db[0]["got"])
	if string(ja) != `{"currants":0,"items":[{"c":"potion_favor","n":1,"r":7,"t":"humbaba"}]}` || string(jb) != `{"currants":0,"items":[{"c":"npc_cubimal_chick","n":1,"r":0,"t":"#!@$ Bob"},{"c":"apple","n":2}]}` {
		t.Fatalf("got: Ada %s, Bo %s", ja, jb)
	}
}

func TestAGiftCarriesACopysWordsAndABoxItsContents(t *testing.T) {
	r := newGiftRig(t)
	ada, bo := r.together()
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "ref": "s1", "item": map[string]any{"c": "npc_cubimal_chick", "n": 1, "r": 0, "t": "Bob"}})
	r.settle()
	if c := r.site.calls[0]; string(c.Gave.Items[0].T) != `"Bob"` {
		t.Fatalf("the site is asked: %+v", c.Gave.Items)
	}
	drain(ada)
	if got := only(t, drain(bo), "gift_got"); got["item"].(map[string]any)["t"] != "Bob" {
		t.Fatalf("Bo is told: %v", got)
	}
	r.do(ada, map[string]any{"t": "gift_give", "to": 9, "ref": "s2", "box": []map[string]any{{"c": "still", "n": 1, "r": 0, "t": map[string]any{"hopper": 140, "hooch": 2}}, {"c": "cherry", "n": 3, "t": "no"}}})
	r.settle()
	drain(ada)
	got := only(t, drain(bo), "gift_got")
	box, _ := json.Marshal(got["box"])
	if string(box) != `[{"c":"still","n":1,"r":0,"t":{"hooch":2,"hopper":140,"workingSince":0}},{"c":"cherry","n":3}]` {
		t.Fatalf("the box: %s", box)
	}
}

func TestAStackOnTheGroundKeepsItsWordsForWhoeverPicksItUp(t *testing.T) {
	r := newDropRig(t)
	ada, bo, cy := r.trio()
	r.do(ada, map[string]any{"t": "ground_drop", "ref": "d1", "item": map[string]any{"c": "npc_cubimal_chick", "n": 1, "r": 0, "t": "Bob"}, "x": 1050, "y": -120})
	r.settle()
	op := r.site.drops[0].Op
	if string(r.site.drops[0].Item.T) != `"Bob"` {
		t.Fatalf("the site is asked: %+v", r.site.drops[0].Item)
	}
	drain(ada)
	if s := only(t, drain(cy), "ground_added")["stack"].(map[string]any); s["t"] != "Bob" {
		t.Fatalf("the street sees: %v", s)
	}
	// Ada walks away; Bo walks over and picks it up, claiming other words: the stack's own are the ones he gets.
	r.move(ada, 3000, -100)
	r.move(bo, 1050, -120)
	drain(ada)
	drain(bo)
	r.do(bo, map[string]any{"t": "ground_pickup", "id": op, "t2": "Mine", "item": map[string]any{"c": "npc_cubimal_chick", "n": 1, "t": "Mine"}})
	r.settle()
	got := pick(t, drain(bo), "ground_taken")
	if got["status"] != "settled" || got["item"].(map[string]any)["t"] != "Bob" {
		t.Fatalf("Bo takes: %v", got)
	}
	// After a restart the site's stored words come back with the stack (checked again).
	r2 := newDropRig(t)
	r2.site.ground = []dropRow{{ID: "drop-00000000000000aa", By: 7, Name: "Ada", Street: dropStreet, X: 1000, Y: -100, C: "pumpkin_lit_1", N: 1, T: json.RawMessage(`["Boo","fuck"]`), At: r2.now.Unix()}}
	r2.m.loaded = false
	r2.m.load()
	r2.settle()
	dee := r2.connect(13, "Dee")
	r2.at(dee, dropStreet, 0, 0)
	st := pick(t, drain(dee), "ground_state")
	s, _ := json.Marshal(st["stacks"].([]any)[0].(map[string]any)["t"])
	if string(s) != `["Boo","#!@$"]` {
		t.Fatalf("after a restart: %s", s)
	}
}
