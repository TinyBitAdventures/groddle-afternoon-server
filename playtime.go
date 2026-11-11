package main

// Play time (Austin 2026-11-04: "A running total time stored in the user's
// meta data for how long they've played. No need to make that visible
// anywhere. I just want to collect that info for administrator purposes.").
// The remake's own; Glitch kept no such total.
//
// What counts: a character's time in the game, from its game socket's
// sign-in to the socket's close, while the player is active. The idle rule
// is presence.go's: a player is idle once presenceIdle has gone by without an
// input (the game's inputKinds frames, the "active" a key or a click sends),
// so each input counts the time after it up to the next input or
// presenceIdle, whichever comes first, and an idle stretch past that counts
// nothing. A "move" counts only when it moves the avatar (x, y or facing): one
// that changes only the animation is the standing avatar's own idle
// animations, which the game sends for minutes after the last key (QA on
// groddle.localhost counted about two idle minutes too many before this). The sign-in counts as an input. One session per character, as the
// hub keeps one game socket per character: a socket that replaces another
// (a second tab, a reload) goes on with the same session, so time is counted
// once. The website's /chat/ sockets (web.go) count nothing, and neither do
// their inputs. Times are differences of the hub's clock, time.Now in the
// server, whose readings carry Go's monotonic clock: a change of the wall
// clock moves no count. Only "last" is wall time (unix seconds, the end of the
// last stretch counted).
//
// What is sent, in signed batches (ledger.go's X-Groddle-Service), every
// playEvery while anyone plays, at once when a player's game socket goes, and
// again while the site does not answer (a backoff up to playBackoffMax):
//
//	POST <site>/wp-json/groddle/v1/play-time
//	     {"id":"pt-<process>-<n>","players":[{"user":7,"seconds":60,"last":1730000000},…]}   at most playBatchMax players
//	     200 {"done":"pt-…"}
//
// The site (groddle-afternoon-site src/PlayTime.php) adds each player's
// seconds to user meta groddle_play_seconds and keeps groddle_play_last, and
// keeps each batch id, so a batch sent again (a lost answer, a restart) adds
// nothing. One batch is in flight at a time and is sent with the same id until
// the site names it done; what is counted meanwhile waits in "pending",
// summed per player, for the next batch.
//
// Restarts lose nothing: the batch in flight and the pending seconds are kept
// in a state file (-play-time-file, default realtime-play-time.json beside
// -secret-file; off: memory only, for lane and QA servers), written within
// playSaveAfter of a change, before a batch is first sent, after the site
// answers, and at shutdown, when every session's time up to then is counted
// first (a restart starts each session again on the player's next sign-in). A
// crash (no shutdown hook) loses at most the last playEvery of the players
// online then. -play-time off measures and sends nothing; with no -ledger-url
// there is nowhere to send, so it is off too.
//
// The world's tallies ride along (tally.go, for the console's Analytics tab):
// a batch may carry "counts", per UTC day the acts that landed, everyone's
// together and nobody named, {"2026-11-11":{"pig:nibble":3,"rock:mine":40}}, and
// a batch with counts and no players is sent too. They wait, are kept in the
// state file and are taken once by the batch id with the seconds, so a tally
// is never added twice either. A day keeps at most tallyDayKeys keys.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

const (
	playRoute      = "/groddle/v1/play-time"
	playEvery      = time.Minute
	playIdle       = presenceIdle
	playBatchMax   = 500
	playBackoffMin = 5 * time.Second
	playBackoffMax = 5 * time.Minute
	playSaveAfter  = time.Second
	playStopWait   = 2 * time.Second // shutdown waits this long for a send under way, then saves
)

var (
	playTimeFlag     = flag.String("play-time", "on", "on: each player's active time in the game goes to the site's user meta (needs -ledger-url); off: nothing is measured or sent")
	playTimeFileFlag = flag.String("play-time-file", "", "file that keeps play time the site has not taken yet (default: realtime-play-time.json beside -secret-file; off: memory only)")

	// Under go test the flags are ignored: a test gives the sink and the file, and sends with flush
	// itself (no sender goroutine) unless playRunForTest.
	playFileForTest string
	playSinkForTest playSink
	playRunForTest  bool
)

// playEntry is one player's seconds in a batch (or waiting for one).
type playEntry struct {
	User    int64 `json:"user"`
	Seconds int64 `json:"seconds"`
	Last    int64 `json:"last"`
}

type playBatch struct {
	ID      string      `json:"id"`
	Players []playEntry `json:"players"`
	Counts  playCounts  `json:"counts,omitempty"`
}

// playCounts is the world's tallies by UTC day ("2006-01-02") and key (tally.go).
type playCounts map[string]map[string]int64

// add puts another's tallies into c (a day full at tallyDayKeys takes no new key).
func (c playCounts) add(o playCounts) {
	for day, keys := range o {
		for k, n := range keys {
			c.addOne(day, k, n)
		}
	}
}

func (c playCounts) addOne(day, key string, n int64) {
	if n <= 0 {
		return
	}
	d := c[day]
	if d == nil {
		d = map[string]int64{}
		c[day] = d
	}
	if _, ok := d[key]; !ok && len(d) >= tallyDayKeys {
		return
	}
	d[key] += n
}

func (c playCounts) clone() playCounts {
	if len(c) == 0 {
		return nil
	}
	out := playCounts{}
	out.add(c)
	return out
}

// playSink is the site. post answers true once the site has added the batch, now or before.
type playSink interface {
	post(b playBatch) bool
}

type playFile struct {
	V       int         `json:"v"`
	Sent    *playBatch  `json:"sent,omitempty"`
	Pending []playEntry `json:"pending"`
	Counts  playCounts  `json:"counts,omitempty"`
}

// playSession is one character's game session (under the hub lock).
type playSession struct {
	input   time.Time     // the last input, or the sign-in
	counted time.Time     // counted up to here
	acc     time.Duration // counted, not yet handed to the outbox (under a second, or since the last tick)
}

type playTime struct {
	h        *Hub
	sink     playSink
	path     string
	boot     string
	sessions map[int64]*playSession
	carry    map[int64]time.Duration // a gone session's part of a second, for its next
	tickStop func() bool

	mu       sync.Mutex // the outbox: taken under the hub lock by the hooks, and by the sender without it
	pending  map[int64]playEntry
	counts   playCounts // the world's tallies not yet in a batch (tally.go)
	sent     *playBatch
	seq      uint64
	saving   bool
	saveMu   sync.Mutex // one write of the file at a time, each of the state as it was when it began
	final    bool       // under saveMu: shutdown wrote the file for the last time
	stop     chan struct{}
	wake     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func init() { modules = append(modules, newPlayTime) }

func newPlayTime(h *Hub) Module {
	var b [4]byte
	rand.Read(b[:])
	m := &playTime{h: h, boot: hex.EncodeToString(b[:]), sessions: map[int64]*playSession{}, carry: map[int64]time.Duration{},
		pending: map[int64]playEntry{}, counts: playCounts{}, stop: make(chan struct{}), wake: make(chan struct{}, 1), done: make(chan struct{})}
	if testing.Testing() {
		m.sink = playSinkForTest
	} else if *playTimeFlag != "off" {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.sink = &httpPlaySink{l: l}
				log.Printf("play time: players' active time goes to %s", l.base)
			}
		}
	}
	if m.sink == nil {
		close(m.done)
		return m
	}
	if m.path = statePath(playFileForTest, playTimeFileFlag, "realtime-play-time.json"); m.path != "" {
		var f playFile
		if ok, err := readStateFile("play time", m.path, &f); err != nil {
			log.Printf("play time %s: %v (kept in memory only)", m.path, err)
			m.path = ""
		} else if ok {
			if f.Sent != nil && f.Sent.ID != "" && (len(f.Sent.Players) > 0 || len(f.Sent.Counts) > 0) {
				if f.Sent.Players == nil {
					f.Sent.Players = []playEntry{}
				}
				m.sent = f.Sent
			}
			for _, e := range f.Pending {
				m.addPendingLocked(e)
			}
			m.counts.add(f.Counts)
			if m.sent != nil || len(m.pending) > 0 {
				log.Printf("play time %s: %d players' seconds from before the restart to send", m.path, len(m.pending)+m.sentPlayers())
			}
		}
	}
	if testing.Testing() && !playRunForTest {
		close(m.done)
		return m
	}
	go m.run()
	return m
}

func (m *playTime) Register(r *Registry) {
	if m.sink == nil {
		return
	}
	r.OnConnect(m.connected)
	r.OnDisconnect(m.disconnected)
	r.OnShutdown(m.shutdown)
	r.Provide("playtime.input", m.input)
	r.Provide("playtime.count", m.count)
}

// count adds n to today's (UTC, the hub's clock) tally of key, for the next batch (Hub.tallyLocked).
func (m *playTime) count(key string, n int64) {
	day := m.h.Now().UTC().Format("2006-01-02")
	m.mu.Lock()
	m.counts.addOne(day, key, n)
	m.saveSoonLocked()
	m.mu.Unlock()
}

// tallied is the tallies not yet taken by the site, sent or waiting (tests).
func (m *playTime) tallied() playCounts {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.counts.clone()
	if out == nil {
		out = playCounts{}
	}
	if m.sent != nil {
		out.add(m.sent.Counts)
	}
	return out
}

// playInputLocked tells the module a game socket sent an input (hub.go Handle). Under the hub lock.
func (h *Hub) playInputLocked(cl *Client) {
	if fn, ok := h.provided["playtime.input"].(func(*Client)); ok {
		fn(cl)
	}
}

// —— Sessions (under the hub lock) ——

// connected starts a session at the sign-in; a socket that replaces another goes on with its session,
// and the sign-in counts as an input.
func (m *playTime) connected(cl *Client) {
	now := m.h.Now()
	if s := m.sessions[cl.ID()]; s != nil {
		m.touch(s, now)
	} else {
		m.sessions[cl.ID()] = &playSession{input: now, counted: now, acc: m.carry[cl.ID()]}
		delete(m.carry, cl.ID())
	}
	if m.tickStop == nil {
		m.tickStop = m.h.After(playEvery, m.tick)
	}
}

// input is a game socket's input: what came before it counts (up to playIdle), and it starts a new stretch.
func (m *playTime) input(cl *Client) {
	if s := m.sessions[cl.ID()]; s != nil {
		m.touch(s, m.h.Now())
	}
}

func (m *playTime) touch(s *playSession, now time.Time) {
	accrue(s, now)
	s.input, s.counted = now, now
}

// accrue counts a session's active time up to now: from where it was counted to its last input plus
// playIdle, never past now.
func accrue(s *playSession, now time.Time) {
	end := s.input.Add(playIdle)
	if now.Before(end) {
		end = now
	}
	if end.After(s.counted) {
		s.acc += end.Sub(s.counted)
		s.counted = end
	}
}

// disconnected ends a session (the character's game socket went, not replaced) and sends its time.
func (m *playTime) disconnected(cl *Client) {
	s := m.sessions[cl.ID()]
	if s == nil {
		return
	}
	delete(m.sessions, cl.ID())
	accrue(s, m.h.Now())
	m.hand(cl.ID(), s)
	if s.acc > 0 {
		m.carry[cl.ID()] = s.acc
	}
	if len(m.sessions) == 0 && m.tickStop != nil {
		m.tickStop()
		m.tickStop = nil
	}
	m.poke()
}

// tick hands every session's time since the last tick to the outbox.
func (m *playTime) tick() {
	m.tickStop = nil
	now := m.h.Now()
	for id, s := range m.sessions {
		accrue(s, now)
		m.hand(id, s)
	}
	if len(m.sessions) > 0 {
		m.tickStop = m.h.After(playEvery, m.tick)
	}
	m.poke()
}

// hand moves a session's whole seconds into the outbox (its part of a second stays).
func (m *playTime) hand(id int64, s *playSession) {
	secs := int64(s.acc / time.Second)
	if secs <= 0 {
		return
	}
	s.acc -= time.Duration(secs) * time.Second
	m.mu.Lock()
	m.addPendingLocked(playEntry{User: id, Seconds: secs, Last: s.counted.Unix()})
	m.saveSoonLocked()
	m.mu.Unlock()
}

// shutdown counts every session up to now, keeps it all in the state file and stops the sender.
func (m *playTime) shutdown() {
	now := m.h.Now()
	for id, s := range m.sessions {
		accrue(s, now)
		m.hand(id, s)
	}
	m.sessions = map[int64]*playSession{}
	if m.tickStop != nil {
		m.tickStop()
		m.tickStop = nil
	}
	m.stopOnce.Do(func() { close(m.stop) })
	select {
	case <-m.done:
	case <-time.After(playStopWait):
	}
	m.save()
	m.saveMu.Lock()
	m.final = true
	m.saveMu.Unlock()
}

// —— The outbox ——

func (m *playTime) addPendingLocked(e playEntry) {
	if e.User <= 0 || e.Seconds <= 0 {
		return
	}
	p := m.pending[e.User]
	p.User = e.User
	p.Seconds += e.Seconds
	p.Last = max(p.Last, e.Last)
	m.pending[e.User] = p
}

func (m *playTime) sentPlayers() int {
	if m.sent == nil {
		return 0
	}
	return len(m.sent.Players)
}

// waiting is the seconds not yet taken by the site, per player (tests).
func (m *playTime) waiting() map[int64]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int64]int64{}
	for id, e := range m.pending {
		out[id] += e.Seconds
	}
	if m.sent != nil {
		for _, e := range m.sent.Players {
			out[e.User] += e.Seconds
		}
	}
	return out
}

func (m *playTime) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *playTime) saveSoonLocked() {
	if m.path == "" || m.saving {
		return
	}
	m.saving = true
	time.AfterFunc(playSaveAfter, func() {
		m.mu.Lock()
		m.saving = false
		m.mu.Unlock()
		m.save()
	})
}

func (m *playTime) save() {
	if m.path == "" {
		return
	}
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	if m.final {
		return // a send that ended after shutdown: the file keeps its batch, which the site dedupes
	}
	m.mu.Lock()
	f := playFile{V: 1, Pending: make([]playEntry, 0, len(m.pending)), Counts: m.counts.clone()}
	if m.sent != nil {
		b := *m.sent
		b.Players = append([]playEntry(nil), m.sent.Players...)
		b.Counts = m.sent.Counts.clone()
		f.Sent = &b
	}
	for _, e := range m.pending {
		f.Pending = append(f.Pending, e)
	}
	m.mu.Unlock()
	sort.Slice(f.Pending, func(i, j int) bool { return f.Pending[i].User < f.Pending[j].User })
	b, err := json.Marshal(f)
	if err == nil {
		err = writeFileSynced(m.path, b)
	}
	if err != nil {
		log.Printf("play time %s: %v", m.path, err)
	}
}

// flush sends batches until nothing waits; false when the site did not take one (it stays, to be sent
// again with the same id).
func (m *playTime) flush() bool {
	for {
		m.mu.Lock()
		fresh := false
		if m.sent == nil {
			if len(m.pending) == 0 && len(m.counts) == 0 {
				m.mu.Unlock()
				return true
			}
			ids := make([]int64, 0, len(m.pending))
			for id := range m.pending {
				ids = append(ids, id)
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			ids = ids[:min(len(ids), playBatchMax)]
			m.seq++
			b := &playBatch{ID: "pt-" + m.boot + "-" + strconv.FormatUint(m.seq, 36), Players: make([]playEntry, 0, len(ids))}
			for _, id := range ids {
				b.Players = append(b.Players, m.pending[id])
				delete(m.pending, id)
			}
			if len(m.counts) > 0 {
				b.Counts, m.counts = m.counts, playCounts{}
			}
			m.sent, fresh = b, true
		}
		b := *m.sent
		m.mu.Unlock()
		if fresh {
			m.save() // on disk before the site sees it: a crash after the site took it resends the same id
		}
		if !m.sink.post(b) {
			return false
		}
		m.mu.Lock()
		if m.sent != nil && m.sent.ID == b.ID {
			m.sent = nil
		}
		m.mu.Unlock()
		m.save()
	}
}

// run sends what waits whenever the module is poked (a tick, a disconnect), at once at start (what a
// restart kept), and again after a backoff while the site does not answer, until shutdown.
func (m *playTime) run() {
	defer close(m.done)
	backoff := playBackoffMin
	for {
		if m.flush() {
			backoff = playBackoffMin
			select {
			case <-m.stop:
				return
			case <-m.wake:
			}
			continue
		}
		log.Printf("play time: the site did not take a batch (again in %s)", backoff)
		t := time.NewTimer(backoff)
		select {
		case <-m.stop:
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(2*backoff, playBackoffMax)
	}
}

// —— The site ——

type httpPlaySink struct {
	l *httpLedger
}

func (s *httpPlaySink) post(b playBatch) bool {
	body, err := json.Marshal(b)
	if err != nil {
		return false
	}
	code, raw, err := s.l.raw(playRoute, body)
	if err != nil {
		log.Printf("play time: %v", err)
		return false
	}
	var a struct {
		Done string `json:"done"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &a) != nil || a.Done != b.ID {
		log.Printf("play time: HTTP %d: %.200s", code, raw)
		return false
	}
	return true
}
