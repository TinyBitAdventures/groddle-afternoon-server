package main

// A character's new name, as the site tells it (register G1213, report #636: a player changes their
// name for 1,000 currants once in 30 days, groddle-afternoon-site src/CharacterName.php; Glitch's
// Account page had "Change name"). The site checked everything (the name rules, nobody else has it,
// the cost, the wait) and stored it; every ticket from now on carries it (Claims.Name). This makes it
// true at once for the sockets already open, so nobody has to reconnect:
//
//	site -> server (signed, teleport.go serviceHandlers):
//	  POST ?call=rename {"player":7,"name":"Ada Two"}   200 {"status":"ok"} | {"status":"offline"} | 400 {"status":"bad"}
//
//	server -> every game and web socket:
//	  {"t":"renamed","id":7,"name":"Ada Two","was":"Ada"}
//
// Everyone hears it (renames are rare, and a buddy list, an IM, a party or a chat roster can name
// anyone, on any street): the game redraws the name over their head and in its lists, the player's own
// game takes it as theirs. Kept here as well: the socket's own name (every line, roster and buddy
// message after this reads it), the party's (invites by name, its member list), the IM conversations'
// (the names saved with them) and the Notice Board notes they pinned (each carries its author's name,
// as the site gives it again on a restart). What was said before keeps the name it was said under
// (chat lines, the chat log), as the site keeps it for its copies.

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// renameMax is the longest name the site keeps (groddle_players.name varchar(32)).
const renameMax = 32

func init() { serviceHandlers["rename"] = renameService }

type renameCall struct {
	Player int64  `json:"player"`
	Name   string `json:"name"`
}

// renameName is the name as the site sent it, or "" when it is not one a character could have.
func renameName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > renameMax {
		return ""
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return s
}

func renameService(h *Hub, body []byte) (int, any) {
	var c renameCall
	if json.Unmarshal(body, &c) != nil || c.Player <= 0 {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	name := renameName(c.Name)
	if name == "" {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return http.StatusOK, map[string]string{"status": h.renameLocked(c.Player, name)}
}

// renameLocked gives a character its new name everywhere the server holds one: "ok", or "offline"
// when none of their sockets is open (the next ticket carries it; their notes and IMs change all the same).
func (h *Hub) renameLocked(id int64, name string) string {
	was := ""
	cl := h.clients[id]
	if cl != nil {
		was = cl.name
		cl.name = name
		cl.player.Name = name
	}
	webs := h.WebLocked(id)
	for _, w := range webs {
		if was == "" {
			was = w.name
		}
		w.name = name
	}
	for _, hook := range []string{"party.renamed", "ims.renamed", "boards.renamed"} {
		if fn, ok := h.ProvidedLocked(hook).(func(int64, string)); ok {
			fn(id, name)
		}
	}
	if cl == nil && len(webs) == 0 {
		return "offline"
	}
	b, _ := json.Marshal(map[string]any{"t": "renamed", "id": id, "name": name, "was": was})
	for _, o := range h.clients {
		h.pushLocked(o, b)
	}
	for _, set := range h.webs {
		for o := range set {
			h.pushLocked(o, b)
		}
	}
	if fn, ok := h.ProvidedLocked("presence.changed").(func()); ok {
		fn() // the who's-around panel and the counts carry names
	}
	return "ok"
}
