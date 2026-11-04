package main

// IMs between players: one player's private messages to another (the
// original's IMs: GameServerJS main.js doSendIM 4263-4380, im_send in, im_recv
// out; the client's IM panes, RightSideManager.as and ChatArea.as). Kept from
// the source: anyone may IM anyone, not yourself ('self', 4317), not between
// players where either ignores the other ('ignoring you' 4344-4348, 'on your
// ignore list' 4350-4354, buddies_is_ignored_by / buddies_is_ignoring), the
// text trimmed and through the chat filter (4266-4268; ours is wordfilter.go,
// with markup taken out where filter_chat kept <b> and <i>), the echo to the
// sender carries the text as filtered (the client showed its own line only
// from it, NCMsgHandler.as do_IM_SEND 1395-1408). The client took at most 1000
// characters in any chat (InputField.as:32), with no cap on the server; here
// an IM is a chat line like every other (maxChat). The source had no rate.
//
// Ours (Austin 2026-11-02): an IM to a player who is offline is kept and given
// to them when they are back (the source refused it: 'offline', 4356-4359,
// the client's "<Name> is offline"), and a short history is kept here so a
// reload or another computer shows the conversation (the client kept each
// chat's last 150 lines in Flash local storage, ChatHistory.as 47-93,
// ChatArea.as:53 MAX_ELEMENTS). The source wrote every IM's text to its
// action log (apiLogAction('IM', …, 'msg='+msg.txt), 4342); IMs are private
// here and their text is written nowhere but the state file below and the
// site's long-term history (chatstore.go, Austin 2026-11-04), which gives a
// conversation to its two players only.
//
// Client to server:
//
//	{"t":"im_send","to":9,"text":"hi"}   an IM to player 9 (the sender is this socket's character, never a field)
//	{"t":"im_sync"}                      the conversations kept for you (sent once your buddy list is in)
//	{"t":"im_read","with":9}             you have read the conversation with 9
//	{"t":"im_close","with":9}            you closed it: it leaves your list until the next line in it
//	{"t":"im_react","with":9,"line":12,"e":"heart","on":true}   a reaction on line 12 of the conversation with 9 (reactions.go; on left out toggles)
//
// Server to client:
//
//	{"t":"im","with":{"id":9,"name":"Bo","online":true},"line":{"id":12,"from":7,"text":"hi","at":1730000000000,"r":[…]},"unread":1}
//	        a line in the conversation with 9: to the recipient, and back to the sender (from is the sender;
//	        online false on the sender's copy: Bo gets it when they are back)
//	{"t":"im_state","convs":[{"with":{"id":9,"name":"Bo","online":false},"lines":[line…],"unread":2}]}
//	        your open conversations, newest first, after im_sync (and after a new socket's buddy list)
//	{"t":"im_reacted","with":9,"line":12,"r":[{"e":"heart","by":[{"id":7,"name":"Ada"}]}]}   line 12's reactions now, to both
//	        (a line's "r" is left out while it has none; refused: {"t":"error","code":"react",…} or slow_down)
//	{"t":"im_no","reason":"…","with":9,"message":"…"}  an im_send refused: reason is the source's
//	        ("self", "ignoring you", "on your ignore list", "offline") or ours ("full": imUnreadMax waiting
//	        unread, "slow_down"); the client says "<Name> is <reason>" as the source's did (RightSideManager.as:600)
//
// Who may IM whom: anyone online, and while they are offline anyone on your
// buddy list or who has you on theirs (the site signs both, buddies.go), or
// anyone you already have a conversation with: a player id the server cannot
// vouch for is refused ("offline", as the source said), so nobody can write to ids made up. Not
// between players where either blocks the other (blockedBetween, the
// buddy list the site signed): refused while the recipient's list is known, and
// a line kept for someone offline is dropped, unseen, if their list says they
// block the sender when they come back. Lines are given to a player only once
// their game has sent its buddy list (so their blocks are known first).
//
// The text is a chat line (cleanChat: one trimmed line, at most maxChat
// characters, no control characters), markup taken out (stripMarkup: the
// client draws text, never HTML, but a line is kept and shown on other
// screens), and through the word filter (wordfilter.go) before it is kept or
// sent: the original words are never stored.
//
// Kept (offline delivery and history): the last imKeep lines of each
// conversation, none older than imKeepFor, each side's read mark and closed
// flag, at most imConvsPer conversations a player (the oldest goes) and
// imConvsMax in all; at most imUnreadMax lines from one sender waiting
// unread ("full"). The state file -im-file (default realtime-im.json beside
// -secret-file; off: memory only, mode 0600) is written within a second of a
// change and at shutdown. IM text is private: it is never written to the chat
// log (chatlog.go) or the server's log, and the administrators' console has no
// call that reads it (nor does the site's: its groddle_im_lines answer only the
// conversation's two players). Rates: imBurst lines then one each imEvery; imNewBurst new
// conversations then one each imNewEvery.

import (
	"encoding/json"
	"flag"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	imKeep      = 50                  // lines kept a conversation (the drawer shows 200)
	imKeepFor   = 14 * 24 * time.Hour // a line is forgotten after this
	imUnreadMax = 20                  // lines one sender may have waiting unread
	imConvsPer  = 100                 // conversations kept a player
	imConvsMax  = 5000                // conversations kept in all
	imBurst     = 5
	imEvery     = time.Second
	imNewBurst  = 10
	imNewEvery  = time.Minute
	imSyncBurst = 4
	imSyncEvery = 5 * time.Second
	imSaveAfter = time.Second
)

var imFileFlag = flag.String("im-file", "", "file that keeps players' IMs for offline delivery and a short history (default: realtime-im.json beside -secret-file; off: memory only)")

// imFileForTest lets a test give the module a state file; under go test the flag is ignored.
var imFileForTest string

var markupRe = regexp.MustCompile(`<[^<>]*>`)

// stripMarkup takes tags out of a line ("<b>hi</b>" is "hi"); a lone < or > stays.
func stripMarkup(s string) string {
	for i := 0; i < 4; i++ {
		t := markupRe.ReplaceAllString(s, "")
		if t == s {
			break
		}
		s = t
	}
	return s
}

func init() { modules = append(modules, newIMs) }

type imLine struct {
	ID   int64     `json:"id"`
	From int64     `json:"from"`
	Text string    `json:"text"`
	At   int64     `json:"at"`          // Unix ms
	R    reactions `json:"r,omitempty"` // reactions.go: the two players' reactions on it
}

// imConv is the conversation between A and B (A < B); index 0 is A's side, 1 is B's.
type imConv struct {
	A      int64     `json:"a"`
	B      int64     `json:"b"`
	Names  [2]string `json:"names"`
	Lines  []imLine  `json:"lines"`
	Seq    int64     `json:"seq"`
	Read   [2]int64  `json:"read"`
	Closed [2]bool   `json:"closed"`
	Last   int64     `json:"last"` // Unix ms of the last line
}

type imFile struct {
	V     int       `json:"v"`
	Convs []*imConv `json:"convs"`
}

type ims struct {
	h      *Hub
	convs  map[[2]int64]*imConv
	by     map[int64]map[[2]int64]bool // player -> their conversations
	sent   map[int64]*Client           // the socket im_state went to
	want   map[int64]bool              // asked (im_sync) before their buddy list was in
	rates  map[string]*bucket
	path   string
	saving bool
}

func newIMs(h *Hub) Module {
	m := &ims{h: h, convs: map[[2]int64]*imConv{}, by: map[int64]map[[2]int64]bool{}, sent: map[int64]*Client{}, want: map[int64]bool{}, rates: map[string]*bucket{}}
	if m.path = statePath(imFileForTest, imFileFlag, "realtime-im.json"); m.path != "" {
		var f imFile
		if ok, err := readStateFile("im", m.path, &f); err != nil {
			log.Printf("im %s: %v (IMs are kept in memory only)", m.path, err)
			m.path = ""
		} else if ok {
			for _, c := range f.Convs {
				if c != nil && c.A > 0 && c.B > c.A {
					m.put(c)
				}
			}
			m.prune()
		}
	}
	return m
}

func (m *ims) Register(r *Registry) {
	r.Handle("im_send", m.send)
	r.Handle("im_sync", m.sync)
	r.Handle("im_read", m.read)
	r.Handle("im_close", m.close)
	r.Handle("im_react", m.react)
	r.OnConnect(func(cl *Client) { delete(m.sent, cl.ID()); delete(m.want, cl.ID()) })
	r.OnDisconnect(func(cl *Client) {
		delete(m.sent, cl.ID())
		delete(m.want, cl.ID())
		m.forget(cl.ID())
	})
	r.OnShutdown(func() { m.save() })
	r.Provide("ims.self", m) // chatstore.go reads the kept lines once
	// buddies.go calls this when a player's signed list is in: their blocks are known, so their lines can go.
	r.Provide("im.watched", m.watched)
}

func pairOf(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}

func (c *imConv) side(id int64) int {
	if id == c.B {
		return 1
	}
	return 0
}

func (c *imConv) other(id int64) int64 {
	if id == c.A {
		return c.B
	}
	return c.A
}

// unread is the lines from the other side after id's read mark.
func (c *imConv) unread(id int64) int {
	n, mark := 0, c.Read[c.side(id)]
	for _, l := range c.Lines {
		if l.From != id && l.ID > mark {
			n++
		}
	}
	return n
}

func (m *ims) put(c *imConv) {
	k := [2]int64{c.A, c.B}
	m.convs[k] = c
	for _, id := range k {
		if m.by[id] == nil {
			m.by[id] = map[[2]int64]bool{}
		}
		m.by[id][k] = true
	}
}

func (m *ims) drop(k [2]int64) {
	delete(m.convs, k)
	for _, id := range k {
		delete(m.by[id], k)
		if len(m.by[id]) == 0 {
			delete(m.by, id)
		}
	}
}

// prune forgets lines older than imKeepFor and conversations left with none.
func (m *ims) prune() {
	cut := m.h.Now().Add(-imKeepFor).UnixMilli()
	for k, c := range m.convs {
		i := 0
		for i < len(c.Lines) && c.Lines[i].At < cut {
			i++
		}
		if i > 0 {
			c.Lines = append([]imLine(nil), c.Lines[i:]...)
		}
		if len(c.Lines) == 0 {
			m.drop(k)
		}
	}
}

// oldestOf is the conversation with the oldest last line among keys.
func (m *ims) oldestOf(keys map[[2]int64]bool, except [2]int64) ([2]int64, bool) {
	var best [2]int64
	found := false
	for k := range keys {
		if k == except {
			continue
		}
		c := m.convs[k]
		if !found || c.Last < m.convs[best].Last || (c.Last == m.convs[best].Last && (k[0] < best[0] || (k[0] == best[0] && k[1] < best[1]))) {
			best, found = k, true
		}
	}
	return best, found
}

// makeRoom keeps a new conversation within imConvsPer for each side and imConvsMax in all.
func (m *ims) makeRoom(k [2]int64) {
	for _, id := range k {
		for len(m.by[id]) > imConvsPer {
			old, ok := m.oldestOf(m.by[id], k)
			if !ok {
				break
			}
			m.drop(old)
		}
	}
	for len(m.convs) > imConvsMax {
		all := make(map[[2]int64]bool, len(m.convs))
		for kk := range m.convs {
			all[kk] = true
		}
		old, ok := m.oldestOf(all, k)
		if !ok {
			break
		}
		m.drop(old)
	}
}

func (m *ims) allow(kind string, cl *Client, burst float64, every time.Duration) bool {
	key := kind + ":" + strconv.FormatInt(cl.ID(), 10)
	b := m.rates[key]
	if b == nil {
		b = &bucket{}
		m.rates[key] = b
	}
	return b.take(m.h.Now(), burst, every)
}

func (m *ims) forget(id int64) {
	suffix := ":" + strconv.FormatInt(id, 10)
	for k := range m.rates {
		if strings.HasSuffix(k, suffix) {
			delete(m.rates, k)
		}
	}
}

// ready is whether id is online with a signed buddy list in (their blocks known), or the
// build has no buddies module or no secret for it (then no blocks are known anywhere).
func (m *ims) ready(id int64) *Client {
	cl := m.h.ClientLocked(id)
	if cl == nil {
		return nil
	}
	if fn, ok := m.h.ProvidedLocked("buddies.watching").(func(int64) bool); ok && !fn(id) {
		return nil
	}
	return cl
}

// vouched is whether the server knows to IM id is to IM a real player: online, on from's signed
// buddy list either way, or in a conversation with from already.
func (m *ims) vouched(from, to int64) bool {
	if m.h.ClientLocked(to) != nil || m.convs[pairOf(from, to)] != nil {
		return true
	}
	fn, ok := m.h.ProvidedLocked("buddies.holds").(func(int64, int64) bool)
	return ok && fn(from, to)
}

func (m *ims) who(c *imConv, id int64) map[string]any {
	return map[string]any{"id": id, "name": c.Names[c.side(id)], "online": m.h.ClientLocked(id) != nil}
}

func lineMsg(l imLine) map[string]any {
	msg := map[string]any{"id": l.ID, "from": l.From, "text": l.Text, "at": l.At}
	if len(l.R) > 0 {
		msg["r"] = l.R
	}
	return msg
}

// no refuses an IM to with: the source's reason (or ours) for the client's "<Name> is <reason>".
func (m *ims) no(cl *Client, with int64, reason, message string) {
	m.h.SendLocked(cl, map[string]any{"t": "im_no", "reason": reason, "with": with, "message": message})
}

func (m *ims) send(cl *Client, frame json.RawMessage) {
	var msg struct {
		To   int64  `json:"to"`
		Text string `json:"text"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	from := cl.ID()
	text := strings.TrimSpace(cleanChat(stripMarkup(cleanChat(msg.Text))))
	if text == "" || msg.To <= 0 {
		return
	}
	if msg.To == from {
		m.no(cl, from, "self", "You can't IM yourself.")
		return
	}
	if !m.allow("im", cl, imBurst, imEvery) {
		m.no(cl, msg.To, "slow_down", "Slow down a little.")
		return
	}
	if ign, ok := m.h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool); ok {
		if ign(msg.To, from) {
			m.no(cl, msg.To, "ignoring you", "That player is ignoring you.")
			return
		}
		if ign(from, msg.To) {
			m.no(cl, msg.To, "on your ignore list", "That player is on your ignore list.")
			return
		}
	}
	if !m.vouched(from, msg.To) {
		m.no(cl, msg.To, "offline", "That player is offline.")
		return
	}
	k := pairOf(from, msg.To)
	c := m.convs[k]
	if c == nil {
		if !m.allow("im_new", cl, imNewBurst, imNewEvery) {
			m.no(cl, msg.To, "slow_down", "Slow down a little.")
			return
		}
		c = &imConv{A: k[0], B: k[1]}
		m.put(c)
		m.makeRoom(k)
	} else {
		waiting := 0
		mark := c.Read[c.side(msg.To)]
		for _, l := range c.Lines {
			if l.From == from && l.ID > mark {
				waiting++
			}
		}
		if waiting >= imUnreadMax {
			m.no(cl, msg.To, "full", "They have "+strconv.Itoa(waiting)+" of your IMs to read already.")
			return
		}
	}
	c.Names[c.side(from)] = cl.Name()
	if to := m.h.ClientLocked(msg.To); to != nil {
		c.Names[c.side(msg.To)] = to.Name()
	}
	now := m.h.Now().UnixMilli()
	c.Seq++
	l := imLine{ID: c.Seq, From: from, Text: filterWords(text), At: now}
	c.Lines = append(c.Lines, l)
	if len(c.Lines) > imKeep {
		c.Lines = append([]imLine(nil), c.Lines[len(c.Lines)-imKeep:]...)
	}
	c.Last = now
	c.Closed = [2]bool{}
	c.Read[c.side(from)] = c.Seq // your own line is read
	m.saveSoon()
	m.h.storeChatLocked(imEntry(c, l)) // chatstore.go: kept on the site, for the two of them only
	m.h.SendLocked(cl, map[string]any{"t": "im", "with": m.who(c, msg.To), "line": lineMsg(l), "unread": c.unread(from)})
	if to := m.ready(msg.To); to != nil && m.sent[msg.To] == to {
		m.h.SendLocked(to, map[string]any{"t": "im", "with": m.who(c, from), "line": lineMsg(l), "unread": c.unread(msg.To)})
	}
}

// state is the conversations for cl, newest first: not closed, not with a player either blocks.
func (m *ims) state(cl *Client) {
	id := cl.ID()
	m.sent[id] = cl
	delete(m.want, id)
	m.prune()
	var list []*imConv
	for k := range m.by[id] {
		c := m.convs[k]
		if c == nil || c.Closed[c.side(id)] || blockedBetween(m.h, id, c.other(id)) {
			continue
		}
		// A line kept while you were away from someone you block is not yours to read.
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Last != list[j].Last {
			return list[i].Last > list[j].Last
		}
		return list[i].other(id) < list[j].other(id)
	})
	convs := make([]map[string]any, 0, len(list))
	for _, c := range list {
		lines := make([]map[string]any, 0, len(c.Lines))
		for _, l := range c.Lines {
			lines = append(lines, lineMsg(l))
		}
		convs = append(convs, map[string]any{"with": m.who(c, c.other(id)), "lines": lines, "unread": c.unread(id)})
	}
	m.h.SendLocked(cl, map[string]any{"t": "im_state", "convs": convs})
}

func (m *ims) sync(cl *Client, frame json.RawMessage) {
	if !m.allow("im_sync", cl, imSyncBurst, imSyncEvery) {
		return
	}
	if m.ready(cl.ID()) == nil {
		m.want[cl.ID()] = true // buddies.go's watch sends it (watched)
		return
	}
	m.state(cl)
}

// watched is buddies.go's word that cl's signed list is in: the first time on a socket, or
// when it asked before, it gets its conversations.
func (m *ims) watched(cl *Client) {
	if m.want[cl.ID()] || m.sent[cl.ID()] != cl {
		m.state(cl)
	}
}

func (m *ims) withMsg(cl *Client, frame json.RawMessage) *imConv {
	var msg struct {
		With int64 `json:"with"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.With <= 0 {
		return nil
	}
	return m.convs[pairOf(cl.ID(), msg.With)]
}

func (m *ims) read(cl *Client, frame json.RawMessage) {
	if c := m.withMsg(cl, frame); c != nil && c.Read[c.side(cl.ID())] != c.Seq {
		c.Read[c.side(cl.ID())] = c.Seq
		m.saveSoon()
	}
}

func (m *ims) close(cl *Client, frame json.RawMessage) {
	if c := m.withMsg(cl, frame); c != nil {
		s := c.side(cl.ID())
		c.Read[s], c.Closed[s] = c.Seq, true
		if c.Closed[0] && c.Closed[1] {
			m.drop([2]int64{c.A, c.B}) // both closed it: nothing is kept
		}
		m.saveSoon()
	}
}

// react is a reaction on an IM line (reactions.go): either side of the conversation, not while either
// blocks the other; both are told.
func (m *ims) react(cl *Client, frame json.RawMessage) {
	var msg reactFrame
	if json.Unmarshal(frame, &msg) != nil || msg.With <= 0 || msg.Line <= 0 {
		return
	}
	if !validReaction(msg.E) {
		m.h.ErrorLocked(cl, "react", "That is not a reaction the game has.")
		return
	}
	if !m.allow("react", cl, reactBurst, reactEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return
	}
	id := cl.ID()
	c := m.convs[pairOf(id, msg.With)]
	if c == nil || msg.With == id {
		m.h.ErrorLocked(cl, "react", "That line is too old to react to now.")
		return
	}
	if blockedBetween(m.h, id, msg.With) {
		m.h.ErrorLocked(cl, "react", "You can't react to that player's lines.")
		return
	}
	i := -1
	for j := range c.Lines {
		if c.Lines[j].ID == msg.Line {
			i = j
		}
	}
	if i < 0 {
		m.h.ErrorLocked(cl, "react", "That line is too old to react to now.")
		return
	}
	l := &c.Lines[i]
	on := !l.R.has(msg.E, id)
	if msg.On != nil {
		on = *msg.On
	}
	next, changed := l.R.set(msg.E, id, cl.Name(), on)
	if changed {
		l.R = next
		m.saveSoon()
		m.h.storeChatLocked(imReactEntry(c, *l))
	}
	r := []reaction(l.R)
	if r == nil {
		r = []reaction{}
	}
	m.h.SendLocked(cl, map[string]any{"t": "im_reacted", "with": msg.With, "line": l.ID, "r": r})
	if !changed {
		return
	}
	if to := m.ready(msg.With); to != nil && m.sent[msg.With] == to {
		m.h.SendLocked(to, map[string]any{"t": "im_reacted", "with": id, "line": l.ID, "r": r})
	}
}

// —— The state file ——

func (m *ims) saveSoon() {
	if m.path == "" || m.saving {
		return
	}
	m.saving = true
	m.h.After(imSaveAfter, func() {
		m.saving = false
		m.save()
	})
}

func (m *ims) save() bool {
	if m.path == "" {
		return true
	}
	m.prune()
	f := imFile{V: 1, Convs: make([]*imConv, 0, len(m.convs))}
	for _, c := range m.convs {
		f.Convs = append(f.Convs, c)
	}
	sort.Slice(f.Convs, func(i, j int) bool {
		if f.Convs[i].A != f.Convs[j].A {
			return f.Convs[i].A < f.Convs[j].A
		}
		return f.Convs[i].B < f.Convs[j].B
	})
	b, err := json.Marshal(f)
	if err == nil {
		err = writeFileSynced(m.path, b)
	}
	if err != nil {
		log.Printf("im %s: %v", m.path, err) // never the text
		return false
	}
	return true
}
