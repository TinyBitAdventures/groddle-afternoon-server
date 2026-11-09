package main

// Door keys lying on their streets, shared (#157, Austin 2026-11-09: "shared key, 5 minutes").
// Each keyed door's key lay on the ground of its street, or a street near it, and came back a
// while after someone took it: the December 2012 location files list a `spawner` item on each of
// those streets (GameServerJS items/spawner.js 22-32: spawn class, interval, max count; their
// settings were lost). Online there is ONE key on each spot for everyone on the street: the first
// to pick it up has it, it is gone for everyone, and it comes back 5 minutes after the pickup.
//
// The spots (street, feet x/y in the street's screen space, key id) are the game's table
// (groddle-afternoon public/js/door-key-spawns.js KEY_SPAWNS, from the fan wiki and forum), in
// data/door-keys.json written by its tools/build-realtime-door-keys.mjs; a spot's id is its index
// there. The pickup reach is the source's verb_radius, 800 px (config_prod.js:37, as drops.go). A
// pickup is credited to the site's ledger as an item taken from the street (credits.go "take"), so
// the key in the taker's pack counts outside the earning budget.
//
// What lies where is kept in memory only: after a restart every key is back.
//
// Client to server:
//
//	{"t":"key_sync","street":"GCR…"}               after a join: the keys on this street, please
//	{"t":"key_take","street":"GCR…","i":4}         Pick up on spot 4's key
//
// Server to client:
//
//	{"t":"keys","street":"GCR…","keys":[{"i":4,"key":4,"x":300,"y":1263,"here":true}]}
//	    the answer to key_sync ("keys":[] for a street with none); "back": ms until a taken key returns
//	{"t":"key_got","street":"GCR…","i":4,"key":4}  to the taker: door_key_4 goes into the pack
//	{"t":"key_taken","street":"GCR…","i":4,"id":7,"name":"Ada"}   to everyone else on the street
//	{"t":"key_back","street":"GCR…","i":4}          to everyone on the street: it lies there again
//	{"t":"key_refused","street":"GCR…","i":4,"why":"gone|far|street|unknown|slow"}
//
// QA: GRODDLE_KEYS_RESPAWN=<seconds> (env, lane servers only) shortens the respawn for a test; the
// shipped value is the data's 300.

import (
	_ "embed"
	"encoding/json"
	"log"
	"math"
	"os"
	"strconv"
	"time"
)

//go:embed data/door-keys.json
var doorKeysJSON []byte

const (
	keyReach       = 800.0 // config_prod.js:37 verb_radius, as drops.go dropReach
	keyTakesPerSec = 4
)

type keySpot struct {
	Street string
	X, Y   float64
	Key    int
}

type keyData struct {
	Respawn int `json:"respawn"`
	spots   []keySpot
	byStr   map[string][]int
}

func parseKeyData(b []byte) (*keyData, error) {
	var raw struct {
		Respawn int                  `json:"respawn"`
		Spots   [][4]json.RawMessage `json:"spots"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	d := &keyData{Respawn: raw.Respawn, byStr: map[string][]int{}}
	for _, r := range raw.Spots {
		var s keySpot
		if json.Unmarshal(r[0], &s.Street) != nil || json.Unmarshal(r[1], &s.X) != nil || json.Unmarshal(r[2], &s.Y) != nil || json.Unmarshal(r[3], &s.Key) != nil {
			continue
		}
		d.byStr[s.Street] = append(d.byStr[s.Street], len(d.spots))
		d.spots = append(d.spots, s)
	}
	return d, nil
}

type keyTaken struct {
	stop func() bool
	due  time.Time
}

type keysModule struct {
	h       *Hub
	data    *keyData
	respawn time.Duration
	// gone[i]: spot i's key was taken; it comes back at due.
	gone map[int]keyTaken
	// takes: per player, pickups asked in the current second.
	takes map[int64]*keyRate
}

type keyRate struct {
	window time.Time
	n      int
}

func init() { modules = append(modules, newKeys) }

func newKeys(h *Hub) Module {
	d, err := parseKeyData(doorKeysJSON)
	if err != nil {
		panic("realtime: data/door-keys.json: " + err.Error())
	}
	return newKeysWith(h, d)
}

func newKeysWith(h *Hub, d *keyData) *keysModule {
	respawn := time.Duration(d.Respawn) * time.Second
	if respawn <= 0 {
		respawn = 5 * time.Minute
	}
	if s, err := strconv.Atoi(os.Getenv("GRODDLE_KEYS_RESPAWN")); err == nil && s > 0 {
		respawn = time.Duration(s) * time.Second
		log.Printf("keys: GRODDLE_KEYS_RESPAWN: door keys come back %v after a pickup (QA)", respawn)
	}
	return &keysModule{h: h, data: d, respawn: respawn, gone: map[int]keyTaken{}, takes: map[int64]*keyRate{}}
}

func (m *keysModule) Register(r *Registry) {
	r.Handle("key_sync", m.sync)
	r.Handle("key_take", m.take)
	r.OnDisconnect(func(cl *Client) { delete(m.takes, cl.ID()) })
}

func (m *keysModule) sync(cl *Client, frame json.RawMessage) {
	var msg struct {
		Street string `json:"street"`
	}
	if json.Unmarshal(frame, &msg) != nil || cl.Street() == "" || msg.Street != cl.Street() {
		return
	}
	if m.slow(cl) {
		return
	}
	now := m.h.Now()
	list := []map[string]any{}
	for _, i := range m.data.byStr[msg.Street] {
		s := m.data.spots[i]
		k := map[string]any{"i": i, "key": s.Key, "x": s.X, "y": s.Y, "here": true}
		if g, ok := m.gone[i]; ok {
			k["here"] = false
			k["back"] = max(int64(0), g.due.Sub(now).Milliseconds())
		}
		list = append(list, k)
	}
	m.h.SendLocked(cl, map[string]any{"t": "keys", "street": msg.Street, "keys": list})
}

// slow counts a request against the player's per-second allowance.
func (m *keysModule) slow(cl *Client) bool {
	now := m.h.Now()
	r := m.takes[cl.ID()]
	if r == nil {
		r = &keyRate{}
		m.takes[cl.ID()] = r
	}
	if now.Sub(r.window) >= time.Second {
		r.window, r.n = now, 0
	}
	r.n++
	return r.n > keyTakesPerSec
}

func (m *keysModule) refuse(cl *Client, street string, i int, why string) {
	m.h.SendLocked(cl, map[string]any{"t": "key_refused", "street": street, "i": i, "why": why})
}

func (m *keysModule) take(cl *Client, frame json.RawMessage) {
	var msg struct {
		Street string `json:"street"`
		I      *int   `json:"i"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.I == nil || !streetRe.MatchString(msg.Street) {
		m.h.ErrorLocked(cl, "bad_key", "That key pickup did not make sense.")
		return
	}
	i := *msg.I
	if m.slow(cl) {
		m.refuse(cl, msg.Street, i, "slow")
		return
	}
	street := cl.Street()
	if street == "" || msg.Street != street {
		m.refuse(cl, msg.Street, i, "street")
		return
	}
	if i < 0 || i >= len(m.data.spots) || m.data.spots[i].Street != street {
		m.refuse(cl, street, i, "unknown")
		return
	}
	if _, gone := m.gone[i]; gone {
		m.refuse(cl, street, i, "gone")
		return
	}
	s := m.data.spots[i]
	px, py := cl.Pos()
	if math.Hypot(s.X-px, s.Y-py) > keyReach {
		m.refuse(cl, street, i, "far")
		return
	}
	// First to ask has it: gone for everyone, back after the respawn.
	due := m.h.Now().Add(m.respawn)
	m.gone[i] = keyTaken{due: due, stop: m.h.After(m.respawn, func() { m.back(i) })}
	m.h.SendLocked(cl, map[string]any{"t": "key_got", "street": street, "i": i, "key": s.Key})
	m.h.BroadcastStreetLocked(street, map[string]any{"t": "key_taken", "street": street, "i": i, "id": cl.ID(), "name": cl.Name()}, cl.ID())
	creditLocked(m.h, cl.ID(), "take", creditItem("door_key_"+strconv.Itoa(s.Key), 1))
}

func (m *keysModule) back(i int) {
	if _, ok := m.gone[i]; !ok {
		return
	}
	delete(m.gone, i)
	street := m.data.spots[i].Street
	m.h.BroadcastStreetLocked(street, map[string]any{"t": "key_back", "street": street, "i": i}, 0)
}
