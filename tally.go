package main

// World tallies for the console's Analytics tab (Austin 2026-11-11: "quirky
// things like trees harvested, mines mined, piggies nibbled, butterflies
// milked"). This server is the one that decides every act on the world's
// shared things (objects.go, gardens.go, quoins.go), so it counts them here:
// one number a UTC day for each kind of act, for everyone together. No player
// is named in a tally, and nothing about a player is kept.
//
// A key is "<thing>:<act>", lowercase letters, digits and underscores:
//
//	<kind>:<verb>    a shared object's act that landed (finishAct's obj_ok):
//	                 rock:mine, tree:harvest|pet|water|poison|antidote, wood:harvest,
//	                 pig:pet|nibble|feed|capture|name, chicken:squeeze|incubate,
//	                 butterfly:massage|milk, batterfly:feed|pickup, kitty:pet, crab:hug|…, dragon:pet
//	<class>:<verb>   a resource node's (its class says more than "node"): dirt_pile:dig,
//	                 peat_1:harvest, mortar_barnacle:scrape, ice_knob:scrape, jellisac:scoop,
//	                 npc_salmon:pocket, paper_tree:harvest|pet|water
//	garden:<verb>    a public garden act (gardens.go ok): plant, water, water_all, harvest, clear, …
//	quoin:<type>     a quoin grabbed on a shared street (quoins.go): xp, mood, currants, …
//
// An act the source rolls and that missed ("success": false, a Piggy that would
// not be nibbled) is not counted. The site keeps the tallies it is sent and
// chooses which to show (groddle-afternoon-site src/Analytics.php).
//
// They reach the site on the play-time batches (playtime.go): a batch carries
// "counts": {"2026-11-11": {"pig:nibble": 3, …}} beside its players, sent,
// kept across a restart and taken once by its id exactly as play time is.
// With play time off (no -ledger-url, -play-time off) nothing is tallied.

import "regexp"

// tallyKeyRe is a key's shape; the site checks the same (Analytics::KEY).
var tallyKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}:[a-z][a-z0-9_]{0,23}$`)

// tallyDayKeys is the most keys one day keeps (a bound, far above the acts there are).
const tallyDayKeys = 256

// tallyLocked adds n to today's tally of key. Under the hub lock.
func (h *Hub) tallyLocked(key string, n int64) {
	if n <= 0 || !tallyKeyRe.MatchString(key) {
		return
	}
	if fn, ok := h.provided["playtime.count"].(func(string, int64)); ok {
		fn(key, n)
	}
}

// objTallyKey is a shared object's act as a tally key: a node by its class, the rest by kind.
func objTallyKey(spot *objSpot, verb string) string {
	if spot.kind == "node" {
		return spot.class + ":" + verb
	}
	return spot.kind + ":" + verb
}

// tallyLanded is whether an obj_ok's act landed: a rolled act that missed says "success": false.
func tallyLanded(extra map[string]any) bool {
	s, ok := extra["success"].(bool)
	return !ok || s
}
