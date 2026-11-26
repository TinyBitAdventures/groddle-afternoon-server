package main

// Street sounds (#715): a note one player plays on a street's own event scripts is heard by everyone
// else on the street.
//
// In Glitch a play_sound with all_players went to every active player on the location
// (GameServerJS locations/inc_events.js 399-404, events_runSoundAction 531-538), whoever's
// Collision-triggered Event Box fired the event (items/cteb.js onEvent, broadcastLocationEvents).
// Piano Party's 66 keys and stars all play that way. Here each game runs the street's scripts itself
// (the remake's location-events-feature), so a game that ran one of these events tells the server
// (street_sound) and the server tells everyone else on the street; their games run it as an echo:
// the sound and the light that flashes and goes out, never the flags or the puzzle (Austin
// 2026-11-25). The sender already heard its own, so it is left out.
//
// The event must be one the street lists in data/street-sounds.json (go:embed; written by the game
// repo's tools/build-realtime-street-sounds.mjs from its street files: the events that play a sound
// for every player, arrivals, toy streets and streets another system runs left out), on the street
// the sender stands on, as streettoys.go checks a toy against its street. Any other event is
// dropped, so this relays no sound of a player's choosing. A player sends at most one every
// streetSoundEvery (ours, the toy clicks' 200 ms); more often is dropped.
//
// Client to server:
//
//	{"t":"street_sound","event":"key1"}
//
// Server to the others on the street:
//
//	{"t":"street_sound","street":"GA96OS42D6F3M9P","event":"key1","by":7}

import (
	_ "embed"
	"encoding/json"
	"log"
	"time"
)

//go:embed data/street-sounds.json
var streetSoundsJSON []byte

// streetSoundEvery is the toy clicks' limit (streettoys.go toyClickEvery).
const streetSoundEvery = toyClickEvery

// streetSoundTable: each street's shared events, keyed without its first letter (G or L).
var streetSoundTable = parseStreetSounds(streetSoundsJSON)

func parseStreetSounds(b []byte) map[string]map[string]bool {
	var f struct {
		Streets map[string][]string `json:"streets"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		log.Printf("street sounds: %v (none are shared)", err)
		return map[string]map[string]bool{}
	}
	out := make(map[string]map[string]bool, len(f.Streets))
	for tsid, names := range f.Streets {
		if len(tsid) < 2 {
			continue
		}
		set := make(map[string]bool, len(names))
		for _, n := range names {
			set[n] = true
		}
		out[tsid[1:]] = set
	}
	return out
}

type streetSounds struct {
	h      *Hub
	sentAt map[int64]time.Time
}

func init() { modules = append(modules, newStreetSounds) }

func newStreetSounds(h *Hub) Module {
	return &streetSounds{h: h, sentAt: map[int64]time.Time{}}
}

func (m *streetSounds) Register(r *Registry) {
	r.Handle("street_sound", m.sound)
	r.OnDisconnect(func(cl *Client) { delete(m.sentAt, cl.ID()) })
}

func (m *streetSounds) sound(cl *Client, frame json.RawMessage) {
	var f struct {
		Event string `json:"event"`
	}
	if json.Unmarshal(frame, &f) != nil {
		return
	}
	street := cl.Street()
	if len(street) < 2 || !streetSoundTable[street[1:]][f.Event] {
		return
	}
	now := m.h.Now()
	if at, ok := m.sentAt[cl.ID()]; ok && now.Sub(at) < streetSoundEvery {
		return
	}
	m.sentAt[cl.ID()] = now
	m.h.BroadcastStreetLocked(street, map[string]any{"t": "street_sound", "street": street, "event": f.Event, "by": cl.ID()}, cl.ID())
}
