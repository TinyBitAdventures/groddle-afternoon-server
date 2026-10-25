package main

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"sync"
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
//	{"t":"emote","e":"wave"}
//	{"t":"leave"}                                      off the street, still connected
//	{"t":"ping"}
//
// Server to client:
//
//	{"t":"hello","you":{"id":7,"name":"Ada"}}
//	{"t":"street","street":"GA5…","players":[Player…]}  everyone else there, after a join
//	{"t":"joined","player":Player}
//	{"t":"left","id":7}
//	{"t":"moved","id":7,"x":0,"y":0,"dir":1,"anim":"walk"}
//	{"t":"said","id":7,"name":"Ada","text":"hi","at":1730000000000}
//	{"t":"emoted","id":7,"e":"wave"}
//	{"t":"error","code":"…","message":"…"}
//	{"t":"pong"}
//
// One live session per character: a second socket for the same player closes the
// first with closeReplaced.
//
// Shared systems (quoins, street objects, parties, …) are modules (modules.go),
// each in a file of its own with its message kinds listed in its header.

const (
	maxStreet   = 32
	maxLook     = 4096
	maxChat     = 200
	maxAnim     = 24
	sendBuffer  = 64
	movesPerSec = 20
	chatBurst   = 5
	chatEvery   = time.Second
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
}

// Hub holds every connected character and the street each stands on.
type Hub struct {
	now func() time.Time

	mu      sync.Mutex
	clients map[int64]*Client
	streets map[string]map[int64]*Client

	// Modules (modules.go): extra message kinds and lifecycle hooks, all run under mu.
	handlers     map[string]func(cl *Client, frame json.RawMessage)
	onConnect    []func(cl *Client)
	onJoin       []func(cl *Client)
	onLeave      []func(cl *Client, street string)
	onDisconnect []func(cl *Client)
	afterFunc    func(d time.Duration, fn func()) func() bool
}

func NewHub(now func() time.Time) *Hub {
	if now == nil {
		now = time.Now
	}
	h := &Hub{now: now, clients: map[int64]*Client{}, streets: map[string]map[int64]*Client{}, handlers: map[string]func(*Client, json.RawMessage){}, afterFunc: realAfter}
	for _, m := range modules {
		m(h).Register(&Registry{h: h})
	}
	return h
}

// Stats counts characters online and streets with someone on them.
func (h *Hub) Stats() (players, streets int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients), len(h.streets)
}

// Connect adds a character, closing any socket it already had.
func (h *Hub) Connect(c Claims) *Client {
	cl := &Client{id: c.Sub, name: c.Name, send: make(chan []byte, sendBuffer), done: make(chan struct{}), chatTokens: chatBurst, chatAt: h.now()}
	h.mu.Lock()
	defer h.mu.Unlock()
	if old := h.clients[c.Sub]; old != nil {
		h.leaveLocked(old)
		h.closeLocked(old, closeReplaced)
	}
	h.clients[c.Sub] = cl
	h.queueLocked(cl, map[string]any{"t": "hello", "you": map[string]any{"id": cl.id, "name": cl.name}})
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
	switch m.T {
	case "join":
		if !streetRe.MatchString(m.Street) {
			h.replyLocked(cl, "bad_street", "That street name is not one the game uses.")
			return
		}
		if len(m.Look) > maxLook || (len(m.Look) > 0 && !json.Valid(m.Look)) {
			m.Look = nil
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
		if !h.allowChatLocked(cl) {
			h.replyLocked(cl, "slow_down", "Slow down a little.")
			return
		}
		msg := map[string]any{"t": "said", "id": cl.id, "name": cl.name, "text": text, "at": h.now().UnixMilli()}
		h.queueLocked(cl, msg)
		h.broadcastLocked(cl, msg)
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
	default:
		h.closeLocked(cl, closeSlow)
		h.leaveLocked(cl)
		if h.clients[cl.id] == cl {
			delete(h.clients, cl.id)
			for _, fn := range h.onDisconnect {
				fn(cl)
			}
		}
	}
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
