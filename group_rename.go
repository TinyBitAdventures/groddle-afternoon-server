package main

// A group's new name, as the site tells it (register G1300, report #698: a group's administrator
// renames it as well as editing its description, as Glitch's groups/group.js 494-499 admin_edit set
// label and desc). The site checked the name (its rules, the word filter, no other group has it) and
// stored it; the next groups list carries it. This tells the members online at once:
//
//	site -> server (signed, teleport.go serviceHandlers):
//	  POST ?call=group_rename {"group":12,"name":"Zebra Club","by":"Ada"}   200 {"status":"ok"} | 400 {"status":"bad"}
//
//	server -> the group's members:
//	  {"t":"channel_note","channel":"group:12","text":"Ada renamed the group to Zebra Club.","at":…}
//	        to everyone with its chat open, and kept in the chat's history (id 0, note) for whoever opens it later
//	  {"t":"group_renamed","group":12,"name":"Zebra Club"}
//	        to every game socket whose signed list holds the group, chat open or not: its tabs and lists take the name
//
// Ours: Glitch's admin_edit changed the name with no word to the members; the note is Austin's ruling.
// The note is the server's, like the featured street project's (channels.go notice): nobody can react
// to it or reply to it, and it is not sent to the site's long-term chat history, which keeps players' lines.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
)

func init() { serviceHandlers["group_rename"] = groupRenameService }

type groupRenameCall struct {
	Group int64  `json:"group"`
	Name  string `json:"name"`
	By    string `json:"by"`
}

// groupRenameText is the line the group's chat shows.
func groupRenameText(by, name string) string {
	return by + " renamed the group to " + name + "."
}

func groupRenameService(h *Hub, body []byte) (int, any) {
	var c groupRenameCall
	if json.Unmarshal(body, &c) != nil || c.Group <= 0 {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	name := renameName(c.Name) // a group's name fits a character name's bounds (the site's is stricter)
	if name == "" {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	by := renameName(c.By)
	if by == "" {
		by = unnamedPlayer
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m, _ := h.provided["channels.self"].(*channels)
	if m == nil {
		return http.StatusServiceUnavailable, map[string]string{"status": "bad"}
	}
	m.groupRenamed(c.Group, name, by)
	return http.StatusOK, map[string]string{"status": "ok"}
}

// groupRenamed posts the note in the group's chat and tells every member online the new name.
func (m *channels) groupRenamed(group int64, name, by string) {
	room := "group:" + strconv.FormatInt(group, 10)
	text := groupRenameText(by, name)
	r := m.rooms[room]
	if r == nil {
		r = &chanRoom{members: map[int64]bool{}}
		m.rooms[room] = r
	}
	at := m.h.Now().UnixMilli()
	r.history = append(r.history, chanLine{Text: text, At: at, Note: true})
	if over := len(r.history) - chanHistory; over > 0 {
		r.history = append([]chanLine{}, r.history[over:]...)
	}
	m.broadcast(room, map[string]any{"t": "channel_note", "channel": room, "text": text, "at": at}, 0)
	ids := make([]int64, 0, len(m.watch))
	for id, w := range m.watch {
		if w.groups[group] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		m.send(id, map[string]any{"t": "group_renamed", "group": group, "name": name})
	}
}
