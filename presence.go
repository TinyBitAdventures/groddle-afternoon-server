package main

// Who's around (Austin 2026-11-04: "show panel with active players"): the
// players online now, for the website's /chat/ panel (web.go sockets) and a
// count in the game's Social drawer. The remake's own: Glitch had no
// world-wide list of who was online, only the buddy list (players/inc_buddies.js,
// buddies.go here) and a street's own players.
//
// Client to server (the game):
//
//	{"t":"presence_watch"}            the drawer's count, now and whenever it changes
//
// Server to client:
//
//	{"t":"presence_count","n":12}                                       the game: players you would see in the panel, you not counted
//	{"t":"presence","players":[P…],"you":{"off":false}}                 a web socket, after its web_watch: everyone it may see
//	{"t":"presence_up","player":P}                                      someone came, or changed (status, name, street)
//	{"t":"presence_gone","id":7}                                        someone went (or is hidden from you now)
//
//	P: {"id":7,"name":"Ada","status":"game"|"web"|"idle","buddy":true,"street":"GA5…"}
//
// status is "game" while the character has its game socket, "web" while only
// the website's, and "idle" when nothing was done on any of them for
// presenceIdle (the game's inputs, inputKinds; the page's "active"). No
// locations, but a buddy (either way, buddies.go holds) sees the street they
// are on as the game's buddy list does (buddies.go shownStreet: a private
// place has none). You are in your own list ("me": true), so the panel can say
// how you show.
//
// Never listed: a player with "Appear offline" on (to anyone else, buddies
// included: buddies.go tellable, im.go online, the channels' rosters, admin.go
// online marks them for the site's phone list), and between two players where
// either blocks the other, whichever list says so (the game's buddies watch or
// a web socket's). Both lists change who sees whom: a new list, a block, a
// switch of the setting, a socket that comes or goes.
//
// "Appear offline" is the account's (the site keeps it, user meta
// groddle_appear_offline): every ticket carries it (Claims.Off), and the site
// tells the server when it changes (presence_hide, signed), which takes effect
// on every socket of that player at once. Those players chat and post as
// before, by name.
//
// Ours: a socket that drops has presenceGrace to come back before it is gone
// from the lists (a reload is not leaving); changes go out at most each
// presenceSettle, all together.

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

const (
	presenceIdle   = 5 * time.Minute
	presenceGrace  = 10 * time.Second
	presenceSettle = 300 * time.Millisecond
	presenceTick   = 30 * time.Second // idle is looked at this often
	presenceMax    = 500              // entries one list carries (more: the first presenceMax by buddies, then name)
)

func init() {
	modules = append(modules, newPresence)
	serviceHandlers["presence_hide"] = presenceHideService
}

type presEntry struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Buddy  bool   `json:"buddy,omitempty"`
	Street string `json:"street,omitempty"`
	Me     bool   `json:"me,omitempty"`
	Off    bool   `json:"off,omitempty"` // your own entry only
}

// presGone is a character whose last socket went less than presenceGrace ago.
type presGone struct {
	name   string
	game   bool
	street string
	at     time.Time
	input  time.Time
	off    bool
}

type presence struct {
	h        *Hub
	counts   map[*Client]int                 // game sockets that asked, and the count last sent
	lists    map[*Client]map[int64]presEntry // web sockets, and the list last sent
	gone     map[int64]presGone
	pending  bool
	ticking  bool
	lastTick time.Time
}

func newPresence(h *Hub) Module {
	return &presence{h: h, counts: map[*Client]int{}, lists: map[*Client]map[int64]presEntry{}, gone: map[int64]presGone{}}
}

func (m *presence) Register(r *Registry) {
	r.Handle("presence_watch", m.watch)
	r.OnConnect(func(cl *Client) { delete(m.gone, cl.ID()); m.changed() })
	r.OnJoin(func(cl *Client) { m.changed() })
	r.OnLeave(func(cl *Client, _ string) { m.changed() })
	r.OnDisconnect(func(cl *Client) {
		delete(m.counts, cl)
		m.left(cl, true)
	})
	r.OnWeb(func(cl *Client, event string) {
		switch event {
		case "connect":
			delete(m.gone, cl.ID())
		case "watch":
			m.sendFull(cl) // the whole list; changes after it
		case "disconnect":
			delete(m.lists, cl)
			m.left(cl, false)
			return
		}
		m.changed()
	})
	r.Provide("presence.changed", m.changed)
	r.Provide("presence.touched", m.touched)
	r.Provide("presence.self", m)
}

// left starts a character's grace when their last socket went.
func (m *presence) left(cl *Client, game bool) {
	id := cl.ID()
	h := m.h
	if h.clients[id] != nil || len(h.webs[id]) > 0 {
		m.changed() // another socket of theirs is still here (their status may change)
		return
	}
	g := presGone{name: cl.Name(), game: game, at: h.Now(), input: h.inputAt[id], off: h.off[id]}
	if game {
		g.street = shownStreet(cl)
	}
	m.gone[id] = g
	h.After(presenceGrace+time.Millisecond, func() { m.changed() })
	m.changed()
}

// touched is a character doing something (hub.go): an idle one is active again at once.
func (m *presence) touched(id int64, was time.Time) {
	if !was.IsZero() && m.h.Now().Sub(was) >= presenceIdle {
		m.changed()
	}
}

// changed schedules one flush.
func (m *presence) changed() {
	if m.pending || (len(m.counts) == 0 && len(m.lists) == 0) {
		return
	}
	m.pending = true
	m.h.After(presenceSettle, func() {
		m.pending = false
		m.flush()
	})
}

// tick looks at idle now and then while anyone watches.
func (m *presence) tick() {
	if m.ticking || (len(m.counts) == 0 && len(m.lists) == 0) {
		return
	}
	m.ticking = true
	m.h.After(presenceTick, func() {
		m.ticking = false
		m.flush()
		m.tick()
	})
}

func (m *presence) watch(cl *Client, _ json.RawMessage) {
	m.counts[cl] = -1
	m.sendCount(cl)
	m.tick()
}

// online is everyone with a socket now, or in their grace: id -> who they are as seen by nobody in particular.
type presWho struct {
	name   string
	game   bool
	street string
	input  time.Time
	off    bool
}

func (m *presence) online() map[int64]presWho {
	h := m.h
	now := h.Now()
	out := map[int64]presWho{}
	for id, cl := range h.clients {
		if cl.closed {
			continue
		}
		out[id] = presWho{name: cl.Name(), game: true, street: shownStreet(cl), input: h.inputAt[id], off: h.off[id]}
	}
	for id, tabs := range h.webs {
		if _, ok := out[id]; ok || len(tabs) == 0 {
			continue
		}
		var name string
		for cl := range tabs {
			name = cl.Name()
		}
		out[id] = presWho{name: name, input: h.inputAt[id], off: h.off[id]}
	}
	for id, g := range m.gone {
		if _, ok := out[id]; ok {
			delete(m.gone, id)
			continue
		}
		if now.Sub(g.at) > presenceGrace {
			delete(m.gone, id)
			continue
		}
		out[id] = presWho{name: g.name, game: g.game, street: g.street, input: g.input, off: g.off} // as they were, until the grace ends
	}
	return out
}

// entry is what viewer sees of id, or false when viewer may not see them.
func (m *presence) entry(viewer, id int64, w presWho) (presEntry, bool) {
	h := m.h
	if id == viewer {
		return presEntry{ID: id, Name: w.name, Status: m.status(w), Me: true, Off: w.off}, true
	}
	if w.off || h.ignoringLocked(viewer, id) || h.ignoringLocked(id, viewer) {
		return presEntry{}, false
	}
	e := presEntry{ID: id, Name: w.name, Status: m.status(w)}
	holds, _ := h.ProvidedLocked("buddies.holds").(func(int64, int64) bool)
	if (holds != nil && holds(viewer, id)) || h.webHolds(viewer, id) {
		e.Buddy = true
		e.Street = w.street
	}
	return e, true
}

func (m *presence) status(w presWho) string {
	if !w.input.IsZero() && m.h.Now().Sub(w.input) >= presenceIdle {
		return "idle"
	}
	if w.game {
		return "game"
	}
	return "web"
}

// listFor is everything viewer sees now, by id.
func (m *presence) listFor(viewer int64, on map[int64]presWho) map[int64]presEntry {
	out := make(map[int64]presEntry, len(on))
	for id, w := range on {
		if e, ok := m.entry(viewer, id, w); ok {
			out[id] = e
		}
	}
	if len(out) > presenceMax {
		all := sortedEntries(out)
		out = make(map[int64]presEntry, presenceMax)
		for _, e := range all[:presenceMax] {
			out[e.ID] = e
		}
	}
	return out
}

// sortedEntries: you, then buddies, then by name.
func sortedEntries(list map[int64]presEntry) []presEntry {
	out := make([]presEntry, 0, len(list))
	for _, e := range list {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Me != b.Me {
			return a.Me
		}
		if a.Buddy != b.Buddy {
			return a.Buddy
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return out
}

func (m *presence) sendFull(cl *Client) {
	list := m.listFor(cl.ID(), m.online())
	m.lists[cl] = list
	m.h.SendLocked(cl, map[string]any{"t": "presence", "players": sortedEntries(list), "you": map[string]any{"off": m.h.OffLocked(cl.ID())}})
	m.tick()
}

func (m *presence) sendCount(cl *Client) {
	list := m.listFor(cl.ID(), m.online())
	n := len(list)
	if _, ok := list[cl.ID()]; ok {
		n--
	}
	if m.counts[cl] == n {
		return
	}
	m.counts[cl] = n
	m.h.SendLocked(cl, map[string]any{"t": "presence_count", "n": n})
}

// flush sends each watcher what changed for them.
func (m *presence) flush() {
	if len(m.counts) == 0 && len(m.lists) == 0 {
		return
	}
	on := m.online()
	for cl := range m.counts {
		if cl.closed || m.h.ClientLocked(cl.ID()) != cl {
			delete(m.counts, cl)
			continue
		}
		list := m.listFor(cl.ID(), on)
		n := len(list)
		if _, ok := list[cl.ID()]; ok {
			n--
		}
		if m.counts[cl] != n {
			m.counts[cl] = n
			m.h.SendLocked(cl, map[string]any{"t": "presence_count", "n": n})
		}
	}
	viewers := make([]*Client, 0, len(m.lists))
	for cl := range m.lists {
		viewers = append(viewers, cl)
	}
	sort.Slice(viewers, func(i, j int) bool {
		return viewers[i].ID() < viewers[j].ID() || (viewers[i].ID() == viewers[j].ID() && viewers[i].chatAt.Before(viewers[j].chatAt))
	})
	for _, cl := range viewers {
		if cl.closed || cl.ww == nil {
			continue
		}
		was := m.lists[cl]
		now := m.listFor(cl.ID(), on)
		for _, e := range sortedEntries(now) {
			if old, ok := was[e.ID]; !ok || old != e {
				m.h.SendLocked(cl, map[string]any{"t": "presence_up", "player": e})
			}
		}
		gone := []int64{}
		for id := range was {
			if _, ok := now[id]; !ok {
				gone = append(gone, id)
			}
		}
		sort.Slice(gone, func(i, j int) bool { return gone[i] < gone[j] })
		for _, id := range gone {
			m.h.SendLocked(cl, map[string]any{"t": "presence_gone", "id": id})
		}
		m.lists[cl] = now
	}
}

// —— The site's call: "Appear offline" changed ——

type presenceHideCall struct {
	Player int64 `json:"player"`
	Off    *bool `json:"off"`
}

func presenceHideService(h *Hub, body []byte) (int, any) {
	var c presenceHideCall
	if json.Unmarshal(body, &c) != nil || c.Player <= 0 || c.Off == nil {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c.Player] == nil && len(h.webs[c.Player]) == 0 {
		return http.StatusOK, map[string]string{"status": "offline"} // the next ticket carries it
	}
	if h.off[c.Player] == *c.Off {
		return http.StatusOK, map[string]string{"status": "ok"}
	}
	h.off[c.Player] = *c.Off
	if fn, ok := h.ProvidedLocked("buddies.visibility").(func(int64)); ok {
		fn(c.Player)
	}
	// The setting's own sockets hear it (the game's Account panel, the page's switch).
	msg := map[string]any{"t": "presence_you", "off": *c.Off}
	if cl := h.clients[c.Player]; cl != nil {
		h.queueLocked(cl, msg)
	}
	for _, cl := range h.WebLocked(c.Player) {
		h.queueLocked(cl, msg)
	}
	if fn, ok := h.ProvidedLocked("presence.changed").(func()); ok {
		fn()
	}
	return http.StatusOK, map[string]string{"status": "ok"}
}
