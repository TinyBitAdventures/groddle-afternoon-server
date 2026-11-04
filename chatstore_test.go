package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeChatSink is the site: it keeps what it was given, and fails while down.
type fakeChatSink struct {
	mu      sync.Mutex
	down    bool
	posts   [][]chatEntry
	byLid   map[int64]chatEntry
	reacted map[int64][]storeReact
}

func (s *fakeChatSink) post(entries []chatEntry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return false
	}
	s.posts = append(s.posts, append([]chatEntry(nil), entries...))
	if s.byLid == nil {
		s.byLid, s.reacted = map[int64]chatEntry{}, map[int64][]storeReact{}
	}
	for _, e := range entries {
		switch e.K {
		case "line":
			s.byLid[e.Lid] = e // idempotent by lid, as the site's INSERT IGNORE
		case "react":
			s.reacted[e.Lid] = e.R // the whole set, as the site's update
		}
	}
	return true
}

func (s *fakeChatSink) all() []chatEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []chatEntry
	for _, p := range s.posts {
		out = append(out, p...)
	}
	return out
}

// storeRig is a hub with buddies, channels, IMs, reactions, admin and the chat store, a manual clock.
type storeRig struct {
	*reactRig
	cs   *chatStore
	ch   *channels
	sink *fakeChatSink
}

func newStoreRig(t *testing.T, file string, sink *fakeChatSink) *storeRig {
	t.Helper()
	r := &storeRig{reactRig: &reactRig{t: t, now: time.Unix(1_700_000_000, 0)}, sink: sink}
	savedB, savedC, savedIM, savedF, savedS, savedBans, saved := buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest, modules
	var asSink chatSink
	if sink != nil {
		asSink = sink
	}
	buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest = buddyTestSecret, chanTestSecret, "", file, asSink, "off"
	modules = []moduleFactory{newBuddies, func(h *Hub) Module { r.ch = newChannels(h).(*channels); return r.ch }, newParties, newAdmin,
		func(h *Hub) Module { r.im = newIMs(h).(*ims); return r.im },
		func(h *Hub) Module { r.rx = newReacts(h).(*reacts); return r.rx },
		func(h *Hub) Module { r.cs = newChatStore(h).(*chatStore); return r.cs }}
	r.hub = NewHub(func() time.Time { return r.now })
	buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest, modules = savedB, savedC, savedIM, savedF, savedS, savedBans, saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool { return func() bool { return false } }
	return r
}

func (r *storeRig) inChannel(cl *Client, chans string) {
	r.do(cl, map[string]any{"t": "groups_watch", "token": chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": cl.ID(), "g": "", "c": chans, "s": "", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	r.do(cl, map[string]any{"t": "channel_join", "channel": "global"})
	drain(cl)
}

func TestChatStoreKeepsChannelAndIMLinesNotLocal(t *testing.T) {
	sink := &fakeChatSink{}
	r := newStoreRig(t, "", sink)
	ada, bo := r.online(7, "Ada"), r.online(9, "Bo")
	r.inChannel(ada, "global")
	r.inChannel(bo, "global")
	r.street("GA5134UPLRU29OH", ada, bo)
	r.do(ada, map[string]any{"t": "chat", "text": "local words"})
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "hello fuck"})
	lid := lidOf(t, drain(bo), "channel_said")
	r.react(bo, lid, "heart")
	r.react(bo, lid, "heart", false)
	r.do(ada, map[string]any{"t": "im_send", "to": 9, "text": "<b>psst</b>"})
	r.do(bo, map[string]any{"t": "im_react", "with": 7, "line": 1, "e": "like", "on": true})
	if !r.cs.flush(0) {
		t.Fatal("flush failed")
	}
	got := sink.all()
	var kinds []string
	for _, e := range got {
		kinds = append(kinds, e.K)
		if strings.Contains(e.Text, "local") {
			t.Fatalf("Local chat was stored: %+v", e)
		}
	}
	if strings.Join(kinds, ",") != "line,react,react,im,im_react" {
		t.Fatalf("kinds %v", kinds)
	}
	if l := got[0]; l.Room != "global" || l.Lid != lid || l.ID != 7 || l.Name != "Ada" || l.Text != "hello #!@$" || l.Web {
		t.Fatalf("line %+v", l)
	}
	if len(got[1].R) != 1 || got[1].R[0].E != "heart" || got[1].R[0].By[0] != 9 || len(got[2].R) != 0 {
		t.Fatalf("reactions %+v %+v", got[1], got[2])
	}
	b, _ := json.Marshal(got[2])
	if !strings.Contains(string(b), `"r":[]`) {
		t.Fatalf("an emptied set must say so: %s", b)
	}
	if im := got[3]; im.A != 7 || im.B != 9 || im.From != 7 || im.Line != 1 || im.Text != "psst" {
		t.Fatalf("im %+v", im)
	}
	if r.cs.pending() != 0 {
		t.Fatal("sent entries still wait")
	}
}

func TestChatStoreBatchesInOrder(t *testing.T) {
	sink := &fakeChatSink{}
	r := newStoreRig(t, "", sink)
	r.hub.mu.Lock()
	for i := 1; i <= 450; i++ {
		r.hub.storeChatLocked(chatEntry{K: "line", Room: "global", Lid: int64(i), ID: 7, Name: "Ada", Text: "x", At: int64(i)})
	}
	r.hub.mu.Unlock()
	r.cs.flush(0)
	if len(sink.posts) != 3 || len(sink.posts[0]) != chatStoreBatch || len(sink.posts[2]) != 50 {
		t.Fatalf("batches %d", len(sink.posts))
	}
	all := sink.all()
	for i, e := range all {
		if e.Lid != int64(i+1) || e.Seq != int64(i+1) {
			t.Fatalf("order at %d: %+v", i, e)
		}
	}
}

func TestChatStoreWaitsInItsFileWhileTheSiteIsDownAndSendsOnceBack(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "realtime-chat-store.json")
	sink := &fakeChatSink{down: true}
	r := newStoreRig(t, file, sink)
	r.hub.mu.Lock()
	r.hub.storeChatLocked(chatEntry{K: "line", Room: "trade", Lid: 41, ID: 7, Name: "Ada", Text: "selling", At: 1})
	r.hub.storeChatLocked(chatEntry{K: "im", A: 7, B: 9, Line: 3, From: 9, Text: "secret", At: 2})
	r.hub.mu.Unlock()
	if r.cs.flush(0) {
		t.Fatal("a down site took the batch")
	}
	if r.cs.pending() != 2 {
		t.Fatalf("pending %d", r.cs.pending())
	}
	r.cs.save()
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("the file holds IM text: mode %v", st.Mode().Perm())
	}
	// A restart: the new server finds what waited, and its numbering goes on.
	sink2 := &fakeChatSink{}
	r2 := newStoreRig(t, file, sink2)
	if r2.cs.pending() != 2 {
		t.Fatalf("after restart: pending %d", r2.cs.pending())
	}
	r2.hub.mu.Lock()
	r2.hub.storeChatLocked(chatEntry{K: "line", Room: "global", Lid: 42, ID: 9, Name: "Bo", Text: "hi", At: 3})
	r2.hub.mu.Unlock()
	if !r2.cs.flush(0) {
		t.Fatal("flush")
	}
	got := sink2.all()
	if len(got) != 3 || got[0].Lid != 41 || got[1].Text != "secret" || got[2].Seq != 3 {
		t.Fatalf("sent %+v", got)
	}
	// Sent again (an answer lost): the site keeps one of each.
	sink2.post(got)
	if len(sink2.byLid) != 2 {
		t.Fatalf("idempotent by lid: %d", len(sink2.byLid))
	}
	r2.cs.save()
	var f chatStoreFile
	b, _ := os.ReadFile(file)
	if json.Unmarshal(b, &f) != nil || len(f.Pending) != 0 || f.Seq != 3 {
		t.Fatalf("file after sending: %s", b)
	}
}

func TestChatStoreSendsKeptIMsOnceAtFirstRun(t *testing.T) {
	dir := t.TempDir()
	imFile := filepath.Join(dir, "im.json")
	conv := imFile
	os.WriteFile(conv, []byte(`{"v":1,"convs":[{"a":7,"b":9,"names":["Ada","Bo"],"lines":[{"id":1,"from":7,"text":"old one","at":`+itoa(int(time.Now().UnixMilli()))+`,"r":[{"e":"like","by":[{"id":9,"name":"Bo"}]}]}],"seq":1,"read":[1,1],"closed":[false,false],"last":1}]}`), 0o600)
	savedIM := imFileForTest
	defer func() { imFileForTest = savedIM }()
	file := filepath.Join(dir, "store.json")
	sink := &fakeChatSink{}
	r := newStoreRigIM(t, file, imFile, sink)
	r.cs.importOnce()
	r.cs.flush(0)
	got := sink.all()
	if len(got) != 2 || got[0].K != "im" || got[0].Text != "old one" || got[1].K != "im_react" {
		t.Fatalf("imported %+v", got)
	}
	r.cs.save()
	sink2 := &fakeChatSink{}
	r2 := newStoreRigIM(t, file, imFile, sink2)
	r2.cs.importOnce()
	r2.cs.flush(0)
	if len(sink2.all()) != 0 {
		t.Fatal("imported twice")
	}
}

func newStoreRigIM(t *testing.T, file, imFile string, sink *fakeChatSink) *storeRig {
	savedIM := imFileForTest
	imFileForTest = imFile
	defer func() { imFileForTest = savedIM }()
	r := &storeRig{reactRig: &reactRig{t: t, now: time.Unix(1_700_000_000, 0)}, sink: sink}
	savedF, savedS, saved := chatStoreFileForTest, chatStoreSinkForTest, modules
	chatStoreFileForTest, chatStoreSinkForTest = file, sink
	modules = []moduleFactory{func(h *Hub) Module { r.im = newIMs(h).(*ims); return r.im }, func(h *Hub) Module { r.cs = newChatStore(h).(*chatStore); return r.cs }}
	r.hub = NewHub(func() time.Time { return r.now })
	chatStoreFileForTest, chatStoreSinkForTest, modules = savedF, savedS, saved
	return r
}

func TestChatStoreOffKeepsNothing(t *testing.T) {
	r := newStoreRig(t, "", nil)
	if r.hub.chatStoreOf() != nil {
		t.Fatal("no sink, no store")
	}
	ada := r.online(7, "Ada")
	r.inChannel(ada, "global")
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "hi"})
	if r.cs.pending() != 0 {
		t.Fatal("kept with no site")
	}
}

func TestChatStoreSignsItsCallsToTheSite(t *testing.T) {
	var sig, route string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sig, route = req.Header.Get("X-Groddle-Service"), req.URL.Path
		w.WriteHeader(status)
		w.Write([]byte(`{"status":"ok","stored":1}`))
	}))
	defer srv.Close()
	s := &httpChatSink{l: &httpLedger{base: srv.URL, secret: []byte("s3cret"), client: srv.Client(), now: time.Now, pause: func(time.Duration) {}}}
	if !s.post([]chatEntry{{K: "line", Lid: 1, Room: "global", ID: 7, Name: "Ada", Text: "hi", At: 1}}) {
		t.Fatal("not ok")
	}
	if route != "/wp-json/groddle/v1/chat/ingest" || strings.Count(sig, ".") != 2 {
		t.Fatalf("route %q sig %q", route, sig)
	}
	status = http.StatusInternalServerError
	if s.post([]chatEntry{{K: "line", Lid: 2}}) {
		t.Fatal("a 500 is not kept")
	}
}

func webSay(t *testing.T, r *storeRig, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	code, out := webSayService(r.hub, b)
	raw, _ := json.Marshal(out)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return code, m
}

func TestWebSayIsARealChannelLineMarkedWeb(t *testing.T) {
	sink := &fakeChatSink{}
	r := newStoreRig(t, "", sink)
	bo := r.online(9, "Bo")
	r.inChannel(bo, "global")
	code, out := webSay(t, r, map[string]any{"player": 7, "name": "Ada", "channel": "global", "rooms": "global,newbie", "text": "  hi   from the web fuck "})
	if code != http.StatusOK || out["status"] != "sent" {
		t.Fatalf("%d %v", code, out)
	}
	said := one(t, drain(bo), "channel_said")
	if said["web"] != true || said["text"] != "hi from the web #!@$" || said["name"] != "Ada" || said["id"].(float64) != 7 {
		t.Fatalf("said %v", said)
	}
	// It is in the history whoever opens the channel next gets.
	cy := r.online(11, "Cy")
	r.do(cy, map[string]any{"t": "groups_watch", "token": chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": 11, "g": "", "c": "global", "s": "", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	r.do(cy, map[string]any{"t": "channel_join", "channel": "global"})
	joined := one(t, drain(cy), "channel_joined")
	if h := joined["history"].([]any); len(h) != 1 || h[0].(map[string]any)["web"] != true {
		t.Fatalf("history %v", joined["history"])
	}
	r.cs.flush(0)
	if got := sink.all(); len(got) != 1 || !got[0].Web || got[0].Room != "global" {
		t.Fatalf("stored %+v", got)
	}
	// Live Help is the room the player's level gives.
	webSay(t, r, map[string]any{"player": 7, "name": "Ada", "channel": "help", "rooms": "global,newbie", "text": "help me"})
	r.cs.flush(0)
	if got := sink.all(); got[len(got)-1].Room != "newbie" {
		t.Fatalf("help room %+v", got[len(got)-1])
	}
}

func TestWebSayRefusesWhatTheGameWould(t *testing.T) {
	r := newStoreRig(t, "", &fakeChatSink{})
	base := map[string]any{"player": 7, "name": "Ada", "channel": "global", "rooms": "global,help", "text": "hi"}
	with := func(k string, v any) map[string]any {
		out := map[string]any{}
		for a, b := range base {
			out[a] = b
		}
		out[k] = v
		return out
	}
	if code, out := webSay(t, r, with("channel", "trade")); code != http.StatusForbidden || out["status"] != "closed" {
		t.Fatalf("trade without the upgrade: %d %v", code, out)
	}
	if code, _ := webSay(t, r, with("channel", "group:4")); code != http.StatusBadRequest {
		t.Fatal("no group chat from the web")
	}
	if code, out := webSay(t, r, with("silence", "chat")); code != http.StatusForbidden || out["status"] != "silenced" || out["message"] != chanSuspended {
		t.Fatalf("cone of silence: %d %v", code, out)
	}
	if code, _ := webSay(t, r, with("text", "   ")); code != http.StatusBadRequest {
		t.Fatal("blank")
	}
	if code, _ := webSay(t, r, with("rooms", "global,bogus")); code != http.StatusBadRequest {
		t.Fatal("rooms are the token's words")
	}
	for i := 0; i < chanSayBurst; i++ {
		if code, _ := webSay(t, r, base); code != http.StatusOK {
			t.Fatalf("line %d refused", i)
		}
	}
	if code, out := webSay(t, r, base); code != http.StatusTooManyRequests || out["status"] != "slow_down" {
		t.Fatalf("rate: %d %v", code, out)
	}
	r.hub.mu.Lock()
	r.hub.adminOf().bans[7] = true
	r.hub.mu.Unlock()
	r.now = r.now.Add(time.Minute)
	if code, out := webSay(t, r, base); code != http.StatusForbidden || out["status"] != "banned" {
		t.Fatalf("banned: %d %v", code, out)
	}
}
