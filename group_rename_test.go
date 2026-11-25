package main

import (
	"fmt"
	"net/http"
	"testing"
)

// G1300 (#698): the site's group_rename call puts one note in the group's chat and gives the
// members online the new name; nobody outside the group hears anything.
func TestGroupRenameNotesTheChatAndTellsTheMembers(t *testing.T) {
	r := newChanRig(t)
	a, b, c, d := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy"), r.connect(4, "Di")
	r.watch(a, []int64{12}, "global", "")
	r.watch(b, []int64{7, 12}, "global", "")
	r.watch(c, []int64{7}, "global", "")
	r.watch(d, []int64{12}, "global", "") // a member with the chat closed
	r.send(a, map[string]any{"t": "channel_join", "channel": "group:12"})
	r.send(b, map[string]any{"t": "channel_join", "channel": "group:12"})
	r.send(c, map[string]any{"t": "channel_join", "channel": "group:7"})
	for _, cl := range []*Client{a, b, c, d} {
		drain(cl)
	}

	code, out := groupRenameService(r.hub, []byte(`{"group":12,"name":"Zebra Club","by":"Ada"}`))
	if code != http.StatusOK || out.(map[string]string)["status"] != "ok" {
		t.Fatalf("group_rename %d %v", code, out)
	}
	const want = "[group:12: Ada renamed the group to Zebra Club.]"
	for _, cl := range []*Client{a, b} {
		msgs := drain(cl)
		if n := chanNotes(msgs); fmt.Sprint(n) != want {
			t.Fatalf("%s notes: %v", cl.Name(), n)
		}
		if g := chanKind(msgs, "group_renamed"); len(g) != 1 || g[0]["group"].(float64) != 12 || g[0]["name"] != "Zebra Club" {
			t.Fatalf("%s group_renamed: %v", cl.Name(), g)
		}
	}
	// Di has the chat closed: no note now, the name all the same, and the note when she opens it.
	msgs := drain(d)
	if n := chanNotes(msgs); len(n) != 0 {
		t.Fatalf("Di notes: %v", n)
	}
	if g := chanKind(msgs, "group_renamed"); len(g) != 1 || g[0]["name"] != "Zebra Club" {
		t.Fatalf("Di group_renamed: %v", g)
	}
	r.send(d, map[string]any{"t": "channel_join", "channel": "group:12"})
	j := chanKind(drain(d), "channel_joined")
	if len(j) != 1 {
		t.Fatalf("Di joined: %v", j)
	}
	h := j[0]["history"].([]any)
	if len(h) != 1 || h[0].(map[string]any)["note"] != true || h[0].(map[string]any)["text"] != "Ada renamed the group to Zebra Club." {
		t.Fatalf("Di history: %v", h)
	}
	// Cy is not in group 12.
	if got := drain(c); len(got) != 0 {
		t.Fatalf("Cy heard %v", got)
	}
}

func TestGroupRenameNotesAGroupNobodyHasOpen(t *testing.T) {
	r := newChanRig(t)
	a := r.connect(1, "Ada")
	r.watch(a, []int64{12}, "global", "")
	drain(a)
	// No name from the site: "Someone", as the site and the other notes say.
	if code, _ := groupRenameService(r.hub, []byte(`{"group":12,"name":"Zebra Club"}`)); code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	if g := chanKind(drain(a), "group_renamed"); len(g) != 1 {
		t.Fatalf("group_renamed %v", g)
	}
	r.send(a, map[string]any{"t": "channel_join", "channel": "group:12"})
	h := chanKind(drain(a), "channel_joined")[0]["history"].([]any)
	if len(h) != 1 || h[0].(map[string]any)["text"] != "Someone renamed the group to Zebra Club." {
		t.Fatalf("history %v", h)
	}
}

func TestGroupRenameRefusesWhatIsNotAName(t *testing.T) {
	r := newChanRig(t)
	for _, body := range []string{`{"group":12}`, `{"group":0,"name":"Zebra Club"}`, `{"group":-3,"name":"Zebra Club"}`, `{"group":12,"name":"  "}`, `{"group":12,"name":"a\u0007b"}`, `{"group":12,"name":"abcdefghijklmnopqrstuvwxyzabcdefg"}`, `nope`} {
		if code, _ := groupRenameService(r.hub, []byte(body)); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, code)
		}
	}
	if len(r.m.rooms) != 0 {
		t.Fatalf("rooms %v", r.m.rooms)
	}
}
