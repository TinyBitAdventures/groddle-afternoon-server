package main

// Reactions on chat lines (the remake's own addition, Austin 2026-11-04: "can we
// add emoji responses?"). Glitch had none: its client turned a smiley typed in
// Local chat into the avatar's happy, angry or surprised animation (glitch-client
// port/Emotes.as, RightSideManager.as 470-474) and its updates feed had replies,
// but no chat line could be reacted to. Here a player may put one of a small
// fixed set (reactionKeys) on a line of Local chat, a party, a channel (Global
// Chat, Live Help, the Trade Channel) or a group chat, and on an IM line (im.go
// im_react): each of the set at most once a player a line, toggled.
//
// Client to server:
//
//	{"t":"react","line":1700000000123,"e":"heart","on":true}   a line by its "lid"; on false takes yours away, left out it toggles
//
// Server to client:
//
//	{"t":"reacted","line":1700000000123,"r":[{"e":"heart","by":[{"id":7,"name":"Ada"}]}]}   the line's reactions now, to everyone who has it
//	{"t":"error","code":"react","message":"…"}                                             refused (slow_down for the rate)
//
// Line ids: every Local line ("said", hub.go), party line ("party_said",
// party.go) and channel or group line ("channel_said" and the history of
// "channel_joined", channels.go) carries "lid", a number unique across the
// server's lines (it starts at the clock's milliseconds when the server starts,
// so a restart never gives an old id again). A game that does not know lid
// ignores it; one that does draws the reaction button only on lines with one.
// IM lines keep their own id in the conversation (im.go).
//
// Who may react, and who hears it: those who have the line. A Local line is had
// by everyone on the street when it was said, a party line by the members it
// went to (both kept as the line's audience), a channel line by whoever has the
// channel open now (its history goes to whoever opens it later). Not between
// players where either blocks the other (blockedBetween): a blocked player can't
// react to your lines, nor you to theirs. Each copy sent leaves out the
// reactions of anyone its player blocks (buddies.ignoring), and nobody is sent a
// line's reactions when they block whoever said it (their game hides the line),
// as blocks hide chat. A channel's cone of silence refuses reactions there too.
//
// Kept: the last reactKeep lines of each street, party and channel, none older
// than reactKeepFor, at most reactLinesMax in all, in memory only (the server's
// chat keeps no more than that: Local and party have no history, a channel its
// last chanHistory lines; a restart forgets channel history and reactions
// alike). IM reactions are kept with the IM line in -im-file.
//
// The chat log (chatlog.go) does not record reactions: it is there to read what
// players wrote, for moderation, and a reaction is one of seven fixed marks.
//
// Rates: reactBurst, then one each reactEvery, for react and im_react each.

import (
	"encoding/json"
	"sort"
	"time"
)

const (
	reactBurst      = 8
	reactEvery      = 500 * time.Millisecond
	reactKeep       = 50  // lines a street, party or channel keeps reactable (chanHistory)
	reactKeepThread = 500 // thread replies a channel keeps reactable (threads.go)
	reactKeepFor    = 6 * time.Hour
	reactLinesMax   = 20000
	reactPruneGap   = time.Minute
)

// reactionKeys are the reactions the game offers, in the order they show. The
// game's js/chat-reactions.js REACTIONS draws them (👍 ❤️ 😂 😮 😢 🎉 and Tiny
// Speck's own "Hi" sign).
var reactionKeys = []string{"like", "heart", "laugh", "wow", "sad", "tada", "hi"}

func reactionOrder(e string) int {
	for i, k := range reactionKeys {
		if k == e {
			return i
		}
	}
	return -1
}

func validReaction(e string) bool { return reactionOrder(e) >= 0 }

type reactBy struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type reaction struct {
	E  string    `json:"e"`
	By []reactBy `json:"by"`
}

// reactions are a line's, in reactionKeys order, none without a player.
type reactions []reaction

func (rs reactions) has(e string, id int64) bool {
	for _, r := range rs {
		if r.E == e {
			for _, b := range r.By {
				if b.ID == id {
					return true
				}
			}
		}
	}
	return false
}

// set puts id's e on (or takes it off); changed is false when it already was so.
func (rs reactions) set(e string, id int64, name string, on bool) (out reactions, changed bool) {
	if rs.has(e, id) == on {
		return rs, false
	}
	out = make(reactions, 0, len(rs)+1)
	placed := false
	for _, r := range rs {
		if r.E == e {
			placed = true
			by := make([]reactBy, 0, len(r.By)+1)
			for _, b := range r.By {
				if b.ID != id {
					by = append(by, b)
				}
			}
			if on {
				by = append(by, reactBy{ID: id, Name: name})
			}
			if len(by) > 0 {
				out = append(out, reaction{E: e, By: by})
			}
			continue
		}
		out = append(out, r)
	}
	if on && !placed {
		out = append(out, reaction{E: e, By: []reactBy{{ID: id, Name: name}}})
		sort.SliceStable(out, func(i, j int) bool { return reactionOrder(out[i].E) < reactionOrder(out[j].E) })
	}
	return out, true
}

// shownTo is what viewer sees: never the reactions of a player viewer blocks. Never nil.
func (rs reactions) shownTo(viewer int64, ign func(int64, int64) bool) []reaction {
	out := []reaction{}
	for _, r := range rs {
		by := make([]reactBy, 0, len(r.By))
		for _, b := range r.By {
			if b.ID == viewer || ign == nil || !ign(viewer, b.ID) {
				by = append(by, b)
			}
		}
		if len(by) > 0 {
			out = append(out, reaction{E: r.E, By: by})
		}
	}
	return out
}

// reactFrame reads a react or im_react: the line, the reaction and on (nil: toggle).
type reactFrame struct {
	With int64  `json:"with"`
	Line int64  `json:"line"`
	E    string `json:"e"`
	On   *bool  `json:"on"`
}

func init() { modules = append(modules, newReacts) }

type reactLine struct {
	kind   string // local | party | channel
	where  string // the street, the party's log id, the channel's room
	author int64
	at     time.Time
	seen   map[int64]bool // local and party: who has the line (the speaker too)
	r      reactions
}

type reacts struct {
	h       *Hub
	seq     int64
	lines   map[int64]*reactLine
	places  map[string][]int64 // kind:where -> its lines, oldest first
	rates   map[int64]*bucket
	prunely time.Time
}

func newReacts(h *Hub) Module {
	return &reacts{h: h, seq: h.Now().UnixMilli(), lines: map[int64]*reactLine{}, places: map[string][]int64{}, rates: map[int64]*bucket{}}
}

func (m *reacts) Register(r *Registry) {
	r.Handle("react", m.react)
	r.OnDisconnect(func(cl *Client) { delete(m.rates, cl.ID()) })
	// The chat modules give each line its id (chatLineIDLocked), and a channel's history its reactions.
	r.Provide("reactions.line", m.line)
	r.Provide("reactions.of", m.of)
	r.Provide("reactions.self", m) // threads.go: whether a lid is a line of a channel
}

// chatLineIDLocked is the id a new chat line carries (0 when the server has no reactions module):
// kind local, party or channel; where its street, party log id or room; seen who has it (local, party).
func (h *Hub) chatLineIDLocked(kind, where string, author int64, seen []int64) int64 {
	if fn, ok := h.ProvidedLocked("reactions.line").(func(string, string, int64, []int64) int64); ok {
		return fn(kind, where, author, seen)
	}
	return 0
}

// lineReactionsLocked is what viewer sees of line lid's reactions (nil for none, or no module).
func (h *Hub) lineReactionsLocked(lid, viewer int64) []reaction {
	if fn, ok := h.ProvidedLocked("reactions.of").(func(int64, int64) []reaction); ok {
		return fn(lid, viewer)
	}
	return nil
}

func (m *reacts) line(kind, where string, author int64, seen []int64) int64 {
	m.seq++
	l := &reactLine{kind: kind, where: where, author: author, at: m.h.Now()}
	if kind != "channel" && kind != "thread" {
		l.seen = make(map[int64]bool, len(seen)+1)
		l.seen[author] = true
		for _, id := range seen {
			l.seen[id] = true
		}
	}
	m.lines[m.seq] = l
	key := kind + ":" + where
	ids := append(m.places[key], m.seq)
	keep := reactKeep
	if kind == "thread" {
		keep = reactKeepThread // a channel's thread replies, all its threads together (threads.go)
	}
	for len(ids) > keep {
		delete(m.lines, ids[0])
		ids = ids[1:]
	}
	m.places[key] = ids
	m.prune(false)
	return m.seq
}

// prune forgets lines older than reactKeepFor (at most once each reactPruneGap unless forced) and keeps
// the total within reactLinesMax.
func (m *reacts) prune(force bool) {
	now := m.h.Now()
	if !force && len(m.lines) <= reactLinesMax && now.Sub(m.prunely) < reactPruneGap {
		return
	}
	m.prunely = now
	cut := now.Add(-reactKeepFor)
	for key, ids := range m.places {
		i := 0
		for i < len(ids) && (m.lines[ids[i]] == nil || m.lines[ids[i]].at.Before(cut)) {
			delete(m.lines, ids[i])
			i++
		}
		if i == len(ids) {
			delete(m.places, key)
		} else if i > 0 {
			m.places[key] = append([]int64(nil), ids[i:]...)
		}
	}
	if over := len(m.lines) - reactLinesMax; over > 0 {
		all := make([]int64, 0, len(m.lines))
		for id := range m.lines {
			all = append(all, id)
		}
		sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
		for _, id := range all[:over] {
			l := m.lines[id]
			delete(m.lines, id)
			key := l.kind + ":" + l.where
			ids := m.places[key]
			for len(ids) > 0 && m.lines[ids[0]] == nil {
				ids = ids[1:]
			}
			if len(ids) == 0 {
				delete(m.places, key)
			} else {
				m.places[key] = ids
			}
		}
	}
}

func (m *reacts) ignoring() func(int64, int64) bool {
	ign, _ := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return ign
}

func (m *reacts) of(lid, viewer int64) []reaction {
	l := m.lines[lid]
	if l == nil || len(l.r) == 0 {
		return nil
	}
	return l.r.shownTo(viewer, m.ignoring())
}

func (m *reacts) refuse(cl *Client, message string) {
	m.h.ErrorLocked(cl, "react", message)
}

func (m *reacts) react(cl *Client, frame json.RawMessage) {
	var msg reactFrame
	if json.Unmarshal(frame, &msg) != nil || msg.Line <= 0 {
		return
	}
	if !validReaction(msg.E) {
		m.refuse(cl, "That is not a reaction the game has.")
		return
	}
	b := m.rates[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.rates[cl.ID()] = b
	}
	if !b.take(m.h.Now(), reactBurst, reactEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	m.prune(false)
	l := m.lines[msg.Line]
	if l == nil {
		m.refuse(cl, "That line is too old to react to now.")
		return
	}
	id := cl.ID()
	if blockedBetween(m.h, id, l.author) {
		m.refuse(cl, "You can't react to that player's lines.")
		return
	}
	var to []int64
	switch l.kind {
	case "channel", "thread":
		fn, ok := m.h.ProvidedLocked("channels.audience").(func(string, int64) ([]int64, string))
		if !ok {
			m.refuse(cl, "That chat is not open to you.")
			return
		}
		members, why := fn(l.where, id)
		if why != "" {
			m.refuse(cl, why)
			return
		}
		to = members
	default:
		if !l.seen[id] {
			m.refuse(cl, "That line is not one you have.")
			return
		}
		for who := range l.seen {
			to = append(to, who)
		}
	}
	on := !l.r.has(msg.E, id)
	if msg.On != nil {
		on = *msg.On
	}
	next, changed := l.r.set(msg.E, id, cl.Name(), on)
	if !changed {
		// Already so (a second click on a slow line): the reactor gets the line's state again.
		m.h.SendLocked(cl, map[string]any{"t": "reacted", "line": msg.Line, "r": l.r.shownTo(id, m.ignoring())})
		return
	}
	l.r = next
	if l.kind == "channel" || l.kind == "thread" {
		// chatstore.go: a channel or group line's reactions are kept on the site with it.
		m.h.storeChatLocked(chatEntry{K: "react", Lid: msg.Line, R: storeReactions(l.r)})
	}
	m.tell(msg.Line, l, to)
}

// tell sends a line's reactions to each player in to who is online, as they may see them.
func (m *reacts) tell(lid int64, l *reactLine, to []int64) {
	sort.Slice(to, func(i, j int) bool { return to[i] < to[j] })
	ign := m.ignoring()
	for _, who := range to {
		cl := m.h.ClientLocked(who)
		if cl == nil {
			continue
		}
		// Someone who blocks the speaker has no line to put them on.
		if who != l.author && ign != nil && ign(who, l.author) {
			continue
		}
		m.h.SendLocked(cl, map[string]any{"t": "reacted", "line": lid, "r": l.r.shownTo(who, ign)})
	}
	if l.kind == "channel" || l.kind == "thread" {
		// web.go: the website's readers of the channel, each as they may see it.
		for _, cl := range m.h.webViewersLocked() {
			if !cl.ww.readsRoom(l.where) || (cl.ID() != l.author && ign != nil && ign(cl.ID(), l.author)) {
				continue
			}
			m.h.SendLocked(cl, map[string]any{"t": "reacted", "line": lid, "r": l.r.shownTo(cl.ID(), ign)})
		}
	}
}
