package main

// The Ur subway between real players (G115 online, lane MPPUBLIC): riders who
// board the same line at the same station share one train, see each other in
// the carriage and chat there, and each gets off at their own stop. The rules
// are GameServerJS locations/inc_transit.js with config_prod.js
// transit_instances.subway_1 (data/subway.json, written by the game's
// tools/build-realtime-subway.mjs):
//
//   - transit_enter_instance 147-177 / transit_next_instance 116-141: a rider
//     boards a train of that line standing at the station with room (more than
//     nobody, fewer than max_capacity 10, current_stop the station), else a new
//     one. One train is one location instance: everyone on it is on one street
//     (here TRAIN_<id>).
//   - transit_schedule_departure 214-259: a new train waits until min_capacity
//     3 riders have boarded ("This train will be departing when N more players
//     board, or in S seconds.") or initial_departure_timeout 60 s; then the doors
//     close and it departs 3 s later (transit_initial_departure 261-264).
//   - transit_depart 266-312: a train with nobody on board goes out of service.
//   - transit_arrive 345-400: at each stop it stands departure_timeout 10 s
//     (doors 3 s before), every rider is asked "Get off train?", and riders
//     there may board it (transit_add_instance at 362).
//
// Ours:
//   - A lone rider is not kept the source's 60 s: while nobody else stands on
//     the station's platform the train leaves 10 s after boarding (solo's wait,
//     js/subway.js rideTiming board); with someone else on the platform it
//     waits for them as the source did (60 s, or at once with 3 aboard).
//   - Stops are 20 s apart, as in solo (source arrival_timeout 30 s).
//   - Boarding a train standing at a later stop keeps its 10 s dwell (the source
//     closed the doors at once, 245-248, cutting short the riders deciding
//     whether to get off); a late boarder still gets the 3 s of doors.
//   - The fare stays the client's 50 currants, a loss the ledger always
//     accepts, as before; the server owns only who rides which train.
//   - A rider whose socket drops keeps their place 15 s, as a racer does.
//
// Client to server:
//
//	{"t":"subway_board","station":3,"line":"forwards"}   on the station's platform, ticket paid
//
// Server to client:
//
//	{"t":"subway_train","train":"…","street":"TRAIN_…","line":"forwards","stop":3,"phase":"boarding|moving|stopped",
//	    "initial":true,"depart_in":9000,"arrive_in":0,"need":2,"riders":[{"id","name"}]}   to every rider, on any change
//	{"t":"subway_refused","why":"station|street"}

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

//go:embed data/subway.json
var subwayJSON []byte

type subwayStation struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Street string `json:"street"`
	Next   int    `json:"next"`
	Prev   int    `json:"prev"`
}

type subwayData struct {
	MinCapacity int             `json:"min_capacity"`
	MaxCapacity int             `json:"max_capacity"`
	Initial     int             `json:"initial_departure_timeout"`
	Dwell       int             `json:"departure_timeout"`
	Lonely      int             `json:"lonely"`
	Doors       int             `json:"doors"`
	Travel      int             `json:"travel"`
	Stations    []subwayStation `json:"stations"`
}

const (
	subwayGrace   = 15 * time.Second // a dropped rider's place is kept this long
	subwayPending = 10 * time.Second // a boarder who never reaches the train is dropped at departure after this
	subwayRecheck = time.Second      // a new train looks at its platform this often
)

type subwayRider struct {
	name string
	on   bool // has joined the train's street
	at   time.Time
}

type subwayTrain struct {
	id, street, dir string
	stop            int    // the station it stands at, or last left
	phase           string // boarding | moving | stopped
	initial         bool   // not yet departed once (has_initial_departure false)
	boardAt         time.Time
	departAt        time.Time
	arriveAt        time.Time
	riders          map[int64]*subwayRider
	gen             int
	stopTimer       func() bool
}

type subway struct {
	h       *Hub
	d       subwayData
	byID    map[int]*subwayStation
	trains  map[string]*subwayTrain
	riderOf map[int64]*subwayTrain
	graces  map[int64]func() bool
	newID   func() string
}

func init() { modules = append(modules, newSubway) }

func parseSubway(b []byte) (subwayData, error) {
	var d subwayData
	err := json.Unmarshal(b, &d)
	return d, err
}

func newSubway(h *Hub) Module {
	d, err := parseSubway(subwayJSON)
	if err != nil || len(d.Stations) == 0 {
		panic("realtime: data/subway.json: bad")
	}
	return newSubwayWith(h, d)
}

func newSubwayWith(h *Hub, d subwayData) *subway {
	m := &subway{h: h, d: d, byID: map[int]*subwayStation{}, trains: map[string]*subwayTrain{}, riderOf: map[int64]*subwayTrain{},
		graces: map[int64]func() bool{},
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return strings.ToUpper(hex.EncodeToString(b))
		}}
	for i := range d.Stations {
		m.byID[d.Stations[i].ID] = &d.Stations[i]
	}
	return m
}

func (m *subway) Register(r *Registry) {
	r.Handle("subway_board", m.board)
	r.OnJoin(m.onJoin)
	r.OnDisconnect(m.onDisconnect)
	r.GateJoin(m.gate)
}

func (m *subway) secs(n int) time.Duration { return time.Duration(n) * time.Second }

// gate keeps a train to its riders.
func (m *subway) gate(cl *Client, street string) (bool, string, string) {
	if !strings.HasPrefix(street, "TRAIN_") {
		return true, "", ""
	}
	if t := m.riderOf[cl.ID()]; t != nil && t.street == street {
		return true, "", ""
	}
	return false, "private_street", "That train has left."
}

func (m *subway) next(t *subwayTrain) *subwayStation {
	here := m.byID[t.stop]
	if here == nil {
		return nil
	}
	if t.dir == "backwards" {
		return m.byID[here.Prev]
	}
	return m.byID[here.Next]
}

// board is subway_gate.js push → transit_enter_instance.
func (m *subway) board(cl *Client, frame json.RawMessage) {
	var msg struct {
		Station int    `json:"station"`
		Dir     string `json:"line"`
	}
	json.Unmarshal(frame, &msg)
	st := m.byID[msg.Station]
	if st == nil || (msg.Dir != "forwards" && msg.Dir != "backwards") {
		m.h.SendLocked(cl, map[string]any{"t": "subway_refused", "why": "station"})
		return
	}
	if cl.Street() != st.Street {
		m.h.SendLocked(cl, map[string]any{"t": "subway_refused", "why": "street"})
		return
	}
	now := m.h.Now()
	if old := m.riderOf[cl.ID()]; old != nil {
		m.drop(old, cl.ID())
	}
	t := m.standing(st.ID, msg.Dir, now)
	if t == nil {
		id := m.newID()
		for m.trains[id] != nil {
			id = m.newID()
		}
		t = &subwayTrain{id: id, street: "TRAIN_" + id, dir: msg.Dir, stop: st.ID, phase: "boarding", initial: true, boardAt: now, riders: map[int64]*subwayRider{}}
		m.trains[id] = t
	}
	t.riders[cl.ID()] = &subwayRider{name: cl.Name(), at: now}
	m.riderOf[cl.ID()] = t
	if t.initial {
		m.plan(t, now)
	} else if d := m.secs(m.d.Doors); t.departAt.Sub(now) < d {
		t.departAt = now.Add(d)
	}
	m.schedule(t)
	m.tell(t)
}

// standing is transit_next_instance: the oldest train of that line standing at the station with room.
func (m *subway) standing(stop int, dir string, now time.Time) *subwayTrain {
	var best *subwayTrain
	for _, t := range m.trains {
		if t.dir != dir || t.stop != stop || t.phase == "moving" || len(t.riders) == 0 || len(t.riders) >= m.d.MaxCapacity {
			continue
		}
		if !t.departAt.After(now) {
			continue
		}
		if best == nil || t.boardAt.Before(best.boardAt) || (t.boardAt.Equal(best.boardAt) && t.id < best.id) {
			best = t
		}
	}
	return best
}

// plan sets a new train's departure (transit_schedule_departure): with min_capacity aboard, in the doors' 3 s;
// else 60 s after the first boarding while someone else is on the platform, 10 s while nobody is. Once the
// doors are closing it no longer moves.
func (m *subway) plan(t *subwayTrain, now time.Time) {
	doors := m.secs(m.d.Doors)
	if !t.departAt.IsZero() && t.departAt.Sub(now) <= doors {
		return
	}
	var target time.Time
	if len(t.riders) >= m.d.MinCapacity {
		target = now.Add(doors)
	} else {
		wait := m.secs(m.d.Lonely)
		if m.platformOthers(t) > 0 {
			wait = m.secs(m.d.Initial)
		}
		target = t.boardAt.Add(wait)
	}
	if target.Before(now.Add(doors)) {
		target = now.Add(doors)
	}
	t.departAt = target
}

// platformOthers counts the players on the train's platform who are not aboard it.
func (m *subway) platformOthers(t *subwayTrain) int {
	st := m.byID[t.stop]
	if st == nil {
		return 0
	}
	n := 0
	for _, o := range m.h.StreetLocked(st.Street) {
		if m.riderOf[o.ID()] != t {
			n++
		}
	}
	return n
}

// schedule arms the train's one timer for its next event.
func (m *subway) schedule(t *subwayTrain) {
	if t.stopTimer != nil {
		t.stopTimer()
	}
	t.gen++
	gen := t.gen
	now := m.h.Now()
	var at time.Time
	if t.phase == "moving" {
		at = t.arriveAt
	} else {
		at = t.departAt
	}
	d := at.Sub(now)
	if t.initial && d > subwayRecheck {
		d = subwayRecheck
	}
	if d < 0 {
		d = 0
	}
	t.stopTimer = m.h.After(d, func() {
		if m.trains[t.id] != t || t.gen != gen {
			return
		}
		m.step(t)
	})
}

func (m *subway) step(t *subwayTrain) {
	now := m.h.Now()
	switch {
	case t.phase != "moving" && !now.Before(t.departAt):
		m.depart(t, now)
	case t.phase == "moving" && !now.Before(t.arriveAt):
		m.arrive(t, now)
	case t.initial:
		before := t.departAt
		m.plan(t, now)
		if !t.departAt.Equal(before) {
			m.tell(t)
		}
	}
	if m.trains[t.id] == t {
		m.schedule(t)
	}
}

// depart is transit_depart: boarders who never reached the train are let go, an empty train goes out of service.
func (m *subway) depart(t *subwayTrain, now time.Time) {
	for id, r := range t.riders {
		if !r.on && now.Sub(r.at) > subwayPending {
			delete(t.riders, id)
			delete(m.riderOf, id)
		}
	}
	if len(t.riders) == 0 {
		m.retire(t)
		return
	}
	t.phase = "moving"
	t.initial = false
	t.arriveAt = t.departAt.Add(m.secs(m.d.Travel))
	m.tell(t)
}

// arrive is transit_arrive: the next station, standing departure_timeout.
func (m *subway) arrive(t *subwayTrain, now time.Time) {
	if nx := m.next(t); nx != nil {
		t.stop = nx.ID
	}
	t.phase = "stopped"
	t.departAt = t.arriveAt.Add(m.secs(m.d.Dwell))
	m.tell(t)
}

func (m *subway) retire(t *subwayTrain) {
	if t.stopTimer != nil {
		t.stopTimer()
	}
	for id := range t.riders {
		delete(m.riderOf, id)
	}
	delete(m.trains, t.id)
}

// drop takes a rider off (got off, went elsewhere, gone offline); an empty train goes.
func (m *subway) drop(t *subwayTrain, id int64) {
	if _, ok := t.riders[id]; !ok {
		return
	}
	delete(t.riders, id)
	if m.riderOf[id] == t {
		delete(m.riderOf, id)
	}
	if stop := m.graces[id]; stop != nil {
		stop()
		delete(m.graces, id)
	}
	if len(t.riders) == 0 {
		m.retire(t)
		return
	}
	if t.initial {
		m.plan(t, m.h.Now())
		m.schedule(t)
	}
	m.tell(t)
}

// onJoin: on the train's street the rider is aboard; joining any other street but the platform they boarded
// from (a stop, the Map, a teleport) takes them off.
func (m *subway) onJoin(cl *Client) {
	t := m.riderOf[cl.ID()]
	if t == nil {
		return
	}
	r := t.riders[cl.ID()]
	if stop := m.graces[cl.ID()]; stop != nil {
		stop()
		delete(m.graces, cl.ID())
	}
	if cl.Street() == t.street {
		r.on = true
		m.tellOne(t, cl)
		return
	}
	if st := m.byID[t.stop]; !r.on && st != nil && cl.Street() == st.Street {
		return // a fresh join on the platform before stepping aboard
	}
	m.drop(t, cl.ID())
}

func (m *subway) onDisconnect(cl *Client) {
	id := cl.ID()
	t := m.riderOf[id]
	if t == nil {
		return
	}
	if stop := m.graces[id]; stop != nil {
		stop()
	}
	m.graces[id] = m.h.After(subwayGrace, func() {
		delete(m.graces, id)
		if m.h.ClientLocked(id) != nil {
			return
		}
		if t2 := m.riderOf[id]; t2 != nil {
			m.drop(t2, id)
		}
	})
}

func (m *subway) row(t *subwayTrain) map[string]any {
	now := m.h.Now()
	ms := func(at time.Time) int64 {
		if at.IsZero() || !at.After(now) {
			return 0
		}
		return at.Sub(now).Milliseconds()
	}
	ids := make([]int64, 0, len(t.riders))
	for id := range t.riders {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	riders := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		riders = append(riders, map[string]any{"id": id, "name": t.riders[id].name})
	}
	need := 0
	if t.initial && len(t.riders) < m.d.MinCapacity {
		need = m.d.MinCapacity - len(t.riders)
	}
	out := map[string]any{"t": "subway_train", "train": t.id, "street": t.street, "line": t.dir, "stop": t.stop, "phase": t.phase,
		"initial": t.initial, "need": need, "riders": riders}
	if t.phase == "moving" {
		out["arrive_in"] = ms(t.arriveAt)
		out["depart_in"] = 0
	} else {
		out["depart_in"] = ms(t.departAt)
		out["arrive_in"] = 0
	}
	return out
}

// tell sends the train to every rider online.
func (m *subway) tell(t *subwayTrain) {
	row := m.row(t)
	for id := range t.riders {
		if cl := m.h.ClientLocked(id); cl != nil {
			m.h.SendLocked(cl, row)
		}
	}
}

func (m *subway) tellOne(t *subwayTrain, cl *Client) { m.h.SendLocked(cl, m.row(t)) }
