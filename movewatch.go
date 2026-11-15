package main

// The move watch (register G740): a log, nothing more. The game moves its own avatar and the
// server takes each move frame's x/y as the player's place; this notes in the process log (main.go's
// log, journald on the live machine: `journalctl -u groddle-realtime | grep "realtime: move"`;
// realtime/realtime.log under launchd) when a step is longer than any walk, jump or fall could make
// it, and when a join lands far from every way onto the street. It never refuses a move, never pulls a
// player back and never tells them: a watch before any rule, to see what honest play looks like.
//
// A step: the distance from the last move (or the join) is over moveTopSpeed times the seconds since
// it plus moveMargin. Ours: vx_max 335 px/s (avatar-step.js) times the biggest buff (Spinach's 1.7) is
// about 570 px/s across, and a fall is at most 670 px/s (TERMINAL_VY), so 1,500 px/s and a 600 px
// margin (lag, a burst of queued frames) leave a wide margin. Each oversized step is counted; one
// line per player per street per moveLogEvery says how many, the biggest and the last. A join
// starts the count afresh (street changes, quest rooms, Hell, homes), and so does any message the
// server sends that places the player (moveFreeKinds: an administrator's teleport, a summons, a
// follow, a race or a game's room): steps in the next moveFreeFor are not checked.
//
// A join: far (over joinNear) from every entry point of the street (data/entries.json, written by
// the game's tools/build-realtime-entries.mjs: signpost and door arrive points and the street's own
// signposts and doors) and from where the server last saw this character on that street (a reload
// or a dropped socket comes back where it was). Streets with no entry points (instances, homes,
// quest rooms) are never noted. A character the server has not seen since it started is compared
// with the entry points only, so the minutes after a restart note some honest reloads.
//
// Log lines:
//
//	realtime: move far player=7 name="Ada" street=GA5… steps=3 most=2400 allowed=650 last=1200,640
//	realtime: join far player=7 name="Ada" street=GA5… at=3100,620 nearest=2900

import (
	_ "embed"
	"encoding/json"
	"log"
	"math"
	"strconv"
	"time"
)

//go:embed data/entries.json
var entriesJSON []byte

const (
	moveTopSpeed = 1500.0           // px/s; ours (see above)
	moveMargin   = 600.0            // px; ours
	joinNear     = 800.0            // px from an entry point or the last place seen; ours (config_prod.js verb_radius is 800)
	moveFreeFor  = 15 * time.Second // after a placing message; ours
	moveLogEvery = time.Minute      // one line per player per street at most this often
	seenKeep     = 6 * time.Hour    // a character's last place is kept this long after it was seen
)

// moveFreeKinds are the server's messages that put a player somewhere (the next steps are not checked).
var moveFreeKinds = map[string]bool{
	"teleport": true, "summon_go": true, "follow_go": true, "follow_teleported": true,
	"crown_match": true, "race_match": true, "race_go": true, "race_end": true,
	"mayhem_room": true, "mayhem_end": true, "party_space": true, "party_space_kicked": true,
	"subway_train": true, "home_entered": true,
}

// moveWatch is a client's last checked place.
type moveWatch struct {
	x, y float64
	at   time.Time
	free time.Time // steps before this are not checked
}

// moveNote is one player's notes on one street within a minute.
type moveNote struct {
	name  string
	at    time.Time // the last line written
	steps int       // oversized steps since then
	most  float64
	allow float64
	lx    float64
	ly    float64
}

// seenAt is where a character was last seen on a street.
type seenAt struct {
	street string
	x, y   float64
	at     time.Time
}

type moveKey struct {
	id     int64
	street string
}

// moveBook is the hub's side of the watch (all under h.mu).
type moveBook struct {
	entries map[string][][2]float64
	seen    map[int64]seenAt
	notes   map[moveKey]*moveNote
	swept   time.Time
}

func parseEntries(b []byte) map[string][][2]float64 {
	var d struct {
		Streets map[string][][2]float64 `json:"streets"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		panic("realtime: data/entries.json: " + err.Error())
	}
	return d.Streets
}

func (h *Hub) moves() *moveBook {
	if h.moveBook == nil {
		h.moveBook = &moveBook{entries: parseEntries(entriesJSON), seen: map[int64]seenAt{}, notes: map[moveKey]*moveNote{}}
	}
	return h.moveBook
}

// movePlacedLocked notes a message that places cl: its next steps are not checked.
func (h *Hub) movePlacedLocked(cl *Client, msg any) {
	m, ok := msg.(map[string]any)
	if !ok {
		return
	}
	if t, _ := m["t"].(string); moveFreeKinds[t] {
		cl.moveW.free = h.now().Add(moveFreeFor)
	}
}

// watchJoinLocked checks a join's place (cl.player is set) and starts the step count from it.
func (h *Hub) watchJoinLocked(cl *Client) {
	b := h.moves()
	now := h.now()
	x, y := cl.player.X, cl.player.Y
	if pts := b.entries[cl.street]; len(pts) > 0 && now.After(cl.moveW.free) {
		nearest := math.Inf(1)
		for _, p := range pts {
			nearest = math.Min(nearest, math.Hypot(x-p[0], y-p[1]))
		}
		if s, ok := b.seen[cl.id]; ok && s.street == cl.street {
			nearest = math.Min(nearest, math.Hypot(x-s.x, y-s.y))
		}
		if nearest > joinNear && h.moveNoteDueLocked(cl, now) {
			log.Printf("realtime: join far player=%d name=%q street=%s at=%s nearest=%.0f", cl.id, cl.name, cl.street, xy(x, y), nearest)
		}
	}
	cl.moveW.x, cl.moveW.y, cl.moveW.at = x, y, now
	b.seen[cl.id] = seenAt{street: cl.street, x: x, y: y, at: now}
}

// watchMoveLocked checks one move to (x, y) against the last place; call before cl.player changes.
func (h *Hub) watchMoveLocked(cl *Client, x, y float64) {
	b := h.moves()
	now := h.now()
	if !cl.moveW.at.IsZero() && now.After(cl.moveW.free) {
		step := math.Hypot(x-cl.moveW.x, y-cl.moveW.y)
		allowed := moveTopSpeed*now.Sub(cl.moveW.at).Seconds() + moveMargin
		if step > allowed {
			k := moveKey{cl.id, cl.street}
			n := b.notes[k]
			if n == nil {
				n = &moveNote{}
				b.notes[k] = n
			}
			n.steps++
			if step > n.most {
				n.most, n.allow = step, allowed
			}
			n.lx, n.ly, n.name = x, y, cl.name
			if now.Sub(n.at) >= moveLogEvery {
				n.writeLocked(k, now)
			}
		}
	}
	cl.moveW.x, cl.moveW.y, cl.moveW.at = x, y, now
	b.seen[cl.id] = seenAt{street: cl.street, x: x, y: y, at: now}
	h.sweepMovesLocked(now)
}

// writeLocked writes the line for the steps counted and starts the next minute's count.
func (n *moveNote) writeLocked(k moveKey, now time.Time) {
	log.Printf("realtime: move far player=%d name=%q street=%s steps=%d most=%.0f allowed=%.0f last=%s", k.id, n.name, k.street, n.steps, n.most, n.allow, xy(n.lx, n.ly))
	*n = moveNote{at: now}
}

// moveNoteDueLocked: true when cl's street may have a line now (shared with the step lines).
func (h *Hub) moveNoteDueLocked(cl *Client, now time.Time) bool {
	b := h.moves()
	k := moveKey{cl.id, cl.street}
	n := b.notes[k]
	if n == nil {
		b.notes[k] = &moveNote{at: now}
		return true
	}
	if now.Sub(n.at) < moveLogEvery {
		return false
	}
	n.at = now
	return true
}

// sweepMovesLocked writes counts a minute old that no later step wrote, and forgets old notes and
// places, at most once a minute.
func (h *Hub) sweepMovesLocked(now time.Time) {
	b := h.moves()
	if now.Sub(b.swept) < time.Minute {
		return
	}
	b.swept = now
	for k, n := range b.notes {
		if n.steps > 0 && now.Sub(n.at) >= moveLogEvery {
			n.writeLocked(k, now)
		}
		if now.Sub(n.at) > 10*moveLogEvery && n.steps == 0 {
			delete(b.notes, k)
		}
	}
	for id, s := range b.seen {
		if now.Sub(s.at) > seenKeep {
			delete(b.seen, id)
		}
	}
}

func xy(x, y float64) string {
	return strconv.FormatFloat(x, 'f', 0, 64) + "," + strconv.FormatFloat(y, 'f', 0, 64)
}
