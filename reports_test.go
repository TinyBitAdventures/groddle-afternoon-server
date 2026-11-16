package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// reportRig is a hub with the chat log, channels, parties, buddies, IMs and reports, and a manual clock.
type reportRig struct {
	t   *testing.T
	hub *Hub
	now time.Time
}

func newReportRig(t *testing.T) *reportRig {
	t.Helper()
	r := &reportRig{t: t, now: oct31}
	savedDir, savedChan, savedBuddy, savedIM, saved := chatlogDirForTest, chanSecretForTest, buddySecretForTest, imFileForTest, modules
	chatlogDirForTest, chanSecretForTest, buddySecretForTest, imFileForTest = t.TempDir(), chanTestSecret, buddyTestSecret, ""
	modules = []moduleFactory{newChatlogModule, newChannels, newParties, newBuddies, newIMs, newReports}
	r.hub = NewHub(func() time.Time { return r.now })
	chatlogDirForTest, chanSecretForTest, buddySecretForTest, imFileForTest, modules = savedDir, savedChan, savedBuddy, savedIM, saved
	t.Cleanup(r.hub.Shutdown)
	return r
}

func (r *reportRig) send(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *reportRig) online(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.send(cl, map[string]any{"t": "buddies_watch", "token": signBuddy(buddyTestSecret, map[string]any{"k": "buddies", "sub": id, "b": "", "r": "", "i": "", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	r.send(cl, map[string]any{"t": "groups_watch", "token": chanSign(chanTestSecret, map[string]any{"k": "groups", "sub": id, "g": "", "c": "global", "s": "", "iat": r.now.Unix(), "exp": r.now.Add(24 * time.Hour).Unix()})})
	drain(cl)
	return cl
}

func (r *reportRig) report(reporter, reported int64, name string) (int, []reportLine, string) {
	r.t.Helper()
	body, _ := json.Marshal(map[string]any{"reporter": reporter, "reported": reported, "name": name})
	code, out := reportLinesService(r.hub, body)
	raw, _ := json.Marshal(out)
	var a struct {
		Status string       `json:"status"`
		Lines  []reportLine `json:"lines"`
	}
	json.Unmarshal(raw, &a)
	return code, a.Lines, string(raw)
}

func reportTexts(lines []reportLine) []string {
	out := []string{}
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

func TestReportCarriesThePublicLinesTheReporterSawAndNeverAnIM(t *testing.T) {
	r := newReportRig(t)
	ada, bo, cy := r.online(1, "Ada"), r.online(2, "Boz"), r.online(3, "Cy")
	r.send(ada, map[string]any{"t": "join", "street": "GA5ONE"})
	r.send(bo, map[string]any{"t": "join", "street": "GA5ONE"})
	r.send(cy, map[string]any{"t": "join", "street": "GA5TWO"})
	r.send(bo, map[string]any{"t": "chat", "text": "too old to carry"})
	r.now = r.now.Add(reportWindow + time.Minute)

	r.send(bo, map[string]any{"t": "chat", "text": "hey Ada"})
	r.send(ada, map[string]any{"t": "chat", "text": "please stop"})
	r.send(cy, map[string]any{"t": "chat", "text": "a street Ada never stood on"})
	// IMs both ways: private, never in the chat log, never in a report.
	r.send(bo, map[string]any{"t": "im_send", "to": 1, "text": "secret words in an IM"})
	r.send(ada, map[string]any{"t": "im_send", "to": 2, "text": "a private reply"})
	if raw, _ := json.Marshal(drain(ada)); !strings.Contains(string(raw), "secret words in an IM") {
		t.Fatalf("the IM did not reach Ada, so the test proves nothing: %s", raw)
	}
	// Global: both in it.
	r.now = r.now.Add(time.Second)
	r.send(ada, map[string]any{"t": "channel_join", "channel": "global"})
	r.send(bo, map[string]any{"t": "channel_join", "channel": "global"})
	r.send(bo, map[string]any{"t": "channel_say", "channel": "global", "text": "global rudeness"})
	// A party line that went to Ada.
	r.send(ada, map[string]any{"t": "party_invite", "id": 2})
	r.send(bo, map[string]any{"t": "party_accept", "from": 1})
	r.send(bo, map[string]any{"t": "party_chat", "text": "party words"})
	// Ada walks on; what Bo says on GA5ONE after that, she did not hear.
	r.now = r.now.Add(time.Second)
	r.send(ada, map[string]any{"t": "join", "street": "GA5TWO"})
	r.send(bo, map[string]any{"t": "chat", "text": "said after she left"})
	r.send(cy, map[string]any{"t": "chat", "text": "Boz was rude to me too"})

	code, lines, raw := r.report(1, 2, "Boz")
	if code != 200 {
		t.Fatalf("code %d %s", code, raw)
	}
	got := strings.Join(reportTexts(lines), "|")
	for _, want := range []string{"hey Ada", "please stop", "global rudeness", "party words", "Boz was rude to me too"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
	for _, never := range []string{"secret words", "a private reply", "too old", "never stood on", "after she left"} {
		if strings.Contains(raw, never) {
			t.Fatalf("%q reached a report: %s", never, raw)
		}
	}
	for _, l := range lines {
		if l.Reported != (l.ID == 2) {
			t.Fatalf("reported mark on %+v", l)
		}
		if l.Kind != "local" && l.Kind != "channel" && l.Kind != "party" {
			t.Fatalf("kind %q", l.Kind)
		}
	}
	if lines[0].Text != "hey Ada" {
		t.Fatalf("oldest first: %v", reportTexts(lines))
	}
	// The same call from someone who saw none of it: nothing of Bo's on GA5ONE or the party.
	_, cyLines, _ := r.report(3, 2, "Boz")
	if g := strings.Join(reportTexts(cyLines), "|"); strings.Contains(g, "hey Ada") || strings.Contains(g, "party words") || strings.Contains(g, "global rudeness") {
		t.Fatalf("Cy's report carries what Cy could not see: %s", g)
	}
}

func TestReportSightNeverSeesAnIMOrAGroupOrARefusedLine(t *testing.T) {
	at := oct31
	s := reportSight{reporter: 1, streets: []reportVisit{{street: "GA5ONE", from: at.Add(-time.Hour)}}, rooms: map[string]bool{"global": true, "group:5": true}}
	for _, l := range []chatLine{
		{Kind: "im", Where: "GA5ONE", Delivered: true, To: []int64{1}},
		{Kind: "channel", Where: "group:5", Delivered: true},
		{Kind: "local", Where: "GA5ONE", Delivered: false, Why: "slow_down"},
		{Kind: "party", Where: "party-1", Delivered: true, To: []int64{2, 3}},
		{Kind: "", Where: "GA5ONE", Delivered: true},
	} {
		if s.sees(l, at) {
			t.Fatalf("sees %+v", l)
		}
	}
	if !s.sees(chatLine{Kind: "local", Where: "GA5ONE", Delivered: true}, at) || !s.sees(chatLine{Kind: "channel", Where: "global", Delivered: true}, at) {
		t.Fatal("misses a line it saw")
	}
	// The server's IM path stays out of reports entirely: the file names no IM.
	src, err := os.ReadFile("reports.go")
	if err != nil {
		t.Fatal(err)
	}
	code := string(src)
	if i := strings.Index(code, "\nimport ("); i > 0 {
		code = code[i:] // the header comment explains the rule in words
	}
	for _, bad := range []string{"*ims", "im_send", "imLine", "imFile", `"ims.`, "chatstore"} {
		if strings.Contains(code, bad) {
			t.Fatalf("reports.go reaches the IM path: %q", bad)
		}
	}
}

func TestPickReportLinesKeepsTheirLinesWithContextAndACap(t *testing.T) {
	var lines []chatLine
	for i := 0; i < 30; i++ {
		lines = append(lines, chatLine{Kind: "local", Where: "A", ID: 1, Text: fmt.Sprintf("a%d", i)})
	}
	lines[10].ID, lines[10].Text = 2, "theirs"
	lines = append(lines, chatLine{Kind: "channel", Where: "global", ID: 3, Text: "elsewhere"})
	got := pickReportLines(lines, 2, "Boz")
	want := []string{"a7", "a8", "a9", "theirs", "a11", "a12", "a13"}
	if fmt.Sprint(lineTexts(got)) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", lineTexts(got), want)
	}
	// A line naming them is an anchor too (Glitch's test: chats that have the name in them).
	lines[25].Text = "boz keeps following me"
	if g := fmt.Sprint(lineTexts(pickReportLines(lines, 2, "Boz"))); !strings.Contains(g, "a24 boz keeps following me a26") || strings.Contains(g, "elsewhere") {
		t.Fatalf("mention: %v", g)
	}
	// Nothing of theirs: the newest reportQuietLines.
	quiet := pickReportLines(lines[:10], 2, "Zed")
	if len(quiet) != 10 || pickReportLines(lines, 9, "Zed")[reportQuietLines-1].Text != "elsewhere" {
		t.Fatalf("quiet: %v", lineTexts(quiet))
	}
	// At most reportMaxLines, their lines kept before context, the newest kept.
	var many []chatLine
	for i := 0; i < 200; i++ {
		id := int64(1)
		if i%2 == 0 {
			id = 2
		}
		many = append(many, chatLine{Kind: "local", Where: "A", ID: id, Text: fmt.Sprintf("m%d", i)})
	}
	capped := pickReportLines(many, 2, "")
	if len(capped) != reportMaxLines || capped[len(capped)-1].Text != "m198" {
		t.Fatalf("cap: %d %v", len(capped), lineTexts(capped))
	}
	for _, l := range capped {
		if l.ID != 2 {
			t.Fatalf("context kept over their lines: %+v", l)
		}
	}
}

func lineTexts(lines []chatLine) []string {
	out := []string{}
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

func TestReportLinesCallRefusesABadBodyAndAnswersOffWithoutALog(t *testing.T) {
	saved := modules
	modules = []moduleFactory{newReports}
	h := NewHub(time.Now)
	modules = saved
	t.Cleanup(h.Shutdown)
	for _, body := range []string{`{}`, `{"reporter":1,"reported":1}`, `{"reporter":-1,"reported":2}`, `not json`} {
		if code, _ := reportLinesService(h, []byte(body)); code != 400 {
			t.Fatalf("%s: %d", body, code)
		}
	}
	code, out := reportLinesService(h, []byte(`{"reporter":1,"reported":2,"name":"Boz"}`))
	raw, _ := json.Marshal(out)
	if code != 200 || string(raw) != `{"lines":[],"status":"off","window":1800}` {
		t.Fatalf("%d %s", code, raw)
	}
}
