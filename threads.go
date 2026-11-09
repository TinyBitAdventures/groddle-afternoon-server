package main

// Threads on chat lines (Austin 2026-11-04: "Is it possible to have threaded
// conversations on an individual chat?"). The remake's own: Glitch's chat had
// none (its forums had threads, its chat panes a flat log). Any line of Global
// Chat, Live Help, the Trade Channel or a group's chat can take replies in a
// thread of its own; the replies are not lines of the channel (they never show
// in its log), the line shows how many it has and who replied last, and
// opening it shows them with a box of its own. IM threads are im.go's: an
// im_send with "parent" (the conversation's two players only).
//
// Client to server (the game; the website's replies are the site's signed
// web_say with "parent", webchat.go):
//
//	{"t":"thread_say","channel":"global","parent":1730000000123,"text":"hi"}   a reply under that line
//	{"t":"thread_get","channel":"global","parent":1730000000123}               the replies the server keeps
//
// Server to client:
//
//	{"t":"thread_said","channel":"global","parent":…,"line":{"id":7,"name":"Ada","text":"hi","at":…,"lid":…,"web":true},"n":3,"last":{"id":7,"name":"Ada"}}
//	        to everyone with the channel open (the game) and its readers on the website (web.go), not to one
//	        who blocks the replier; n and last are the thread's as the server knows them
//	{"t":"thread","channel":"global","parent":…,"lines":[line…],"known":true}  thread_get's answer, oldest
//	        first, with their reactions ("r") as you may see them; known false when the server no longer
//	        holds that thread (the site keeps every reply: groddle/v1/chat/channels/<key>/threads/<lid>)
//	{"t":"error","code":"thread","message":"…"}
//
// A channel line's history (channel_joined) carries "th":{"n":3,"last":{…}}
// for each line with replies (left out without), counted as the reader may
// see them (replies by players they block are not counted).
//
// The rules are the channel's (channels.go say): the channel open on this
// socket, its cone of silence, the same rate (one bucket with channel_say),
// one cleaned line through the word filter (wordfilter.go; the chat log
// keeps it as typed, as a channel line), a suspended account can't connect.
// A reply is a line of its own for reactions (its own lid, reactions.go kind
// "thread"), and is kept on the site with its parent (chatstore.go "parent").
// Blocks are applied by whoever reads, as in the channel: the game hides a
// blocked player's lines (realtimeHidden), the website's socket never gets
// them (web.go), the site leaves them out of what it serves.
//
// Only a line of the channel takes replies: a parent is a line the server has
// for that channel (its history, a reactable line, a thread it holds), never
// a reply. Kept here: the last threadKeep replies of each thread (n counts
// them all) and at most threadsMax threads, the least recently replied to
// going first; in memory only (a restart forgets them with the channel's
// history; the site keeps them all).

import (
	"encoding/json"
	"sort"
	"time"
)

const (
	threadKeep = 200
	threadsMax = 5000
)

type threadSum struct {
	N    int     `json:"n"`
	Last reactBy `json:"last"`
}

type chanThread struct {
	room    string
	replies []chanLine
	n       int
	at      time.Time
}

func init() { modules = append(modules, newThreads) }

type threads struct {
	h       *Hub
	threads map[int64]*chanThread // by the parent's lid
	replyOf map[int64]int64       // a reply's lid -> its parent
}

func newThreads(h *Hub) Module {
	return &threads{h: h, threads: map[int64]*chanThread{}, replyOf: map[int64]int64{}}
}

func (m *threads) Register(r *Registry) {
	r.Handle("thread_say", m.say)
	r.Handle("thread_get", m.get)
	r.Provide("threads.summary", m.summary)
	r.Provide("threads.web", m.webReply) // webchat.go
	r.Provide("threads.self", m)         // chatdelete.go
}

func (m *threads) refuse(cl *Client, message string) {
	m.h.ErrorLocked(cl, "thread", message)
}

func (m *threads) channels() *channels {
	c, _ := m.h.provided["channels.self"].(*channels)
	return c
}

// summary is the thread under lid as viewer may see it, nil when it has no reply they may see.
func (m *threads) summary(lid, viewer int64) *threadSum {
	t := m.threads[lid]
	if t == nil || t.n == 0 {
		return nil
	}
	hidden := 0
	var last *chanLine
	for i := len(t.replies) - 1; i >= 0; i-- {
		r := &t.replies[i]
		if r.ID != viewer && m.h.ignoringLocked(viewer, r.ID) {
			hidden++
			continue
		}
		if last == nil {
			last = r
		}
	}
	n := t.n - hidden
	if n <= 0 || last == nil {
		return nil
	}
	return &threadSum{N: n, Last: reactBy{ID: last.ID, Name: last.Name}}
}

// known is whether lid is a line of room the server has (its history, a reactable channel line, a
// thread it holds), and not a reply.
func (m *threads) known(room string, lid int64) bool {
	if lid <= 0 || m.replyOf[lid] != 0 {
		return false
	}
	if t := m.threads[lid]; t != nil {
		return t.room == room
	}
	if c := m.channels(); c != nil {
		if r := c.rooms[room]; r != nil {
			for _, l := range r.history {
				if l.Lid == lid {
					return true
				}
			}
		}
	}
	if rx, ok := m.h.provided["reactions.self"].(*reacts); ok {
		if l := rx.lines[lid]; l != nil {
			return l.kind == "channel" && l.where == room
		}
	}
	return false
}

func threadFrame(frame json.RawMessage) (ch, text string, parent int64, ok bool) {
	var msg struct {
		Channel string `json:"channel"`
		Parent  int64  `json:"parent"`
		Text    string `json:"text"`
	}
	if json.Unmarshal(frame, &msg) != nil || !knownChannel(msg.Channel) || msg.Parent <= 0 {
		return "", "", 0, false
	}
	return msg.Channel, msg.Text, msg.Parent, true
}

func (m *threads) say(cl *Client, frame json.RawMessage) {
	ch, raw, parent, ok := threadFrame(frame)
	c := m.channels()
	if !ok || c == nil {
		return
	}
	text := cleanChat(raw)
	if text == "" {
		return
	}
	id := cl.ID()
	room := c.open[id][ch]
	w := c.watchFor(cl)
	if room == "" || w == nil {
		m.refuse(cl, chanNotIn)
		return
	}
	if ((room == "global" || room == "trade") && w.silence["chat"]) || ((room == "help" || room == "newbie") && w.silence["help"]) || m.h.modSilencedRoomLocked(id, room) { // moderation.go
		m.h.logChatLocked("channel", room, cl, text, "silenced", nil)
		m.refuse(cl, chanSuspended)
		return
	}
	if !m.known(room, parent) {
		m.refuse(cl, "That line is too old to reply to here. Reply to a newer one, or on the website's /chat/.")
		return
	}
	if !c.allow("say", cl, chanSayBurst, chanSayEvery) {
		m.h.logChatLocked("channel", room, cl, text, "slow_down", nil)
		return
	}
	line := chanLine{ID: id, Name: cl.Name(), Text: filterWords(text), At: m.h.Now().UnixMilli()}
	line = m.add(ch, room, parent, line)
	m.h.logChatLineLocked("channel", room, id, cl.Name(), text, "", nil, line.Lid)
}

// webReply is webchat.go's: a reply written on the website, under a line the server has for that
// channel or one the site keeps for it (kept: the site found it in its history, so an old line takes
// replies too). The line comes back with its lid; a zero line when it may not go.
func (m *threads) webReply(ch, room string, parent int64, kept bool, line chanLine) chanLine {
	if m.replyOf[parent] != 0 || (!kept && !m.known(room, parent)) {
		return chanLine{}
	}
	return m.add(ch, room, parent, line)
}

// add keeps a reply, tells everyone who has the channel, and sends it to the site.
func (m *threads) add(ch, room string, parent int64, line chanLine) chanLine {
	line.Lid = m.h.chatLineIDLocked("thread", room, line.ID, nil)
	t := m.threads[parent]
	if t == nil {
		t = &chanThread{room: room}
		m.threads[parent] = t
		m.prune()
	}
	t.replies = append(t.replies, line)
	if over := len(t.replies) - threadKeep; over > 0 {
		for _, r := range t.replies[:over] {
			delete(m.replyOf, r.Lid)
		}
		t.replies = append([]chanLine{}, t.replies[over:]...)
	}
	t.n++
	t.at = m.h.Now()
	if line.Lid > 0 {
		m.replyOf[line.Lid] = parent
	}
	out := map[string]any{"id": line.ID, "name": line.Name, "text": line.Text, "at": line.At}
	if line.Lid > 0 {
		out["lid"] = line.Lid
	}
	if line.Web {
		out["web"] = true
	}
	said := map[string]any{"t": "thread_said", "channel": ch, "parent": parent, "line": out, "n": t.n, "last": reactBy{ID: line.ID, Name: line.Name}}
	if c := m.channels(); c != nil {
		c.broadcast(room, said, 0)
	}
	m.h.webRoomLocked(room, said, line.ID)
	if line.Lid > 0 {
		m.h.storeChatLocked(chatEntry{K: "line", Room: room, Lid: line.Lid, ID: line.ID, Name: line.Name, Text: line.Text, At: line.At, Web: line.Web, Parent: parent})
	}
	return line
}

// prune keeps at most threadsMax threads, the least recently replied to going first.
func (m *threads) prune() {
	over := len(m.threads) - threadsMax
	if over <= 0 {
		return
	}
	ids := make([]int64, 0, len(m.threads))
	for id := range m.threads {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := m.threads[ids[i]], m.threads[ids[j]]
		return a.at.Before(b.at) || (a.at.Equal(b.at) && ids[i] < ids[j])
	})
	for _, id := range ids[:over] {
		for _, r := range m.threads[id].replies {
			delete(m.replyOf, r.Lid)
		}
		delete(m.threads, id)
	}
}

func (m *threads) get(cl *Client, frame json.RawMessage) {
	ch, _, parent, ok := threadFrame(frame)
	c := m.channels()
	if !ok || c == nil {
		return
	}
	id := cl.ID()
	room := c.open[id][ch]
	if room == "" {
		m.refuse(cl, chanNotIn)
		return
	}
	t := m.threads[parent]
	lines := []chanLine{}
	known := t != nil && t.room == room
	if known {
		for _, r := range t.replies {
			if r.ID != id && m.h.ignoringLocked(id, r.ID) {
				continue
			}
			r.R = m.h.lineReactionsLocked(r.Lid, id)
			lines = append(lines, r)
		}
	} else if m.known(room, parent) {
		known = true // a line of the channel nobody has replied to yet
	}
	m.h.SendLocked(cl, map[string]any{"t": "thread", "channel": ch, "parent": parent, "lines": lines, "known": known})
}
