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
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
	Jti  string `json:"jti"`
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
	c.Name = cleanName(c.Name)
	return c, nil
}

// cleanName keeps a display name to one short line.
func cleanName(name string) string {
	name = strings.TrimSpace(stripControl(name))
	if r := []rune(name); len(r) > 40 {
		name = string(r[:40])
	}
	if name == "" {
		name = "Glitchen"
	}
	return name
}
