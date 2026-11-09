package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newModRig is newDelRig's hub with the moderation module.
func newModRig(t *testing.T) *delRig {
	t.Helper()
	r := &delRig{liveRig: &liveRig{t: t, now: time.Unix(1_700_000_000, 0), sink: &fakeChatSink{}}, dir: t.TempDir()}
	savedB, savedC, savedIM, savedF, savedS, savedBans, savedDir, saved := buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest, chatlogDirForTest, modules
	buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest, chatlogDirForTest = buddyTestSecret, chanTestSecret, "", "", r.sink, "off", r.dir
	modules = []moduleFactory{newChatlogModule, newBuddies, newChannels, newParties, newAdmin, newIMs, newReacts,
		func(h *Hub) Module { r.cs = newChatStore(h).(*chatStore); return r.cs }, newThreads, newPresence, newModeration}
	r.hub = NewHub(func() time.Time { return r.now })
	buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest, chatlogDirForTest, modules = savedB, savedC, savedIM, savedF, savedS, savedBans, savedDir, saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool { return func() bool { return false } }
	t.Cleanup(r.hub.Shutdown)
	return r
}

func callService(t *testing.T, fn func(*Hub, []byte) (int, any), h *Hub, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	code, out := fn(h, b)
	raw, _ := json.Marshal(out)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return code, m
}

func TestModeratorSilenceHoldsInChannelsAndLocalChatAndLapses(t *testing.T) {
	r := newModRig(t)
	ada := r.game(7, "Ada", lists{chans: "global,newbie"})
	bo := r.game(9, "Bo", lists{chans: "global,newbie"})
	r.do(ada, map[string]any{"t": "join", "street": "GA5HERE"})
	r.do(bo, map[string]any{"t": "join", "street": "GA5HERE"})
	r.do(ada, map[string]any{"t": "channel_join", "channel": "help"})
	drain(ada)
	drain(bo)

	until := r.now.Add(30 * time.Minute).UnixMilli()
	code, out := callService(t, modRestrictService, r.hub, map[string]any{"player": 7, "silence": "chat", "silence_until": until})
	if code != 200 || out["status"] != "ok" || out["online"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if c := one(t, drain(ada), "cone"); c["on"] != true {
		t.Fatalf("the game hears the cone: %v", c)
	}
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "buy my stuff"})
	r.do(ada, map[string]any{"t": "chat", "text": "buy my stuff"})
	got := drain(ada)
	if len(kinds(got, "channel_said")) != 0 || len(kinds(got, "said")) != 0 {
		t.Fatalf("a silenced player is heard: %v", got)
	}
	if len(kinds(drain(bo), "said")) != 0 {
		t.Fatal("Bo heard a silenced Local line")
	}
	// Live Help is the help silence's, not this one's.
	r.do(ada, map[string]any{"t": "channel_say", "channel": "help", "text": "how do I mine"})
	if len(kinds(drain(ada), "channel_said")) != 1 {
		t.Fatal("a chat silence closes Live Help too")
	}
	// The chat log says why.
	silenced := 0
	for _, l := range r.log(map[string]any{"refused": true}) {
		if l["why"] == "silenced" {
			silenced++
		}
	}
	if silenced != 2 {
		t.Fatalf("silenced lines in the log: %d", silenced)
	}
	// It ends by itself.
	r.now = r.now.Add(30*time.Minute + time.Second)
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "sorry"})
	if len(kinds(drain(ada), "channel_said")) != 1 {
		t.Fatal("an ended silence still holds")
	}
}

func TestModeratorIMRestrictionToEveryoneOrToNonBuddies(t *testing.T) {
	r := newModRig(t)
	ada := r.game(7, "Ada", lists{bud: []int64{9}})
	r.game(9, "Bo", lists{rev: []int64{7}})
	r.game(11, "Cy", lists{})
	send := func(to int64) map[string]any {
		r.do(ada, map[string]any{"t": "im_send", "to": to, "text": "hello there"})
		for _, m := range drain(ada) {
			if m["t"] == "im" || m["t"] == "im_no" {
				return m
			}
		}
		return nil
	}
	callService(t, modRestrictService, r.hub, map[string]any{"player": 7, "im": "buddies"})
	if m := send(9); m["t"] != "im" {
		t.Fatalf("to a buddy: %v", m)
	}
	if m := send(11); m["t"] != "im_no" || m["reason"] != "restricted" {
		t.Fatalf("to a non-buddy: %v", m)
	}
	callService(t, modRestrictService, r.hub, map[string]any{"player": 7, "im": "all", "im_until": r.now.Add(time.Minute).UnixMilli()})
	if m := send(9); m["t"] != "im_no" || m["reason"] != "restricted" {
		t.Fatalf("all, to a buddy: %v", m)
	}
	r.now = r.now.Add(2 * time.Minute)
	if m := send(11); m["t"] != "im" {
		t.Fatalf("after it ended: %v", m)
	}
	// Lifted: the whole set, nothing in it.
	callService(t, modRestrictService, r.hub, map[string]any{"player": 7, "im": "all"})
	callService(t, modRestrictService, r.hub, map[string]any{"player": 7})
	if m := send(11); m["t"] != "im" {
		t.Fatalf("after the lift: %v", m)
	}
	for _, bad := range []map[string]any{{"player": 0}, {"player": 7, "im": "some"}, {"player": 7, "silence": "local"}, {"player": 7, "im_until": -1}} {
		if code, _ := callService(t, modRestrictService, r.hub, bad); code != 400 {
			t.Fatalf("%v: %d", bad, code)
		}
	}
}

func TestATicketCarriesRestrictionsButNeverUndoesANewerWord(t *testing.T) {
	r := newModRig(t)
	iat := r.now.Unix()
	ada := r.hub.Connect(Claims{Sub: 7, Name: "Ada", Iat: iat, Mod: &modClaim{S: "chat", IM: "all"}})
	if h := one(t, drain(ada), "hello"); h["cone"] != true {
		t.Fatalf("hello: %v", h)
	}
	r.hub.mu.Lock()
	on := r.hub.modSilencedLocked(7, "chat") && r.hub.modIMRestrictedLocked(7, 9)
	r.hub.mu.Unlock()
	if !on {
		t.Fatal("the ticket's restrictions hold")
	}
	// A moderator lifts both; a ticket made a moment before that still says silenced.
	r.now = r.now.Add(3 * time.Second)
	callService(t, modRestrictService, r.hub, map[string]any{"player": 7})
	r.hub.Connect(Claims{Sub: 7, Name: "Ada", Iat: iat + 1, Mod: &modClaim{S: "chat"}})
	r.hub.mu.Lock()
	on = r.hub.modSilencedLocked(7, "chat")
	r.hub.mu.Unlock()
	if on {
		t.Fatal("an older ticket laid a lifted silence again")
	}
}

func TestPurgeTakesEverythingAPlayerSaidOutOfEveryWindow(t *testing.T) {
	r := newModRig(t)
	ada := r.game(7, "Ada", lists{})
	bo := r.game(9, "Bo", lists{})
	cy := r.web(11, "Cy", lists{})
	r.do(bo, map[string]any{"t": "channel_say", "channel": "global", "text": "hi all"})
	boLid := lidOf(t, drain(ada), "channel_said")
	drain(bo)
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "FREE CURRANTS at my site"})
	adaLid := lidOf(t, drain(bo), "channel_said")
	r.do(ada, map[string]any{"t": "thread_say", "channel": "global", "parent": boLid, "text": "FREE CURRANTS"})
	r.do(ada, map[string]any{"t": "im_send", "to": 9, "text": "FREE CURRANTS"})
	r.do(bo, map[string]any{"t": "im_send", "to": 7, "text": "stop"})
	drain(ada)
	drain(bo)
	drain(cy)

	code, out := callService(t, modPurgeService, r.hub, map[string]any{"player": 7, "since": 0, "by": 1})
	if code != 200 || out["status"] != "purged" || out["channel"].(float64) != 1 || out["replies"].(float64) != 1 || out["ims"].(float64) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	if strings.Contains(func() string { b, _ := json.Marshal(out); return string(b) }(), "FREE") {
		t.Fatal("the answer carries words")
	}
	for who, cl := range map[string]*Client{"Ada": ada, "Bo": bo, "Cy (web)": cy} {
		m := one(t, drain(cl), "chat_purged")
		if m["player"].(float64) != 7 || m["since"].(float64) != 0 || len(m) != 3 {
			t.Fatalf("%s heard %v", who, m)
		}
	}
	// The channel's history keeps Bo's line only; the thread lost Ada's reply.
	di := r.game(13, "Di", lists{})
	r.do(di, map[string]any{"t": "channel_leave", "channel": "global"})
	r.do(di, map[string]any{"t": "channel_join", "channel": "global"})
	for _, l := range historyLids(drain(di)) {
		if l == adaLid {
			t.Fatal("a purged line is still in the history")
		}
	}
	// Bo's IMs keep his own line, not Ada's.
	r.do(bo, map[string]any{"t": "im_sync"})
	st := one(t, drain(bo), "im_state")
	for _, c := range st["convs"].([]any) {
		for _, l := range c.(map[string]any)["lines"].([]any) {
			if l.(map[string]any)["from"].(float64) == 7 {
				t.Fatalf("Bo still has Ada's IM: %v", l)
			}
		}
	}
	if b, _ := json.Marshal(st); !strings.Contains(string(b), "stop") {
		t.Fatalf("Bo's own line went too: %s", b)
	}
	// Nobody reacts to the purged line.
	r.do(bo, map[string]any{"t": "react", "line": adaLid, "e": "heart", "on": true})
	if len(kinds(drain(bo), "error")) != 1 {
		t.Fatal("a reaction on a purged line must be refused")
	}
	// The site hears it after the lines, through the chat store; the log keeps the words, marked purged.
	if !r.cs.flush(0) {
		t.Fatal("flush")
	}
	all := r.sink.all()
	if len(all) == 0 || all[len(all)-1].K != "purge" || all[len(all)-1].ID != 7 || all[len(all)-1].By != 1 {
		t.Fatalf("store: %+v", all[len(all)-1])
	}
	lines := r.log(map[string]any{"q": "FREE"})
	if len(lines) == 0 {
		t.Fatal("the log lost the evidence")
	}
	for _, l := range lines {
		d, _ := l["deleted"].(map[string]any)
		if d == nil || d["purge"] != true {
			t.Fatalf("not marked purged: %v", l)
		}
	}
	if hi := r.log(map[string]any{"q": "hi all"}); len(hi) != 1 || hi[0]["deleted"] != nil {
		t.Fatalf("Bo's line is marked: %v", hi)
	}
}

func TestPurgeSinceKeepsWhatCameBefore(t *testing.T) {
	r := newModRig(t)
	ada := r.game(7, "Ada", lists{})
	bo := r.game(9, "Bo", lists{})
	r.do(ada, map[string]any{"t": "im_send", "to": 9, "text": "old line"})
	r.now = r.now.Add(48 * time.Hour)
	r.do(ada, map[string]any{"t": "im_send", "to": 9, "text": "spam line"})
	drain(ada)
	drain(bo)
	since := r.now.Add(-24 * time.Hour).UnixMilli()
	if _, out := callService(t, modPurgeService, r.hub, map[string]any{"player": 7, "since": since, "by": 1}); out["ims"].(float64) != 1 {
		t.Fatalf("%v", out)
	}
	r.do(bo, map[string]any{"t": "im_sync"})
	b, _ := json.Marshal(one(t, drain(bo), "im_state"))
	if !strings.Contains(string(b), "old line") || strings.Contains(string(b), "spam line") {
		t.Fatalf("%s", b)
	}
}

// No call names or reads an IM line (Austin 2026-11-09: the privacy policy promises only an IM's
// two players read it). The purge is the one IM action, by player id, and its answer is counts.
func TestNoModeratorCallReachesAnIMLine(t *testing.T) {
	for _, call := range []string{"im_delete", "im_read", "im_report", "im_lines"} {
		if _, ok := serviceHandlers[call]; ok {
			t.Fatalf("%s is a call", call)
		}
	}
	h := NewHub(nil)
	t.Cleanup(h.Shutdown)
	srv := httptest.NewServer(NewServer(NewTickets(testSecret, nil), h, nil))
	t.Cleanup(srv.Close)
	code, out := siteCall(t, srv.URL, "im_delete", map[string]any{"a": 7, "b": 9, "line": 1, "at": 1, "by": 1}, "")
	mustStatus(t, code, out, 404, "bad")

	r := newModRig(t)
	ada := r.game(7, "Ada", lists{})
	bo := r.game(9, "Bo", lists{})
	r.do(ada, map[string]any{"t": "im_send", "to": 9, "text": "secret spam words"})
	drain(ada)
	drain(bo)
	_, purged := callService(t, modPurgeService, r.hub, map[string]any{"player": 7, "since": 0, "by": 1})
	b, _ := json.Marshal(purged)
	if purged["ims"].(float64) != 1 || strings.Contains(string(b), "secret") || strings.Contains(string(b), "spam") {
		t.Fatalf("the purge answer must be counts only: %s", b)
	}
	for who, cl := range map[string]*Client{"Ada": ada, "Bo": bo} {
		for _, m := range drain(cl) {
			if mb, _ := json.Marshal(m); strings.Contains(string(mb), "secret") {
				t.Fatalf("%s heard the words: %s", who, mb)
			}
		}
	}
}

func TestChatDeleteReachesAGroupsChat(t *testing.T) {
	r := newModRig(t)
	ada := r.hub.Connect(Claims{Sub: 7, Name: "Ada"})
	bo := r.hub.Connect(Claims{Sub: 9, Name: "Bo"})
	for _, p := range []struct {
		cl *Client
		id int64
	}{{ada, 7}, {bo, 9}} {
		r.do(p.cl, map[string]any{"t": "buddies_watch", "token": r.buddyToken(p.id, nil, nil, nil)})
		r.do(p.cl, map[string]any{"t": "groups_watch", "token": chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": p.id, "g": chanIDs(12), "c": "global", "s": "", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
		r.do(p.cl, map[string]any{"t": "channel_join", "channel": "group:12"})
	}
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "channel_say", "channel": "group:12", "text": "rude"})
	lid := lidOf(t, drain(bo), "channel_said")
	drain(ada)
	code, out := r.del(map[string]any{"lid": lid, "rooms": "group:12", "by": 1, "by_name": "Grok Bot"})
	if code != 200 || out["room"] != "group:12" || out["held"] != true {
		t.Fatalf("%d %v", code, out)
	}
	for _, cl := range []*Client{ada, bo} {
		if m := one(t, drain(cl), "chat_deleted"); m["channel"] != "group:12" {
			t.Fatalf("%v", m)
		}
	}
}

func TestModerationCallsAreOnlyTheSitesSignedCalls(t *testing.T) {
	h := NewHub(nil)
	t.Cleanup(h.Shutdown)
	srv := httptest.NewServer(NewServer(NewTickets(testSecret, nil), h, nil))
	t.Cleanup(srv.Close)
	ws, base := "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", srv.URL
	for call, body := range map[string]map[string]any{
		"mod_restrict": {"player": 5, "im": "all"},
		"mod_purge":    {"player": 5, "since": 0, "by": 1},
	} {
		code, out := siteCall(t, base, call, body, "-")
		mustStatus(t, code, out, 401, "refused")
		ada := signIn(t, ws, 1, "Ada")
		ada.send(map[string]any{"t": call, "player": 1, "im": "all", "since": 0, "by": 1})
		if e := ada.next("error"); e["code"] != "bad_frame" {
			t.Fatalf("socket %s: %v", call, e)
		}
	}
}

func flagsIn(r *delRig) []chatEntry {
	r.t.Helper()
	if !r.cs.flush(0) {
		r.t.Fatal("flush")
	}
	var out []chatEntry
	for _, e := range r.sink.all() {
		if e.K == "flag" {
			out = append(out, e)
		}
	}
	return out
}

func TestSignalsFlagSpamWithoutKeepingAWord(t *testing.T) {
	r := newModRig(t)
	ada := r.game(7, "Ada", lists{})
	for i := int64(0); i < modFanoutFlag; i++ {
		r.game(100+i, "Player", lists{})
	}
	for i := int64(0); i < modFanoutFlag-1; i++ {
		r.do(ada, map[string]any{"t": "im_send", "to": 100 + i, "text": "visit my shop"})
		r.now = r.now.Add(1100 * time.Millisecond) // under the IM rate
	}
	if f := flagsIn(r); len(f) != 0 {
		t.Fatalf("flagged too soon: %+v", f)
	}
	r.do(ada, map[string]any{"t": "im_send", "to": 100 + modFanoutFlag - 1, "text": "visit my shop"})
	f := flagsIn(r)
	if len(f) != 1 || f[0].Signal != "im_fanout" || f[0].ID != 7 || f[0].N != modFanoutFlag || f[0].Window != 600 || f[0].Text != "" {
		t.Fatalf("%+v", f)
	}
	b, _ := json.Marshal(f[0])
	if strings.Contains(string(b), "shop") {
		t.Fatalf("a flag carries words: %s", b)
	}
	// Once an hour for a signal.
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "im_send", "to": 100, "text": "again"})
	if f := flagsIn(r); len(f) != 1 {
		t.Fatalf("flagged twice: %+v", f)
	}
}

func TestSignalsFlagRefusedIMsFilteredIMsAndRefusedChat(t *testing.T) {
	r := newModRig(t)
	ada := r.game(7, "Ada", lists{})
	r.game(9, "Bo", lists{})
	// Bo never reads: after imUnreadMax lines every IM is "full"; the rate refuses some on the way.
	for i := 0; i < imUnreadMax+modRefusedFlag+5; i++ {
		r.do(ada, map[string]any{"t": "im_send", "to": 9, "text": "fuck you"})
		r.now = r.now.Add(1100 * time.Millisecond)
	}
	got := map[string]bool{}
	for _, f := range flagsIn(r) {
		got[f.Signal] = true
	}
	if !got["im_refused"] || !got["im_filtered"] {
		t.Fatalf("%v", got)
	}
	// Refused channel lines: say faster than the channel rate allows.
	for i := 0; i < 40; i++ {
		r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "spam"})
	}
	got = map[string]bool{}
	for _, f := range flagsIn(r) {
		got[f.Signal] = true
	}
	if !got["chat_refused"] {
		t.Fatalf("%v", got)
	}
}
