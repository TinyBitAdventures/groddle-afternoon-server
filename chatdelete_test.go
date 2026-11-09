package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// delRig is liveRig's hub (every chat module, threads, the web's sockets, the chat store's fake site)
// with the chat log in a temp dir.
type delRig struct {
	*liveRig
	dir string
}

func newDelRig(t *testing.T) *delRig {
	t.Helper()
	r := &delRig{liveRig: &liveRig{t: t, now: time.Unix(1_700_000_000, 0), sink: &fakeChatSink{}}, dir: t.TempDir()}
	savedB, savedC, savedIM, savedF, savedS, savedBans, savedDir, saved := buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest, chatlogDirForTest, modules
	buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest, chatlogDirForTest = buddyTestSecret, chanTestSecret, "", "", r.sink, "off", r.dir
	modules = []moduleFactory{newChatlogModule, newBuddies, newChannels, newParties, newAdmin, newIMs, newReacts,
		func(h *Hub) Module { r.cs = newChatStore(h).(*chatStore); return r.cs }, newThreads, newPresence}
	r.hub = NewHub(func() time.Time { return r.now })
	buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest, chatlogDirForTest, modules = savedB, savedC, savedIM, savedF, savedS, savedBans, savedDir, saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool { return func() bool { return false } }
	t.Cleanup(r.hub.Shutdown)
	return r
}

func (r *delRig) del(body map[string]any) (int, map[string]any) {
	r.t.Helper()
	b, _ := json.Marshal(body)
	code, out := chatDeleteService(r.hub, b)
	raw, _ := json.Marshal(out)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return code, m
}

func (r *delRig) log(q map[string]any) []map[string]any {
	r.t.Helper()
	body, _ := json.Marshal(q)
	_, out := adminChatlog(r.hub, body)
	raw, _ := json.Marshal(out)
	var m struct {
		Lines []map[string]any `json:"lines"`
	}
	json.Unmarshal(raw, &m)
	return m.Lines
}

func historyLids(msgs []map[string]any) []int64 {
	var out []int64
	for _, m := range kinds(msgs, "channel_joined") {
		for _, l := range m["history"].([]any) {
			out = append(out, int64(l.(map[string]any)["lid"].(float64)))
		}
	}
	return out
}

func TestChatDeleteTakesALineAwayForEveryoneAndKeepsTheEvidence(t *testing.T) {
	r := newDelRig(t)
	// Ada is new (Live Help is the newbie room for her); Bo reads it in the game, Cy on the website.
	ada := r.game(7, "Ada", lists{chans: "global,newbie"})
	bo := r.game(9, "Bo", lists{chans: "global,newbie"})
	cy := r.web(11, "Cy", lists{chans: "global,newbie"})
	r.do(ada, map[string]any{"t": "channel_join", "channel": "help"})
	r.do(bo, map[string]any{"t": "channel_join", "channel": "help"})
	r.do(ada, map[string]any{"t": "channel_say", "channel": "help", "text": "d/leave"})
	lid := lidOf(t, drain(bo), "channel_said")
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "channel_say", "channel": "help", "text": "how do I leave a group?"})
	drain(ada)
	drain(bo)
	drain(cy)

	code, out := r.del(map[string]any{"lid": lid, "rooms": "help,newbie", "by": 1, "by_name": "Austin"})
	if code != 200 || out["status"] != "deleted" || out["room"] != "newbie" || out["held"] != true || out["parent"].(float64) != 0 || out["author"].(float64) != 7 || out["text"] != "d/leave" {
		t.Fatalf("%d %v", code, out)
	}
	for who, cl := range map[string]*Client{"Ada": ada, "Bo": bo, "Cy (web)": cy} {
		m := one(t, drain(cl), "chat_deleted")
		if m["channel"] != "help" || int64(m["lid"].(float64)) != lid || m["parent"] != nil {
			t.Fatalf("%s heard %v", who, m)
		}
	}
	// A late joiner (and a reload) never gets it again; the line after it stays.
	di := r.game(13, "Di", lists{chans: "global,newbie"})
	r.do(di, map[string]any{"t": "channel_join", "channel": "help"})
	got := historyLids(drain(di))
	if len(got) != 1 || got[0] == lid {
		t.Fatalf("history after the delete: %v (deleted %d)", got, lid)
	}
	// Nobody reacts to it or replies under it now.
	r.do(bo, map[string]any{"t": "react", "line": lid, "e": "heart", "on": true})
	if len(kinds(drain(bo), "error")) != 1 {
		t.Fatal("a reaction on a deleted line must be refused")
	}
	r.do(bo, map[string]any{"t": "thread_say", "channel": "help", "parent": lid, "text": "you typed it in chat"})
	if len(kinds(drain(bo), "error")) != 1 {
		t.Fatal("a reply under a deleted line must be refused")
	}

	// The site hears it after the line itself, through the chat store.
	if !r.cs.flush(0) {
		t.Fatal("flush")
	}
	var order []string
	for _, e := range r.sink.all() {
		if e.Lid == lid {
			order = append(order, e.K)
			if e.K == "delete" && (e.By != 1 || e.At != r.now.UnixMilli()) {
				t.Fatalf("delete entry %+v", e)
			}
		}
	}
	if strings.Join(order, ",") != "line,delete" {
		t.Fatalf("store order %v", order)
	}

	// The moderation log keeps the line as it was written, marked deleted, and who did it.
	lines := r.log(map[string]any{"q": "d/leave"})
	if len(lines) != 1 || lines[0]["text"] != "d/leave" || int64(lines[0]["lid"].(float64)) != lid {
		t.Fatalf("log %v", lines)
	}
	d, _ := lines[0]["deleted"].(map[string]any)
	if d == nil || d["by"].(float64) != 1 || d["by_name"] != "Austin" || !strings.HasPrefix(d["at"].(string), "2023-11-14T22:13:21") {
		t.Fatalf("deleted mark %v", lines[0])
	}
	if other := r.log(map[string]any{"q": "group"}); len(other) != 1 || other[0]["deleted"] != nil {
		t.Fatalf("a line nobody deleted is not marked: %v", other)
	}

	// Asked again: this server no longer has it; with the site's row it answers (and tells readers) again.
	if code, out := r.del(map[string]any{"lid": lid, "rooms": "help,newbie", "by": 1, "by_name": "Austin"}); code != 404 || out["status"] != "unknown" {
		t.Fatalf("again without the site's row: %d %v", code, out)
	}
	if code, out := r.del(map[string]any{"lid": lid, "rooms": "help,newbie", "by": 1, "by_name": "Austin", "room": "newbie", "author": 7, "at": 1, "text": "d/leave"}); code != 200 || out["held"] != false {
		t.Fatalf("again with the site's row: %d %v", code, out)
	}
	if len(kinds(drain(bo), "chat_deleted")) != 1 {
		t.Fatal("readers are told again")
	}
}

func TestChatDeleteAThreadReplyAndALineWithReplies(t *testing.T) {
	r := newDelRig(t)
	ada := r.game(7, "Ada", lists{})
	bo := r.game(9, "Bo", lists{})
	cy := r.web(11, "Cy", lists{})
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "anyone selling cherries?"})
	parent := lidOf(t, drain(bo), "channel_said")
	r.do(bo, map[string]any{"t": "thread_say", "channel": "global", "parent": parent, "text": "me"})
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "thread_say", "channel": "global", "parent": parent, "text": "oops wrong window"})
	var replies []int64
	for _, m := range kinds(drain(bo), "thread_said") {
		replies = append(replies, int64(m["line"].(map[string]any)["lid"].(float64)))
	}
	drain(ada)
	drain(cy)
	if len(replies) != 2 {
		t.Fatalf("replies %v", replies)
	}

	code, out := r.del(map[string]any{"lid": replies[1], "rooms": "global", "by": 1, "by_name": "Austin"})
	if code != 200 || out["held"] != true || int64(out["parent"].(float64)) != parent {
		t.Fatalf("%d %v", code, out)
	}
	for _, cl := range []*Client{ada, bo, cy} {
		m := one(t, drain(cl), "chat_deleted")
		if int64(m["parent"].(float64)) != parent || m["n"].(float64) != 1 || m["last"].(map[string]any)["name"] != "Bo" {
			t.Fatalf("reply deleted: %v", m)
		}
	}
	r.do(bo, map[string]any{"t": "thread_get", "channel": "global", "parent": parent})
	th := one(t, drain(bo), "thread")
	if ls := th["lines"].([]any); len(ls) != 1 || ls[0].(map[string]any)["text"] != "me" {
		t.Fatalf("thread after: %v", th)
	}

	// The line itself goes with its thread.
	if code, _ := r.del(map[string]any{"lid": parent, "rooms": "global", "by": 1, "by_name": "Austin"}); code != 200 {
		t.Fatal(code)
	}
	r.do(bo, map[string]any{"t": "thread_get", "channel": "global", "parent": parent})
	if th := one(t, drain(bo), "thread"); th["known"] != false || len(th["lines"].([]any)) != 0 {
		t.Fatalf("thread of a deleted line: %v", th)
	}
}

func TestChatDeleteRefusesWhatIsNotAChannelLine(t *testing.T) {
	r := newDelRig(t)
	for _, body := range []map[string]any{
		{"lid": 0, "rooms": "global", "by": 1},
		{"lid": 5, "rooms": "global", "by": 0},
		{"lid": 5, "rooms": "", "by": 1},
		{"lid": 5, "rooms": "group:12,global", "by": 1}, // a group's chat is named alone (moderation.go)
		{"lid": 5, "rooms": "local", "by": 1},
		{"lid": 5, "rooms": "global", "by": 1, "room": "trade"},
	} {
		if code, out := r.del(body); code != 400 || out["status"] != "bad" {
			t.Fatalf("%v: %d %v", body, code, out)
		}
	}
	if code, out := r.del(map[string]any{"lid": 5, "rooms": "global", "by": 1}); code != 404 || out["status"] != "unknown" {
		t.Fatalf("%d %v", code, out)
	}
	// A Local line is not a channel's, even with its lid.
	ada := r.game(7, "Ada", lists{})
	bo := r.game(9, "Bo", lists{})
	r.do(ada, map[string]any{"t": "join", "street": "GA5HERE"})
	r.do(bo, map[string]any{"t": "join", "street": "GA5HERE"})
	drain(bo)
	r.do(ada, map[string]any{"t": "chat", "text": "local words"})
	lid := lidOf(t, drain(bo), "said")
	if code, _ := r.del(map[string]any{"lid": lid, "rooms": "global,newbie,trade", "by": 1}); code != 404 {
		t.Fatalf("a Local line: %d", code)
	}
}

func TestChatDeleteIsOnlyTheSitesSignedCall(t *testing.T) {
	h := NewHub(nil)
	t.Cleanup(h.Shutdown)
	srv := httptest.NewServer(NewServer(NewTickets(testSecret, nil), h, nil))
	t.Cleanup(srv.Close)
	ws, base := "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", srv.URL
	body := map[string]any{"lid": 5, "rooms": "global", "by": 1, "room": "global"}
	code, out := siteCall(t, base, "chat_delete", body, "-")
	mustStatus(t, code, out, 401, "refused")
	code, out = siteCall(t, base, "chat_delete", body, "")
	mustStatus(t, code, out, 200, "deleted")
	ada := signIn(t, ws, 1, "Ada")
	ada.send(map[string]any{"t": "chat_delete", "lid": 5, "rooms": "global", "by": 1})
	if e := ada.next("error"); e["code"] != "bad_frame" {
		t.Fatalf("socket chat_delete: %v", e)
	}
}

func TestChatlogMarksALineFromBeforeLidsAndPrunesOldDeletions(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	c := newChatLog(dir, 30, func() time.Time { return now }, 16)
	go c.run()
	said := now.Add(-time.Hour)
	// Written before lines carried a lid; the word filter's copy is what the site keeps.
	c.addLocked(chatLine{At: said.Format("2006-01-02T15:04:05.000Z"), Kind: "channel", Where: "newbie", ID: 7, Name: "Ada", Text: "d/leave fuck", Delivered: true, Filtered: true, Shown: "d/leave #!@$", t: said})
	c.addLocked(chatLine{At: said.Format("2006-01-02T15:04:05.000Z"), Kind: "channel", Where: "global", ID: 7, Name: "Ada", Text: "d/leave fuck", Delivered: true, t: said})
	c.addLocked(chatLine{Delivered: true, t: now, del: &chatDeletion{At: now.Format(time.RFC3339Nano), Where: "newbie", Lid: 99, ID: 7, LineAt: said.Add(400 * time.Millisecond).UnixMilli(), Text: "d/leave #!@$", By: 1, ByName: "Austin"}})
	// An old deletion, past the 30 days.
	c.addLocked(chatLine{Delivered: true, t: now, del: &chatDeletion{At: now.AddDate(0, 0, -40).Format(time.RFC3339Nano), Where: "global", Lid: 98, By: 1, ByName: "Austin"}})
	c.flush()
	lines, _ := c.search(chatQuery{Limit: 10})
	marks := map[string]bool{}
	for _, l := range lines {
		marks[l.Where] = l.Deleted != nil
	}
	if !marks["newbie"] || marks["global"] {
		t.Fatalf("marks %v", marks)
	}
	if st, err := os.Stat(filepath.Join(dir, chatlogDeletedFile)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("deleted.jsonl: %v %v", st, err)
	}
	now = now.Add(24 * time.Hour)
	c.prune()
	if ds := c.deletions(); len(ds) != 1 || ds[0].Lid != 99 {
		t.Fatalf("after prune: %+v", ds)
	}
	c.close()
}
