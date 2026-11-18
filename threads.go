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
//	{"t":"error","code":"thread","message":"…","channel":"global","parent":…,"old":true}
//	        a reply refused; channel and parent name the line when the refusal is about one reply, and old
//	        when that line is not there to reply to (the game offers to say the words in Global instead)
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
// for that channel (its history, a reactable line, a thread it holds), or,
// for an older line (R5, register G1025), one the site keeps for that room,
// asked off the hub lock (POST groddle/v1/chat/line, signed like the ledger's
// calls: groddle-afternoon-site src/ChatHistory.php line), never a reply. The
// room is the one this socket has the channel open in, so the line is one the
// player may read. Kept here: the last threadKeep replies of each thread (n counts
// them all) and at most threadsMax threads, the least recently replied to
// going first; in memory only (a restart forgets them with the channel's
// history; the site keeps them all).

import (
	"encoding/json"
	"log"
	"sort"
	"testing"
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
	site    lineSite              // nil: only the lines this server holds take replies
	spawn   func(func())          // runs a site call off the hub lock (tests run it in place)
	asking  map[int64]bool        // a player's reply waiting on the site
}

// lineSite is the site's chat history as threads asks it (R5): whether it keeps lid as a line of
// room (not a reply, not deleted). ok false: no answer.
type lineSite interface {
	keptLine(room string, lid int64) (kept, ok bool)
}

const chatRouteLine = "/groddle/v1/chat/line"

func (l *httpLedger) keptLine(room string, lid int64) (bool, bool) {
	a := l.call(chatRouteLine, map[string]any{"room": room, "lid": lid})
	switch a.Status {
	case "settled":
		return true, true
	case "refused":
		return false, true
	}
	return false, false
}

// threadSiteForTest is the site a test's threads module asks (nil: none).
var threadSiteForTest lineSite

const (
	threadTextOld     = "That line is not there to reply to any more."
	threadTextNoSite  = "The chat's history did not answer, so that older line can't take a reply right now."
	threadTextWaiting = "Hold on, still sending your last reply."
)

func newThreads(h *Hub) Module {
	m := &threads{h: h, threads: map[int64]*chanThread{}, replyOf: map[int64]int64{}, spawn: func(fn func()) { go fn() }, asking: map[int64]bool{}}
	if testing.Testing() {
		m.site = threadSiteForTest
		if m.site != nil {
			m.spawn = func(fn func()) { fn() }
		}
	} else if site, _ := newLedgerFromFlags(); site != nil {
		if l, ok := site.(*httpLedger); ok {
			m.site = l
		}
	}
	return m
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

// refuseReply refuses one reply, naming its line, so the game can give the words back (old: the line is
// not there to reply to, and the game offers to say them in Global instead).
func (m *threads) refuseReply(cl *Client, ch string, parent int64, message string, old bool) {
	out := map[string]any{"t": "error", "code": "thread", "message": message, "channel": ch, "parent": parent}
	if old {
		out["old"] = true
	}
	m.h.queueLocked(cl, out)
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
	if !ok || m.channels() == nil {
		return
	}
	text := cleanChat(raw)
	if text == "" {
		return
	}
	room, ok := m.mayReply(cl, ch, text)
	if !ok {
		return
	}
	if m.known(room, parent) {
		m.reply(cl, ch, room, parent, text)
		return
	}
	m.askSite(cl, ch, room, parent, text)
}

// mayReply is the room cl has ch open in when they may say a line there now (refused otherwise).
func (m *threads) mayReply(cl *Client, ch, text string) (string, bool) {
	c := m.channels()
	if c == nil {
		return "", false
	}
	id := cl.ID()
	room := c.open[id][ch]
	w := c.watchFor(cl)
	if room == "" || w == nil {
		m.refuse(cl, chanNotIn)
		return "", false
	}
	if ((room == "global" || room == "trade") && w.silence["chat"]) || ((room == "help" || room == "newbie") && w.silence["help"]) || m.h.modSilencedRoomLocked(id, room) { // moderation.go
		m.h.logChatLocked("channel", room, cl, text, "silenced", nil)
		m.refuse(cl, chanSuspended)
		return "", false
	}
	return room, true
}

// reply says a reply under a line known to be room's.
func (m *threads) reply(cl *Client, ch, room string, parent int64, text string) {
	c := m.channels()
	if !c.allow("say", cl, chanSayBurst, chanSayEvery) {
		m.h.logChatLocked("channel", room, cl, text, "slow_down", nil) // allow said slow_down
		return
	}
	id := cl.ID()
	line := chanLine{ID: id, Name: cl.Name(), Text: filterWords(text), At: m.h.Now().UnixMilli()}
	line = m.add(ch, room, parent, line)
	m.h.logChatLineLocked("channel", room, id, cl.Name(), text, "", nil, line.Lid)
}

// askSite: a line this server does not hold (older than its history, or from before a restart) takes
// the reply when the site keeps it for that room (R5). The ask runs off the hub lock; its answer is
// checked again under it (the socket still there, the channel still open in that room, not silenced).
func (m *threads) askSite(cl *Client, ch, room string, parent int64, text string) {
	id := cl.ID()
	if m.site == nil || parent <= 0 || m.replyOf[parent] != 0 {
		m.refuseReply(cl, ch, parent, threadTextOld, true)
		return
	}
	if m.asking[id] {
		m.refuseReply(cl, ch, parent, threadTextWaiting, false)
		return
	}
	m.asking[id] = true
	site := m.site
	m.spawn(func() {
		kept, ok := site.keptLine(room, parent)
		m.h.After(0, func() {
			delete(m.asking, id)
			if m.h.clients[id] != cl || cl.closed {
				return
			}
			if !ok {
				log.Printf("threads: the site did not answer for %s line %d", room, parent)
				m.refuseReply(cl, ch, parent, threadTextNoSite, true)
				return
			}
			if !kept || m.replyOf[parent] != 0 {
				m.refuseReply(cl, ch, parent, threadTextOld, true)
				return
			}
			if now, ok := m.mayReply(cl, ch, text); !ok || now != room {
				return
			}
			m.reply(cl, ch, room, parent, text)
		})
	})
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
