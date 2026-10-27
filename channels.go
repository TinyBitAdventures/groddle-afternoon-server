package main

// Channels and group chat (register G326): the game-wide chat channels of the
// original (Global Chat, Live Help and its newbie twin, the Trade Channel)
// and the chat of player groups. A channel in the source is a group with a
// fixed tsid that anyone may open (config_prod.js:1290-1294
// live_help_groups, newbie_live_help_groups, global_chat_groups,
// trade_chat_groups; players/inc_groups.js:319-404 handles them before
// membership); a player group's chat is open to its members only
// (inc_groups.js:325 this.groups.groups[tsid]). The groups themselves (name,
// members, invites) are the site's (groddle-afternoon-site src/Groups.php,
// tables groddle_groups and groddle_group_members): this module never takes
// a group list from a client. The site signs one (the same secret and
// signing as the realtime ticket) and the game hands it over; from then on
// the socket may open the chat of those groups only, and of the channels
// the site says this player may use.
//
// Client to server:
//
//	{"t":"groups_watch","token":"…"}                  the list the site signed (groddle/v1/groups answers one); again after each join or leave
//	{"t":"channel_join","channel":"global"}           global | help | trade | group:<id>
//	{"t":"channel_leave","channel":"global"}
//	{"t":"channel_say","channel":"global","text":"hi"}
//	{"t":"channel_who","channel":"group:12"}          the source's /who in a group pane (main.js:5573-5582)
//
// Server to client:
//
//	{"t":"groups_status","groups":[12],"channels":["global","help"],"newbie":true,"open":["global"]}   after each watch
//	{"t":"channel_joined","channel":"global","history":[{"id":7,"name":"Ada","text":"hi","at":…}]}  the last chanHistory lines
//	{"t":"channel_left","channel":"group:12"}                                                      you left, or may no longer be there
//	{"t":"channel_said","channel":"global","id":7,"name":"Ada","text":"hi","at":1730000000000}     to everyone in it, you too
//	{"t":"channel_note","channel":"group:12","text":"Ada has joined"}                              the source's lines from "God"
//	{"t":"error","code":"channels","message":"…"}
//
// The watch token is base64url(JSON) "." base64url(HMAC-SHA256(secret, the
// first part)), with JSON {"k":"groups","sub":7,"g":"…","c":"global,newbie,trade",
// "s":"","iat":…,"exp":…}: g the player's group ids (buddies.go buddyIDs: sorted,
// differences, base 36, comma separated), c the channels the site lets them use
// (global always; newbie or help by level; trade with the upgrade), s their cone
// of silence ("chat", "help" or both, comma separated). A realtime ticket and a
// buddy list have another "k", so neither is a group list.
//
// Rules kept from the source:
//   - Live Help: a player under level 11 gets the newbie channel, others the
//     regular one (main.js:579-585); both show as "Live Help". The site
//     decides from the save's level; the game asks for "help".
//   - Trade Channel: only with the Trade Channelling upgrade
//     (player.js:3404 canJoinTradeChat; main.js:591-594).
//   - Who is in a channel is not announced; a player group tells its chat who
//     has joined and left, and tells the one joining who is there
//     (group.js:1008-1052 chat_join / chat_leave, chat_get_roster_msg
//     1105-1123). /who answers the roster in either (main.js:5573-5582).
//   - Saying something needs you in that chat: "You're not in that group"
//     (group.js:1054-1061); a group you are not in is "Group not found"
//     (inc_groups.js:329, 346).
//   - The cone of silence: "Sorry, you're currently suspended from this
//     chat." in Global Chat and the Trade Channel (main.js:5821, 5833), and
//     the help cone in Live Help (main.js:5809). Group chat has no cone.
//   - Going offline leaves every chat (inc_groups.js:371-391 groups_logout).
//   - Blocking: the source delivers a blocked player's lines and the client
//     drops them (RightSideManager.as:218-223 chatUpdate, pc.ignored), so the
//     game hides them (buddies-feature realtimeHidden); nothing is refused here.
//
// Ours: each chat keeps its last chanHistory lines, sent to whoever opens it;
// a /who in a channel names at most chanWhoNames players; rates; the chat a
// player has open survives a new socket for the same character (a reload).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	chanHistory      = 50 // lines each chat keeps for whoever opens it
	chanWhoNames     = 30 // names a /who in a big channel lists before "and N others"
	chanMaxGroups    = 64 // group ids one token may carry (the site's limit is 20, config_prod.js:39 max_groups)
	chanSayBurst     = 3
	chanSayEvery     = time.Second
	chanJoinBurst    = 12
	chanJoinEvery    = time.Second
	chanWatchBurst   = 6
	chanWatchEvery   = 5 * time.Second
	chanSuspended    = "Sorry, you're currently suspended from this chat." // main.js:5809, 5821, 5833
	chanNotIn        = "You're not in that group"                          // group.js:1058
	chanGroupMissing = "Group not found"                                   // inc_groups.js:329, 346, 365
)

// The source's channel rooms (config_prod.js:1290-1294); a group's room is its name, "group:<id>".
var chanRooms = map[string]bool{"global": true, "help": true, "newbie": true, "trade": true}

// chanSecretForTest is the secret in tests (the flag is not parsed there).
var chanSecretForTest []byte

func init() { modules = append(modules, newChannels) }

type chanLine struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Text string `json:"text"`
	At   int64  `json:"at"`
}

type chanRoom struct {
	members map[int64]bool
	history []chanLine
}

type chanWatch struct {
	groups  map[int64]bool
	rooms   map[string]bool // the channel rooms this player may open
	silence map[string]bool // "chat", "help"
	exp     time.Time
}

type channels struct {
	h      *Hub
	secret []byte
	watch  map[int64]*chanWatch
	rooms  map[string]*chanRoom
	open   map[int64]map[string]string // player -> the chat as the game names it -> its room
	rates  map[string]*bucket
}

func newChannels(h *Hub) Module {
	secret := chanSecretForTest
	if !testing.Testing() {
		path := ""
		if f := flag.Lookup("secret-file"); f != nil {
			path = expandHome(f.Value.String())
		}
		// No secret leaves group chat off: every watch is refused, so no chat opens.
		secret, _ = readSecret(path)
	}
	return &channels{h: h, secret: secret, watch: map[int64]*chanWatch{}, rooms: map[string]*chanRoom{},
		open: map[int64]map[string]string{}, rates: map[string]*bucket{}}
}

func (m *channels) Register(r *Registry) {
	r.Handle("groups_watch", m.watchMsg)
	r.Handle("channel_join", m.join)
	r.Handle("channel_leave", m.leave)
	r.Handle("channel_say", m.say)
	r.Handle("channel_who", m.who)
	r.OnDisconnect(m.disconnected)
}

// chanLog notes a refusal in the server log (quiet under go test).
func chanLog(format string, args ...any) {
	if !testing.Testing() {
		log.Printf("channels: "+format, args...)
	}
}

// —— Tokens ——

type chanClaims struct {
	K   string `json:"k"`
	Sub int64  `json:"sub"`
	G   string `json:"g"`
	C   string `json:"c"`
	S   string `json:"s"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

var (
	errChanOff     = errors.New("group chat is not set up on this server")
	errChanToken   = errors.New("that is not a token the site signed")
	errChanExpired = errors.New("that token has expired")
	errChanOwner   = errors.New("that token is not yours")
)

func (m *channels) verify(token string, owner int64) (chanClaims, error) {
	var c chanClaims
	if len(m.secret) == 0 {
		return c, errChanOff
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return c, errChanToken
	}
	want := hmac.New(sha256.New, m.secret)
	want.Write([]byte(body))
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want.Sum(nil)) {
		return c, errChanToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.K != "groups" || c.Sub <= 0 {
		return chanClaims{}, errChanToken
	}
	if c.Sub != owner {
		return chanClaims{}, errChanOwner
	}
	if c.Exp < m.h.Now().Unix() {
		return chanClaims{}, errChanExpired
	}
	return c, nil
}

func chanWords(s string, allowed map[string]bool) (map[string]bool, bool) {
	out := map[string]bool{}
	if s == "" {
		return out, true
	}
	for _, w := range strings.Split(s, ",") {
		if !allowed[w] {
			return nil, false
		}
		out[w] = true
	}
	return out, true
}

// —— Names ——

// roomFor is the room behind the chat the game names, for this watcher: "" when it is none of theirs.
func (m *channels) roomFor(w *chanWatch, channel string) (room string, group int64) {
	switch channel {
	case "global", "trade":
		if w.rooms[channel] {
			return channel, 0
		}
	case "help":
		// main.js:579-585: the newbie channel under level 11, else the regular one.
		if w.rooms["newbie"] {
			return "newbie", 0
		}
		if w.rooms["help"] {
			return "help", 0
		}
	default:
		if id, ok := groupChannel(channel); ok && w.groups[id] {
			return channel, id
		}
	}
	return "", 0
}

// groupChannel reads "group:<id>".
func groupChannel(channel string) (int64, bool) {
	rest, ok := strings.CutPrefix(channel, "group:")
	if !ok || rest == "" || len(rest) > 19 || rest[0] == '0' {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
}

func isGroupRoom(room string) bool { return strings.HasPrefix(room, "group:") }

func knownChannel(channel string) bool {
	if channel == "global" || channel == "help" || channel == "trade" {
		return true
	}
	_, ok := groupChannel(channel)
	return ok
}

// —— Helpers ——

func (m *channels) send(id int64, msg any) {
	if cl := m.h.ClientLocked(id); cl != nil {
		m.h.SendLocked(cl, msg)
	}
}

// broadcast sends msg to everyone in room but except (0: everyone).
func (m *channels) broadcast(room string, msg any, except int64) {
	r := m.rooms[room]
	if r == nil {
		return
	}
	ids := make([]int64, 0, len(r.members))
	for id := range r.members {
		if id != except {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		m.send(id, msg)
	}
}

func (m *channels) allow(kind string, cl *Client, burst float64, every time.Duration) bool {
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

func (m *channels) refuse(cl *Client, message string) {
	m.h.ErrorLocked(cl, "channels", message)
}

// names lists who is in room, sorted, but skip.
func (m *channels) names(room string, skip int64) []string {
	r := m.rooms[room]
	var out []string
	if r == nil {
		return out
	}
	for id := range r.members {
		if id == skip {
			continue
		}
		if cl := m.h.ClientLocked(id); cl != nil {
			out = append(out, cl.Name())
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

// rosterText is group.js:1105-1123 chat_get_roster_msg, with ours: at most chanWhoNames names.
func rosterText(names []string) string {
	switch len(names) {
	case 0:
		return "You are the only person here."
	case 1:
		return names[0] + " is here"
	}
	if len(names) > chanWhoNames {
		more := len(names) - chanWhoNames
		return strings.Join(names[:chanWhoNames], ", ") + " and " + strconv.Itoa(more) + " others are here"
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " are here"
}

func (m *channels) watchFor(cl *Client) *chanWatch {
	w := m.watch[cl.ID()]
	if w == nil || w.exp.Before(m.h.Now()) {
		return nil
	}
	return w
}

// —— Handlers ——

func (m *channels) watchMsg(cl *Client, frame json.RawMessage) {
	var msg struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	if !m.allow("watch", cl, chanWatchBurst, chanWatchEvery) {
		return
	}
	c, err := m.verify(msg.Token, cl.ID())
	var groups map[int64]bool
	var rooms, silence map[string]bool
	if err == nil {
		var ok1, ok2, ok3 bool
		groups, ok1 = buddyIDs(c.G)
		rooms, ok2 = chanWords(c.C, chanRooms)
		silence, ok3 = chanWords(c.S, map[string]bool{"chat": true, "help": true})
		if !ok1 || !ok2 || !ok3 || len(groups) > chanMaxGroups || (rooms["help"] && rooms["newbie"]) {
			err = errChanToken
		}
	}
	if err != nil {
		chanLog("player %d (%s): groups_watch refused: %v", cl.ID(), cl.Name(), err)
		m.refuse(cl, "Your groups could not be checked: "+err.Error()+".")
		return
	}
	id := cl.ID()
	w := &chanWatch{groups: groups, rooms: rooms, silence: silence, exp: time.Unix(c.Exp, 0)}
	m.watch[id] = w
	// A chat the new list no longer allows closes; Live Help moves to the
	// channel the player's level now gives (main.js:579-585).
	open := m.open[id]
	chans := make([]string, 0, len(open))
	for ch := range open {
		chans = append(chans, ch)
	}
	sort.Strings(chans)
	for _, ch := range chans {
		was := open[ch]
		room, _ := m.roomFor(w, ch)
		if room == was {
			continue
		}
		m.leaveRoom(cl, ch, was, room == "")
		if room != "" {
			m.joinRoom(cl, ch, room)
		}
	}
	var gs []int64
	for g := range groups {
		gs = append(gs, g)
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i] < gs[j] })
	var cs []string
	for _, ch := range []string{"global", "help", "trade"} {
		if room, _ := m.roomFor(w, ch); room != "" {
			cs = append(cs, ch)
		}
	}
	chanLog("player %d (%s): watching %d groups, channels %v", id, cl.Name(), len(gs), cs)
	m.send(id, map[string]any{"t": "groups_status", "groups": nonNil(gs), "channels": nonNilS(cs), "newbie": rooms["newbie"], "open": m.openList(id)})
}

func nonNil(v []int64) []int64 {
	if v == nil {
		return []int64{}
	}
	return v
}

func nonNilS(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (m *channels) openList(id int64) []string {
	out := []string{}
	for ch := range m.open[id] {
		out = append(out, ch)
	}
	sort.Strings(out)
	return out
}

func channelOf(frame json.RawMessage) (string, string, bool) {
	var msg struct {
		Channel string `json:"channel"`
		Text    string `json:"text"`
	}
	if json.Unmarshal(frame, &msg) != nil || !knownChannel(msg.Channel) {
		return "", "", false
	}
	return msg.Channel, msg.Text, true
}

func (m *channels) join(cl *Client, frame json.RawMessage) {
	ch, _, ok := channelOf(frame)
	if !ok {
		m.refuse(cl, "That is not a chat the game has.")
		return
	}
	if !m.allow("join", cl, chanJoinBurst, chanJoinEvery) {
		return
	}
	w := m.watchFor(cl)
	if w == nil {
		chanLog("player %d (%s): %s refused: no signed group list", cl.ID(), cl.Name(), ch)
		m.refuse(cl, "Your groups are not checked yet. Try again in a moment.")
		return
	}
	room, _ := m.roomFor(w, ch)
	if room == "" {
		if strings.HasPrefix(ch, "group:") {
			chanLog("player %d (%s): %s refused: not in their signed list", cl.ID(), cl.Name(), ch)
			m.refuse(cl, chanGroupMissing)
		} else if ch == "trade" {
			m.refuse(cl, "The Trade Channel needs the Trade Channelling upgrade.")
		} else {
			m.refuse(cl, "That chat is not open to you.")
		}
		return
	}
	if m.open[cl.ID()][ch] == room {
		// Open already (a reload, a second click): the history again, nobody told.
		m.send(cl.ID(), map[string]any{"t": "channel_joined", "channel": ch, "history": m.history(room)})
		return
	}
	if was := m.open[cl.ID()][ch]; was != "" {
		m.leaveRoom(cl, ch, was, false)
	}
	m.joinRoom(cl, ch, room)
}

// joinRoom is group.js:1008-1030 chat_join.
func (m *channels) joinRoom(cl *Client, ch, room string) {
	id := cl.ID()
	r := m.rooms[room]
	if r == nil {
		r = &chanRoom{members: map[int64]bool{}}
		m.rooms[room] = r
	}
	m.send(id, map[string]any{"t": "channel_joined", "channel": ch, "history": m.history(room)})
	if isGroupRoom(room) {
		// The one joining hears who is there; everyone there hears they joined.
		m.send(id, map[string]any{"t": "channel_note", "channel": ch, "text": rosterText(m.names(room, id))})
		m.broadcast(room, map[string]any{"t": "channel_note", "channel": ch, "text": cl.Name() + " has joined"}, id)
	}
	r.members[id] = true
	if m.open[id] == nil {
		m.open[id] = map[string]string{}
	}
	m.open[id][ch] = room
}

// leaveRoom is group.js:1032-1052 chat_leave; tell sends the player channel_left.
func (m *channels) leaveRoom(cl *Client, ch, room string, tell bool) {
	id := cl.ID()
	if r := m.rooms[room]; r != nil && r.members[id] {
		delete(r.members, id)
		if isGroupRoom(room) {
			m.broadcast(room, map[string]any{"t": "channel_note", "channel": ch, "text": cl.Name() + " has left"}, id)
		}
	}
	if open := m.open[id]; open != nil {
		delete(open, ch)
		if len(open) == 0 {
			delete(m.open, id)
		}
	}
	if tell {
		m.send(id, map[string]any{"t": "channel_left", "channel": ch})
	}
}

func (m *channels) history(room string) []chanLine {
	r := m.rooms[room]
	if r == nil {
		return []chanLine{}
	}
	return append([]chanLine{}, r.history...)
}

func (m *channels) leave(cl *Client, frame json.RawMessage) {
	ch, _, ok := channelOf(frame)
	if !ok || !m.allow("join", cl, chanJoinBurst, chanJoinEvery) {
		return
	}
	room := m.open[cl.ID()][ch]
	if room == "" {
		m.send(cl.ID(), map[string]any{"t": "channel_left", "channel": ch})
		return
	}
	m.leaveRoom(cl, ch, room, true)
}

func (m *channels) say(cl *Client, frame json.RawMessage) {
	ch, raw, ok := channelOf(frame)
	if !ok {
		return
	}
	text := cleanChat(raw)
	if text == "" {
		return
	}
	id := cl.ID()
	room := m.open[id][ch]
	w := m.watchFor(cl)
	if room == "" || w == nil {
		// group.js:1058 chat_send: not in the roster.
		m.refuse(cl, chanNotIn)
		return
	}
	// main.js:5808-5843: the cones of silence.
	if ((room == "global" || room == "trade") && w.silence["chat"]) || ((room == "help" || room == "newbie") && w.silence["help"]) {
		m.refuse(cl, chanSuspended)
		return
	}
	if !m.allow("say", cl, chanSayBurst, chanSayEvery) {
		return
	}
	line := chanLine{ID: id, Name: cl.Name(), Text: text, At: m.h.Now().UnixMilli()}
	r := m.rooms[room]
	r.history = append(r.history, line)
	if over := len(r.history) - chanHistory; over > 0 {
		r.history = append([]chanLine{}, r.history[over:]...)
	}
	// chat_send_msg: everyone in it, the speaker too.
	m.broadcast(room, map[string]any{"t": "channel_said", "channel": ch, "id": id, "name": line.Name, "text": text, "at": line.At}, 0)
}

func (m *channels) who(cl *Client, frame json.RawMessage) {
	ch, _, ok := channelOf(frame)
	if !ok || !m.allow("join", cl, chanJoinBurst, chanJoinEvery) {
		return
	}
	room := m.open[cl.ID()][ch]
	if room == "" {
		m.refuse(cl, chanNotIn)
		return
	}
	m.send(cl.ID(), map[string]any{"t": "channel_note", "channel": ch, "text": rosterText(m.names(room, 0))})
}

// —— Lifecycle ——

// disconnected is inc_groups.js:371-391 groups_logout: every chat is left.
func (m *channels) disconnected(cl *Client) {
	id := cl.ID()
	open := m.open[id]
	chans := make([]string, 0, len(open))
	for ch := range open {
		chans = append(chans, ch)
	}
	sort.Strings(chans)
	for _, ch := range chans {
		m.leaveRoom(cl, ch, open[ch], false)
	}
	for room, r := range m.rooms {
		// A group's chat nobody has open keeps its lines; an empty one goes.
		if len(r.members) == 0 && len(r.history) == 0 && isGroupRoom(room) {
			delete(m.rooms, room)
		}
	}
	delete(m.watch, id)
	delete(m.open, id)
	suffix := ":" + strconv.FormatInt(id, 10)
	for k := range m.rates {
		if strings.HasSuffix(k, suffix) {
			delete(m.rates, k)
		}
	}
}
