package main

// Street toys (register G679): the Clickable Broadcasters of Lotha Harte and Icicle Dangles, shared
// by everyone on the street.
//
// In Glitch these were the location's own state, never one player's. Each street's event scripts
// (streets location_data L*.xml "events") ran on the location (GameServerJS locations/inc_events.js
// events_broadcast): a flag (events_set_flag) and a code_location variable (loc.note1) were the
// location's, a deco_show / deco_hide went to every player there
// (inc_geo.js geo_deco_toggle_visibility 431-438, apiSendMsg) and was kept in hidden_decos, which
// events_broadcast replays to each player_enter (inc_events.js 199-205), and a play_sound with
// all_players went to every active player (373-430). How each street used it:
//
//   - Lotha Harte (LHV88RQU60B3Q0G.xml): first_note, second_note, third_note play WM_D1, WM_D2,
//     WM_E2 with all_players, then stop on flag lock, then set loc.note1..3 (a wrong order clears
//     them). The right order runs correct_order: flag lock, the heart's decos shown to the street,
//     light_fade 22 s later (decos hidden), reset 3.5 s after that (notes false, lock cleared).
//     A click from anyone counts toward the one order the street keeps.
//   - Icicle Dangles (LHF70EIUS0D3RDM.xml): each light's click broadcasts shroom_light_N_on and
//     _off; flag lightN_on (set while the light is out) picks which one runs, flag lightN locks
//     the light for 700 ms (lightN_reset). The light is the street's: out for everyone.
//
// Here the server holds that state and the clients draw it with the street's own scripts:
//
//   - A click (toy_click) is checked against the street the clicker stands on and the toy's
//     script, and what the street runs goes to everyone there as toy_played, the clicker too;
//     each client runs those events (the sounds, the fades). The 1000 px reach of
//     clickable_broadcaster.js is the clicker's game's check: a toy hangs on a parallax deco whose
//     place depends on each player's camera, so this server cannot place it. A click a player
//     sends more often than toyClickEvery is dropped (ours; the source took every click).
//   - Lotha Harte's notes and lock, and the 22 s and 3.5 s timers, run here; light_fade goes out
//     as toy_played when its time comes. Icicle Dangles' three lights and their 700 ms locks too.
//   - A player arriving mid-state is sent the street's state (toy_state) after the join and
//     catches up from it: the heart still lit, a light still out.
//
// Client to server:
//
//	{"t":"toy_click","toy":"lotha_note_1"}
//
// Server to client:
//
//	{"t":"toy_state","street":"G…","toys":"lotha","lit":true}                    after each join on a toy street
//	{"t":"toy_state","street":"G…","toys":"dangles","lights":[true,false,true]}   true: the light shows
//	{"t":"toy_played","street":"G…","toy":"lotha_note_3","by":7,"events":["third_note","correct_order"]}
//	{"t":"toy_played","street":"G…","toy":"","by":0,"events":["light_fade"]}                     a timer of the street's

import (
	"encoding/json"
	"log"
	"time"
)

const (
	toyClickEvery = 200 * time.Millisecond

	// LHV88RQU60B3Q0G.xml correct_order: light_fade at delay 22000; light_fade: reset at delay 3500.
	lothaLit   = 22 * time.Second
	lothaReset = 3500 * time.Millisecond
	// LHF70EIUS0D3RDM.xml shroom_light_N_on/_off: lightN_reset at delay 700.
	danglesLock = 700 * time.Millisecond
)

const (
	toyLotha   = "lotha"
	toyDangles = "dangles"
)

// toyStreetKinds: each toy street, keyed without its first letter (G or L).
var toyStreetKinds = map[string]string{
	"HV88RQU60B3Q0G": toyLotha,   // Lotha Harte
	"HF70EIUS0D3RDM": toyDangles, // Icicle Dangles
}

// lothaToys: each icicle and the note event its broadcaster fires (js/street-toys.js STREET_TOYS).
var lothaToys = map[string]string{"lotha_note_1": "first_note", "lotha_note_2": "second_note", "lotha_note_3": "third_note"}

// danglesToys: each light (js/street-toys.js icicle_light_N).
var danglesToys = map[string]int{"icicle_light_1": 0, "icicle_light_2": 1, "icicle_light_3": 2}

func toyKind(street string) string {
	if len(street) < 2 {
		return ""
	}
	return toyStreetKinds[street[1:]]
}

type toyStreet struct {
	kind string
	// Lotha Harte
	notes     [3]bool
	litUntil  time.Time
	lockUntil time.Time
	// Icicle Dangles: out[i] is flag lightN_on (the light is out); lightLock[i] the 700 ms lock.
	out       [3]bool
	lightLock [3]time.Time
}

type streetToys struct {
	h       *Hub
	streets map[string]*toyStreet
	clickAt map[int64]time.Time
}

func init() { modules = append(modules, newStreetToys) }

func newStreetToys(h *Hub) Module {
	m := &streetToys{h: h, streets: map[string]*toyStreet{}, clickAt: map[int64]time.Time{}}
	return m
}

func (m *streetToys) Register(r *Registry) {
	r.Handle("toy_click", m.click)
	r.OnJoin(m.joined)
	r.OnDisconnect(func(cl *Client) { delete(m.clickAt, cl.ID()) })
}

func (m *streetToys) street(street string) *toyStreet {
	kind := toyKind(street)
	if kind == "" {
		return nil
	}
	st := m.streets[street]
	if st == nil {
		st = &toyStreet{kind: kind}
		m.streets[street] = st
	}
	return st
}

// joined sends the joiner the street's state.
func (m *streetToys) joined(cl *Client) {
	if st := m.street(cl.Street()); st != nil {
		m.h.SendLocked(cl, m.state(cl.Street(), st, m.h.Now()))
	}
}

func (m *streetToys) state(street string, st *toyStreet, now time.Time) map[string]any {
	out := map[string]any{"t": "toy_state", "street": street, "toys": st.kind}
	switch st.kind {
	case toyLotha:
		out["lit"] = now.Before(st.litUntil)
	case toyDangles:
		out["lights"] = []bool{!st.out[0], !st.out[1], !st.out[2]}
	}
	return out
}

func (m *streetToys) played(street, toy string, by int64, events []string, st *toyStreet) map[string]any {
	return map[string]any{"t": "toy_played", "street": street, "toy": toy, "by": by, "events": events}
}

// click is click_broadcast on one of the clicker's street's toys.
func (m *streetToys) click(cl *Client, frame json.RawMessage) {
	var f struct {
		Toy string `json:"toy"`
	}
	if json.Unmarshal(frame, &f) != nil {
		return
	}
	street := cl.Street()
	st := m.street(street)
	if st == nil {
		return
	}
	now := m.h.Now()
	if at, ok := m.clickAt[cl.ID()]; ok && now.Sub(at) < toyClickEvery {
		return
	}
	switch st.kind {
	case toyLotha:
		ev, ok := lothaToys[f.Toy]
		if !ok {
			return
		}
		m.clickAt[cl.ID()] = now
		events := []string{ev}
		// The note sounds for everyone; flag_has_exit('lock') stops the rest while the heart is lit.
		if !now.Before(st.lockUntil) && lothaNote(&st.notes, ev) {
			events = append(events, "correct_order")
			st.litUntil = now.Add(lothaLit)
			st.lockUntil = now.Add(lothaLit + lothaReset)
			log.Printf("toys: %d lit the heart on %s", cl.ID(), street)
			m.h.After(lothaLit, func() {
				m.h.BroadcastStreetLocked(street, m.played(street, "", 0, []string{"light_fade"}, nil), 0)
			})
		}
		m.h.BroadcastStreetLocked(street, m.played(street, f.Toy, cl.ID(), events, nil), 0)
	case toyDangles:
		i, ok := danglesToys[f.Toy]
		if !ok {
			return
		}
		m.clickAt[cl.ID()] = now
		// Both events exit at flag lightN while it is set: nothing happens, nothing is heard.
		if now.Before(st.lightLock[i]) {
			return
		}
		st.out[i] = !st.out[i]
		st.lightLock[i] = now.Add(danglesLock)
		ev := "shroom_light_" + itoa(i+1) + "_on"
		if st.out[i] {
			ev = "shroom_light_" + itoa(i+1) + "_off"
		}
		m.h.BroadcastStreetLocked(street, m.played(street, f.Toy, cl.ID(), []string{ev}, nil), 0)
	}
}

// lothaNote is the notes' code_location, statement by statement (js/street-toys.js lothaNote):
// true when third_note finds note1 and note2 set (correct_order); the notes are then clear again.
func lothaNote(n *[3]bool, ev string) bool {
	switch ev {
	case "first_note":
		n[0] = true
	case "second_note":
		if n[0] {
			n[1] = true
		} else {
			*n = [3]bool{}
		}
	case "third_note":
		ok := n[0] && n[1]
		*n = [3]bool{}
		return ok
	}
	return false
}
