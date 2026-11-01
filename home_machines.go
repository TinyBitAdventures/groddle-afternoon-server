package main

// Machines set down on a home street (lane MACHINES; Austin 2026-11-01: "Let
// owners set an assembled machine down on their street, as the original
// did."). A Fuelmaker, Blockmaker, Metal Machine or Woodworker an owner puts
// down stands there for everyone on the street: anyone may use it, refuel it
// and collect what they made, only the owner or a key-holder may pick it up
// (GameServerJS machine_base, fuelmaker.js verbs.use 633-684, refuel
// 442-558, collect 560-631, disassemble 296-349), and a job finished by
// someone who is not the owner adds 7 to the owner's Magic Rock pool
// (players/inc_making.js:836).
//
// This module checks what the site cannot see: that the actor stands in the
// owner's home, on HOME_<owner>, in HOUSE_<owner> or on a floor of their
// Tower, TOWER_<owner>_<floor> (the verbs' ok_states "in_location"; homes.go
// lets only the owner, holders of the site's pass and, in the house, a
// key-holder or someone let in), that only the owner sets one down, and that
// neither blocks the other. It names the place to the site ("street",
// "house", "tower:<floor>"; lane HOMES2, Austin 2026-11-01: "Machines may
// stand on the home street, in the house and in the Tower"), so a machine is
// used only by someone standing where it stands. The site decides the rest from the
// machine's row, the recipe data and the actor's ledger and stored save
// (groddle-afternoon-site src/HomeMachines.php, POST groddle/v1/homes/machines,
// signed as ledger.go signs): whether it is free, the fuel, the ingredients
// and Fuel Cells it takes from the actor's ledger, the output it hands over,
// the pool. The actor is answered; everyone else on that home street and the
// owner (online anywhere) are told how the machine stands now.
//
// Client to server:
//
//	{"t":"home_machine","req":3,"owner":7,"verb":"place","class":"fuelmaker","rx":120,"fuel":50}
//	{"t":"home_machine","req":4,"owner":7,"verb":"use","id":"hm-…","key":"fuel_cell","qty":2}
//	{"t":"home_machine","req":5,"owner":7,"verb":"refuel|collect|take|music|check","id":"hm-…","all":true,"force":true,"on":false}
//
// Server to client:
//
//	{"t":"home_machine_done","req":3,"owner":7,"verb":"use","op":"hm-…","status":"settled|refused","why":"…","text":"…","machine":{…},"gone":"hm-…","receipt":{…},"taken":{…},"now":ms}
//	{"t":"home_machine_changed","owner":7,"verb":"use","by":{"id":9,"name":"Bo"},"machine":{…},"gone":"hm-…","paid":[…],"now":ms}
//
// Ours: an act the site has not answered in time is refused to the actor;
// the site settles each op once, so asking again never counts twice.

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
	"strings"
	"testing"
	"time"
)

const (
	ledgerRouteHomeMachines = "/groddle/v1/homes/machines"
	machineBurst            = 10
	machineEvery            = time.Second
	machineRxMax            = 20000 // HomeMachines::RX_MAX

	machineTextAway = "You need to be in their home to do that."
	machineTextOwn  = "Only the owner can set a machine down here."
	machineTextOff  = "That can't be done right now."
	machineTextBad  = "Nothing to do here."
)

var (
	machineIDRe    = regexp.MustCompile(`^hm-[0-9a-f]{16}$`)
	machineClassRe = regexp.MustCompile(`^(fuelmaker|blockmaker|metalmaker|woodworker)$`)
	machineKeyRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}$`)
	machineVerbs   = map[string]bool{"place": true, "take": true, "use": true, "collect": true, "refuel": true, "music": true, "check": true}
)

func init() { modules = append(modules, newHomeMachines) }

// machineAnswer is the site's word on an act. Status "" means no answer came.
type machineAnswer struct {
	Status  string          `json:"status"` // settled | refused
	Why     string          `json:"why,omitempty"`
	Text    string          `json:"text,omitempty"`
	Machine json.RawMessage `json:"machine,omitempty"`
	Gone    string          `json:"gone,omitempty"`
	Receipt json.RawMessage `json:"receipt,omitempty"`
	Taken   json.RawMessage `json:"taken,omitempty"`
	Paid    json.RawMessage `json:"paid,omitempty"`
	Cells   int             `json:"cells,omitempty"`
	Now     int64           `json:"now,omitempty"`
}

type machineAct struct {
	Op    string `json:"op"`
	Owner int64  `json:"owner"`
	Actor int64  `json:"actor"`
	Place string `json:"place"`
	Verb  string `json:"verb"`
	ID    string `json:"id,omitempty"`
	Class string `json:"class,omitempty"`
	Rx    *int   `json:"rx,omitempty"`
	Fuel  *int   `json:"fuel,omitempty"`
	Key   string `json:"key,omitempty"`
	Qty   int    `json:"qty,omitempty"`
	All   bool   `json:"all,omitempty"`
	Force bool   `json:"force,omitempty"`
	On    *bool  `json:"on,omitempty"`
}

// machineSite is the site's machines (httpLedger). Calls block; the module makes them off the hub lock.
type machineSite interface {
	homeMachine(a machineAct) machineAnswer
}

func (l *httpLedger) homeMachine(a machineAct) machineAnswer {
	body, _ := json.Marshal(a)
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * 400 * time.Millisecond)
		}
		out, err := l.homeMachineOnce(body)
		if err == nil {
			return out
		}
		log.Printf("home machines: %s: %v", ledgerRouteHomeMachines, err)
	}
	return machineAnswer{}
}

func (l *httpLedger) homeMachineOnce(body []byte) (machineAnswer, error) {
	var nb [8]byte
	rand.Read(nb[:])
	req, err := http.NewRequest(http.MethodPost, l.base+"/wp-json"+ledgerRouteHomeMachines, bytes.NewReader(body))
	if err != nil {
		return machineAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Groddle-Service", signService(l.secret, http.MethodPost, ledgerRouteHomeMachines, l.now().Unix(), hex.EncodeToString(nb[:]), body))
	res, err := l.client.Do(req)
	if err != nil {
		return machineAnswer{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	var a machineAnswer
	if (res.StatusCode == http.StatusOK || res.StatusCode == http.StatusConflict) && json.Unmarshal(raw, &a) == nil && (a.Status == "settled" || a.Status == "refused") {
		return a, nil
	}
	return machineAnswer{}, fmt.Errorf("HTTP %d: %.200s", res.StatusCode, raw)
}

type homeMachines struct {
	h     *Hub
	site  machineSite // nil: no machines (no -ledger-url, or -trade-ledger off)
	spawn func(func())
	newID func() string
	rates map[int64]*bucket
}

func newHomeMachines(h *Hub) Module {
	m := &homeMachines{h: h, spawn: func(fn func()) { go fn() }, rates: map[int64]*bucket{},
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

func (m *homeMachines) Register(r *Registry) {
	r.Handle("home_machine", m.act)
	r.OnDisconnect(func(cl *Client) { delete(m.rates, cl.ID()) })
}

func (m *homeMachines) refuse(cl *Client, req any, owner int64, verb, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "home_machine_done", "req": req, "owner": owner, "verb": verb, "status": "refused", "why": why, "text": text})
}

func (m *homeMachines) blocked(owner, actor int64) bool {
	fn, _ := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return fn != nil && (fn(owner, actor) || fn(actor, owner))
}

// act is one verb on a machine on the owner's home street.
func (m *homeMachines) act(cl *Client, frame json.RawMessage) {
	b := m.rates[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.rates[cl.ID()] = b
	}
	if !b.take(m.h.Now(), machineBurst, machineEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	var msg struct {
		Req   any    `json:"req"`
		Owner int64  `json:"owner"`
		Verb  string `json:"verb"`
		ID    string `json:"id"`
		Class string `json:"class"`
		Rx    *int   `json:"rx"`
		Fuel  *int   `json:"fuel"`
		Key   string `json:"key"`
		Qty   int    `json:"qty"`
		All   bool   `json:"all"`
		Force bool   `json:"force"`
		On    *bool  `json:"on"`
	}
	_ = json.Unmarshal(frame, &msg)
	owner, actor := msg.Owner, cl.ID()
	req := msg.Req
	if f, ok := req.(float64); !ok || f < 0 || f > 1e9 {
		req = nil
	}
	ok := owner > 0 && machineVerbs[msg.Verb]
	switch msg.Verb {
	case "place":
		ok = ok && machineClassRe.MatchString(msg.Class) && msg.Rx != nil && *msg.Rx >= -machineRxMax && *msg.Rx <= machineRxMax && (msg.Fuel == nil || (*msg.Fuel >= 0 && *msg.Fuel <= 200))
	case "use":
		ok = ok && machineIDRe.MatchString(msg.ID) && machineKeyRe.MatchString(msg.Key) && msg.Qty >= 1 && msg.Qty <= 999
	default:
		ok = ok && machineIDRe.MatchString(msg.ID)
	}
	if !ok {
		m.refuse(cl, req, owner, msg.Verb, "bad", machineTextBad)
		return
	}
	place, street := machinePlace(cl.Street(), owner)
	if place == "" {
		m.refuse(cl, req, owner, msg.Verb, "away", machineTextAway)
		return
	}
	if msg.Verb == "place" && owner != actor {
		m.refuse(cl, req, owner, msg.Verb, "own", machineTextOwn)
		return
	}
	if owner != actor && m.blocked(owner, actor) {
		m.refuse(cl, req, owner, msg.Verb, "blocked", homeTextBlocked)
		return
	}
	if m.site == nil {
		m.refuse(cl, req, owner, msg.Verb, "off", machineTextOff)
		return
	}
	a := machineAct{Op: "hm-" + m.newID(), Owner: owner, Actor: actor, Place: place, Verb: msg.Verb, ID: msg.ID, Class: msg.Class, Rx: msg.Rx, Fuel: msg.Fuel, Key: msg.Key, Qty: msg.Qty, All: msg.All, Force: msg.Force, On: msg.On}
	// Each verb carries only what it needs.
	if msg.Verb != "place" {
		a.Class, a.Rx, a.Fuel = "", nil, nil
	}
	if msg.Verb != "use" {
		a.Key, a.Qty = "", 0
	}
	if msg.Verb != "refuel" {
		a.All = false
	}
	if msg.Verb != "take" {
		a.Force = false
	}
	if msg.Verb != "music" {
		a.On = nil
	}
	site := m.site
	name := cl.Name()
	m.spawn(func() {
		ans := site.homeMachine(a)
		m.h.After(0, func() { m.answered(a, street, req, name, ans) })
	})
}

// machinePlace names where in owner's home a street is: "street" (HOME_<owner>), "house"
// (HOUSE_<owner>) or "tower:<floor>" (TOWER_<owner>_<floor>); "" anywhere else.
func machinePlace(street string, owner int64) (string, string) {
	if t := towerOwner(street); t > 0 {
		if t != owner {
			return "", ""
		}
		_, floor, _ := strings.Cut(strings.TrimPrefix(street, towerStreetPrefix), "_")
		return "tower:" + floor, street
	}
	o, house := homeOwner(street)
	if o != owner || o == 0 {
		return "", ""
	}
	if house {
		return "house", street
	}
	return "street", street
}

func machineRaw(j json.RawMessage) any {
	if len(j) == 0 || string(j) == "null" {
		return nil
	}
	return j
}

func (m *homeMachines) answered(a machineAct, street string, req any, name string, ans machineAnswer) {
	if ans.Status == "" {
		ans = machineAnswer{Status: "refused", Why: "off", Text: machineTextOff}
	}
	if c := m.h.ClientLocked(a.Actor); c != nil {
		out := map[string]any{"t": "home_machine_done", "req": req, "owner": a.Owner, "verb": a.Verb, "op": a.Op, "status": ans.Status,
			"why": ans.Why, "text": ans.Text, "machine": machineRaw(ans.Machine), "receipt": machineRaw(ans.Receipt), "taken": machineRaw(ans.Taken), "now": ans.Now}
		if ans.Gone != "" {
			out["gone"] = ans.Gone
		}
		if ans.Cells > 0 {
			out["cells"] = ans.Cells
		}
		m.h.SendLocked(c, out)
	}
	paid := machineRaw(ans.Paid)
	if string(ans.Paid) == "[]" {
		paid = nil
	}
	// A refusal changes nothing anyone sees, unless the site settled a finished job on the way.
	if ans.Status != "settled" && paid == nil {
		return
	}
	if ans.Status == "settled" {
		log.Printf("home machines: %d %s %s in %d's home, %s (%s)", a.Actor, a.Verb, a.ID, a.Owner, a.Place, a.Op)
	}
	out := map[string]any{"t": "home_machine_changed", "owner": a.Owner, "verb": a.Verb, "by": map[string]any{"id": a.Actor, "name": name}, "machine": machineRaw(ans.Machine), "paid": paid, "now": ans.Now}
	if ans.Status != "settled" {
		out["verb"] = "check"
	}
	if ans.Gone != "" {
		out["gone"] = ans.Gone
	}
	told := map[int64]bool{a.Actor: true}
	if o := m.h.ClientLocked(a.Owner); o != nil && !told[a.Owner] {
		told[a.Owner] = true
		m.h.SendLocked(o, out)
	}
	for _, o := range m.h.StreetLocked(street) {
		if !told[o.ID()] {
			told[o.ID()] = true
			m.h.SendLocked(o, out)
		}
	}
}
