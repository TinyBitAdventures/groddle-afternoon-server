package main

// Long-term chat history on the site (Austin 2026-11-04: "persistent long-term
// chat history ... stored on the Minn site, with the realtime server staying
// the live relay"). This server still keeps what an instant join needs (a
// channel's last chanHistory lines, im.go's short IM history); every channel
// and group line, every IM line and every change to their reactions also goes
// to the site, which keeps them (groddle-afternoon-site src/ChatHistory.php,
// tables groddle_chat_lines and groddle_im_lines) and serves them to signed-in
// players: a channel's lines to who may open it, an IM conversation to its two
// players only. Local and party chat are not kept (as before). The 30-day chat
// log for moderation (chatlog.go) is separate and unchanged: it never has IMs.
//
// What is sent, in signed batches (ledger.go's X-Groddle-Service, the route
// /groddle/v1/chat/ingest) every chatStoreEvery while anything waits:
//
//	POST <site>/wp-json/groddle/v1/chat/ingest {"entries":[Entry…]}   200 {"status":"ok","stored":n}
//
//	{"k":"line","seq":1,"room":"global","lid":1730000000123,"id":7,"name":"Ada","text":"hi","at":1730000000000,"web":true}
//	{"k":"react","seq":2,"lid":1730000000123,"r":[{"e":"heart","by":[7,9]}]}          the line's reactions now (all of them)
//	{"k":"im","seq":3,"a":7,"b":9,"line":12,"from":7,"text":"hi","at":1730000000000}  a and b the pair, a < b
//	{"k":"im_react","seq":4,"a":7,"b":9,"line":12,"at":1730000000000,"r":[…]}           the IM line's reactions now
//
// room: global, help, newbie, trade or group:<id> (channels.go rooms). Every
// entry is idempotent at the site: a line by its lid (an IM line by its pair,
// line id and time), reactions as the whole set, so sending one again (a lost
// answer, a restart) changes nothing. The text is what everyone saw (the word
// filter's), never the words as typed.
//
// When the site does not answer, the entries wait and are sent later, in
// order: they are kept in a state file (-chat-store-file, default
// realtime-chat-store.json beside -secret-file, mode 0600 as it holds IM text;
// off: memory only, for lane and QA servers), written within chatStoreSaveAfter
// of a change and at shutdown; at most chatStoreMax wait (the oldest go first,
// logged). -chat-store off sends nothing (nor keeps anything); with no
// -ledger-url there is nowhere to send, so it is off too.
//
// Once, the first time a server runs this (the state file's "imported"), the
// IM lines im.go already keeps are sent too, so the conversations players
// have now are not lost; channel history is in memory only and a restart has
// already forgotten it, so channels start fresh.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"
)

const (
	chatStoreRoute     = "/groddle/v1/chat/ingest"
	chatStoreEvery     = 3 * time.Second
	chatStoreBatch     = 200
	chatStoreMax       = 200000
	chatStoreSaveAfter = time.Second
	chatStoreBackoff   = 30 * time.Second // the longest wait between tries while the site is down
)

var (
	chatStoreFlag     = flag.String("chat-store", "on", "on: channel, group and IM lines are kept on the site (needs -ledger-url); off: nothing is sent")
	chatStoreFileFlag = flag.String("chat-store-file", "", "file that keeps chat lines waiting for the site (default: realtime-chat-store.json beside -secret-file; off: memory only)")

	// Under go test the flags are ignored: a test gives the sink and the file, and sends with flush
	// itself (no sender goroutine) unless chatStoreRunForTest.
	chatStoreFileForTest string
	chatStoreSinkForTest chatSink
	chatStoreRunForTest  bool
)

// storeReact is a reaction as the site keeps it: the key and who, by id (names are the site's).
type storeReact struct {
	E  string  `json:"e"`
	By []int64 `json:"by"`
}

type chatEntry struct {
	K    string       `json:"k"`
	Seq  int64        `json:"seq"`
	Room string       `json:"room,omitempty"`
	Lid  int64        `json:"lid,omitempty"`
	ID   int64        `json:"id,omitempty"`
	Name string       `json:"name,omitempty"`
	Text string       `json:"text,omitempty"`
	At   int64        `json:"at,omitempty"`
	Web  bool         `json:"web,omitempty"`
	A    int64        `json:"a,omitempty"`
	B    int64        `json:"b,omitempty"`
	Line int64        `json:"line,omitempty"`
	From int64        `json:"from,omitempty"`
	R    []storeReact `json:"r,omitempty"`
}

// MarshalJSON writes a react's "r" even when it is empty (every reaction taken off).
func (e chatEntry) MarshalJSON() ([]byte, error) {
	type plain chatEntry
	b, err := json.Marshal(plain(e))
	if err != nil || (e.K != "react" && e.K != "im_react") || len(e.R) > 0 {
		return b, err
	}
	return append(b[:len(b)-1], []byte(`,"r":[]}`)...), nil
}

func storeReactions(rs reactions) []storeReact {
	out := make([]storeReact, 0, len(rs))
	for _, r := range rs {
		ids := make([]int64, 0, len(r.By))
		for _, b := range r.By {
			ids = append(ids, b.ID)
		}
		if len(ids) > 0 {
			out = append(out, storeReact{E: r.E, By: ids})
		}
	}
	return out
}

// chatSink is the site. post answers true once the site has kept every entry.
type chatSink interface {
	post(entries []chatEntry) bool
}

func init() { modules = append(modules, newChatStore) }

type chatStore struct {
	h        *Hub
	sink     chatSink
	path     string
	mu       sync.Mutex // the queue (taken under the hub lock by the chat modules, and by the sender without it)
	queue    []chatEntry
	seq      int64
	imported bool
	saving   bool
	dropped  int
	stop     chan struct{}
	wake     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

type chatStoreFile struct {
	V        int         `json:"v"`
	Imported bool        `json:"imported"`
	Seq      int64       `json:"seq"`
	Pending  []chatEntry `json:"pending"`
}

func newChatStore(h *Hub) Module {
	m := &chatStore{h: h, stop: make(chan struct{}), wake: make(chan struct{}, 1), done: make(chan struct{})}
	if testing.Testing() {
		m.sink = chatStoreSinkForTest
	} else if *chatStoreFlag != "off" {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.sink = &httpChatSink{l: l}
				log.Printf("chat store: channel, group and IM lines are kept at %s", l.base)
			}
		}
	}
	if m.sink == nil {
		close(m.done)
		return m
	}
	if m.path = statePath(chatStoreFileForTest, chatStoreFileFlag, "realtime-chat-store.json"); m.path != "" {
		var f chatStoreFile
		if ok, err := readStateFile("chat store", m.path, &f); err != nil {
			log.Printf("chat store %s: %v (waiting lines are kept in memory only)", m.path, err)
			m.path = ""
		} else if ok {
			m.queue, m.seq, m.imported = f.Pending, f.Seq, f.Imported
		}
	}
	if testing.Testing() && !chatStoreRunForTest {
		close(m.done)
		return m
	}
	go m.run()
	return m
}

func (m *chatStore) Register(r *Registry) {
	r.Provide("chatstore.self", m)
	r.OnShutdown(m.shutdown)
}

// chatStoreOf is the hub's chat store, nil when nothing is kept.
func (h *Hub) chatStoreOf() *chatStore {
	m, _ := h.provided["chatstore.self"].(*chatStore)
	if m == nil || m.sink == nil {
		return nil
	}
	return m
}

// storeChatLocked queues an entry for the site (a no-op when nothing is kept). Called under the hub lock.
func (h *Hub) storeChatLocked(e chatEntry) {
	if m := h.chatStoreOf(); m != nil {
		m.add(e)
	}
}

func (m *chatStore) add(e chatEntry) {
	m.mu.Lock()
	m.seq++
	e.Seq = m.seq
	m.queue = append(m.queue, e)
	if over := len(m.queue) - chatStoreMax; over > 0 {
		m.queue = append([]chatEntry(nil), m.queue[over:]...)
		was := m.dropped
		m.dropped += over
		if was == 0 || was/1000 != m.dropped/1000 {
			log.Printf("chat store: %d waiting lines dropped so far (the site has not answered)", m.dropped)
		}
	}
	m.saveSoonLocked()
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// pending is how many entries wait (tests and /healthz-style looks).
func (m *chatStore) pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queue)
}

func (m *chatStore) saveSoonLocked() {
	if m.path == "" || m.saving {
		return
	}
	m.saving = true
	time.AfterFunc(chatStoreSaveAfter, func() {
		m.mu.Lock()
		m.saving = false
		m.mu.Unlock()
		m.save()
	})
}

func (m *chatStore) save() {
	if m.path == "" {
		return
	}
	m.mu.Lock()
	f := chatStoreFile{V: 1, Imported: m.imported, Seq: m.seq, Pending: append([]chatEntry{}, m.queue...)}
	m.mu.Unlock()
	b, err := json.Marshal(f)
	if err == nil {
		err = writeFileSynced(m.path, b)
	}
	if err != nil {
		log.Printf("chat store %s: %v", m.path, err) // never the text
	}
}

// importOnce queues the IM lines im.go keeps now, the first time this store runs.
func (m *chatStore) importOnce() {
	m.mu.Lock()
	done := m.imported
	m.mu.Unlock()
	if done {
		return
	}
	m.h.mu.Lock()
	var lines []chatEntry
	im, ok := m.h.provided["ims.self"].(*ims)
	if !ok || im == nil {
		// No IM module (yet): nothing is marked, so a later run still brings them.
		m.h.mu.Unlock()
		return
	}
	{
		keys := make([][2]int64, 0, len(im.convs))
		for k := range im.convs {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			return keys[i][0] < keys[j][0] || (keys[i][0] == keys[j][0] && keys[i][1] < keys[j][1])
		})
		for _, k := range keys {
			c := im.convs[k]
			for _, l := range c.Lines {
				lines = append(lines, imEntry(c, l))
				if len(l.R) > 0 {
					lines = append(lines, imReactEntry(c, l))
				}
			}
		}
	}
	m.h.mu.Unlock()
	for _, e := range lines {
		m.add(e)
	}
	m.mu.Lock()
	m.imported = true
	m.saveSoonLocked()
	m.mu.Unlock()
	if len(lines) > 0 {
		log.Printf("chat store: %d kept IM entries sent to the site once", len(lines))
	}
}

func imEntry(c *imConv, l imLine) chatEntry {
	return chatEntry{K: "im", A: c.A, B: c.B, Line: l.ID, From: l.From, Text: l.Text, At: l.At}
}

func imReactEntry(c *imConv, l imLine) chatEntry {
	return chatEntry{K: "im_react", A: c.A, B: c.B, Line: l.ID, At: l.At, R: storeReactions(l.R)}
}

// run sends what waits, a batch at a time, until shutdown.
func (m *chatStore) run() {
	defer close(m.done)
	wait := chatStoreEvery
	imported := false
	timer := time.NewTimer(chatStoreEvery)
	defer timer.Stop()
	for {
		select {
		case <-m.stop:
			m.flush(1)
			return
		case <-timer.C:
		case <-m.wake:
			// A new line: wait out the gap anyway, so lines go in batches.
			continue
		}
		if !imported {
			// After the first wait: every module has registered by then (the IM module's lines are read here).
			m.importOnce()
			imported = true
		}
		if m.flush(0) {
			wait = chatStoreEvery
		} else {
			wait = min(chatStoreBackoff, wait*2)
		}
		timer.Reset(wait)
	}
}

// flush sends batches until nothing waits (or tries runs out, 0: as long as the site keeps answering);
// false when the site did not take one.
func (m *chatStore) flush(tries int) bool {
	for n := 0; tries == 0 || n < tries; n++ {
		m.mu.Lock()
		batch := append([]chatEntry(nil), m.queue[:min(len(m.queue), chatStoreBatch)]...)
		m.mu.Unlock()
		if len(batch) == 0 {
			return true
		}
		if !m.sink.post(batch) {
			return false
		}
		last := batch[len(batch)-1].Seq
		m.mu.Lock()
		i := 0
		for i < len(m.queue) && m.queue[i].Seq <= last {
			i++
		}
		m.queue = append([]chatEntry(nil), m.queue[i:]...)
		m.saveSoonLocked()
		m.mu.Unlock()
	}
	return true
}

func (m *chatStore) shutdown() {
	if m.sink == nil {
		return
	}
	m.stopOnce.Do(func() { close(m.stop) })
	if testing.Testing() && !chatStoreRunForTest {
		m.flush(1)
	}
	select {
	case <-m.done:
	case <-time.After(6 * time.Second):
	}
	m.save()
}

// —— The site ——

type httpChatSink struct {
	l *httpLedger
}

func (s *httpChatSink) post(entries []chatEntry) bool {
	body, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		return false
	}
	code, raw, err := s.l.raw(chatStoreRoute, body)
	if err != nil {
		log.Printf("chat store: %v", err)
		return false
	}
	var a struct {
		Status string `json:"status"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &a) != nil || a.Status != "ok" {
		log.Printf("chat store: HTTP %d: %.200s", code, raw)
		return false
	}
	return true
}

// raw posts one signed call and answers its status and body.
func (l *httpLedger) raw(route string, body []byte) (int, []byte, error) {
	var nb [8]byte
	rand.Read(nb[:])
	req, err := http.NewRequest(http.MethodPost, l.base+"/wp-json"+route, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Groddle-Service", signService(l.secret, http.MethodPost, route, l.now().Unix(), hex.EncodeToString(nb[:]), body))
	res, err := l.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", route, err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	return res.StatusCode, out, nil
}
