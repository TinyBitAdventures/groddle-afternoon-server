package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The 2026-11-11 reconnect loop: a player owed 68 project messages (55 project_given, 13
// project_reward) was sent all of them on every project_sync, the send queue (sendBuffer 64)
// overflowed, the socket closed with 4008 and the game reconnected every few seconds, forever.

// seedOwed writes a projects state with n messages owed to player (the first given, the last
// rewards, as the live player's were) and points the module at it. It returns the ids in order.
func seedOwed(t *testing.T, player int64, n, rewards int) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projects.json")
	var owed []map[string]any
	var ids []string
	for i := 0; i < n; i++ {
		var msg map[string]any
		if i >= n-rewards {
			id := fmt.Sprintf("job-r-test-%03d", i)
			msg = map[string]any{"t": "project_reward", "id": id, "job": "222", "phase": 0, "cls": "job_standard6_for_new_streets", "final": false, "share": 0.5, "participation": 50,
				"rewards": map[string]any{"xp": 10}, "performance": map[string]any{}, "winner": false, "top5": false, "phases": 1, "option": "", "readyAt": 0}
		} else {
			id := fmt.Sprintf("job-g-test-%03d", i)
			msg = map[string]any{"t": "project_given", "id": id, "job": "222", "req": "r152", "cls": "job_standard6_for_new_streets", "phase": 1, "items": map[string]int{"sparkle_powder": 1}, "currants": 0, "cost": 450}
		}
		ids = append(ids, msg["id"].(string))
		owed = append(owed, map[string]any{"id": msg["id"], "msg": msg})
	}
	b, _ := json.Marshal(map[string]any{"jobs": map[string]any{}, "rest": map[string]any{}, "gives": map[string]any{}, "credits": map[string]any{}, "owed": map[string]any{fmt.Sprint(player): owed}, "seq": 1})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	projectsFileForTest = path
	t.Cleanup(func() { projectsFileForTest = "" })
	return ids
}

// read is the next frame, or the error that ended the socket.
func (p *peer) read(d time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, b, err := p.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m, nil
}

func isOwedKind(m map[string]any) bool {
	return m["t"] == "project_given" || m["t"] == "project_reward"
}

// untilPong reads up to the pong for a ping sent now: everything the server queued before it.
func (p *peer) untilPong() []map[string]any {
	p.t.Helper()
	p.send(map[string]any{"t": "ping"})
	var out []map[string]any
	for {
		m, err := p.read(2 * time.Second)
		if err != nil {
			p.t.Fatalf("waiting for pong: %v (code %d)", err, websocket.CloseStatus(err))
		}
		if m["t"] == "pong" {
			return out
		}
		out = append(out, m)
	}
}

func TestOwedProjectMessagesGoInBatchesAndDrainWithAcks(t *testing.T) {
	for _, tc := range []struct{ n, rewards int }{{68, 13}, {200, 40}} {
		t.Run(fmt.Sprint(tc.n), func(t *testing.T) {
			ids := seedOwed(t, 500, tc.n, tc.rewards)
			url := startServer(t)
			p := signIn(t, url, 500, "Granberry")
			p.send(map[string]any{"t": "project_sync"})

			// The sync's answer: the world, the featured project and the oldest owedBatch, no more.
			var first []string
			for _, m := range p.untilPong() {
				if isOwedKind(m) {
					first = append(first, m["id"].(string))
				}
			}
			if len(first) != owedBatch || strings.Join(first, ",") != strings.Join(ids[:owedBatch], ",") {
				t.Fatalf("the sync sent %d owed (%v), want the oldest %d", len(first), first, owedBatch)
			}

			// Each ack brings the next; every message arrives once, in order, and the socket stays open.
			got := append([]string{}, first...)
			for _, id := range first {
				p.send(map[string]any{"t": "project_ack", "id": id})
			}
			for len(got) < tc.n {
				m, err := p.read(2 * time.Second)
				if err != nil {
					t.Fatalf("after %d of %d: %v (close code %d)", len(got), tc.n, err, websocket.CloseStatus(err))
				}
				if !isOwedKind(m) {
					continue
				}
				id := m["id"].(string)
				got = append(got, id)
				p.send(map[string]any{"t": "project_ack", "id": id})
			}
			if strings.Join(got, ",") != strings.Join(ids, ",") {
				t.Fatalf("delivered out of order or twice:\n%v\nwant\n%v", got, ids)
			}
			for _, m := range p.untilPong() {
				if isOwedKind(m) {
					t.Fatalf("sent again after its ack: %v", m)
				}
			}

			// Nothing is owed now: a new socket's sync sends none.
			p.conn.Close(websocket.StatusNormalClosure, "")
			q := signIn(t, url, 500, "Granberry")
			q.send(map[string]any{"t": "project_sync"})
			for _, m := range q.untilPong() {
				if isOwedKind(m) {
					t.Fatalf("still owed after every ack: %v", m)
				}
			}
		})
	}
}

func TestOwedSyncStartsTheWindowOverAndAMessageNeverAckedHoldsOnlyItsSlot(t *testing.T) {
	ids := seedOwed(t, 501, 40, 0)
	url := startServer(t)
	p := signIn(t, url, 501, "Bo")
	p.send(map[string]any{"t": "project_sync"})
	if n := len(p.untilPong()); n < owedBatch {
		t.Fatalf("first window %d frames", n)
	}
	// Ack all but the oldest: the window moves on past it.
	for _, id := range ids[1:owedBatch] {
		p.send(map[string]any{"t": "project_ack", "id": id})
	}
	var next []string
	for _, m := range p.untilPong() {
		if isOwedKind(m) {
			next = append(next, m["id"].(string))
		}
	}
	if strings.Join(next, ",") != strings.Join(ids[owedBatch:2*owedBatch-1], ",") {
		t.Fatalf("after 15 acks: %v", next)
	}
	// A sync (a street change, a reconnect) starts over from the oldest still owed.
	p.send(map[string]any{"t": "project_sync"})
	var again []string
	for _, m := range p.untilPong() {
		if isOwedKind(m) {
			again = append(again, m["id"].(string))
		}
	}
	want := append([]string{ids[0]}, ids[owedBatch:2*owedBatch-1]...)
	if strings.Join(again, ",") != strings.Join(want, ",") {
		t.Fatalf("the sync sent %v, want %v", again, want)
	}
}

func TestOwedWindowWaitsForRoomInTheSendQueue(t *testing.T) {
	h := NewHub(nil)
	h.mu.Lock()
	defer h.mu.Unlock()
	cl := &Client{id: 9, name: "Cy", send: make(chan []byte, sendBuffer), done: make(chan struct{})}
	var later []func()
	h.afterFunc = func(d time.Duration, fn func()) func() bool {
		later = append(later, fn)
		return func() bool { return true }
	}
	var items []owedItem
	for i := 0; i < 30; i++ {
		items = append(items, owedItem{ID: fmt.Sprintf("job-g-%d", i), Msg: json.RawMessage(`{"t":"project_given"}`)})
	}
	for i := 0; i < sendBuffer-owedReserve-4; i++ {
		h.queueLocked(cl, map[string]any{"t": "other"})
	}
	p := newOwedPacer(h, func(int64) []owedItem { return items })
	p.start(cl, nil)
	if len(cl.send) != sendBuffer-owedReserve || cl.closed {
		t.Fatalf("queued %d (closed %v), want the window to stop at %d free", len(cl.send), cl.closed, owedReserve)
	}
	if len(later) != 1 {
		t.Fatalf("%d later tries", len(later))
	}
	for len(cl.send) > 0 {
		<-cl.send
	}
	h.mu.Unlock()
	later[0]() // the hub's After runs it under the lock
	h.mu.Lock()
	if len(cl.send) != owedBatch-4 {
		t.Fatalf("the later try sent %d, want the rest of the window", len(cl.send))
	}
}

// —— Close lines ——

// logBuf is the log's output in a test, safe to read while the socket goroutines write it.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuf) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(b)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func captureLog(t *testing.T) *logBuf {
	t.Helper()
	buf := &logBuf{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

// waitLog waits for a line containing want (the socket's goroutines log after the client sees the close).
func waitLog(t *testing.T, buf *logBuf, want ...string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(buf.String(), "\n") {
			ok := true
			for _, w := range want {
				ok = ok && strings.Contains(line, w)
			}
			if ok {
				return line
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no log line with %q in:\n%s", want, buf.String())
	return ""
}

func TestCloseLinesNameTheCodeAndWhy(t *testing.T) {
	buf := captureLog(t)
	before := healthDisconnects.Load()

	// Too far behind: the hub's close, with the queue and the last kinds sent.
	h := NewHub(nil)
	cl := h.Connect(Claims{Sub: 31, Name: "Slow"})
	h.mu.Lock()
	for i := 0; i <= sendBuffer; i++ {
		h.queueLocked(cl, map[string]any{"t": "project_given", "id": i})
	}
	h.mu.Unlock()
	h.CloseNote(cl, 1006, "")
	line := waitLog(t, buf, "realtime: close player=31")
	for _, want := range []string{`name="Slow"`, "via=game", "code=4008", `reason="too far behind"`, "queue=64", "last=project_given,project_given,project_given,project_given", "after="} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q: %s", want, line)
		}
	}

	// Over the server: a frame over the read limit, a client's own close, a refused ticket.
	url := startServer(t)
	p := signIn(t, url, 32, "Big")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.conn.Write(ctx, websocket.MessageText, []byte(`{"t":"chat","text":"`+strings.Repeat("a", maxFrame)+`"}`))
	p.closeCode()
	waitLog(t, buf, "realtime: close player=32", "code=1009", "read limit")

	q := signIn(t, url, 33, "Bye")
	q.conn.Close(websocket.StatusGoingAway, "tab closed")
	waitLog(t, buf, "realtime: close player=33", "code=1001", `reason="tab closed"`)

	r := dial(t, url)
	r.send(map[string]any{"t": "auth", "ticket": "nope"})
	if code := r.closeCode(); code != closeTicket {
		t.Fatalf("bad ticket closed %d", code)
	}
	waitLog(t, buf, "realtime: refused code=4003", "ticket")

	// Replaced: the old socket's line says so.
	a := signIn(t, url, 34, "Twice")
	signIn(t, url, 34, "Twice")
	if code := a.closeCode(); code != closeReplaced {
		t.Fatalf("replaced closed %d", code)
	}
	waitLog(t, buf, "realtime: close player=34", "code=4001")

	if healthDisconnects.Load()-before < 5 {
		t.Fatalf("health counted %d disconnects", healthDisconnects.Load()-before)
	}
}

func TestReconnectLoopWarnsOnceAMinuteAndHoldsBackItsCloseLines(t *testing.T) {
	buf := captureLog(t)
	now := time.Unix(1_700_000_000, 0)
	h := NewHub(func() time.Time { return now })
	loop := func(n int) {
		for i := 0; i < n; i++ {
			cl := h.Connect(Claims{Sub: 41, Name: "Loop"})
			h.mu.Lock()
			for j := 0; j <= sendBuffer; j++ {
				h.queueLocked(cl, map[string]any{"t": "project_given"})
			}
			h.mu.Unlock()
			h.CloseNote(cl, 1006, "")
			h.Disconnect(cl)
			now = now.Add(3500 * time.Millisecond)
		}
	}
	count := func(s string) int { return strings.Count(buf.String(), s) }

	loop(reconnectLoopMax)
	if count("reconnect loop") != 0 || count("realtime: close player=41") != reconnectLoopMax {
		t.Fatalf("five connects in a minute are no loop:\n%s", buf.String())
	}
	loop(10) // 15 in under a minute
	if count("reconnect loop") != 1 {
		t.Fatalf("want one warning:\n%s", buf.String())
	}
	line := waitLog(t, buf, "realtime: reconnect loop player=41")
	if !strings.Contains(line, `name="Loop"`) || !strings.Contains(line, "connects=6") || !strings.Contains(line, "last close code=4008") {
		t.Fatalf("warning: %s", line)
	}
	if count("realtime: close player=41") != reconnectLoopMax {
		t.Fatalf("close lines went on in the loop:\n%s", buf.String())
	}
	loop(20) // the next minute: one more warning, still no close lines
	if count("reconnect loop") != 2 || count("realtime: close player=41") != reconnectLoopMax {
		t.Fatalf("second minute:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "close lines held back") {
		t.Fatalf("no held-back count:\n%s", buf.String())
	}

	// Settled: a while later its closes log again.
	now = now.Add(5 * time.Minute)
	loop(1)
	if count("realtime: close player=41") != reconnectLoopMax+1 {
		t.Fatalf("after settling:\n%s", buf.String())
	}
}
