package main

// Visiting real players' homes (register G328): a player's home street and
// house are places others can stand in together, as in the original. The
// rules are GameServerJS locations/inc_pols.js pols_canEnter 278-532 (a home
// street is public unless its owner blocks you, 448-455; the house lets in
// a key-holder, 461-476, or anyone the owner, online, has let in, 478-532),
// players/inc_houses.js (knocking: houses_familiar_knock 446-495,
// houses_auth_request 119-152, houses_auth_req 154-165, house_set_auth
// 77-96; houses_logout 6-58 sends everyone without a key out of the house
// of an owner who goes offline), players/inc_acl_keys.js (acl_keys_grant_receive
// 44-65 tells the new key-holder; acl_keys_remove 67-81 walks a holder whose
// key is taken back out of the house, pols_kickPlayer). The copy players
// read is theirs.
//
// What a home looks like is the site's: groddle/v1/homes/<id>
// (groddle-afternoon-site src/Neighbours.php) reads the owner's save, checks
// houses_visit (blocked, tutorial, no house) and signs a pass the game hands
// over here. Nobody but the owner joins HOME_<owner> (the home street) or
// HOUSE_<owner> (the house) without one (GateJoin), so the server never
// takes a client's word for whose home it may stand in.
//
// Client to server:
//
//	{"t":"home_pass","token":"…"}            the site's pass for one home (again after each fetch, and on a new socket)
//	{"t":"home_knock","owner":7}             knock on the door (houses_familiar_knock 'knock')
//	{"t":"home_answer","id":9,"allow":true}  the owner's answer (houses_auth_req)
//	{"t":"home_key_tell","token":"…"}        the site's proof of a key given or taken back
//
// Server to client:
//
//	{"t":"home_pass_ok","owner":7,"key":true}
//	{"t":"home_knocked","id":9,"name":"Bo","text":"Bo is knocking on your door and would like to enter your house..."}   to the owner, for knockWait
//	{"t":"home_knock_sent","owner":7,"text":"Ok! We'll let you know if they answer the door"}
//	{"t":"home_open","owner":7,"why":"key|let_in|owner"}            the door is open to you: go in
//	{"t":"home_let_in","owner":{"id":7,"name":"Ada"},"text":"Ada has let you into their house."}
//	{"t":"home_answered","owner":7,"allow":false}                   no answer, or no (the source says nothing)
//	{"t":"home_refused","owner":7,"why":"offline|away|asked|blocked|private","text":"…"}
//	{"t":"home_entered","id":9,"name":"Bo","text":"Bo just entered your house."}   to the owner, a key-holder came in
//	{"t":"home_key","from":{"id":7,"name":"Ada"},"on":true,"text":"Ada sent you a key to their house! (Keys can be found at your Magic Rock.)"}
//	{"t":"home_out","owner":7,"why":"revoked|owner_left"}           you are out of the house: back to the home street
//	{"t":"error","code":"home_private|home_blocked|home_locked|homes","message":"…"}
//
// A pass is base64url(JSON) "." base64url(HMAC-SHA256(secret, the first
// part)), JSON {"k":"home","sub":visitor,"o":owner,"n":"owner's name",
// "key":0|1,"iat","exp"}; a key proof {"k":"house_key","sub":owner,
// "to":holder,"on":0|1,"iat","exp","n"} is good once (n). Neither has a
// jti, so neither opens a socket as a realtime ticket does.
//
// Ours: a pass is good for homePassLife at most whatever it says; a knock
// nobody answers in knockWait is told to the knocker (the source said
// nothing, and the knocker could not knock again until the owner logged
// out); being let in lasts while the owner is online (house_auth lives
// until houses_logout).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	homeStreetPrefix  = "HOME_"
	houseStreetPrefix = "HOUSE_"
	homeKnockWait     = 20 * time.Second // inc_houses.js:146 the knock prompt's timeout 20
	homePassLife      = time.Hour        // ours: the longest a pass is honoured, whatever it says
	homeTellLife      = 2 * time.Minute  // the longest a key proof is honoured
	homePassBurst     = 12
	homePassEvery     = 2 * time.Second
	homeKnockBurst    = 4
	homeKnockEvery    = 5 * time.Second
	homeTellBurst     = 10
	homeTellEvery     = 3 * time.Second

	homeTextKnocking = " is knocking on your door and would like to enter your house..."         // inc_houses.js:142
	homeTextSent     = "Ok! We'll let you know if they answer the door"                          // inc_houses.js:488
	homeTextLetIn    = " has let you into their house."                                          // inc_houses.js:89
	homeTextEntered  = " just entered your house."                                               // inc_pols.js:466
	homeTextKey      = " sent you a key to their house! (Keys can be found at your Magic Rock.)" // inc_acl_keys.js:57
	homeTextPrivate  = "Visit their home street from the game first."                            // ours: the source never let anyone try
	homeTextBlocked  = "You are blocked by that player."                                         // inc_houses.js:941
	homeTextLocked   = "That house is locked."                                                   // ours
)

// homesSecretForTest is the secret in tests (the flag is not parsed there).
var homesSecretForTest []byte

func init() { modules = append(modules, newHomes) }

// homePass is one visitor's pass to one home.
type homePass struct {
	name string // the owner's name, as the site signed it
	key  bool
	exp  time.Time
}

type homes struct {
	h      *Hub
	secret []byte
	passes map[int64]map[int64]*homePass   // visitor -> owner -> pass
	authed map[int64]map[int64]bool        // owner -> visitors let in (house_auth), while the owner is online
	asked  map[int64]map[int64]func() bool // owner -> visitors knocking now (house_auth_req) -> stop the wait
	spent  map[string]int64                // key proofs used: n -> exp
	rates  map[string]*bucket
}

func newHomes(h *Hub) Module {
	secret := homesSecretForTest
	if !testing.Testing() {
		path := ""
		if f := flag.Lookup("secret-file"); f != nil {
			path = expandHome(f.Value.String())
		}
		// No secret leaves visiting off: every pass is refused, and only owners stand in their homes.
		secret, _ = readSecret(path)
	}
	return &homes{h: h, secret: secret, passes: map[int64]map[int64]*homePass{}, authed: map[int64]map[int64]bool{},
		asked: map[int64]map[int64]func() bool{}, spent: map[string]int64{}, rates: map[string]*bucket{}}
}

func (m *homes) Register(r *Registry) {
	r.Handle("home_pass", m.passMsg)
	r.Handle("home_knock", m.knock)
	r.Handle("home_answer", m.answer)
	r.Handle("home_key_tell", m.keyTell)
	r.GateJoin(m.gate)
	r.OnJoin(m.joined)
	r.OnDisconnect(m.disconnected)
}

// homeOwner reads the owner's id from a HOME_<id> or HOUSE_<id> street; 0 for any other street.
func homeOwner(street string) (owner int64, house bool) {
	rest, ok := strings.CutPrefix(street, homeStreetPrefix)
	if !ok {
		if rest, ok = strings.CutPrefix(street, houseStreetPrefix); !ok {
			return 0, false
		}
		house = true
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != rest {
		return 0, false
	}
	return id, house
}

// —— Tokens ——

type homeClaims struct {
	K   string `json:"k"`
	Sub int64  `json:"sub"`
	O   int64  `json:"o"`
	Nm  string `json:"name"`
	Key int    `json:"key"`
	N   string `json:"n"`
	To  int64  `json:"to"`
	On  int    `json:"on"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// verify checks a token the site signed: signature, kind, the sender, expiry (buddies.go's signing).
func (m *homes) verify(token, kind string, sender int64) (homeClaims, error) {
	var c homeClaims
	if len(m.secret) == 0 {
		return c, errBuddyOff
	}
	raw, err := signedBody(m.secret, token)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.K != kind || c.Sub <= 0 {
		return homeClaims{}, errBuddyToken
	}
	if c.Sub != sender {
		return homeClaims{}, errBuddyOwner
	}
	if c.Exp < m.h.Now().Unix() {
		return homeClaims{}, errBuddyExpired
	}
	return c, nil
}

// signedBody is the JSON a token signed with secret carries, or an error.
func signedBody(secret []byte, token string) ([]byte, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return nil, errBuddyToken
	}
	want := hmac.New(sha256.New, secret)
	want.Write([]byte(body))
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want.Sum(nil)) {
		return nil, errBuddyToken
	}
	return base64.RawURLEncoding.DecodeString(body)
}

// pass is visitor's live pass to owner's home, or nil.
func (m *homes) pass(visitor, owner int64) *homePass {
	p := m.passes[visitor][owner]
	if p == nil || p.exp.Before(m.h.Now()) {
		return nil
	}
	return p
}

func (m *homes) blocked(owner, visitor int64) bool {
	fn, _ := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return fn != nil && fn(owner, visitor)
}

// ownerName is the owner's name: online, their socket's; else what the pass says.
func (m *homes) ownerName(owner int64, p *homePass) string {
	if cl := m.h.ClientLocked(owner); cl != nil {
		return cl.Name()
	}
	if p != nil && p.name != "" {
		return p.name
	}
	return "The owner"
}

// atHome is houses_is_at_home: on their home street or in their house.
func atHome(cl *Client) bool {
	if cl == nil {
		return false
	}
	o, _ := homeOwner(cl.Street())
	return o == cl.ID()
}

func (m *homes) allow(kind string, cl *Client, burst float64, every time.Duration) bool {
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

func (m *homes) send(id int64, msg any) {
	if cl := m.h.ClientLocked(id); cl != nil {
		m.h.SendLocked(cl, msg)
	}
}

// —— Who may stand where (pols_canEnter) ——

func (m *homes) gate(cl *Client, street string) (bool, string, string) {
	owner, house := homeOwner(street)
	if owner == 0 || owner == cl.ID() {
		return true, "", ""
	}
	p := m.pass(cl.ID(), owner)
	if p == nil {
		return false, "home_private", homeTextPrivate
	}
	// inc_pols.js:448-455 blocked_by_owner: a block made since the pass still keeps them out.
	if m.blocked(owner, cl.ID()) {
		return false, "home_blocked", homeTextBlocked
	}
	if !house || p.key || (m.authed[owner][cl.ID()] && m.h.ClientLocked(owner) != nil) {
		return true, "", ""
	}
	return false, "home_locked", homeTextLocked
}

// joined: a key-holder coming into the house is told to the owner (inc_pols.js:463-473).
func (m *homes) joined(cl *Client) {
	owner, house := homeOwner(cl.Street())
	if !house || owner == cl.ID() {
		return
	}
	if p := m.pass(cl.ID(), owner); p != nil && p.key {
		m.send(owner, map[string]any{"t": "home_entered", "id": cl.ID(), "name": cl.Name(), "text": cl.Name() + homeTextEntered})
	}
}

// disconnected is houses_logout 6-58 for the owner, and the end of a visitor's passes and knocks.
func (m *homes) disconnected(cl *Client) {
	id := cl.ID()
	delete(m.passes, id)
	for _, stop := range m.asked[id] {
		stop()
	}
	delete(m.asked, id)
	for owner, waiting := range m.asked {
		if stop := waiting[id]; stop != nil {
			stop()
			delete(waiting, id)
			if len(waiting) == 0 {
				delete(m.asked, owner)
			}
		}
	}
	delete(m.authed, id)
	for _, o := range m.h.StreetLocked(houseStreetPrefix + strconv.FormatInt(id, 10)) {
		if p := m.pass(o.ID(), id); p != nil && p.key {
			continue
		}
		m.out(o, id, "owner_left")
	}
}

// out walks a visitor out of owner's house (pols_kickPlayer: to the entrance outside).
func (m *homes) out(cl *Client, owner int64, why string) {
	m.h.leaveLocked(cl)
	m.h.SendLocked(cl, map[string]any{"t": "home_out", "owner": owner, "why": why})
}

// —— Handlers ——

func (m *homes) passMsg(cl *Client, frame json.RawMessage) {
	if !m.allow("pass", cl, homePassBurst, homePassEvery) {
		return
	}
	var msg struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(frame, &msg)
	c, err := m.verify(msg.Token, "home", cl.ID())
	if err != nil || c.O <= 0 {
		if err == nil {
			err = errBuddyToken
		}
		m.h.ErrorLocked(cl, "homes", err.Error())
		return
	}
	exp := time.Unix(c.Exp, 0)
	if most := m.h.Now().Add(homePassLife); exp.After(most) {
		exp = most
	}
	if m.passes[cl.ID()] == nil {
		m.passes[cl.ID()] = map[int64]*homePass{}
	}
	m.passes[cl.ID()][c.O] = &homePass{name: cleanName(c.Nm), key: c.Key == 1, exp: exp}
	m.h.SendLocked(cl, map[string]any{"t": "home_pass_ok", "owner": c.O, "key": c.Key == 1})
}

func (m *homes) refuse(cl *Client, owner int64, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "home_refused", "owner": owner, "why": why, "text": text})
}

// knock is houses_familiar_knock 446-495 and houses_auth_request 119-152.
func (m *homes) knock(cl *Client, frame json.RawMessage) {
	if !m.allow("knock", cl, homeKnockBurst, homeKnockEvery) {
		return
	}
	var msg struct {
		Owner int64 `json:"owner"`
	}
	_ = json.Unmarshal(frame, &msg)
	o := msg.Owner
	if o == cl.ID() {
		m.h.SendLocked(cl, map[string]any{"t": "home_open", "owner": o, "why": "owner"})
		return
	}
	p := m.pass(cl.ID(), o)
	if p == nil {
		m.refuse(cl, o, "private", homeTextPrivate)
		return
	}
	name := m.ownerName(o, p)
	if m.blocked(o, cl.ID()) {
		m.refuse(cl, o, "blocked", name+" is blocking you.") // inc_houses.js:453
		return
	}
	owner := m.h.ClientLocked(o)
	if p.key || (owner != nil && m.authed[o][cl.ID()]) {
		why := "key"
		if !p.key {
			why = "let_in"
		}
		m.h.SendLocked(cl, map[string]any{"t": "home_open", "owner": o, "why": why})
		return
	}
	if owner == nil {
		m.refuse(cl, o, "offline", "The owner of this location ("+name+") is currently offline") // inc_houses.js:434
		return
	}
	if !atHome(owner) {
		m.refuse(cl, o, "away", "The owner of this location ("+name+") is not at home right now") // inc_houses.js:441
		return
	}
	if m.asked[o][cl.ID()] != nil {
		m.refuse(cl, o, "asked", "You've already asked "+name+" if you can enter - be patient!") // inc_houses.js:462
		return
	}
	if m.asked[o] == nil {
		m.asked[o] = map[int64]func() bool{}
	}
	visitor := cl.ID()
	m.asked[o][visitor] = m.h.After(homeKnockWait, func() {
		// timeout_value 'dismiss' (inc_houses.js:147): the request goes, unanswered.
		if waiting := m.asked[o]; waiting != nil && waiting[visitor] != nil {
			delete(waiting, visitor)
			if len(waiting) == 0 {
				delete(m.asked, o)
			}
			m.send(visitor, map[string]any{"t": "home_answered", "owner": o, "allow": false})
			m.send(o, map[string]any{"t": "home_knock_over", "id": visitor})
		}
	})
	m.h.SendLocked(owner, map[string]any{"t": "home_knocked", "id": visitor, "name": cl.Name(), "text": cl.Name() + homeTextKnocking})
	m.h.SendLocked(cl, map[string]any{"t": "home_knock_sent", "owner": o, "text": homeTextSent})
}

// answer is houses_auth_req 154-165: allow sets house_auth (house_set_auth 77-96), anything else drops the request.
func (m *homes) answer(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID    int64 `json:"id"`
		Allow bool  `json:"allow"`
	}
	_ = json.Unmarshal(frame, &msg)
	waiting := m.asked[cl.ID()]
	stop := waiting[msg.ID]
	if stop == nil {
		return
	}
	stop()
	delete(waiting, msg.ID)
	if len(waiting) == 0 {
		delete(m.asked, cl.ID())
	}
	if !msg.Allow {
		m.send(msg.ID, map[string]any{"t": "home_answered", "owner": cl.ID(), "allow": false})
		return
	}
	if m.authed[cl.ID()] == nil {
		m.authed[cl.ID()] = map[int64]bool{}
	}
	m.authed[cl.ID()][msg.ID] = true
	m.send(msg.ID, map[string]any{"t": "home_let_in", "owner": map[string]any{"id": cl.ID(), "name": cl.Name()}, "text": cl.Name() + homeTextLetIn})
}

// keyTell is acl_keys_grant_receive 44-65 (a key given) and acl_keys_remove 67-81 (taken back).
func (m *homes) keyTell(cl *Client, frame json.RawMessage) {
	if !m.allow("key", cl, homeTellBurst, homeTellEvery) {
		return
	}
	var msg struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(frame, &msg)
	c, err := m.verify(msg.Token, "house_key", cl.ID())
	if err == nil && (c.To <= 0 || c.To == cl.ID()) {
		err = errBuddyToken
	}
	if err == nil && c.N == "" {
		err = errBuddyToken
	}
	if err != nil {
		m.h.ErrorLocked(cl, "homes", err.Error())
		return
	}
	now := m.h.Now().Unix()
	if c.Exp > now+int64(homeTellLife.Seconds()) {
		c.Exp = now + int64(homeTellLife.Seconds())
	}
	for n, exp := range m.spent {
		if exp < now {
			delete(m.spent, n)
		}
	}
	if _, used := m.spent[c.N]; used {
		return
	}
	m.spent[c.N] = c.Exp
	owner, holder, on := cl.ID(), c.To, c.On == 1
	if p := m.passes[holder][owner]; p != nil {
		p.key = on
	}
	from := map[string]any{"id": owner, "name": cl.Name()}
	if on {
		m.send(holder, map[string]any{"t": "home_key", "from": from, "on": true, "text": cl.Name() + homeTextKey})
		return
	}
	delete(m.authed[owner], holder)
	m.send(holder, map[string]any{"t": "home_key", "from": from, "on": false})
	if h := m.h.ClientLocked(holder); h != nil {
		if o, house := homeOwner(h.Street()); house && o == owner {
			m.out(h, owner, "revoked")
		}
	}
}
