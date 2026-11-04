package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// liveRig is a hub with every chat module, presence and threads, the chat store's fake site, a manual
// clock and manual timers: the website's sockets (web.go) beside the game's.
type liveRig struct {
	t      *testing.T
	hub    *Hub
	now    time.Time
	sink   *fakeChatSink
	cs     *chatStore
	timers []*rigTimer
}

func newLiveRig(t *testing.T) *liveRig {
	t.Helper()
	r := &liveRig{t: t, now: time.Unix(1_700_000_000, 0), sink: &fakeChatSink{}}
	savedB, savedC, savedIM, savedF, savedS, savedBans, saved := buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest, modules
	buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest = buddyTestSecret, chanTestSecret, "", "", r.sink, "off"
	modules = []moduleFactory{newBuddies, newChannels, newParties, newAdmin, newIMs, newReacts,
		func(h *Hub) Module { r.cs = newChatStore(h).(*chatStore); return r.cs }, newThreads, newPresence}
	r.hub = NewHub(func() time.Time { return r.now })
	buddySecretForTest, chanSecretForTest, imFileForTest, chatStoreFileForTest, chatStoreSinkForTest, bansFileForTest, modules = savedB, savedC, savedIM, savedF, savedS, savedBans, saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

// settle runs every timer due within d, as often as they rearm.
func (r *liveRig) settle(d time.Duration) {
	for i := 0; i < 20; i++ {
		ran := false
		for _, tm := range append([]*rigTimer{}, r.timers...) {
			if tm.d <= d && !tm.stopped {
				tm.stopped = true
				tm.fn() // h.After's own wrapper takes the hub lock
				ran = true
			}
		}
		if !ran {
			return
		}
	}
}

func (r *liveRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *liveRig) buddyToken(sub int64, bud, rev, ign []int64) string {
	return signBuddy(buddyTestSecret, map[string]any{"k": "buddies", "sub": sub, "b": encodeIDs(bud), "r": encodeIDs(rev), "i": encodeIDs(ign), "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})
}

func (r *liveRig) groupsToken(sub int64, groups []int64, chans string) string {
	return chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": sub, "g": chanIDs(groups...), "c": chans, "s": "", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})
}

type lists struct {
	bud, rev, ign []int64
	chans         string
	off           bool
}

// game connects a character's game with its buddy list, in Global Chat.
func (r *liveRig) game(id int64, name string, l lists) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name, Off: l.off})
	r.do(cl, map[string]any{"t": "buddies_watch", "token": r.buddyToken(id, l.bud, l.rev, l.ign)})
	chans := l.chans
	if chans == "" {
		chans = "global,newbie"
	}
	r.do(cl, map[string]any{"t": "groups_watch", "token": r.groupsToken(id, nil, chans)})
	r.do(cl, map[string]any{"t": "channel_join", "channel": "global"})
	r.settle(presenceSettle)
	drain(cl)
	return cl
}

// web opens a /chat/ socket for a character with its two lists.
func (r *liveRig) web(id int64, name string, l lists) *Client {
	cl := r.hub.ConnectWeb(Claims{Sub: id, Name: name, Via: "web", Off: l.off})
	chans := l.chans
	if chans == "" {
		chans = "global,newbie"
	}
	r.do(cl, map[string]any{"t": "web_watch", "buddies": r.buddyToken(id, l.bud, l.rev, l.ign), "groups": r.groupsToken(id, nil, chans)})
	r.settle(presenceSettle)
	return cl
}

func names(players []any) string {
	var out []string
	for _, p := range players {
		m := p.(map[string]any)
		s := m["name"].(string) + ":" + m["status"].(string)
		if st, _ := m["street"].(string); st != "" {
			s += "@" + st
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestWebSocketIsBesideTheGameNeverReplacingIt(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{})
	w1 := r.web(7, "Ada", lists{})
	w2 := r.web(7, "Ada", lists{})
	if ada.closed || w1.closed || w2.closed || r.hub.ClientLocked(7) != ada || len(r.hub.WebLocked(7)) != 2 {
		t.Fatal("a web socket must not replace the game or another tab")
	}
	hello := kinds(drain(w1), "hello")
	if len(hello) != 1 || hello[0]["web"] != true {
		t.Fatalf("hello %v", hello)
	}
	// The game's frames mean nothing on a web socket: it has no street, no chat of its own.
	r.do(w1, map[string]any{"t": "join", "street": "GA5"})
	r.do(w1, map[string]any{"t": "channel_say", "channel": "global", "text": "hi"})
	if errs := kinds(drain(w1), "error"); len(errs) != 2 || errs[0]["code"] != "web" {
		t.Fatalf("errors %v", errs)
	}
	if len(r.hub.streets) != 0 {
		t.Fatal("a web socket joined a street")
	}
	if p, _ := r.hub.Stats(); p != 1 {
		t.Fatalf("players %d: a web socket is not a player online", p)
	}
	r.hub.DisconnectWeb(w1)
	if len(r.hub.WebLocked(7)) != 1 || r.hub.ClientLocked(7) != ada {
		t.Fatal("closing one tab touched the rest")
	}
}

func TestWebSocketGetsNothingBeforeItsListsAndRefusesBadLists(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{})
	bo := r.hub.ConnectWeb(Claims{Sub: 9, Name: "Bo", Via: "web"})
	drain(bo)
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "before"})
	if got := kinds(drain(bo), "channel_said"); len(got) != 0 {
		t.Fatal("a line reached a web socket with no lists")
	}
	// Someone else's lists, a made-up signature: refused.
	r.do(bo, map[string]any{"t": "web_watch", "buddies": r.buddyToken(7, nil, nil, nil), "groups": r.groupsToken(9, nil, "global")})
	r.do(bo, map[string]any{"t": "web_watch", "buddies": r.buddyToken(9, nil, nil, nil), "groups": "x.y"})
	errs := kinds(drain(bo), "error")
	if len(errs) != 2 || !strings.Contains(errs[0]["message"].(string), "not yours") {
		t.Fatalf("errors %v", errs)
	}
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "still nothing"})
	if got := kinds(drain(bo), "channel_said"); len(got) != 0 {
		t.Fatal("a refused list opened the channel")
	}
}

func TestWebSocketHearsTheChannelsItMayReadLiveButNotBlockedPlayers(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{chans: "global,newbie,trade"})
	cy := r.game(11, "Cy", lists{})
	r.do(ada, map[string]any{"t": "channel_join", "channel": "trade"})
	drain(ada)
	bo := r.web(9, "Bo", lists{ign: []int64{11}}) // Bo blocks Cy; no Trade Channel
	drain(bo)
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "hello web"})
	r.do(ada, map[string]any{"t": "channel_say", "channel": "trade", "text": "selling"})
	r.do(cy, map[string]any{"t": "channel_say", "channel": "global", "text": "from cy"})
	got := kinds(drain(bo), "channel_said")
	if len(got) != 1 || got[0]["text"] != "hello web" || got[0]["channel"] != "global" || got[0]["lid"] == nil {
		t.Fatalf("web got %v", got)
	}
	// Bo's reaction-free view: Cy's reaction on Ada's line is left out, Ada's own shows.
	lid := int64(got[0]["lid"].(float64))
	r.do(cy, map[string]any{"t": "react", "line": lid, "e": "heart"})
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "react", "line": lid, "e": "like"})
	var last map[string]any
	for _, m := range kinds(drain(bo), "reacted") {
		last = m
	}
	if last == nil || reactedText(last) != "like:7" {
		t.Fatalf("reacted on the web %v", last)
	}
	// A line said on the website reaches the other tabs too.
	bo2 := r.web(9, "Bo", lists{})
	drain(bo2)
	code, _ := webSayService(r.hub, []byte(`{"player":9,"name":"Bo","channel":"global","rooms":"global,newbie","text":"from the site"}`))
	if code != http.StatusOK || len(kinds(drain(bo2), "channel_said")) != 1 {
		t.Fatalf("web_say %d did not reach the other tab", code)
	}
}

func TestThreadsRepliesStayOutOfTheChannelAndCarryTheirCount(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{})
	bo := r.game(9, "Bo", lists{})
	web := r.web(11, "Cy", lists{})
	drain(web)
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "who wants pie"})
	lid := lidOf(t, drain(bo), "channel_said")
	drain(ada)
	drain(web)
	r.now = r.now.Add(time.Second)
	r.do(bo, map[string]any{"t": "thread_say", "channel": "global", "parent": lid, "text": "me fuck"})
	for _, who := range []*Client{ada, bo, web} {
		f := drain(who)
		if len(kinds(f, "channel_said")) != 0 {
			t.Fatal("a reply showed in the channel")
		}
		m := one(t, f, "thread_said")
		line := m["line"].(map[string]any)
		if int64(m["parent"].(float64)) != lid || m["n"].(float64) != 1 || line["text"] != "me #!@$" || line["name"] != "Bo" || line["lid"] == nil {
			t.Fatalf("thread_said %v", m)
		}
		if last := m["last"].(map[string]any); last["name"] != "Bo" {
			t.Fatalf("last %v", last)
		}
	}
	// The channel's history carries the count; the reply is not a line of it.
	r.do(ada, map[string]any{"t": "channel_join", "channel": "global"})
	hist := one(t, drain(ada), "channel_joined")["history"].([]any)
	if len(hist) != 1 {
		t.Fatalf("history %v", hist)
	}
	if th := hist[0].(map[string]any)["th"].(map[string]any); th["n"].(float64) != 1 {
		t.Fatalf("th %v", th)
	}
	// thread_get answers the replies; a reply's own lid takes reactions, not replies.
	r.do(ada, map[string]any{"t": "thread_get", "channel": "global", "parent": lid})
	th := one(t, drain(ada), "thread")
	lines := th["lines"].([]any)
	if th["known"] != true || len(lines) != 1 {
		t.Fatalf("thread %v", th)
	}
	reply := int64(lines[0].(map[string]any)["lid"].(float64))
	r.do(ada, map[string]any{"t": "react", "line": reply, "e": "laugh"})
	if m := one(t, drain(bo), "reacted"); reactedText(m) != "laugh:7" {
		t.Fatalf("reaction on a reply %v", m)
	}
	if m := one(t, drain(web), "reacted"); reactedText(m) != "laugh:7" {
		t.Fatalf("web reaction on a reply %v", m)
	}
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "thread_say", "channel": "global", "parent": reply, "text": "nested"})
	r.do(ada, map[string]any{"t": "thread_say", "channel": "global", "parent": 12345, "text": "made up"})
	errs := kinds(drain(ada), "error")
	if len(errs) != 2 || errs[0]["code"] != "thread" {
		t.Fatalf("errors %v", errs)
	}
	// Kept on the site with its parent.
	r.cs.flush(0)
	var stored []chatEntry
	for _, e := range r.sink.all() {
		if e.K == "line" && e.Parent == lid {
			stored = append(stored, e)
		}
	}
	if len(stored) != 1 || stored[0].Text != "me #!@$" || stored[0].Room != "global" {
		t.Fatalf("stored %+v", stored)
	}
}

func TestThreadsKeepTheChannelsRules(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{})
	bo := r.game(9, "Bo", lists{ign: []int64{7}})
	r.do(bo, map[string]any{"t": "channel_say", "channel": "global", "text": "line"})
	lid := lidOf(t, drain(ada), "channel_said")
	drain(bo)
	// The same bucket as channel_say: three, then slow down.
	for i := 0; i < 5; i++ {
		r.do(ada, map[string]any{"t": "thread_say", "channel": "global", "parent": lid, "text": "again " + string(rune('a'+i))})
	}
	if n := len(kinds(drain(ada), "thread_said")); n != chanSayBurst {
		t.Fatalf("%d replies went", n)
	}
	// Bo blocks Ada: the game hides her lines (realtimeHidden); her replies are not counted for Bo.
	r.do(bo, map[string]any{"t": "channel_join", "channel": "global"})
	hist := one(t, drain(bo), "channel_joined")["history"].([]any)
	if _, has := hist[0].(map[string]any)["th"]; has {
		t.Fatalf("Bo sees replies by someone he blocks: %v", hist[0])
	}
	// Not open: refused. The cone of silence: refused.
	cy := r.hub.Connect(Claims{Sub: 11, Name: "Cy"})
	r.do(cy, map[string]any{"t": "thread_say", "channel": "global", "parent": lid, "text": "hi"})
	if errs := kinds(drain(cy), "error"); len(errs) != 1 || errs[0]["message"] != chanNotIn {
		t.Fatalf("errors %v", errs)
	}
	r.do(cy, map[string]any{"t": "groups_watch", "token": chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": 11, "g": "", "c": "global", "s": "chat", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	r.do(cy, map[string]any{"t": "channel_join", "channel": "global"})
	drain(cy)
	r.do(cy, map[string]any{"t": "thread_say", "channel": "global", "parent": lid, "text": "hi"})
	if errs := kinds(drain(cy), "error"); len(errs) != 1 || errs[0]["message"] != chanSuspended {
		t.Fatalf("errors %v", errs)
	}
}

func TestThreadsFromTheWebsite(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{})
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "old line"})
	lid := lidOf(t, drain(ada), "channel_said")
	code, out := webSayService(r.hub, []byte(`{"player":9,"name":"Bo","channel":"global","rooms":"global,newbie","text":"from the web","parent":`+i64s(lid)+`}`))
	if code != http.StatusOK || out.(map[string]any)["status"] != "sent" {
		t.Fatalf("%d %v", code, out)
	}
	m := one(t, drain(ada), "thread_said")
	if line := m["line"].(map[string]any); line["web"] != true || line["text"] != "from the web" {
		t.Fatalf("%v", m)
	}
	reply := int64(out.(map[string]any)["line"].(map[string]any)["lid"].(int64))
	code, _ = webSayService(r.hub, []byte(`{"player":9,"name":"Bo","channel":"global","rooms":"global,newbie","text":"nested","parent":`+i64s(reply)+`,"kept":true}`))
	if code != http.StatusNotFound {
		t.Fatalf("a reply to a reply: %d", code)
	}
	code, _ = webSayService(r.hub, []byte(`{"player":9,"name":"Bo","channel":"global","rooms":"global,newbie","text":"made up","parent":99}`))
	if code != http.StatusNotFound {
		t.Fatalf("a reply to a reply: %d", code)
	}
}

func i64s(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestIMThreadsArePrivateLinesWithAParent(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{bud: []int64{9}})
	bo := r.game(9, "Bo", lists{rev: []int64{7}})
	r.do(ada, map[string]any{"t": "im_send", "to": 9, "text": "first"})
	r.now = r.now.Add(time.Second)
	r.do(bo, map[string]any{"t": "im_send", "to": 7, "text": "a reply", "parent": 1})
	got := kinds(drain(ada), "im")
	if len(got) != 2 || got[1]["line"].(map[string]any)["parent"].(float64) != 1 {
		t.Fatalf("ada got %v", got)
	}
	drain(bo)
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "im_send", "to": 9, "text": "nested", "parent": 2})
	r.do(ada, map[string]any{"t": "im_send", "to": 9, "text": "future", "parent": 99})
	nos := kinds(drain(ada), "im_no")
	if len(nos) != 2 || nos[0]["reason"] != "thread" {
		t.Fatalf("im_no %v", nos)
	}
	r.cs.flush(0)
	n := 0
	for _, e := range r.sink.all() {
		if e.K == "im" && e.Parent == 1 && e.Text == "a reply" {
			n++
		}
	}
	if n != 1 {
		t.Fatal("the IM reply was not kept with its parent")
	}
}

func TestPresenceListsStatusesIdleAndBuddyStreets(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{bud: []int64{9}})
	r.do(ada, map[string]any{"t": "join", "street": "GA5"})
	cy := r.game(11, "Cy", lists{})
	r.do(cy, map[string]any{"t": "join", "street": "GA6"})
	bo := r.web(9, "Bo", lists{rev: []int64{7}})
	p := one(t, drain(bo), "presence")
	if got := names(p["players"].([]any)); got != "Bo:web,Ada:game@GA5,Cy:game" {
		t.Fatalf("presence %s", got)
	}
	// The game's count: everyone Ada may see but her.
	r.do(ada, map[string]any{"t": "presence_watch"})
	if c := one(t, drain(ada), "presence_count"); c["n"].(float64) != 2 {
		t.Fatalf("count %v", c)
	}
	// Five minutes without input: idle; a move: back.
	r.now = r.now.Add(presenceIdle + time.Second)
	r.do(cy, map[string]any{"t": "move", "x": 1, "y": 1})
	r.settle(presenceTick)
	ups := kinds(drain(bo), "presence_up")
	statuses := map[string]string{}
	for _, u := range ups {
		pl := u["player"].(map[string]any)
		statuses[pl["name"].(string)] = pl["status"].(string)
	}
	if statuses["Ada"] != "idle" || statuses["Bo"] != "idle" || statuses["Cy"] != "" {
		t.Fatalf("after five minutes %v", statuses)
	}
	r.do(bo, map[string]any{"t": "active"})
	r.settle(presenceSettle)
	if u := one(t, drain(bo), "presence_up"); u["player"].(map[string]any)["status"] != "web" {
		t.Fatalf("active again %v", u)
	}
	// Ada changes street: her buddy hears it; a reload is not leaving; leaving is, after the grace.
	r.do(ada, map[string]any{"t": "join", "street": "GA7"})
	r.settle(presenceSettle)
	if u := one(t, drain(bo), "presence_up"); u["player"].(map[string]any)["street"] != "GA7" {
		t.Fatalf("moved %v", u)
	}
	r.hub.Disconnect(ada)
	r.settle(presenceSettle)
	if g := kinds(drain(bo), "presence_gone"); len(g) != 0 {
		t.Fatal("gone within the grace")
	}
	r.now = r.now.Add(presenceGrace + time.Second)
	r.settle(presenceGrace + time.Second)
	if g := one(t, drain(bo), "presence_gone"); g["id"].(float64) != 7 {
		t.Fatalf("gone %v", g)
	}
}

func TestPresenceBlocksHideBothWays(t *testing.T) {
	r := newLiveRig(t)
	r.game(7, "Ada", lists{ign: []int64{9}}) // Ada blocks Bo
	r.game(11, "Cy", lists{})
	bo := r.web(9, "Bo", lists{})
	if got := names(one(t, drain(bo), "presence")["players"].([]any)); got != "Bo:web,Cy:game" {
		t.Fatalf("Bo sees %s", got)
	}
	dee := r.web(13, "Dee", lists{ign: []int64{11}}) // Dee blocks Cy
	if got := names(one(t, drain(dee), "presence")["players"].([]any)); got != "Dee:web,Ada:game,Bo:web" {
		t.Fatalf("Dee sees %s", got)
	}
	// Cy's count leaves out Dee, who blocks her (only her web list says so).
	cy := r.hub.ClientLocked(11)
	r.do(cy, map[string]any{"t": "presence_watch"})
	if c := one(t, drain(cy), "presence_count"); c["n"].(float64) != 2 {
		t.Fatalf("Cy's count %v", c)
	}
}

func TestAppearOfflineHidesFromEveryoneButNotTheirWords(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{bud: []int64{9}, off: true})
	bo := r.game(9, "Bo", lists{rev: []int64{7}})
	web := r.web(11, "Cy", lists{bud: []int64{7}})
	// Not in the panel, not online for buddies, not in a roster, offline in IMs.
	if got := names(one(t, drain(web), "presence")["players"].([]any)); got != "Cy:web,Bo:game" {
		t.Fatalf("presence %s", got)
	}
	r.do(bo, map[string]any{"t": "buddies_watch", "token": r.buddyToken(9, nil, []int64{7}, nil)})
	if st := one(t, drain(bo), "buddies_status"); len(st["online"].([]any)) != 0 {
		t.Fatalf("buddies_status %v", st)
	}
	r.do(bo, map[string]any{"t": "channel_who", "channel": "global"})
	if note := one(t, drain(bo), "channel_note"); strings.Contains(note["text"].(string), "Ada") {
		t.Fatalf("roster %v", note)
	}
	r.do(ada, map[string]any{"t": "channel_who", "channel": "global"})
	if note := one(t, drain(ada), "channel_note"); !strings.Contains(note["text"].(string), "Ada") {
		t.Fatalf("her own roster leaves her out: %v", note)
	}
	r.do(bo, map[string]any{"t": "im_send", "to": 7, "text": "there?"})
	if im := one(t, drain(bo), "im"); im["with"].(map[string]any)["online"] != false {
		t.Fatalf("im %v", im)
	}
	// She still chats by name.
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "boo"})
	if said := one(t, drain(bo), "channel_said"); said["name"] != "Ada" {
		t.Fatalf("said %v", said)
	}
	drain(ada)
	// The site switches it off: buddies hear her online, the panel lists her, her sockets are told.
	code, _ := presenceHideService(r.hub, []byte(`{"player":7,"off":false}`))
	r.settle(presenceSettle)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if on := one(t, drain(bo), "buddy_online"); on["name"] != "Ada" {
		t.Fatalf("buddy_online %v", on)
	}
	if u := one(t, drain(web), "presence_up"); u["player"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("presence_up %v", u)
	}
	if you := one(t, drain(ada), "presence_you"); you["off"] != false {
		t.Fatalf("presence_you %v", you)
	}
	// And on again: gone for everyone.
	presenceHideService(r.hub, []byte(`{"player":7,"off":true}`))
	r.settle(presenceSettle)
	one(t, drain(bo), "buddy_offline")
	if g := one(t, drain(web), "presence_gone"); g["id"].(float64) != 7 {
		t.Fatalf("gone %v", g)
	}
	// The console still sees her, marked; a bad call is refused.
	_, out := adminOnline(r.hub, []byte(`{}`))
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"name":"Ada"`) || !strings.Contains(string(b), `"off":true`) {
		t.Fatalf("online %s", b)
	}
	if code, _ := presenceHideService(r.hub, []byte(`{"player":7}`)); code != http.StatusBadRequest {
		t.Fatalf("no off: %d", code)
	}
}

func TestCommunityNotesReachSignedInReadersNotBlockedOnes(t *testing.T) {
	r := newLiveRig(t)
	bo := r.web(9, "Bo", lists{})
	cy := r.web(11, "Cy", lists{ign: []int64{7}})
	drain(bo)
	drain(cy)
	code, out := communityNoteService(r.hub, []byte(`{"board":"off-topic","thread":12,"post":34,"author":7,"kind":"reply"}`))
	if code != http.StatusOK || out.(map[string]any)["told"] != 1 {
		t.Fatalf("%d %v", code, out)
	}
	if m := one(t, drain(bo), "board_post"); m["board"] != "off-topic" || m["thread"].(float64) != 12 {
		t.Fatalf("%v", m)
	}
	if len(drain(cy)) != 0 {
		t.Fatal("a reader who blocks the writer was told")
	}
	if code, _ := communityNoteService(r.hub, []byte(`{"board":"<b>","thread":1,"post":1,"author":7,"kind":"reply"}`)); code != http.StatusBadRequest {
		t.Fatal("a bad board was taken")
	}
}

func TestABanClosesTheWebsiteSocketsToo(t *testing.T) {
	r := newLiveRig(t)
	w := r.web(9, "Bo", lists{})
	drain(w)
	adminBan(r.hub, []byte(`{"player":9,"reason":"spam"}`))
	if !w.closed || w.why != closeBanned || len(r.hub.WebLocked(9)) != 0 {
		t.Fatal("the web socket stayed open")
	}
}

// The socket itself: a web ticket opens a web socket beside the game's; a page from another site is refused.
func TestWebTicketsOverTheSocketAndOrigins(t *testing.T) {
	srv := NewServer(NewTickets(testSecret, nil), NewHub(nil), []string{"groddle.localhost"})
	url := startServerWith(t, srv)
	game := signIn(t, url, 7, "Ada")
	jti++
	now := time.Now().Unix()
	webTicket := signTicket(t, Claims{Sub: 7, Name: "Ada", Iat: now, Exp: now + 60, Jti: "w" + i64s(int64(jti)), Via: "web"}, testSecret)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://groddle.localhost"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	web := &peer{t: t, conn: conn}
	web.send(map[string]any{"t": "auth", "ticket": webTicket})
	if h := web.next("hello"); h["web"] != true {
		t.Fatalf("hello %v", h)
	}
	game.send(map[string]any{"t": "ping"})
	game.next("pong") // the game's socket is still open
	_, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another site's page was let in: %v", err)
	}
}

func startServerWith(t *testing.T, s *Server) string {
	t.Helper()
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
}

func TestIMsFromTheWebsiteAndOnItsSockets(t *testing.T) {
	r := newLiveRig(t)
	bo := r.game(9, "Bo", lists{rev: []int64{7}})
	ada := r.web(7, "Ada", lists{bud: []int64{9}})
	drain(ada)
	code, out := webIMService(r.hub, []byte(`{"player":7,"name":"Ada","to":9,"text":"hi from the web"}`))
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	got := one(t, drain(bo), "im")
	if got["line"].(map[string]any)["text"] != "hi from the web" || got["with"].(map[string]any)["id"].(float64) != 7 {
		t.Fatalf("game got %v", got)
	}
	mine := one(t, drain(ada), "im")
	if mine["with"].(map[string]any)["id"].(float64) != 9 || mine["with"].(map[string]any)["online"] != true {
		t.Fatalf("web copy %v", mine)
	}
	// Bo answers in the game: Ada's tab hears it; she reacts and reads from the tab.
	r.now = r.now.Add(time.Second)
	r.do(bo, map[string]any{"t": "im_send", "to": 7, "text": "hey"})
	if m := one(t, drain(ada), "im"); m["line"].(map[string]any)["text"] != "hey" {
		t.Fatalf("%v", m)
	}
	drain(bo)
	r.do(ada, map[string]any{"t": "im_react", "with": 9, "line": 2, "e": "heart"})
	if m := one(t, drain(bo), "im_reacted"); reactedText(m) != "heart:7" {
		t.Fatalf("%v", m)
	}
	r.do(ada, map[string]any{"t": "im_read", "with": 9})
	// Refusals in the game's words; a ban refuses.
	code, out = webIMService(r.hub, []byte(`{"player":7,"name":"Ada","to":7,"text":"me"}`))
	if code != http.StatusForbidden || out.(map[string]string)["reason"] != "self" {
		t.Fatalf("%d %v", code, out)
	}
	adminBan(r.hub, []byte(`{"player":7}`))
	if code, _ := webIMService(r.hub, []byte(`{"player":7,"name":"Ada","to":9,"text":"x"}`)); code != http.StatusForbidden {
		t.Fatal("a banned account sent an IM")
	}
}

func TestReactionsFromTheWebsiteSocket(t *testing.T) {
	r := newLiveRig(t)
	ada := r.game(7, "Ada", lists{})
	web := r.web(9, "Bo", lists{chans: "global,newbie"})
	drain(web)
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "react to me"})
	lid := lidOf(t, drain(web), "channel_said")
	drain(ada)
	r.do(web, map[string]any{"t": "react", "line": lid, "e": "tada"})
	if m := one(t, drain(ada), "reacted"); reactedText(m) != "tada:9" {
		t.Fatalf("%v", m)
	}
	if m := one(t, drain(web), "reacted"); reactedText(m) != "tada:9" {
		t.Fatalf("%v", m)
	}
	// A trade line the web list does not read: refused.
	r2 := newLiveRig(t)
	cy := r2.game(11, "Cy", lists{chans: "global,newbie,trade"})
	r2.do(cy, map[string]any{"t": "channel_join", "channel": "trade"})
	drain(cy)
	r2.do(cy, map[string]any{"t": "channel_say", "channel": "trade", "text": "wares"})
	tl := lidOf(t, drain(cy), "channel_said")
	w2 := r2.web(9, "Bo", lists{})
	drain(w2)
	r2.do(w2, map[string]any{"t": "react", "line": tl, "e": "like"})
	if e := one(t, drain(w2), "error"); e["code"] != "react" {
		t.Fatalf("%v", e)
	}
}
