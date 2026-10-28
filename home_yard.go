package main

// Visitors' verbs on what grows and walks on a real player's home street
// (registers G328 and G201; homes.go is the visiting itself). In the original
// a home street was public: anyone standing on it could pet and nibble its
// Piggies, massage and milk its Butterflies, squeeze its Chickens, dig and
// tend its Patches and pet, water and harvest the trees in them (the
// ordinary verbs of those items: GameServerJS items/npc_piggy.js 818-960,
// npc_butterfly.js 695-800, npc_chicken.js 739, patch.js 120-380,
// trant_base 463-600, wood_tree.js 474-560; none of them asks for the owner
// or a key). Each act by someone who is not the owner added a fixed amount
// to the owner's Magic Rock pool (locations/inc_cultivation.js
// cultivation_add_img_rewards 168-178).
//
// This module checks the one thing the site cannot see: that the visitor
// stands on HOME_<owner> (the verbs' ok_states "in_location"; homes.go only
// lets a holder of the site's pass onto that street). The site decides the
// rest from the owner's stored save and its own tables (groddle-afternoon-site
// src/HomeYard.php, POST groddle/v1/homes/yard, signed as ledger.go signs):
// whether the thing is there and in a state for the verb, the visitor's
// daily limits, the pool's amount, the fruit a harvest takes. The visitor is
// answered; the owner (online anywhere) and everyone else at that home are
// told, so the owner's game applies it at once.
//
// Client to server:
//
//	{"t":"home_yard","req":3,"owner":7,"kind":"animal|plot|tree","id":"a3|patch:0","verb":"pet"}
//
// Server to client:
//
//	{"t":"home_yard_done","req":3,"owner":7,"status":"settled|refused","why":"…","text":"…","act":{…},"added":4}
//	{"t":"home_yard_acted","owner":7,"from":{"id":9,"name":"Bo"},"act":{"id":"yd-…","kind":"tree","target":"patch:1","verb":"water","took":0,"at":…}}
//
// Ours: an act the site has not answered in time is refused to the visitor,
// and its op is idempotent at the site, so nothing counts twice.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"testing"
	"time"
)

const (
	ledgerRouteHomeYard = "/groddle/v1/homes/yard"
	yardBurst           = 12
	yardEvery           = time.Second

	yardTextAway = "You need to be on their home street to do that."
	yardTextOff  = "That can't be done right now."
	yardTextBad  = "Nothing to do here."
)

var (
	yardAnimalRe = regexp.MustCompile(`^a[0-9]{1,6}$`)
	yardPlotRe   = regexp.MustCompile(`^patch(_dark)?:[0-9]{1,2}$`)
	yardVerbRe   = regexp.MustCompile(`^(pet|nibble|massage|milk|squeeze|dig|tend|water|harvest)$`)
)

func init() { modules = append(modules, newHomeYard) }

// yardAnswer is the site's word on an act. Status "" means no answer came.
type yardAnswer struct {
	Status string          `json:"status"` // settled | refused
	Why    string          `json:"why,omitempty"`
	Text   string          `json:"text,omitempty"`
	Added  int             `json:"added,omitempty"`
	Today  int             `json:"today,omitempty"`
	Again  bool            `json:"again,omitempty"`
	Act    json.RawMessage `json:"act,omitempty"`
}

type yardAct struct {
	Op      string `json:"op"`
	Owner   int64  `json:"owner"`
	Visitor int64  `json:"visitor"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Verb    string `json:"verb"`
}

// yardSite is the site's yard (httpLedger). Calls block; the module makes them off the hub lock.
type yardSite interface {
	homeYard(a yardAct) yardAnswer
}

func (l *httpLedger) homeYard(a yardAct) yardAnswer {
	body, _ := json.Marshal(a)
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * 400 * time.Millisecond)
		}
		out, err := l.homeYardOnce(body)
		if err == nil {
			return out
		}
		log.Printf("home yard: %s: %v", ledgerRouteHomeYard, err)
	}
	return yardAnswer{}
}

func (l *httpLedger) homeYardOnce(body []byte) (yardAnswer, error) {
	var nb [8]byte
	rand.Read(nb[:])
	req, err := http.NewRequest(http.MethodPost, l.base+"/wp-json"+ledgerRouteHomeYard, bytes.NewReader(body))
	if err != nil {
		return yardAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Groddle-Service", signService(l.secret, http.MethodPost, ledgerRouteHomeYard, l.now().Unix(), hex.EncodeToString(nb[:]), body))
	res, err := l.client.Do(req)
	if err != nil {
		return yardAnswer{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	var a yardAnswer
	if (res.StatusCode == http.StatusOK || res.StatusCode == http.StatusConflict) && json.Unmarshal(raw, &a) == nil && (a.Status == "settled" || a.Status == "refused") {
		return a, nil
	}
	return yardAnswer{}, fmt.Errorf("HTTP %d: %.200s", res.StatusCode, raw)
}

type homeYard struct {
	h     *Hub
	site  yardSite // nil: no visitor verbs (no -ledger-url, or -trade-ledger off)
	spawn func(func())
	newID func() string
	rates map[int64]*bucket
}

func newHomeYard(h *Hub) Module {
	m := &homeYard{h: h, spawn: func(fn func()) { go fn() }, rates: map[int64]*bucket{},
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return hex.EncodeToString(b)
		}}
	if !testing.Testing() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.site = l
			}
		}
	}
	return m
}

func (m *homeYard) Register(r *Registry) {
	r.Handle("home_yard", m.act)
	r.OnDisconnect(func(cl *Client) { delete(m.rates, cl.ID()) })
}

func (m *homeYard) refuse(cl *Client, req any, owner int64, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "home_yard_done", "req": req, "owner": owner, "status": "refused", "why": why, "text": text})
}

func (m *homeYard) blocked(owner, visitor int64) bool {
	fn, _ := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return fn != nil && (fn(owner, visitor) || fn(visitor, owner))
}

// act is one verb by a visitor on the owner's home street.
func (m *homeYard) act(cl *Client, frame json.RawMessage) {
	b := m.rates[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.rates[cl.ID()] = b
	}
	if !b.take(m.h.Now(), yardBurst, yardEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	var msg struct {
		Req   any    `json:"req"`
		Owner int64  `json:"owner"`
		Kind  string `json:"kind"`
		ID    string `json:"id"`
		Verb  string `json:"verb"`
	}
	_ = json.Unmarshal(frame, &msg)
	owner, visitor := msg.Owner, cl.ID()
	req := msg.Req
	if f, ok := req.(float64); !ok || f < 0 || f > 1e9 {
		req = nil
	}
	okID := (msg.Kind == "animal" && yardAnimalRe.MatchString(msg.ID)) || ((msg.Kind == "plot" || msg.Kind == "tree") && yardPlotRe.MatchString(msg.ID))
	if owner <= 0 || owner == visitor || !okID || !yardVerbRe.MatchString(msg.Verb) {
		m.refuse(cl, req, owner, "bad", yardTextBad)
		return
	}
	if cl.Street() != homeStreetPrefix+strconv.FormatInt(owner, 10) {
		m.refuse(cl, req, owner, "away", yardTextAway)
		return
	}
	if m.blocked(owner, visitor) {
		m.refuse(cl, req, owner, "blocked", homeTextBlocked)
		return
	}
	if m.site == nil {
		m.refuse(cl, req, owner, "off", yardTextOff)
		return
	}
	a := yardAct{Op: "yd-" + m.newID(), Owner: owner, Visitor: visitor, Kind: msg.Kind, ID: msg.ID, Verb: msg.Verb}
	site := m.site
	name := cl.Name()
	m.spawn(func() {
		ans := site.homeYard(a)
		m.h.After(0, func() {
			if ans.Status == "" {
				ans = yardAnswer{Status: "refused", Why: "off", Text: yardTextOff}
			}
			var act any
			if len(ans.Act) > 0 && string(ans.Act) != "null" {
				act = ans.Act
			}
			if c := m.h.ClientLocked(visitor); c != nil {
				m.h.SendLocked(c, map[string]any{"t": "home_yard_done", "req": req, "owner": owner, "status": ans.Status, "why": ans.Why, "text": ans.Text, "act": act, "added": ans.Added})
			}
			if ans.Status != "settled" || act == nil {
				return
			}
			log.Printf("home yard: %d %s %s %s on %d's home street (%s, pool +%d)", visitor, a.Verb, a.Kind, a.ID, owner, a.Op, ans.Added)
			out := map[string]any{"t": "home_yard_acted", "owner": owner, "from": map[string]any{"id": visitor, "name": name}, "act": act}
			told := map[int64]bool{visitor: true}
			if o := m.h.ClientLocked(owner); o != nil {
				told[owner] = true
				m.h.SendLocked(o, out)
			}
			id := strconv.FormatInt(owner, 10)
			for _, street := range []string{homeStreetPrefix + id, houseStreetPrefix + id} {
				for _, o := range m.h.StreetLocked(street) {
					if !told[o.ID()] {
						told[o.ID()] = true
						m.h.SendLocked(o, out)
					}
				}
			}
		})
	})
}
