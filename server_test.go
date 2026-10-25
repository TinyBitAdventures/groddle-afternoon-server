package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef-test")

// signTicket makes a ticket the way RealtimeTicket::sign does.
func signTicket(t *testing.T, c Claims, secret []byte) string {
	t.Helper()
	raw, _ := json.Marshal(c)
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

var jti int

func ticketFor(t *testing.T, id int64, name string) string {
	jti++
	now := time.Now().Unix()
	return signTicket(t, Claims{Sub: id, Name: name, Iat: now, Exp: now + 60, Jti: fmt.Sprintf("j%d", jti)}, testSecret)
}

type peer struct {
	t    *testing.T
	conn *websocket.Conn
}

func startServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(NewServer(NewTickets(testSecret, nil), NewHub(nil), nil))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

func dial(t *testing.T, url string) *peer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return &peer{t: t, conn: conn}
}

func (p *peer) send(v any) {
	p.t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.conn.Write(ctx, websocket.MessageText, b); err != nil {
		p.t.Fatal(err)
	}
}

// next reads frames until one of kind t arrives.
func (p *peer) next(kind string) map[string]any {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, b, err := p.conn.Read(ctx)
		if err != nil {
			p.t.Fatalf("waiting for %q: %v", kind, err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		if m["t"] == kind {
			return m
		}
	}
}

func (p *peer) closeCode() websocket.StatusCode {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		if _, _, err := p.conn.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func signIn(t *testing.T, url string, id int64, name string) *peer {
	p := dial(t, url)
	p.send(map[string]any{"t": "auth", "ticket": ticketFor(t, id, name)})
	hello := p.next("hello")
	if you := hello["you"].(map[string]any); you["name"] != name || you["id"].(float64) != float64(id) {
		t.Fatalf("hello named %v", you)
	}
	return p
}

func TestTwoPlayersSeeAndHearEachOther(t *testing.T) {
	url := startServer(t)
	ada := signIn(t, url, 1, "Ada")
	ada.send(map[string]any{"t": "join", "street": "GA5TEST", "x": 100, "y": 50, "dir": 1, "look": map[string]any{"skin": "a"}})
	if got := ada.next("street")["players"].([]any); len(got) != 0 {
		t.Fatalf("an empty street listed %v", got)
	}
	bo := signIn(t, url, 2, "Bo")
	bo.send(map[string]any{"t": "join", "street": "GA5TEST", "x": 300, "y": 50, "dir": -1})
	there := bo.next("street")["players"].([]any)
	if len(there) != 1 || there[0].(map[string]any)["name"] != "Ada" || there[0].(map[string]any)["look"] == nil {
		t.Fatalf("Bo saw %v", there)
	}
	if j := ada.next("joined")["player"].(map[string]any); j["name"] != "Bo" || j["dir"].(float64) != -1 {
		t.Fatalf("Ada saw %v join", j)
	}

	bo.send(map[string]any{"t": "move", "x": 320.04, "y": 40, "dir": 1, "anim": "walk"})
	if m := ada.next("moved"); m["x"].(float64) != 320 || m["anim"] != "walk" {
		t.Fatalf("move %v", m)
	}

	ada.send(map[string]any{"t": "chat", "text": "  hello\n\tthere  "})
	if s := bo.next("said"); s["text"] != "hello there" || s["name"] != "Ada" {
		t.Fatalf("chat %v", s)
	}
	if s := ada.next("said"); s["text"] != "hello there" {
		t.Fatalf("the speaker hears their own line back, got %v", s)
	}

	ada.send(map[string]any{"t": "emote", "e": "wave"})
	if e := bo.next("emoted"); e["e"] != "wave" {
		t.Fatalf("emote %v", e)
	}

	bo.send(map[string]any{"t": "join", "street": "GA5OTHER"})
	if l := ada.next("left"); l["id"].(float64) != 2 {
		t.Fatalf("left %v", l)
	}
}

func TestStreetsAreSeparate(t *testing.T) {
	url := startServer(t)
	ada := signIn(t, url, 1, "Ada")
	ada.send(map[string]any{"t": "join", "street": "ONE"})
	ada.next("street")
	bo := signIn(t, url, 2, "Bo")
	bo.send(map[string]any{"t": "join", "street": "TWO"})
	bo.next("street")
	bo.send(map[string]any{"t": "chat", "text": "anyone?"})
	bo.next("said")
	ada.send(map[string]any{"t": "ping"})
	// Ada's next frame is the pong, never Bo's line from another street.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, b, err := ada.conn.Read(ctx)
	if err != nil || !strings.Contains(string(b), `"pong"`) {
		t.Fatalf("Ada heard %s (%v)", b, err)
	}
}

func TestSecondSocketReplacesTheFirst(t *testing.T) {
	url := startServer(t)
	first := signIn(t, url, 1, "Ada")
	signIn(t, url, 1, "Ada")
	if code := first.closeCode(); code != closeReplaced {
		t.Fatalf("first socket closed with %d", code)
	}
}

func TestBadTicketsAreRefused(t *testing.T) {
	url := startServer(t)
	now := time.Now().Unix()
	cases := map[string]string{
		"garbage":  "not-a-ticket",
		"forged":   signTicket(t, Claims{Sub: 1, Name: "Ada", Exp: now + 60, Jti: "f"}, []byte("another secret entirely, 32+ bytes")),
		"expired":  signTicket(t, Claims{Sub: 1, Name: "Ada", Exp: now - 1, Jti: "e"}, testSecret),
		"no-user":  signTicket(t, Claims{Name: "Ada", Exp: now + 60, Jti: "n"}, testSecret),
		"tampered": strings.Replace(ticketFor(t, 1, "Ada"), "e", "f", 1),
	}
	for name, ticket := range cases {
		p := dial(t, url)
		p.send(map[string]any{"t": "auth", "ticket": ticket})
		if code := p.closeCode(); code != closeTicket {
			t.Errorf("%s: closed with %d", name, code)
		}
	}
	// The first frame must be the ticket.
	p := dial(t, url)
	p.send(map[string]any{"t": "join", "street": "ONE"})
	if code := p.closeCode(); code != closeTicket {
		t.Errorf("join before auth: closed with %d", code)
	}
}

func TestATicketWorksOnce(t *testing.T) {
	url := startServer(t)
	ticket := ticketFor(t, 1, "Ada")
	p := dial(t, url)
	p.send(map[string]any{"t": "auth", "ticket": ticket})
	p.next("hello")
	again := dial(t, url)
	again.send(map[string]any{"t": "auth", "ticket": ticket})
	if code := again.closeCode(); code != closeTicket {
		t.Fatalf("a reused ticket closed with %d", code)
	}
}

func TestChatIsRateLimitedAndCleaned(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	hub := NewHub(func() time.Time { return now })
	cl := hub.Connect(Claims{Sub: 1, Name: "Ada"})
	<-cl.send // hello
	hub.Handle(cl, []byte(`{"t":"join","street":"ONE"}`))
	<-cl.send // street
	said := 0
	for i := 0; i < chatBurst+3; i++ {
		hub.Handle(cl, []byte(`{"t":"chat","text":"hi"}`))
		if strings.Contains(string(<-cl.send), `"said"`) {
			said++
		}
	}
	if said != chatBurst {
		t.Fatalf("%d lines got through a burst of %d", said, chatBurst)
	}
	now = now.Add(2 * time.Second)
	hub.Handle(cl, []byte(`{"t":"chat","text":"`+strings.Repeat("é", 300)+"\\u0007\\u2028"+`"}`))
	var m map[string]any
	json.Unmarshal(<-cl.send, &m)
	if r := []rune(m["text"].(string)); len(r) != maxChat {
		t.Fatalf("a long line kept %d characters", len(r))
	}
	if cleanName("  \u0000  ") != "Glitchen" {
		t.Fatal("an empty name has a stand-in")
	}
}

func TestBadValuesAreTamed(t *testing.T) {
	hub := NewHub(nil)
	cl := hub.Connect(Claims{Sub: 1, Name: "Ada"})
	<-cl.send
	hub.Handle(cl, []byte(`{"t":"join","street":"../etc"}`))
	if !strings.Contains(string(<-cl.send), "bad_street") {
		t.Fatal("a street name with slashes is refused")
	}
	hub.Handle(cl, []byte(`{"t":"join","street":"ONE","x":1e300,"anim":"<b>","look":"`+strings.Repeat("x", maxLook)+`"}`))
	<-cl.send
	if p := cl.player; p.X != 1e6 || p.Anim != "idle" || p.Look != nil {
		t.Fatalf("player kept %+v", p)
	}
}
