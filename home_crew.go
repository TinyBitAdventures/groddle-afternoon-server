package main

// Crews on a home street (register G929, G947; shared-play audit M3): help mine at a rock the
// owner cultivated and help dig at their Dirt Pile, for the owner and everyone visiting. In the
// original a home street's rocks and piles were the ordinary items (items/include/mining.js,
// items/dirt_pile.js), so their crews were too: whoever starts a swing while others swing helps
// each of them (mining.js addMiner 68-91, removeMiner 93-122; dirt_pile.js addDigger 153-166,
// removeDigger 497-521), the verb reads "Help mine" or "Help dig" (getVerbLabel), a landed swing
// gets min(i+1, 4) chunks for each of up to 9 helps (mining.js 327-342), a landed dig 1-3 items
// and a Loam chance (dirt_pile.js 345-358), and the helper counts help_mine (mining.js 501-505)
// or the helped digger helped_dig (dirt_pile.js 481-483). Nothing there asks for the owner or a key.
//
// What is on a home street is the site's and the owner's save (home_yard.go, the site's
// HomeYard.php), so this module keeps only the crews: who swings at which rock or pile there now
// and who helped whom, by the ids the game and the site use (rock:<i>, node:dirt_pile:<n>). A
// visitor's landed swing or dig goes to the site through home_yard, which takes the crew's bonus
// from here (homecrew.land) and has the site roll it into what it takes and credits. The owner's
// own swing runs in the owner's game on the owner's own rock, so the owner asks for the bonus as
// the swing lands ("land") and the owner's game applies it.
//
// Client to server:
//
//	{"t":"home_crew","owner":7,"id":"rock:0","do":"start|stop"}        a swing or dig starts, or is cancelled
//	{"t":"home_crew","owner":7,"id":"rock:0","do":"land","req":3}      the owner's own swing landed
//
// Server to client:
//
//	{"t":"home_crew_ok","owner":7,"id":"rock:0","helping":true}        answers a start: someone else was already at it
//	{"t":"home_crew_help","owner":7,"id":"rock:0","req":3,"help":{"bonus":3,"by":["Bo"]}}
//	   (a Dirt Pile: "help":{"n":2,"items":1,"class":"earth","loam":false,"by":[…]}; "help":null when nobody helped)
//	{"t":"home_crews","owner":7,"crews":{"rock:0":[7,9]}}              who is at what now: to the street on a change,
//	                                                                    and to someone who arrives while a crew works
//
// A player who leaves the street is off every crew there, their helps taken back
// (miningSanityCheck 53-66, dirt_pile.js onPlayerExit 491-495).

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	homeCrewBurst = 20
	homeCrewEvery = 250 * time.Millisecond
)

// homeCrewIDRe names what has a crew on a home street: a rock, and a Dirt Pile (an Ice Nubbin's
// scrapers help too, include/scraping.js, but none can be cultivated; a Mortar Barnacle, a Jellisac
// and a Peat Bog have no helpers in the source).
var homeCrewIDRe = regexp.MustCompile(`^(rock:[0-9]{1,2}|node:dirt_pile:[0-9]{1,2})$`)

func init() { modules = append(modules, newHomeCrew) }

type homeCrew struct {
	h *Hub
	// crews: home street -> id -> its crew (a liveObj's miners and helpers; mining.js keeps them on the rock).
	crews map[string]map[string]*liveObj
	// starts: when each miner's swing started ("<street>|<id>|<who>", Unix ms), so the site can pay a
	// swing that started while the rock had chunks after another emptied it (mining.js 215-217 checks
	// isUseable only as the swing starts; onMiningComplete 293-383 never again).
	starts map[string]int64
	rates  map[int64]*bucket
	roll   func() float64
}

func newHomeCrew(h *Hub) Module {
	return &homeCrew{h: h, crews: map[string]map[string]*liveObj{}, starts: map[string]int64{}, rates: map[int64]*bucket{}, roll: rand.Float64}
}

func (m *homeCrew) Register(r *Registry) {
	r.Handle("home_crew", m.handle)
	r.OnJoin(m.joined)
	r.OnLeave(m.left)
	r.OnDisconnect(func(cl *Client) { delete(m.rates, cl.ID()) })
	r.Provide("homecrew.land", m.land)
	r.Provide("homecrew.started", m.started)
}

func startKey(street, id string, who int64) string {
	return street + "|" + id + "|" + strconv.FormatInt(who, 10)
}

// started is when who's swing at owner's id started (Unix ms), or 0 when who is not at it.
func (m *homeCrew) started(owner int64, id string, who int64) int64 {
	street := homeStreetPrefix + strconv.FormatInt(owner, 10)
	if ob := m.crew(street, id, false); ob == nil || !ob.mining(who) {
		return 0
	}
	return m.starts[startKey(street, id, who)]
}

// homeCrewOwner is the owner of a home street (HOME_<id>), or 0.
func homeCrewOwner(street string) int64 {
	if !strings.HasPrefix(street, homeStreetPrefix) {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(street, homeStreetPrefix), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func (m *homeCrew) blocked(owner, who int64) bool {
	if owner == who {
		return false
	}
	fn, _ := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return fn != nil && (fn(owner, who) || fn(who, owner))
}

func (m *homeCrew) handle(cl *Client, frame json.RawMessage) {
	b := m.rates[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.rates[cl.ID()] = b
	}
	if !b.take(m.h.Now(), homeCrewBurst, homeCrewEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	var msg struct {
		Owner int64  `json:"owner"`
		ID    string `json:"id"`
		Do    string `json:"do"`
		Req   any    `json:"req"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.Owner <= 0 || !homeCrewIDRe.MatchString(msg.ID) {
		return
	}
	street := homeStreetPrefix + strconv.FormatInt(msg.Owner, 10)
	if cl.Street() != street || m.blocked(msg.Owner, cl.ID()) {
		return
	}
	switch msg.Do {
	case "start":
		ob := m.crew(street, msg.ID, true)
		// getVerbLabel: "Help mine" / "Help dig" when someone else is already at it.
		ob.removeMiner(cl.ID(), true)
		helping := len(ob.miners) > 0
		ob.addMiner(cl.ID())
		m.starts[startKey(street, msg.ID, cl.ID())] = m.h.Now().UnixMilli()
		m.h.SendLocked(cl, map[string]any{"t": "home_crew_ok", "owner": msg.Owner, "id": msg.ID, "helping": helping})
		m.tell(street, nil)
	case "stop":
		if ob := m.crew(street, msg.ID, false); ob != nil && ob.mining(cl.ID()) {
			ob.removeMiner(cl.ID(), true)
			delete(m.starts, startKey(street, msg.ID, cl.ID()))
			m.forget(street, msg.ID)
			m.tell(street, nil)
		}
	case "land":
		// The owner's own swing or dig, which ran on the owner's own rock or pile. A visitor's lands
		// through home_yard, where the site credits the bonus.
		if cl.ID() != msg.Owner {
			return
		}
		req := msg.Req
		if f, ok := req.(float64); !ok || f < 0 || f > 1e9 {
			req = nil
		}
		help := m.land(msg.Owner, msg.ID, cl.ID())
		var out any
		if help != nil {
			out = help
		}
		m.h.SendLocked(cl, map[string]any{"t": "home_crew_help", "owner": msg.Owner, "id": msg.ID, "req": req, "help": out})
	}
}

func (m *homeCrew) crew(street, id string, create bool) *liveObj {
	list := m.crews[street]
	if list == nil {
		if !create {
			return nil
		}
		list = map[string]*liveObj{}
		m.crews[street] = list
	}
	ob := list[id]
	if ob == nil && create {
		ob = &liveObj{}
		list[id] = ob
	}
	return ob
}

// forget drops a crew nobody is in (its helps went with its last miner).
func (m *homeCrew) forget(street, id string) {
	if ob := m.crews[street][id]; ob != nil && len(ob.miners) == 0 {
		delete(m.crews[street], id)
	}
	if len(m.crews[street]) == 0 {
		delete(m.crews, street)
	}
}

// view is who is at what on a home street now.
func (m *homeCrew) view(street string) map[string][]int64 {
	out := map[string][]int64{}
	for id, ob := range m.crews[street] {
		if len(ob.miners) > 0 {
			out[id] = append([]int64(nil), ob.miners...)
		}
	}
	return out
}

// tell sends the street's crews to everyone on it (or to one arrival).
func (m *homeCrew) tell(street string, to *Client) {
	owner := homeCrewOwner(street)
	if owner == 0 {
		return
	}
	frame := map[string]any{"t": "home_crews", "owner": owner, "crews": m.view(street)}
	if to != nil {
		m.h.SendLocked(to, frame)
		return
	}
	for _, o := range m.h.StreetLocked(street) {
		m.h.SendLocked(o, frame)
	}
}

func (m *homeCrew) joined(cl *Client) {
	if street := cl.Street(); homeCrewOwner(street) != 0 && len(m.crews[street]) > 0 {
		m.tell(street, cl)
	}
}

func (m *homeCrew) left(cl *Client, street string) {
	changed := false
	for id, ob := range m.crews[street] {
		if ob.mining(cl.ID()) {
			ob.removeMiner(cl.ID(), true)
			delete(m.starts, startKey(street, id, cl.ID()))
			m.forget(street, id)
			changed = true
		}
	}
	if changed {
		m.tell(street, nil)
	}
}

// land is a swing or dig by who landing at owner's home (mining.js onMiningComplete 327-383,
// dirt_pile.js onDigComplete 345-485): the crew's help it had, then who is off the crew, their
// helps kept for the others (removeMiner(pc, false)). nil when nobody helped (or who never started).
// A rock's help is {"bonus","by"}; a Dirt Pile's {"n","items","class","loam","by"}. Run under the hub
// lock (home_yard calls it as a visitor's act reaches it).
func (m *homeCrew) land(owner int64, id string, who int64) map[string]any {
	street := homeStreetPrefix + strconv.FormatInt(owner, 10)
	ob := m.crew(street, id, false)
	if ob == nil || !ob.mining(who) {
		return nil
	}
	helps, n, by := 0, 0, []string{}
	for hid, times := range ob.helpers[who] {
		helps += times
		n++
		if c := m.h.ClientLocked(hid); c != nil {
			by = append(by, c.Name())
		}
	}
	sort.Strings(by)
	ob.removeMiner(who, false)
	delete(m.starts, startKey(street, id, who))
	m.forget(street, id)
	m.tell(street, nil)
	if n == 0 {
		return nil
	}
	if strings.HasPrefix(id, "rock:") {
		return map[string]any{"bonus": helpMineBonus(helps), "by": by}
	}
	items, loam := homeDigHelp(n, m.roll())
	class := "earth"
	if loam {
		class = "loam"
	}
	return map[string]any{"n": n, "items": items, "class": class, "loam": loam, "by": by}
}

// homeDigHelp is dirt_pile.js 345-358 and 390-410 for a dig with n helpers (each counted once):
// 1 bonus item past 0 helpers, 2 past 2, 3 past 5; a 5% chance past 1 helper, 15% past 5, that the
// dig's yield and the bonus items are Loam. The source divides the chance by 1 minus the skill's own
// Loam drop chance because that drop was already rolled and missed; on a home street the site rolls
// the dig (HomeYard.php judgeStock: its yield is the cultivation's Earth, no skill drop), so the
// chance stands as it is.
func homeDigHelp(n int, roll float64) (items int, loam bool) {
	chance := 0.0
	if n > 0 {
		items++
	}
	if n > 1 {
		chance += 0.05
	}
	if n > 2 {
		items++
	}
	if n > 5 {
		items++
		chance += 0.1
	}
	return items, chance > 0 && roll < math.Min(1, chance)
}
