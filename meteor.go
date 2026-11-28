package main

// Meteor showers and lone shooting stars in the night sky (Austin 2026-11-27; the game's
// js/meteor-shower.js and features/meteor-shower-feature.js). Ours: Glitch had neither (the game's
// inventions register, meteor-shower).
//
// Never on a timer: an administrator starts one from the site's console (groddle-afternoon-site
// src/AdminSky.php), which makes the signed call meteor (admin.go's X-Groddle-Service, POST
// /ws?call=meteor). The server picks the event's seed and length and sends it to every game socket
// online; each game draws the same streaks from the seed at the same moment on this server's clock
// (the frame carries now). One event at a time; it is kept in memory only (a restart ends it), and
// a game asks for it as it comes online (meteor_sync), so a player who connects mid-shower joins it
// in progress, and one who connects in the two minutes before a hinted shower gets the hint too.
//
//	meteor {"op":"shower","lead":0|120}   200 {"status":"started","event":Event,"players":n} | {"status":"busy","event":Event}
//	meteor {"op":"star"}                  200 the same, a lone star (no lead)
//	meteor {"op":"stop"}                  200 {"status":"stopped","event":Event,"players":n} | {"status":"none"}
//	meteor {"op":"status"}                200 {"status":"ok","event":Event|null,"now":ms}
//	any other body                        400 {"status":"bad"}
//
//	Event: {"id":"mt-<16 hex>","kind":"shower"|"star","seed":1..2^31-2,"start":ms,"dur":ms,"hint":bool}
//	lead (seconds, 0..meteorLeadMax): the shower starts that long after the call, and hint is true
//	(the game's activity feed says the sky feels restless as the frame comes).
//
// Server to the game (game sockets only, never the website's):
//
//	{"t":"meteor","id":…,"kind":"shower","seed":…,"start":ms,"dur":ms,"hint":true,"now":ms}   started, and to meteor_sync while one is on or coming
//	{"t":"meteor","id":…,"kind":"stop","now":ms}                                                an administrator stopped it
//
// Client to server:
//
//	{"t":"meteor_sync"}   asks for the event on or coming (nothing when there is none)

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

const (
	// A shower's length, picked in this range (js/meteor-shower.js METEOR.showerMinMs, showerMaxMs).
	meteorShowerMin = 180 * time.Second
	meteorShowerMax = 240 * time.Second
	// A lone star's event (METEOR.starDurMs): its one streak and the fade.
	meteorStarDur = 6 * time.Second
	// The furthest ahead a shower may be started (the console offers 2 minutes).
	meteorLeadMax = 300
	// A finished event is still answered to meteor_sync this long, for a streak's last fade.
	meteorTail = 4 * time.Second
	// The seed's range (Park-Miller, js/sky-clock.js starRandom).
	meteorSeedMax = 2147483646
)

type meteorEvent struct {
	ID    string
	Kind  string
	Seed  int64
	Start time.Time
	Dur   time.Duration
	Hint  bool
}

func (e *meteorEvent) out() map[string]any {
	return map[string]any{"id": e.ID, "kind": e.Kind, "seed": e.Seed, "start": e.Start.UnixMilli(), "dur": e.Dur.Milliseconds(), "hint": e.Hint}
}

func (e *meteorEvent) frame(now time.Time) map[string]any {
	f := e.out()
	f["t"] = "meteor"
	f["now"] = now.UnixMilli()
	return f
}

type meteors struct {
	h   *Hub
	now *meteorEvent
	// random fills b (crypto/rand; a test makes it fixed).
	random func(b []byte)
}

func init() {
	modules = append(modules, newMeteors)
	serviceHandlers["meteor"] = adminMeteor
}

func newMeteors(h *Hub) Module {
	return &meteors{h: h, random: func(b []byte) { _, _ = rand.Read(b) }}
}

func (m *meteors) Register(r *Registry) {
	r.Provide("meteor.self", m)
	r.Handle("meteor_sync", func(cl *Client, _ json.RawMessage) { m.sync(cl) })
}

func (h *Hub) meteorsOf() *meteors {
	m, _ := h.provided["meteor.self"].(*meteors)
	return m
}

// current is the event on or coming, nil once it (and its last fade) is over.
func (m *meteors) current() *meteorEvent {
	e := m.now
	if e != nil && !m.h.now().Before(e.Start.Add(e.Dur+meteorTail)) {
		m.now = nil
		return nil
	}
	return e
}

func (m *meteors) sync(cl *Client) {
	if cl == nil || cl.web {
		return
	}
	if e := m.current(); e != nil {
		m.h.SendLocked(cl, e.frame(m.h.now()))
	}
}

// everyone sends msg to every game socket online and says how many.
func (m *meteors) everyone(msg map[string]any) int {
	b, _ := json.Marshal(msg)
	n := 0
	for _, cl := range m.h.clients {
		if !cl.web && !cl.closed {
			m.h.pushLocked(cl, b)
			n++
		}
	}
	return n
}

func (m *meteors) start(kind string, lead int) *meteorEvent {
	var b [12]byte
	m.random(b[:])
	seed := int64(binary.BigEndian.Uint32(b[8:12])%meteorSeedMax) + 1
	dur := meteorStarDur
	if kind == "shower" {
		span := uint32((meteorShowerMax - meteorShowerMin) / time.Second)
		dur = meteorShowerMin + time.Duration(binary.BigEndian.Uint32(b[0:4])%(span+1))*time.Second
	}
	now := m.h.now()
	e := &meteorEvent{ID: "mt-" + hex.EncodeToString(b[:8]), Kind: kind, Seed: seed, Start: now.Add(time.Duration(lead) * time.Second), Dur: dur, Hint: lead > 0}
	m.now = e
	return e
}

func adminMeteor(h *Hub, body []byte) (int, any) {
	var c struct {
		Op   string `json:"op"`
		Lead *int   `json:"lead"`
	}
	if json.Unmarshal(body, &c) != nil {
		return http.StatusBadRequest, adminBad
	}
	lead := 0
	if c.Lead != nil {
		lead = *c.Lead
	}
	if lead < 0 || lead > meteorLeadMax || (c.Op == "star" && lead != 0) {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.meteorsOf()
	if m == nil {
		return http.StatusBadRequest, adminBad
	}
	switch c.Op {
	case "status":
		var ev any
		if e := m.current(); e != nil {
			ev = e.out()
		}
		return http.StatusOK, map[string]any{"status": "ok", "event": ev, "now": h.now().UnixMilli()}
	case "shower", "star":
		if e := m.current(); e != nil {
			return http.StatusOK, map[string]any{"status": "busy", "event": e.out()}
		}
		e := m.start(c.Op, lead)
		n := m.everyone(e.frame(h.now()))
		log.Printf("meteor: an administrator started a %s (%s, seed %d, %ds, in %ds) for %d online", e.Kind, e.ID, e.Seed, int(e.Dur/time.Second), lead, n)
		return http.StatusOK, map[string]any{"status": "started", "event": e.out(), "players": n}
	case "stop":
		e := m.current()
		if e == nil {
			return http.StatusOK, statusOf("none")
		}
		m.now = nil
		n := m.everyone(map[string]any{"t": "meteor", "id": e.ID, "kind": "stop", "now": h.now().UnixMilli()})
		log.Printf("meteor: an administrator stopped %s (%s) for %d online", e.ID, e.Kind, n)
		return http.StatusOK, map[string]any{"status": "stopped", "event": e.out(), "players": n}
	}
	return http.StatusBadRequest, adminBad
}
