package main

// Buddies (register G325): who on your buddy list is online and where, as in
// the original. The list itself is the site's (groddle-afternoon-site
// src/Buddies.php, tables groddle_buddies and groddle_ignores); this module
// only knows presence. It never takes a buddy list from a client: the site
// signs one (the same secret and signing as the realtime ticket) and the
// game hands it over, and from then on the server tells the watcher when
// those players come online, go offline or change street. The rules are
// GameServerJS players/inc_buddies.js and players/player.js; the copy
// players read is the game's (js/buddies.js).
//
// Client to server:
//
//	{"t":"buddies_watch","token":"…"}   the list the site signed (groddle/v1/buddies answers one); again after each change
//	{"t":"buddy_tell","token":"…"}      "I just added them": the proof the site signed with the add
//
// Server to client:
//
//	{"t":"buddies_status","online":[{"id":7,"name":"Ada","street":"GA5…"}]}   who on the watched list is online, after each watch
//	{"t":"buddy_online","id":7,"name":"Ada","street":"GA5…"}
//	{"t":"buddy_offline","id":7,"name":"Ada"}
//	{"t":"buddy_moved","id":7,"name":"Ada","street":"GA5…"}                  street "" is a private place (a home, a quest room, a race)
//	{"t":"buddy_added","id":7,"name":"Ada"}                                  Ada just added you
//	{"t":"error","code":"buddies","message":"…"}
//
// The watch token is base64url(JSON) "." base64url(HMAC-SHA256(secret, the
// first part)), with JSON {"k":"buddies","sub":7,"b":"…","r":"…","i":"…",
// "iat":…,"exp":…}: b the player's buddies, r who has them as a buddy (the
// source's reverse contacts), i who they ignore, each a list of ids sorted,
// as differences from the one before, in base 36, joined by commas
// (buddyIDs). The add proof is {"k":"buddy_added","sub":7,"to":9,"iat",
// "exp","n"}: good once (n), for two minutes. Neither has a jti, so neither
// opens a socket as a realtime ticket does (ticket.go), and a ticket has no
// "k", so it is no buddy list.
//
// Rules kept from the source:
//   - Coming online and going offline is told to the players who hold you as
//     a buddy (player.js:113-127 buddies_send_buddy_online to the reverse
//     contacts, player.js:248-252 buddies_send_buddy_offline;
//     inc_buddies.js:834-853). Here it is also told to the players you hold
//     (the lead's design, G325: the reverse entry is you choosing them), so a
//     watcher hears about everyone on both lists.
//   - What is told: the name and where they are, "A secret place" for a
//     hidden one (inc_buddies.js:343 and 384, location.is_hidden): street "" here.
//   - Ignoring: someone you ignore hears nothing about you, and you hear
//     nothing about them (the source removes the buddy entries both ways,
//     inc_buddies.js:512-597; the site's list drops them). Their party
//     invites and trade offers are refused (inc_trading.js:125-130 "You are
//     blocking or blocked by that player."): party.go and trade.go ask
//     h.ProvidedLocked("buddies.ignoring"), a func(ignorer, other int64) bool.
//   - "X just added you as a contact!" (inc_buddies.js:209
//     buddies_has_added, sendOnlineActivity: only when online; offline the
//     site's list marks the reverse entry new).
//
// Ours: a player counts as online for their buddies once their game has sent
// its own signed list (so their ignore list is known before anyone hears
// about them); a socket that drops has buddyGrace to come back before its
// buddies hear it went offline (a reload is not a logout); a street change
// (a leave, then a join) is told once, after buddySettle; rates.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	buddyGrace      = 15 * time.Second       // ours: a dropped socket's time to come back
	buddySettle     = 300 * time.Millisecond // ours: a leave then a join is one move
	buddyTellLife   = 2 * time.Minute        // the longest an add proof is honoured, whatever it says
	buddyMaxIDs     = 4000                   // ids one token may carry (the site's limit is 1000 buddies)
	buddyWatchBurst = 6
	buddyWatchEvery = 5 * time.Second
	buddyTellBurst  = 10
	buddyTellEvery  = 3 * time.Second
)

// buddySecretForTest is the secret in tests (the flag is not parsed there).
var buddySecretForTest []byte

func init() { modules = append(modules, newBuddies) }

type buddyWatch struct {
	bud, rev, ign map[int64]bool
	exp           time.Time
}

type buddies struct {
	h      *Hub
	secret []byte
	watch  map[int64]*buddyWatch
	live   map[int64]string // told online, and the street last told
	away   map[int64]func() bool
	settle map[int64]func() bool
	spent  map[string]int64 // add proofs used: n -> exp
	rates  map[string]*bucket
}

func newBuddies(h *Hub) Module {
	secret := buddySecretForTest
	if !testing.Testing() {
		path := ""
		if f := flag.Lookup("secret-file"); f != nil {
			path = expandHome(f.Value.String())
		}
		// No secret (a build run without one) leaves buddies off: every watch is refused.
		secret, _ = readSecret(path)
	}
	return &buddies{h: h, secret: secret, watch: map[int64]*buddyWatch{}, live: map[int64]string{},
		away: map[int64]func() bool{}, settle: map[int64]func() bool{}, spent: map[string]int64{}, rates: map[string]*bucket{}}
}

func (m *buddies) Register(r *Registry) {
	r.Handle("buddies_watch", m.watchMsg)
	r.Handle("buddy_tell", m.tell)
	r.OnJoin(func(cl *Client) { m.joined(cl) })
	r.OnLeave(func(cl *Client, _ string) { m.left(cl) })
	r.OnDisconnect(m.disconnected)
	// For party.go and trade.go: refuse what an ignored player sends (fn(ignorer, other)).
	r.Provide("buddies.ignoring", m.ignores)
}

// —— Tokens ——

type buddyClaims struct {
	K   string `json:"k"`
	Sub int64  `json:"sub"`
	B   string `json:"b"`
	R   string `json:"r"`
	I   string `json:"i"`
	To  int64  `json:"to"`
	N   string `json:"n"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

var (
	errBuddyOff     = errors.New("buddies are not set up on this server")
	errBuddyToken   = errors.New("that is not a token the site signed")
	errBuddyExpired = errors.New("that token has expired")
	errBuddyOwner   = errors.New("that token is not yours")
)

// verify checks a token's signature, kind, owner and expiry.
func (m *buddies) verify(token, kind string, owner int64) (buddyClaims, error) {
	var c buddyClaims
	if len(m.secret) == 0 {
		return c, errBuddyOff
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return c, errBuddyToken
	}
	want := hmac.New(sha256.New, m.secret)
	want.Write([]byte(body))
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want.Sum(nil)) {
		return c, errBuddyToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.K != kind || c.Sub <= 0 {
		return buddyClaims{}, errBuddyToken
	}
	if c.Sub != owner {
		return buddyClaims{}, errBuddyOwner
	}
	if c.Exp < m.h.Now().Unix() {
		return buddyClaims{}, errBuddyExpired
	}
	return c, nil
}

// buddyIDs reads a token's id list: base-36 differences, comma separated.
func buddyIDs(s string) (map[int64]bool, bool) {
	out := map[int64]bool{}
	if s == "" {
		return out, true
	}
	var at int64
	for i, part := range strings.Split(s, ",") {
		if i >= buddyMaxIDs {
			return nil, false
		}
		d, err := strconv.ParseInt(part, 36, 64)
		if err != nil || d < 0 || (i > 0 && d == 0) {
			return nil, false
		}
		at += d
		if at <= 0 {
			return nil, false
		}
		out[at] = true
	}
	return out, true
}

// —— Who hears what ——

func (m *buddies) ignores(ignorer, other int64) bool {
	w := m.watch[ignorer]
	return w != nil && w.ign[other]
}

// blocked is either one ignoring the other (inc_trading.js:125 buddies_is_ignored_by || buddies_is_ignoring).
func (m *buddies) blocked(a, b int64) bool { return m.ignores(a, b) || m.ignores(b, a) }

// holds is whether watcher's signed list names id, as a buddy or a reverse buddy.
func (m *buddies) holds(watcher, id int64) bool {
	w := m.watch[watcher]
	return w != nil && !w.exp.Before(m.h.Now()) && (w.bud[id] || w.rev[id])
}

// tellable is whether watcher may hear about id's comings and goings now.
func (m *buddies) tellable(watcher, id int64) bool {
	return watcher != id && m.h.ClientLocked(watcher) != nil && m.holds(watcher, id) && !m.blocked(watcher, id)
}

// shownStreet is where others are told a player is: "" in a private place (a race is one).
func shownStreet(cl *Client) string {
	if cl == nil || strings.HasPrefix(cl.Street(), "RACE_") {
		return ""
	}
	return cl.Street()
}

func (m *buddies) send(id int64, msg any) {
	if cl := m.h.ClientLocked(id); cl != nil {
		m.h.SendLocked(cl, msg)
	}
}

// tellWatchers sends msg about id to everyone who may hear it, but skip.
func (m *buddies) tellWatchers(id int64, msg map[string]any, skip map[int64]bool) {
	ids := make([]int64, 0, len(m.watch))
	for w := range m.watch {
		ids = append(ids, w)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, w := range ids {
		if !skip[w] && m.tellable(w, id) {
			m.send(w, msg)
		}
	}
}

func (m *buddies) presence(t string, cl *Client, street string) map[string]any {
	msg := map[string]any{"t": t, "id": cl.ID(), "name": cl.Name()}
	if t != "buddy_offline" {
		msg["street"] = street
	}
	return msg
}

func (m *buddies) allow(kind string, cl *Client, burst float64, every time.Duration) bool {
	key := kind + ":" + strconv.FormatInt(cl.ID(), 10)
	b := m.rates[key]
	if b == nil {
		b = &bucket{}
		m.rates[key] = b
	}
	if !b.take(m.h.Now(), burst, every) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return false
	}
	return true
}

// —— Handlers ——

func (m *buddies) watchMsg(cl *Client, frame json.RawMessage) {
	var msg struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	if !m.allow("watch", cl, buddyWatchBurst, buddyWatchEvery) {
		return
	}
	c, err := m.verify(msg.Token, "buddies", cl.ID())
	if err != nil {
		m.h.ErrorLocked(cl, "buddies", "Your buddy list could not be checked: "+err.Error()+".")
		return
	}
	bud, ok1 := buddyIDs(c.B)
	rev, ok2 := buddyIDs(c.R)
	ign, ok3 := buddyIDs(c.I)
	if !ok1 || !ok2 || !ok3 {
		m.h.ErrorLocked(cl, "buddies", "Your buddy list could not be checked: "+errBuddyToken.Error()+".")
		return
	}
	id := cl.ID()
	old := m.watch[id]
	// Who could hear about this player before the new list (an ignore can take them off).
	var before map[int64]bool
	if _, on := m.live[id]; on {
		before = map[int64]bool{}
		for w := range m.watch {
			if m.tellable(w, id) {
				before[w] = true
			}
		}
	}
	m.watch[id] = &buddyWatch{bud: bud, rev: rev, ign: ign, exp: time.Unix(c.Exp, 0)}
	if old != nil && before != nil {
		// A new ignore takes them off the other's list now, as the source's
		// removeBuddy does (inc_buddies.js:512-597); an un-ignore puts them back.
		for w := range before {
			if !m.tellable(w, id) {
				m.send(w, m.presence("buddy_offline", cl, ""))
			}
		}
		street := m.live[id]
		for w := range m.watch {
			if !before[w] && m.tellable(w, id) {
				m.send(w, m.presence("buddy_online", cl, street))
			}
		}
	}
	m.cameBack(cl)
	// Who on the list is online now.
	online := []map[string]any{}
	var ids []int64
	for o := range bud {
		ids = append(ids, o)
	}
	for o := range rev {
		if !bud[o] {
			ids = append(ids, o)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, o := range ids {
		ocl := m.h.ClientLocked(o)
		if _, on := m.live[o]; !on || ocl == nil || !m.tellable(id, o) {
			continue
		}
		online = append(online, map[string]any{"id": o, "name": ocl.Name(), "street": m.live[o]})
	}
	m.send(id, map[string]any{"t": "buddies_status", "online": online})
}

// cameBack makes a player with a list online for their buddies (or back from a dropped socket).
func (m *buddies) cameBack(cl *Client) {
	id := cl.ID()
	if stop := m.away[id]; stop != nil {
		stop()
		delete(m.away, id)
	}
	street := shownStreet(cl)
	told, on := m.live[id]
	m.live[id] = street
	switch {
	case !on:
		m.tellWatchers(id, m.presence("buddy_online", cl, street), nil)
	case told != street:
		m.tellWatchers(id, m.presence("buddy_moved", cl, street), nil)
	}
}

func (m *buddies) tell(cl *Client, frame json.RawMessage) {
	var msg struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	if !m.allow("tell", cl, buddyTellBurst, buddyTellEvery) {
		return
	}
	c, err := m.verify(msg.Token, "buddy_added", cl.ID())
	if err == nil && (c.To <= 0 || c.To == c.Sub || c.N == "" || len(c.N) > 64 || c.Iat > m.h.Now().Unix()+60 || c.Exp > m.h.Now().Add(buddyTellLife).Unix()) {
		err = errBuddyToken
	}
	if err != nil {
		m.h.ErrorLocked(cl, "buddies", "That could not be checked: "+err.Error()+".")
		return
	}
	now := m.h.Now().Unix()
	for n, exp := range m.spent {
		if exp < now {
			delete(m.spent, n)
		}
	}
	if _, used := m.spent[c.N]; used {
		return
	}
	m.spent[c.N] = c.Exp
	// inc_buddies.js:209 sendOnlineActivity: only when they are online, and not to someone ignoring you.
	if m.h.ClientLocked(c.To) == nil || m.ignores(c.To, c.Sub) {
		return
	}
	m.send(c.To, map[string]any{"t": "buddy_added", "id": cl.ID(), "name": cl.Name()})
}

// —— Lifecycle ——

func (m *buddies) joined(cl *Client) {
	id := cl.ID()
	if stop := m.settle[id]; stop != nil {
		stop()
		delete(m.settle, id)
	}
	told, on := m.live[id]
	if !on {
		return
	}
	if street := shownStreet(cl); street != told {
		m.live[id] = street
		m.tellWatchers(id, m.presence("buddy_moved", cl, street), nil)
	}
}

// left waits a moment: a street change is a leave, then a join.
func (m *buddies) left(cl *Client) {
	id := cl.ID()
	if _, on := m.live[id]; !on || m.settle[id] != nil {
		return
	}
	m.settle[id] = m.h.After(buddySettle, func() {
		delete(m.settle, id)
		now := m.h.ClientLocked(id)
		told, on := m.live[id]
		if !on || now == nil {
			return
		}
		if street := shownStreet(now); street != told {
			m.live[id] = street
			m.tellWatchers(id, m.presence("buddy_moved", now, street), nil)
		}
	})
}

func (m *buddies) disconnected(cl *Client) {
	id := cl.ID()
	if stop := m.settle[id]; stop != nil {
		stop()
		delete(m.settle, id)
	}
	if _, on := m.live[id]; !on {
		delete(m.watch, id)
		m.forget(id)
		return
	}
	if stop := m.away[id]; stop != nil {
		stop()
	}
	name := cl.Name()
	m.away[id] = m.h.After(buddyGrace, func() {
		delete(m.away, id)
		if m.h.ClientLocked(id) != nil {
			return
		}
		// player.js:248-252: the buddies hear you went offline.
		msg := map[string]any{"t": "buddy_offline", "id": id, "name": name}
		m.tellWatchers(id, msg, nil)
		delete(m.live, id)
		delete(m.watch, id)
		m.forget(id)
	})
}

func (m *buddies) forget(id int64) {
	suffix := ":" + strconv.FormatInt(id, 10)
	for k := range m.rates {
		if strings.HasSuffix(k, suffix) {
			delete(m.rates, k)
		}
	}
}

// blockedBetween is whether either player blocks the other (buddies.ignoring), for the systems that
// refuse a blocked player: party invites and trades (inc_trading.js 125-130). False in a build without
// the buddies module.
func blockedBetween(h *Hub, a, b int64) bool {
	ign, ok := h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return ok && (ign(a, b) || ign(b, a))
}
