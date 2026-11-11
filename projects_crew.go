package main

// Who is working on a street project now (gap 2 of the street projects audit,
// glitch-audit research/street-projects-audit-2026-11-11.md). In Glitch each
// worker's tool bubble went to everyone else on the street: items/include/jobs.js
// startJobWork 293-307 built a pc_overlay (the tool's tool_animation in a 40x40
// bubble, delta_y -110, uid <pc>_job_work_all) for the whole run of units and
// sent it with this.container.apiSendAnnouncementX(anncx, pc), the location's
// announcement to every player there but pc (eleven-server
// src/model/LocationApi.js 225-236); cancelJobWork 435-438 took it down for the
// same players (overlay_cancel, apiSendMsgAsIsX(..., pc)). The tool's sound was
// the worker's own: pc.announce_sound (jobs.js 308, players/inc_announcements.js 6)
// plays to that player only and cancelJobWork 457 stops it, so the street sees
// the bubbles and hears nothing.
//
// The game says when its run of units starts and when it stops (one message
// each, not one a unit) and the server tells the street, the way radiance.go
// tells it about a meditation's bubble:
//
//	{"t":"project_work_start","job":"62","req":"r138","tool":"hatchet"}   client: my run of work began
//	{"t":"project_work_stop"}                                              client: it ended
//	{"t":"project_working","id":7,"tool":"hatchet"}                       to the street but the worker ("" when it stopped)
//	{"t":"project_workers","list":[{"id":7,"tool":"hatchet"}]}           to a player joining a street where someone works
//
// A start counts only where a unit of work would (projects.go gate: the
// board's street, the project open; an open work requirement with work left).
// A refused start is silent: a "project" error stops the game's own work
// (street-jobs-feature.js createMp error handler). The tool is the game's
// word, a class-shaped one: data/projects.json keeps no tool for a work row
// (the game's catalog has them, js/street-jobs.js workGear), and the game
// draws only a tool its catalog uses for work. The bubble comes down when the
// worker says so, leaves the street or goes offline, and when no unit has
// counted for projectCrewIdle (a game that stopped without saying so).

import (
	"encoding/json"
	"regexp"
	"sort"
	"time"
)

const (
	projectCrewIdle  = 10 * time.Second // the game's units come every 1,500 ms and it gives one up after 6 s (WORK_WAIT_MS)
	projectCrewBurst = 4                // starts and stops a player may send at once
	projectCrewEvery = time.Second      // and then one a second
)

var projectToolRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

type projWorker struct {
	street, job, tool string
	last              time.Time // the start, then each unit that counted
	stop              func() bool
}

func (m *projects) registerCrew(r *Registry) {
	m.crew = map[int64]*projWorker{}
	m.crewRate = map[int64]*bucket{}
	r.Handle("project_work_start", m.crewStart)
	r.Handle("project_work_stop", func(cl *Client, _ json.RawMessage) {
		if m.crewAllow(cl) {
			m.crewStop(cl.ID())
		}
	})
	r.OnJoin(m.crewJoined)
	r.OnLeave(func(cl *Client, _ string) { m.crewStop(cl.ID()) })
	r.OnDisconnect(func(cl *Client) {
		m.crewStop(cl.ID())
		delete(m.crewRate, cl.ID())
	})
}

func (m *projects) crewAllow(cl *Client) bool {
	b := m.crewRate[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.crewRate[cl.ID()] = b
	}
	return b.take(m.h.Now(), projectCrewBurst, projectCrewEvery)
}

func (m *projects) crewStart(cl *Client, frame json.RawMessage) {
	var f struct {
		Job  string `json:"job"`
		Req  string `json:"req"`
		Tool string `json:"tool"`
	}
	if json.Unmarshal(frame, &f) != nil || len(f.Job) > 16 || len(f.Req) > 16 || !projectToolRe.MatchString(f.Tool) || cl.Street() == "" || !m.crewAllow(cl) {
		return
	}
	job, why := m.gate(cl, f.Job)
	if why != "" {
		return
	}
	open := false
	for _, v := range m.reqs(job, m.st.Jobs[f.Job]) {
		if v.ID == f.Req && v.Type == "work" && v.open && v.Need-v.got-v.held > 0 {
			open = true
		}
	}
	if !open {
		return
	}
	id, now := cl.ID(), m.h.Now()
	w := m.crew[id]
	if w != nil && w.street == cl.Street() && w.tool == f.Tool {
		w.job, w.last = f.Job, now // the same bubble: nothing new for the street
		return
	}
	if w == nil {
		w = &projWorker{}
		m.crew[id] = w
		m.crewWatch(id, w, projectCrewIdle)
	}
	w.street, w.job, w.tool, w.last = cl.Street(), f.Job, f.Tool, now
	m.h.BroadcastStreetLocked(w.street, map[string]any{"t": "project_working", "id": id, "tool": w.tool}, id)
}

// crewUnit: a unit of the player's counted (projects.go work), so the bubble stays.
func (m *projects) crewUnit(cl *Client) {
	if w := m.crew[cl.ID()]; w != nil {
		w.last = m.h.Now()
	}
}

// crewWatch takes the bubble down once no unit has counted for projectCrewIdle.
func (m *projects) crewWatch(id int64, w *projWorker, after time.Duration) {
	w.stop = m.h.After(after, func() {
		if m.crew[id] != w {
			return
		}
		idle := m.h.Now().Sub(w.last)
		if idle >= projectCrewIdle {
			m.crewStop(id)
			return
		}
		m.crewWatch(id, w, projectCrewIdle-idle)
	})
}

// crewStop: cancelJobWork 435-438 (jobs.js), the bubble comes down for the street the worker was on.
func (m *projects) crewStop(id int64) {
	w := m.crew[id]
	if w == nil {
		return
	}
	delete(m.crew, id)
	if w.stop != nil {
		w.stop()
	}
	m.h.BroadcastStreetLocked(w.street, map[string]any{"t": "project_working", "id": id, "tool": ""}, id)
}

// crewJoined tells a player arriving on a street whose tool bubbles are up there.
func (m *projects) crewJoined(cl *Client) {
	var list []map[string]any
	for id, w := range m.crew {
		if id != cl.ID() && w.street == cl.Street() {
			list = append(list, map[string]any{"id": id, "tool": w.tool})
		}
	}
	if len(list) == 0 {
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i]["id"].(int64) < list[j]["id"].(int64) })
	m.h.SendLocked(cl, map[string]any{"t": "project_workers", "list": list})
}
