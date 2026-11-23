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
//	{"t":"new_ur","now":…,"midnights":[…ms],"big":…ms}                to new_ur_sync
//	{"t":"new_ur","now":…,"ev":"midnight","at":…,"index":4,"big":true} at each of the six midnights
//
// The page keeps now minus its own clock and runs the countdown and the show on it.
//
// Client to server:
//
//	{"t":"new_ur_sync"}   asks for the eve and the clock (the page sends it as it comes online)
//
// Global hears newUrNotices before the big midnight (channels.go notice), each once. Nothing is
// written to disk: after a restart a line whose moment passed more than newUrNoticeLate ago is not
// posted late, and the midnights are worked out again from the clock.

import (
	"encoding/json"
	"fmt"
	"log"
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
)

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
	h     *Hub
	told  map[string]bool // "<year>:<lead>" lines posted
	shown map[int64]bool  // midnights (unix s) broadcast
	timer func() bool
}

func init() { modules = append(modules, newNewUr) }

func newNewUr(h *Hub) Module {
	return &newUr{h: h, told: map[string]bool{}, shown: map[int64]bool{}}
}

func (m *newUr) Register(r *Registry) {
	r.Handle("new_ur_sync", func(cl *Client, _ json.RawMessage) { m.sendState(cl) })
	// The timers start with the first player online and re-arm at each change. The clock itself is
	// sent when the page asks (new_ur_sync, as it comes online), so a connect sends nothing new.
	r.OnConnect(func(*Client) { m.wake() })
}

// state is the eve's midnights and the server's clock.
func (m *newUr) state() map[string]any {
	ats, big := newUrEve(m.h.Now())
	ms := make([]int64, len(ats))
	for i, t := range ats {
		ms[i] = t.UnixMilli()
	}
	return map[string]any{"t": "new_ur", "now": m.h.Now().UnixMilli(), "midnights": ms, "big": ats[big].UnixMilli()}
}

func (m *newUr) sendState(cl *Client) {
	if cl == nil || cl.web {
		return
	}
	m.h.SendLocked(cl, m.state())
}

// broadcast sends one message to every game socket.
func (m *newUr) broadcast(msg map[string]any) {
	b, _ := json.Marshal(msg)
	for _, cl := range m.h.clients {
		if !cl.web {
			m.h.pushLocked(cl, b)
		}
	}
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
			m.broadcast(map[string]any{"t": "new_ur", "now": now.UnixMilli(), "ev": "midnight", "at": at.UnixMilli(), "index": i + 1, "big": i == big})
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
