package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Claims is what a ticket from groddle/v1/realtime (groddle-afternoon-site src/RealtimeTicket.php) says.
type Claims struct {
	Sub  int64  `json:"sub"`
	Name string `json:"name"`
	// Acct is the login the character belongs to (groddle-afternoon-site src/Logins.php): a login may
	// have alternate characters, and only one of them plays at a time (hub.go closeOtherCharacter).
	// 0 or absent is the character's own id (a character with no login row is its own login);
	// Check fills it in, so Acct is never 0 after it. Never sent to a player.
	Acct int64  `json:"acct,omitempty"`
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
	Jti  string `json:"jti"`
	// Via is "web" for the website's /chat/ page (web.go): a light socket with no avatar and no street,
	// beside the character's game socket, never replacing it. "" is the game.
	Via string `json:"via,omitempty"`
	// Off is the account's "Appear offline" setting when the ticket was made (presence.go).
	Off bool `json:"off,omitempty"`
	// Cone: the character is under the chat cone of silence when the ticket was made (hub.go, G480).
	Cone bool `json:"cone,omitempty"`
	// Mod: a moderator's restrictions that stand when the ticket was made (moderation.go).
	Mod *modClaim `json:"mod,omitempty"`
}

// login is the claims' login id: Acct, or the character's own id when the ticket names none.
func (c Claims) login() int64 {
	if c.Acct > 0 {
		return c.Acct
	}
	return c.Sub
}

var (
	errTicketShape   = errors.New("ticket is not a ticket")
	errTicketSigned  = errors.New("ticket signature does not match")
	errTicketExpired = errors.New("ticket has expired")
	errTicketReused  = errors.New("ticket was already used")
)

// Tickets checks tickets against the shared secret and refuses any ticket twice.
type Tickets struct {
	secret []byte
	now    func() time.Time

	used *proofJournal // spent jtis until they expire (proofs.go: kept across a restart)
}

func NewTickets(secret []byte, now func() time.Time) *Tickets {
	if now == nil {
		now = time.Now
	}
	return &Tickets{secret: secret, now: now, used: sharedProofs()}
}

// Check verifies a ticket's signature and expiry, then spends it.
func (t *Tickets) Check(ticket string) (Claims, error) {
	var c Claims
	body, sig, ok := strings.Cut(ticket, ".")
	if !ok || body == "" || sig == "" {
		return c, errTicketShape
	}
	want := hmac.New(sha256.New, t.secret)
	want.Write([]byte(body))
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want.Sum(nil)) {
		return c, errTicketSigned
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Sub <= 0 || c.Jti == "" {
		return Claims{}, errTicketShape
	}
	now := t.now().Unix()
	if c.Exp < now {
		return Claims{}, errTicketExpired
	}
	if !t.used.spend("ticket", c.Jti, c.Exp, now) {
		return Claims{}, errTicketReused
	}
	c.Acct = c.login()
	c.Name = cleanName(c.Name)
	return c, nil
}

// unnamedPlayer is the name shown for a player whose ticket names nobody (a
// blank display name). The original always had a pc.label, so no fallback
// survives; "Someone" is the word boards.go, drops.go and party_persist.go
// already use, and the game's own fallback (js/player-names.js UNNAMED_PLAYER).
// Ours: never the game's name in our own copy (was "Glitchen").
const unnamedPlayer = "Someone"

// cleanName keeps a display name to one short line.
func cleanName(name string) string {
	name = strings.TrimSpace(stripControl(name))
	if r := []rune(name); len(r) > 40 {
		name = string(r[:40])
	}
	if name == "" {
		name = unnamedPlayer
	}
	return name
}
