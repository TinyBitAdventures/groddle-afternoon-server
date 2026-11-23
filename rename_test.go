package main

import (
	"net/http"
	"testing"
	"time"
)

// G1213 (#636): the site's rename call gives a character its new name on the sockets already open.
func TestRenameReachesEveryoneWithoutAReconnect(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{bud: []int64{9}, rev: []int64{9}})
	bo := r.game(9, "Bo", lists{bud: []int64{7}, rev: []int64{7}})
	web := r.web(11, "Cy", lists{})
	for _, cl := range []*Client{ada, bo} {
		r.do(cl, map[string]any{"t": "join", "street": "GA5", "x": 0, "y": 0})
	}
	r.do(bo, map[string]any{"t": "im_send", "to": 7, "text": "hi"})
	r.settle(presenceSettle)
	drain(ada)
	drain(bo)
	drain(web)

	code, out := renameService(r.hub, []byte(`{"player":7,"name":"Ada Two"}`))
	if code != http.StatusOK || out.(map[string]string)["status"] != "ok" {
		t.Fatalf("rename %d %v", code, out)
	}
	r.settle(presenceSettle)
	for who, cl := range map[string]*Client{"Ada": ada, "Bo": bo, "the web": web} {
		msgs := drain(cl)
		got := one(t, msgs, "renamed")
		if got["id"].(float64) != 7 || got["name"] != "Ada Two" || got["was"] != "Ada" {
			t.Fatalf("%s heard %v", who, got)
		}
		if cl == web {
			if u := one(t, msgs, "presence_up"); u["player"].(map[string]any)["name"] != "Ada Two" {
				t.Fatalf("presence_up %v", u)
			}
		}
	}
	if ada.Name() != "Ada Two" || ada.player.Name != "Ada Two" {
		t.Fatalf("socket name %q, street player %q", ada.Name(), ada.player.Name)
	}
	// What she says from now on is under the new name; a street newcomer sees it over her head.
	r.do(ada, map[string]any{"t": "chat", "text": "new name"})
	if said := one(t, drain(bo), "said"); said["name"] != "Ada Two" {
		t.Fatalf("said %v", said)
	}
	r.do(bo, map[string]any{"t": "leave"})
	r.do(bo, map[string]any{"t": "join", "street": "GA5", "x": 0, "y": 0})
	st := one(t, drain(bo), "street")
	if p := st["players"].([]any)[0].(map[string]any); p["name"] != "Ada Two" {
		t.Fatalf("street %v", st)
	}
	// Her IM conversation keeps the new name; a party invite by the new name finds her.
	r.do(bo, map[string]any{"t": "im_send", "to": 7, "text": "is that you?"})
	if im := one(t, drain(ada), "im"); im["with"].(map[string]any)["name"] != "Bo" {
		t.Fatalf("im %v", im)
	}
	if m, ok := r.hub.ProvidedLocked("ims.self").(*ims); !ok || m.convs[[2]int64{7, 9}].Names[0] != "Ada Two" {
		t.Fatal("the conversation kept the old name")
	}
	r.do(bo, map[string]any{"t": "party_invite", "name": "ada two"})
	if inv := one(t, drain(ada), "party_invited"); inv["from"].(map[string]any)["name"] != "Bo" {
		t.Fatalf("party_invited %v", inv)
	}
	// Buddies hear the new name when she comes and goes.
	r.hub.Disconnect(ada)
	r.settle(time.Hour)
	if off := kinds(drain(bo), "buddy_offline"); len(off) != 1 || off[0]["name"] != "Ada Two" {
		t.Fatalf("buddy_offline %v", off)
	}
}

func TestRenameRefusesWhatIsNotANameAndSaysOffline(t *testing.T) {
	r := newLiveRig(t)
	bo := r.game(9, "Bo", lists{})
	for _, body := range []string{`{"player":7}`, `{"player":0,"name":"X"}`, `{"player":7,"name":"   "}`, `{"player":7,"name":"a\u0007b"}`, `{"player":7,"name":"abcdefghijklmnopqrstuvwxyzabcdefg"}`, `nope`} {
		if code, _ := renameService(r.hub, []byte(body)); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, code)
		}
	}
	// Nobody of theirs online: nothing to tell, the next ticket carries it.
	if _, out := renameService(r.hub, []byte(`{"player":7,"name":"Ada Two"}`)); out.(map[string]string)["status"] != "offline" {
		t.Fatalf("offline %v", out)
	}
	if got := kinds(drain(bo), "renamed"); len(got) != 0 {
		t.Fatalf("told about an offline player: %v", got)
	}
}

func TestRenameChangesTheirPinnedNotesAuthor(t *testing.T) {
	m := &boards{notes: map[string]*boardNote{
		"a": {ID: "a", Author: 7, AuthorName: "Ada"},
		"b": {ID: "b", Author: 9, AuthorName: "Bo"},
		"c": {ID: "c", Author: 7, AuthorName: "", Remake: true},
	}}
	m.renamed(7, "Ada Two")
	if m.notes["a"].AuthorName != "Ada Two" || m.notes["b"].AuthorName != "Bo" || m.notes["c"].AuthorName != "" {
		t.Fatalf("notes %+v %+v %+v", m.notes["a"], m.notes["b"], m.notes["c"])
	}
}
