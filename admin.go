package main

// The administrators' console (groddle-afternoon-site src/AdminConsole.php,
// https://<site>/console/, manage_options only). The site asks; this server
// answers for who is connected and reaches a connected player live. Every
// call is the site's, signed as teleport.go's are (X-Groddle-Service over the
// route /realtime/v1/<call> and the body, 60 s, each nonce once), on
// POST /ws?call=<call>. None is ever a socket message: no module handles
// these kinds, so a client sending one gets the hub's bad_frame.
//
//	online  {}                                        200 {"status":"ok","players":[{"id":7,"name":"Ada","street":"GA5…","since":1730000000}]}
//	                                                  (street "" while between streets; since: unix seconds this session began)
//	message {"player":7,"id":"am-<16 hex>","text":"…"} 200 {"status":"sent"} | {"status":"queued"} (offline: kept for their next connect)
//	boot    {"player":7,"reason":"…"}                 200 {"status":"sent"} | {"status":"offline"}
//	ban     {"player":7,"reason":"…"}                 200 {"status":"sent"} (was online: signed out) | {"status":"banned"}
//	unban   {"player":7}                              200 {"status":"unbanned"}
//	bans    {"players":[7,9]}                         200 {"status":"ok","count":2}   the whole list, replacing this server's
//	give    {"player":7,"op":"admin-give-<16 hex>","items":[{"c":"cherry","n":3}],"currants":50,"text":"An administrator gave you 3 Cherries and 50 currants."}
//	                                                  200 {"status":"sent"} | {"status":"offline"} (the site's ledger already holds it; their next load applies it)
//	refill  {"player":7}                              200 {"status":"sent"} | {"status":"offline"}
//	any:    401 {"status":"refused"} unsigned, a bad, old or replayed signature; 400 {"status":"bad"} not a call this server can make
//
// Server to the player:
//
//	{"t":"admin_message","id":"am-…","text":"…","at":1730000000000}
//	{"t":"booted","reason":"…","banned":false}   then the socket closes with closeBooted (4010), or closeBanned (4011) when banned
//	{"t":"admin_give","op":"admin-give-…","items":[{"c":"cherry","n":3}],"currants":50,"text":"An administrator gave you …"}
//	{"t":"admin_refill"}
//
// Bans are enforced here too, not only by the site refusing the ticket: a
// ticket for a banned character is refused at auth (closeBanned, "suspended"),
// so a ticket signed just before the ban, or a site that has not caught up,
// still cannot connect. The list is kept in a state file (-bans-file, default
// realtime-bans.json beside -secret-file; off: memory only), and the site
// sends the whole list (bans) whenever its console opens, so a server that
// lost its file is set right. Messages for an offline player wait in memory
// only (at most adminQueueMax each, adminQueueFor), so a restart drops them;
// the site's log says "queued", never "delivered".

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	closeBooted = 4010 // an administrator signed this character out
	closeBanned = 4011 // this account is suspended

	adminMaxText   = 500
	adminMaxReason = 200
	adminQueueMax  = 5
	adminQueueFor  = 7 * 24 * time.Hour
	adminGiveMax   = 10 // stacks in one give, as the site's AdminConsole::GIVE_STACKS
)

var (
	bansFileFlag    = flag.String("bans-file", "", "file that keeps the suspended accounts the site's console banned (default: realtime-bans.json beside -secret-file; off: memory only)")
	bansFileForTest string

	adminMessageIDRe = regexp.MustCompile(`^am-[0-9a-f]{16}$`)
	adminGiveOpRe    = regexp.MustCompile(`^admin-give-[0-9a-f]{16}$`)
	itemClassRe      = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

func init() {
	modules = append(modules, newAdmin)
	serviceHandlers["online"] = adminOnline
	serviceHandlers["message"] = adminMessage
	serviceHandlers["boot"] = adminBoot
	serviceHandlers["ban"] = adminBan
	serviceHandlers["unban"] = adminUnban
	serviceHandlers["bans"] = adminBans
	serviceHandlers["give"] = adminGive
	serviceHandlers["refill"] = adminRefill
}

type adminQueued struct {
	ID   string
	Text string
	At   time.Time
}

type admin struct {
	h      *Hub
	path   string
	bans   map[int64]bool
	since  map[int64]time.Time // when each connected character's session began
	queued map[int64][]adminQueued
}

func newAdmin(h *Hub) Module {
	m := &admin{h: h, bans: map[int64]bool{}, since: map[int64]time.Time{}, queued: map[int64][]adminQueued{}}
	m.path = statePath(bansFileForTest, bansFileFlag, "realtime-bans.json")
	if m.path != "" {
		var file struct {
			Banned []int64 `json:"banned"`
		}
		if ok, err := readStateFile("bans", m.path, &file); err != nil {
			log.Printf("bans %s: %v (bans are kept in memory only)", m.path, err)
			m.path = ""
		} else if ok {
			for _, id := range file.Banned {
				if id > 0 {
					m.bans[id] = true
				}
			}
		}
	}
	return m
}

func (m *admin) Register(r *Registry) {
	r.Provide("admin.self", m)
	r.OnConnect(m.connected)
	r.OnDisconnect(func(cl *Client) { delete(m.since, cl.id) })
}

// connected starts a session's clock (a replaced socket keeps it) and hands over waiting messages.
func (m *admin) connected(cl *Client) {
	if _, ok := m.since[cl.id]; !ok {
		m.since[cl.id] = m.h.now()
	}
	waiting := m.queued[cl.id]
	delete(m.queued, cl.id)
	for _, q := range waiting {
		if m.h.now().Sub(q.At) <= adminQueueFor {
			m.h.queueLocked(cl, adminMessageFrame(q))
		}
	}
}

func adminMessageFrame(q adminQueued) map[string]any {
	return map[string]any{"t": "admin_message", "id": q.ID, "text": q.Text, "at": q.At.UnixMilli()}
}

// saveLocked writes the ban list (under the hub lock; small, and only on a ban or an unban).
func (m *admin) saveLocked() {
	if m.path == "" {
		return
	}
	ids := make([]int64, 0, len(m.bans))
	for id := range m.bans {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	b, _ := json.Marshal(map[string]any{"banned": ids})
	if err := writeFileSynced(m.path, b); err != nil {
		log.Printf("bans %s: %v", m.path, err)
	}
}

// adminOf is the hub's admin module (nil in a build without it).
func (h *Hub) adminOf() *admin {
	m, _ := h.provided["admin.self"].(*admin)
	return m
}

// Banned is whether a character's account is suspended (checked at auth, before Connect).
func (h *Hub) Banned(id int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.adminOf()
	return m != nil && m.bans[id]
}

// bootLocked tells a connected character why, takes them off their street and offline, and closes the socket with why.
func (h *Hub) bootLocked(cl *Client, reason string, banned bool) {
	code := closeBooted
	if banned {
		code = closeBanned
	}
	h.queueLocked(cl, map[string]any{"t": "booted", "reason": reason, "banned": banned})
	h.leaveLocked(cl)
	if h.clients[cl.id] == cl {
		delete(h.clients, cl.id)
		for _, fn := range h.onDisconnect {
			fn(cl)
		}
	}
	h.closeLocked(cl, code)
}

// cleanReason keeps an administrator's words to one line of at most n characters.
func cleanReason(s string, n int) string {
	s = strings.Join(strings.Fields(stripControl(strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " "))), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

// cleanMessage keeps line breaks (at most two in a row) and drops other control characters.
func cleanMessage(s string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.Join(strings.Fields(stripControl(l)), " ")
		if l == "" {
			blank++
			if blank > 1 || len(out) == 0 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	s = strings.TrimSpace(strings.Join(out, "\n"))
	if r := []rune(s); len(r) > adminMaxText {
		s = strings.TrimSpace(string(r[:adminMaxText]))
	}
	return s
}

var (
	adminBad = map[string]string{"status": "bad"}
	statusOf = func(s string) map[string]string { return map[string]string{"status": s} }
)

type adminPlayerCall struct {
	Player int64  `json:"player"`
	Reason string `json:"reason"`
	Text   string `json:"text"`
	ID     string `json:"id"`
}

func readPlayerCall(body []byte) (adminPlayerCall, bool) {
	var c adminPlayerCall
	if json.Unmarshal(body, &c) != nil || c.Player <= 0 {
		return c, false
	}
	return c, true
}

func adminOnline(h *Hub, body []byte) (int, any) {
	if !json.Valid(body) {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.adminOf()
	type row struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Street string `json:"street"`
		Since  int64  `json:"since"`
	}
	players := make([]row, 0, len(h.clients))
	for id, cl := range h.clients {
		if cl.closed {
			continue
		}
		since := h.now()
		if m != nil {
			if t, ok := m.since[id]; ok {
				since = t
			}
		}
		players = append(players, row{ID: id, Name: cl.name, Street: cl.street, Since: since.Unix()})
	}
	sort.Slice(players, func(i, j int) bool {
		a, b := strings.ToLower(players[i].Name), strings.ToLower(players[j].Name)
		return a < b || (a == b && players[i].ID < players[j].ID)
	})
	return http.StatusOK, map[string]any{"status": "ok", "players": players}
}

func adminMessage(h *Hub, body []byte) (int, any) {
	c, ok := readPlayerCall(body)
	text := cleanMessage(c.Text)
	if !ok || text == "" || !adminMessageIDRe.MatchString(c.ID) {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	q := adminQueued{ID: c.ID, Text: text, At: h.now()}
	if cl := h.clients[c.Player]; cl != nil && !cl.closed {
		h.queueLocked(cl, adminMessageFrame(q))
		return http.StatusOK, statusOf("sent")
	}
	m := h.adminOf()
	if m == nil {
		return http.StatusOK, statusOf("offline")
	}
	waiting := append(m.queued[c.Player], q)
	if len(waiting) > adminQueueMax {
		waiting = waiting[len(waiting)-adminQueueMax:]
	}
	m.queued[c.Player] = waiting
	return http.StatusOK, statusOf("queued")
}

func adminBoot(h *Hub, body []byte) (int, any) {
	c, ok := readPlayerCall(body)
	if !ok {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cl := h.clients[c.Player]
	if cl == nil || cl.closed {
		return http.StatusOK, statusOf("offline")
	}
	h.bootLocked(cl, cleanReason(c.Reason, adminMaxReason), false)
	return http.StatusOK, statusOf("sent")
}

func adminBan(h *Hub, body []byte) (int, any) {
	c, ok := readPlayerCall(body)
	if !ok {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.adminOf()
	if m == nil {
		return http.StatusBadRequest, adminBad
	}
	if !m.bans[c.Player] {
		m.bans[c.Player] = true
		m.saveLocked()
	}
	delete(m.queued, c.Player)
	if cl := h.clients[c.Player]; cl != nil && !cl.closed {
		h.bootLocked(cl, cleanReason(c.Reason, adminMaxReason), true)
		return http.StatusOK, statusOf("sent")
	}
	return http.StatusOK, statusOf("banned")
}

func adminUnban(h *Hub, body []byte) (int, any) {
	c, ok := readPlayerCall(body)
	if !ok {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.adminOf()
	if m == nil {
		return http.StatusBadRequest, adminBad
	}
	if m.bans[c.Player] {
		delete(m.bans, c.Player)
		m.saveLocked()
	}
	return http.StatusOK, statusOf("unbanned")
}

func adminBans(h *Hub, body []byte) (int, any) {
	var c struct {
		Players []int64 `json:"players"`
	}
	if json.Unmarshal(body, &c) != nil || c.Players == nil {
		return http.StatusBadRequest, adminBad
	}
	next := map[int64]bool{}
	for _, id := range c.Players {
		if id <= 0 {
			return http.StatusBadRequest, adminBad
		}
		next[id] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.adminOf()
	if m == nil {
		return http.StatusBadRequest, adminBad
	}
	changed := len(next) != len(m.bans)
	for id := range next {
		changed = changed || !m.bans[id]
	}
	if changed {
		m.bans = next
		m.saveLocked()
	}
	// Anyone the list names who is connected now goes (a server that had lost its file).
	for id := range next {
		if cl := h.clients[id]; cl != nil && !cl.closed {
			h.bootLocked(cl, "", true)
		}
	}
	return http.StatusOK, map[string]any{"status": "ok", "count": len(next)}
}

type adminGiveCall struct {
	Player int64  `json:"player"`
	Op     string `json:"op"`
	Items  []struct {
		C string `json:"c"`
		N int    `json:"n"`
	} `json:"items"`
	Currants int    `json:"currants"`
	Text     string `json:"text"`
}

func adminGive(h *Hub, body []byte) (int, any) {
	var c adminGiveCall
	if json.Unmarshal(body, &c) != nil || c.Player <= 0 || !adminGiveOpRe.MatchString(c.Op) || len(c.Items) > adminGiveMax || c.Currants < 0 || c.Currants > 1000000 || (len(c.Items) == 0 && c.Currants == 0) {
		return http.StatusBadRequest, adminBad
	}
	items := make([]map[string]any, 0, len(c.Items))
	seen := map[string]bool{}
	for _, it := range c.Items {
		if !itemClassRe.MatchString(it.C) || it.N < 1 || it.N > 9999 || seen[it.C] {
			return http.StatusBadRequest, adminBad
		}
		seen[it.C] = true
		items = append(items, map[string]any{"c": it.C, "n": it.N})
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cl := h.clients[c.Player]
	if cl == nil || cl.closed {
		return http.StatusOK, statusOf("offline")
	}
	h.queueLocked(cl, map[string]any{"t": "admin_give", "op": c.Op, "items": items, "currants": c.Currants, "text": cleanReason(c.Text, adminMaxReason)})
	return http.StatusOK, statusOf("sent")
}

func adminRefill(h *Hub, body []byte) (int, any) {
	c, ok := readPlayerCall(body)
	if !ok {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cl := h.clients[c.Player]
	if cl == nil || cl.closed {
		return http.StatusOK, statusOf("offline")
	}
	h.queueLocked(cl, map[string]any{"t": "admin_refill"})
	return http.StatusOK, statusOf("sent")
}
