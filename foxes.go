package main

// Fox Brushing Preserves between players (register G329; the fox_flurrier badge,
// G134). In solo each game runs its own fox. On a preserve shared by real
// players the server owns the foxes and the baits, so everyone there sees the
// same number and the badges that count them are decided here:
//
//   - locations/inc_npcs.js:130-160 spawn_fox: max_foxes = active players / 3
//     (a float: one player gets one fox, a second fox needs four players, a
//     fourth needs ten); no spawn while that many foxes are there or a spawn
//     is already due. A fox that appears while more than two are there
//     (num_foxes > 2, counted before it) counts fox / three_or_more for every
//     player on the street (154), which grants Fox Flurrier
//     (achievements/fox_flurrier.js:17-22: "With 9 Glitches, witnessed the
//     simultaneous presence of three foxes in one location").
//   - items/fox_bait.js ignite: a spawn 4 s later (117); more than four placed
//     baits within 400 px refuse it ("There is already too much bait around
//     here. It will confuse the foxes."); locations/location.js:4347-4362
//     dropFoxBaits: three baits, each gone after 5 minutes.
//   - items/npc_fox.js endBrushing 425-434: a fox leaves once three players
//     have brushed it, after 21 brushes, or with no bait left ("The fox
//     leaves..."), and with bait still about another one comes 10 s later;
//     startMoving with no bait at all: "No more bait remains, so the Fox
//     leaves..."; a brush within 2 s of your last one does nothing (212).
//
// Each player's game still runs the foxes along the street with the solo rules
// (js/foxes.js), reports when a fox it draws reaches a bait (the first report
// eats it for everyone) and applies its own brush rewards; the server counts
// brushes and brushers. Constants and the preserve list are data/foxes.json,
// written by the game's tools/build-realtime-foxes.mjs.
//
// Client to server:
//
//	{"t":"fox_ignite","baits":[x,x,x]}       you ignited a Fox Bait; where its three baits fall
//	{"t":"fox_brush","fox":"…"}              you brushed this fox
//	{"t":"fox_eat","fox":"…","bait":"…"}     on your screen this fox reached that bait
//
// Server to client:
//
//	{"t":"fox_state","foxes":[{"id":"…","leaving":false}],"baits":[{"id":"…","x":0}]}  on joining a preserve
//	{"t":"fox_baits","by":7,"baits":[{"id":"…","x":0}]}   three baits fall
//	{"t":"fox_bait_gone","bait":"…"}         eaten or gone stale
//	{"t":"fox_spawn","fox":"…","n":2,"text":"A fox appears!"}
//	{"t":"fox_leave","fox":"…","text":"The fox leaves..."}
//	{"t":"fox_gone","fox":"…"}               it has left the street
//	{"t":"fox_flurry"}                       three foxes were here when another appeared (fox / three_or_more)
//	{"t":"error","code":"fox","message":"…"} the source's refusal
//
// Ours: a leaving fox counts for leaveMs (the source counts it until it reaches
// the exit, which the server cannot see); a preserve nobody is on forgets its
// foxes and baits; the bait spots come from the igniter's game (within reach of
// where the server last saw them), and the Ranger's gates (licence, brush,
// eight inside, a safe distance past him) stay in each game.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"
)

//go:embed data/foxes.json
var foxesJSON []byte

const (
	foxIgniteBurst = 3
	foxIgniteEvery = 2 * time.Second
	foxEatBurst    = 10
	foxEatEvery    = 200 * time.Millisecond
	foxMaxBaits    = 60 // ours: a preserve keeps at most this many baits
	foxBaitReach   = 600.0
)

type foxTable struct {
	Preserves           []string `json:"preserves"`
	PlayersPerFox       float64  `json:"playersPerFox"`
	SpawnDelayMs        int      `json:"spawnDelayMs"`
	RespawnAfterLeaveMs int      `json:"respawnAfterLeaveMs"`
	LeaveMs             int      `json:"leaveMs"`
	BaitExpireMs        int      `json:"baitExpireMs"`
	BaitDropCount       int      `json:"baitDropCount"`
	BaitDropSpread      float64  `json:"baitDropSpread"`
	BaitDropSpacing     float64  `json:"baitDropSpacing"`
	BaitCrowdRadius     float64  `json:"baitCrowdRadius"`
	BaitCrowdMax        int      `json:"baitCrowdMax"`
	MaxBrushes          int      `json:"maxBrushes"`
	MaxBrushers         int      `json:"maxBrushers"`
	BrushCooldownMs     int      `json:"brushCooldownMs"`
	Text                struct {
		Appears      string `json:"appears"`
		Leaves       string `json:"leaves"`
		LeavesNoBait string `json:"leavesNoBait"`
		Crowded      string `json:"crowded"`
		NotPreserve  string `json:"notPreserve"`
	} `json:"text"`
}

type preserveFox struct {
	id       string
	brushes  int
	brushers map[int64]bool
	leaving  bool
	gone     func() bool
}

type preserveBait struct {
	id     string
	x      float64
	by     int64
	expire func() bool
}

type preserve struct {
	foxes []*preserveFox
	baits []*preserveBait
	spawn func() bool // a spawn is due (apiTimerExists('spawn_fox'))
}

type foxes struct {
	h         *Hub
	t         foxTable
	preserves map[string]bool
	at        map[string]*preserve
	seq       int64
	ignites   map[int64]*bucket
	eats      map[int64]*bucket
	lastBrush map[int64]time.Time
}

func init() { modules = append(modules, newFoxes) }

func newFoxes(h *Hub) Module {
	var t foxTable
	if err := json.Unmarshal(foxesJSON, &t); err != nil || len(t.Preserves) == 0 || t.PlayersPerFox <= 0 {
		panic(fmt.Sprintf("realtime: data/foxes.json: %v", err))
	}
	m := &foxes{h: h, t: t, preserves: map[string]bool{}, at: map[string]*preserve{},
		ignites: map[int64]*bucket{}, eats: map[int64]*bucket{}, lastBrush: map[int64]time.Time{}}
	for _, s := range t.Preserves {
		m.preserves[s] = true
	}
	return m
}

func (m *foxes) Register(r *Registry) {
	r.Handle("fox_ignite", m.ignite)
	r.Handle("fox_brush", m.brush)
	r.Handle("fox_eat", m.eat)
	r.OnJoin(m.joined)
	r.OnLeave(func(cl *Client, street string) { m.left(street) })
	r.OnDisconnect(func(cl *Client) {
		delete(m.ignites, cl.ID())
		delete(m.eats, cl.ID())
		delete(m.lastBrush, cl.ID())
	})
	// The fox count other modules may want (a Feat, a quest), as the source's countItemClass('npc_fox').
	r.Provide("foxes.count", func(street string) int {
		if p := m.at[street]; p != nil {
			return len(p.foxes)
		}
		return 0
	})
}

func (m *foxes) ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func (m *foxes) refuse(cl *Client, text string) { m.h.ErrorLocked(cl, "fox", text) }

func (m *foxes) allow(set map[int64]*bucket, cl *Client, burst float64, every time.Duration) bool {
	b := set[cl.ID()]
	if b == nil {
		b = &bucket{}
		set[cl.ID()] = b
	}
	return b.take(m.h.Now(), burst, every)
}

func (m *foxes) newID(kind string) string {
	m.seq++
	return kind + "-" + strconv.FormatInt(m.seq, 36)
}

func foxesJSONList(p *preserve) []map[string]any {
	out := make([]map[string]any, 0, len(p.foxes))
	for _, f := range p.foxes {
		out = append(out, map[string]any{"id": f.id, "leaving": f.leaving})
	}
	return out
}

func baitsJSONList(list []*preserveBait) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, b := range list {
		out = append(out, map[string]any{"id": b.id, "x": b.x})
	}
	return out
}

// —— Street lifecycle ——

func (m *foxes) joined(cl *Client) {
	if !m.preserves[cl.Street()] {
		return
	}
	p := m.at[cl.Street()]
	if p == nil {
		p = &preserve{}
		m.at[cl.Street()] = p
	}
	// location.js onPlayerEnter: a newcomer sees the foxes and baits already there.
	m.h.SendLocked(cl, map[string]any{"t": "fox_state", "foxes": foxesJSONList(p), "baits": baitsJSONList(p.baits)})
}

func (m *foxes) left(street string) {
	p := m.at[street]
	if p == nil || len(m.h.StreetLocked(street)) > 0 {
		return
	}
	// Nobody left on the preserve: forget it (ours).
	if p.spawn != nil {
		p.spawn()
	}
	for _, f := range p.foxes {
		if f.gone != nil {
			f.gone()
		}
	}
	for _, b := range p.baits {
		if b.expire != nil {
			b.expire()
		}
	}
	delete(m.at, street)
}

// —— Ignite ——

func (m *foxes) ignite(cl *Client, frame json.RawMessage) {
	var msg struct {
		Baits []float64 `json:"baits"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	street := cl.Street()
	if !m.preserves[street] {
		m.refuse(cl, m.t.Text.NotPreserve)
		return
	}
	if !m.allow(m.ignites, cl, foxIgniteBurst, foxIgniteEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	px, _ := cl.Pos()
	if len(msg.Baits) != m.t.BaitDropCount {
		return
	}
	reach := m.t.BaitDropSpread + m.t.BaitDropSpacing*float64(m.t.BaitDropCount) + foxBaitReach
	for _, x := range msg.Baits {
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x-px) > reach {
			return
		}
	}
	p := m.at[street]
	if p == nil {
		p = &preserve{}
		m.at[street] = p
	}
	// fox_bait.js ignite conditions: findAllCloseStacks(is_bait, 400).length > 4.
	near := 0
	for _, b := range p.baits {
		if math.Abs(b.x-px) <= m.t.BaitCrowdRadius {
			near++
		}
	}
	if near > m.t.BaitCrowdMax || len(p.baits)+len(msg.Baits) > foxMaxBaits {
		m.refuse(cl, m.t.Text.Crowded)
		return
	}
	dropped := make([]*preserveBait, 0, len(msg.Baits))
	for _, x := range msg.Baits {
		b := &preserveBait{id: m.newID("b"), x: math.Round(x), by: cl.ID()}
		bait := b
		b.expire = m.h.After(m.ms(m.t.BaitExpireMs), func() { m.removeBait(street, bait.id) })
		p.baits = append(p.baits, b)
		dropped = append(dropped, b)
	}
	m.h.BroadcastStreetLocked(street, map[string]any{"t": "fox_baits", "by": cl.ID(), "baits": baitsJSONList(dropped)}, 0)
	// fox_bait.js:117 apiSetTimer('spawn_fox', 4000); one spawn due at a time (the remake keeps the first).
	m.scheduleSpawn(street, p, m.ms(m.t.SpawnDelayMs))
}

func (m *foxes) scheduleSpawn(street string, p *preserve, d time.Duration) {
	if p.spawn != nil {
		return
	}
	p.spawn = m.h.After(d, func() {
		if m.at[street] != p {
			return
		}
		p.spawn = nil
		m.spawnFox(street, p)
	})
}

// spawnFox is inc_npcs.js spawn_fox.
func (m *foxes) spawnFox(street string, p *preserve) {
	players := m.h.StreetLocked(street)
	before := len(p.foxes)
	if float64(before) >= float64(len(players))/m.t.PlayersPerFox {
		return
	}
	f := &preserveFox{id: m.newID("f"), brushers: map[int64]bool{}}
	p.foxes = append(p.foxes, f)
	m.h.BroadcastStreetLocked(street, map[string]any{"t": "fox_spawn", "fox": f.id, "n": len(p.foxes), "text": m.t.Text.Appears}, 0)
	log.Printf("foxes: %s: %s appears, %d foxes for %d players", street, f.id, len(p.foxes), len(players))
	if before > 2 {
		// inc_npcs.js:153-155: every player there witnessed the flurry.
		m.h.BroadcastStreetLocked(street, map[string]any{"t": "fox_flurry", "foxes": len(p.foxes)}, 0)
		log.Printf("foxes: %s: a flurry, %d players saw %d foxes", street, len(players), len(p.foxes))
	}
	// A fox that finds no bait leaves at once (startMoving).
	if len(p.baits) == 0 {
		m.leave(street, p, f, m.t.Text.LeavesNoBait)
	}
}

func (m *foxes) findFox(p *preserve, id string) *preserveFox {
	for _, f := range p.foxes {
		if f.id == id {
			return f
		}
	}
	return nil
}

func (m *foxes) leave(street string, p *preserve, f *preserveFox, text string) {
	if f.leaving {
		return
	}
	f.leaving = true
	m.h.BroadcastStreetLocked(street, map[string]any{"t": "fox_leave", "fox": f.id, "text": text}, 0)
	f.gone = m.h.After(m.ms(m.t.LeaveMs), func() {
		if m.at[street] != p {
			return
		}
		for i, o := range p.foxes {
			if o == f {
				p.foxes = append(p.foxes[:i], p.foxes[i+1:]...)
				m.h.BroadcastStreetLocked(street, map[string]any{"t": "fox_gone", "fox": f.id}, 0)
				return
			}
		}
	})
}

// —— Brushing ——

func (m *foxes) brush(cl *Client, frame json.RawMessage) {
	var msg struct {
		Fox string `json:"fox"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	street := cl.Street()
	p := m.at[street]
	if p == nil {
		return
	}
	f := m.findFox(p, msg.Fox)
	if f == nil {
		return
	}
	now := m.h.Now()
	// npc_fox.js:212: a brush within 2 s of your last one does nothing.
	if last, ok := m.lastBrush[cl.ID()]; ok && now.Sub(last) < m.ms(m.t.BrushCooldownMs) {
		return
	}
	m.lastBrush[cl.ID()] = now
	if f.leaving {
		return // "If we're leaving, just keep going"
	}
	f.brushes++
	f.brushers[cl.ID()] = true
	// npc_fox.js:425-434.
	if len(f.brushers) >= m.t.MaxBrushers || f.brushes >= m.t.MaxBrushes || len(p.baits) == 0 {
		m.leave(street, p, f, m.t.Text.Leaves)
		if len(p.baits) > 0 {
			m.scheduleSpawn(street, p, m.ms(m.t.RespawnAfterLeaveMs))
		}
	}
}

// —— Bait ——

func (m *foxes) eat(cl *Client, frame json.RawMessage) {
	var msg struct {
		Fox  string `json:"fox"`
		Bait string `json:"bait"`
	}
	if json.Unmarshal(frame, &msg) != nil || !m.allow(m.eats, cl, foxEatBurst, foxEatEvery) {
		return
	}
	p := m.at[cl.Street()]
	if p == nil || m.findFox(p, msg.Fox) == nil {
		return
	}
	m.removeBait(cl.Street(), msg.Bait)
}

func (m *foxes) removeBait(street, id string) {
	p := m.at[street]
	if p == nil {
		return
	}
	for i, b := range p.baits {
		if b.id != id {
			continue
		}
		if b.expire != nil {
			b.expire()
		}
		p.baits = append(p.baits[:i], p.baits[i+1:]...)
		m.h.BroadcastStreetLocked(street, map[string]any{"t": "fox_bait_gone", "bait": id}, 0)
		if len(p.baits) == 0 {
			// startMoving finds no bait: every fox there goes.
			for _, f := range append([]*preserveFox{}, p.foxes...) {
				m.leave(street, p, f, m.t.Text.LeavesNoBait)
			}
		}
		return
	}
}
