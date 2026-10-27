package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

var chanTestSecret = []byte("channels-test-secret-channels-test-secret")

// chanRig is a hub with only the channels module and a manual clock.
type chanRig struct {
	t   *testing.T
	hub *Hub
	m   *channels
	now time.Time
}

func newChanRig(t *testing.T) *chanRig {
	r := &chanRig{t: t, now: time.Unix(1_700_000_000, 0)}
	savedSecret := chanSecretForTest
	chanSecretForTest = chanTestSecret
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.m = newChannels(h).(*channels); return r.m }}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	chanSecretForTest = savedSecret
	return r
}

func (r *chanRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

func (r *chanRig) send(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

// chanSign makes a token as the site's Groups::watch does.
func chanSign(secret []byte, claims map[string]any) string {
	raw, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func chanIDs(ids ...int64) string {
	var parts []string
	var at int64
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id-at, 36))
		at = id
	}
	return strings.Join(parts, ",")
}

func (r *chanRig) token(sub int64, groups []int64, chans, silence string) string {
	return chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": sub, "g": chanIDs(groups...), "c": chans, "s": silence, "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})
}

func (r *chanRig) watch(cl *Client, groups []int64, chans, silence string) {
	r.send(cl, map[string]any{"t": "groups_watch", "token": r.token(cl.ID(), groups, chans, silence)})
}

func chanKind(msgs []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, m := range msgs {
		if m["t"] == kind {
			out = append(out, m)
		}
	}
	return out
}

func chanNotes(msgs []map[string]any) []string {
	var out []string
	for _, m := range chanKind(msgs, "channel_note") {
		out = append(out, fmt.Sprint(m["channel"], ": ", m["text"]))
	}
	return out
}

func chanErrors(msgs []map[string]any) []string {
	var out []string
	for _, m := range chanKind(msgs, "error") {
		out = append(out, fmt.Sprint(m["code"], ": ", m["message"]))
	}
	return out
}

func TestChannelsWatchRefusesWhatTheSiteDidNotSign(t *testing.T) {
	r := newChanRig(t)
	a := r.connect(1, "Ada")
	good := r.token(1, []int64{12}, "global,help", "")
	forged := chanSign([]byte("not-the-secret-not-the-secret-not-the-secret"), map[string]any{"k": "groups", "sub": 1, "g": "c", "c": "global", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})
	stale := chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": 1, "g": "c", "c": "global", "iat": r.now.Add(-2 * time.Hour).Unix(), "exp": r.now.Add(-time.Hour).Unix()})
	notMine := r.token(2, []int64{12}, "global", "")
	buddyList := chanSign(chanTestSecret, map[string]any{"k": "buddies", "sub": 1, "b": "c", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})
	both := r.token(1, nil, "help,newbie", "")
	for name, tok := range map[string]string{"forged": forged, "stale": stale, "not mine": notMine, "a buddy list": buddyList, "both helps": both, "junk": "x.y"} {
		r.send(a, map[string]any{"t": "groups_watch", "token": tok})
		got := drain(a)
		if errs := chanErrors(got); len(errs) != 1 || !strings.HasPrefix(errs[0], "channels: Your groups could not be checked") {
			t.Fatalf("%s: %v", name, got)
		}
		r.now = r.now.Add(5 * time.Second)
	}
	// Nothing opens without a list.
	r.send(a, map[string]any{"t": "channel_join", "channel": "global"})
	if errs := chanErrors(drain(a)); len(errs) != 1 || !strings.Contains(errs[0], "not checked yet") {
		t.Fatalf("join without a list: %v", errs)
	}
	r.send(a, map[string]any{"t": "groups_watch", "token": good})
	st := chanKind(drain(a), "groups_status")
	if len(st) != 1 || fmt.Sprint(st[0]["groups"]) != "[12]" || fmt.Sprint(st[0]["channels"]) != "[global help]" || st[0]["newbie"] != false {
		t.Fatalf("status: %v", st)
	}
}

func TestChannelsGlobalChatAndHistory(t *testing.T) {
	r := newChanRig(t)
	a, b, c := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	for _, cl := range []*Client{a, b, c} {
		r.watch(cl, nil, "global,help", "")
		drain(cl)
	}
	r.send(a, map[string]any{"t": "channel_join", "channel": "global"})
	r.send(b, map[string]any{"t": "channel_join", "channel": "global"})
	got := drain(a)
	if j := chanKind(got, "channel_joined"); len(j) != 1 || len(j[0]["history"].([]any)) != 0 {
		t.Fatalf("a joined: %v", got)
	}
	if len(chanNotes(got)) != 0 || len(chanNotes(drain(b))) != 0 {
		t.Fatal("a channel does not announce who joins (group.js:1011)")
	}
	r.send(a, map[string]any{"t": "channel_say", "channel": "global", "text": "  hello   there  "})
	for _, cl := range []*Client{a, b} {
		s := chanKind(drain(cl), "channel_said")
		if len(s) != 1 || s[0]["text"] != "hello there" || s[0]["name"] != "Ada" || s[0]["channel"] != "global" {
			t.Fatalf("%s heard %v", cl.Name(), s)
		}
	}
	if len(drain(c)) != 0 {
		t.Fatal("Cy is not in Global Chat")
	}
	// Cy opens it later and gets the history; the ring keeps the last chanHistory lines.
	for i := 0; i < chanHistory+5; i++ {
		r.now = r.now.Add(time.Second)
		r.send(b, map[string]any{"t": "channel_say", "channel": "global", "text": fmt.Sprint("line ", i)})
	}
	if errs := chanErrors(drain(b)); len(errs) != 0 {
		t.Fatalf("one a second is fine: %v", errs)
	}
	r.send(c, map[string]any{"t": "channel_join", "channel": "global"})
	j := chanKind(drain(c), "channel_joined")
	h := j[0]["history"].([]any)
	if len(h) != chanHistory || h[0].(map[string]any)["text"] != "line 5" || h[len(h)-1].(map[string]any)["text"] != fmt.Sprint("line ", chanHistory+4) {
		t.Fatalf("history %d lines, first %v", len(h), h[0])
	}
	// The length cap street chat has.
	r.now = r.now.Add(time.Second)
	r.send(c, map[string]any{"t": "channel_say", "channel": "global", "text": strings.Repeat("x", 500)})
	if s := chanKind(drain(c), "channel_said"); len([]rune(s[0]["text"].(string))) != maxChat {
		t.Fatal("not capped")
	}
	// Leaving: no more lines.
	r.send(c, map[string]any{"t": "channel_leave", "channel": "global"})
	if l := chanKind(drain(c), "channel_left"); len(l) != 1 {
		t.Fatal("no channel_left")
	}
	r.now = r.now.Add(time.Second)
	r.send(a, map[string]any{"t": "channel_say", "channel": "global", "text": "bye"})
	if len(drain(c)) != 0 {
		t.Fatal("left, still hears")
	}
	r.send(c, map[string]any{"t": "channel_say", "channel": "global", "text": "hey"})
	if errs := chanErrors(drain(c)); len(errs) != 1 || errs[0] != "channels: "+chanNotIn {
		t.Fatalf("saying outside: %v", errs)
	}
}

func TestChannelsRateLimit(t *testing.T) {
	r := newChanRig(t)
	a := r.connect(1, "Ada")
	r.watch(a, nil, "global", "")
	r.send(a, map[string]any{"t": "channel_join", "channel": "global"})
	drain(a)
	for i := 0; i < chanSayBurst+2; i++ {
		r.send(a, map[string]any{"t": "channel_say", "channel": "global", "text": "spam"})
	}
	got := drain(a)
	if len(chanKind(got, "channel_said")) != chanSayBurst || len(chanKind(got, "error")) != 2 || chanKind(got, "error")[0]["code"] != "slow_down" {
		t.Fatalf("burst: %v", got)
	}
	r.now = r.now.Add(chanSayEvery)
	r.send(a, map[string]any{"t": "channel_say", "channel": "global", "text": "again"})
	if len(chanKind(drain(a), "channel_said")) != 1 {
		t.Fatal("a second later it goes")
	}
}

func TestChannelsLiveHelpByLevelAndTradeByUpgrade(t *testing.T) {
	r := newChanRig(t)
	n, v := r.connect(1, "Newt"), r.connect(2, "Vet")
	r.watch(n, nil, "global,newbie", "")
	r.watch(v, nil, "global,help,trade", "")
	if st := chanKind(drain(n), "groups_status"); st[0]["newbie"] != true || fmt.Sprint(st[0]["channels"]) != "[global help]" {
		t.Fatalf("newbie status %v", st)
	}
	drain(v)
	r.send(n, map[string]any{"t": "channel_join", "channel": "help"})
	r.send(v, map[string]any{"t": "channel_join", "channel": "help"})
	drain(n)
	drain(v)
	r.send(n, map[string]any{"t": "channel_say", "channel": "help", "text": "how do I jump"})
	if s := chanKind(drain(n), "channel_said"); len(s) != 1 || s[0]["channel"] != "help" {
		t.Fatal("newbie line")
	}
	if len(drain(v)) != 0 {
		t.Fatal("the regular Live Help is another channel (main.js:579-585)")
	}
	// Trade: only with the upgrade.
	r.send(n, map[string]any{"t": "channel_join", "channel": "trade"})
	if errs := chanErrors(drain(n)); len(errs) != 1 || !strings.Contains(errs[0], "Trade Channelling") {
		t.Fatalf("trade without the upgrade: %v", errs)
	}
	r.send(v, map[string]any{"t": "channel_join", "channel": "trade"})
	if j := chanKind(drain(v), "channel_joined"); len(j) != 1 {
		t.Fatal("trade with the upgrade")
	}
	// Newt reaches level 11: the next list moves them to the regular Live Help.
	r.now = r.now.Add(10 * time.Second)
	r.watch(n, nil, "global,help", "")
	got := drain(n)
	if j := chanKind(got, "channel_joined"); len(j) != 1 || j[0]["channel"] != "help" || len(j[0]["history"].([]any)) != 0 {
		t.Fatalf("moved: %v", got)
	}
	if len(chanKind(got, "channel_left")) != 0 {
		t.Fatal("a move is not a leave")
	}
	r.send(n, map[string]any{"t": "channel_say", "channel": "help", "text": "now I help"})
	drain(n)
	if s := chanKind(drain(v), "channel_said"); len(s) != 1 {
		t.Fatal("same channel now")
	}
}

func TestChannelsConeOfSilence(t *testing.T) {
	r := newChanRig(t)
	a := r.connect(1, "Ada")
	r.watch(a, []int64{5}, "global,help,trade", "chat")
	for _, ch := range []string{"global", "help", "trade", "group:5"} {
		r.send(a, map[string]any{"t": "channel_join", "channel": ch})
	}
	drain(a)
	say := func(ch string) []map[string]any {
		r.now = r.now.Add(time.Second)
		r.send(a, map[string]any{"t": "channel_say", "channel": ch, "text": "hi"})
		return drain(a)
	}
	for _, ch := range []string{"global", "trade"} {
		if errs := chanErrors(say(ch)); len(errs) != 1 || errs[0] != "channels: "+chanSuspended {
			t.Fatalf("%s: %v", ch, errs)
		}
	}
	if len(chanKind(say("help"), "channel_said")) != 1 || len(chanKind(say("group:5"), "channel_said")) != 1 {
		t.Fatal("the chat cone is not the help cone, and groups have none")
	}
	r.now = r.now.Add(10 * time.Second)
	r.watch(a, []int64{5}, "global,help,trade", "help")
	drain(a)
	if errs := chanErrors(say("help")); len(errs) != 1 || errs[0] != "channels: "+chanSuspended {
		t.Fatalf("help cone: %v", errs)
	}
	if len(chanKind(say("global"), "channel_said")) != 1 {
		t.Fatal("lifted")
	}
}

func TestChannelsGroupChatIsForMembers(t *testing.T) {
	r := newChanRig(t)
	a, b, c := r.connect(1, "Ada"), r.connect(2, "Bo"), r.connect(3, "Cy")
	r.watch(a, []int64{12}, "global", "")
	r.watch(b, []int64{7, 12}, "global", "")
	r.watch(c, []int64{7}, "global", "")
	drain(a)
	drain(b)
	drain(c)
	r.send(a, map[string]any{"t": "channel_join", "channel": "group:12"})
	if n := chanNotes(drain(a)); fmt.Sprint(n) != "[group:12: You are the only person here.]" {
		t.Fatalf("a: %v", n)
	}
	r.send(b, map[string]any{"t": "channel_join", "channel": "group:12"})
	if n := chanNotes(drain(b)); fmt.Sprint(n) != "[group:12: Ada is here]" {
		t.Fatalf("b: %v", n)
	}
	if n := chanNotes(drain(a)); fmt.Sprint(n) != "[group:12: Bo has joined]" {
		t.Fatalf("a told: %v", n)
	}
	// Cy is not a member: the room stays shut, whatever Cy asks.
	r.send(c, map[string]any{"t": "channel_join", "channel": "group:12"})
	if errs := chanErrors(drain(c)); len(errs) != 1 || errs[0] != "channels: "+chanGroupMissing {
		t.Fatalf("c: %v", errs)
	}
	r.send(c, map[string]any{"t": "channel_say", "channel": "group:12", "text": "let me in"})
	if errs := chanErrors(drain(c)); len(errs) != 1 || errs[0] != "channels: "+chanNotIn {
		t.Fatalf("c says: %v", errs)
	}
	// Cy's own token, forged to name group 12, is refused and changes nothing.
	r.now = r.now.Add(10 * time.Second)
	forged := chanSign([]byte("a-guess-at-the-secret-a-guess-at-the-secret"), map[string]any{"k": "groups", "sub": 3, "g": chanIDs(7, 12), "c": "global", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})
	r.send(c, map[string]any{"t": "groups_watch", "token": forged})
	drain(c)
	r.send(c, map[string]any{"t": "channel_join", "channel": "group:12"})
	if errs := chanErrors(drain(c)); len(errs) != 1 || errs[0] != "channels: "+chanGroupMissing {
		t.Fatalf("c forged: %v", errs)
	}
	// Ada's token, sent from Cy's socket, is not Cy's.
	r.send(c, map[string]any{"t": "groups_watch", "token": r.token(1, []int64{12}, "global", "")})
	if errs := chanErrors(drain(c)); len(errs) != 1 || !strings.Contains(errs[0], "not yours") {
		t.Fatalf("c borrowed: %v", errs)
	}
	r.send(a, map[string]any{"t": "channel_say", "channel": "group:12", "text": "hi Bo"})
	for _, cl := range []*Client{a, b} {
		if s := chanKind(drain(cl), "channel_said"); len(s) != 1 || s[0]["text"] != "hi Bo" {
			t.Fatalf("%s: %v", cl.Name(), s)
		}
	}
	if got := drain(c); len(got) != 0 {
		t.Fatalf("Cy heard %v", got)
	}
	// /who.
	r.send(b, map[string]any{"t": "channel_who", "channel": "group:12"})
	if n := chanNotes(drain(b)); fmt.Sprint(n) != "[group:12: Ada and Bo are here]" {
		t.Fatalf("who: %v", n)
	}
	// Bo leaves the group on the site: the next list closes the chat, and Ada hears it.
	r.now = r.now.Add(10 * time.Second)
	r.watch(b, []int64{7}, "global", "")
	got := drain(b)
	if l := chanKind(got, "channel_left"); len(l) != 1 || l[0]["channel"] != "group:12" {
		t.Fatalf("b: %v", got)
	}
	if st := chanKind(got, "groups_status"); fmt.Sprint(st[0]["open"]) != "[]" {
		t.Fatalf("b open: %v", st)
	}
	if n := chanNotes(drain(a)); fmt.Sprint(n) != "[group:12: Bo has left]" {
		t.Fatalf("a told: %v", n)
	}
	r.send(a, map[string]any{"t": "channel_say", "channel": "group:12", "text": "gone?"})
	drain(a)
	if len(drain(b)) != 0 {
		t.Fatal("Bo left")
	}
}

func TestChannelsReloadKeepsChatsAndOfflineLeavesThem(t *testing.T) {
	r := newChanRig(t)
	a, b := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.watch(a, []int64{3}, "global", "")
	r.watch(b, []int64{3}, "global", "")
	for _, cl := range []*Client{a, b} {
		r.send(cl, map[string]any{"t": "channel_join", "channel": "group:3"})
		r.send(cl, map[string]any{"t": "channel_join", "channel": "global"})
	}
	drain(a)
	drain(b)
	// Bo reloads: a new socket for the same character keeps the chats, and opening one again tells nobody.
	b2 := r.connect(2, "Bo")
	r.send(b2, map[string]any{"t": "channel_join", "channel": "group:3"})
	if j := chanKind(drain(b2), "channel_joined"); len(j) != 1 {
		t.Fatal("history again")
	}
	if n := chanNotes(drain(a)); len(n) != 0 {
		t.Fatalf("a reload is not a join: %v", n)
	}
	r.send(a, map[string]any{"t": "channel_say", "channel": "global", "text": "still here?"})
	drain(a)
	if s := chanKind(drain(b2), "channel_said"); len(s) != 1 {
		t.Fatal("the new socket hears")
	}
	// Bo goes offline: every chat is left (groups_logout), the group hears it.
	r.hub.Disconnect(b2)
	if n := chanNotes(drain(a)); fmt.Sprint(n) != "[group:3: Bo has left]" {
		t.Fatalf("offline: %v", n)
	}
	if len(r.m.open[2]) != 0 || r.m.watch[2] != nil || r.m.rooms["global"].members[2] {
		t.Fatal("state kept for an offline player")
	}
}

func TestChannelsRejectsUnknownChats(t *testing.T) {
	r := newChanRig(t)
	a := r.connect(1, "Ada")
	r.watch(a, []int64{3}, "global", "")
	drain(a)
	for _, ch := range []string{"", "newbie", "Global", "group:", "group:0", "group:03", "group:-1", "group:x", "g3"} {
		r.send(a, map[string]any{"t": "channel_join", "channel": ch})
		if errs := chanErrors(drain(a)); len(errs) != 1 || errs[0] != "channels: That is not a chat the game has." {
			t.Fatalf("%q: %v", ch, errs)
		}
	}
	if len(r.m.open[1]) != 0 {
		t.Fatal("nothing opened")
	}
}

func TestRosterText(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  string
	}{
		{nil, "You are the only person here."},
		{[]string{"Ada"}, "Ada is here"},
		{[]string{"Ada", "Bo"}, "Ada and Bo are here"},
		{[]string{"Ada", "Bo", "Cy"}, "Ada, Bo and Cy are here"},
	} {
		if got := rosterText(c.names); got != c.want {
			t.Fatalf("%v: %q", c.names, got)
		}
	}
	many := make([]string, chanWhoNames+4)
	for i := range many {
		many[i] = fmt.Sprint("P", i)
	}
	if got := rosterText(many); !strings.HasSuffix(got, " and 4 others are here") || strings.Count(got, ",") != chanWhoNames-1 {
		t.Fatalf("many: %q", got)
	}
}
