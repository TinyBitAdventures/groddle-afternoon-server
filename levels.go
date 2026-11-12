package main

// Level-ups told to the street (rules-b, 2026-11-12). GameServerJS players/inc_stats.js 374: on a
// level-up the player reads "Woo! you've reached level N!!!" and everyone else in the location
// reads "<name> just reached level N!" (sendLocationActivity, players/player.js 472-500), except the
// players the leveller ignores (buddies_get_ignoring_tsids, inc_buddies.js 619-630).
//
// Client to server:
//
//	{"t":"level_up","level":7}           you just reached level 7 (the game's own level-up, js/progression.js)
//
// Server to client:
//
//	{"t":"level_up","id":7,"name":"Ada","level":7}   to everyone else on Ada's street who neither blocks nor is blocked by her
//
// Levels are the client's (the site's ledger credits their currants, LedgerMath LEVELS_DAILY), so the
// server only keeps the street's word honest in size: a level from 2 to 60, higher than the last one
// this character announced since the server started, at most one a second.
//
// Not sent: pc_level_up to the players who hold you as a buddy (inc_stats.js 427-434). The 2012
// client had no handler for it (glitch-client NCMsgHandler.as, type_to_func[PC_LEVEL_UP] = null), so a
// buddy elsewhere saw nothing.

import (
	"encoding/json"
	"time"
)

const (
	levelMax   = 60 // config_prod.js level_limit
	levelEvery = time.Second
)

func init() { modules = append(modules, newLevels) }

type levels struct {
	h    *Hub
	last map[int64]int       // the highest level each character has announced
	at   map[int64]time.Time // when they last announced one
}

func newLevels(h *Hub) Module {
	return &levels{h: h, last: map[int64]int{}, at: map[int64]time.Time{}}
}

func (m *levels) Register(r *Registry) {
	r.Handle("level_up", m.levelUp)
}

func (m *levels) levelUp(cl *Client, frame json.RawMessage) {
	var msg struct {
		Level int `json:"level"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.Level < 2 || msg.Level > levelMax {
		return
	}
	id := cl.ID()
	now := m.h.Now()
	if msg.Level <= m.last[id] || now.Sub(m.at[id]) < levelEvery {
		return
	}
	m.last[id] = msg.Level
	m.at[id] = now
	street := cl.Street()
	if street == "" {
		return
	}
	out := map[string]any{"t": "level_up", "id": id, "name": cl.Name(), "level": msg.Level}
	for _, o := range m.h.StreetLocked(street) {
		if o.ID() == id || blockedBetween(m.h, id, o.ID()) {
			continue
		}
		m.h.SendLocked(o, out)
	}
}
