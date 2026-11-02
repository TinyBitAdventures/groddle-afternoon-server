package main

// The Butler's IMs between real players (register G367): while a home's owner
// is online but away from their home street, their Butler tells them who comes
// onto it and what visitors say there, and does what the owner tells it from
// afar. The rules are GameServerJS items/bag_butler.js and
// items/include/butler_data.js; the owner's IM window is the game's (a Butler
// chat in the Social drawer, "/butler …" in Local chat, js/butler-im.js).
//
//   - A visitor arriving: "$pc is on your Home Street." (bag_butler.js
//     onPlayerEnter 4198-4240, visitorNotifyOwnerHere), when the owner is
//     online and not on the home street (inside the house or the tower counts
//     as away: isOwnerHome 2938-2951, isOwnerInTower 2953-2963) and the
//     owner has not said quit (notifications, butler_data.js doFarCommand
//     1146-1175). The visitor becomes the Butler's most recent notification
//     (4202), forgotten when they leave (onPlayerExit 4269-4271).
//   - A visitor speaking: "$pc said '$param'" (butler_data.js notifyOwner
//     2390-2406, location.js butlerProcessText 4284-4297 from main.js
//     doLocalChat 1728-1730), under the same rule, also not after the owner's
//     "Stop telling me things visitors say" (bag_butler.js
//     stop_eavesdropping 300-330, the eavesdropping flag).
//   - The owner's far commands (butler_data.js doFarCommand 1189-1296):
//     tell / t <message> says tellMessage "$pc, $owner just told me to say
//     '$speech' to you." (butler_strings.js:284) on the street to the most
//     recent visitor, or farCommandTellFail when there is none (:101);
//     announce / a <message> says the message itself (announceMessage
//     "$speech", :285) to everybody there; who / w lists the players on the
//     street (whoMessage, :286-287). quit / resume and help are the game's
//     (they change only what the owner's game sends in butler_watch).
//
// Client to server:
//
//	{"t":"butler_watch","on":true,"notify":true,"listen":true}   the owner's game: it has a Butler, IMs about visitors, speech reports
//	{"t":"butler_announce","text":"…"}                         the owner: announce
//	{"t":"butler_tell","text":"…"}                             the owner: tell the most recent visitor
//	{"t":"butler_who"}                                         the owner: who is on my home street
//
// Server to client:
//
//	{"t":"butler_im","kind":"visitor","from":{"id":9,"name":"Bo"}}               to the owner, away
//	{"t":"butler_im","kind":"speech","from":{"id":9,"name":"Bo"},"text":"hi"}    to the owner, away
//	{"t":"butler_im","kind":"announced","text":"…"}                              confirmAnnounce
//	{"t":"butler_im","kind":"told","text":"…"}                                   confirmTell (text: what the Butler said)
//	{"t":"butler_im","kind":"tell_fail"}                                         farCommandTellFail
//	{"t":"butler_im","kind":"who","names":["Bo"]}                                whoMessage / whoMessageNone
//	{"t":"butler_im","kind":"no_butler"}                                         a command before butler_watch on
//	{"t":"butler_said","owner":7,"text":"…","to":{"id":9,"name":"Bo"}}            to everyone on HOME_<owner>: the Butler says it (sendBubbleAndChat)
//
// Ours: the owner's game says whether it has a Butler (the Butler lives in the
// owner's own save; a false claim only lets an owner hear and speak on their
// own home street, which standing there does anyway). Blocks both ways keep a
// visitor's arrival and words from the owner and the Butler's lines from a
// visitor (buddies.go blockedBetween). The Butler's lines on the street are
// written to the chat log as the owner's local lines. Rates.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

const (
	butlerSayBurst = 4
	butlerSayEvery = 4 * time.Second
	butlerWhoBurst = 4
	butlerWhoEvery = 2 * time.Second
	butlerTellText = "%s, %s just told me to say '%s' to you." // butler_strings.js:284 tellMessage
)

func init() { modules = append(modules, newButlerIM) }

// butlerWatch is what the owner's game said about its Butler.
type butlerWatch struct {
	notify bool // not "quit" (butler_data.js notifications)
	listen bool // not "stop telling me things visitors say" (bag_butler.js eavesdropping)
}

type butlerIM struct {
	h      *Hub
	watch  map[int64]*butlerWatch // owner online with a Butler
	recent map[int64]int64        // owner -> the most recent visitor on their home street
	rates  map[string]*bucket
}

func newButlerIM(h *Hub) Module {
	return &butlerIM{h: h, watch: map[int64]*butlerWatch{}, recent: map[int64]int64{}, rates: map[string]*bucket{}}
}

func (m *butlerIM) Register(r *Registry) {
	r.Handle("butler_watch", m.watchMsg)
	r.Handle("butler_announce", m.announce)
	r.Handle("butler_tell", m.tell)
	r.Handle("butler_who", m.who)
	r.OnJoin(m.joined)
	r.OnLeave(m.left)
	r.OnChat(m.chat)
	r.OnDisconnect(func(cl *Client) { delete(m.watch, cl.ID()) })
}

func (m *butlerIM) allow(kind string, cl *Client, burst float64, every time.Duration) bool {
	key := kind + ":" + strconv.FormatInt(cl.ID(), 10)
	b := m.rates[key]
	if b == nil {
		b = &bucket{}
		m.rates[key] = b
	}
	if !b.take(m.h.Now(), burst, every) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return false
	}
	return true
}

// homeOf is the owner whose home street this is (HOME_<id>, not the house), or 0.
func homeOf(street string) int64 {
	owner, house := homeOwner(street)
	if house {
		return 0
	}
	return owner
}

// away is the owner online with a Butler and not on their home street (notifyOwner 2396).
func (m *butlerIM) away(owner int64) (*Client, *butlerWatch) {
	w := m.watch[owner]
	o := m.h.ClientLocked(owner)
	if w == nil || o == nil || o.Street() == homeStreetPrefix+strconv.FormatInt(owner, 10) {
		return nil, nil
	}
	return o, w
}

func (m *butlerIM) watchMsg(cl *Client, frame json.RawMessage) {
	var msg struct {
		On     bool `json:"on"`
		Notify bool `json:"notify"`
		Listen bool `json:"listen"`
	}
	_ = json.Unmarshal(frame, &msg)
	if !msg.On {
		delete(m.watch, cl.ID())
		return
	}
	m.watch[cl.ID()] = &butlerWatch{notify: msg.Notify, listen: msg.Listen}
}

// joined is onPlayerEnter: the Butler notes the visitor and IMs an owner who is away.
func (m *butlerIM) joined(cl *Client) {
	owner := homeOf(cl.Street())
	if owner == 0 || owner == cl.ID() {
		return
	}
	m.recent[owner] = cl.ID()
	if o, w := m.away(owner); o != nil && w.notify && !blockedBetween(m.h, owner, cl.ID()) {
		m.h.SendLocked(o, map[string]any{"t": "butler_im", "kind": "visitor", "from": map[string]any{"id": cl.ID(), "name": cl.Name()}})
	}
}

// left is onPlayerExit: the most recent visitor is forgotten when they go.
func (m *butlerIM) left(cl *Client, street string) {
	if owner := homeOf(street); owner != 0 && m.recent[owner] == cl.ID() {
		delete(m.recent, owner)
	}
}

// chat is notifyOwner: a visitor's Local chat line on the home street, to an owner who is away.
func (m *butlerIM) chat(cl *Client, text string) {
	owner := homeOf(cl.Street())
	if owner == 0 || owner == cl.ID() {
		return
	}
	if o, w := m.away(owner); o != nil && w.notify && w.listen && !blockedBetween(m.h, owner, cl.ID()) {
		m.h.SendLocked(o, map[string]any{"t": "butler_im", "kind": "speech", "from": map[string]any{"id": cl.ID(), "name": cl.Name()}, "text": text})
	}
}

// say is sendBubbleAndChat on the owner's home street: everyone there but a player blocking or blocked by the owner.
// What is said goes through the word filter (wordfilter.go); the log keeps the owner's words as typed. It returns what was said.
func (m *butlerIM) say(owner *Client, text string, to *Client) string {
	street := homeStreetPrefix + strconv.FormatInt(owner.ID(), 10)
	shown := filterWords(text)
	msg := map[string]any{"t": "butler_said", "owner": owner.ID(), "text": shown}
	if to != nil {
		msg["to"] = map[string]any{"id": to.ID(), "name": to.Name()}
	}
	b, _ := json.Marshal(msg)
	for _, o := range m.h.StreetLocked(street) {
		if o.ID() == owner.ID() || !blockedBetween(m.h, owner.ID(), o.ID()) {
			m.h.pushLocked(o, b)
		}
	}
	m.h.logChatLocked("local", street, owner, text, "", nil) // chatlog.go: the owner's words, said by the Butler
	return shown
}

func (m *butlerIM) command(cl *Client, frame json.RawMessage) (string, bool) {
	if !m.allow("butler_say", cl, butlerSayBurst, butlerSayEvery) {
		return "", false
	}
	if m.watch[cl.ID()] == nil {
		m.h.SendLocked(cl, map[string]any{"t": "butler_im", "kind": "no_butler"})
		return "", false
	}
	var msg struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(frame, &msg)
	return cleanChat(msg.Text), true
}

// announce is doFarCommand's announceRe (1245-1268): the Butler says it to everybody on the street.
func (m *butlerIM) announce(cl *Client, frame json.RawMessage) {
	text, ok := m.command(cl, frame)
	if !ok || text == "" {
		return
	}
	m.h.SendLocked(cl, map[string]any{"t": "butler_im", "kind": "announced", "text": m.say(cl, text, nil)})
}

// tell is doFarCommand's tellRecentRe (1196-1244): to the most recent visitor, if they are still there.
func (m *butlerIM) tell(cl *Client, frame json.RawMessage) {
	text, ok := m.command(cl, frame)
	if !ok || text == "" {
		return
	}
	var to *Client
	if id := m.recent[cl.ID()]; id != 0 {
		if v := m.h.ClientLocked(id); v != nil && v.Street() == homeStreetPrefix+strconv.FormatInt(cl.ID(), 10) {
			to = v
		}
	}
	if to == nil || blockedBetween(m.h, cl.ID(), to.ID()) {
		m.h.SendLocked(cl, map[string]any{"t": "butler_im", "kind": "tell_fail"})
		return
	}
	said := fmt.Sprintf(butlerTellText, to.Name(), cl.Name(), text)
	m.h.SendLocked(cl, map[string]any{"t": "butler_im", "kind": "told", "text": m.say(cl, said, to)})
}

// who is doFarCommand's whoRe (1270-1296): the players on the home street now.
func (m *butlerIM) who(cl *Client, frame json.RawMessage) {
	if !m.allow("butler_who", cl, butlerWhoBurst, butlerWhoEvery) {
		return
	}
	if m.watch[cl.ID()] == nil {
		m.h.SendLocked(cl, map[string]any{"t": "butler_im", "kind": "no_butler"})
		return
	}
	names := []string{}
	for _, o := range m.h.StreetLocked(homeStreetPrefix + strconv.FormatInt(cl.ID(), 10)) {
		if o.ID() != cl.ID() && !blockedBetween(m.h, cl.ID(), o.ID()) {
			names = append(names, o.Name())
		}
	}
	sort.Strings(names)
	m.h.SendLocked(cl, map[string]any{"t": "butler_im", "kind": "who", "names": names})
}
