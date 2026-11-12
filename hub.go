package main

import (
	"encoding/json"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// The protocol, one JSON object per text frame, "t" naming the kind.
//
// Client to server:
//
//	{"t":"auth","ticket":"…"}                          first frame, within authWait
//	{"t":"join","street":"GA5…","x":0,"y":0,"dir":1,"anim":"idle","look":{…}}
//	{"t":"move","x":0,"y":0,"dir":1,"anim":"walk"}
//	{"t":"chat","text":"hi"}
//	{"t":"typing"}                                     started a Local chat line (no text)
//	{"t":"emote","e":"wave"}
//	{"t":"leave"}                                      off the street, still connected
//	{"t":"ping"}
//
// Server to client:
//
//	{"t":"hello","you":{"id":7,"name":"Ada"},"cone":true}  cone only for a player under the cone of silence
//	{"t":"cone","on":false}                             the site lifted or laid the cone since hello
//	{"t":"street","street":"GA5…","players":[Player…]}  everyone else there, after a join
//	{"t":"joined","player":Player}
//	{"t":"left","id":7}
//	{"t":"moved","id":7,"x":0,"y":0,"dir":1,"anim":"walk"}
//	{"t":"said","id":7,"name":"Ada","text":"hi","at":1730000000000,"lid":1730000000123}  text through the word filter (wordfilter.go); lid the line's id for reactions (reactions.go)
//	{"t":"typing","id":7}                              Ada is typing a Local chat line: "…" over her for typingShow
//	{"t":"emoted","id":7,"e":"wave"}
//	{"t":"error","code":"…","message":"…"}
//	{"t":"pong"}
//	{"t":"teleport","street":"GA5…","x":0,"y":0,"by":"admin"}  an administrator moved you (teleport.go; the site's call, never a client's)
//	{"t":"admin_message"|"booted"|"admin_give"|"admin_refill",…}  the administrators' console (admin.go; the site's calls, never a client's)
//
// One live session per character: a second socket for the same player closes the
// first with closeReplaced. One character per login in the game: a login's
// alternate characters (Claims.Acct) share it, and a second character of the
// same login connecting a game socket closes the first character's with
// closeOtherCharacter (4012, "Playing as another character"; the game does not
// reconnect on it). The website's /chat/ sockets (web.go) are never closed by
// it. An administrator's boot closes with closeBooted (4010), a ban with
// closeBanned (4011), which also refuses a banned account at auth; a ban on a
// login covers every character of it (admin.go).
//
// The typing mark (register G366) is main.js doLocalChatStart 1677-1694: a
// player starting a Local chat line (ChatArea.as 529-537, local_chat_start) puts
// the typing_in_location_overlay over their head for everyone else on the
// location for 5 s (duration 5000, apiSendAnnouncementX: not the typer), and
// their line clears it (doLocalChat 1758, overlay_dismiss). The client sends one
// per line, at most each 4 s (ChatArea.as ACTIVITY_TIMER 4000); the server
// relays at most one each typingEvery, with no text, and not between players
// where either blocks the other (blockedBetween).
//
// The cone of silence (register G480) holds in Local chat as in the channels:
// no mark for a player under it (main.js doLocalChatStart 1679), and their line
// goes to nobody and comes back as "Sorry, you're currently suspended from this
// chat." (doLocalChat 1708-1710), before any Feat or hi hears it. It is the
// "chat" cone (isInConeOfSilence with no type, player.js 2927-2934), never the
// help cone. The hub's word comes from the signed ticket at connect and the
// newest signed groups list after (channels.go), so a client that sends no list,
// or an old one, can't speak its way out. hello carries "cone", and
// {"t":"cone","on":true} tells the game when a new list changes it.
//
// Shared systems (quoins, street objects, parties, …) are modules (modules.go),
// each in a file of its own with its message kinds listed in its header.

const (
	maxStreet   = 32
	maxLook     = 4096
	maxChat     = 1000 // InputField.as 32 MAX_CHARS: the original chat box took 1,000; the site's history keeps as many (ChatHistory::TEXT_MAX)
	maxAnim     = 24
	sendBuffer  = 64
	movesPerSec = 20
	chatBurst   = 5
	chatEvery   = time.Second
	typingEvery = 4 * time.Second // ChatArea.as:65 ACTIVITY_TIMER 4000
)

var (
	streetRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	wordRe   = regexp.MustCompile(`^[a-z0-9_]{1,24}$`)
)

// Player is one character as the others on a street see them.
type Player struct {
	ID   int64           `json:"id"`
	Name string          `json:"name"`
	X    float64         `json:"x"`
	Y    float64         `json:"y"`
	Dir  int             `json:"dir"`
	Anim string          `json:"anim"`
	Look json.RawMessage `json:"look,omitempty"`
}

// Client is one signed-in socket. The hub owns every field but send and done.
type Client struct {
	id     int64
	acct   int64 // the login (Claims.Acct; the character's own id when it has none). Never sent to anyone.
	name   string
	send   chan []byte
	done   chan struct{}
	closed bool
	why    int // close code once closed

	street string
	player Player

	moveWindow time.Time
	moveCount  int
	chatTokens float64
	chatAt     time.Time
	typingAt   time.Time

	// web.go: a socket from the website's /chat/ (Claims.Via "web"), never in clients or on a street.
	web     bool
	ww      *webWatch
	webRate bucket

	// For the close line (closeNoteLocked): when the socket connected, the last few frames queued
	// (only their slices are kept; a slow close reads their kinds), what the hub's close saw, and
	// the writer's own failure (main.go write, set off the hub lock).
	at     time.Time
	recent [4][]byte
	nsent  int
	detail string
	wfail  atomic.Pointer[string]
}

// Hub holds every connected character and the street each stands on.
type Hub struct {
	now func() time.Time

	mu      sync.Mutex
	clients map[int64]*Client
	streets map[string]map[int64]*Client
	logins  map[int64]int64 // login id to the one character of it with a game socket now (closeOtherCharacter)

	// Modules (modules.go): extra message kinds and lifecycle hooks, all run under mu.
	handlers     map[string]func(cl *Client, frame json.RawMessage)
	onConnect    []func(cl *Client)
	onJoin       []func(cl *Client)
	onLeave      []func(cl *Client, street string)
	onDisconnect []func(cl *Client)
	onShutdown   []func()
	onChat       []func(cl *Client, text string)
	joinGates    []func(cl *Client, street string) (ok bool, code, message string)
	afterFunc    func(d time.Duration, fn func()) func() bool
	provided     map[string]any // hooks one module offers another (Registry.Provide)

	// web.go: the website's sockets by character (any number of tabs), and presence.go's inputs:
	// the account's "Appear offline" (from its last ticket or the site's presence_hide call) and when
	// each character last did something on any of its sockets.
	webs    map[int64]map[*Client]bool
	onWeb   []func(cl *Client, event string)
	off     map[int64]bool
	inputAt map[int64]time.Time

	// The cone of silence's word for Local chat: the newest signed one per character (G480).
	cones map[int64]coneWord

	// The close lines' reconnect-loop watch (closeNoteLocked, noteConnectLocked): connects per character a minute.
	rates      map[int64]*connRate
	ratesSwept time.Time
}

// coneWord is whether a character is under the chat cone, as of a signed ticket or groups list made at iat.
type coneWord struct {
	on  bool
	iat int64
}

// setConeLocked takes the site's word on a character's chat cone unless a newer word already
// stands (a saved list resent after a newer ticket says nothing). True when the cone changed.
func (h *Hub) setConeLocked(id int64, on bool, iat int64) bool {
	was, known := h.cones[id]
	if known && iat < was.iat {
		return false
	}
	h.cones[id] = coneWord{on: on, iat: iat}
	return was.on != on
}

func NewHub(now func() time.Time) *Hub {
	if now == nil {
		now = time.Now
	}
	h := &Hub{now: now, clients: map[int64]*Client{}, streets: map[string]map[int64]*Client{}, logins: map[int64]int64{}, handlers: map[string]func(*Client, json.RawMessage){}, afterFunc: realAfter, provided: map[string]any{},
		webs: map[int64]map[*Client]bool{}, off: map[int64]bool{}, inputAt: map[int64]time.Time{}, cones: map[int64]coneWord{}, rates: map[int64]*connRate{}}
	for _, m := range modules {
		m(h).Register(&Registry{h: h})
	}
	// After every module's own hook: a character with no socket left has no presence (web.go).
	h.onDisconnect = append(h.onDisconnect, func(cl *Client) {
		h.forgetIdleLocked(cl.id)
		if h.logins[cl.acct] == cl.id && h.clients[cl.id] == nil {
			delete(h.logins, cl.acct)
		}
	})
	return h
}

// Shutdown runs each module's shutdown hook once, under mu, after the HTTP server has stopped:
// state saved within a second of a change is written now, not lost with the process.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, fn := range h.onShutdown {
		fn()
	}
}

// Stats counts characters online and streets with someone on them.
func (h *Hub) Stats() (players, streets int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients), len(h.streets)
}

// Connect adds a character, closing any socket it already had, and the game socket of any other
// character of the same login (closeOtherCharacter): that one goes offline as if it had left.
func (h *Hub) Connect(c Claims) *Client {
	cl := &Client{id: c.Sub, acct: c.login(), name: c.Name, send: make(chan []byte, sendBuffer), done: make(chan struct{}), chatTokens: chatBurst, chatAt: h.now(), at: h.now()}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.noteConnectLocked(cl)
	if other := h.logins[cl.acct]; other != 0 && other != cl.id {
		if oc := h.clients[other]; oc != nil && oc.acct == cl.acct {
			h.leaveLocked(oc)
			delete(h.clients, other)
			for _, fn := range h.onDisconnect {
				fn(oc)
			}
			h.closeLocked(oc, closeOtherCharacter)
		}
	}
	if old := h.clients[c.Sub]; old != nil {
		h.leaveLocked(old)
		h.closeLocked(old, closeReplaced)
		if old.acct != cl.acct && h.logins[old.acct] == old.id {
			delete(h.logins, old.acct)
		}
	}
	h.clients[c.Sub] = cl
	h.logins[cl.acct] = cl.id
	h.off[c.Sub] = c.Off
	h.inputAt[c.Sub] = h.now()
	h.setConeLocked(c.Sub, c.Cone, c.Iat)
	h.applyModClaimLocked(c.Sub, c.Mod, c.Iat) // moderation.go
	hello := map[string]any{"t": "hello", "you": map[string]any{"id": cl.id, "name": cl.name}, "off": c.Off}
	if h.modConeLocked(c.Sub) {
		hello["cone"] = true
	}
	h.queueLocked(cl, hello)
	for _, fn := range h.onConnect {
		fn(cl)
	}
	return cl
}

// Disconnect removes a socket that ended on its own.
func (h *Hub) Disconnect(cl *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[cl.id] == cl {
		h.leaveLocked(cl)
		delete(h.clients, cl.id)
		for _, fn := range h.onDisconnect {
			fn(cl)
		}
	}
	h.closeLocked(cl, 0)
}

// Handle acts on one frame from a client.
func (h *Hub) Handle(cl *Client, frame []byte) {
	defer healthNoteLag(time.Now()) // health.go: the slowest frame a minute (deferred first, so it runs after the unlock)
	var m struct {
		T      string          `json:"t"`
		Street string          `json:"street"`
		X      *float64        `json:"x"`
		Y      *float64        `json:"y"`
		Dir    int             `json:"dir"`
		Anim   string          `json:"anim"`
		Look   json.RawMessage `json:"look"`
		Text   string          `json:"text"`
		E      string          `json:"e"`
	}
	if json.Unmarshal(frame, &m) != nil {
		h.reply(cl, "bad_frame", "That message was not JSON.")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if cl.closed {
		return
	}
	if cl.web {
		h.handleWebLocked(cl, m.T, frame) // web.go: the website's few kinds
		return
	}
	if inputKinds[m.T] {
		h.touchLocked(cl.id) // presence.go: not idle
		// playtime.go: the game's inputs only (web sockets returned above). A move that changes only the
		// animation is the standing avatar's own idle animations (the game sends them for minutes), not the player.
		if m.T != "move" || cl.street == "" || coord(m.X) != cl.player.X || coord(m.Y) != cl.player.Y || sign(m.Dir) != cl.player.Dir {
			h.playInputLocked(cl)
		}
	}
	switch m.T {
	case "join":
		if !streetRe.MatchString(m.Street) {
			h.replyLocked(cl, "bad_street", "That street name is not one the game uses.")
			return
		}
		if len(m.Look) > maxLook || (len(m.Look) > 0 && !json.Valid(m.Look)) {
			m.Look = nil
		}
		for _, gate := range h.joinGates {
			if ok, code, message := gate(cl, m.Street); !ok {
				h.replyLocked(cl, code, message)
				return
			}
		}
		h.leaveLocked(cl)
		cl.street = m.Street
		cl.player = Player{ID: cl.id, Name: cl.name, X: coord(m.X), Y: coord(m.Y), Dir: sign(m.Dir), Anim: word(m.Anim, "idle"), Look: m.Look}
		room := h.streets[m.Street]
		if room == nil {
			room = map[int64]*Client{}
			h.streets[m.Street] = room
		}
		others := make([]Player, 0, len(room))
		for _, o := range room {
			others = append(others, o.player)
		}
		room[cl.id] = cl
		h.queueLocked(cl, map[string]any{"t": "street", "street": m.Street, "players": others})
		h.broadcastLocked(cl, map[string]any{"t": "joined", "player": cl.player})
		for _, fn := range h.onJoin {
			fn(cl)
		}
	case "move":
		if cl.street == "" || !h.allowMoveLocked(cl) {
			return
		}
		cl.player.X, cl.player.Y, cl.player.Dir, cl.player.Anim = coord(m.X), coord(m.Y), sign(m.Dir), word(m.Anim, cl.player.Anim)
		h.broadcastLocked(cl, map[string]any{"t": "moved", "id": cl.id, "x": cl.player.X, "y": cl.player.Y, "dir": cl.player.Dir, "anim": cl.player.Anim})
	case "chat":
		text := cleanChat(m.Text)
		if cl.street == "" || text == "" {
			return
		}
		if h.modConeLocked(cl.id) { // main.js doLocalChat 1708-1710; a moderator's chat silence too (moderation.go)
			h.logChatLocked("local", cl.street, cl, text, "silenced", nil) // chatlog.go; before the reply, which may drop a client that stopped reading
			h.replyLocked(cl, "suspended", chanSuspended)
			return
		}
		if !h.allowChatLocked(cl) {
			h.logChatLocked("local", cl.street, cl, text, "slow_down", nil) // chatlog.go; before the reply, which may drop a client that stopped reading
			h.replyLocked(cl, "slow_down", "Slow down a little.")
			return
		}
		shown := filterWords(text) // wordfilter.go (the remake's own addition): nobody hears a bad word, the log keeps it
		msg := map[string]any{"t": "said", "id": cl.id, "name": cl.name, "text": shown, "at": h.now().UnixMilli()}
		// reactions.go: the line's id, for reactions; everyone on the street now has the line.
		here := make([]int64, 0, len(h.streets[cl.street]))
		for id := range h.streets[cl.street] {
			here = append(here, id)
		}
		if lid := h.chatLineIDLocked("local", cl.street, cl.id, here); lid > 0 {
			msg["lid"] = lid
		}
		h.queueLocked(cl, msg)
		h.broadcastLocked(cl, msg)
		h.logChatLocked("local", cl.street, cl, text, "", nil)
		for _, fn := range h.onChat {
			fn(cl, shown)
		}
	case "typing":
		now := h.now()
		if cl.street == "" || h.cones[cl.id].on || (!cl.typingAt.IsZero() && now.Sub(cl.typingAt) < typingEvery) { // the cone: main.js doLocalChatStart 1679
			return
		}
		cl.typingAt = now
		b, _ := json.Marshal(map[string]any{"t": "typing", "id": cl.id})
		for id, o := range h.streets[cl.street] {
			if id != cl.id && !blockedBetween(h, cl.id, id) {
				h.pushLocked(o, b)
			}
		}
	case "emote":
		e := word(m.E, "")
		if cl.street == "" || e == "" || !h.allowChatLocked(cl) {
			return
		}
		h.broadcastLocked(cl, map[string]any{"t": "emoted", "id": cl.id, "e": e})
	case "leave":
		h.leaveLocked(cl)
	case "ping":
		h.queueLocked(cl, map[string]any{"t": "pong"})
	case "active":
		// A key or a click in the game: all it does is count as an input, which inputKinds did above.
	default:
		if fn := h.handlers[m.T]; fn != nil {
			fn(cl, frame)
			return
		}
		h.replyLocked(cl, "bad_frame", "That message kind is not one the server knows.")
	}
}

func (h *Hub) reply(cl *Client, code, message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.replyLocked(cl, code, message)
}

func (h *Hub) replyLocked(cl *Client, code, message string) {
	h.queueLocked(cl, map[string]any{"t": "error", "code": code, "message": message})
}

func (h *Hub) leaveLocked(cl *Client) {
	if cl.street == "" {
		return
	}
	room := h.streets[cl.street]
	if room[cl.id] == cl {
		delete(room, cl.id)
		h.broadcastLocked(cl, map[string]any{"t": "left", "id": cl.id})
		if len(room) == 0 {
			delete(h.streets, cl.street)
		}
	}
	street := cl.street
	cl.street = ""
	for _, fn := range h.onLeave {
		fn(cl, street)
	}
}

// broadcastLocked sends to everyone on cl's street but cl.
func (h *Hub) broadcastLocked(cl *Client, msg any) {
	room := h.streets[cl.street]
	if len(room) == 0 {
		return
	}
	b, _ := json.Marshal(msg)
	for id, o := range room {
		if id != cl.id {
			h.pushLocked(o, b)
		}
	}
}

func (h *Hub) queueLocked(cl *Client, msg any) {
	b, _ := json.Marshal(msg)
	h.pushLocked(cl, b)
}

// pushLocked drops a client that has stopped reading rather than stall the street.
func (h *Hub) pushLocked(cl *Client, b []byte) {
	if cl.closed {
		return
	}
	select {
	case cl.send <- b:
		cl.recent[cl.nsent%len(cl.recent)] = b
		cl.nsent++
	default:
		// The close line (main.go) says how far behind and what was being sent.
		cl.detail = "queue=" + itoa(len(cl.send)) + " last=" + strings.Join(cl.recentKindsLocked(), ",")
		h.closeLocked(cl, closeSlow)
		if cl.web {
			h.dropWebLocked(cl)
			return
		}
		h.leaveLocked(cl)
		if h.clients[cl.id] == cl {
			delete(h.clients, cl.id)
			for _, fn := range h.onDisconnect {
				fn(cl)
			}
		}
	}
}

// RoomLocked is how many more frames cl's send queue takes before pushLocked drops it as too far behind.
func (h *Hub) RoomLocked(cl *Client) int {
	if cl == nil || cl.closed {
		return 0
	}
	return cap(cl.send) - len(cl.send)
}

func (h *Hub) closeLocked(cl *Client, why int) {
	if cl.closed {
		return
	}
	cl.closed = true
	cl.why = why
	close(cl.done)
}

func (h *Hub) allowMoveLocked(cl *Client) bool {
	now := h.now()
	if now.Sub(cl.moveWindow) >= time.Second {
		cl.moveWindow, cl.moveCount = now, 0
	}
	cl.moveCount++
	return cl.moveCount <= movesPerSec
}

func (h *Hub) allowChatLocked(cl *Client) bool {
	now := h.now()
	cl.chatTokens = math.Min(chatBurst, cl.chatTokens+now.Sub(cl.chatAt).Seconds()/chatEvery.Seconds())
	cl.chatAt = now
	if cl.chatTokens < 1 {
		return false
	}
	cl.chatTokens--
	return true
}

func coord(v *float64) float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return 0
	}
	return math.Round(math.Max(-1e6, math.Min(1e6, *v))*10) / 10
}

func sign(d int) int {
	if d < 0 {
		return -1
	}
	return 1
}

func word(s, fallback string) string {
	if wordRe.MatchString(s) {
		return s
	}
	return fallback
}

func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, s)
}

// cleanChat keeps a chat line to one trimmed line of at most maxChat characters.
func cleanChat(s string) string {
	s = strings.Join(strings.Fields(stripControl(strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " "))), " ")
	if r := []rune(s); len(r) > maxChat {
		s = string(r[:maxChat])
	}
	return s
}

// —— Close lines and the reconnect-loop watch ——
//
// Every socket that ends logs one line (main.go socket calls CloseNote), the
// hub's own closes and the ones it never saw coming alike:
//
//	realtime: close player=7 name="Ada" via=game code=4008 reason="too far behind" after=3.4s queue=64 last=project_given,project_given,project_reward,pong
//	realtime: close player=7 name="Ada" via=game code=1009 reason="read limit: read limited at 8193 bytes" after=41m2s
//	realtime: close player=7 name="Ada" via=game code=1006 reason="write: failed to write msg: …" after=2h3m
//	realtime: close player=7 name="Ada" via=game code=1001 reason="" after=12m40s
//	realtime: close player=7 name="Ada" via=game code=4012 reason="Playing as another character" after=5m2s
//
// code is the WebSocket close code (the hub's 4001/4008/4010/4011/4012, the peer's
// own, 1009 for a frame over the read limit, 1006 for a socket that dropped
// with no close frame). A character that connects more than reconnectLoopMax
// times in a minute is in a loop: one warning a minute names it, with the last
// close, and its close lines stop until it settles:
//
//	realtime: reconnect loop player=7 name="Ada" connects=6 in 1m0s, 0 close lines held back; last close code=4008 reason="too far behind" queue=64 last=…
//
// Refused sockets (no ticket, a bad one, a suspended account) log as
// "realtime: refused …", at most refusedLogMax lines a minute.

const (
	reconnectLoopMax = 5
	refusedLogMax    = 20
)

// connRate counts one character's connects in a minute.
type connRate struct {
	start  time.Time // the minute counted from
	n      int       // connects since start
	held   int       // close lines not logged since the last warning
	warned time.Time // the last loop warning
	last   string    // the last close: code=… reason=… [detail]
}

// noteConnectLocked counts a game socket's connect and warns, once a minute, of a reconnect loop.
func (h *Hub) noteConnectLocked(cl *Client) {
	now := h.now()
	if now.Sub(h.ratesSwept) >= time.Minute {
		h.ratesSwept = now
		for id, r := range h.rates {
			if now.Sub(r.start) >= 2*time.Minute && now.Sub(r.warned) >= 2*time.Minute {
				delete(h.rates, id)
			}
		}
	}
	r := h.rates[cl.id]
	if r == nil {
		r = &connRate{}
		h.rates[cl.id] = r
	}
	if now.Sub(r.start) >= time.Minute {
		r.start, r.n = now, 0
	}
	r.n++
	if r.n > reconnectLoopMax && (r.warned.IsZero() || now.Sub(r.warned) >= time.Minute) {
		log.Printf("realtime: reconnect loop player=%d name=%q connects=%d in %s, %d close lines held back; last close %s", cl.id, cl.name, r.n, now.Sub(r.start).Round(time.Second), r.held, r.last)
		r.warned, r.held = now, 0
	}
}

// looping: is this character in a reconnect loop now (its close lines are held back)?
func (r *connRate) looping(now time.Time) bool {
	return r != nil && ((r.n > reconnectLoopMax && now.Sub(r.start) < time.Minute) || (!r.warned.IsZero() && now.Sub(r.warned) < 2*time.Minute))
}

// CloseNote logs one line for a socket that ended (main.go socket). code and reason are what the
// socket saw (the peer's close, a read or write failure); a close the hub made itself (cl.why)
// takes their place. It also counts the close for the health sample.
func (h *Hub) CloseNote(cl *Client, code int, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closeNoteLocked(cl, code, reason)
}

func (h *Hub) closeNoteLocked(cl *Client, code int, reason string) {
	healthDisconnects.Add(1)
	if cl.why != 0 {
		code, reason = cl.why, closeReasons[cl.why]
	} else if w := cl.wfail.Load(); w != nil {
		code, reason = 1006, *w
	}
	via := "game"
	if cl.web {
		via = "web"
	}
	what := "code=" + itoa(code) + " reason=" + strconv.Quote(reason)
	if cl.detail != "" {
		what += " " + cl.detail
	}
	now := h.now()
	if !cl.web {
		if r := h.rates[cl.id]; r != nil {
			r.last = what
			if r.looping(now) {
				r.held++
				return
			}
		}
	}
	after := time.Duration(0)
	if !cl.at.IsZero() {
		after = now.Sub(cl.at).Round(100 * time.Millisecond)
	}
	log.Printf("realtime: close player=%d name=%q via=%s %s after=%s", cl.id, cl.name, via, what, after)
}

// closeReasons are the hub's close codes as the socket sends them (main.go write).
var closeReasons = map[int]string{
	closeReplaced:       "this character connected somewhere else",
	closeOtherCharacter: "Playing as another character",
	closeSlow:           "too far behind",
	closeBooted:         "an administrator signed you out",
	closeBanned:         "this account is suspended",
}

// recentKindsLocked: the kinds ("t") of the last few frames queued for cl, oldest first.
func (cl *Client) recentKindsLocked() []string {
	out := []string{}
	n := len(cl.recent)
	for i := 0; i < n; i++ {
		k := cl.nsent - n + i
		if k < 0 {
			continue
		}
		var m struct {
			T string `json:"t"`
		}
		if json.Unmarshal(cl.recent[k%n], &m) != nil || m.T == "" {
			m.T = "?"
		}
		out = append(out, m.T)
	}
	return out
}

// refusedLog throttles the refused-socket lines: at most refusedLogMax a minute, then a count.
var refusedLog struct {
	sync.Mutex
	start time.Time
	n     int
	held  int
}

// RefusedNote logs a socket refused before it was a client (main.go auth, a suspended account).
func RefusedNote(code int, reason string, after time.Duration) {
	healthDisconnects.Add(1)
	refusedLog.Lock()
	defer refusedLog.Unlock()
	now := time.Now()
	if now.Sub(refusedLog.start) >= time.Minute {
		if refusedLog.held > 0 {
			log.Printf("realtime: refused %d more sockets in the last minute (lines held back)", refusedLog.held)
		}
		refusedLog.start, refusedLog.n, refusedLog.held = now, 0, 0
	}
	refusedLog.n++
	if refusedLog.n > refusedLogMax {
		refusedLog.held++
		return
	}
	log.Printf("realtime: refused code=%d reason=%s after=%s", code, strconv.Quote(reason), after.Round(100*time.Millisecond))
}
