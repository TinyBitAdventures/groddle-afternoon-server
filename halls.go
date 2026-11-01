package main

// The Bureaucratic Halls between real players (G479, lane MPPUBLIC): everyone
// in a street hall (Gregarious Grange's, the Andra and the Muufo halls; the
// game's scene street HALL_<tsid>) shares its four bureaucrats. The server
// knows which bureaucrat is busy with whom and keeps a line for the ones
// waiting; the visit itself (the greeting, Please Wait, Talk to, the random
// question, the fee) runs in the served player's game as in solo, and what a
// bureaucrat sells is bought there, a loss the ledger always accepts like any
// vendor purchase. Data: data/halls.json from the game's
// tools/build-realtime-halls.mjs.
//
// The rules are GameServerJS items/npc_bureaucrat.js and locations/location.js:
//
//   - isBusy 805-837: a bureaucrat is busy while greeting or helping someone,
//     or while promised to someone (will_help); a promise or a help ends when
//     that player is no longer in the hall (player_exit 705-715, the reminder's
//     check 690-704).
//   - location.js 1076-1092 (bureaucratic_hall_entered) and catalog_buffs.js
//     please_wait on_remove 1650-1662: the nearest free bureaucrat greets you,
//     and after Please Wait the nearest free one helps you (findCloseStack).
//   - getRandomBureaucrat 314-321: the next official named in a hand-off is the
//     first free one in the room's order, or "Hmmm. It seems no one is able to
//     help you with this." when none is.
//   - idle_onMsg 733-765: an idle bureaucrat strolls (25% each interval, x
//     500-1060 at 20-40 px/s) or may turn round; the next interval is 1-10 s.
//   - returnToStart 901-907: a bureaucrat let go walks back to where it was.
//
// The halls themselves were location-managed instances (locations/inc_instances.js
// 1-5, 24-50: an instance is reused while it has room, one player by default),
// "generally one person instance unless people follow each other in" (forum
// general/17265; bugs/5478 "[not a bug] Bureaucratic Hall - Can't See Someone").
//
// Ours:
//   - One hall per door for everyone in it, so players meet there and share its
//     bureaucrats.
//   - When no bureaucrat is free to greet or help you, you wait in the hall's line
//     and the next one freed comes to you (the source simply never came); the
//     line goes in order.
//   - A bureaucrat you hold with no progress for `turn` (60 s: no talk, no answer)
//     is lost to you, as its reminders warn ("Come speak to me or lose your place
//     in line", helping_reminder): it walks back and you are told hall_lost. The
//     source reminded every 8 s for as long as you stayed.
//   - A promised bureaucrat you never reached is let go when your visit ends.
//
// Client to server (all on a hall street):
//
//	{"t":"hall_ask","kind":"greet|help|will","x":1200}   a bureaucrat for you (the nearest free one; will: the first free one)
//	{"t":"hall_as","i":2,"kind":"help"}                    the one promised to you now helps you
//	{"t":"hall_pose","i":2,"x":1180,"face":"left","anim":"walk2"}   where yours stands (others draw it there)
//	{"t":"hall_touch","i":2}                               your visit moved on
//	{"t":"hall_done","i":2,"x":1640}                       yours is back at its place: free again
//
// Server to client:
//
//	{"t":"hall_state","street":"HALL_…","clerks":[{"i","name","by","who","kind","x","face","anim","to","speed"}],"line":[{"id","name","kind"}]}
//	{"t":"hall_given","kind":"greet","i":2}               it is yours (i -1: none free, for will)
//	{"t":"hall_wait","kind":"help","place":2}             all busy: your place in the line
//	{"t":"hall_pose","i":2,"x":1180,"face":"left","anim":"walk2"}   another player's bureaucrat moved
//	{"t":"hall_lost","i":2,"why":"turn"}                  you lost your turn

import (
	_ "embed"
	"encoding/json"
	"math"
	"math/rand"
	"time"
)

//go:embed data/halls.json
var hallsJSON []byte

type hallsData struct {
	Halls []struct {
		Street string `json:"street"`
		Label  string `json:"label"`
	} `json:"halls"`
	Clerks []struct {
		Name string  `json:"name"`
		Home float64 `json:"home"`
	} `json:"clerks"`
	Stroll      [2]float64 `json:"stroll"`
	StrollSpeed [2]float64 `json:"stroll_speed"`
	WalkSpeed   float64    `json:"walk_speed"`
	Reminder    int64      `json:"reminder"`
	Turn        int64      `json:"turn"`
}

const (
	hallTick  = time.Second
	hallWidth = 2212.0 // bureaucratic_hall_bg stage width (js/bureaucratic-hall.js HALL)
)

type hallClerk struct {
	name  string
	home  float64
	by    int64
	who   string
	kind  string // "" (idle) | greet | help | will
	x     float64
	dir   string
	anim  string
	to    float64
	speed float64
	from  time.Time // walking since (zero: standing)

	touched  time.Time
	nextIdle time.Time
}

type hallWaiter struct {
	id   int64
	name string
	kind string
	x    float64
}

type hallRoom struct {
	street string
	clerks []*hallClerk
	line   []*hallWaiter
	stop   func() bool
}

type halls struct {
	h     *Hub
	d     hallsData
	known map[string]bool
	rooms map[string]*hallRoom
	rng   func() float64
}

func init() { modules = append(modules, newHalls) }

func newHalls(h *Hub) Module {
	var d hallsData
	if err := json.Unmarshal(hallsJSON, &d); err != nil || len(d.Halls) == 0 || len(d.Clerks) == 0 {
		panic("realtime: data/halls.json: bad")
	}
	return newHallsWith(h, d)
}

func newHallsWith(h *Hub, d hallsData) *halls {
	m := &halls{h: h, d: d, known: map[string]bool{}, rooms: map[string]*hallRoom{}, rng: rand.Float64}
	for _, hl := range d.Halls {
		m.known[hl.Street] = true
	}
	return m
}

func (m *halls) Register(r *Registry) {
	r.Handle("hall_ask", m.ask)
	r.Handle("hall_as", m.as)
	r.Handle("hall_pose", m.pose)
	r.Handle("hall_touch", m.touch)
	r.Handle("hall_done", m.done)
	r.OnJoin(m.onJoin)
	r.OnLeave(m.onLeave)
}

// where a clerk stands now (a walk moves it at its speed towards to).
func (c *hallClerk) at(now time.Time) float64 {
	if c.from.IsZero() {
		return c.x
	}
	d := c.speed * now.Sub(c.from).Seconds()
	if math.Abs(c.to-c.x) <= d {
		return c.to
	}
	if c.to > c.x {
		return c.x + d
	}
	return c.x - d
}

// settle ends a walk where it has got to.
func (c *hallClerk) settle(now time.Time) {
	c.x = c.at(now)
	c.from = time.Time{}
	c.anim = "idle"
}

func (c *hallClerk) walk(now time.Time, to, speed float64, anim string) {
	c.settle(now)
	c.to = to
	c.speed = speed
	c.from = now
	c.anim = anim
	if to > c.x {
		c.dir = "right"
	} else if to < c.x {
		c.dir = "left"
	}
}

func (m *halls) room(street string) *hallRoom {
	if !m.known[street] {
		return nil
	}
	r := m.rooms[street]
	if r == nil {
		now := m.h.Now()
		r = &hallRoom{street: street}
		for _, c := range m.d.Clerks {
			r.clerks = append(r.clerks, &hallClerk{name: c.Name, home: c.Home, x: c.Home, dir: "left", anim: "idle", nextIdle: now.Add(time.Second)})
		}
		m.rooms[street] = r
		m.arm(r)
	}
	return r
}

func (m *halls) arm(r *hallRoom) {
	r.stop = m.h.After(hallTick, func() {
		if m.rooms[r.street] != r {
			return
		}
		m.tick(r)
		if m.rooms[r.street] == r {
			m.arm(r)
		}
	})
}

// tick: walks end, idle bureaucrats stroll or turn round (idle_onMsg), turns run out.
func (m *halls) tick(r *hallRoom) {
	now := m.h.Now()
	if len(m.h.StreetLocked(r.street)) == 0 {
		m.close(r)
		return
	}
	changed := false
	for i, c := range r.clerks {
		if c.by != 0 {
			if m.d.Turn > 0 && now.Sub(c.touched) >= time.Duration(m.d.Turn)*time.Millisecond {
				who := c.by
				m.free(r, c, now, true)
				if cl := m.h.ClientLocked(who); cl != nil {
					m.h.SendLocked(cl, map[string]any{"t": "hall_lost", "i": i, "why": "turn"})
				}
				changed = true
			}
			continue
		}
		if !c.from.IsZero() {
			if c.at(now) == c.to {
				c.settle(now)
				c.nextIdle = now.Add(time.Second) // pathing status 3: the next interval in 1 s
				changed = true
			}
			continue
		}
		if now.Before(c.nextIdle) {
			continue
		}
		if m.rng() < 0.25 {
			to := m.d.Stroll[0] + math.Floor(m.rng()*(m.d.Stroll[1]-m.d.Stroll[0]+1))
			speed := m.d.StrollSpeed[0] + math.Floor(m.rng()*(m.d.StrollSpeed[1]-m.d.StrollSpeed[0]+1))
			c.walk(now, to, speed, "walk1")
		} else {
			if m.rng() < 0.1 {
				if c.dir == "left" {
					c.dir = "right"
				} else {
					c.dir = "left"
				}
			}
			c.nextIdle = now.Add(time.Duration(1000+m.rng()*9000) * time.Millisecond)
		}
		changed = true
	}
	if m.serve(r, now) {
		changed = true
	}
	if changed {
		m.tell(r, nil)
	}
}

func (m *halls) close(r *hallRoom) {
	if r.stop != nil {
		r.stop()
	}
	delete(m.rooms, r.street)
}

// free lets a bureaucrat go: back to where it started from (returnToStart) when it was taken away, or where
// its player left it.
func (m *halls) free(r *hallRoom, c *hallClerk, now time.Time, walkHome bool) {
	c.by, c.who, c.kind = 0, "", ""
	c.nextIdle = now.Add(time.Second)
	if walkHome && math.Abs(c.at(now)-c.home) > 1 {
		c.walk(now, c.home, m.d.WalkSpeed, "walk1")
	} else {
		c.settle(now)
	}
}

func (m *halls) hold(c *hallClerk, id int64, name, kind string, now time.Time) {
	c.settle(now)
	c.by, c.who, c.kind = id, name, kind
	c.touched = now
}

func (m *halls) holds(r *hallRoom, id int64) (n int, kinds map[string]int) {
	kinds = map[string]int{}
	for i, c := range r.clerks {
		if c.by == id {
			n++
			kinds[c.kind] = i
		}
	}
	return
}

// nearest free bureaucrat to x (findCloseStack), or -1.
func (m *halls) nearest(r *hallRoom, x float64, now time.Time) int {
	best, bd := -1, math.Inf(1)
	for i, c := range r.clerks {
		if c.by != 0 {
			continue
		}
		if d := math.Abs(c.at(now) - x); d < bd {
			best, bd = i, d
		}
	}
	return best
}

// serve gives freed bureaucrats to the line, in order.
func (m *halls) serve(r *hallRoom, now time.Time) bool {
	served := false
	for len(r.line) > 0 {
		a := r.line[0]
		i := m.nearest(r, a.x, now)
		if i < 0 {
			break
		}
		r.line = r.line[1:]
		m.hold(r.clerks[i], a.id, a.name, a.kind, now)
		if cl := m.h.ClientLocked(a.id); cl != nil {
			m.h.SendLocked(cl, map[string]any{"t": "hall_given", "kind": a.kind, "i": i})
		}
		served = true
	}
	if served {
		m.tellPlaces(r)
	}
	return served
}

func (m *halls) tellPlaces(r *hallRoom) {
	for n, a := range r.line {
		if cl := m.h.ClientLocked(a.id); cl != nil {
			m.h.SendLocked(cl, map[string]any{"t": "hall_wait", "kind": a.kind, "place": n + 1})
		}
	}
}

func (m *halls) mine(cl *Client, i int) (*hallRoom, *hallClerk) {
	r := m.rooms[cl.Street()]
	if r == nil || i < 0 || i >= len(r.clerks) || r.clerks[i].by != cl.ID() {
		return nil, nil
	}
	return r, r.clerks[i]
}

func (m *halls) ask(cl *Client, frame json.RawMessage) {
	var msg struct {
		Kind string   `json:"kind"`
		X    *float64 `json:"x"`
	}
	json.Unmarshal(frame, &msg)
	r := m.rooms[cl.Street()]
	if r == nil || (msg.Kind != "greet" && msg.Kind != "help" && msg.Kind != "will") {
		return
	}
	now := m.h.Now()
	x, _ := cl.Pos()
	if msg.X != nil && !math.IsNaN(*msg.X) && !math.IsInf(*msg.X, 0) {
		x = math.Max(0, math.Min(hallWidth, *msg.X))
	}
	given := func(i int) { m.h.SendLocked(cl, map[string]any{"t": "hall_given", "kind": msg.Kind, "i": i}) }
	n, held := m.holds(r, cl.ID())
	if i, ok := held[msg.Kind]; ok {
		given(i)
		return
	}
	for p, a := range r.line {
		if a.id == cl.ID() {
			m.h.SendLocked(cl, map[string]any{"t": "hall_wait", "kind": a.kind, "place": p + 1})
			return
		}
	}
	// A visit holds at most its helper and the one promised next (helping + will_help).
	if n >= 2 {
		given(-1)
		return
	}
	i := -1
	if msg.Kind == "will" {
		for k, c := range r.clerks { // getRandomBureaucrat: the first free one
			if c.by == 0 {
				i = k
				break
			}
		}
		if i >= 0 {
			m.hold(r.clerks[i], cl.ID(), cl.Name(), "will", now)
			m.tell(r, nil)
		}
		given(i)
		return
	}
	if len(r.line) == 0 {
		i = m.nearest(r, x, now)
	}
	if i < 0 {
		r.line = append(r.line, &hallWaiter{id: cl.ID(), name: cl.Name(), kind: msg.Kind, x: x})
		m.h.SendLocked(cl, map[string]any{"t": "hall_wait", "kind": msg.Kind, "place": len(r.line)})
		m.tell(r, nil)
		return
	}
	m.hold(r.clerks[i], cl.ID(), cl.Name(), msg.Kind, now)
	given(i)
	m.tell(r, nil)
}

func (m *halls) as(cl *Client, frame json.RawMessage) {
	var msg struct {
		I    int    `json:"i"`
		Kind string `json:"kind"`
	}
	if json.Unmarshal(frame, &msg) != nil || (msg.Kind != "help" && msg.Kind != "will" && msg.Kind != "greet") {
		return
	}
	r, c := m.mine(cl, msg.I)
	if c == nil {
		return
	}
	c.kind = msg.Kind
	c.touched = m.h.Now()
	m.tell(r, nil)
}

func (m *halls) pose(cl *Client, frame json.RawMessage) {
	var msg struct {
		I    int     `json:"i"`
		X    float64 `json:"x"`
		Dir  string  `json:"face"`
		Anim string  `json:"anim"`
	}
	if json.Unmarshal(frame, &msg) != nil || math.IsNaN(msg.X) || math.IsInf(msg.X, 0) {
		return
	}
	r, c := m.mine(cl, msg.I)
	if c == nil {
		return
	}
	c.from = time.Time{}
	c.x = math.Round(math.Max(0, math.Min(hallWidth, msg.X)))
	if msg.Dir == "left" || msg.Dir == "right" {
		c.dir = msg.Dir
	}
	if wordRe.MatchString(msg.Anim) {
		c.anim = msg.Anim
	}
	c.touched = m.h.Now()
	m.h.BroadcastStreetLocked(r.street, map[string]any{"t": "hall_pose", "i": msg.I, "x": c.x, "face": c.dir, "anim": c.anim}, cl.ID())
}

func (m *halls) touch(cl *Client, frame json.RawMessage) {
	var msg struct {
		I int `json:"i"`
	}
	json.Unmarshal(frame, &msg)
	if _, c := m.mine(cl, msg.I); c != nil {
		c.touched = m.h.Now()
	}
}

func (m *halls) done(cl *Client, frame json.RawMessage) {
	var msg struct {
		I int      `json:"i"`
		X *float64 `json:"x"`
	}
	json.Unmarshal(frame, &msg)
	r, c := m.mine(cl, msg.I)
	if c == nil {
		return
	}
	now := m.h.Now()
	if msg.X != nil && !math.IsNaN(*msg.X) && !math.IsInf(*msg.X, 0) {
		c.from = time.Time{}
		c.x = math.Round(math.Max(0, math.Min(hallWidth, *msg.X)))
	}
	m.free(r, c, now, false)
	m.serve(r, now)
	m.tell(r, nil)
}

func (m *halls) onJoin(cl *Client) {
	if r := m.room(cl.Street()); r != nil {
		m.tell(r, cl)
	}
}

// onLeave is player_exit / location.js 810-819: out of the hall, your place in line and your bureaucrats go.
func (m *halls) onLeave(cl *Client, street string) {
	r := m.rooms[street]
	if r == nil {
		return
	}
	now := m.h.Now()
	changed := false
	for k := 0; k < len(r.line); k++ {
		if r.line[k].id == cl.ID() {
			r.line = append(r.line[:k], r.line[k+1:]...)
			k--
			changed = true
		}
	}
	for _, c := range r.clerks {
		if c.by == cl.ID() {
			m.free(r, c, now, true)
			changed = true
		}
	}
	if len(m.h.StreetLocked(street)) == 0 {
		m.close(r)
		return
	}
	if changed {
		m.serve(r, now)
		m.tellPlaces(r)
		m.tell(r, nil)
	}
}

func (m *halls) row(r *hallRoom) map[string]any {
	now := m.h.Now()
	clerks := make([]map[string]any, 0, len(r.clerks))
	for i, c := range r.clerks {
		row := map[string]any{"i": i, "name": c.name, "by": c.by, "who": c.who, "kind": c.kind, "x": math.Round(c.at(now)), "face": c.dir, "anim": c.anim}
		if !c.from.IsZero() && c.at(now) != c.to {
			row["to"] = c.to
			row["speed"] = c.speed
		}
		clerks = append(clerks, row)
	}
	line := make([]map[string]any, 0, len(r.line))
	for _, a := range r.line {
		line = append(line, map[string]any{"id": a.id, "name": a.name, "kind": a.kind})
	}
	return map[string]any{"t": "hall_state", "street": r.street, "clerks": clerks, "line": line}
}

// tell sends the hall's state to one player, or (nil) everyone in it.
func (m *halls) tell(r *hallRoom, cl *Client) {
	if cl != nil {
		m.h.SendLocked(cl, m.row(r))
		return
	}
	m.h.BroadcastStreetLocked(r.street, m.row(r), 0)
}
