package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Alternate characters: one login, up to three characters, one playing at a time (hub.go
// closeOtherCharacter); a ban on the login covers each of them (admin.go).

// altTicket is a ticket for character id of login acct (0: none named), on via ("" the game, "web" /chat/).
func altTicket(t *testing.T, id, acct int64, name, via string) string {
	jti++
	now := time.Now().Unix()
	return signTicket(t, Claims{Sub: id, Acct: acct, Name: name, Iat: now, Exp: now + 60, Jti: fmt.Sprintf("alt%d", jti), Via: via}, testSecret)
}

func signInAs(t *testing.T, url string, id, acct int64, name, via string) *peer {
	t.Helper()
	p := dial(t, url)
	p.send(map[string]any{"t": "auth", "ticket": altTicket(t, id, acct, name, via)})
	if you := p.next("hello")["you"].(map[string]any); you["id"].(float64) != float64(id) || you["acct"] != nil {
		t.Fatalf("hello named %v", you)
	}
	return p
}

func TestAnotherCharacterOfTheLoginClosesTheFirst(t *testing.T) {
	buf := captureLog(t)
	url := startServer(t)
	ada := signInAs(t, url, 1, 0, "Ada", "") // the login's own character: no acct
	ada.send(map[string]any{"t": "join", "street": "GA5TEST"})
	ada.next("street")
	cy := signInAs(t, url, 3, 0, "Cy", "") // another login, on the same street
	cy.send(map[string]any{"t": "join", "street": "GA5TEST"})
	cy.next("street")

	bea := signInAs(t, url, 2, 1, "Bea", "") // Ada's login's second character
	if code := ada.closeCode(); code != closeOtherCharacter {
		t.Fatalf("Ada's socket closed with %d, want %d", code, closeOtherCharacter)
	}
	if l := cy.next("left"); l["id"].(float64) != 1 {
		t.Fatalf("Cy saw %v", l)
	}
	if line := waitLog(t, buf, "realtime: close player=1 ", "code=4012", `reason="Playing as another character"`); strings.Contains(line, "acct") {
		t.Fatalf("the close line names the login: %s", line)
	}
	// Cy (another login) is unaffected, and Bea plays.
	pong(t, cy)
	pong(t, bea)
	bea.send(map[string]any{"t": "join", "street": "GA5TEST"})
	if got := bea.next("street")["players"].([]any); len(got) != 1 || got[0].(map[string]any)["name"] != "Cy" {
		t.Fatalf("Bea saw %v", got)
	}

	// And back again: Ada closes Bea the same way.
	signInAs(t, url, 1, 1, "Ada", "")
	if code := bea.closeCode(); code != closeOtherCharacter {
		t.Fatalf("Bea's socket closed with %d", code)
	}
	pong(t, cy)
}

func TestTwoLoginsPlayTogether(t *testing.T) {
	url := startServer(t)
	ada := signInAs(t, url, 1, 1, "Ada", "")
	bo := signInAs(t, url, 2, 0, "Bo", "")
	cy := signInAs(t, url, 3, 2, "Cy", "") // Bo's login's second character, after Bo: Bo goes
	if code := bo.closeCode(); code != closeOtherCharacter {
		t.Fatalf("Bo closed with %d", code)
	}
	pong(t, ada)
	pong(t, cy)
}

func TestTheSameCharacterTwiceIsStillReplaced(t *testing.T) {
	url := startServer(t)
	first := signInAs(t, url, 2, 1, "Bea", "")
	second := signInAs(t, url, 2, 1, "Bea", "")
	if code := first.closeCode(); code != closeReplaced {
		t.Fatalf("first socket closed with %d, want %d", code, closeReplaced)
	}
	pong(t, second)
	// The login's map still points at Bea: Ada's game socket closes her with 4012.
	signInAs(t, url, 1, 0, "Ada", "")
	if code := second.closeCode(); code != closeOtherCharacter {
		t.Fatalf("second socket closed with %d", code)
	}
}

func TestWebSocketsAreNotClosedByAnotherCharacter(t *testing.T) {
	url := startServer(t)
	adaWeb := signInAs(t, url, 1, 0, "Ada", "web")
	ada := signInAs(t, url, 1, 0, "Ada", "")
	beaWeb := signInAs(t, url, 2, 1, "Bea", "web") // a web socket never closes the game
	pong(t, ada)
	bea := signInAs(t, url, 2, 1, "Bea", "")
	if code := ada.closeCode(); code != closeOtherCharacter {
		t.Fatalf("Ada's game socket closed with %d", code)
	}
	pong(t, adaWeb)
	pong(t, beaWeb)
	pong(t, bea)
}

func TestAcctAbsentIsTheCharacter(t *testing.T) {
	tk := NewTickets(testSecret, nil)
	for _, acct := range []int64{0, -4} {
		c, err := tk.Check(altTicket(t, 7, acct, "Ada", ""))
		if err != nil || c.Acct != 7 {
			t.Fatalf("acct %d: claims %+v %v", acct, c, err)
		}
	}
	if c, err := tk.Check(altTicket(t, 8, 7, "Bea", "")); err != nil || c.Acct != 7 || c.Sub != 8 {
		t.Fatalf("claims %+v %v", c, err)
	}
	// A hub fed claims with no Acct (Check never ran) treats the character as its own login.
	h := NewHub(nil)
	a := h.Connect(Claims{Sub: 7, Name: "Ada"})
	b := h.Connect(Claims{Sub: 8, Acct: 7, Name: "Bea"})
	if !a.closed || a.why != closeOtherCharacter || b.closed {
		t.Fatalf("Ada closed %v why %d, Bea closed %v", a.closed, a.why, b.closed)
	}
	if p, _ := h.Stats(); p != 1 {
		t.Fatalf("players %d", p)
	}
	h.Disconnect(b)
	if len(h.logins) != 0 {
		t.Fatalf("logins left %v", h.logins)
	}
}

func TestABanOnTheLoginCoversEachCharacter(t *testing.T) {
	ws, base := startTeleportServer(t)
	bea := signInAs(t, ws, 2, 1, "Bea", "") // the login (1) plays as Bea
	beaWeb := signInAs(t, ws, 2, 1, "Bea", "web")
	cy := signInAs(t, ws, 3, 0, "Cy", "")
	code, out := siteCall(t, base, "ban", map[string]any{"player": 1, "reason": "Spam"}, "")
	mustStatus(t, code, out, 200, "sent")
	if m := bea.next("booted"); m["banned"] != true || m["reason"] != "Spam" {
		t.Fatalf("booted %v", m)
	}
	if c := bea.closeCode(); c != closeBanned {
		t.Fatalf("Bea closed with %d", c)
	}
	if c := beaWeb.closeCode(); c != closeBanned {
		t.Fatalf("Bea's web socket closed with %d", c)
	}
	pong(t, cy)
	// Any character of the login is refused at auth, game or web; the login's own too.
	for _, tc := range []struct {
		id, acct int64
		via      string
	}{{2, 1, ""}, {4, 1, "web"}, {1, 0, ""}} {
		p := dial(t, ws)
		p.send(map[string]any{"t": "auth", "ticket": altTicket(t, tc.id, tc.acct, "X", tc.via)})
		if c := p.closeCode(); c != closeBanned {
			t.Fatalf("%+v closed with %d", tc, c)
		}
	}
	// Unbanned, Bea plays again; the bans list names logins the same way.
	siteCall(t, base, "unban", map[string]any{"player": 1}, "")
	bea = signInAs(t, ws, 2, 1, "Bea", "")
	code, out = siteCall(t, base, "bans", map[string]any{"players": []int{1}}, "")
	mustStatus(t, code, out, 200, "ok")
	bea.next("booted")
	if c := bea.closeCode(); c != closeBanned {
		t.Fatalf("Bea closed by the list with %d", c)
	}
	pong(t, cy)
}
