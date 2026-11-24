package main

// Happy New Ur (G1195, Austin's picks 2026-11-23; the game's js/new-ur.js and
// features/new-ur-feature.js). The countdown at each Groddle midnight on December 31 (Ur's clock,
// Pacific): six of them, a game day being four real hours from Glitch's epoch, the one nearest
// 1 PM Pacific the big one with the party at Unnu Slight. Every time is derived from the game clock
// (newUrEpoch, newUrDay), none written down.
//
// What Glitch did: one moment for everyone, midnight Pacific on 2011-12-31, a couple of seconds of
// Sparkle Powder over every player online. Matched: the server owns the moment so every player
// online sees the same second. Ours (the game's inventions register, new-ur-countdown): the moment
// is a Groddle midnight, six of them, and Global's lines before the big one.
//
// Server to client (every game socket; nothing is asked of the player):
//
//	{"t":"new_ur","now":…,"midnights":[…ms],"big":…ms,"seen":[4]}                 to new_ur_sync
//	{"t":"new_ur","now":…,"ev":"midnight","at":…,"index":4,"big":true,"seen":true} at each of the six midnights
//	{"t":"new_ur_pop","ok":true,"near":2}                                          to the popper (ok false, why "slow")
//	{"t":"new_ur_popped","id":7,"name":"Ada","x":…,"y":…,"c":"red","near":true}    to everyone else on the street
//	{"t":"new_ur_toast","ok":true,"with":["Bo"]}                                    to the toaster (ok false, why "alone"|"slow")
//	{"t":"new_ur_toasted","from":{"id":7,"name":"Ada"}}                             to each player toasted
//
// The page keeps now minus its own clock and runs the countdown and the show on it.
//
// The keepsake and the badge (G1195 step 4, the game's js/new-ur-party.js): "seen" is this server's
// word that the character was connected at that midnight. The midnight goes to every game socket
// connected at that moment, each marked seen, and the eve's seen midnights come back with
// new_ur_sync, so a page that reloads in the show still gets its keepsake. Kept in memory (an eve's
// worth); a restart forgets who saw a midnight, but each page has granted its keepsake by then.
//
// Client to server:
//
//	{"t":"new_ur_sync"}               asks for the eve and the clock (the page sends it as it comes online)
//	{"t":"new_ur_pop","c":"red"}      a Confetti Popper popped (c: mix, red, green, blue, shiny): the
//	                                  burst for everyone on the street, surprise for those within newUrPopReach
//	                                  (the popper is a loss from the pack: nothing for the ledger)
//	{"t":"new_ur_toast"}              a Bubbly raised: the players within newUrToastReach toast with you
//
// Global hears newUrNotices before the big midnight (channels.go notice), each once. Nothing is
// written to disk: after a restart a line whose moment passed more than newUrNoticeLate ago is not
// posted late, and the midnights are worked out again from the clock.

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"
)

const (
	// Glitch's epoch and day (the game's js/game-day.js GAME_EPOCH_MS, GAME_DAY_MS; TimeController.as).
	newUrEpoch = 1238562000
	newUrDay   = 14400 * time.Second
	// The big midnight is the one nearest this hour on December 31, Ur's clock (Austin's pick).
	newUrBigHour = 13
	// A line still posts this long after its moment (a restart), not later.
	newUrNoticeLate = 2 * time.Minute
	// The Confetti Popper (js/new-ur-party.js NEW_UR_PARTY): who jumps, how often one pops.
	newUrPopReach = 600.0
	newUrPopEvery = time.Second
	// Bubbly's Toast: who it reaches, how often.
	newUrToastReach = 400.0
	newUrToastEvery = 3 * time.Second
)

// newUrPopColours are the poppers' colours: the Toy Vendor's mix and the four Elements.
var newUrPopColours = map[string]bool{"mix": true, "red": true, "green": true, "blue": true, "shiny": true}

// newUrNotices are Global's lines before the big midnight (js/new-ur.js NEW_UR_NOTICES, word for word).
var newUrNotices = []struct {
	lead time.Duration
	text string
}{
	{24 * time.Hour, "Happy New Ur in 24 hours! The year turns at a Groddle midnight. Party at Unnu Slight."},
	{time.Hour, "Happy New Ur in one hour! Party at Unnu Slight, or watch the sky wherever you are."},
	{10 * time.Minute, "Happy New Ur in 10 minutes! The year turns at Groddle midnight. Party at Unnu Slight!"},
}

// newUrMidnights are the Groddle midnights on December 31 of year y on Ur's clock, soonest first,
// and the index of the big one.
func newUrMidnights(y int) ([]time.Time, int) {
	start := time.Date(y, time.December, 31, 0, 0, 0, 0, urZone)
	end := time.Date(y+1, time.January, 1, 0, 0, 0, 0, urZone)
	epoch := time.Unix(newUrEpoch, 0)
	n := (start.Sub(epoch) + newUrDay - 1) / newUrDay
	var out []time.Time
	for t := epoch.Add(n * newUrDay); t.Before(end); t = t.Add(newUrDay) {
		out = append(out, t)
	}
	target := start.Add(newUrBigHour * time.Hour)
	big := 0
	for i, t := range out {
		if absDur(t.Sub(target)) < absDur(out[big].Sub(target)) {
			big = i
		}
	}
	return out, big
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// newUrEve is the eve whose midnights are next or under way at now: the first whose last midnight's
// minute has not passed (last year's in early January, next year's late on December 31).
func newUrEve(now time.Time) ([]time.Time, int) {
	y := now.In(urZone).Year()
	for _, yy := range []int{y - 1, y} {
		if ats, big := newUrMidnights(yy); now.Before(ats[len(ats)-1].Add(time.Minute)) {
			return ats, big
		}
	}
	return newUrMidnights(y + 1)
}

type newUr struct {
	h       *Hub
	told    map[string]bool          // "<year>:<lead>" lines posted
	shown   map[int64]bool           // midnights (unix s) broadcast
	seen    map[int64]map[int64]bool // midnight (unix s) -> characters connected when it came
	popAt   map[int64]time.Time      // each character's last pop
	toastAt map[int64]time.Time      // each character's last toast
	timer   func() bool
}

func init() { modules = append(modules, newNewUr) }

func newNewUr(h *Hub) Module {
	return &newUr{h: h, told: map[string]bool{}, shown: map[int64]bool{}, seen: map[int64]map[int64]bool{}, popAt: map[int64]time.Time{}, toastAt: map[int64]time.Time{}}
}

func (m *newUr) Register(r *Registry) {
	r.Handle("new_ur_sync", func(cl *Client, _ json.RawMessage) { m.sendState(cl) })
	r.Handle("new_ur_pop", m.pop)
	r.Handle("new_ur_toast", m.toast)
	// The timers start with the first player online and re-arm at each change. The clock itself is
	// sent when the page asks (new_ur_sync, as it comes online), so a connect sends nothing new.
	r.OnConnect(func(*Client) { m.wake() })
}

// state is the eve's midnights, the server's clock and the midnights (1-6) id was connected for.
func (m *newUr) state(id int64) map[string]any {
	ats, big := newUrEve(m.h.Now())
	ms := make([]int64, len(ats))
	seen := []int{}
	for i, t := range ats {
		ms[i] = t.UnixMilli()
		if m.seen[t.Unix()][id] {
			seen = append(seen, i+1)
		}
	}
	return map[string]any{"t": "new_ur", "now": m.h.Now().UnixMilli(), "midnights": ms, "big": ats[big].UnixMilli(), "seen": seen}
}

func (m *newUr) sendState(cl *Client) {
	if cl == nil || cl.web {
		return
	}
	m.h.SendLocked(cl, m.state(cl.ID()))
}

// midnight sends the midnight to every game socket connected now, each marked seen (the keepsake's
// check), and remembers who saw it for new_ur_sync. Older eves' records are dropped.
func (m *newUr) midnight(msg map[string]any, at time.Time) {
	for k := range m.seen {
		if at.Unix()-k > int64(2*24*time.Hour/time.Second) {
			delete(m.seen, k)
		}
	}
	who := m.seen[at.Unix()]
	if who == nil {
		who = map[int64]bool{}
		m.seen[at.Unix()] = who
	}
	msg["seen"] = true
	b, _ := json.Marshal(msg)
	for _, cl := range m.h.clients {
		if !cl.web {
			who[cl.ID()] = true
			m.h.pushLocked(cl, b)
		}
	}
}

// near is everyone else on cl's street within reach px of cl, by id.
func (m *newUr) near(cl *Client, reach float64) (all, close []*Client) {
	street := cl.Street()
	if street == "" {
		return nil, nil
	}
	for _, o := range m.h.StreetLocked(street) {
		if o.ID() == cl.ID() || o.web {
			continue
		}
		all = append(all, o)
		dx, dy := o.player.X-cl.player.X, o.player.Y-cl.player.Y
		if dx*dx+dy*dy <= reach*reach {
			close = append(close, o)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID() < all[j].ID() })
	sort.Slice(close, func(i, j int) bool { return close[i].ID() < close[j].ID() })
	return all, close
}

// pop is a Confetti Popper popped: the burst for the whole street, surprise for those near.
func (m *newUr) pop(cl *Client, frame json.RawMessage) {
	var msg struct {
		C string `json:"c"`
	}
	if cl == nil || cl.web || json.Unmarshal(frame, &msg) != nil || !newUrPopColours[msg.C] {
		return
	}
	now := m.h.Now()
	if at, ok := m.popAt[cl.ID()]; ok && now.Sub(at) < newUrPopEvery {
		m.h.SendLocked(cl, map[string]any{"t": "new_ur_pop", "ok": false, "why": "slow"})
		return
	}
	m.popAt[cl.ID()] = now
	all, close := m.near(cl, newUrPopReach)
	isNear := map[int64]bool{}
	for _, o := range close {
		isNear[o.ID()] = true
	}
	for _, o := range all {
		m.h.SendLocked(o, map[string]any{"t": "new_ur_popped", "id": cl.ID(), "name": cl.Name(), "x": cl.player.X, "y": cl.player.Y, "c": msg.C, "near": isNear[o.ID()]})
	}
	m.h.SendLocked(cl, map[string]any{"t": "new_ur_pop", "ok": true, "near": len(close)})
}

// toast is a Bubbly raised: everyone within reach toasts with the toaster, or nobody is near.
func (m *newUr) toast(cl *Client, _ json.RawMessage) {
	if cl == nil || cl.web {
		return
	}
	now := m.h.Now()
	if at, ok := m.toastAt[cl.ID()]; ok && now.Sub(at) < newUrToastEvery {
		m.h.SendLocked(cl, map[string]any{"t": "new_ur_toast", "ok": false, "why": "slow"})
		return
	}
	_, close := m.near(cl, newUrToastReach)
	if len(close) == 0 {
		m.h.SendLocked(cl, map[string]any{"t": "new_ur_toast", "ok": false, "why": "alone"})
		return
	}
	m.toastAt[cl.ID()] = now
	with := make([]string, 0, len(close))
	from := map[string]any{"id": cl.ID(), "name": cl.Name()}
	for _, o := range close {
		with = append(with, o.Name())
		m.h.SendLocked(o, map[string]any{"t": "new_ur_toasted", "from": from})
	}
	m.h.SendLocked(cl, map[string]any{"t": "new_ur_toast", "ok": true, "with": with})
}

// wake posts what is due (a notice, a midnight) and arms the timer for the next thing.
func (m *newUr) wake() {
	now := m.h.Now()
	ats, big := newUrEve(now)
	year := ats[0].In(urZone).Year()
	var next time.Time
	soonest := func(t time.Time) {
		if t.After(now) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	for _, n := range newUrNotices {
		at := ats[big].Add(-n.lead)
		key := newUrKey(year, n.lead)
		if !now.Before(at) && now.Sub(at) <= newUrNoticeLate && !m.told[key] {
			if post, _ := m.h.ProvidedLocked("channels.notice").(func(string, string)); post != nil {
				post("global", n.text)
				log.Printf("new ur: Global told %q", n.text)
			}
			m.told[key] = true
		}
		soonest(at)
	}
	for i, at := range ats {
		if !now.Before(at) && now.Sub(at) < time.Minute && !m.shown[at.Unix()] {
			m.shown[at.Unix()] = true
			m.midnight(map[string]any{"t": "new_ur", "now": now.UnixMilli(), "ev": "midnight", "at": at.UnixMilli(), "index": i + 1, "big": i == big}, at)
			log.Printf("new ur: midnight %d of %d (big %v)", i+1, year, i == big)
		}
		soonest(at)
	}
	if !next.After(now) {
		// The eve is over: wake for next year's (newUrEve moves on a minute after its last midnight).
		next = maxTime(ats[len(ats)-1].Add(time.Minute+time.Second), now.Add(time.Second))
	}
	if m.timer != nil {
		m.timer()
	}
	m.timer = m.h.After(min(next.Sub(now), seasonWakeMax), func() {
		m.timer = nil
		m.wake()
	})
}

// newUrKey names one of a year's notices.
func newUrKey(year int, lead time.Duration) string {
	return fmt.Sprintf("%d:%s", year, lead)
}
