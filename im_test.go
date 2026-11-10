package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// imRig is a hub with the buddies, IM and chat-log modules, a manual clock and manual timers.
type imRig struct {
	t      *testing.T
	hub    *Hub
	m      *ims
	now    time.Time
	timers []*rigTimer
	logDir string
}

func newIMRig(t *testing.T, file string) *imRig {
	t.Helper()
	r := &imRig{t: t, now: time.Unix(1_700_000_000, 0), logDir: t.TempDir()}
	savedSecret, savedFile, savedDir, saved := buddySecretForTest, imFileForTest, chatlogDirForTest, modules
	buddySecretForTest, imFileForTest, chatlogDirForTest = buddyTestSecret, file, r.logDir
	modules = []moduleFactory{newBuddies, func(h *Hub) Module { r.m = newIMs(h).(*ims); return r.m }, newChatlogModule}
	r.hub = NewHub(func() time.Time { return r.now })
	buddySecretForTest, imFileForTest, chatlogDirForTest, modules = savedSecret, savedFile, savedDir, saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	t.Cleanup(r.hub.Shutdown)
	return r
}

func (r *imRig) fire(d time.Duration) {
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if tm.d == d && !tm.stopped {
			tm.stopped = true
			tm.fn()
		}
	}
}

func (r *imRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *imRig) watchToken(sub int64, bud, rev, ign []int64) string {
	return signBuddy(buddyTestSecret, map[string]any{"k": "buddies", "sub": sub, "b": encodeIDs(bud), "r": encodeIDs(rev), "i": encodeIDs(ign), "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})
}

// online connects a player and sends their signed list; the frames that brought are drained.
func (r *imRig) online(id int64, name string, bud, rev, ign []int64) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.do(cl, map[string]any{"t": "buddies_watch", "token": r.watchToken(id, bud, rev, ign)})
	drain(cl)
	return cl
}

func (r *imRig) im(cl *Client, to int64, text string) {
	r.do(cl, map[string]any{"t": "im_send", "to": to, "text": text})
}

// errCode is the refusal's reason ("" for none).
func errCode(frames []map[string]any) string {
	for _, f := range frames {
		if f["t"] == "im_no" {
			return f["reason"].(string)
		}
	}
	return ""
}

func TestIMGoesLiveAndBackToTheSender(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", nil, nil, nil)
	bo := r.online(9, "Bo", nil, nil, nil)
	r.im(ada, 9, "  hi   <b>Bo</b> ")
	got := one(t, drain(bo), "im")
	line := got["line"].(map[string]any)
	with := got["with"].(map[string]any)
	if line["text"] != "hi Bo" || line["from"] != float64(7) || with["id"] != float64(7) || with["name"] != "Ada" || got["unread"] != float64(1) {
		t.Fatalf("Bo got %v", got)
	}
	echo := one(t, drain(ada), "im")
	if echo["line"].(map[string]any)["text"] != "hi Bo" || echo["with"].(map[string]any)["id"] != float64(9) || echo["with"].(map[string]any)["online"] != true {
		t.Fatalf("Ada's copy %v", echo)
	}
}

func TestIMTheSenderIsTheSocketNeverAField(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", nil, nil, nil)
	bo := r.online(9, "Bo", nil, nil, nil)
	r.online(11, "Cy", nil, nil, nil)
	r.do(ada, map[string]any{"t": "im_send", "to": 9, "from": 11, "name": "Cy", "id": 11, "text": "it's Cy"})
	got := one(t, drain(bo), "im")
	if got["line"].(map[string]any)["from"] != float64(7) || got["with"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("spoofed: %v", got)
	}
	if r.m.convs[pairOf(9, 11)] != nil {
		t.Fatal("a conversation was made for Cy")
	}
}

func TestIMNobodyCanIMThemselvesOrAnIDMadeUp(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", nil, nil, nil)
	r.im(ada, 7, "me")
	if c := errCode(drain(ada)); c != "self" {
		t.Fatalf("self: %q", c)
	}
	r.im(ada, 424242, "anyone?")
	if c := errCode(drain(ada)); c != "offline" {
		t.Fatalf("made-up id: %q", c)
	}
	if len(r.m.convs) != 0 {
		t.Fatal("kept something")
	}
}

func TestIMBlocksEitherWayRefuse(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", nil, nil, []int64{9}) // Ada blocks Bo
	bo := r.online(9, "Bo", nil, nil, nil)
	r.im(bo, 7, "hello?")
	if c := errCode(drain(bo)); c != "ignoring you" {
		t.Fatalf("Bo to Ada: %q", c)
	}
	r.im(ada, 9, "go away")
	if c := errCode(drain(ada)); c != "on your ignore list" {
		t.Fatalf("Ada to Bo: %q", c)
	}
	if len(drain(ada))+len(drain(bo)) != 0 || len(r.m.convs) != 0 {
		t.Fatal("something went through")
	}
}

func TestIMOfflineBuddyGetsItWhenBack(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", []int64{9}, nil, nil)
	r.im(ada, 9, "see you later")
	echo := one(t, drain(ada), "im")
	if echo["with"].(map[string]any)["online"] != false {
		t.Fatalf("Bo is offline: %v", echo)
	}
	// A player who is neither a buddy nor in a conversation is refused while offline.
	r.im(ada, 11, "hi Cy")
	if c := errCode(drain(ada)); c != "offline" {
		t.Fatalf("Cy: %q", c)
	}
	// Bo connects: nothing until the list is in, then the conversation.
	bo := r.hub.Connect(Claims{Sub: 9, Name: "Bo"})
	r.do(bo, map[string]any{"t": "im_sync"})
	if got := kinds(drain(bo), "im_state"); len(got) != 0 {
		t.Fatalf("sent before the list: %v", got)
	}
	r.do(bo, map[string]any{"t": "buddies_watch", "token": r.watchToken(9, nil, []int64{7}, nil)})
	st := one(t, drain(bo), "im_state")
	convs := st["convs"].([]any)
	if len(convs) != 1 {
		t.Fatalf("convs %v", convs)
	}
	c := convs[0].(map[string]any)
	if c["unread"] != float64(1) || c["with"].(map[string]any)["name"] != "Ada" || c["with"].(map[string]any)["online"] != true {
		t.Fatalf("conv %v", c)
	}
	if c["lines"].([]any)[0].(map[string]any)["text"] != "see you later" {
		t.Fatalf("lines %v", c["lines"])
	}
	// Bo answers and reads; a reload brings the same history with nothing unread, Bo's name now known.
	r.im(bo, 7, "back now")
	r.do(bo, map[string]any{"t": "im_read", "with": 7})
	if c := r.m.convs[pairOf(7, 9)]; c.unread(9) != 0 || c.Names != [2]string{"Ada", "Bo"} {
		t.Fatalf("after reading %+v", c)
	}
	r.hub.Disconnect(bo)
	bo = r.hub.Connect(Claims{Sub: 9, Name: "Bo"})
	r.do(bo, map[string]any{"t": "buddies_watch", "token": r.watchToken(9, nil, []int64{7}, nil)})
	again := one(t, drain(bo), "im_state")["convs"].([]any)[0].(map[string]any)
	if again["unread"] != float64(0) || len(again["lines"].([]any)) != 2 {
		t.Fatalf("after a reload %v", again)
	}
	// A second watch on the same socket (the list refreshed) sends nothing new.
	r.do(bo, map[string]any{"t": "buddies_watch", "token": r.watchToken(9, nil, []int64{7}, nil)})
	if got := kinds(drain(bo), "im_state"); len(got) != 0 {
		t.Fatalf("state again: %v", got)
	}
}

func TestIMALineKeptForSomeoneWhoBlocksTheSenderIsNeverShown(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", []int64{9}, nil, nil)
	r.im(ada, 9, "are you there")
	drain(ada)
	bo := r.hub.Connect(Claims{Sub: 9, Name: "Bo"})
	r.do(bo, map[string]any{"t": "buddies_watch", "token": r.watchToken(9, nil, nil, []int64{7})})
	st := one(t, drain(bo), "im_state")
	if n := len(st["convs"].([]any)); n != 0 {
		t.Fatalf("Bo blocks Ada but sees %v", st)
	}
}

func TestIMOnlineButNotYetWatchingWaitsForTheList(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", nil, nil, nil)
	bo := r.hub.Connect(Claims{Sub: 9, Name: "Bo"}) // no list yet
	drain(bo)
	r.im(ada, 9, "quick")
	if got := kinds(drain(bo), "im"); len(got) != 0 {
		t.Fatalf("sent before Bo's blocks were known: %v", got)
	}
	r.do(bo, map[string]any{"t": "buddies_watch", "token": r.watchToken(9, nil, nil, nil)})
	st := one(t, drain(bo), "im_state")
	if len(st["convs"].([]any)) != 1 {
		t.Fatalf("state %v", st)
	}
	// From now on lines go live.
	r.im(ada, 9, "again")
	one(t, drain(bo), "im")
}

func TestIMTheWordFilterAndTheLengthCap(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", nil, nil, nil)
	bo := r.online(9, "Bo", nil, nil, nil)
	r.im(ada, 9, "oh fuck")
	if got := one(t, drain(bo), "im")["line"].(map[string]any)["text"]; got != "oh #!@$" {
		t.Fatalf("filtered %q", got)
	}
	if c := r.m.convs[pairOf(7, 9)]; strings.Contains(fmt.Sprint(c.Lines), "fuck") {
		t.Fatal("the original words were kept")
	}
	r.im(ada, 9, strings.Repeat("a", maxChat+300))
	if got := one(t, drain(bo), "im")["line"].(map[string]any)["text"].(string); len([]rune(got)) != maxChat {
		t.Fatalf("length %d", len(got))
	}
	r.im(ada, 9, "<script>alert(1)</script> hello")
	if got := one(t, drain(bo), "im")["line"].(map[string]any)["text"]; got != "alert(1) hello" {
		t.Fatalf("markup %q", got)
	}
	r.im(ada, 9, "<b></b>")
	if got := drain(bo); len(got) != 0 {
		t.Fatalf("an empty line went: %v", got)
	}
}

func TestIMRateLimits(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", nil, nil, nil)
	bo := r.online(9, "Bo", nil, nil, nil)
	for i := 0; i < imBurst; i++ {
		r.im(ada, 9, fmt.Sprint("line ", i))
	}
	r.im(ada, 9, "one too many")
	if c := errCode(drain(ada)); c != "slow_down" {
		t.Fatalf("burst: %q", c)
	}
	if n := len(kinds(drain(bo), "im")); n != imBurst {
		t.Fatalf("Bo got %d", n)
	}
	r.now = r.now.Add(imEvery)
	r.im(ada, 9, "after a second")
	one(t, drain(bo), "im")
	// New conversations have their own rate.
	for i := int64(0); i < imNewBurst+1; i++ {
		r.online(100+i, fmt.Sprint("P", i), nil, nil, nil)
	}
	made := 0
	for i := int64(0); i < imNewBurst+1; i++ {
		r.now = r.now.Add(imEvery)
		r.im(ada, 100+i, "hi")
		if errCode(drain(ada)) == "" {
			made++
		}
	}
	if made != imNewBurst-1 { // the one with Bo was the first
		t.Fatalf("made %d new conversations", made)
	}
}

func TestIMOfflineQueueAndHistoryAreBounded(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", []int64{9}, nil, nil)
	sent := 0
	for i := 0; i < imUnreadMax+3; i++ {
		r.now = r.now.Add(imEvery)
		r.im(ada, 9, fmt.Sprint("hello ", i))
		if errCode(drain(ada)) == "" {
			sent++
		}
	}
	if sent != imUnreadMax {
		t.Fatalf("kept %d unread for Bo", sent)
	}
	// Bo comes back, reads; the history keeps at most imKeep lines.
	bo := r.online(9, "Bo", nil, []int64{7}, nil)
	r.do(bo, map[string]any{"t": "im_read", "with": 7})
	for i := 0; i < imKeep+10; i++ {
		r.now = r.now.Add(imEvery)
		r.im(bo, 7, fmt.Sprint("reply ", i))
		r.do(ada, map[string]any{"t": "im_read", "with": 9})
	}
	c := r.m.convs[pairOf(7, 9)]
	if len(c.Lines) != imKeep || c.Lines[len(c.Lines)-1].Text != fmt.Sprint("reply ", imKeep+9) {
		t.Fatalf("history %d lines", len(c.Lines))
	}
	// Lines older than imKeepFor are forgotten, and the conversation with them.
	r.now = r.now.Add(imKeepFor + time.Minute)
	r.do(bo, map[string]any{"t": "im_sync"})
	st := one(t, drain(bo), "im_state")
	if len(st["convs"].([]any)) != 0 || len(r.m.convs) != 0 {
		t.Fatalf("old lines kept: %v", st)
	}
}

func TestIMConversationsPerPlayerAreCapped(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", nil, nil, nil)
	for i := int64(0); i < imConvsPer+5; i++ {
		r.now = r.now.Add(imNewEvery)
		p := r.online(1000+i, fmt.Sprint("P", i), nil, nil, nil)
		r.im(p, 7, "hi Ada")
		drain(p)
		drain(ada)
	}
	if n := len(r.m.by[7]); n != imConvsPer {
		t.Fatalf("Ada has %d conversations", n)
	}
	if r.m.convs[pairOf(7, 1000)] != nil || r.m.convs[pairOf(7, 1000+imConvsPer+4)] == nil {
		t.Fatal("the oldest should go first")
	}
}

func TestIMCloseHidesUntilTheNextLine(t *testing.T) {
	r := newIMRig(t, "")
	ada := r.online(7, "Ada", nil, nil, nil)
	bo := r.online(9, "Bo", nil, nil, nil)
	r.im(ada, 9, "hi")
	drain(bo)
	r.do(bo, map[string]any{"t": "im_close", "with": 7})
	r.do(bo, map[string]any{"t": "im_sync"})
	if n := len(one(t, drain(bo), "im_state")["convs"].([]any)); n != 0 {
		t.Fatalf("closed but listed")
	}
	r.now = r.now.Add(10 * time.Second)
	r.im(ada, 9, "still there?")
	one(t, drain(bo), "im")
	r.do(bo, map[string]any{"t": "im_sync"})
	if n := len(one(t, drain(bo), "im_state")["convs"].([]any)); n != 1 {
		t.Fatalf("a new line should bring it back")
	}
	r.do(bo, map[string]any{"t": "im_close", "with": 7})
	r.do(ada, map[string]any{"t": "im_close", "with": 9})
	if len(r.m.convs) != 0 {
		t.Fatal("closed on both sides but kept")
	}
}

func TestIMsSurviveARestartAndStayOutOfTheLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "realtime-im.json")
	r := newIMRig(t, path)
	ada := r.online(7, "Ada", []int64{9}, nil, nil)
	r.im(ada, 9, "secret plans for the zz party")
	r.fire(imSaveAfter)
	// Local chat is logged; the IM is not.
	r.do(ada, map[string]any{"t": "join", "street": "GA5101HF7F429V5"})
	r.do(ada, map[string]any{"t": "chat", "text": "hello street"})
	r.hub.chatlogOf().flush()
	day := r.now.UTC().Format("2006-01-02")
	logged, _ := os.ReadFile(filepath.Join(r.logDir, day+".jsonl"))
	if !strings.Contains(string(logged), "hello street") || strings.Contains(string(logged), "secret plans") {
		t.Fatalf("the chat log reads %s", logged)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file %v %v", info, err)
	}
	r2 := newIMRig(t, path)
	r2.now = r.now
	bo := r2.online(9, "Bo", nil, []int64{7}, nil)
	_ = bo
	c := r2.m.convs[pairOf(7, 9)]
	if c == nil || len(c.Lines) != 1 || c.Lines[0].Text != "secret plans for the zz party" || c.unread(9) != 1 {
		t.Fatalf("after a restart %+v", c)
	}
}

func TestIMTheHeaderListsTheIMKinds(t *testing.T) {
	src, err := os.ReadFile("im.go")
	if err != nil {
		t.Skip()
	}
	for _, kind := range []string{`"t":"im_send"`, `"t":"im_sync"`, `"t":"im_read"`, `"t":"im_close"`, `"t":"im"`, `"t":"im_state"`, `"t":"im_no"`} {
		if !strings.Contains(string(src), kind) {
			t.Fatalf("the header does not list %s", kind)
		}
	}
}
