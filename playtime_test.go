package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePlaySite is the site: it adds each batch once by id, and fails while down (lose: it adds, then
// the answer is lost).
type fakePlaySite struct {
	mu     sync.Mutex
	down   bool
	lose   bool
	posts  []playBatch
	seen   map[string]bool
	total  map[int64]int64
	last   map[int64]int64
	posted chan string
}

func (s *fakePlaySite) post(b playBatch) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return false
	}
	s.posts = append(s.posts, b)
	if s.seen == nil {
		s.seen, s.total, s.last = map[string]bool{}, map[int64]int64{}, map[int64]int64{}
	}
	if !s.seen[b.ID] {
		s.seen[b.ID] = true
		for _, e := range b.Players {
			s.total[e.User] += e.Seconds
			s.last[e.User] = max(s.last[e.User], e.Last)
		}
	}
	if s.posted != nil {
		select {
		case s.posted <- b.ID:
		default:
		}
	}
	if s.lose {
		s.lose = false
		return false
	}
	return true
}

func (s *fakePlaySite) totals() map[int64]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int64]int64{}
	for k, v := range s.total {
		out[k] = v
	}
	return out
}

// playRig is a hub with the play time module, a manual clock and timers run by advance.
type playRig struct {
	t      *testing.T
	hub    *Hub
	pt     *playTime
	now    time.Time
	timers []playTimer
}

type playTimer struct {
	at      time.Time
	fn      func()
	stopped *bool
}

func newPlayRig(t *testing.T, file string, site playSink, run bool) *playRig {
	t.Helper()
	r := &playRig{t: t, now: time.Unix(1_800_000_000, 0)}
	savedF, savedS, savedR, saved := playFileForTest, playSinkForTest, playRunForTest, modules
	playFileForTest, playSinkForTest, playRunForTest = file, site, run
	modules = []moduleFactory{func(h *Hub) Module { r.pt = newPlayTime(h).(*playTime); return r.pt }}
	r.hub = NewHub(func() time.Time { return r.now })
	playFileForTest, playSinkForTest, playRunForTest, modules = savedF, savedS, savedR, saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		stopped := new(bool)
		r.timers = append(r.timers, playTimer{at: r.now.Add(d), fn: fn, stopped: stopped})
		return func() bool { was := !*stopped; *stopped = true; return was }
	}
	return r
}

// advance moves the clock on by d, running each timer as its time comes.
func (r *playRig) advance(d time.Duration) {
	end := r.now.Add(d)
	for {
		sort.SliceStable(r.timers, func(i, j int) bool { return r.timers[i].at.Before(r.timers[j].at) })
		if len(r.timers) == 0 || r.timers[0].at.After(end) {
			break
		}
		tm := r.timers[0]
		r.timers = r.timers[1:]
		if *tm.stopped {
			continue
		}
		r.now = tm.at
		tm.fn()
	}
	r.now = end
}

func (r *playRig) do(cl *Client, kind string) {
	b, _ := json.Marshal(map[string]any{"t": kind, "street": "GA5134UPLRU29OH", "x": 1, "y": 2})
	r.hub.Handle(cl, b)
}

func (r *playRig) game(id int64) *Client {
	return r.hub.Connect(Claims{Sub: id, Name: "P" + string(rune('A'+id%26))})
}

func (r *playRig) waiting(id int64) int64 { return r.pt.waiting()[id] }

func TestPlayTimeCountsActiveTimeFromSignInToClose(t *testing.T) {
	site := &fakePlaySite{}
	r := newPlayRig(t, "", site, false)
	cl := r.game(7)
	for i := 0; i < 3; i++ {
		r.advance(time.Minute)
		r.do(cl, "move")
	}
	r.advance(30 * time.Second)
	r.hub.Disconnect(cl)
	if got := r.waiting(7); got != 210 {
		t.Fatalf("waiting %d s, want 210", got)
	}
	if !r.pt.flush() {
		t.Fatal("flush failed")
	}
	if got := site.totals()[7]; got != 210 {
		t.Fatalf("site has %d s, want 210", got)
	}
	if last := site.last[7]; last != r.now.Unix() {
		t.Fatalf("last %d, want the close %d", last, r.now.Unix())
	}
	if len(r.pt.sessions) != 0 || r.waiting(7) != 0 {
		t.Fatalf("left behind: sessions %d, waiting %d", len(r.pt.sessions), r.waiting(7))
	}
}

func TestPlayTimeLeavesIdleStretchesOut(t *testing.T) {
	r := newPlayRig(t, "", &fakePlaySite{}, false)
	cl := r.game(7)
	r.advance(20 * time.Minute) // idle after presenceIdle: 5 minutes count
	if got := r.waiting(7); got != 300 {
		t.Fatalf("after 20 idle minutes %d s waiting, want 300", got)
	}
	r.do(cl, "active") // back: the idle 15 minutes are not counted
	r.advance(time.Minute)
	r.do(cl, "chat")
	r.advance(90 * time.Second)
	r.hub.Disconnect(cl)
	if got := r.waiting(7); got != 300+60+90 {
		t.Fatalf("waiting %d s, want %d", got, 300+60+90)
	}
	// A standing avatar's idle animations are no input: a move that changes only the animation.
	cl = r.game(7)
	r.hub.Handle(cl, []byte(`{"t":"join","street":"GA5134UPLRU29OH","x":10,"y":-20,"dir":1}`))
	for _, anim := range []string{"idle0", "idle1", "sleepStart", "sleepLoop", "idle0", "sleepLoop", "idle1", "sleepLoop", "idle0", "sleepLoop"} {
		r.advance(time.Minute)
		r.hub.Handle(cl, []byte(`{"t":"move","x":10,"y":-20,"dir":1,"anim":"`+anim+`"}`))
	}
	r.hub.Handle(cl, []byte(`{"t":"move","x":10,"y":-20,"dir":-1,"anim":"stand"}`)) // turning round is input
	r.advance(time.Minute)
	r.hub.Disconnect(cl)
	if got := r.waiting(7); got != 450+300+60 {
		t.Fatalf("idle animations kept the player active: %d s, want %d", got, 450+300+60)
	}
	// Frames that are no input (ping, the lists a game sends by itself) do not keep a player active.
	cl = r.game(7)
	for i := 0; i < 10; i++ {
		r.advance(time.Minute)
		r.do(cl, "ping")
	}
	r.hub.Disconnect(cl)
	if got := r.waiting(7); got != 810+300 {
		t.Fatalf("pings kept the player active: %d s, want %d", got, 810+300)
	}
}

func TestPlayTimeCountsSeveralGameSocketsOnce(t *testing.T) {
	site := &fakePlaySite{}
	r := newPlayRig(t, "", site, false)
	a := r.game(7)
	r.advance(time.Minute)
	b := r.game(7)  // a second tab replaces the first: the same session
	r.do(a, "move") // the replaced socket is closed: its frames do nothing
	r.hub.Disconnect(a)
	r.advance(time.Minute)
	r.do(b, "move")
	r.advance(time.Minute)
	r.hub.Disconnect(b)
	if got := r.waiting(7); got != 180 {
		t.Fatalf("two tabs over 3 minutes: %d s, want 180", got)
	}
}

// G526: the game's "active" keeps a player from going idle and gets no answer; it used to get bad_frame,
// which the game's street objects read as a server without them when a street's snapshot was still to come.
func TestActiveOnAGameSocketCountsAsAnInputAndAnswersNothing(t *testing.T) {
	r := newPlayRig(t, "", &fakePlaySite{}, false)
	cl := r.game(7)
	drain(cl)
	r.advance(4 * time.Minute)
	r.do(cl, "active")
	r.advance(4 * time.Minute)
	if e := kinds(drain(cl), "error"); len(e) != 0 {
		t.Fatalf("active answered %v", e)
	}
	r.hub.Disconnect(cl)
	if got := r.waiting(7); got != 8*60 {
		t.Fatalf("waiting %d s, want %d: without the input only 5 minutes count", got, 8*60)
	}
}

func TestPlayTimeIgnoresWebSockets(t *testing.T) {
	r := newPlayRig(t, "", &fakePlaySite{}, false)
	web := r.hub.ConnectWeb(Claims{Sub: 9, Name: "Bo", Via: "web"})
	for i := 0; i < 10; i++ {
		r.advance(time.Minute)
		r.do(web, "active")
	}
	r.hub.DisconnectWeb(web)
	if got := r.waiting(9); got != 0 || len(r.pt.sessions) != 0 {
		t.Fatalf("a /chat/ socket counted %d s (sessions %d)", got, len(r.pt.sessions))
	}
	// A player in the game and on /chat/: the page's inputs do not keep the game session active.
	cl := r.game(7)
	web = r.hub.ConnectWeb(Claims{Sub: 7, Name: "Ada", Via: "web"})
	for i := 0; i < 12; i++ {
		r.advance(time.Minute)
		r.do(web, "active")
	}
	r.hub.Disconnect(cl)
	r.hub.DisconnectWeb(web)
	if got := r.waiting(7); got != 300 {
		t.Fatalf("web inputs during a game session: %d s, want 300", got)
	}
}

func TestPlayTimeSendsBatchesEveryMinuteAndOnDisconnect(t *testing.T) {
	site := &fakePlaySite{}
	r := newPlayRig(t, "", site, false)
	a, b := r.game(7), r.game(9)
	r.advance(time.Minute) // the tick hands both players' minute over
	if w := r.pt.waiting(); w[7] != 60 || w[9] != 60 {
		t.Fatalf("after the tick %v", w)
	}
	r.pt.flush()
	if len(site.posts) != 1 || len(site.posts[0].Players) != 2 || !strings.HasPrefix(site.posts[0].ID, "pt-"+r.pt.boot+"-") {
		t.Fatalf("one batch of both players, got %+v", site.posts)
	}
	r.do(a, "move")
	r.do(b, "move")
	r.advance(20 * time.Second)
	r.hub.Disconnect(a)
	if w := r.pt.waiting(); w[7] != 20 || w[9] != 0 {
		t.Fatalf("a disconnect hands over that player's time only: %v", w)
	}
	select {
	case <-r.pt.wake:
	default:
		t.Fatal("a disconnect did not wake the sender")
	}
	r.pt.flush()
	r.advance(40 * time.Second)
	r.pt.flush()
	if got := site.totals(); got[7] != 80 || got[9] != 120 {
		t.Fatalf("site totals %v, want 7:80 9:120", got)
	}
	ids := map[string]bool{}
	for _, p := range site.posts {
		if ids[p.ID] {
			t.Fatalf("batch id %s used twice for new time", p.ID)
		}
		ids[p.ID] = true
	}
	// While the site is down the time waits, summed per player, and the tick stops with the last session.
	site.down = true
	r.advance(3 * time.Minute)
	r.pt.flush()
	r.hub.Disconnect(b)
	if w := r.pt.waiting(); w[9] != 180 {
		t.Fatalf("down: waiting %v, want 9:180", w)
	}
	if len(r.pt.sessions) != 0 || r.pt.tickStop != nil {
		t.Fatal("the tick runs with nobody playing")
	}
	// At most playBatchMax players a batch.
	r.pt.mu.Lock()
	for id := int64(1000); id < 1000+playBatchMax+5; id++ {
		r.pt.addPendingLocked(playEntry{User: id, Seconds: 1, Last: 1})
	}
	r.pt.mu.Unlock()
	site.down = false
	n := len(site.posts)
	if !r.pt.flush() {
		t.Fatal("flush failed")
	}
	for _, p := range site.posts[n:] {
		if len(p.Players) > playBatchMax {
			t.Fatalf("a batch of %d players", len(p.Players))
		}
	}
	if len(site.posts)-n < 2 || len(r.pt.waiting()) != 0 {
		t.Fatalf("%d batches, %d players left", len(site.posts)-n, len(r.pt.waiting()))
	}
}

func TestPlayTimeResendsABatchWithItsIDAndNeverAddsTwice(t *testing.T) {
	site := &fakePlaySite{}
	r := newPlayRig(t, "", site, false)
	cl := r.game(7)
	r.advance(time.Minute)
	site.lose = true // the site adds the batch, and its answer is lost
	if r.pt.flush() {
		t.Fatal("a lost answer read as taken")
	}
	r.do(cl, "move")
	r.advance(30 * time.Second)
	r.hub.Disconnect(cl) // new time while the batch is in flight waits for the next one
	if !r.pt.flush() {
		t.Fatal("flush failed")
	}
	if len(site.posts) != 3 || site.posts[0].ID != site.posts[1].ID || site.posts[2].ID == site.posts[0].ID {
		t.Fatalf("posts %+v: want the batch twice with one id, then a new one", site.posts)
	}
	if got := site.totals()[7]; got != 90 {
		t.Fatalf("site total %d, want 90 (the resend added nothing)", got)
	}
}

func TestPlayTimeKeepsWhatWaitsAcrossARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "realtime-play-time.json")
	site := &fakePlaySite{down: true}
	r := newPlayRig(t, file, site, false)
	cl := r.game(7)
	r.game(9)
	r.advance(time.Minute)
	r.pt.flush() // the batch is made and kept on disk before it is sent; the site is down
	r.do(cl, "move")
	r.advance(45 * time.Second)
	r.hub.Shutdown() // SIGTERM: everyone's time up to now, and the file
	var f playFile
	b, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(b, &f) != nil {
		t.Fatalf("state file: %v %s", err, b)
	}
	if f.Sent == nil || len(f.Sent.Players) != 2 || len(f.Pending) != 2 {
		t.Fatalf("file %s", b)
	}
	sentID := f.Sent.ID
	// The sockets close after the hooks ran: nothing is counted twice.
	r.hub.Disconnect(cl)
	if w := r.pt.waiting(); w[7] != 105 || w[9] != 105 {
		t.Fatalf("after shutdown and close %v, want 105 each", w)
	}

	site.down = false
	r2 := newPlayRig(t, file, site, false)
	if w := r2.pt.waiting(); w[7] != 105 || w[9] != 105 {
		t.Fatalf("after the restart %v, want 105 each", w)
	}
	if !r2.pt.flush() {
		t.Fatal("flush failed")
	}
	if got := site.totals(); got[7] != 105 || got[9] != 105 {
		t.Fatalf("site totals %v", got)
	}
	if site.posts[0].ID != sentID {
		t.Fatalf("the batch in flight was resent as %s, want %s", site.posts[0].ID, sentID)
	}
	// Done: the file holds nothing more.
	b, _ = os.ReadFile(file)
	var after playFile
	if json.Unmarshal(b, &after) != nil || after.Sent != nil || len(after.Pending) != 0 {
		t.Fatalf("file after the site took it all %s", b)
	}
	// A third start sends nothing again.
	r3 := newPlayRig(t, file, site, false)
	n := len(site.posts)
	r3.pt.flush()
	if len(site.posts) != n {
		t.Fatal("a restart resent what the site had taken")
	}
}

func TestPlayTimeShutdownCountsSessionsAndStopsTheSender(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pt.json")
	site := &fakePlaySite{posted: make(chan string, 8)}
	r := newPlayRig(t, file, site, true) // a real sender goroutine
	cl := r.game(7)
	r.advance(90 * time.Second) // the tick at 60 s pokes the sender
	select {
	case <-site.posted:
	case <-time.After(5 * time.Second):
		t.Fatal("the sender did not send the tick's batch")
	}
	site.mu.Lock()
	site.down = true // what comes next waits, in the file
	site.mu.Unlock()
	for deadline := time.Now().Add(5 * time.Second); r.pt.waiting()[7] != 0; {
		if time.Now().After(deadline) {
			t.Fatal("the sender did not finish the tick's batch")
		}
		time.Sleep(time.Millisecond)
	}
	r.advance(15 * time.Second)
	r.hub.Shutdown()
	select {
	case <-r.pt.done:
	default:
		t.Fatal("the sender still runs after shutdown")
	}
	var f playFile
	b, _ := os.ReadFile(file)
	if json.Unmarshal(b, &f) != nil {
		t.Fatalf("file %s", b)
	}
	kept := append([]playEntry(nil), f.Pending...)
	if f.Sent != nil {
		kept = append(kept, f.Sent.Players...) // the sender may have made the batch before it stopped
	}
	if len(kept) != 1 || kept[0].User != 7 || kept[0].Seconds != 45 || kept[0].Last != r.now.Unix() {
		t.Fatalf("file %s: want 45 s kept for 7", b)
	}
	if got := site.totals()[7]; got != 60 {
		t.Fatalf("site %d s, want the tick's 60", got)
	}
	r.hub.Disconnect(cl)
	if w := r.pt.waiting()[7]; w != 45 {
		t.Fatalf("a close after shutdown added time: %d", w)
	}
}

func TestPlayTimeCarriesPartsOfASecond(t *testing.T) {
	r := newPlayRig(t, "", &fakePlaySite{}, false)
	for i := 0; i < 4; i++ {
		cl := r.game(7)
		r.advance(1500 * time.Millisecond)
		r.hub.Disconnect(cl)
	}
	if got := r.waiting(7); got != 6 {
		t.Fatalf("four 1.5 s sessions: %d s, want 6", got)
	}
}

func TestPlayTimeOffWithoutASite(t *testing.T) {
	r := newPlayRig(t, "", nil, false)
	cl := r.game(7)
	r.advance(time.Minute)
	r.do(cl, "move")
	r.hub.Disconnect(cl)
	if len(r.pt.sessions) != 0 || len(r.pt.waiting()) != 0 || r.hub.ProvidedLocked("playtime.input") != nil {
		t.Fatal("measured with no site")
	}
}
