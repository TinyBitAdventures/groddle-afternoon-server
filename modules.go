package main

import (
	"encoding/json"
	"time"
)

// A module is one shared system (quoins, street objects, parties, …) in a file of
// its own. It registers in an init func of its file:
//
//	func init() { modules = append(modules, newQuoins) }
//
// and NewHub gives each one the hub. Every handler and hook runs with the hub lock
// held, so a module keeps its state in plain fields and calls the *Locked helpers;
// it never takes h.mu itself. Timers go through h.After, which takes the lock.
type moduleFactory func(h *Hub) Module

var modules []moduleFactory

// Module is what a module registers with the hub.
type Module interface {
	Register(r *Registry)
}

// Registry collects one module's message kinds and lifecycle hooks.
type Registry struct{ h *Hub }

// Handle answers one message kind ("t") the core does not handle. frame is the whole message.
func (r *Registry) Handle(kind string, fn func(cl *Client, frame json.RawMessage)) {
	if _, core := coreKinds[kind]; core {
		panic("realtime: " + kind + " is a core message kind")
	}
	if _, taken := r.h.handlers[kind]; taken {
		panic("realtime: two modules handle " + kind)
	}
	r.h.handlers[kind] = fn
}

// OnConnect runs when a character connects (also when a new socket replaces an old one).
func (r *Registry) OnConnect(fn func(cl *Client)) { r.h.onConnect = append(r.h.onConnect, fn) }

// OnJoin runs after a character joins a street and has the street's player list.
func (r *Registry) OnJoin(fn func(cl *Client)) { r.h.onJoin = append(r.h.onJoin, fn) }

// OnLeave runs after a character leaves a street (a join elsewhere, a leave, a disconnect).
func (r *Registry) OnLeave(fn func(cl *Client, street string)) {
	r.h.onLeave = append(r.h.onLeave, fn)
}

// OnDisconnect runs when a character goes offline (not when a new socket replaces the old).
func (r *Registry) OnDisconnect(fn func(cl *Client)) {
	r.h.onDisconnect = append(r.h.onDisconnect, fn)
}

// OnChat runs after a character's street chat line is said (main.js 1766-1793 checks Charades after
// pc_local_chat), under the hub lock, with the cleaned text.
func (r *Registry) OnChat(fn func(cl *Client, text string)) { r.h.onChat = append(r.h.onChat, fn) }

// OnShutdown runs once when the server stops (SIGTERM or an interrupt), after it stops taking requests.
func (r *Registry) OnShutdown(fn func()) { r.h.onShutdown = append(r.h.onShutdown, fn) }

// GateJoin may refuse a join before it happens (a private street such as a race's instance): the
// character stays where it was and is told code and message. Every gate must agree.
func (r *Registry) GateJoin(fn func(cl *Client, street string) (ok bool, code, message string)) {
	r.h.joinGates = append(r.h.joinGates, fn)
}

// Provide offers fn to the other modules under name ("<module>.<what>"): a hook between two shared
// systems, such as the Rook stripping a shared tree's fruit. Callers look it up when they need it
// (ProvidedLocked), so the order modules register in does not matter.
func (r *Registry) Provide(name string, fn any) {
	if _, taken := r.h.provided[name]; taken {
		panic("realtime: two modules provide " + name)
	}
	r.h.provided[name] = fn
}

// ProvidedLocked is what a module offered under name, or nil when no module did (a build without it).
// The caller asserts the type it expects: fn, ok := h.ProvidedLocked("objects.rookTree").(func(string, string) bool).
func (h *Hub) ProvidedLocked(name string) any { return h.provided[name] }

var coreKinds = map[string]struct{}{"auth": {}, "join": {}, "move": {}, "chat": {}, "emote": {}, "leave": {}, "ping": {}}

// Helpers for modules. All need the hub lock, which every handler and hook already holds.

// ID, Name, Street and Pos read a client.
func (cl *Client) ID() int64           { return cl.id }
func (cl *Client) Name() string        { return cl.name }
func (cl *Client) Street() string      { return cl.street }
func (cl *Client) Pos() (x, y float64) { return cl.player.X, cl.player.Y }
func (cl *Client) Online() bool        { return !cl.closed }

// SendLocked queues a message for one client.
func (h *Hub) SendLocked(cl *Client, msg any) { h.queueLocked(cl, msg) }

// ErrorLocked sends one client an error message.
func (h *Hub) ErrorLocked(cl *Client, code, message string) { h.replyLocked(cl, code, message) }

// StreetLocked lists the clients on a street.
func (h *Hub) StreetLocked(street string) []*Client {
	room := h.streets[street]
	out := make([]*Client, 0, len(room))
	for _, cl := range room {
		out = append(out, cl)
	}
	return out
}

// BroadcastStreetLocked sends to everyone on a street but the character with id except (0: everyone).
func (h *Hub) BroadcastStreetLocked(street string, msg any, except int64) {
	room := h.streets[street]
	if len(room) == 0 {
		return
	}
	b, _ := json.Marshal(msg)
	for id, o := range room {
		if id != except {
			h.pushLocked(o, b)
		}
	}
}

// ClientLocked finds an online character by id.
func (h *Hub) ClientLocked(id int64) *Client { return h.clients[id] }

// Now is the hub's clock.
func (h *Hub) Now() time.Time { return h.now() }

// After runs fn with the hub lock held once d has passed. Tests replace h.afterFunc.
func (h *Hub) After(d time.Duration, fn func()) (stop func() bool) {
	return h.afterFunc(d, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		fn()
	})
}

func realAfter(d time.Duration, fn func()) func() bool { return time.AfterFunc(d, fn).Stop }
