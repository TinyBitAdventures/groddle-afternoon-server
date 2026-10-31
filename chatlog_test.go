package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// chatRig is a hub with the chat log, the channels and parties, a manual clock and a log in a temp dir.
type chatRig struct {
	t   *testing.T
	hub *Hub
	dir string
	now time.Time
}

func newChatRig(t *testing.T, at time.Time) *chatRig {
	t.Helper()
	r := &chatRig{t: t, dir: t.TempDir(), now: at}
	savedDir, savedSecret, savedModules := chatlogDirForTest, chanSecretForTest, modules
	chatlogDirForTest, chanSecretForTest = r.dir, chanTestSecret
	modules = []moduleFactory{newChatlogModule, newChannels, newParties}
	r.hub = NewHub(func() time.Time { return r.now })
	chatlogDirForTest, chanSecretForTest, modules = savedDir, savedSecret, savedModules
	t.Cleanup(r.hub.Shutdown)
	return r
}

func (r *chatRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

func (r *chatRig) send(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

// lines reads one day's file as written so far (after the writer has flushed).
func (r *chatRig) lines(day string) []chatLine {
	r.t.Helper()
	r.hub.chatlogOf().flush()
	b, err := os.ReadFile(filepath.Join(r.dir, day+".jsonl"))
	if err != nil {
		r.t.Fatal(err)
	}
	var out []chatLine
	for _, row := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		var l chatLine
		if err := json.Unmarshal([]byte(row), &l); err != nil {
			r.t.Fatalf("not a JSON line: %q", row)
		}
		out = append(out, l)
	}
	return out
}

func (r *chatRig) query(q map[string]any) (int, map[string]any) {
	r.t.Helper()
	body, _ := json.Marshal(q)
	code, out := adminChatlog(r.hub, body)
	raw, _ := json.Marshal(out)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return code, m
}

func texts2(lines []any) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.(map[string]any)["text"].(string))
	}
	return out
}

var oct31 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func TestChatlogWritesEveryKindAndTheRefusedLines(t *testing.T) {
	r := newChatRig(t, oct31)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.send(ada, map[string]any{"t": "join", "street": "GA5HERE"})
	r.send(bo, map[string]any{"t": "join", "street": "GA5HERE"})
	r.send(ada, map[string]any{"t": "leave"}) // not on a street: nothing said, nothing written
	r.send(ada, map[string]any{"t": "chat", "text": "nobody hears this"})
	r.send(ada, map[string]any{"t": "join", "street": "GA5HERE"})
	r.send(ada, map[string]any{"t": "chat", "text": "  hello\tBo  "})
	for i := 0; i < chatBurst; i++ {
		r.send(ada, map[string]any{"t": "chat", "text": fmt.Sprintf("spam %d", i)})
	}
	// Channels: Bo is under the cone of silence for chat.
	r.now = r.now.Add(time.Minute)
	r.send(ada, map[string]any{"t": "groups_watch", "token": chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": 1, "g": "", "c": "global", "s": "", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	r.send(bo, map[string]any{"t": "groups_watch", "token": chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": 2, "g": "", "c": "global", "s": "chat", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	r.send(ada, map[string]any{"t": "channel_join", "channel": "global"})
	r.send(bo, map[string]any{"t": "channel_join", "channel": "global"})
	r.send(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "hi all"})
	r.send(bo, map[string]any{"t": "channel_say", "channel": "global", "text": "buy gold"})
	// A party.
	r.now = r.now.Add(time.Minute)
	r.send(ada, map[string]any{"t": "party_invite", "id": 2})
	r.send(bo, map[string]any{"t": "party_accept", "from": 1})
	r.send(bo, map[string]any{"t": "party_chat", "text": "/who"}) // a slash command is refused, not chat
	r.send(bo, map[string]any{"t": "party_chat", "text": "party on"})
	for i := 0; i < chatBurst; i++ {
		r.send(bo, map[string]any{"t": "party_chat", "text": "again"})
	}

	got := r.lines("2026-10-31")
	type want struct {
		kind, where, name, text, why string
	}
	var wants []want
	wants = append(wants, want{"local", "GA5HERE", "Ada", "hello Bo", ""})
	for i := 0; i < chatBurst-1; i++ {
		wants = append(wants, want{"local", "GA5HERE", "Ada", fmt.Sprintf("spam %d", i), ""})
	}
	wants = append(wants, want{"local", "GA5HERE", "Ada", fmt.Sprintf("spam %d", chatBurst-1), "slow_down"},
		want{"channel", "global", "Ada", "hi all", ""}, want{"channel", "global", "Bo", "buy gold", "silenced"},
		want{"party", "", "Bo", "party on", ""})
	for i := 0; i < chatBurst-1; i++ {
		wants = append(wants, want{"party", "", "Bo", "again", ""})
	}
	wants = append(wants, want{"party", "", "Bo", "again", "slow_down"})
	if len(got) != len(wants) {
		t.Fatalf("%d lines, want %d: %+v", len(got), len(wants), got)
	}
	for i, w := range wants {
		g := got[i]
		if g.Kind != w.kind || g.Name != w.name || g.Text != w.text || g.Why != w.why || g.Delivered != (w.why == "") || (w.where != "" && g.Where != w.where) {
			t.Fatalf("line %d: %+v, want %+v", i, g, w)
		}
	}
	party := got[len(got)-1]
	if !strings.HasPrefix(party.Where, "party-2-") || got[len(got)-2].Where != party.Where {
		t.Fatalf("party id %q", party.Where)
	}
	if fmt.Sprint(got[len(got)-2].To) != "[1 2]" || party.To != nil {
		t.Fatalf("party to: %v / %v", got[len(got)-2].To, party.To)
	}
	if got[0].At != "2026-10-31T12:00:00.000Z" || got[0].ID != 1 {
		t.Fatalf("first line %+v", got[0])
	}
	for _, p := range []string{r.dir, filepath.Join(r.dir, "2026-10-31.jsonl")} {
		fi, _ := os.Stat(p)
		if want := map[bool]os.FileMode{true: 0o700, false: 0o600}[fi.IsDir()]; fi.Mode().Perm() != want {
			t.Fatalf("%s mode %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
}

func TestChatlogStartsANewFileEachUTCDayAndFlushesAtShutdown(t *testing.T) {
	r := newChatRig(t, time.Date(2026, 10, 30, 23, 59, 59, 0, time.UTC))
	ada := r.connect(1, "Ada")
	r.send(ada, map[string]any{"t": "join", "street": "GA5HERE"})
	r.send(ada, map[string]any{"t": "chat", "text": "late"})
	r.now = r.now.Add(2 * time.Second)
	r.send(ada, map[string]any{"t": "chat", "text": "early"})
	r.hub.Shutdown() // drains and closes; nothing waits on the flush call
	for day, text := range map[string]string{"2026-10-30": "late", "2026-10-31": "early"} {
		b, err := os.ReadFile(filepath.Join(r.dir, day+".jsonl"))
		if err != nil || strings.Count(string(b), "\n") != 1 || !strings.Contains(string(b), `"text":"`+text+`"`) {
			t.Fatalf("%s: %q %v", day, b, err)
		}
	}
	r.send(ada, map[string]any{"t": "chat", "text": "after"}) // after shutdown: dropped quietly, no panic
}

func TestChatlogPrunesOldDaysAtStartAndOnANewDay(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"2026-09-29.jsonl", "2026-09-30.jsonl", "2026-10-01.jsonl", "2026-10-31.jsonl", "notes.txt"} {
		os.WriteFile(filepath.Join(dir, n), []byte("{}\n"), 0o600)
	}
	now := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	c := newChatLog(dir, 30, func() time.Time { return now }, 8)
	go c.run()
	c.flush() // the writer has pruned before it takes a request
	left := func() string {
		es, _ := os.ReadDir(dir)
		var n []string
		for _, e := range es {
			n = append(n, e.Name())
		}
		return strings.Join(n, " ")
	}
	if got := left(); got != "2026-10-01.jsonl 2026-10-31.jsonl notes.txt" {
		t.Fatalf("after start: %s", got)
	}
	// The next day: its first line opens a new file and prunes the day that went past 30.
	now = time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC)
	c.addLocked(chatLine{At: "2026-11-01T00:00:01.000Z", Kind: "local", Where: "GA5", ID: 1, Name: "Ada", Text: "hi", Delivered: true, t: now})
	c.flush()
	if got := left(); got != "2026-10-31.jsonl 2026-11-01.jsonl notes.txt" {
		t.Fatalf("after a new day: %s", got)
	}
	c.close()
}

func TestChatlogAFloodOfRefusedLinesIsCappedAndCounted(t *testing.T) {
	r := newChatRig(t, oct31)
	ada := r.connect(1, "Ada")
	r.send(ada, map[string]any{"t": "join", "street": "GA5HERE"})
	for i := 0; i < chatBurst+100; i++ {
		r.send(ada, map[string]any{"t": "chat", "text": "flood"})
		drain(ada) // keeps reading, or the hub drops her as a slow client
	}
	r.now = r.now.Add(chatlogRefusedEvery)
	for i := 0; i < chatBurst+1; i++ { // the chat bucket has three tokens back, then refusals again: the first is written with the count
		r.send(ada, map[string]any{"t": "chat", "text": "flood"})
	}
	got := r.lines("2026-10-31")
	refused := 0
	for _, l := range got {
		if !l.Delivered {
			refused++
		}
	}
	last := got[len(got)-1]
	if refused != chatlogRefusedBurst+1 || last.Skipped != 100-chatlogRefusedBurst || last.Why != "slow_down" {
		t.Fatalf("%d refused lines, last %+v", refused, last)
	}
}

func TestChatlogQueryFiltersNewestFirstAndPages(t *testing.T) {
	r := newChatRig(t, oct31)
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.send(ada, map[string]any{"t": "join", "street": "GA5HERE"})
	r.send(bo, map[string]any{"t": "join", "street": "GA5HERE"})
	for i := 0; i < 7; i++ {
		r.now = r.now.Add(time.Minute)
		r.send(ada, map[string]any{"t": "chat", "text": fmt.Sprintf("Ada %d", i)})
		r.send(bo, map[string]any{"t": "chat", "text": fmt.Sprintf("bo SELLS gold %d", i)})
	}
	r.now = time.Date(2026, 11, 1, 0, 0, 30, 0, time.UTC) // a second day's file
	r.send(ada, map[string]any{"t": "chat", "text": "next day"})
	os.WriteFile(filepath.Join(r.dir, "2026-10-29.jsonl"), []byte("not json\n{\"at\":\"2026-10-29T10:00:00.000Z\",\"kind\":\"party\",\"where\":\"party-9-x\",\"id\":9,\"name\":\"Cy\",\"text\":\"old gold\",\"delivered\":true}\n{\"at\":\"half a li"), 0o600)

	code, out := r.query(map[string]any{})
	if code != 200 || out["status"] != "ok" || out["days"] != 30.0 || out["dropped"] != 0.0 {
		t.Fatalf("%d %v", code, out)
	}
	all := texts2(out["lines"].([]any))
	if len(all) != 16 || all[0] != "next day" || all[1] != "bo SELLS gold 6" || all[15] != "old gold" {
		t.Fatalf("all, newest first, the bad and the unfinished line passed over: %v", all)
	}
	for name, c := range map[string]struct {
		q    map[string]any
		want string
	}{
		"player":   {map[string]any{"player": 1}, "next day|Ada 6|Ada 5|Ada 4|Ada 3|Ada 2|Ada 1|Ada 0"},
		"name":     {map[string]any{"name": "B"}, "bo SELLS gold 6|bo SELLS gold 5|bo SELLS gold 4|bo SELLS gold 3|bo SELLS gold 2|bo SELLS gold 1|bo SELLS gold 0"},
		"words":    {map[string]any{"q": "GOLD", "since": time.Date(2026, 10, 31, 12, 5, 0, 0, time.UTC).UnixMilli()}, "bo SELLS gold 6|bo SELLS gold 5|bo SELLS gold 4"},
		"kind":     {map[string]any{"kind": "party", "q": "gold"}, "old gold"},
		"until":    {map[string]any{"player": 1, "until": time.Date(2026, 10, 31, 12, 3, 0, 0, time.UTC).UnixMilli()}, "Ada 1|Ada 0"},
		"one day":  {map[string]any{"since": time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixMilli()}, "next day"},
		"refused":  {map[string]any{"undelivered": true}, ""},
		"no match": {map[string]any{"q": "nothing like this"}, ""},
	} {
		_, out := r.query(c.q)
		if got := strings.Join(texts2(out["lines"].([]any)), "|"); got != c.want || out["next"] != "" {
			t.Fatalf("%s: %q next %v, want %q", name, got, out["next"], c.want)
		}
	}
	// Pages of 3 over Ada's lines: every line once, in order, then no cursor.
	var paged []string
	before := ""
	for pages := 0; ; pages++ {
		q := map[string]any{"player": 1, "limit": 3}
		if before != "" {
			q["before"] = before
		}
		_, out := r.query(q)
		paged = append(paged, texts2(out["lines"].([]any))...)
		before = out["next"].(string)
		if before == "" || pages > 5 {
			break
		}
	}
	if strings.Join(paged, "|") != "next day|Ada 6|Ada 5|Ada 4|Ada 3|Ada 2|Ada 1|Ada 0" {
		t.Fatalf("paged: %v", paged)
	}
	if _, out := r.query(map[string]any{"limit": 999}); len(out["lines"].([]any)) != 16 {
		t.Fatal("limit is capped, not refused")
	}
	for _, bad := range []map[string]any{{"kind": "whisper"}, {"before": "yesterday"}, {"player": -1}, {"q": strings.Repeat("x", maxChat+1)}, {"limit": -2}} {
		if code, out := r.query(bad); code != 400 || out["status"] != "bad" {
			t.Fatalf("%v: %d %v", bad, code, out)
		}
	}
}

func TestChatlogCallIsOnlyTheSitesSignedPost(t *testing.T) {
	dir := t.TempDir()
	saved := chatlogDirForTest
	chatlogDirForTest = dir
	h := NewHub(nil)
	chatlogDirForTest = saved
	t.Cleanup(h.Shutdown)
	srv := httptest.NewServer(NewServer(NewTickets(testSecret, nil), h, nil))
	t.Cleanup(srv.Close)
	ws, base := "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", srv.URL
	ada := signIn(t, ws, 1, "Ada")
	ada.send(map[string]any{"t": "join", "street": "GA5HERE"})
	ada.next("street")
	ada.send(map[string]any{"t": "chat", "text": "logged"})
	ada.next("said")

	code, out := siteCall(t, base, "chatlog", map[string]any{}, "-")
	mustStatus(t, code, out, 401, "refused")
	code, out = siteCall(t, base, "chatlog", map[string]any{}, "1700000000.0123456789abcdef.9MY3KNwUctqbzzlpAAxyoTdMJzr9DOkqYm5bW0LQN-Y")
	mustStatus(t, code, out, 401, "refused")
	// A signature for another call is not one for this call.
	raw, _ := json.Marshal(map[string]any{})
	other := signService(testSecret, "POST", serviceRoutePrefix+"online", time.Now().Unix(), "00000000000c4a71", raw)
	code, out = siteCall(t, base, "chatlog", map[string]any{}, other)
	mustStatus(t, code, out, 401, "refused")
	code, out = siteCall(t, base, "chatlog", map[string]any{"player": 1}, "")
	mustStatus(t, code, out, 200, "ok")
	if lines := out["lines"].([]any); len(lines) != 1 || lines[0].(map[string]any)["text"] != "logged" || lines[0].(map[string]any)["where"] != "GA5HERE" {
		t.Fatalf("lines %v", lines)
	}
	// Never a socket message.
	ada.send(map[string]any{"t": "chatlog"})
	if e := ada.next("error"); e["code"] != "bad_frame" {
		t.Fatalf("socket chatlog: %v", e)
	}
	// No log kept: the call says so.
	_, base2 := startTeleportServer(t)
	code, out = siteCall(t, base2, "chatlog", map[string]any{}, "")
	mustStatus(t, code, out, 200, "off")
}

func TestChatlogAFullQueueDropsLinesAndNeverHoldsUpChat(t *testing.T) {
	savedQ, savedHold := chatlogQueue, chatlogHoldForTest
	hold := make(chan struct{})
	chatlogQueue, chatlogHoldForTest = 2, hold
	r := newChatRig(t, oct31)
	chatlogQueue, chatlogHoldForTest = savedQ, savedHold
	ada, bo := r.connect(1, "Ada"), r.connect(2, "Bo")
	r.send(ada, map[string]any{"t": "join", "street": "GA5HERE"})
	r.send(bo, map[string]any{"t": "join", "street": "GA5HERE"})
	drain(bo)
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 40; i++ {
			r.now = r.now.Add(chatEvery)
			r.send(ada, map[string]any{"t": "chat", "text": fmt.Sprintf("line %d", i)})
			if i%10 == 9 {
				drain(ada)
				drain(bo)
			}
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("chat waited on the log's writer")
	}
	c := r.hub.chatlogOf()
	if c.dropped.Load() != 38 {
		t.Fatalf("dropped %d, want 38", c.dropped.Load())
	}
	close(hold)
	got := r.lines("2026-10-31")
	if len(got) != 2 || got[0].Text != "line 0" || got[1].Text != "line 1" {
		t.Fatalf("written %+v", got)
	}
	if _, out := r.query(map[string]any{}); out["dropped"] != 38.0 {
		t.Fatalf("the call reports %v dropped", out["dropped"])
	}
}
