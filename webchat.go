package main

// The game's channels from the website (Austin 2026-11-04: players read AND
// post into the live channels from groddle-afternoon-site's /chat/). Reading is
// the site's (it keeps every channel line, chatstore.go); a line written there
// comes here as the site's signed call and goes out as a real channel line,
// marked as said from the web ("web": true; the game shows "(web)" by the name):
//
//	web_say {"player":7,"name":"Ada","channel":"global","rooms":"global,newbie,trade","silence":"","text":"hi"}
//	        200 {"status":"sent","line":{"lid":…,"id":7,"name":"Ada","text":"hi","at":…,"web":true}}
//	        403 {"status":"banned"} | {"status":"closed"} (not a channel this player may use) | {"status":"silenced","message":"…"}
//	        429 {"status":"slow_down"}   400 {"status":"bad"}
//	web_say {…,"parent":1730000000123,"kept":true}   a reply in that line's thread (threads.go), the same rules;
//	        the parent is a line the server has for the channel, or one the site keeps ("kept"); 404 {"status":"no_line"}
//
// channel: global, help or trade (the game's words; help is the newbie room
// under level 11). Group chats are not offered on the web. rooms and silence
// are what the site's groups watch token carries (channels.go): the channel
// rooms the save allows and the cones of silence; the call is signed, so they
// are the site's word, as a token's are.
//
// The same rules as a line typed in the game: one trimmed line of at most
// maxChat characters (cleanChat), the word filter for what is heard and kept
// (the chat log keeps it as typed), the cone of silence refuses it (logged
// "silenced"), the same rate as the game's channel_say for that player
// (chanSayBurst, chanSayEvery; logged "slow_down"), a suspended account is
// refused (admin.go). Blocks are applied by whoever reads: the game hides a
// blocked player's lines (buddies-feature realtimeHidden) and the site leaves
// them out of what it serves. The line joins the channel's history, everyone
// in the channel hears it, the chat log has it, and the site keeps it.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func init() { serviceHandlers["web_say"] = webSayService }

type webSayCall struct {
	Player  int64  `json:"player"`
	Name    string `json:"name"`
	Channel string `json:"channel"`
	Rooms   string `json:"rooms"`
	Silence string `json:"silence"`
	Text    string `json:"text"`
	Parent  int64  `json:"parent"`
	Kept    bool   `json:"kept"` // the site has the parent in its history
}

func webSayService(h *Hub, body []byte) (int, any) {
	var c webSayCall
	if json.Unmarshal(body, &c) != nil || c.Player <= 0 || c.Parent < 0 || (c.Channel != "global" && c.Channel != "help" && c.Channel != "trade") {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	text := cleanChat(c.Text)
	if text == "" {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	rooms, ok1 := chanWords(c.Rooms, chanRooms)
	silence, ok2 := chanWords(c.Silence, map[string]bool{"chat": true, "help": true})
	if !ok1 || !ok2 || (rooms["help"] && rooms["newbie"]) {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m, _ := h.provided["channels.self"].(*channels)
	if m == nil {
		return http.StatusServiceUnavailable, map[string]string{"status": "bad"}
	}
	if a := h.adminOf(); a != nil && a.bans[c.Player] {
		return http.StatusForbidden, map[string]string{"status": "banned"}
	}
	room, _ := m.roomFor(&chanWatch{rooms: rooms}, c.Channel)
	if room == "" {
		return http.StatusForbidden, map[string]string{"status": "closed"}
	}
	name := strings.TrimSpace(cleanChat(c.Name))
	if cl := h.ClientLocked(c.Player); cl != nil {
		name = cl.Name() // the name the game shows them by now
	}
	if r := []rune(name); len(r) > 32 {
		name = string(r[:32])
	}
	if name == "" {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	if ((room == "global" || room == "trade") && silence["chat"]) || ((room == "help" || room == "newbie") && silence["help"]) {
		h.logChatAsLocked("channel", room, c.Player, name, text, "silenced", nil)
		return http.StatusForbidden, map[string]string{"status": "silenced", "message": chanSuspended}
	}
	if !m.webAllow(c.Player) {
		h.logChatAsLocked("channel", room, c.Player, name, text, "slow_down", nil)
		return http.StatusTooManyRequests, map[string]string{"status": "slow_down"}
	}
	line := chanLine{ID: c.Player, Name: name, Text: filterWords(text), At: h.Now().UnixMilli(), Web: true}
	if c.Parent > 0 {
		reply, ok := h.provided["threads.web"].(func(string, string, int64, bool, chanLine) chanLine)
		if !ok {
			return http.StatusServiceUnavailable, map[string]string{"status": "bad"}
		}
		if line = reply(c.Channel, room, c.Parent, c.Kept, line); line.ID == 0 {
			return http.StatusNotFound, map[string]string{"status": "no_line"} // a reply's reply, or no such line
		}
		h.logChatLineLocked("channel", room, c.Player, name, text, "", nil, line.Lid)
		return http.StatusOK, map[string]any{"status": "sent", "line": map[string]any{"lid": line.Lid, "id": line.ID, "name": line.Name, "text": line.Text, "at": line.At, "web": true, "parent": c.Parent}}
	}
	line.Lid = h.chatLineIDLocked("channel", room, c.Player, nil)
	r := m.rooms[room]
	if r == nil {
		r = &chanRoom{members: map[int64]bool{}}
		m.rooms[room] = r
	}
	r.history = append(r.history, line)
	if over := len(r.history) - chanHistory; over > 0 {
		r.history = append([]chanLine{}, r.history[over:]...)
	}
	said := map[string]any{"t": "channel_said", "channel": c.Channel, "id": line.ID, "name": line.Name, "text": line.Text, "at": line.At, "web": true}
	if line.Lid > 0 {
		said["lid"] = line.Lid
	}
	// The game names Live Help "help" whichever room it is (channels.go roomFor).
	m.broadcast(room, said, 0)
	h.webRoomLocked(room, said, c.Player) // web.go: the other tabs on /chat/
	h.logChatLineLocked("channel", room, c.Player, name, text, "", nil, line.Lid)
	m.store(room, line, true)
	return http.StatusOK, map[string]any{"status": "sent", "line": map[string]any{"lid": line.Lid, "id": line.ID, "name": line.Name, "text": line.Text, "at": line.At, "web": true}}
}

// webAllow is the channel rate for a player, shared with their game's channel_say bucket.
func (m *channels) webAllow(id int64) bool {
	key := "say:" + strconv.FormatInt(id, 10)
	b := m.rates[key]
	if b == nil {
		// A player who is not connected has no bucket of their own yet; old ones go when the map grows.
		if len(m.rates) > 20000 {
			cut := m.h.Now().Add(-time.Minute)
			for k, v := range m.rates {
				if v.at.Before(cut) {
					delete(m.rates, k)
				}
			}
		}
		b = &bucket{}
		m.rates[key] = b
	}
	return b.take(m.h.Now(), chanSayBurst, chanSayEvery)
}
