package main

// Moderation the site's moderators run (Austin 2026-11-09: "Put Grok Bot in
// charge of keeping the game safe"; "If someone spams folks over IM we need to
// be able to both block them and remove the spam from people's chat windows").
// The remake's own: Glitch had the cone of silence (main.js 5808-5843) and
// administrators reading everything; here the site decides (groddle-afternoon-site
// src/Moderation.php: an administrator, or the moderator token Grok Bot holds) and
// this server enforces, through the site's signed calls (teleport.go's
// POST /ws?call=…). No socket message reaches any of these.
//
//	mod_restrict {"player":7,"silence":"chat,help","silence_until":1730000000000,"im":"all","im_until":0}
//	        200 {"status":"ok","online":true}
//	        The player's moderation restrictions as they stand now (the whole set: an empty word lifts
//	        it). silence: "chat" (Global Chat, the Trade Channel and Local chat, as the chat cone),
//	        "help" (Live Help), or both; im: "all" (no IMs to anyone) or "buddies" (IMs only to players
//	        on their buddy list, either way). *_until: Unix ms when it ends by itself, 0 never.
//	mod_purge {"player":7,"since":1730000000000,"by":1}
//	        200 {"status":"purged","channel":n,"replies":n,"ims":n,"reactable":n}
//	        Everything the player said since then (0: all this server holds) goes: their lines of every
//	        channel and group history and thread, their side of every IM conversation kept here, and
//	        their Local and party lines (none are kept here, but they stop taking reactions). Everyone
//	        connected, in the game and on the website, is told at once:
//	          {"t":"chat_purged","player":7,"since":1730000000000}
//	        and takes the player's lines said since then out of every pane: chat, IM, threads,
//	        reactions. Nobody gets the words: the call names a player, never a line. The chat store
//	        tells the site again ({"k":"purge"}, after any line still waiting), so a line said a
//	        moment ago is marked there too. The chat log keeps what was said, as for a deletion, and
//	        records the purge (deleted.jsonl, "purge": true): the evidence stays for 30 days. A
//	        purge is final: there is no call to bring lines back.
//	im_delete {"a":7,"b":9,"line":12,"at":1730000000000,"by":1}
//	        200 {"status":"deleted","held":true}   one IM line (a reported one: the site sends only
//	        lines a recipient reported) out of the conversation kept here; both players are told
//	          {"t":"im_deleted","with":<the other>,"line":12,"at":1730000000000}
//	        (a line is its id and time: ids start again when both close a conversation)
//	        404 {"status":"unknown"} when the conversation is not kept here (the site's copy is marked anyway)
//
// A ticket (RealtimeTicket.php) carries the restrictions that stand when it is made
// ("mod":{"s":"chat","su":ms,"im":"all","iu":ms}), so they hold from the first frame after a
// reconnect or a restart; the newest word wins (a ticket made before a live mod_restrict does not
// undo it). Enforced: the silence in channel_say, thread replies, Local chat and the website's
// web_say, as the cone of silence is (the game hears {"t":"cone","on":true} for Local chat); the IM
// restriction in im_send and web_im ("restricted"). Ended restrictions lapse here by themselves.

import (
	"encoding/json"
	"log"
	"net/http"
	"testing"
)

func init() {
	modules = append(modules, newModeration)
	serviceHandlers["mod_restrict"] = modRestrictService
	serviceHandlers["mod_purge"] = modPurgeService
	serviceHandlers["im_delete"] = imDeleteService
}

// modClaim is a ticket's word on a player's restrictions (RealtimeTicket.php "mod").
type modClaim struct {
	S  string `json:"s,omitempty"`
	SU int64  `json:"su,omitempty"`
	IM string `json:"im,omitempty"`
	IU int64  `json:"iu,omitempty"`
}

// modWord is one player's restrictions here, as of at (Unix ms of the word).
type modWord struct {
	silence      map[string]bool
	silenceUntil int64
	im           string
	imUntil      int64
	at           int64
}

type moderation struct {
	h       *Hub
	words   map[int64]modWord
	signals map[int64]*modSignals // modsignals.go
}

func newModeration(h *Hub) Module {
	return &moderation{h: h, words: map[int64]modWord{}}
}

func (m *moderation) Register(r *Registry) {
	r.Provide("moderation.self", m)
	r.OnDisconnect(func(cl *Client) { m.forgetSignals(cl.ID()) })
}

func (h *Hub) moderationOf() *moderation {
	m, _ := h.provided["moderation.self"].(*moderation)
	return m
}

var modSilenceWords = map[string]bool{"chat": true, "help": true}

// set takes a word unless a newer one stands; true when it took it.
func (m *moderation) set(id int64, w modWord) bool {
	if was, ok := m.words[id]; ok && w.at < was.at {
		return false
	}
	if len(w.silence) == 0 && w.im == "" {
		// Kept as an empty word with its time, so an older ticket can't lay it again.
		w.silence = nil
	}
	m.words[id] = w
	return true
}

// applyModClaimLocked takes a ticket's restrictions for a character (iat: when the site made it, Unix s).
func (h *Hub) applyModClaimLocked(id int64, c *modClaim, iat int64) {
	m := h.moderationOf()
	if m == nil {
		return
	}
	w := modWord{at: iat * 1000}
	if c != nil {
		w.silence, _ = chanWords(c.S, modSilenceWords)
		w.silenceUntil = c.SU
		if c.IM == "all" || c.IM == "buddies" {
			w.im, w.imUntil = c.IM, c.IU
		}
	}
	m.set(id, w)
}

// modSilencedLocked is whether a moderator's silence holds for id in word's chats ("chat" or "help").
func (h *Hub) modSilencedLocked(id int64, word string) bool {
	m := h.moderationOf()
	if m == nil {
		return false
	}
	w, ok := m.words[id]
	if !ok || !w.silence[word] {
		return false
	}
	return w.silenceUntil == 0 || h.now().UnixMilli() < w.silenceUntil
}

// modSilencedRoomLocked is modSilencedLocked for a channel room (group chats have no silence).
func (h *Hub) modSilencedRoomLocked(id int64, room string) bool {
	switch room {
	case "global", "trade":
		return h.modSilencedLocked(id, "chat")
	case "help", "newbie":
		return h.modSilencedLocked(id, "help")
	}
	return false
}

// modIMRestrictedLocked is whether a moderator's IM restriction stops from writing to to.
func (h *Hub) modIMRestrictedLocked(from, to int64) bool {
	m := h.moderationOf()
	if m == nil {
		return false
	}
	w, ok := m.words[from]
	if !ok || w.im == "" || (w.imUntil != 0 && h.now().UnixMilli() >= w.imUntil) {
		return false
	}
	if w.im == "buddies" {
		fn, ok := h.ProvidedLocked("buddies.holds").(func(int64, int64) bool)
		return !((ok && fn(from, to)) || h.webHolds(from, to))
	}
	return true
}

// modConeLocked is whether Local chat is closed to id: the cone or a moderator's chat silence.
func (h *Hub) modConeLocked(id int64) bool {
	return h.cones[id].on || h.modSilencedLocked(id, "chat")
}

// —— The site's calls ——

type modRestrictCall struct {
	Player       int64  `json:"player"`
	Silence      string `json:"silence"`
	SilenceUntil int64  `json:"silence_until"`
	IM           string `json:"im"`
	IMUntil      int64  `json:"im_until"`
}

func modRestrictService(h *Hub, body []byte) (int, any) {
	var c modRestrictCall
	if json.Unmarshal(body, &c) != nil || c.Player <= 0 || c.SilenceUntil < 0 || c.IMUntil < 0 {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	silence, ok := chanWords(c.Silence, modSilenceWords)
	if !ok || (c.IM != "" && c.IM != "all" && c.IM != "buddies") {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.moderationOf()
	if m == nil {
		return http.StatusServiceUnavailable, map[string]string{"status": "bad"}
	}
	before := h.modConeLocked(c.Player)
	w := modWord{silence: silence, silenceUntil: c.SilenceUntil, im: c.IM, imUntil: c.IMUntil, at: h.now().UnixMilli()}
	if c.IM == "" {
		w.imUntil = 0
	}
	m.set(c.Player, w)
	cl := h.clients[c.Player]
	if cl != nil && h.modConeLocked(c.Player) != before {
		h.queueLocked(cl, map[string]any{"t": "cone", "on": !before})
	}
	if !testing.Testing() {
		log.Printf("moderation: restrictions for %d now silence=%q im=%q", c.Player, c.Silence, c.IM)
	}
	return http.StatusOK, map[string]any{"status": "ok", "online": cl != nil}
}

type modPurgeCall struct {
	Player int64 `json:"player"`
	Since  int64 `json:"since"`
	By     int64 `json:"by"`
}

func modPurgeService(h *Hub, body []byte) (int, any) {
	var c modPurgeCall
	if json.Unmarshal(body, &c) != nil || c.Player <= 0 || c.Since < 0 || c.By <= 0 {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.purgeLocked(c.Player, c.Since)
	h.logDeletionLocked(chatDeletion{Where: "*", Purge: true, ID: c.Player, LineAt: c.Since, By: c.By})
	h.storeChatLocked(chatEntry{K: "purge", ID: c.Player, At: c.Since, By: c.By})
	msg := map[string]any{"t": "chat_purged", "player": c.Player, "since": c.Since}
	b, _ := json.Marshal(msg)
	for _, cl := range h.clients {
		h.pushLocked(cl, b)
	}
	for _, tabs := range h.webs {
		for cl := range tabs {
			h.pushLocked(cl, b)
		}
	}
	if !testing.Testing() {
		log.Printf("moderation: purged %d since %d by %d (%v)", c.Player, c.Since, c.By, n) // counts, never words
	}
	return http.StatusOK, map[string]any{"status": "purged", "channel": n["channel"], "replies": n["replies"], "ims": n["ims"], "reactable": n["reactable"]}
}

// purgeLocked takes a player's lines said at or after since out of everything this server sends again.
func (h *Hub) purgeLocked(player, since int64) map[string]int {
	n := map[string]int{}
	gone := map[int64]bool{} // lids that went, for the reactable lines
	if ch, _ := h.provided["channels.self"].(*channels); ch != nil {
		for _, r := range ch.rooms {
			kept := r.history[:0:0]
			for _, l := range r.history {
				if l.ID == player && l.At >= since {
					gone[l.Lid] = true
					n["channel"]++
					continue
				}
				kept = append(kept, l)
			}
			r.history = kept
		}
	}
	if th, _ := h.provided["threads.self"].(*threads); th != nil {
		for parent, t := range th.threads {
			if gone[parent] {
				// The line with the thread went: its replies go out of sight with it (as chat_delete).
				for _, r := range t.replies {
					delete(th.replyOf, r.Lid)
				}
				delete(th.threads, parent)
				continue
			}
			kept := t.replies[:0:0]
			for _, r := range t.replies {
				if r.ID == player && r.At >= since {
					delete(th.replyOf, r.Lid)
					gone[r.Lid] = true
					if t.n > 0 {
						t.n--
					}
					n["replies"]++
					continue
				}
				kept = append(kept, r)
			}
			t.replies = kept
		}
	}
	if rx, _ := h.provided["reactions.self"].(*reacts); rx != nil {
		for lid, l := range rx.lines {
			if gone[lid] || (l.author == player && l.at.UnixMilli() >= since) {
				delete(rx.lines, lid)
				n["reactable"]++
			}
		}
	}
	if im, _ := h.provided["ims.self"].(*ims); im != nil {
		changed := false
		for k := range im.by[player] {
			c := im.convs[k]
			if c == nil {
				continue
			}
			kept := c.Lines[:0:0]
			for _, l := range c.Lines {
				if l.From == player && l.At >= since {
					n["ims"]++
					changed = true
					continue
				}
				kept = append(kept, l)
			}
			c.Lines = kept
		}
		if changed {
			im.prune() // a conversation left with no lines goes
			im.saveSoon()
		}
	}
	return n
}

type imDeleteCall struct {
	A    int64 `json:"a"`
	B    int64 `json:"b"`
	Line int64 `json:"line"`
	At   int64 `json:"at"`
	By   int64 `json:"by"`
}

func imDeleteService(h *Hub, body []byte) (int, any) {
	var c imDeleteCall
	if json.Unmarshal(body, &c) != nil || c.A <= 0 || c.B <= c.A || c.Line <= 0 || c.At <= 0 || c.By <= 0 {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	im, _ := h.provided["ims.self"].(*ims)
	if im == nil {
		return http.StatusServiceUnavailable, map[string]string{"status": "bad"}
	}
	conv := im.convs[[2]int64{c.A, c.B}]
	held := false
	if conv != nil {
		kept := conv.Lines[:0:0]
		for _, l := range conv.Lines {
			if (l.ID == c.Line && l.At == c.At) || (l.Parent == c.Line && l.At >= c.At) {
				held = true
				continue
			}
			kept = append(kept, l)
		}
		conv.Lines = kept
		if held {
			im.prune()
			im.saveSoon()
		}
	}
	for _, side := range [][2]int64{{c.A, c.B}, {c.B, c.A}} {
		msg := map[string]any{"t": "im_deleted", "with": side[1], "line": c.Line, "at": c.At}
		if cl := h.clients[side[0]]; cl != nil {
			h.queueLocked(cl, msg)
		}
		for _, w := range h.WebLocked(side[0]) {
			h.queueLocked(w, msg)
		}
	}
	if !testing.Testing() {
		log.Printf("moderation: IM line %d between %d and %d deleted by %d", c.Line, c.A, c.B, c.By) // never the words
	}
	if conv == nil {
		return http.StatusNotFound, map[string]string{"status": "unknown"}
	}
	return http.StatusOK, map[string]any{"status": "deleted", "held": held}
}
