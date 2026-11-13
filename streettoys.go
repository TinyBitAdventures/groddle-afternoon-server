package main

// Street toys (register G679): the Clickable Broadcasters of Lotha Harte and Icicle Dangles, and
// Koulato Cluster's three firefly jars, shared by everyone on the street.
//
// In Glitch these were the location's own state, never one player's. Each street's event scripts
// (streets location_data L*.xml "events") ran on the location (GameServerJS locations/inc_events.js
// events_broadcast): a flag (events_set_flag) and a code_location variable (loc.note1, loc.j1,
// loc.num) were the location's, a deco_show / deco_hide went to every player there
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
//   - Koulato Cluster (LIF10RP1DBU11NU.xml): player_enter counts loc.num for every arrival and at
//     30 runs place_jars (reset_vars, the three jars shown, flag jars_on). jarN_on / jarN_off set
//     loc.jN from three Collision-triggered Event Boxes (enter / exit), and check_done runs complete
//     and turn_off once j1, j2 and j3 are all true. complete (once, flag completed) shows three
//     light pools, hides everything a second later (hide_all) and puts three Firefly Jars, 7
//     Fireflies each, on the ground (loc.createItemStack('firefly_jar', 1, x, -800) at -270, -98
//     and 62): three stacks on the location, which anyone there could pick up. Three Glitches
//     stand on the three jars, so the street gives three jars, one at each jar.
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
//   - Koulato Cluster's count is every player's arrival on the street (OnJoin), as loc.num was;
//     a join that only takes back the street after a dropped socket or a replaced one (the same
//     player, the same street, within toyRejoinGrace) is not an arrival (the source counted
//     player_enter, never player_reconnect, which the street has no script for). The jars are lit
//     from the positions this server already has (each player's last move), never a client's word:
//     every toyJarTick while the jars are out and someone is there, a jar is lit while a player's
//     feet are in its box (the boxes are the game's js/koulato-jars.js JAR_BOXES: the source's are
//     lost; one player can stand in only one, so three lit jars are three players). When all
//     three are lit, the jars are put away at once (so it pays once, whatever anyone sends or
//     however often they reconnect) and each jar's Firefly Jar goes on the ground through the
//     ground drops (drops.go, Provide "drops.place"), placed under the player standing on that
//     jar: the source's three stacks, with that player's 100 px first chance (ours, as a fed
//     Piggy's Plop is the feeder's). Where no shared drop can be placed (no ledger, the street
//     full), the player is told to put it down in their own game ("own"), as the game does for a
//     stack it cannot share. Solo is the game's own and keeps the source's rule: the jars come out
//     but a lone Glitch cannot light all three.
//   - A player arriving mid-state is sent the street's state (toy_state) after the join and
//     catches up from it: the heart still lit, a light still out, the jars out and which are lit.
//
// Koulato Cluster's count and whether its jars are out survive a restart (-toys-file, default
// realtime-toys.json beside -secret-file; off: memory only), as loc.num and the flags lived on the
// location. The heart's 25 s and the lights' state are short and start afresh.
//
// Client to server:
//
//	{"t":"toy_click","toy":"lotha_note_1"}
//
// Server to client:
//
//	{"t":"toy_state","street":"G…","toys":"lotha","lit":true}                    after each join on a toy street
//	{"t":"toy_state","street":"G…","toys":"dangles","lights":[true,false,true]}   true: the light shows
//	{"t":"toy_state","street":"G…","toys":"koulato","arrivals":12,"out":true,"jars":[false,true,false]}
//	{"t":"toy_played","street":"G…","toy":"lotha_note_3","by":7,"events":["third_note","correct_order"]}
//	{"t":"toy_played","street":"G…","toy":"","by":0,"events":["light_fade"]}                     a timer of the street's
//	{"t":"toy_played","street":"G…","toy":"","by":0,"events":["jar3_on","complete","turn_off"],
//	 "jars":[{"n":1,"id":7,"name":"Ada","own":false},…]}                                         the puzzle done
//	{"t":"toy_played","street":"G…","toy":"","by":0,"events":[…],"arrivals":30?,"out":true,"lit":[…]}  jars placed, lit, put out

import (
	"encoding/json"
	"flag"
	"log"
	"math"
	"sort"
	"time"
)

const (
	toyClickEvery  = 200 * time.Millisecond
	toyRejoinGrace = 2 * time.Minute
	toyJarTick     = 200 * time.Millisecond

	// LHV88RQU60B3Q0G.xml correct_order: light_fade at delay 22000; light_fade: reset at delay 3500.
	lothaLit   = 22 * time.Second
	lothaReset = 3500 * time.Millisecond
	// LHF70EIUS0D3RDM.xml shroom_light_N_on/_off: lightN_reset at delay 700.
	danglesLock = 700 * time.Millisecond
	// LIF10RP1DBU11NU.xml player_enter: if(loc.num>=30) call_event('place_jars').
	koulatoArrivals = 30
	// complete: f1.setInstanceProp('num_flies', 7).
	koulatoFlies = 7
)

var toysFileFlag = flag.String("toys-file", "", "file that keeps Koulato Cluster's arrival count and whether its firefly jars are out (default: realtime-toys.json beside -secret-file; off: memory only)")

// toysFileForTest lets a test give the module a state file; under go test the flag is ignored.
var toysFileForTest string

const (
	toyLotha   = "lotha"
	toyDangles = "dangles"
	toyKoulato = "koulato"
)

// toyStreetKinds: each toy street, keyed without its first letter (G or L).
var toyStreetKinds = map[string]string{
	"HV88RQU60B3Q0G": toyLotha,   // Lotha Harte
	"HF70EIUS0D3RDM": toyDangles, // Icicle Dangles
	"IF10RP1DBU11NU": toyKoulato, // Koulato Cluster
}

// lothaToys: each icicle and the note event its broadcaster fires (js/street-toys.js STREET_TOYS).
var lothaToys = map[string]string{"lotha_note_1": "first_note", "lotha_note_2": "second_note", "lotha_note_3": "third_note"}

// danglesToys: each light (js/street-toys.js icicle_light_N).
var danglesToys = map[string]int{"icicle_light_1": 0, "icicle_light_2": 1, "icicle_light_3": 2}

// koulatoJar is one of js/koulato-jars.js JAR_BOXES (bottom-centre, the players' own space) and
// where its Firefly Jar comes to rest: the box's drop x, on the ledge under it (the line's y there).
type koulatoJar struct{ X, Y, W, H, DropX, DropY float64 }

var koulatoJars = [3]koulatoJar{
	{X: 2245, Y: 1180, W: 130, H: 220, DropX: 2230, DropY: 1166},
	{X: 2416, Y: 1180, W: 130, H: 220, DropX: 2402, DropY: 1168},
	{X: 2582, Y: 1180, W: 130, H: 220, DropX: 2562, DropY: 1167},
}

// in is js/koulato-jars.js inJarBox: feet within half the width, from the box's top to 4 px under it.
func (j koulatoJar) in(x, y float64) bool {
	return math.Abs(x-j.X) <= j.W/2 && y <= j.Y+4 && y >= j.Y-j.H
}

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
	// Koulato Cluster
	arrivals int
	jarsOut  bool
	lit      [3]bool
	ticking  bool
}

type toyLeft struct {
	street  string
	at      time.Time
	dropped bool
}

type streetToys struct {
	h       *Hub
	streets map[string]*toyStreet
	clickAt map[int64]time.Time
	left    map[int64]toyLeft
	path    string
	saveAt  bool
}

func init() { modules = append(modules, newStreetToys) }

func newStreetToys(h *Hub) Module {
	m := &streetToys{h: h, streets: map[string]*toyStreet{}, clickAt: map[int64]time.Time{}, left: map[int64]toyLeft{}}
	if m.path = statePath(toysFileForTest, toysFileFlag, "realtime-toys.json"); m.path != "" {
		if err := m.load(); err != nil {
			log.Printf("toys %s: %v (Koulato's count is kept in memory only)", m.path, err)
			m.path = ""
		}
	}
	return m
}

func (m *streetToys) Register(r *Registry) {
	r.Handle("toy_click", m.click)
	r.OnJoin(m.joined)
	r.OnLeave(func(cl *Client, street string) {
		m.left[cl.ID()] = toyLeft{street: street, at: m.h.Now()}
	})
	// A socket that ended, or one a new socket replaced: the next join of the same street is a rejoin.
	r.OnDisconnect(func(cl *Client) {
		m.markDropped(cl.ID())
		delete(m.clickAt, cl.ID())
	})
	r.OnConnect(func(cl *Client) { m.markDropped(cl.ID()) })
	r.OnShutdown(func() { m.save() })
	// For tests and other modules: Koulato's count and whether its jars are out on a street.
	r.Provide("toys.koulato", func(street string) (int, bool) {
		if st := m.streets[street]; st != nil {
			return st.arrivals, st.jarsOut
		}
		return 0, false
	})
}

func (m *streetToys) markDropped(id int64) {
	if l, ok := m.left[id]; ok {
		l.dropped = true
		m.left[id] = l
	}
	if len(m.left) > 4096 {
		cut := m.h.Now().Add(-toyRejoinGrace)
		for k, l := range m.left {
			if l.at.Before(cut) {
				delete(m.left, k)
			}
		}
	}
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

// joined counts a Koulato arrival and sends the joiner the street's state.
func (m *streetToys) joined(cl *Client) {
	street := cl.Street()
	st := m.street(street)
	l, had := m.left[cl.ID()]
	delete(m.left, cl.ID())
	if st == nil {
		return
	}
	now := m.h.Now()
	if st.kind == toyKoulato {
		rejoin := had && l.dropped && l.street == street && now.Sub(l.at) <= toyRejoinGrace
		if !rejoin {
			st.arrivals++
			if st.arrivals >= koulatoArrivals {
				// place_jars: reset_vars (num 0, j1-3 false), the jars shown, jars_on.
				st.arrivals = 0
				st.jarsOut = true
				st.lit = [3]bool{}
				log.Printf("toys: the firefly jars are out on %s (arrival of %d)", street, cl.ID())
				m.h.BroadcastStreetLocked(street, m.played(street, "", 0, []string{"place_jars"}, st), cl.ID())
			}
			m.saveSoon()
		}
		m.tickSoon(street, st)
	}
	m.h.SendLocked(cl, m.state(street, st, now))
}

func (m *streetToys) state(street string, st *toyStreet, now time.Time) map[string]any {
	out := map[string]any{"t": "toy_state", "street": street, "toys": st.kind}
	switch st.kind {
	case toyLotha:
		out["lit"] = now.Before(st.litUntil)
	case toyDangles:
		out["lights"] = []bool{!st.out[0], !st.out[1], !st.out[2]}
	case toyKoulato:
		out["arrivals"] = st.arrivals
		out["out"] = st.jarsOut
		out["jars"] = st.lit[:]
	}
	return out
}

func (m *streetToys) played(street, toy string, by int64, events []string, st *toyStreet) map[string]any {
	msg := map[string]any{"t": "toy_played", "street": street, "toy": toy, "by": by, "events": events}
	if st != nil && st.kind == toyKoulato {
		msg["arrivals"] = st.arrivals
		msg["out"] = st.jarsOut
		msg["lit"] = []bool{st.lit[0], st.lit[1], st.lit[2]}
	}
	return msg
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

// tickSoon watches the jars while they are out and someone is on the street.
func (m *streetToys) tickSoon(street string, st *toyStreet) {
	if st.ticking || !st.jarsOut || len(m.h.StreetLocked(street)) == 0 {
		return
	}
	st.ticking = true
	m.h.After(toyJarTick, func() {
		st.ticking = false
		m.tick(street, st)
		m.tickSoon(street, st)
	})
}

// tick lights each jar a player stands on (jarN_on / jarN_off) and completes the street when all
// three are lit at once (check_done: complete, turn_off).
func (m *streetToys) tick(street string, st *toyStreet) {
	if !st.jarsOut {
		return
	}
	var on [3]*Client
	for _, cl := range m.h.StreetLocked(street) {
		x, y := cl.Pos()
		for i, j := range koulatoJars {
			if !j.in(x, y) {
				continue
			}
			// Two on one jar: the one nearer its middle stands on it (then the lower id).
			if o := on[i]; o == nil || nearer(cl, o, j.X) {
				on[i] = cl
			}
		}
	}
	var events []string
	for i := range on {
		now := on[i] != nil
		if now == st.lit[i] {
			continue
		}
		st.lit[i] = now
		if now {
			events = append(events, "jar"+itoa(i+1)+"_on")
		} else {
			events = append(events, "jar"+itoa(i+1)+"_off")
		}
	}
	if len(events) == 0 {
		return
	}
	if !(st.lit[0] && st.lit[1] && st.lit[2]) {
		m.h.BroadcastStreetLocked(street, m.played(street, "", 0, events, st), 0)
		return
	}
	// complete and turn_off: the jars go away now, so nothing can pay twice.
	st.jarsOut = false
	st.lit = [3]bool{}
	m.saveSoon()
	events = append(events, "complete", "turn_off")
	jars := make([]map[string]any, 0, 3)
	place, _ := m.h.ProvidedLocked("drops.place").(func(string, int64, string, float64, float64, tradeItem) bool)
	ids := make([]int64, 0, 3)
	for i, cl := range on {
		j := koulatoJars[i]
		flies := koulatoFlies
		shared := place != nil && place(street, cl.ID(), cl.Name(), j.DropX, j.DropY, tradeItem{C: "firefly_jar", N: 1, R: &flies})
		jars = append(jars, map[string]any{"n": i + 1, "id": cl.ID(), "name": cl.Name(), "own": !shared, "x": j.DropX, "y": j.DropY})
		ids = append(ids, cl.ID())
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	log.Printf("toys: the firefly jars on %s were lit by %v", street, ids)
	msg := m.played(street, "", 0, events, st)
	msg["jars"] = jars
	m.h.BroadcastStreetLocked(street, msg, 0)
}

func nearer(a, b *Client, x float64) bool {
	ax, _ := a.Pos()
	bx, _ := b.Pos()
	da, db := math.Abs(ax-x), math.Abs(bx-x)
	if da != db {
		return da < db
	}
	return a.ID() < b.ID()
}

// —— State file ——

type toysFile struct {
	V       int                    `json:"v"`
	Koulato map[string]koulatoSave `json:"koulato"`
}

type koulatoSave struct {
	Arrivals int  `json:"arrivals"`
	Out      bool `json:"out"`
}

func (m *streetToys) load() error {
	var f toysFile
	ok, err := readStateFile("toys", m.path, &f)
	if err != nil || !ok {
		return err
	}
	for street, k := range f.Koulato {
		if toyKind(street) != toyKoulato || !streetRe.MatchString(street) {
			continue
		}
		st := m.street(street)
		st.arrivals = min(max(k.Arrivals, 0), koulatoArrivals-1)
		st.jarsOut = k.Out
	}
	log.Printf("toys %s: %d Koulato streets", m.path, len(f.Koulato))
	return nil
}

func (m *streetToys) save() bool {
	if m.path == "" {
		return true
	}
	f := toysFile{V: 1, Koulato: map[string]koulatoSave{}}
	for street, st := range m.streets {
		if st.kind == toyKoulato && (st.arrivals > 0 || st.jarsOut) {
			f.Koulato[street] = koulatoSave{Arrivals: st.arrivals, Out: st.jarsOut}
		}
	}
	b, err := json.Marshal(f)
	if err == nil {
		err = writeFileSynced(m.path, b)
	}
	if err != nil {
		log.Printf("toys %s: %v", m.path, err)
		return false
	}
	return true
}

func (m *streetToys) saveSoon() {
	if m.path == "" || m.saveAt {
		return
	}
	m.saveAt = true
	m.h.After(time.Second, func() {
		m.saveAt = false
		m.save()
	})
}
