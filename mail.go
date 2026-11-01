package main

// Mail between real players (register G478): the "you have mail" nudge.
// Letters, their postage and what they carry are the site's
// (groddle-afternoon-site src/LedgerMail.php, through the ledger); this
// server only tells a receiver who is online, when the letter arrives, that
// it has, so the game can fetch it and its Frog can bring it, as the source
// scheduled deliver_mail for an online player (players/inc_mail.js
// schedule_next_delivery 879-938, deliver_mail 299-326, new_inbox_growl
// 670-695). A player who is offline then finds the letter in the mailbox
// when the game next asks the site (mail_login 1448-1453).
//
// Client to server:
//
//	{"t":"mail_tell","token":"…"}   the site's proof of a letter just sent
//
// Server to client:
//
//	{"t":"mail_arrived","id":"<16 hex>","from":{"id":7,"name":"Ada"}}   to the receiver, once the letter arrives
//
// A proof is the site's signature (buddies.go's signing) over JSON
// {"k":"mail","sub":sender,"to":receiver,"n":letter id,"at":arrives (unix),
// "iat","exp"}, good once (n, the proofs journal). Only the sender's socket
// can hand it over, and a nudge between players who block each other is
// dropped (the site refuses such letters anyway; a block may come later).

import (
	"encoding/json"
	"flag"
	"strconv"
	"testing"
	"time"
)

const (
	mailTellBurst = 10
	mailTellEvery = 6 * time.Second
	// The furthest ahead a letter may say it arrives: the site's 420 s, with room.
	mailTellAhead = 15 * time.Minute
)

func init() { modules = append(modules, newMailNudge) }

type mailNudge struct {
	h      *Hub
	secret []byte
	spent  *proofJournal
	rates  map[string]*bucket
}

func newMailNudge(h *Hub) Module {
	secret := homesSecretForTest
	if !testing.Testing() {
		path := ""
		if f := flag.Lookup("secret-file"); f != nil {
			path = expandHome(f.Value.String())
		}
		// No secret: every proof is refused and letters wait for the receiver's next look.
		secret, _ = readSecret(path)
	}
	return &mailNudge{h: h, secret: secret, spent: sharedProofs(), rates: map[string]*bucket{}}
}

func (m *mailNudge) Register(r *Registry) {
	r.Handle("mail_tell", m.tell)
}

type mailClaims struct {
	K   string `json:"k"`
	Sub int64  `json:"sub"`
	To  int64  `json:"to"`
	N   string `json:"n"`
	At  int64  `json:"at"`
	Exp int64  `json:"exp"`
}

func (m *mailNudge) tell(cl *Client, frame json.RawMessage) {
	key := "mail:" + strconv.FormatInt(cl.ID(), 10)
	b := m.rates[key]
	if b == nil {
		b = &bucket{}
		m.rates[key] = b
	}
	if !b.take(m.h.Now(), mailTellBurst, mailTellEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	var msg struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(frame, &msg)
	var c mailClaims
	err := errBuddyOff
	if len(m.secret) > 0 {
		var raw []byte
		raw, err = signedBody(m.secret, msg.Token)
		if err == nil && (json.Unmarshal(raw, &c) != nil || c.K != "mail" || c.Sub <= 0 || c.To <= 0 || c.To == c.Sub || c.N == "" || len(c.N) > 40) {
			err = errBuddyToken
		}
	}
	now := m.h.Now()
	if err == nil && c.Sub != cl.ID() {
		err = errBuddyOwner
	}
	if err == nil && c.Exp < now.Unix() {
		err = errBuddyExpired
	}
	if err == nil && c.At > now.Add(mailTellAhead).Unix() {
		err = errBuddyToken
	}
	if err != nil {
		m.h.ErrorLocked(cl, "mail", err.Error())
		return
	}
	if !m.spent.spend("mail", c.N, c.Exp, now.Unix()) {
		return
	}
	from := map[string]any{"id": cl.ID(), "name": cl.Name()}
	arrive := func() {
		to := m.h.ClientLocked(c.To)
		if to == nil || m.blocked(c.To, c.Sub) || m.blocked(c.Sub, c.To) {
			return
		}
		m.h.SendLocked(to, map[string]any{"t": "mail_arrived", "id": c.N, "from": from})
	}
	if wait := time.Unix(c.At, 0).Sub(now); wait > 0 {
		m.h.After(wait, arrive)
		return
	}
	arrive()
}

func (m *mailNudge) blocked(ignorer, other int64) bool {
	fn, _ := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return fn != nil && fn(ignorer, other)
}
