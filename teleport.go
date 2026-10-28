package main

// Admin teleport (v0.2.2): an administrator frees a stuck player. The site's
// admin route (groddle-afternoon-site src/AdminTeleport.php, POST
// groddle/v1/admin/teleport, administrators only) asks this server to move a
// connected player live; nothing else can.
//
//	POST /ws?call=teleport
//	X-Groddle-Service: <seconds>.<nonce>.<HMAC>, signed as ledger.go signs, over route /realtime/v1/teleport
//	     {"player":7,"street":"GA5…","x":120,"y":540}            x and y optional
//	     200 {"status":"sent"}      the player was online: told, and taken off their street
//	     200 {"status":"offline"}   nobody by that id is connected (nothing happens; the site writes the save)
//	     401 {"status":"refused"}   no signature, a bad one, an old one or one seen before
//	     400 {"status":"bad"}       not a teleport this server can send
//
// Server to the moved player:
//
//	{"t":"teleport","street":"GA5…","x":120,"y":540,"by":"admin"}   x and y only when the site named a spot
//
// The call rides POST on /ws so it reaches this server through the same
// Caddy route as the socket (reverse_proxy /ws). It is never a socket
// message: no module handles "teleport", so a client sending one gets the
// hub's bad_frame and nobody moves. The player is dropped from their street
// as a leave would be (the others see them go) and joins the new one when
// their game has entered it.

import (
	"crypto/hmac"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	serviceRoutePrefix   = "/realtime/v1/"
	serviceRouteTeleport = serviceRoutePrefix + "teleport"
	serviceMaxBody       = 4096
	serviceMaxBans       = 65536 // the whole ban list (admin.go "bans")
	serviceWindow        = 60    // seconds either side, as src/ServiceCall.php
)

var serviceHeaderRe = regexp.MustCompile(`^(\d{1,12})\.([0-9a-f]{16})\.([A-Za-z0-9_-]{43})$`)

// serviceCalls checks the site's signed calls and refuses any nonce twice.
type serviceCalls struct {
	secret []byte
	now    func() time.Time

	mu   sync.Mutex
	seen map[string]int64 // nonce -> the seconds it was signed at
}

func newServiceCalls(secret []byte, now func() time.Time) *serviceCalls {
	if now == nil {
		now = time.Now
	}
	return &serviceCalls{secret: secret, now: now, seen: map[string]int64{}}
}

// check is whether header signs this call now, and spends its nonce.
func (s *serviceCalls) check(header, method, route string, body []byte) bool {
	m := serviceHeaderRe.FindStringSubmatch(header)
	if m == nil || len(s.secret) == 0 {
		return false
	}
	at, _ := strconv.ParseInt(m[1], 10, 64)
	now := s.now().Unix()
	if at < now-serviceWindow || at > now+serviceWindow {
		return false
	}
	want := signService(s.secret, method, route, at, m[2], body)
	if !hmac.Equal([]byte(want), []byte(header)) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for n, t := range s.seen {
		if t < now-2*serviceWindow {
			delete(s.seen, n)
		}
	}
	if _, used := s.seen[m[2]]; used {
		return false
	}
	s.seen[m[2]] = at
	return true
}

// teleportCall is the site's body.
type teleportCall struct {
	Player int64    `json:"player"`
	Street string   `json:"street"`
	X      *float64 `json:"x"`
	Y      *float64 `json:"y"`
}

// service answers the site's signed calls (POST /ws?call=<name>): teleport
// here, the console's calls in admin.go. Each is signed over its own route,
// /realtime/v1/<name>, so a signature for one call is never good for another.
func (s *Server) service(w http.ResponseWriter, r *http.Request) {
	answer := func(code int, out any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(out)
	}
	status := func(v string) map[string]string { return map[string]string{"status": v} }
	call := r.URL.Query().Get("call")
	handle, known := serviceHandlers[call]
	if !known {
		answer(http.StatusNotFound, status("bad"))
		return
	}
	limit := serviceMaxBody
	if call == "bans" {
		limit = serviceMaxBans
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if err != nil || len(body) > limit {
		answer(http.StatusBadRequest, status("bad"))
		return
	}
	if s.calls == nil || !s.calls.check(r.Header.Get("X-Groddle-Service"), http.MethodPost, serviceRoutePrefix+call, body) {
		answer(http.StatusUnauthorized, status("refused"))
		return
	}
	code, out := handle(s.hub, body)
	answer(code, out)
}

// serviceHandlers are the signed calls by name; each answers a status code and a JSON body.
var serviceHandlers = map[string]func(h *Hub, body []byte) (int, any){
	"teleport": teleportService,
}

func teleportService(h *Hub, body []byte) (int, any) {
	var c teleportCall
	if json.Unmarshal(body, &c) != nil || c.Player <= 0 || !streetRe.MatchString(c.Street) || !finite(c.X) || !finite(c.Y) {
		return http.StatusBadRequest, map[string]string{"status": "bad"}
	}
	if h.Teleport(c.Player, c.Street, c.X, c.Y) {
		return http.StatusOK, map[string]string{"status": "sent"}
	}
	return http.StatusOK, map[string]string{"status": "offline"}
}

func finite(v *float64) bool {
	return v == nil || (!math.IsNaN(*v) && !math.IsInf(*v, 0))
}

// Teleport tells a connected player to go to street (at x, y when given) and
// takes them off the street they are on, as a leave would. False when no one
// by that id is online: nothing changes.
func (h *Hub) Teleport(id int64, street string, x, y *float64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	cl := h.clients[id]
	if cl == nil || cl.closed {
		return false
	}
	msg := map[string]any{"t": "teleport", "street": street, "by": "admin"}
	if x != nil {
		msg["x"] = coord(x)
	}
	if y != nil {
		msg["y"] = coord(y)
	}
	h.leaveLocked(cl)
	h.queueLocked(cl, msg)
	return true
}
