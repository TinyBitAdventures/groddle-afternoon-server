package main

// Helping out at a real player's home (register G328, slice 2; homes.go is
// slice 1, the visiting itself):
//
//   - The owner's Butler takes a visitor's package and message
//     (GameServerJS items/bag_butler.js: leave_package 991-1144 and
//     onLeavePackage 3776-3845, leave_message 1146-1195 and takeMessage
//     5833-5873). Only a visitor standing on the owner's home street (which
//     homes.go only lets a holder of the site's pass onto) may leave one,
//     one visitor at a time ("The Butler is talking to somebody else right
//     now.", include/butler_strings.js:165). The package moves through the
//     site's ledger (groddle-afternoon-site src/ButlerParcels.php, POST
//     groddle/v1/ledger/butler, signed as ledger.go signs): it leaves the
//     visitor's ledger at once and waits with the Butler for the owner, who
//     is told now if online (the source's Butler IMed an owner away from
//     home, bag_butler.js sendIM).
//   - A key-holder's tending of the owner's gardens is the site's to check
//     and keep (src/HomeTending.php); its signed proof brings the owner and
//     everyone else on that street the change at once (home_tend_tell).
//   - Hosting (locations/home.js:55-66): whenever someone comes into a house
//     while its owner is in it, four or more players there counts the owner
//     a hosted party of three guests (crowded_house), eighteen or more one of
//     seventeen (house_party). The owner's game counts the badge.
//
// Client to server:
//
//	{"t":"butler_leave","owner":7,"item":{"c":"apple","n":3},"text":"…"}   item or text or both
//	{"t":"home_tend_tell","token":"…"}                                    the site's proof of tending done
//
// Server to client:
//
//	{"t":"butler_left","owner":7,"op":"butler-…","status":"settled|refused","why":"busy|full|type|blocked|no_butler|short|off","text":"…","item":{…}|null,"message":true}
//	{"t":"butler_parcel","from":{"id":9,"name":"Bo"},"item":true,"message":true}   to the owner, online
//	{"t":"home_tended","owner":7,"from":{"id":9,"name":"Bo"},"acts":[{"y":"street","g":"…","p":"1-2","a":"water","s":"","at":…}]}
//	{"t":"home_hosted","guests":3}                                        to the owner in the house
//
// A tend proof is base64url(JSON) "." base64url(HMAC-SHA256(secret, the first
// part)), JSON {"k":"home_tend","sub":visitor,"o":owner,"acts":[…],"iat",
// "exp","n"}, good once (n).
//
// Ours: a visitor may leave a parcel only from the owner's home street (the
// source's Butler stood there and took drops in reach, proximity_override
// 550); a parcel the site has not answered in time is refused, so nothing
// moves twice (the site's op is idempotent).

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ledgerRouteButler = "/groddle/v1/ledger/butler"
	butlerMessageMax  = 431  // bag_butler.js:3762 input_max_chars, 5850 slice(0, 431)
	butlerCountMax    = 9999 // a stack
	butlerBurst       = 4
	butlerEvery       = 6 * time.Second
	tendBurst         = 20
	tendEvery         = 2 * time.Second
	homeTendLife      = 2 * time.Minute
	hostedThree       = 4  // locations/home.js:59 activePlayers.length >= 4
	hostedSeventeen   = 18 // locations/home.js:63 activePlayers.length >= 18

	butlerTextBusy = "The Butler is talking to somebody else right now." // include/butler_strings.js:165
	butlerTextOff  = "The Butler can't take anything right now."         // ours: no ledger to carry it
	butlerTextAway = "You need to be on their home street to leave something with their Butler."
	butlerTextBad  = "Leave a package or a message."
)

var butlerClassRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func init() { modules = append(modules, newHomeHelp) }

// butlerAnswer is the site's word on a parcel. Status "" means no answer came.
type butlerAnswer struct {
	Status string `json:"status"` // settled | refused
	Why    string `json:"why,omitempty"`
	Text   string `json:"text,omitempty"`
}

// butlerSite is the site's Butler (httpLedger). Calls block; the module makes them off the hub lock.
type butlerSite interface {
	butler(op string, from, to int64, gave tradeGoods, text string) butlerAnswer
}

func (l *httpLedger) butler(op string, from, to int64, gave tradeGoods, text string) butlerAnswer {
	body, _ := json.Marshal(map[string]any{"op": op, "from": from, "to": to, "gave": goodsJSON(gave), "text": text})
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * 400 * time.Millisecond)
		}
		a, err := l.butlerOnce(body)
		if err == nil {
			return a
		}
		log.Printf("butler: %s: %v", ledgerRouteButler, err)
	}
	return butlerAnswer{}
}

func (l *httpLedger) butlerOnce(body []byte) (butlerAnswer, error) {
	var nb [8]byte
	rand.Read(nb[:])
	req, err := http.NewRequest(http.MethodPost, l.base+"/wp-json"+ledgerRouteButler, bytes.NewReader(body))
	if err != nil {
		return butlerAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Groddle-Service", signService(l.secret, http.MethodPost, ledgerRouteButler, l.now().Unix(), hex.EncodeToString(nb[:]), body))
	res, err := l.client.Do(req)
	if err != nil {
		return butlerAnswer{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	var a butlerAnswer
	if (res.StatusCode == http.StatusOK || res.StatusCode == http.StatusConflict) && json.Unmarshal(raw, &a) == nil && (a.Status == "settled" || a.Status == "refused") {
		return a, nil
	}
	return butlerAnswer{}, fmt.Errorf("HTTP %d: %.200s", res.StatusCode, raw)
}

// homeHelpSecretForTest is the secret in tests (the flag is not parsed there).
var homeHelpSecretForTest []byte

type homeHelp struct {
	h      *Hub
	secret []byte
	site   butlerSite // nil: the Butler takes nothing (no -ledger-url, or -trade-ledger off)
	spawn  func(func())
	newID  func() string
	busy   map[int64]int64 // owner -> the visitor whose parcel the site is weighing
	spent  map[string]int64
	rates  map[string]*bucket
}

func newHomeHelp(h *Hub) Module {
	m := &homeHelp{h: h, secret: homeHelpSecretForTest, spawn: func(fn func()) { go fn() }, busy: map[int64]int64{}, spent: map[string]int64{}, rates: map[string]*bucket{},
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return hex.EncodeToString(b)
		}}
	if !testing.Testing() {
		path := ""
		if f := flag.Lookup("secret-file"); f != nil {
			path = expandHome(f.Value.String())
		}
		m.secret, _ = readSecret(path)
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.site = l
			}
		}
	}
	return m
}

func (m *homeHelp) Register(r *Registry) {
	r.Handle("butler_leave", m.leave)
	r.Handle("home_tend_tell", m.tendTell)
	r.OnJoin(m.joined)
	r.OnDisconnect(m.disconnected)
}

func (m *homeHelp) allow(kind string, cl *Client, burst float64, every time.Duration) bool {
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

func (m *homeHelp) blocked(owner, visitor int64) bool {
	fn, _ := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return fn != nil && fn(owner, visitor)
}

// cleanButlerText is takeMessage's text: no tags or control characters, one line, at most 431 characters.
func cleanButlerText(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>' && inTag:
			inTag = false
		case inTag:
		case unicode.IsControl(r) || unicode.IsSpace(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if utf8.RuneCountInString(out) > butlerMessageMax {
		out = string([]rune(out)[:butlerMessageMax])
	}
	return out
}

// —— The Butler ——

func (m *homeHelp) refuse(cl *Client, owner int64, why, text string, item any, message bool) {
	m.h.SendLocked(cl, map[string]any{"t": "butler_left", "owner": owner, "status": "refused", "why": why, "text": text, "item": item, "message": message})
}

// leave is leave_package / leave_message from a visitor on the owner's home street.
func (m *homeHelp) leave(cl *Client, frame json.RawMessage) {
	if !m.allow("butler", cl, butlerBurst, butlerEvery) {
		return
	}
	var msg struct {
		Owner int64 `json:"owner"`
		Item  *struct {
			C string `json:"c"`
			N int    `json:"n"`
		} `json:"item"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(frame, &msg)
	owner, visitor := msg.Owner, cl.ID()
	text := cleanButlerText(msg.Text)
	var item any
	gave := tradeGoods{Items: []tradeItem{}}
	if msg.Item != nil {
		if !butlerClassRe.MatchString(msg.Item.C) || msg.Item.N < 1 || msg.Item.N > butlerCountMax {
			m.refuse(cl, owner, "bad", butlerTextBad, nil, text != "")
			return
		}
		gave.Items = []tradeItem{{C: msg.Item.C, N: msg.Item.N}}
		item = map[string]any{"c": msg.Item.C, "n": msg.Item.N}
	}
	if owner <= 0 || owner == visitor || (item == nil && text == "") {
		m.refuse(cl, owner, "bad", butlerTextBad, item, text != "")
		return
	}
	if cl.Street() != homeStreetPrefix+strconv.FormatInt(owner, 10) {
		m.refuse(cl, owner, "away", butlerTextAway, item, text != "")
		return
	}
	if m.blocked(owner, visitor) {
		m.refuse(cl, owner, "blocked", homeTextBlocked, item, text != "")
		return
	}
	if who := m.busy[owner]; who != 0 {
		m.refuse(cl, owner, "busy", butlerTextBusy, item, text != "")
		return
	}
	if m.site == nil {
		m.refuse(cl, owner, "off", butlerTextOff, item, text != "")
		return
	}
	op := "butler-" + m.newID()
	m.busy[owner] = visitor
	site := m.site
	name := cl.Name()
	m.spawn(func() {
		a := site.butler(op, visitor, owner, gave, text)
		m.h.After(0, func() {
			if m.busy[owner] == visitor {
				delete(m.busy, owner)
			}
			status := a.Status
			if status == "" {
				// No answer: the op is idempotent at the site, but nobody here will ask again; say no.
				status, a.Why, a.Text = "refused", "off", butlerTextOff
			}
			if c := m.h.ClientLocked(visitor); c != nil {
				m.h.SendLocked(c, map[string]any{"t": "butler_left", "owner": owner, "op": op, "status": status, "why": a.Why, "text": a.Text, "item": item, "message": text != ""})
			}
			if status == "settled" {
				log.Printf("butler: %d left %s with %d's Butler (item %v, message %v)", visitor, op, owner, item != nil, text != "")
				if o := m.h.ClientLocked(owner); o != nil {
					m.h.SendLocked(o, map[string]any{"t": "butler_parcel", "from": map[string]any{"id": visitor, "name": name}, "item": item != nil, "message": text != ""})
				}
			}
		})
	})
}

// —— Tending ——

type tendClaims struct {
	K    string           `json:"k"`
	Sub  int64            `json:"sub"`
	O    int64            `json:"o"`
	Acts []map[string]any `json:"acts"`
	N    string           `json:"n"`
	Exp  int64            `json:"exp"`
}

// tendTell passes the site's word that a key-holder tended the owner's gardens to the owner and everyone there.
func (m *homeHelp) tendTell(cl *Client, frame json.RawMessage) {
	if !m.allow("tend", cl, tendBurst, tendEvery) {
		return
	}
	var msg struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(frame, &msg)
	var c tendClaims
	err := errBuddyOff
	if len(m.secret) > 0 {
		err = errBuddyToken
		if raw, e := signedBody(m.secret, msg.Token); e == nil && json.Unmarshal(raw, &c) == nil && c.K == "home_tend" && c.O > 0 && c.N != "" && len(c.Acts) > 0 && len(c.Acts) <= 8 {
			switch {
			case c.Sub != cl.ID():
				err = errBuddyOwner
			case c.Exp < m.h.Now().Unix():
				err = errBuddyExpired
			default:
				err = nil
			}
		}
	}
	if err != nil {
		m.h.ErrorLocked(cl, "homes", err.Error())
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
	exp := c.Exp
	if most := now + int64(homeTendLife.Seconds()); exp > most {
		exp = most
	}
	m.spent[c.N] = exp
	acts := make([]map[string]any, 0, len(c.Acts))
	for _, a := range c.Acts {
		act := map[string]any{}
		for _, k := range []string{"y", "g", "p", "a", "s", "at"} {
			if v, ok := a[k]; ok {
				act[k] = v
			}
		}
		acts = append(acts, act)
	}
	out := map[string]any{"t": "home_tended", "owner": c.O, "from": map[string]any{"id": cl.ID(), "name": cl.Name()}, "acts": acts}
	told := map[int64]bool{cl.ID(): true}
	if o := m.h.ClientLocked(c.O); o != nil {
		m.h.SendLocked(o, out)
		told[c.O] = true
	}
	id := strconv.FormatInt(c.O, 10)
	for _, street := range []string{homeStreetPrefix + id, houseStreetPrefix + id} {
		for _, o := range m.h.StreetLocked(street) {
			if !told[o.ID()] {
				told[o.ID()] = true
				m.h.SendLocked(o, out)
			}
		}
	}
}

// —— Hosting ——

// joined is locations/home.js:55-66: someone came into a house with its owner in it.
func (m *homeHelp) joined(cl *Client) {
	owner, inside := homeOwner(cl.Street())
	if !inside {
		return
	}
	o := m.h.ClientLocked(owner)
	if o == nil || o.Street() != cl.Street() {
		return
	}
	if n := len(m.h.StreetLocked(cl.Street())); n >= hostedThree {
		m.h.SendLocked(o, map[string]any{"t": "home_hosted", "guests": n - 1, "three": true, "seventeen": n >= hostedSeventeen})
	}
}

func (m *homeHelp) disconnected(cl *Client) {
	for owner, who := range m.busy {
		if who == cl.ID() {
			// The site still answers; the answer finds nobody to tell. Free the Butler now.
			delete(m.busy, owner)
		}
	}
}
