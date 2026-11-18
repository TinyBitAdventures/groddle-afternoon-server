package main

// Thanksgiving Day's feast (G1009, Austin 2026-11-18; the game's js/feast-day.js and
// features/feast-day-feature.js). Ours: Glitch never ran a Thanksgiving event (the game's
// inventions register, feast-day-piles).
//
// On Thanksgiving Day (00:00-23:59 on Ur's clock, the day Pot Luck Day's window is built around)
// the week's Harvest Table food spills over Groddle Forest Junction as eat-in-place piles, as
// Hell One's grapes lie there to be squished: never picked up, never in a pack. The server owns
// them: feastDaySpots right of the table, each pile one food with feastDayBites bites (one laid dish
// or crop's worth). Eat takes one bite and the first eater wins it. An emptied spot refills
// feastDayRefill later with a class drawn from the week's per-class lay counts (harvest_table.go
// feast.counts), weighted by what is left of each, as a Glitch spawner draws its next spawn
// (items/spawner.js 23-31 spawn_class/max_count/spawn_interval, 163-174 the timer restarting).
// A week laid with fewer than feastDayPotDishes dishes is topped up to that many by Pot, with his own
// harvest dishes (js/pot-luck-feast.js HARVEST_DISHES). Each player eats at most feastDayCap bites
// that day. At midnight the leftovers are packed away for everyone (a timer at the day's end, as the
// Harvest Table's own end timer); the table itself stays until Pot Luck Day ends.
//
// The energy, mood, fullness and badges of a bite are the game's (it eats from a one-item stock,
// game.js eatFromStock): the game refuses a bite at full energy, stuffed or dead before it asks.
//
// Client to server:
//
//	{"t":"feastday_eat","s":3,"n":17}   a bite from spot s, pile serial n (a refilled spot is a new pile)
//	{"t":"feastday_sync"}               asks for feastday_state again
//
// Refused as feastday_no {"s","code","message"}: another street ("not_here"), not Thanksgiving Day
// ("no_season"), this player's bites used up ("full"), too fast ("slow_down"), a pile already
// eaten or refilled ("gone").
//
// Server to client:
//
//	{"t":"feastday_state","street":"G…","on":true,"piles":[{"s","n","c","b","x"}],"left":57,"ate":2,"cap":11}
//	{"t":"feastday_pile","street":"G…","pile":{…},"left":56}                 a pile changed (b 0: emptied)
//	{"t":"feastday_ate","street":"G…","pile":{…},"left":56,"ate":3,"c":"corn"} to the eater
//	{"t":"feastday_left","street":"G…","left":59}                            a lay on the table joined the pool
//
// The feast survives a restart (-feast-day-file, default realtime-feast-day.json beside
// -secret-file; off: memory only).

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"time"
)

const (
	// Piles on the street at once, one per spot (G1009 "about 20"; G1015a: 16 fit clear of the
	// street tree's trunk).
	feastDayPiles = 16
	// A thin week is topped up to this many dishes by Pot (G1009: 20 dishes' worth).
	feastDayPotDishes = 20
	// Bites in one pile: one laid dish or crop's worth.
	feastDayBites = 3
	// Bites one player may eat on the day.
	feastDayCap = 11
	// An emptied spot is laid again this long after.
	feastDayRefill = 20 * time.Second
	// A player's bites come no faster than this (ours).
	feastDayEvery = 300 * time.Millisecond

	feastDayTextNotHere  = "The feast is in Groddle Forest Junction."
	feastDayTextNoSeason = "The feast is only on Thanksgiving Day."
	feastDayTextFull     = "You have had all 11 of your feast bites today. Pot says save room for next year."
	feastDayTextGone     = "Too slow! Somebody else ate that bite."
)

// Where the piles lie, in the street's source x, as runs {from, to, spots}: right of the Harvest
// Table (its right end ~202), either side of the spice tree's trunk (x 422, trunk and roots ~379-451;
// G1015a), ending at 700 clear of the shrine online and the Hardware Vendor solo (~790, base from
// ~730) and the subway door (~1099); signpost 2 (-291) is left of the table.
var feastDayRuns = [...][3]int{{230, 345, 6}, {490, 700, 10}}

var feastDaySpots = func() [feastDayPiles]int {
	var out [feastDayPiles]int
	i := 0
	for _, r := range feastDayRuns {
		from, to, n := r[0], r[1], r[2]-1
		for k := 0; k <= n; k++ {
			out[i] = from + (k*(to-from)+n/2)/n
			i++
		}
	}
	return out
}()

// Pot's own harvest dishes, his top-up for a thin week (js/pot-luck-feast.js HARVEST_DISHES).
var potsHarvestDishes = []string{"pumpkin_pie", "hot_potatoes", "wavy_gravy", "corny_fritter", "potcorn"}

var feastDayFileFlag = flag.String("feast-day-file", "", "file that keeps Thanksgiving Day's feast: its piles, what was put out and each player's bites (default: realtime-feast-day.json beside -secret-file; off: memory only)")

// feastDayFileForTest lets a test give the module a state file; under go test the flag is ignored.
var feastDayFileForTest string

type feastPile struct {
	S int    `json:"s"`
	N int    `json:"n"`
	C string `json:"c"`
	B int    `json:"b"`
	X int    `json:"x"`
}

// feastDaySaved is the state file.
type feastDaySaved struct {
	Key    string         `json:"key"`
	Piles  []feastPile    `json:"piles"`
	Placed map[string]int `json:"placed"`
	Pot    map[string]int `json:"pot"`
	Ate    map[int64]int  `json:"ate"`
	Serial int            `json:"serial"`
}

type feastDay struct {
	h      *Hub
	key    string // "feast_day:2026" on the day, "" otherwise
	piles  [feastDayPiles]feastPile
	placed map[string]int // dishes put out per class (Pot's too)
	pot    map[string]int // Pot's top-up, decided as the day starts
	ate    map[int64]int  // player -> bites eaten today
	serial int
	eatAt  map[int64]time.Time
	refill [feastDayPiles]func() bool
	ending func() bool
	roll   func() float64
	path   string
}

func init() { modules = append(modules, newFeastDay) }

func newFeastDay(h *Hub) Module {
	m := &feastDay{h: h, placed: map[string]int{}, pot: map[string]int{}, ate: map[int64]int{}, eatAt: map[int64]time.Time{}, roll: rand.Float64}
	if m.path = statePath(feastDayFileForTest, feastDayFileFlag, "realtime-feast-day.json"); m.path != "" {
		var s feastDaySaved
		if ok, err := readStateFile("feast day", m.path, &s); err != nil {
			log.Printf("feast day %s: %v (the feast is kept in memory only)", m.path, err)
			m.path = ""
		} else if ok {
			m.load(s)
		}
	}
	return m
}

// load takes a saved feast, keeping only what makes sense (spots in range, food, bites 1..3).
func (m *feastDay) load(s feastDaySaved) {
	m.key, m.serial = s.Key, s.Serial
	for _, p := range s.Piles {
		if p.S >= 0 && p.S < feastDayPiles && p.B > 0 && p.B <= feastDayBites && feastTakes(p.C) {
			p.X = feastDaySpots[p.S]
			m.piles[p.S] = p
			m.serial = max(m.serial, p.N)
		}
	}
	for c, n := range s.Placed {
		if n > 0 && feastTakes(c) {
			m.placed[c] = n
		}
	}
	for c, n := range s.Pot {
		if n > 0 && feastTakes(c) {
			m.pot[c] = n
		}
	}
	for id, n := range s.Ate {
		if n > 0 {
			m.ate[id] = min(n, feastDayCap)
		}
	}
}

func (m *feastDay) Register(r *Registry) {
	r.Handle("feastday_eat", m.eat)
	r.Handle("feastday_sync", func(cl *Client, _ json.RawMessage) { m.joined(cl) })
	r.OnJoin(m.joined)
	r.OnDisconnect(func(cl *Client) { delete(m.eatAt, cl.ID()) })
	r.OnShutdown(func() { m.saveSoon() })
	// harvest_table.go calls it after each lay: on the day a new dish joins the pool at once.
	r.Provide("feastday.laid", m.laid)
}

// laid is a lay on the Harvest Table during the day: spots the pool had left empty are laid again
// feastDayRefill later, and the street hears the servings left.
func (m *feastDay) laid() {
	if m.key == "" {
		return
	}
	for s, p := range m.piles {
		if p.B == 0 {
			m.armRefill(s)
		}
	}
	m.broadcast(map[string]any{"t": "feastday_left", "left": m.left()}, 0)
}

// thanksgiving is the day of November that Thanksgiving falls on in year y (potLuckWindow's middle).
func thanksgiving(y int) int {
	from, _ := potLuckWindow(y)
	return from + 4
}

// feastDayKey is the day's key on the calendar the seasons read, or "" on any other day.
func feastDayKey(now time.Time) string {
	y, mo, d, _ := seasonDate(now)
	if mo != time.November || d != thanksgiving(y) {
		return ""
	}
	return fmt.Sprintf("feast_day:%d", y)
}

// feastDayLeft is how long the day has left (0 off it; a held QA date never ends).
func feastDayLeft(now time.Time) time.Duration {
	if feastDayKey(now) == "" {
		return 0
	}
	if _, _, _, held := seasonDate(now); held {
		return 365 * 24 * time.Hour
	}
	t := now.In(urZone)
	end := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, urZone)
	return max(time.Second, end.Sub(now))
}

// counts is the week's lays per class (harvest_table.go), or none without the table.
func (m *feastDay) counts() map[string]int {
	if fn, ok := m.h.ProvidedLocked("feast.counts").(func() map[string]int); ok {
		return fn()
	}
	return map[string]int{}
}

// remaining is what is left to put out of each class: the week's lays and Pot's, less what went out.
func (m *feastDay) remaining() map[string]int {
	out := map[string]int{}
	for c, n := range m.counts() {
		out[c] += n
	}
	for c, n := range m.pot {
		out[c] += n
	}
	for c, n := range out {
		if left := n - m.placed[c]; left > 0 {
			out[c] = left
		} else {
			delete(out, c)
		}
	}
	return out
}

// left is the servings (bites) still to eat: the piles' and feastDayBites for every dish to come.
func (m *feastDay) left() int {
	n := 0
	for _, p := range m.piles {
		n += p.B
	}
	for _, r := range m.remaining() {
		n += r * feastDayBites
	}
	return n
}

// draw picks the next class, weighted by what is left of each (spawner.js), or "" when none is.
func (m *feastDay) draw() string {
	rem := m.remaining()
	keys := make([]string, 0, len(rem))
	total := 0
	for c, n := range rem {
		keys = append(keys, c)
		total += n
	}
	if total == 0 {
		return ""
	}
	sort.Strings(keys)
	pick := int(m.roll() * float64(total))
	for _, c := range keys {
		if pick < rem[c] {
			return c
		}
		pick -= rem[c]
	}
	return keys[len(keys)-1]
}

// lay puts a new pile on spot s; reports whether there was anything left to put there.
func (m *feastDay) lay(s int) bool {
	c := m.draw()
	if c == "" {
		return false
	}
	m.serial++
	m.placed[c]++
	m.piles[s] = feastPile{S: s, N: m.serial, C: c, B: feastDayBites, X: feastDaySpots[s]}
	return true
}

// start lays the day's feast: Pot's top-up for a thin week, then every spot.
func (m *feastDay) start() {
	total := 0
	for _, n := range m.counts() {
		total += n
	}
	for i := 0; total+i < feastDayPotDishes; i++ {
		m.pot[potsHarvestDishes[i%len(potsHarvestDishes)]]++
	}
	for s := range m.piles {
		m.lay(s)
	}
	log.Printf("feast day: laid out (%s, %d laid this week, Pot adds %d, %d servings)", m.key, total, max(0, feastDayPotDishes-total), m.left())
}

// settle starts or packs away the feast as the calendar says, and arms the day's end and any refills
// a restart left waiting. Reports whether the feast changed (a new day, or packed away).
func (m *feastDay) settle() bool {
	now := m.h.Now()
	key := feastDayKey(now)
	changed := false
	if key != m.key {
		had := m.key != ""
		for s, stop := range m.refill {
			if stop != nil {
				stop()
				m.refill[s] = nil
			}
		}
		m.key, m.piles, m.placed, m.pot, m.ate = key, [feastDayPiles]feastPile{}, map[string]int{}, map[string]int{}, map[int64]int{}
		if had {
			log.Printf("feast day: the leftovers are packed away")
		}
		if key != "" {
			m.start()
		}
		m.saveSoon()
		changed = true
	}
	if m.key == "" {
		return changed
	}
	if m.ending == nil {
		if left := feastDayLeft(now); left > 0 && left < 300*24*time.Hour {
			m.ending = m.h.After(left+time.Second, func() {
				m.ending = nil
				if m.settle() {
					m.broadcastState()
				}
			})
		}
	}
	for s, p := range m.piles {
		if p.B == 0 {
			m.armRefill(s)
		}
	}
	return changed
}

// armRefill lays spot s again feastDayRefill from now, if nothing is waiting there already.
func (m *feastDay) armRefill(s int) {
	if m.refill[s] != nil || len(m.remaining()) == 0 {
		return
	}
	key := m.key
	m.refill[s] = m.h.After(feastDayRefill, func() {
		m.refill[s] = nil
		if m.key != key || m.piles[s].B > 0 {
			return
		}
		if m.lay(s) {
			m.saveSoon()
			m.broadcast(map[string]any{"t": "feastday_pile", "pile": m.piles[s], "left": m.left()}, 0)
		}
	})
}

func (m *feastDay) shown() []feastPile {
	out := []feastPile{}
	for _, p := range m.piles {
		if p.B > 0 {
			out = append(out, p)
		}
	}
	return out
}

func (m *feastDay) state(street string, id int64) map[string]any {
	if m.key == "" {
		return map[string]any{"t": "feastday_state", "street": street, "on": false, "piles": []feastPile{}, "left": 0, "ate": 0, "cap": feastDayCap}
	}
	return map[string]any{"t": "feastday_state", "street": street, "on": true, "piles": m.shown(), "left": m.left(), "ate": m.ate[id], "cap": feastDayCap}
}

// broadcast sends msg to both forms of the table's street (G… and L…), each with its own street.
func (m *feastDay) broadcast(msg map[string]any, except int64) {
	for _, street := range []string{"G" + feastStreetKey, "L" + feastStreetKey} {
		out := map[string]any{"street": street}
		for k, v := range msg {
			out[k] = v
		}
		m.h.BroadcastStreetLocked(street, out, except)
	}
}

// broadcastState tells everyone on the street the feast as it is (each their own bites).
func (m *feastDay) broadcastState() {
	for _, street := range []string{"G" + feastStreetKey, "L" + feastStreetKey} {
		for _, cl := range m.h.StreetLocked(street) {
			m.h.SendLocked(cl, m.state(street, cl.ID()))
		}
	}
}

func (m *feastDay) joined(cl *Client) {
	street := cl.Street()
	if !isFeastStreet(street) {
		return
	}
	if m.settle() {
		m.broadcastState()
		return
	}
	m.h.SendLocked(cl, m.state(street, cl.ID()))
}

func (m *feastDay) refuse(cl *Client, s int, code, message string) {
	m.h.SendLocked(cl, map[string]any{"t": "feastday_no", "s": s, "code": code, "message": message})
}

// eat is one bite: the refusals, then the bite is the first eater's.
func (m *feastDay) eat(cl *Client, frame json.RawMessage) {
	var f struct {
		S *int `json:"s"`
		N int  `json:"n"`
	}
	if json.Unmarshal(frame, &f) != nil || f.S == nil {
		return
	}
	s := *f.S
	street := cl.Street()
	if !isFeastStreet(street) {
		m.refuse(cl, s, "not_here", feastDayTextNotHere)
		return
	}
	if m.settle() {
		m.broadcastState()
	}
	if m.key == "" {
		m.refuse(cl, s, "no_season", feastDayTextNoSeason)
		return
	}
	id := cl.ID()
	if m.ate[id] >= feastDayCap {
		m.refuse(cl, s, "full", feastDayTextFull)
		return
	}
	now := m.h.Now()
	if at, ok := m.eatAt[id]; ok && now.Sub(at) < feastDayEvery {
		m.refuse(cl, s, "slow_down", "Slow down a little.")
		return
	}
	if s < 0 || s >= feastDayPiles || m.piles[s].B == 0 || m.piles[s].N != f.N {
		m.refuse(cl, s, "gone", feastDayTextGone)
		return
	}
	m.eatAt[id] = now
	p := &m.piles[s]
	p.B--
	m.ate[id]++
	pile := *p
	if p.B == 0 {
		m.armRefill(s)
	}
	m.saveSoon()
	left := m.left()
	m.broadcast(map[string]any{"t": "feastday_pile", "pile": pile, "left": left}, id)
	m.h.SendLocked(cl, map[string]any{"t": "feastday_ate", "street": street, "pile": pile, "left": left, "ate": m.ate[id], "c": pile.C})
}

func (m *feastDay) saved() feastDaySaved {
	return feastDaySaved{Key: m.key, Piles: m.shown(), Placed: m.placed, Pot: m.pot, Ate: m.ate, Serial: m.serial}
}

// saveSoon writes the feast off the hub lock (statewrite.go).
func (m *feastDay) saveSoon() {
	if m.path == "" {
		return
	}
	b, _ := json.Marshal(m.saved())
	if err := writeStateSoon(m.path, b); err != nil {
		log.Printf("feast day %s: %v", m.path, err)
	}
}
