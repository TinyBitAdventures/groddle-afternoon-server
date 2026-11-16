package main

// Player reports (register G872, report #417, Austin 2026-11-16: "public chat only, never IMs"):
// a player reports another from the player menu (the site's POST groddle/v1/players/{id}/report,
// groddle-afternoon-site src/PlayerReports.php), and the site asks this server for the public
// chat lines the reporter could see, to go with the report. Glitch's Report Abuse sent the
// client's own chat panes that named the player ("ALL CHATS THAT HAVE <name> in them",
// glitch-client TSFrontController.as reportAbuse 2602-2630, from the chat menu's Report Abuse,
// ChatDropdown.as 77, and the player menu's Block / Report, InteractionMenuController.as 869-875);
// here the lines come from the chat log (chatlog.go), which never holds an IM, so no IM's words
// can reach a report (Austin 2026-11-09, register G607: only an IM's two players read it).
//
// What the reporter could see, in the last reportWindow:
//   - local: street chat said on a street while the reporter stood on it (this module notes each
//     player's streets with their times: OnJoin, OnLeave);
//   - channel: Global Chat, Live Help (help and newbie) and the Trade Channel, in a channel the
//     reporter is in now (a channel sends its recent lines to whoever opens it, channels.go
//     history, so the reporter saw those too). Group chats are left out: a group's chat is its
//     members' own;
//   - party: party lines that went to the reporter (the log's "to").
//
// Only delivered lines (a refused line reached nobody). Of those, the reported player's lines
// and the lines that name them (Glitch's test), each with up to reportContext lines either side
// in the same chat, at most reportMaxLines, the newest kept; when the reported player said
// nothing the reporter saw, the reporter's newest reportQuietLines lines of context instead.
//
// The site's signed call (RealtimeCall, teleport.go's signing), POST /ws?call=report_lines:
//
//	report_lines {"reporter":7,"reported":9,"name":"Bo"}       name: the reported player's name (mentions)
//	        200 {"status":"ok","lines":[line…],"window":1800}  oldest first
//	        200 {"status":"off","lines":[],"window":1800}      no chat log is kept
//	        line: {"at","kind":"local"|"channel"|"party","where","id","name","text","shown"?,"filtered"?,
//	               "deleted"?,"reported"?}   text as typed and shown as the others saw it (wordfilter.go),
//	               deleted: an administrator deleted it since, reported: said by the reported player

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	reportWindow     = 30 * time.Minute
	reportMaxLines   = 50
	reportContext    = 3  // lines kept before and after each anchor, in the same chat
	reportQuietLines = 20 // the reported player said nothing the reporter saw: the newest lines instead
	reportVisitsMax  = 64 // streets remembered for one player within the window
	reportScanMax    = 5000
)

// reportChannels are the public channels a report may carry (channels.go knownChannel, without groups).
var reportChannels = map[string]bool{"global": true, "help": true, "newbie": true, "trade": true}

func init() {
	modules = append(modules, newReports)
	serviceHandlers["report_lines"] = reportLinesService
}

type reportVisit struct {
	street      string
	from, until time.Time // until zero: still there
}

type reports struct {
	h       *Hub
	visits  map[int64][]reportVisit
	sweptAt time.Time
}

func newReports(h *Hub) Module { return &reports{h: h, visits: map[int64][]reportVisit{}} }

func (m *reports) Register(r *Registry) {
	r.Provide("reports.self", m)
	r.OnJoin(func(cl *Client) {
		now := m.h.Now()
		m.sweepLocked(now)
		v := append(m.trimLocked(cl.ID(), now), reportVisit{street: cl.Street(), from: now})
		if len(v) > reportVisitsMax {
			v = v[len(v)-reportVisitsMax:]
		}
		m.visits[cl.ID()] = v
	})
	r.OnLeave(func(cl *Client, street string) {
		v := m.visits[cl.ID()]
		for i := len(v) - 1; i >= 0; i-- {
			if v[i].street == street && v[i].until.IsZero() {
				v[i].until = m.h.Now()
				break
			}
		}
	})
}

// trimLocked drops id's visits that ended before the window.
func (m *reports) trimLocked(id int64, now time.Time) []reportVisit {
	cut := now.Add(-reportWindow)
	var keep []reportVisit
	for _, v := range m.visits[id] {
		if v.until.IsZero() || !v.until.Before(cut) {
			keep = append(keep, v)
		}
	}
	if len(keep) == 0 {
		delete(m.visits, id)
	}
	return keep
}

// sweepLocked trims every player's visits, at most once a minute (players who went offline).
func (m *reports) sweepLocked(now time.Time) {
	if now.Sub(m.sweptAt) < time.Minute {
		return
	}
	m.sweptAt = now
	for id := range m.visits {
		if v := m.trimLocked(id, now); len(v) > 0 {
			m.visits[id] = v
		}
	}
}

// reportSight is what one reporter could see: their streets with times, the channels they are in.
type reportSight struct {
	reporter int64
	streets  []reportVisit
	rooms    map[string]bool
}

func (h *Hub) reportSightLocked(reporter int64, now time.Time) reportSight {
	s := reportSight{reporter: reporter, rooms: map[string]bool{}}
	if m, _ := h.provided["reports.self"].(*reports); m != nil {
		s.streets = append(s.streets, m.trimLocked(reporter, now)...)
		if len(s.streets) > 0 {
			m.visits[reporter] = append([]reportVisit(nil), s.streets...)
		}
	}
	if ch, _ := h.provided["channels.self"].(*channels); ch != nil {
		for room, r := range ch.rooms {
			if reportChannels[room] && r.members[reporter] {
				s.rooms[room] = true
			}
		}
	}
	return s
}

// sees is whether the reporter could see line l, said at at. Any kind but these three is never seen.
func (s reportSight) sees(l chatLine, at time.Time) bool {
	if !l.Delivered {
		return false
	}
	switch l.Kind {
	case "local":
		for _, v := range s.streets {
			if v.street == l.Where && !at.Before(v.from) && (v.until.IsZero() || at.Before(v.until)) {
				return true
			}
		}
	case "channel":
		return reportChannels[l.Where] && s.rooms[l.Where]
	case "party":
		for _, id := range l.To {
			if id == s.reporter {
				return true
			}
		}
	}
	return false
}

// seenSince reads the log's days from since on, oldest first, and keeps the lines s sees (the newest reportScanMax).
func (c *chatLog) seenSince(s reportSight, since, now time.Time) []chatLine {
	dels := c.deletions()
	var out []chatLine
	for d := since.UTC().Truncate(24 * time.Hour); !d.After(now.UTC()); d = d.AddDate(0, 0, 1) {
		b, err := os.ReadFile(filepath.Join(c.dir, d.Format("2006-01-02")+".jsonl"))
		if err != nil {
			continue
		}
		rows := strings.Split(string(b), "\n")
		for _, row := range rows[:len(rows)-1] { // the part after the last newline is a line still being written
			var l chatLine
			if json.Unmarshal([]byte(row), &l) != nil {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, l.At)
			if err != nil || at.Before(since) || at.After(now) || !s.sees(l, at) {
				continue
			}
			l.t = at
			l.Deleted = deletedMark(l, at, dels)
			out = append(out, l)
			if len(out) > reportScanMax {
				out = out[1:]
			}
		}
	}
	return out
}

// pickReportLines chooses from lines (oldest first) the reported player's lines and the lines that
// name them, with reportContext lines of the same chat either side, at most reportMaxLines (context
// goes first, then the oldest); none of theirs: the newest reportQuietLines.
func pickReportLines(lines []chatLine, reported int64, name string) []chatLine {
	name = strings.ToLower(strings.TrimSpace(name))
	room := func(l chatLine) string { return l.Kind + "\x00" + l.Where }
	anchor := make([]bool, len(lines))
	keep := make([]bool, len(lines))
	found := false
	for i, l := range lines {
		if l.ID == reported || (len([]rune(name)) >= 3 && strings.Contains(strings.ToLower(l.Text), name)) {
			anchor[i], keep[i], found = true, true, true
		}
	}
	if !found {
		return append([]chatLine(nil), lines[max(0, len(lines)-reportQuietLines):]...)
	}
	for i := range lines {
		if !anchor[i] {
			continue
		}
		for j, n := i-1, 0; j >= 0 && n < reportContext; j-- {
			if room(lines[j]) == room(lines[i]) {
				keep[j] = true
				n++
			}
		}
		for j, n := i+1, 0; j < len(lines) && n < reportContext; j++ {
			if room(lines[j]) == room(lines[i]) {
				keep[j] = true
				n++
			}
		}
	}
	count := 0
	for _, k := range keep {
		if k {
			count++
		}
	}
	for pass := 0; pass < 2 && count > reportMaxLines; pass++ {
		for i := range lines {
			if count <= reportMaxLines {
				break
			}
			if keep[i] && (pass == 1 || !anchor[i]) {
				keep[i] = false
				count--
			}
		}
	}
	var out []chatLine
	for i, l := range lines {
		if keep[i] {
			out = append(out, l)
		}
	}
	return out
}

type reportCall struct {
	Reporter int64  `json:"reporter"`
	Reported int64  `json:"reported"`
	Name     string `json:"name"`
}

type reportLine struct {
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Where    string `json:"where"`
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Text     string `json:"text"`
	Shown    string `json:"shown,omitempty"`
	Filtered bool   `json:"filtered,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
	Reported bool   `json:"reported,omitempty"`
}

func reportLinesService(h *Hub, body []byte) (int, any) {
	var c reportCall
	if json.Unmarshal(body, &c) != nil || c.Reporter <= 0 || c.Reported <= 0 || c.Reporter == c.Reported || len([]rune(c.Name)) > 64 {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	now := h.now()
	log := h.chatlogOf()
	sight := h.reportSightLocked(c.Reporter, now)
	h.mu.Unlock()
	window := int(reportWindow / time.Second)
	if log == nil {
		return http.StatusOK, map[string]any{"status": "off", "lines": []reportLine{}, "window": window}
	}
	log.flush()
	picked := pickReportLines(log.seenSince(sight, now.Add(-reportWindow), now), c.Reported, c.Name)
	out := make([]reportLine, 0, len(picked))
	for _, l := range picked {
		out = append(out, reportLine{At: l.At, Kind: l.Kind, Where: l.Where, ID: l.ID, Name: l.Name, Text: l.Text,
			Shown: l.Shown, Filtered: l.Filtered, Deleted: l.Deleted != nil, Reported: l.ID == c.Reported})
	}
	return http.StatusOK, map[string]any{"status": "ok", "lines": out, "window": window}
}
