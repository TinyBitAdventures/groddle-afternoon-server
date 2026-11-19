package main

// Graves (register G1022, report #505): the Graveside Marker a croak leaves is the street's, so
// another player walking past can mourn or celebrate it, as in Glitch.
//
// What the source does (tinyspeck/glitch-GameServerJS, CC0):
//
//   - players/player.js croak() 1305-1309: location.createItemStack('graveside_marker', 1, x, y-29)
//     on the street where the player croaked, owned by them (setOwner). An item on the location:
//     everyone there sees it.
//   - items/graveside_marker.js celebrate 37-86 and mourn 88-137: any player there may run either
//     verb once per grave; the first one wins ("Someone else has already recognized the passing of
//     this Glitch.", or "You have already recognized the passing of this Glitch." to the one who
//     did). The recognizer gets 5 favor with the grave's Giant unless it is the generic one
//     (pc.stats_add_favor_points(giant, 5), the activity line names the favor); the grave shows
//     the verb's state to everyone (setAndBroadcastState); the owner gets recognizeDeath(type, pc).
//   - inspect 139-180: "<owner> croaked at this spot, on <game time>. R.I.P." and who mourned or
//     celebrated it.
//   - onCreate 199-202, onFade 211-213, onDisappear: it fades after 45 minutes and is gone 15 minutes
//     later; resurrect() 1338 removes it at once.
//   - player.js recognizeDeath 1430-1477: an online owner is offered a Hell quest or handed a
//     Diabolical Drink Ticket at once; an offline one keeps the record (death_recognized) and it
//     runs at the next login (200-204).
//
// Here the server keeps each dead player's grave and decides who recognized it first; the games
// draw it and run the owner's recognizeDeath (the game's js/hell.js, through
// features/graves-mp-feature.js):
//
//   - The owner's game sends grave_set when it croaks on a street others share (not a private place,
//     never in Naraka), and again after each reconnect while still dead (a restart of this server
//     forgets graves). A new grave is taken only from a player standing on that street, in Naraka,
//     or not on a street yet (a reconnect): a living player elsewhere cannot place one. One grave a
//     player; the same croak again keeps the grave and its state. A state the owner's game reports
//     (its solo companion mourned first, js/hell-tickets.js) is kept; it can only close the grave.
//   - grave_act from another player on the grave's street runs mourn or celebrate. The first wins;
//     the owner must still be dead (offline, or online in Naraka or on no street). The recognizer
//     is told the favor the source gives (graveFavor, 0 for the generic Giant) and their game adds
//     it; favor is not the ledger's (credits.go: rook_you favor), nor is the Drink Ticket (a
//     no_trade class, outside the site's economy), so nothing goes to the site: the owner's game
//     grants the ticket only on this server's grave_mourned.
//   - grave_mourned goes to the owner at once, or at their next connect (kept graveOwedKeep).
//   - A grave goes when its owner sends grave_clear (resurrected), joins a street outside Naraka
//     (alive again, a clear that never came), or graveLife after it was made.
//
// Client to server:
//
//	{"t":"grave_set","croak":3,"street":"G…","x":120,"y":-80,"giant":"alph","age":1200,"state":"none|mourn|celebrate","by":"Amp"}
//	{"t":"grave_clear"}
//	{"t":"grave_act","owner":7,"verb":"mourn|celebrate"}
//
// Server to client:
//
//	{"t":"graves","street":"G…","graves":[Grave,…]}   after a join on a street with graves
//	{"t":"grave","street":"G…","grave":Grave}          to the street: a grave made or recognized
//	{"t":"grave_gone","street":"G…","owner":7}         to the street: a grave removed
//	{"t":"grave_done","owner":7,"verb":"mourn","ok":true,"name":"Ada","giant":"alph","favor":5}
//	{"t":"grave_done","owner":7,"verb":"mourn","ok":false,"text":"Someone else has already …"}
//	{"t":"grave_mourned","verb":"mourn","by":"Bo","croak":3}   to the owner
//
// Grave: {"owner":7,"name":"Ada","x":120,"y":-80,"giant":"alph","state":"none","by":"","age":1200}
// (age: milliseconds since the croak, so each game fades it on its own clock).

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

const (
	graveFade     = 45 * time.Minute // graveside_marker.js onCreate 201 (each game fades it from age)
	graveLife     = 60 * time.Minute // onFade then 15 minutes to onDisappear
	graveFavor    = 5                // graveside_marker.js 70, 121: stats_add_favor_points(giant, 5)
	graveOwedKeep = 7 * 24 * time.Hour
	graveOwedMax  = 4096
	graveNameMax  = 80

	graveTextOther = "Someone else has already recognized the passing of this Glitch." // graveside_marker.js 52, 64
	graveTextSelf  = "You have already recognized the passing of this Glitch."         // graveside_marker.js 50
	graveTextGone  = "Whoops, this gravestone ain't hooked up right."                  // graveside_marker.js 81
)

// graveGiants: js/hell.js HELL_SPRITES.graveside.giants (graveside_marker.js instancePropsChoices).
var graveGiants = map[string]bool{
	"alph": true, "cosma": true, "friendly": true, "generic": true, "grendaline": true, "humbaba": true,
	"lem": true, "mab": true, "pot": true, "spriggan": true, "tii": true, "zille": true,
}

type grave struct {
	owner  int64
	name   string
	croak  int64
	street string
	x, y   float64
	giant  string
	made   time.Time
	state  string // none | mourn | celebrate
	by     string
	byID   int64
}

type graveOwed struct {
	verb, by string
	croak    int64
	at       time.Time
}

type graves struct {
	h     *Hub
	byOwn map[int64]*grave
	owed  map[int64]graveOwed
}

func init() { modules = append(modules, newGraves) }

func newGraves(h *Hub) Module {
	return &graves{h: h, byOwn: map[int64]*grave{}, owed: map[int64]graveOwed{}}
}

func (m *graves) Register(r *Registry) {
	r.Handle("grave_set", m.set)
	r.Handle("grave_clear", func(cl *Client, _ json.RawMessage) { m.remove(cl.ID()) })
	r.Handle("grave_act", m.act)
	r.OnJoin(m.joined)
	r.OnConnect(m.connected)
}

// streetKey: a street's id with the hub's G prefix (the source's L ids name the same street).
func streetKey(s string) string {
	if strings.HasPrefix(s, "L") {
		return "G" + s[1:]
	}
	return s
}

// graveStreetOK: a street others share where a grave may stand (no private place, no Naraka).
func graveStreetOK(s string) bool {
	return s != "" && len(s) <= 64 && !strings.Contains(s, "_") && !inNaraka(s)
}

func (m *graves) view(g *grave) map[string]any {
	return map[string]any{
		"owner": g.owner, "name": g.name, "x": g.x, "y": g.y, "giant": g.giant,
		"state": g.state, "by": g.by, "age": m.h.Now().Sub(g.made).Milliseconds(),
	}
}

// toStreet sends msg to everyone on the grave's street, under either id prefix.
func (m *graves) toStreet(street string, msg any) {
	key := streetKey(street)
	m.h.BroadcastStreetLocked(key, msg, 0)
	if alt := "L" + key[1:]; alt != key {
		m.h.BroadcastStreetLocked(alt, msg, 0)
	}
}

func (m *graves) live(g *grave) bool {
	return g != nil && m.h.Now().Sub(g.made) < graveLife
}

func (m *graves) set(cl *Client, frame json.RawMessage) {
	var f struct {
		Croak  int64   `json:"croak"`
		Street string  `json:"street"`
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Giant  string  `json:"giant"`
		Age    int64   `json:"age"`
		State  string  `json:"state"`
		By     string  `json:"by"`
	}
	if json.Unmarshal(frame, &f) != nil || f.Croak <= 0 || !graveStreetOK(f.Street) ||
		math.IsNaN(f.X) || math.IsInf(f.X, 0) || math.IsNaN(f.Y) || math.IsInf(f.Y, 0) {
		return
	}
	id := cl.ID()
	recognized := f.State == "mourn" || f.State == "celebrate"
	if g := m.byOwn[id]; m.live(g) && g.croak == f.Croak {
		// The same croak again (a reconnect, or the owner's companion mourned first): only closes it.
		if recognized && g.state == "none" {
			g.state = f.State
			g.by = cleanGraveName(f.By)
			m.toStreet(g.street, map[string]any{"t": "grave", "street": g.street, "grave": m.view(g)})
		}
		return
	}
	here := cl.Street()
	if here != "" && streetKey(here) != streetKey(f.Street) && !inNaraka(here) {
		return // a living player elsewhere places no grave
	}
	age := time.Duration(max(0, f.Age)) * time.Millisecond
	if age >= graveLife {
		return
	}
	if old := m.byOwn[id]; old != nil {
		m.drop(old)
	}
	giant := strings.ToLower(f.Giant)
	if !graveGiants[giant] {
		giant = "generic"
	}
	g := &grave{owner: id, name: cl.Name(), croak: f.Croak, street: f.Street, x: f.X, y: f.Y, giant: giant, made: m.h.Now().Add(-age), state: "none"}
	if recognized {
		g.state = f.State
		g.by = cleanGraveName(f.By)
	}
	m.byOwn[id] = g
	m.h.After(graveLife-age, func() {
		if m.byOwn[id] == g {
			m.drop(g)
		}
	})
	m.toStreet(g.street, map[string]any{"t": "grave", "street": g.street, "grave": m.view(g)})
}

func cleanGraveName(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > graveNameMax {
		s = string(r[:graveNameMax])
	}
	return s
}

// drop removes a grave and tells its street.
func (m *graves) drop(g *grave) {
	if m.byOwn[g.owner] == g {
		delete(m.byOwn, g.owner)
	}
	m.toStreet(g.street, map[string]any{"t": "grave_gone", "street": g.street, "owner": g.owner})
}

func (m *graves) remove(owner int64) {
	if g := m.byOwn[owner]; g != nil {
		m.drop(g)
	}
}

// joined sends the street's graves (none: nothing); an owner who walks onto a street outside Naraka is alive again.
func (m *graves) joined(cl *Client) {
	street := cl.Street()
	if g := m.byOwn[cl.ID()]; g != nil && street != "" && !inNaraka(street) {
		m.drop(g)
	}
	key := streetKey(street)
	list := []map[string]any{}
	for _, g := range m.byOwn {
		if streetKey(g.street) == key && m.live(g) {
			list = append(list, m.view(g))
		}
	}
	if len(list) > 0 {
		m.h.SendLocked(cl, map[string]any{"t": "graves", "street": street, "graves": list})
	}
}

// connected hands the owner a recognition made while they were away (player.js 200-204).
func (m *graves) connected(cl *Client) {
	o, ok := m.owed[cl.ID()]
	if !ok {
		return
	}
	delete(m.owed, cl.ID())
	if m.h.Now().Sub(o.at) > graveOwedKeep {
		return
	}
	m.h.SendLocked(cl, map[string]any{"t": "grave_mourned", "verb": o.verb, "by": o.by, "croak": o.croak})
}

// stillDead: the owner is offline, or online in Naraka or on no street (a private place).
func (m *graves) stillDead(owner int64) bool {
	o := m.h.ClientLocked(owner)
	if o == nil || !o.Online() {
		return true
	}
	s := o.Street()
	return s == "" || inNaraka(s)
}

func (m *graves) act(cl *Client, frame json.RawMessage) {
	var f struct {
		Owner int64  `json:"owner"`
		Verb  string `json:"verb"`
	}
	if json.Unmarshal(frame, &f) != nil || (f.Verb != "mourn" && f.Verb != "celebrate") {
		return
	}
	refuse := func(text string) {
		m.h.SendLocked(cl, map[string]any{"t": "grave_done", "owner": f.Owner, "verb": f.Verb, "ok": false, "text": text})
	}
	g := m.byOwn[f.Owner]
	if g == nil || !m.live(g) || f.Owner == cl.ID() || streetKey(cl.Street()) != streetKey(g.street) {
		refuse(graveTextGone)
		return
	}
	if g.state != "none" {
		if g.byID == cl.ID() {
			refuse(graveTextSelf)
		} else {
			refuse(graveTextOther)
		}
		return
	}
	if !m.stillDead(g.owner) {
		// Alive again and the clear never came: the grave is gone (resurrect 1338).
		m.drop(g)
		refuse(graveTextGone)
		return
	}
	g.state, g.by, g.byID = f.Verb, cl.Name(), cl.ID()
	favor := graveFavor
	if g.giant == "generic" {
		favor = 0
	}
	m.h.SendLocked(cl, map[string]any{"t": "grave_done", "owner": g.owner, "verb": f.Verb, "ok": true, "name": g.name, "giant": g.giant, "favor": favor})
	m.toStreet(g.street, map[string]any{"t": "grave", "street": g.street, "grave": m.view(g)})
	msg := map[string]any{"t": "grave_mourned", "verb": f.Verb, "by": cl.Name(), "croak": g.croak}
	if o := m.h.ClientLocked(g.owner); o != nil && o.Online() {
		m.h.SendLocked(o, msg)
		return
	}
	if len(m.owed) >= graveOwedMax {
		cut := m.h.Now().Add(-graveOwedKeep)
		for k, o := range m.owed {
			if o.at.Before(cut) {
				delete(m.owed, k)
			}
		}
	}
	if len(m.owed) < graveOwedMax {
		m.owed[g.owner] = graveOwed{verb: f.Verb, by: cl.Name(), croak: g.croak, at: m.h.Now()}
	}
}
