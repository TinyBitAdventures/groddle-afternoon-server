package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// reactRig is a hub with the buddies, channels, party, IM and reactions modules and a manual clock.
type reactRig struct {
	t   *testing.T
	hub *Hub
	rx  *reacts
	im  *ims
	now time.Time
}

func newReactRig(t *testing.T, imFile string) *reactRig {
	t.Helper()
	r := &reactRig{t: t, now: time.Unix(1_700_000_000, 0)}
	savedB, savedC, savedIM, saved := buddySecretForTest, chanSecretForTest, imFileForTest, modules
	buddySecretForTest, chanSecretForTest, imFileForTest = buddyTestSecret, chanTestSecret, imFile
	modules = []moduleFactory{newBuddies, newChannels, newParties,
		func(h *Hub) Module { r.im = newIMs(h).(*ims); return r.im },
		func(h *Hub) Module { r.rx = newReacts(h).(*reacts); return r.rx }}
	r.hub = NewHub(func() time.Time { return r.now })
	buddySecretForTest, chanSecretForTest, imFileForTest, modules = savedB, savedC, savedIM, saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool { return func() bool { return false } }
	return r
}

func (r *reactRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

// online connects a player with a signed buddy list (ign: who they block).
func (r *reactRig) online(id int64, name string, ign ...int64) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.do(cl, map[string]any{"t": "buddies_watch", "token": signBuddy(buddyTestSecret, map[string]any{"k": "buddies", "sub": id, "b": "", "r": "", "i": encodeIDs(ign), "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	drain(cl)
	return cl
}

func (r *reactRig) street(street string, cls ...*Client) {
	for _, cl := range cls {
		r.do(cl, map[string]any{"t": "join", "street": street})
	}
	for _, cl := range cls {
		drain(cl)
	}
}

func (r *reactRig) react(cl *Client, line int64, e string, on ...bool) {
	msg := map[string]any{"t": "react", "line": line, "e": e}
	if len(on) > 0 {
		msg["on"] = on[0]
	}
	r.do(cl, msg)
	r.now = r.now.Add(time.Second) // past the rate
}

// said is the lid of the one "said" (or party_said, channel_said) frame in msgs.
func lidOf(t *testing.T, msgs []map[string]any, kind string) int64 {
	t.Helper()
	m := one(t, msgs, kind)
	lid, ok := m["lid"].(float64)
	if !ok || lid <= 0 {
		t.Fatalf("%s without a lid: %v", kind, m)
	}
	return int64(lid)
}

// reactedText is a reacted / im_reacted frame's reactions as "e:id+id e:id".
func reactedText(m map[string]any) string {
	var parts []string
	for _, x := range m["r"].([]any) {
		r := x.(map[string]any)
		var ids []string
		for _, b := range r["by"].([]any) {
			ids = append(ids, strconv.Itoa(int(b.(map[string]any)["id"].(float64))))
		}
		parts = append(parts, r["e"].(string)+":"+strings.Join(ids, "+"))
	}
	return strings.Join(parts, " ")
}

// lastReacted is the newest reacted frame for line in msgs, as reactedText; "-" for none.
func lastReacted(msgs []map[string]any, kind string, line int64) string {
	out := "-"
	for _, m := range msgs {
		if m["t"] == kind && int64(m["line"].(float64)) == line {
			out = reactedText(m)
		}
	}
	return out
}

func reactErrors(msgs []map[string]any) []string {
	var out []string
	for _, m := range msgs {
		if m["t"] == "error" {
			out = append(out, m["code"].(string)+": "+m["message"].(string))
		}
	}
	return out
}

func TestReactionsLocalLinesCarryAnIDAndToggleForTheStreet(t *testing.T) {
	r := newReactRig(t, "")
	ada, bo, cy := r.online(7, "Ada"), r.online(9, "Bo"), r.online(11, "Cy")
	r.street("GA5", ada, bo)
	r.do(ada, map[string]any{"t": "chat", "text": "hello"})
	lid := lidOf(t, drain(bo), "said")
	if echo := lidOf(t, drain(ada), "said"); echo != lid {
		t.Fatalf("Ada's echo has lid %d, Bo's %d", echo, lid)
	}
	r.do(bo, map[string]any{"t": "chat", "text": "hi"})
	if next := lidOf(t, drain(ada), "said"); next <= lid {
		t.Fatalf("the next line's lid %d is not after %d", next, lid)
	}
	drain(bo)

	// Bo reacts: both have it. Again with on: nothing changes, Bo alone hears the state.
	r.react(bo, lid, "heart", true)
	if a, b := lastReacted(drain(ada), "reacted", lid), lastReacted(drain(bo), "reacted", lid); a != "heart:9" || b != "heart:9" {
		t.Fatalf("after Bo's heart: Ada %q, Bo %q", a, b)
	}
	r.react(bo, lid, "heart", true)
	if a, b := lastReacted(drain(ada), "reacted", lid), lastReacted(drain(bo), "reacted", lid); a != "-" || b != "heart:9" {
		t.Fatalf("a heart twice: Ada %q, Bo %q", a, b)
	}
	// Ada adds a laugh and a heart (her own line), in the set's order; a toggle takes the heart away.
	r.react(ada, lid, "laugh")
	r.react(ada, lid, "heart")
	if a := lastReacted(drain(ada), "reacted", lid); a != "heart:9+7 laugh:7" {
		t.Fatalf("order: %q", a)
	}
	r.react(ada, lid, "heart")
	if b := lastReacted(drain(bo), "reacted", lid); b != "heart:9 laugh:7" {
		t.Fatalf("toggled off: %q", b)
	}
	// The last one off leaves none.
	r.react(bo, lid, "heart", false)
	r.react(ada, lid, "laugh", false)
	drain(ada)
	if b := lastReacted(drain(bo), "reacted", lid); b != "" {
		t.Fatalf("all off: %q", b)
	}

	// Cy comes later: the line is not his to react to, and he heard nothing.
	r.street("GA5", cy)
	r.react(cy, lid, "like")
	if errs := reactErrors(drain(cy)); len(errs) != 1 || !strings.HasPrefix(errs[0], "react: ") {
		t.Fatalf("Cy: %v", errs)
	}
	if a := lastReacted(drain(ada), "reacted", lid); a != "-" {
		t.Fatalf("Cy's reaction reached Ada: %q", a)
	}
	// Bo walks away: the line is still his (it is in his log), and Ada still hears.
	r.street("GB1", bo)
	r.react(bo, lid, "wow")
	if a := lastReacted(drain(ada), "reacted", lid); a != "wow:9" {
		t.Fatalf("after Bo left: %q", a)
	}
}

func TestReactionsOnlyTheSetAndOnlyLinesItKnows(t *testing.T) {
	r := newReactRig(t, "")
	ada, bo := r.online(7, "Ada"), r.online(9, "Bo")
	r.street("GA5", ada, bo)
	r.do(ada, map[string]any{"t": "chat", "text": "hello"})
	lid := lidOf(t, drain(bo), "said")
	drain(ada)
	for _, e := range []string{"", "👍", "LIKE", "thumbsup", "<b>", "like "} {
		r.react(bo, lid, e)
		if errs := reactErrors(drain(bo)); len(errs) != 1 || errs[0] != "react: That is not a reaction the game has." {
			t.Fatalf("%q: %v", e, errs)
		}
	}
	for _, e := range reactionKeys {
		r.react(bo, lid, e)
	}
	if a := lastReacted(drain(ada), "reacted", lid); a != "like:9 heart:9 laugh:9 wow:9 sad:9 tada:9 hi:9" {
		t.Fatalf("the set: %q", a)
	}
	r.react(bo, lid+1000, "like")
	if errs := reactErrors(drain(bo)); len(errs) != 1 || !strings.Contains(errs[0], "too old") {
		t.Fatalf("unknown line: %v", errs)
	}
	// A street keeps reactKeep lines reactable: the oldest goes.
	for i := 0; i < reactKeep; i++ {
		r.do(ada, map[string]any{"t": "chat", "text": "line " + strconv.Itoa(i)})
		r.now = r.now.Add(time.Second)
	}
	drain(ada)
	drain(bo)
	r.react(bo, lid, "sad")
	if errs := reactErrors(drain(bo)); len(errs) != 1 || !strings.Contains(errs[0], "too old") {
		t.Fatalf("a line past the street's %d: %v", reactKeep, errs)
	}
	// So do lines older than reactKeepFor.
	r.do(ada, map[string]any{"t": "chat", "text": "fresh"})
	fresh := lidOf(t, drain(bo), "said")
	r.now = r.now.Add(reactKeepFor + time.Minute)
	r.react(bo, fresh, "sad")
	if errs := reactErrors(drain(bo)); len(errs) != 1 || !strings.Contains(errs[0], "too old") {
		t.Fatalf("an old line: %v", errs)
	}
}

func TestReactionsAreRateLimited(t *testing.T) {
	r := newReactRig(t, "")
	ada, bo := r.online(7, "Ada"), r.online(9, "Bo")
	r.street("GA5", ada, bo)
	r.do(ada, map[string]any{"t": "chat", "text": "hello"})
	lid := lidOf(t, drain(bo), "said")
	drain(ada)
	for i := 0; i < reactBurst; i++ {
		r.do(bo, map[string]any{"t": "react", "line": lid, "e": "like"})
	}
	if errs := reactErrors(drain(bo)); len(errs) != 0 {
		t.Fatalf("within the burst: %v", errs)
	}
	r.do(bo, map[string]any{"t": "react", "line": lid, "e": "like"})
	if errs := reactErrors(drain(bo)); len(errs) != 1 || errs[0] != "slow_down: Slow down a little." {
		t.Fatalf("past the burst: %v", errs)
	}
	before := lastReacted(drain(ada), "reacted", lid)
	r.now = r.now.Add(reactEvery)
	r.do(bo, map[string]any{"t": "react", "line": lid, "e": "like"})
	if errs := reactErrors(drain(bo)); len(errs) != 0 {
		t.Fatalf("after one reactEvery: %v", errs)
	}
	if before == "-" || lastReacted(drain(ada), "reacted", lid) == "-" {
		t.Fatal("the allowed reactions were not heard")
	}
}

func TestReactionsKeepToBlocks(t *testing.T) {
	r := newReactRig(t, "")
	// Bo blocks Ada.
	ada, bo, cy := r.online(7, "Ada"), r.online(9, "Bo", 7), r.online(11, "Cy")
	r.street("GA5", ada, bo, cy)
	r.do(bo, map[string]any{"t": "chat", "text": "bo here"})
	boLine := lidOf(t, drain(bo), "said")
	drain(ada)
	r.do(ada, map[string]any{"t": "chat", "text": "ada here"})
	adaLine := lidOf(t, drain(ada), "said")
	drain(cy)
	r.do(cy, map[string]any{"t": "chat", "text": "cy here"})
	cyLine := lidOf(t, drain(cy), "said")
	drain(ada)
	drain(bo)

	// Neither may react to the other's lines, either way round.
	r.react(ada, boLine, "like")
	r.react(bo, adaLine, "like")
	for _, cl := range []*Client{ada, bo} {
		if errs := reactErrors(drain(cl)); len(errs) != 1 || errs[0] != "react: You can't react to that player's lines." {
			t.Fatalf("%s: %v", cl.Name(), errs)
		}
	}
	// On Cy's line: Bo never sees Ada's reaction; Ada and Cy see Bo's.
	r.react(ada, cyLine, "heart")
	r.react(bo, cyLine, "heart")
	if b := lastReacted(drain(bo), "reacted", cyLine); b != "heart:9" {
		t.Fatalf("Bo sees %q", b)
	}
	if a, c := lastReacted(drain(ada), "reacted", cyLine), lastReacted(drain(cy), "reacted", cyLine); a != "heart:7+9" || c != "heart:7+9" {
		t.Fatalf("Ada %q, Cy %q", a, c)
	}
	// Ada's own line: Bo blocks its speaker, so he is sent nothing about it.
	r.react(cy, adaLine, "laugh")
	if b := lastReacted(drain(bo), "reacted", adaLine); b != "-" {
		t.Fatalf("Bo heard about Ada's line: %q", b)
	}
	if a := lastReacted(drain(ada), "reacted", adaLine); a != "laugh:11" {
		t.Fatalf("Ada: %q", a)
	}
}

func TestReactionsInChannelsGoToItsMembersAndStayWithItsHistory(t *testing.T) {
	r := newReactRig(t, "")
	ada, bo, cy, di, ed := r.online(7, "Ada"), r.online(9, "Bo"), r.online(11, "Cy", 7), r.online(13, "Di"), r.online(15, "Ed")
	tok := func(cl *Client, silence string) {
		r.do(cl, map[string]any{"t": "groups_watch", "token": chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": cl.ID(), "g": "", "c": "global,help", "s": silence, "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	}
	for _, cl := range []*Client{ada, bo, cy, di, ed} {
		tok(cl, "")
	}
	tok(di, "chat") // Di is in the cone of silence for Global and Trade
	for _, cl := range []*Client{ada, bo, di} {
		r.do(cl, map[string]any{"t": "channel_join", "channel": "global"})
		drain(cl)
	}
	r.do(ada, map[string]any{"t": "channel_say", "channel": "global", "text": "hi all"})
	lid := lidOf(t, drain(bo), "channel_said")
	drain(ada)
	drain(di)
	r.react(bo, lid, "tada")
	if a := lastReacted(drain(ada), "reacted", lid); a != "tada:9" {
		t.Fatalf("Ada: %q", a)
	}
	// Di is suspended there: her reaction is refused as her words would be.
	r.react(di, lid, "like")
	if errs := reactErrors(drain(di)); len(errs) != 1 || errs[0] != "react: "+chanSuspended {
		t.Fatalf("Di: %v", errs)
	}
	// Ed has not opened Global: refused.
	r.react(ed, lid, "like")
	if errs := reactErrors(drain(ed)); len(errs) != 1 || errs[0] != "react: "+chanNotIn {
		t.Fatalf("Ed before joining: %v", errs)
	}
	// Bo reacts to his own later line; Cy, who blocks Ada, opens Global: its history carries the
	// reactions he may see (her line is still in it: his game hides it, as the source's did).
	r.do(bo, map[string]any{"t": "channel_say", "channel": "global", "text": "me too"})
	boLine := lidOf(t, drain(bo), "channel_said")
	drain(ada)
	drain(di)
	r.react(ada, boLine, "heart")
	r.react(bo, boLine, "like")
	r.react(ada, lid, "heart")
	r.do(cy, map[string]any{"t": "channel_join", "channel": "global"})
	for _, x := range one(t, drain(cy), "channel_joined")["history"].([]any) {
		l := x.(map[string]any)
		if int64(l["lid"].(float64)) == boLine && reactedText(l) != "like:9" {
			t.Fatalf("Cy sees Ada's reaction on Bo's line: %v", l)
		}
	}
	r.do(cy, map[string]any{"t": "channel_leave", "channel": "global"})
	drain(cy)
	r.do(ed, map[string]any{"t": "channel_join", "channel": "global"})
	drain(ed)
	r.do(ed, map[string]any{"t": "channel_join", "channel": "global"})
	j := one(t, drain(ed), "channel_joined")
	h := j["history"].([]any)
	line := h[len(h)-2].(map[string]any)
	if int64(line["lid"].(float64)) != lid || reactedText(line) != "heart:7 tada:9" {
		t.Fatalf("Ed's history line %v", line)
	}
	// Bo reopens it (a reload): his history shows Ada's heart.
	r.do(bo, map[string]any{"t": "channel_join", "channel": "global"})
	h = one(t, drain(bo), "channel_joined")["history"].([]any)
	if got := reactedText(h[len(h)-2].(map[string]any)); got != "heart:7 tada:9" {
		t.Fatalf("Bo's history: %q", got)
	}
	// A line with none carries no r.
	r.do(bo, map[string]any{"t": "channel_say", "channel": "global", "text": "quiet"})
	drain(bo)
	r.do(ada, map[string]any{"t": "channel_join", "channel": "global"})
	h = one(t, drain(ada), "channel_joined")["history"].([]any)
	if _, has := h[len(h)-1].(map[string]any)["r"]; has {
		t.Fatalf("an r on a line with none: %v", h[len(h)-1])
	}
	// Leaving the channel ends it: no more reactions there, nor news of them.
	r.do(bo, map[string]any{"t": "channel_leave", "channel": "global"})
	drain(bo)
	r.react(bo, lid, "sad")
	if errs := reactErrors(drain(bo)); len(errs) != 1 || errs[0] != "react: "+chanNotIn {
		t.Fatalf("Bo after leaving: %v", errs)
	}
	r.react(ada, lid, "sad")
	if b := lastReacted(drain(bo), "reacted", lid); b != "-" {
		t.Fatalf("Bo heard after leaving: %q", b)
	}
	if e := lastReacted(drain(ed), "reacted", lid); e != "heart:7 sad:7 tada:9" {
		t.Fatalf("Ed: %q", e)
	}
}

func TestReactionsInAPartyStayWithItsMembers(t *testing.T) {
	r := newReactRig(t, "")
	ada, bo, cy := r.online(7, "Ada"), r.online(9, "Bo"), r.online(11, "Cy")
	r.street("GA5", ada, bo, cy)
	r.do(ada, map[string]any{"t": "party_invite", "id": 9})
	drain(bo)
	r.do(bo, map[string]any{"t": "party_accept", "from": 7})
	drain(ada)
	drain(bo)
	r.do(bo, map[string]any{"t": "party_chat", "text": "party on"})
	lid := lidOf(t, drain(ada), "party_said")
	if echo := lidOf(t, drain(bo), "party_said"); echo != lid {
		t.Fatalf("echo %d, Ada's %d", echo, lid)
	}
	r.react(ada, lid, "laugh")
	drain(ada)
	if b := lastReacted(drain(bo), "reacted", lid); b != "laugh:7" {
		t.Fatalf("Bo: %q", b)
	}
	r.react(cy, lid, "laugh")
	if errs := reactErrors(drain(cy)); len(errs) != 1 || !strings.HasPrefix(errs[0], "react: ") {
		t.Fatalf("Cy, not in the party: %v", errs)
	}
	if a := lastReacted(drain(ada), "reacted", lid); a != "-" {
		t.Fatalf("Cy's reaction reached Ada: %q", a)
	}
}

func TestReactionsOnIMsReachBothAndAreKept(t *testing.T) {
	file := filepath.Join(t.TempDir(), "im.json")
	r := newReactRig(t, file)
	ada, bo := r.online(7, "Ada"), r.online(9, "Bo")
	r.do(ada, map[string]any{"t": "im_sync"})
	r.do(bo, map[string]any{"t": "im_sync"})
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "im_send", "to": 9, "text": "psst"})
	got := one(t, drain(bo), "im")
	line := int64(got["line"].(map[string]any)["id"].(float64))
	if _, has := got["line"].(map[string]any)["r"]; has {
		t.Fatalf("a new line with r: %v", got)
	}
	drain(ada)
	r.do(bo, map[string]any{"t": "im_react", "with": 7, "line": line, "e": "heart"})
	a := one(t, drain(ada), "im_reacted")
	b := one(t, drain(bo), "im_reacted")
	if a["with"] != float64(9) || b["with"] != float64(7) || reactedText(a) != "heart:9" || reactedText(b) != "heart:9" {
		t.Fatalf("Ada %v, Bo %v", a, b)
	}
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "im_react", "with": 9, "line": line, "e": "heart"})
	if got := reactedText(one(t, drain(bo), "im_reacted")); got != "heart:9+7" {
		t.Fatalf("both hearts: %q", got)
	}
	drain(ada)
	// A bad reaction, a line that is not there, a conversation that is not yours.
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "im_react", "with": 9, "line": line, "e": "shrug"})
	r.do(ada, map[string]any{"t": "im_react", "with": 9, "line": line + 5, "e": "like"})
	r.do(ada, map[string]any{"t": "im_react", "with": 11, "line": line, "e": "like"})
	if errs := reactErrors(drain(ada)); len(errs) != 3 || !strings.HasPrefix(errs[0], "react: That is not") || !strings.Contains(errs[1], "too old") || !strings.Contains(errs[2], "too old") {
		t.Fatalf("refusals: %v", errs)
	}
	// A reload: im_state carries them.
	r.do(bo, map[string]any{"t": "im_sync"})
	st := one(t, drain(bo), "im_state")
	l0 := st["convs"].([]any)[0].(map[string]any)["lines"].([]any)[0].(map[string]any)
	if reactedText(l0) != "heart:9+7" {
		t.Fatalf("im_state line %v", l0)
	}
	// Kept in the state file with the line.
	if !r.im.save() {
		t.Fatal("save")
	}
	raw, _ := os.ReadFile(file)
	if !strings.Contains(string(raw), `"r":[{"e":"heart","by":[{"id":9,"name":"Bo"},{"id":7,"name":"Ada"}]}]`) {
		t.Fatalf("state file: %s", raw)
	}
	// A block since: refused both ways.
	r.do(bo, map[string]any{"t": "buddies_watch", "token": signBuddy(buddyTestSecret, map[string]any{"k": "buddies", "sub": 9, "b": "", "r": "", "i": encodeIDs([]int64{7}), "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	drain(bo)
	r.now = r.now.Add(time.Second)
	r.do(ada, map[string]any{"t": "im_react", "with": 9, "line": line, "e": "like"})
	r.do(bo, map[string]any{"t": "im_react", "with": 7, "line": line, "e": "like"})
	for _, cl := range []*Client{ada, bo} {
		if errs := reactErrors(drain(cl)); len(errs) != 1 || errs[0] != "react: You can't react to that player's lines." {
			t.Fatalf("%s after the block: %v", cl.Name(), errs)
		}
	}
}

func TestReactionsAreOffWithoutTheModule(t *testing.T) {
	// A server without reactions (the other test rigs) says lines as before: no lid.
	r := newPartyRig(t)
	ada, bo := r.connect(7, "Ada"), r.connect(9, "Bo")
	r.join(ada, "GA5")
	r.join(bo, "GA5")
	drain(ada)
	drain(bo)
	r.do(ada, map[string]any{"t": "chat", "text": "hello"})
	if _, has := one(t, drain(bo), "said")["lid"]; has {
		t.Fatal("a lid without the module")
	}
}

func TestReactionsSetKeepsOrderAndOnePerPlayer(t *testing.T) {
	var rs reactions
	rs, _ = rs.set("hi", 1, "A", true)
	rs, _ = rs.set("like", 2, "B", true)
	rs, _ = rs.set("hi", 2, "B", true)
	if got := reactSummary(rs); got != "like:2 hi:1+2" {
		t.Fatalf("%q", got)
	}
	if _, changed := rs.set("hi", 1, "A", true); changed {
		t.Fatal("twice changed it")
	}
	rs, _ = rs.set("hi", 1, "A", false)
	rs, _ = rs.set("like", 2, "B", false)
	if got := reactSummary(rs); got != "hi:2" {
		t.Fatalf("%q", got)
	}
	ign := func(a, b int64) bool { return a == 5 && b == 2 }
	if got := reactSummary(rs.shownTo(5, ign)); got != "" {
		t.Fatalf("5 blocks 2: %q", got)
	}
	if got := rs.shownTo(2, ign); len(got) != 1 {
		t.Fatalf("2 sees their own: %v", got)
	}
}

func reactSummary(rs []reaction) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		ids := make([]string, 0, len(r.By))
		for _, b := range r.By {
			ids = append(ids, strconv.FormatInt(b.ID, 10))
		}
		parts = append(parts, r.E+":"+strings.Join(ids, "+"))
	}
	return strings.Join(parts, " ")
}
