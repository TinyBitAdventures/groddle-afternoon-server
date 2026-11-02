package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// The game's shared cases (tests/fixtures/word-filter-vectors.json, copied by its tools/build-word-filter.mjs).
func TestWordFilterPassesTheSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/word-filter-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Vectors []struct {
			In  string `json:"in"`
			Out string `json:"out"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Vectors) < 100 {
		t.Fatalf("only %d vectors", len(v.Vectors))
	}
	for _, c := range v.Vectors {
		if got := filterWords(c.In); got != c.Out {
			t.Errorf("filterWords(%q) = %q, want %q", c.In, got, c.Out)
		}
		if got := filterWords(c.Out); got != c.Out {
			t.Errorf("filtering %q again gave %q", c.Out, got)
		}
		if hasBadWord(c.In) != (c.In != c.Out) {
			t.Errorf("hasBadWord(%q) = %v", c.In, hasBadWord(c.In))
		}
	}
}

func TestWordFilterKeepsTheLengthAndCyclesTheSymbols(t *testing.T) {
	if got := filterWords("fuck"); got != "#!@$" {
		t.Fatalf("fuck -> %q", got)
	}
	long := strings.Repeat("u", 40)
	in := "f" + long + "ck off"
	got := filterWords(in)
	if len(got) != len(in) || !strings.HasSuffix(got, " off") || !strings.HasPrefix(got, "#!@$%&*#!@$%&*") {
		t.Fatalf("%q -> %q", in, got)
	}
}

// Street, channel and party chat: everyone (the speaker too) gets the grawlix; the log keeps the words and marks the line.
func TestWordFilterChatIsHeardFilteredAndLoggedAsTyped(t *testing.T) {
	r := newChatRig(t, oct31)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.send(ada, map[string]any{"t": "join", "street": "GA5HERE"})
	r.send(bo, map[string]any{"t": "join", "street": "GA5HERE"})
	drain(ada)
	drain(bo)
	r.send(ada, map[string]any{"t": "chat", "text": "oh fuck, a rock"})
	for _, cl := range []*Client{ada, bo} {
		if got := kinds(drain(cl), "said"); len(got) != 1 || got[0]["text"] != "oh #!@$, a rock" {
			t.Fatalf("local: %v", got)
		}
	}
	r.send(ada, map[string]any{"t": "chat", "text": "hello class"})
	if got := kinds(drain(bo), "said"); len(got) != 1 || got[0]["text"] != "hello class" {
		t.Fatalf("clean local: %v", got)
	}
	r.now = r.now.Add(time.Minute)
	for _, p := range []struct {
		cl *Client
		id int64
	}{{ada, 1}, {bo, 2}} {
		r.send(p.cl, map[string]any{"t": "groups_watch", "token": chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": p.id, "g": "", "c": "global", "s": "", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
		r.send(p.cl, map[string]any{"t": "channel_join", "channel": "global"})
	}
	drain(ada)
	drain(bo)
	r.send(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "sh1t happens"})
	for _, cl := range []*Client{ada, bo} {
		if got := kinds(drain(cl), "channel_said"); len(got) != 1 || got[0]["text"] != "#!@$ happens" {
			t.Fatalf("global: %v", got)
		}
	}
	r.now = r.now.Add(time.Minute)
	r.send(ada, map[string]any{"t": "party_invite", "id": 2})
	r.send(bo, map[string]any{"t": "party_accept", "from": 1})
	drain(ada)
	drain(bo)
	r.send(bo, map[string]any{"t": "party_chat", "text": "you bastard!"})
	for _, cl := range []*Client{ada, bo} {
		if got := kinds(drain(cl), "party_said"); len(got) != 1 || got[0]["text"] != "you #!@$%&*!" {
			t.Fatalf("party: %v", got)
		}
	}
	got := r.lines("2026-10-31")
	want := []struct {
		text, shown string
	}{{"oh fuck, a rock", "oh #!@$, a rock"}, {"hello class", ""}, {"sh1t happens", "#!@$ happens"}, {"you bastard!", "you #!@$%&*!"}}
	if len(got) != len(want) {
		t.Fatalf("%d lines: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Text != w.text || got[i].Shown != w.shown || got[i].Filtered != (w.shown != "") {
			t.Fatalf("line %d: %+v, want %+v", i, got[i], w)
		}
	}
	// The console's call returns them as written.
	_, out := r.query(map[string]any{"q": "bastard"})
	lines, _ := out["lines"].([]any)
	if len(lines) != 1 || lines[0].(map[string]any)["filtered"] != true || lines[0].(map[string]any)["shown"] != "you #!@$%&*!" {
		t.Fatalf("query: %v", out)
	}
}

// A channel's history (sent to a joiner) is the filtered line.
func TestWordFilterChannelHistoryIsFiltered(t *testing.T) {
	r := newChanRig(t)
	ada := r.connect(1, "Ada")
	r.send(ada, map[string]any{"t": "groups_watch", "token": chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": 1, "g": "", "c": "global", "s": "", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	r.send(ada, map[string]any{"t": "channel_join", "channel": "global"})
	r.send(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "what the fvck"})
	if h := r.m.rooms["global"].history; len(h) != 1 || h[0].Text != "what the #!@$" {
		t.Fatalf("history: %+v", h)
	}
}

// The Butler says the owner's words filtered, reports a visitor's filtered, and a Butler message and a gift note arrive filtered.
func TestWordFilterButlerAndNotes(t *testing.T) {
	r := newButlerRig(t)
	ada, bo := r.awayWithVisitor()
	drain(ada)
	drain(bo)
	r.do(bo, map[string]any{"t": "chat", "text": "this place is shit"})
	if got := kinds(drain(ada), "butler_im"); len(got) != 1 || got[0]["text"] != "this place is #!@$" {
		t.Fatalf("speech report: %v", got)
	}
	r.do(ada, map[string]any{"t": "butler_announce", "text": "piss off"})
	if got := kinds(drain(bo), "butler_said"); len(got) != 1 || got[0]["text"] != "#!@$ off" {
		t.Fatalf("announce: %v", got)
	}
	if got := kinds(drain(ada), "butler_im"); len(got) != 1 || got[0]["text"] != "#!@$ off" {
		t.Fatalf("confirmAnnounce: %v", got)
	}
	r.do(ada, map[string]any{"t": "butler_tell", "text": "go away, wanker"})
	if got := kinds(drain(bo), "butler_said"); len(got) != 1 || !strings.Contains(got[0]["text"].(string), "'go away, #!@$%&'") {
		t.Fatalf("tell: %v", got)
	}

	h := newHelpRig(t)
	_, hb := h.visiting()
	h.do(hb, map[string]any{"t": "butler_leave", "owner": 7, "text": "for you, bitch"})
	h.settle()
	if len(h.site.calls) != 1 || h.site.calls[0]["text"] != "for you, #!@$%" {
		t.Fatalf("butler message: %+v", h.site.calls)
	}

	g := newGiftRig(t)
	ga, _ := g.together()
	g.do(ga, map[string]any{"t": "gift_give", "to": 9, "item": map[string]any{"c": "note", "n": 1}, "note": map[string]any{"title": "Dear dickhead", "body": "line one\nfuck you"}})
	g.settle()
	if len(g.site.calls) != 1 || g.site.calls[0].Note == nil || g.site.calls[0].Note.Title != "Dear #!@$%&*#" || g.site.calls[0].Note.Body != "line one\n#!@$ you" {
		t.Fatalf("gift note: %+v", g.site.calls)
	}
}

// A charades word with a bad word is refused (it would be said to the street at the end).
func TestWordFilterRefusesABadCharadesWord(t *testing.T) {
	r := newVerbsRig(t)
	ada, _ := r.pair()
	r.do(ada, map[string]any{"t": "charades_start", "word": "Shit Head"})
	if s := verbOne(t, drain(ada), "charades_started"); s["status"] != "refused" || s["why"] != "word" || s["text"] != "Please pick a different word." {
		t.Fatalf("bad word: %v", s)
	}
	r.do(ada, map[string]any{"t": "charades_start", "word": "cocktail"})
	if s := verbOne(t, drain(ada), "charades_started"); s["status"] != "ok" {
		t.Fatalf("a fine word: %v", s)
	}
}
