package main

// The website's live socket (Austin 2026-11-04: "could we make it feel more
// realtime for logged in folks?"). The site's /chat/ page
// (groddle-afternoon-site src/CommunityPages.php, community/community.js)
// opens the same /ws as the game with a ticket the site made for it
// (groddle/v1/realtime with {"via":"web"}: Claims.Via "web"), and the
// server pushes what the page used to poll for: channel lines, their
// reactions and thread replies (threads.go), who is around (presence.go)
// and new posts on the Community boards. A web socket is light: no avatar,
// no street, none of the game's systems. It never replaces the character's
// game socket (closeReplaced is for a second game), a player may have the
// page open in several tabs, and it does not count as playing (no credits,
// no buddy_online in the game's list; presence.go says "on the web").
//
// Client to server (a web socket):
//
//	{"t":"auth","ticket":"…"}                        first frame, a ticket with "via":"web"
//	{"t":"web_watch","buddies":"…","groups":"…"}     the two lists the site signed (buddies.go, channels.go tokens),
//	                                                 in the ticket's answer; again before they expire
//	{"t":"active"}                                   the reader did something (presence.go idle)
//	{"t":"react",…} {"t":"im_react",…} {"t":"im_read","with":9}   as the game's (reactions.go, im.go), after web_watch:
//	                                                 a channel line the lists let it read; its own IMs
//	{"t":"ping"}
//
// Server to client:
//
//	{"t":"hello","you":{"id":7,"name":"Ada"},"web":true,"off":false}
//	{"t":"web_status","channels":["global","help"],"groups":[12]}        after each web_watch
//	{"t":"channel_said",…}  {"t":"reacted",…}  {"t":"thread_said",…}     as the game gets them, for each channel
//	                                                 the reader may read (the groups token), never a line by a
//	                                                 player the reader blocks
//	{"t":"presence",…}  {"t":"presence_up",…}  {"t":"presence_gone",…}   presence.go
//	{"t":"im","with":{…},"line":{…},"unread":n} {"t":"im_reacted",…}  the reader's own IMs, either side (im.go deliver)
//	{"t":"board_post","board":"general","thread":12,"post":34,"kind":"reply"}  a new Community post (the page
//	                                                 asks the site for it, which applies blocks again)
//	{"t":"error","code":"web","message":"…"}
//
// The site's signed calls (teleport.go's POST /ws?call=…):
//
//	community_note {"board":"general","thread":12,"post":34,"author":7,"kind":"thread"|"reply"}   200 {"status":"ok","told":n}
//	web_im         {"player":7,"name":"Ada","to":9,"text":"hi","parent":0}  an IM written on the website (im.go sendAs):
//	               200 {"status":"sent"} | 403/429 {"status":"no","reason":"…","message":"…"}
//	presence_hide  {"player":7,"off":true}                                                          200 {"status":"ok"}  (presence.go)
//
// Who gets what: only a signed-in player's socket exists at all (the site
// makes tickets for signed-in players only, and a suspended account is
// refused at auth and closed when banned); nothing is pushed before its
// web_watch (no lists, no lines); the lines are the channels its groups token
// names, as the game's channel_join allows them; IMs never go to a web socket
// (the page has none). Origins are the server's -origins, as for the game.
//
// Rates: webFrameBurst frames, then one each webFrameEvery; more are dropped.

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"time"
)

var boardRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

const (
	webFrameBurst = 12
	webFrameEvery = 250 * time.Millisecond
	webMaxTabs    = 8 // web sockets one character may hold; the oldest goes
)

func init() {
	serviceHandlers["community_note"] = communityNoteService
	serviceHandlers["web_im"] = webIMService
}

type webIMCall struct {
	Player int64  `json:"player"`
	Name   string `json:"name"`
	To     int64  `json:"to"`
	Text   string `json:"text"`
	Parent int64  `json:"parent"`
}

// webIMService is an IM written on the website: the same rules as one typed in the game (im.go).
func webIMService(h *Hub, body []byte) (int, any) {
	var c webIMCall
	if json.Unmarshal(body, &c) != nil || c.Player <= 0 || c.To <= 0 || c.Parent < 0 {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if a := h.adminOf(); a != nil && a.bans[c.Player] {
		return http.StatusForbidden, map[string]string{"status": "banned"}
	}
	m, _ := h.provided["ims.self"].(*ims)
	if m == nil {
		return http.StatusServiceUnavailable, map[string]string{"status": "bad"}
	}
	name := cleanName(c.Name)
	if cl := h.clients[c.Player]; cl != nil {
		name = cl.Name()
	}
	reason, message := m.sendAs(c.Player, name, c.To, c.Text, c.Parent)
	switch reason {
	case "":
		return http.StatusOK, map[string]string{"status": "sent"}
	case "bad":
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	case "slow_down":
		return http.StatusTooManyRequests, map[string]string{"status": "no", "reason": reason, "message": message}
	}
	return http.StatusForbidden, map[string]string{"status": "no", "reason": reason, "message": message}
}

// webWatch is what a web socket's two signed lists say.
type webWatch struct {
	rooms   map[string]bool // the channel rooms (global, help or newbie, trade)
	groups  map[int64]bool
	silence map[string]bool
	bud     map[int64]bool
	rev     map[int64]bool
	ign     map[int64]bool
	exp     time.Time
}

// inputKinds are the game's frames a player sends by doing something (presence.go idle): not ping,
// not the lists and syncs a game sends by itself.
var inputKinds = map[string]bool{
	"join": true, "move": true, "chat": true, "typing": true, "emote": true, "active": true,
	"channel_say": true, "thread_say": true, "im_send": true, "react": true, "im_react": true,
}

// ConnectWeb adds a web socket for a character; the game socket, if any, is untouched.
func (h *Hub) ConnectWeb(c Claims) *Client {
	cl := &Client{id: c.Sub, acct: c.login(), name: c.Name, web: true, send: make(chan []byte, sendBuffer), done: make(chan struct{}), at: h.now()}
	h.mu.Lock()
	defer h.mu.Unlock()
	tabs := h.webs[c.Sub]
	if tabs == nil {
		tabs = map[*Client]bool{}
		h.webs[c.Sub] = tabs
	}
	if len(tabs) >= webMaxTabs {
		var oldest *Client
		for o := range tabs {
			if oldest == nil || o.chatAt.Before(oldest.chatAt) {
				oldest = o
			}
		}
		h.closeLocked(oldest, closeReplaced)
		h.dropWebLocked(oldest)
		tabs = h.webs[c.Sub]
		if tabs == nil {
			tabs = map[*Client]bool{}
			h.webs[c.Sub] = tabs
		}
	}
	cl.chatAt = h.now() // when it connected (the oldest tab goes first)
	tabs[cl] = true
	h.off[c.Sub] = c.Off
	h.inputAt[c.Sub] = h.now()
	h.applyModClaimLocked(c.Sub, c.Mod, c.Iat) // moderation.go
	h.queueLocked(cl, map[string]any{"t": "hello", "you": map[string]any{"id": cl.id, "name": cl.name}, "web": true, "off": c.Off})
	for _, fn := range h.onWeb {
		fn(cl, "connect")
	}
	return cl
}

// DisconnectWeb removes a web socket that ended.
func (h *Hub) DisconnectWeb(cl *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropWebLocked(cl)
	h.closeLocked(cl, 0)
}

// dropWebLocked takes a web socket out of the hub (once).
func (h *Hub) dropWebLocked(cl *Client) {
	tabs := h.webs[cl.id]
	if !tabs[cl] {
		return
	}
	delete(tabs, cl)
	if len(tabs) == 0 {
		delete(h.webs, cl.id)
	}
	for _, fn := range h.onWeb {
		fn(cl, "disconnect")
	}
	h.forgetIdleLocked(cl.id)
}

// forgetIdleLocked drops a character's presence inputs once no socket of theirs is left.
func (h *Hub) forgetIdleLocked(id int64) {
	if h.clients[id] == nil && len(h.webs[id]) == 0 {
		delete(h.off, id)
		delete(h.inputAt, id)
	}
}

// touchLocked notes that a character did something now (presence.go idle).
func (h *Hub) touchLocked(id int64) {
	was := h.inputAt[id]
	h.inputAt[id] = h.now()
	if fn, ok := h.provided["presence.touched"].(func(int64, time.Time)); ok {
		fn(id, was)
	}
}

// OffLocked is whether a connected character has "Appear offline" on.
func (h *Hub) OffLocked(id int64) bool { return h.off[id] }

// WebLocked lists a character's web sockets, oldest first.
func (h *Hub) WebLocked(id int64) []*Client {
	out := make([]*Client, 0, len(h.webs[id]))
	for cl := range h.webs[id] {
		out = append(out, cl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].chatAt.Before(out[j].chatAt) })
	return out
}

// webViewersLocked is every web socket with a list in, by character id then age.
func (h *Hub) webViewersLocked() []*Client {
	ids := make([]int64, 0, len(h.webs))
	for id := range h.webs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []*Client
	for _, id := range ids {
		for _, cl := range h.WebLocked(id) {
			if cl.ww != nil && !cl.closed && !cl.ww.exp.Before(h.now()) {
				out = append(out, cl)
			}
		}
	}
	return out
}

// webIgnores is whether any of a's web sockets carries a list that ignores b.
func (h *Hub) webIgnores(a, b int64) bool {
	for cl := range h.webs[a] {
		if cl.ww != nil && cl.ww.ign[b] {
			return true
		}
	}
	return false
}

// webHolds is whether any of a's web sockets carries a list that names b as a buddy, either way.
func (h *Hub) webHolds(a, b int64) bool {
	for cl := range h.webs[a] {
		if cl.ww != nil && !cl.ww.exp.Before(h.now()) && (cl.ww.bud[b] || cl.ww.rev[b]) {
			return true
		}
	}
	return false
}

// readsRoom is whether a web socket's groups token lets it read a channel room.
func (w *webWatch) readsRoom(room string) bool {
	if w == nil {
		return false
	}
	if id, ok := groupChannel(room); ok {
		return w.groups[id]
	}
	return w.rooms[room]
}

// webRoomLocked sends msg to every web socket that may read room, but the ones whose reader blocks author.
func (h *Hub) webRoomLocked(room string, msg any, author int64) {
	var b []byte
	for _, cl := range h.webViewersLocked() {
		if !cl.ww.readsRoom(room) || (author > 0 && cl.id != author && h.ignoringLocked(cl.id, author)) {
			continue
		}
		if b == nil {
			b, _ = json.Marshal(msg)
		}
		h.pushLocked(cl, b)
	}
}

// ignoringLocked is buddies.ignoring (the game's list and the web's), false without the module.
func (h *Hub) ignoringLocked(a, b int64) bool {
	ign, ok := h.ProvidedLocked("buddies.ignoring").(func(int64, int64) bool)
	return ok && ign(a, b)
}

func (h *Hub) handleWebLocked(cl *Client, kind string, frame []byte) {
	if !cl.webRate.take(h.now(), webFrameBurst, webFrameEvery) {
		return
	}
	switch kind {
	case "ping":
		h.queueLocked(cl, map[string]any{"t": "pong"})
	case "active":
		h.touchLocked(cl.id)
	case "web_watch":
		h.webWatchLocked(cl, frame)
	case "react", "im_react", "im_read":
		// The reactions and read marks of the lines this socket has (reactions.go, im.go), after its lists.
		if cl.ww == nil {
			h.queueLocked(cl, map[string]any{"t": "error", "code": "web", "message": "Your lists are not checked yet."})
			return
		}
		h.touchLocked(cl.id)
		if fn := h.handlers[kind]; fn != nil {
			fn(cl, frame)
		}
	default:
		h.queueLocked(cl, map[string]any{"t": "error", "code": "web", "message": "That message kind is not one the website's socket uses."})
	}
}

func (h *Hub) webWatchLocked(cl *Client, frame []byte) {
	var msg struct {
		Buddies string `json:"buddies"`
		Groups  string `json:"groups"`
	}
	refuse := func(why string) {
		h.queueLocked(cl, map[string]any{"t": "error", "code": "web", "message": "Your lists could not be checked: " + why + "."})
	}
	if json.Unmarshal(frame, &msg) != nil {
		refuse("not JSON")
		return
	}
	bm, _ := h.provided["buddies.self"].(*buddies)
	cm, _ := h.provided["channels.self"].(*channels)
	if bm == nil || cm == nil {
		refuse("this server has no lists")
		return
	}
	bc, err := bm.verify(msg.Buddies, "buddies", cl.id)
	if err != nil {
		refuse(err.Error())
		return
	}
	gc, err := cm.verify(msg.Groups, cl.id)
	if err != nil {
		refuse(err.Error())
		return
	}
	bud, ok1 := buddyIDs(bc.B)
	rev, ok2 := buddyIDs(bc.R)
	ign, ok3 := buddyIDs(bc.I)
	groups, ok4 := buddyIDs(gc.G)
	rooms, ok5 := chanWords(gc.C, chanRooms)
	silence, ok6 := chanWords(gc.S, map[string]bool{"chat": true, "help": true})
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || len(groups) > chanMaxGroups || (rooms["help"] && rooms["newbie"]) {
		refuse(errChanToken.Error())
		return
	}
	exp := time.Unix(min(bc.Exp, gc.Exp), 0)
	cl.ww = &webWatch{rooms: rooms, groups: groups, silence: silence, bud: bud, rev: rev, ign: ign, exp: exp}
	chans := []string{}
	probe := &chanWatch{rooms: rooms}
	for _, ch := range []string{"global", "help", "trade"} {
		if room, _ := cm.roomFor(probe, ch); room != "" {
			chans = append(chans, ch)
		}
	}
	gs := make([]int64, 0, len(groups))
	for g := range groups {
		gs = append(gs, g)
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i] < gs[j] })
	h.queueLocked(cl, map[string]any{"t": "web_status", "channels": chans, "groups": gs})
	for _, fn := range h.onWeb {
		fn(cl, "watch")
	}
}

// WebCount is how many web sockets are open (healthz).
func (h *Hub) WebCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, tabs := range h.webs {
		n += len(tabs)
	}
	return n
}

// closeWebLocked closes every web socket of a character (a ban) with why, and of every character
// whose login is id (a ban on a login covers each character of it).
func (h *Hub) closeWebLocked(id int64, why int) {
	ids := []int64{id}
	for cid, tabs := range h.webs {
		for cl := range tabs {
			if cid != id && cl.acct == id {
				ids = append(ids, cid)
				break
			}
		}
	}
	for _, cid := range ids {
		for _, cl := range h.WebLocked(cid) {
			h.queueLocked(cl, map[string]any{"t": "booted", "banned": why == closeBanned})
			h.dropWebLocked(cl)
			h.closeLocked(cl, why)
		}
	}
}

// —— A new Community post (the site's signed call) ——

type communityNoteCall struct {
	Board  string `json:"board"`
	Thread int64  `json:"thread"`
	Post   int64  `json:"post"`
	Author int64  `json:"author"`
	Kind   string `json:"kind"`
}

func communityNoteService(h *Hub, body []byte) (int, any) {
	var c communityNoteCall
	if json.Unmarshal(body, &c) != nil || c.Thread <= 0 || c.Post <= 0 || c.Author <= 0 || (c.Kind != "thread" && c.Kind != "reply") || !boardRe.MatchString(c.Board) {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	msg, _ := json.Marshal(map[string]any{"t": "board_post", "board": c.Board, "thread": c.Thread, "post": c.Post, "kind": c.Kind})
	told := 0
	for _, cl := range h.webViewersLocked() {
		if cl.id != c.Author && (h.ignoringLocked(cl.id, c.Author) || h.ignoringLocked(c.Author, cl.id)) {
			continue
		}
		h.pushLocked(cl, msg)
		told++
	}
	return http.StatusOK, map[string]any{"status": "ok", "told": told}
}
