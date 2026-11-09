package main

// Deleting a channel line for everyone (Austin 2026-11-07: "as an admin I
// should be able to delete mistakes like this", a player's stray line in Live
// Help). The remake's own: Glitch's channels had no deletion. Only the site
// decides who may (groddle-afternoon-site src/ChatHistory.php, the
// administrators' manage_options, as the console): it marks its kept row
// deleted and asks here with its signed call (teleport.go's POST /ws?call=…),
// so no socket message deletes anything and a player's game can't ask.
//
//	chat_delete {"lid":1730000000123,"rooms":"help,newbie","by":1,"by_name":"Austin",
//	             "room":"newbie","parent":0,"author":7,"at":1730000000000,"text":"d/leave"}
//	        200 {"status":"deleted","room":"newbie","parent":0,"held":true,"author":7,"text":"d/leave"}
//	            author and text: the line as known here, else as the site said (for the console's log)
//	        404 {"status":"unknown"}   neither the site (no "room") nor this server has that line
//	        400 {"status":"bad"}
//
// rooms: the channel rooms the line may be in (the website's key: Live Help is
// help or newbie); global, help, newbie and trade, or one group's chat alone
// ("group:12", since the moderators' delete line, moderation.go: Austin
// 2026-11-09). The group's members hear chat_deleted with "channel":"group:12". room, parent, author, at and text: the site's kept row, when it has
// one (a line said a moment ago may not have reached it yet: this server's
// chat store sends lines every few seconds, so a line it still holds is found
// here). The line is found in the room's history, in a thread this server holds
// (a reply, or a line with replies), or among the lines that take reactions.
//
// What happens, for a line of the channel or a thread reply alike:
//   - it leaves the room's history (channel_joined never sends it again), its
//     thread if it is a reply (the count goes down), and the reactable lines;
//     a deleted line with replies takes its thread with it (the site serves
//     neither: ChatHistory leaves out a deleted parent and so its replies);
//   - everyone with the channel open in the game and every website reader of
//     it hears, at once:
//     {"t":"chat_deleted","channel":"help","lid":…}                                    a line of the channel
//     {"t":"chat_deleted","channel":"help","lid":…,"parent":…,"n":2,"last":{"id":9,"name":"Bo"}}   a reply;
//     n and last are the thread's as this server holds it (left out when it holds
//     none: the reader counts one fewer)
//   - the chat log keeps the line as it was written and records the deletion
//     (chatlog.go deleted.jsonl: who, when), so moderation keeps the evidence;
//   - the site is told again through the chat store ({"k":"delete"}, after the
//     line itself if that was still waiting), so its row is marked whichever
//     came first.
//
// Asking again for a line already deleted answers "deleted" (held false) and
// tells the readers again; nothing breaks.

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"testing"
)

func init() { serviceHandlers["chat_delete"] = chatDeleteService }

type chatDeleteCall struct {
	Lid    int64  `json:"lid"`
	Rooms  string `json:"rooms"`
	By     int64  `json:"by"`
	ByName string `json:"by_name"`
	Room   string `json:"room"`
	Parent int64  `json:"parent"`
	Author int64  `json:"author"`
	At     int64  `json:"at"`
	Text   string `json:"text"`
}

// chatDeleteRooms are the rooms a deletion may reach: the game-wide channels.
var chatDeleteRooms = map[string]bool{"global": true, "help": true, "newbie": true, "trade": true}

func chatDeleteService(h *Hub, body []byte) (int, any) {
	var c chatDeleteCall
	if json.Unmarshal(body, &c) != nil || c.Lid <= 0 || c.By <= 0 || c.Parent < 0 || c.Author < 0 || c.At < 0 {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	rooms, ok := chanWords(c.Rooms, chatDeleteRooms)
	if _, group := groupChannel(c.Rooms); group {
		// A group's chat (moderation.go, Austin 2026-11-09): one room, named alone.
		rooms, ok = map[string]bool{c.Rooms: true}, true
	}
	if !ok || len(rooms) == 0 || (c.Room != "" && !rooms[c.Room]) {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	byName := strings.TrimSpace(cleanChat(c.ByName))
	if r := []rune(byName); len(r) > 64 {
		byName = string(r[:64])
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, _ := h.provided["channels.self"].(*channels)
	if ch == nil {
		return http.StatusServiceUnavailable, map[string]string{"status": "bad"}
	}
	th, _ := h.provided["threads.self"].(*threads)
	rx, _ := h.provided["reactions.self"].(*reacts)

	// Where this server has it, and the line as it was shown.
	room, parent, held := "", int64(0), false
	var line *chanLine
	for r := range rooms {
		if cr := ch.rooms[r]; cr != nil {
			for i := range cr.history {
				if cr.history[i].Lid == c.Lid {
					room, held, line = r, true, &cr.history[i]
				}
			}
		}
	}
	if th != nil && !held {
		if p := th.replyOf[c.Lid]; p != 0 {
			if t := th.threads[p]; t != nil && rooms[t.room] {
				room, parent, held = t.room, p, true
				for i := range t.replies {
					if t.replies[i].Lid == c.Lid {
						line = &t.replies[i]
					}
				}
			}
		} else if t := th.threads[c.Lid]; t != nil && rooms[t.room] {
			room, held = t.room, true
		}
	}
	if rx != nil && !held {
		if l := rx.lines[c.Lid]; l != nil && (l.kind == "channel" || l.kind == "thread") && rooms[l.where] {
			room, held = l.where, true
			if l.kind == "thread" {
				parent = c.Parent // the server keeps no parent with a reactable reply; the site's word
			}
		}
	}
	if !held {
		if c.Room == "" {
			return http.StatusNotFound, map[string]string{"status": "unknown"}
		}
		room, parent = c.Room, c.Parent
	}

	// What the log records of the line: this server's copy, else the site's.
	d := chatDeletion{Where: room, Lid: c.Lid, ID: c.Author, LineAt: c.At, Text: c.Text, By: c.By, ByName: byName}
	if line != nil {
		d.ID, d.LineAt, d.Text = line.ID, line.At, line.Text
	}

	// Gone from what the server sends: the history, the thread, the reactable lines.
	if cr := ch.rooms[room]; cr != nil {
		kept := cr.history[:0:0]
		for _, l := range cr.history {
			if l.Lid != c.Lid {
				kept = append(kept, l)
			}
		}
		cr.history = kept
	}
	var sum *threadSum
	tellSum := false
	if th != nil {
		if t := th.threads[c.Lid]; t != nil && parent == 0 {
			for _, r := range t.replies {
				delete(th.replyOf, r.Lid)
			}
			delete(th.threads, c.Lid)
		}
		if parent > 0 {
			if t := th.threads[parent]; t != nil {
				kept := t.replies[:0:0]
				for _, r := range t.replies {
					if r.Lid != c.Lid {
						kept = append(kept, r)
					} else if t.n > 0 {
						t.n--
					}
				}
				t.replies = kept
				tellSum = true
				if t.n > 0 && len(t.replies) > 0 {
					last := t.replies[len(t.replies)-1]
					sum = &threadSum{N: t.n, Last: reactBy{ID: last.ID, Name: last.Name}}
				}
			}
			delete(th.replyOf, c.Lid)
		}
	}
	if rx != nil {
		delete(rx.lines, c.Lid)
	}

	// Everyone reading the channel, in the game and on the website.
	name := room
	if room == "newbie" {
		name = "help" // the game names Live Help "help" whichever room it is (channels.go roomFor)
	}
	msg := map[string]any{"t": "chat_deleted", "channel": name, "lid": c.Lid}
	if parent > 0 {
		msg["parent"] = parent
		if tellSum {
			if sum != nil {
				msg["n"], msg["last"] = sum.N, sum.Last
			} else {
				msg["n"] = 0
			}
		}
	}
	ch.broadcast(room, msg, 0)
	h.webRoomLocked(room, msg, 0)

	h.logDeletionLocked(d)
	h.storeChatLocked(chatEntry{K: "delete", Lid: c.Lid, By: c.By, At: h.Now().UnixMilli()})
	if !testing.Testing() {
		log.Printf("chat: line %d in %s deleted by %d", c.Lid, room, c.By) // never the words
	}
	return http.StatusOK, map[string]any{"status": "deleted", "room": room, "parent": parent, "held": held, "author": d.ID, "text": d.Text}
}
