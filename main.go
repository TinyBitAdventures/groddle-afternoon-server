// Command realtime is Groddle Afternoon's live server for multiplayer: who is on
// each street, where they stand, chat and emotes. Nothing here lasts: Minn (the
// Groddle site) keeps accounts and saves, and signs the ticket a player opens
// the socket with (groddle/v1/realtime). Caddy sends https://<site>/ws here.
//
//	go run . -addr 127.0.0.1:8793 -secret-file /path/to/realtime.secret
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const (
	closeReplaced = 4001 // the same character connected somewhere else
	closeTicket   = 4003 // no ticket, or one that does not check out
	closeSlow     = 4008 // stopped reading

	authWait  = 5 * time.Second
	pingEvery = 25 * time.Second
	maxFrame  = 8192
)

// version names the build: deploy/install-remote.sh sets it to the release tag (-ldflags -X main.version=...).
var version = "dev"

func main() {
	addr := flag.String("addr", "127.0.0.1:8793", "where to listen")
	secretFile := flag.String("secret-file", "", "the shared secret Minn signs tickets with")
	origins := flag.String("origins", "", "extra allowed page origins (host patterns, comma separated); the socket's own host is always allowed")
	flag.Parse()

	secret, err := readSecret(*secretFile)
	if err != nil {
		log.Fatal(err)
	}
	srv := NewServer(NewTickets(secret, nil), NewHub(nil), splitList(*origins))
	httpServer := &http.Server{Addr: *addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shut)
		srv.hub.Shutdown()
		close(done)
	}()
	log.Printf("groddle realtime listening on %s", *addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-done
}

func readSecret(path string) ([]byte, error) {
	if v := os.Getenv("GRODDLE_REALTIME_SECRET"); v != "" {
		path = ""
		if len(v) >= 32 {
			return []byte(v), nil
		}
	}
	if path == "" {
		return nil, errors.New("give -secret-file (or GRODDLE_REALTIME_SECRET), at least 32 bytes")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(b))
	if len(s) < 32 {
		return nil, errors.New("the secret is shorter than 32 bytes")
	}
	return []byte(s), nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Server answers /ws (the socket, and the site's signed POST calls) and /healthz (counts, for a quick look).
type Server struct {
	tickets *Tickets
	hub     *Hub
	origins []string
	calls   *serviceCalls // the site's signed calls (teleport.go), with the tickets' secret
}

func NewServer(t *Tickets, h *Hub, origins []string) *Server {
	return &Server{tickets: t, hub: h, origins: origins, calls: newServiceCalls(t.secret, t.now)}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/ws":
		if r.Method == http.MethodPost {
			s.service(w, r) // the site's signed calls (teleport.go), never a socket
			return
		}
		s.socket(w, r)
	case "/healthz", "/ws/healthz":
		players, streets := s.hub.Stats()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"players": players, "streets": streets, "web": s.hub.WebCount(), "version": version})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) socket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins})
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrame)
	ctx := r.Context()
	start := time.Now()

	claims, ok := s.auth(ctx, conn, start)
	if !ok {
		return
	}
	// A suspended account never connects, whatever ticket it holds (admin.go).
	if s.hub.Banned(claims.Sub) {
		conn.Close(closeBanned, "this account is suspended")
		RefusedNote(closeBanned, "player "+strconv.FormatInt(claims.Sub, 10)+": this account is suspended", time.Since(start))
		return
	}
	var cl *Client
	if claims.Via == "web" {
		// web.go: the website's /chat/ page, beside the game (never replacing it).
		cl = s.hub.ConnectWeb(claims)
		defer s.hub.DisconnectWeb(cl)
	} else {
		cl = s.hub.Connect(claims)
		defer s.hub.Disconnect(cl)
	}

	go s.write(ctx, conn, cl)
	for {
		kind, frame, err := conn.Read(ctx)
		if err != nil {
			code, reason := readEnd(ctx, err)
			s.hub.CloseNote(cl, code, reason) // hub.go: one line, the hub's own close or the write's failure first
			return
		}
		healthFramesIn.Add(1) // health.go: frames read a minute
		if kind == websocket.MessageText {
			s.hub.Handle(cl, frame)
		}
	}
}

// readEnd is the close code and reason a failed read says: the peer's close frame, a frame over
// maxFrame (1009, as the websocket library closes it), or a socket that dropped with none (1006).
func readEnd(ctx context.Context, err error) (int, string) {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return int(ce.Code), ce.Reason
	}
	msg := err.Error()
	if strings.Contains(msg, "read limited") {
		return int(websocket.StatusMessageTooBig), "read limit: " + msg
	}
	if ctx.Err() != nil {
		return 1006, "request ended: " + msg
	}
	return 1006, msg
}

// auth waits for the first frame, the ticket. A refusal logs one line (hub.go RefusedNote).
func (s *Server) auth(ctx context.Context, conn *websocket.Conn, start time.Time) (Claims, bool) {
	actx, cancel := context.WithTimeout(ctx, authWait)
	defer cancel()
	_, frame, err := conn.Read(actx)
	if err != nil {
		conn.Close(closeTicket, "no ticket")
		RefusedNote(closeTicket, "no ticket: "+err.Error(), time.Since(start))
		return Claims{}, false
	}
	var m struct {
		T      string `json:"t"`
		Ticket string `json:"ticket"`
	}
	if json.Unmarshal(frame, &m) != nil || m.T != "auth" {
		conn.Close(closeTicket, "the first message must be the ticket")
		RefusedNote(closeTicket, "the first message must be the ticket", time.Since(start))
		return Claims{}, false
	}
	claims, err := s.tickets.Check(m.Ticket)
	if err != nil {
		conn.Close(closeTicket, err.Error())
		RefusedNote(closeTicket, "ticket: "+err.Error(), time.Since(start))
		return Claims{}, false
	}
	return claims, true
}

// write drains the client's queue to the socket and keeps it alive with pings.
func (s *Server) write(ctx context.Context, conn *websocket.Conn, cl *Client) {
	tick := time.NewTicker(pingEvery)
	defer tick.Stop()
	for {
		select {
		case b := <-cl.send:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				why := "write: " + err.Error()
				cl.wfail.Store(&why) // the close line (hub.go CloseNote) names it
				conn.CloseNow()
				return
			}
			healthFramesOut.Add(1) // health.go: frames written a minute
		case <-tick.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				why := "ping: " + err.Error()
				cl.wfail.Store(&why)
				conn.CloseNow()
				return
			}
		case <-cl.done:
			// Flush what is queued, then close with the hub's reason.
			for {
				select {
				case b := <-cl.send:
					wctx, cancel := context.WithTimeout(ctx, time.Second)
					conn.Write(wctx, websocket.MessageText, b)
					cancel()
					continue
				default:
				}
				break
			}
			if reason, ok := closeReasons[cl.why]; ok { // hub.go
				conn.Close(websocket.StatusCode(cl.why), reason)
			} else {
				conn.CloseNow()
			}
			return
		case <-ctx.Done():
			return
		}
	}
}
