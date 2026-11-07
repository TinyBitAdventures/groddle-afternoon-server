package main

// The chat log (Austin 2026-10-31: "player chat written to a log for spam and
// abuse moderation"). Every line a player says is written down: street chat
// (hub.go "chat"), the channels and group chats (channels.go channel_say) and
// party chat (party.go party_chat). A line the server refused is written too,
// marked, since that is what moderation looks for: "slow_down" (over the rate)
// and "silenced" (the cone of silence, in a channel or Local chat). A line nobody could hear (no
// street, not in that chat, not in a party, a slash command in party chat) is
// not a chat line and is not written. IMs (im.go) are private and are never
// written here: the console's chat log has no IM text. Reactions on lines
// (reactions.go) are not written either: they are one of a fixed few marks, not words.
//
// Where: one file a UTC day, <dir>/2026-10-31.jsonl, the directory -chatlog-dir
// (default chatlog/ beside -secret-file; off: no log), mode 0700, the files
// 0600. Each line is one JSON object:
//
//	{"at":"2026-10-31T14:03:05.123Z","kind":"local","where":"GA5…","id":7,"name":"Ada","text":"hi","delivered":true}
//	{"at":"…","kind":"channel","where":"global","id":7,"name":"Ada","text":"buy gold","delivered":false,"why":"silenced"}
//	{"at":"…","kind":"party","where":"party-7-lq3x0b2s","id":7,"name":"Ada","text":"hi","delivered":true,"to":[7,9]}
//	{"at":"…","kind":"local","where":"GA5…","id":7,"name":"Ada","text":"spam","delivered":false,"why":"slow_down","skipped":12}
//	{"at":"…","kind":"local","where":"GA5…","id":7,"name":"Ada","text":"oh fuck","delivered":true,"filtered":true,"shown":"oh #!@$"}
//
// kind local | channel | party. where: the street's tsid (the site names it),
// the channel (global, trade, help, newbie, group:<id>) or the party's log id
// (given the first time the party chats; a restart gives a resumed party a new
// one). text exactly as the player typed it (cleanChat); when the word filter
// changed it (wordfilter.go, the remake's own addition) filtered is true and
// shown is what the others saw, so moderation still reads the original. delivered false with why
// for a refused line; skipped counts that player's refused lines left out
// before this one (at most chatlogRefusedBurst refused lines, then one each
// chatlogRefusedEvery, so a flood cannot fill the disk). to: who a party line
// went to.
//
// The write path never holds up chat. The hub (under its lock) only puts the
// line on a queue of chatlogQueue lines; when the queue is full the line is
// dropped and counted (the chatlog call answers "dropped"). One writer
// goroutine owns the files: it writes each line to a buffer and flushes the
// buffer to the file whenever the queue runs empty, so a line reaches the file
// at once when chat is quiet and in batches when it is busy; a crash of this
// process loses nothing that was flushed, a crash of the machine what the
// kernel had not written (the file is fsynced when the day changes and at
// shutdown). On SIGTERM the shutdown hook drains the queue, flushes, fsyncs and
// closes (main.go, Hub.Shutdown).
//
// Retention: -chatlog-days (default 30; 0 turns the log off). A day's file is
// removed once every line in it is more than that many days old, checked at
// start and whenever the UTC day changes (the writer looks each minute).
//
// A channel or thread line (kind channel, delivered) carries "lid", the line's
// id (reactions.go), since 2026-11-07. An administrator may delete such a line
// for everyone (chatdelete.go, Austin 2026-11-07); the line stays in its day's
// file as it was written, and the deletion is one line of deleted.jsonl in the
// same directory (same modes, the same retention by the day it was made):
//
//	{"at":"…","where":"newbie","lid":1730000000123,"id":7,"line_at":1730000000000,"text":"d/leave","by":1,"by_name":"Austin"}
//
// id, line_at and text are the deleted line's (who said it, when, what was
// shown) as far as the site or this server knew them. A search marks each line
// it answers that was deleted: by its lid, or, for a line written before lines
// carried one, by room, speaker, time (within chatlogDeleteSlack) and words:
//
//	{…,"lid":1730000000123,"deleted":{"at":"…","by":1,"by_name":"Austin"}}
//
// The administrators read it through the site's signed call (admin.go's
// console, teleport.go's signing), POST /ws?call=chatlog:
//
//	chatlog {"player":7,"name":"ad","q":"gold","kind":"channel","since":<unix ms>,"until":<unix ms>,"undelivered":true,"limit":100,"before":"2026-10-31:120"}
//	        every field optional; player the user id, name a piece of the name and q a piece of the text
//	        (both ignoring case), since inclusive and until exclusive, undelivered only refused lines
//	        200 {"status":"ok","lines":[line…],"next":"2026-10-31:57"|"","dropped":0,"days":30}   newest first
//	        200 {"status":"off","lines":[],"next":"","dropped":0,"days":0}                         no log kept
//	        limit: default chatlogPageDefault, at most chatlogPageMax, and a page stops near chatlogPageBytes;
//	        next is the cursor for the page after this one ("" at the end), passed back as before.

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	chatlogRefusedBurst = 20
	chatlogRefusedEvery = 3 * time.Second
	chatlogPageDefault  = 100
	chatlogPageMax      = 200
	chatlogPageBytes    = 192 << 10 // the site reads at most 256 KiB of an answer (src/RealtimeCall.php)
	chatlogFlushWait    = 2 * time.Second
	chatlogStopWait     = 5 * time.Second
	chatlogDeletedFile  = "deleted.jsonl"
	chatlogDeleteSlack  = 2 * time.Second // a line before lids: how far its time may be from the deleted line's
)

var (
	chatlogDirFlag  = flag.String("chatlog-dir", "", "directory for the chat log, one file a UTC day (default: chatlog beside -secret-file; off: no log)")
	chatlogDaysFlag = flag.Int("chatlog-days", 30, "days the chat log keeps; 0 turns it off")

	// Under go test the flags are ignored: a test sets the directory (none: no log) and may change the rest.
	chatlogDirForTest  string
	chatlogDaysForTest = 30
	chatlogQueue       = 4096
	chatlogHoldForTest chan struct{} // the writer waits for this before it starts (a full-queue test)

	chatlogFileRe   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.jsonl$`)
	chatlogCursorRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}):(\d{1,9})$`)
)

func init() {
	modules = append(modules, newChatlogModule)
	serviceHandlers["chatlog"] = adminChatlog
}

// chatLine is one line of the log.
type chatLine struct {
	At        string          `json:"at"`
	Kind      string          `json:"kind"`
	Where     string          `json:"where"`
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	Delivered bool            `json:"delivered"`
	Why       string          `json:"why,omitempty"`
	Skipped   int             `json:"skipped,omitempty"`
	To        []int64         `json:"to,omitempty"`
	Filtered  bool            `json:"filtered,omitempty"`
	Shown     string          `json:"shown,omitempty"`
	Lid       int64           `json:"lid,omitempty"`     // a delivered channel or thread line's id (reactions.go)
	Deleted   *chatlogDeleted `json:"deleted,omitempty"` // in a search's answer: an administrator deleted it

	t   time.Time     // when, for the day's file
	del *chatDeletion // set: this is a deletion, written to deleted.jsonl
}

// chatlogDeleted is how a search marks a deleted line.
type chatlogDeleted struct {
	At     string `json:"at"`
	By     int64  `json:"by"`
	ByName string `json:"by_name"`
}

// chatDeletion is one line of deleted.jsonl (chatdelete.go writes it).
type chatDeletion struct {
	At     string `json:"at"`
	Where  string `json:"where"`
	Lid    int64  `json:"lid"`
	ID     int64  `json:"id,omitempty"`
	LineAt int64  `json:"line_at,omitempty"`
	Text   string `json:"text,omitempty"`
	By     int64  `json:"by"`
	ByName string `json:"by_name"`
}

type chatLog struct {
	dir  string
	days int
	now  func() time.Time

	queue    chan chatLine
	flushReq chan chan struct{}
	stop     chan struct{}
	done     chan struct{}
	hold     chan struct{}
	dropped  atomic.Int64

	// Under the hub lock.
	closed  bool
	refused map[int64]*bucket
	skipped map[int64]int
}

// newChatLog makes a log that writes to dir and keeps days; start runs its writer.
func newChatLog(dir string, days int, now func() time.Time, queue int) *chatLog {
	return &chatLog{dir: dir, days: days, now: now, queue: make(chan chatLine, queue), flushReq: make(chan chan struct{}),
		stop: make(chan struct{}), done: make(chan struct{}), refused: map[int64]*bucket{}, skipped: map[int64]int{}}
}

type chatlogModule struct{ c *chatLog }

func newChatlogModule(h *Hub) Module {
	days := *chatlogDaysFlag
	if testing.Testing() {
		days = chatlogDaysForTest
	}
	dir := statePath(chatlogDirForTest, chatlogDirFlag, "chatlog")
	if dir == "" || days <= 0 {
		return &chatlogModule{}
	}
	err := os.MkdirAll(dir, 0o700)
	if err == nil {
		err = os.Chmod(dir, 0o700) // an existing directory too: the log is for administrators only
	}
	if err != nil {
		log.Printf("chatlog %s: %v (no chat log is kept)", dir, err)
		return &chatlogModule{}
	}
	// Retention follows the wall clock (the writer's own goroutine); each line's day is the hub's time when it was said.
	c := newChatLog(dir, days, time.Now, chatlogQueue)
	c.hold = chatlogHoldForTest
	go c.run()
	return &chatlogModule{c: c}
}

func (m *chatlogModule) Register(r *Registry) {
	if m.c == nil {
		return
	}
	r.Provide("chatlog.self", m.c)
	r.OnDisconnect(func(cl *Client) {
		delete(m.c.refused, cl.id)
		delete(m.c.skipped, cl.id)
	})
	r.OnShutdown(m.c.close)
}

// chatlogOf is the hub's chat log (nil when none is kept).
func (h *Hub) chatlogOf() *chatLog {
	c, _ := h.provided["chatlog.self"].(*chatLog)
	return c
}

// logChatLocked writes down one chat line (why "" when it was delivered): text is
// what was typed, and a line the word filter changes is marked with what was
// shown. Under the hub lock; never waits.
func (h *Hub) logChatLocked(kind, where string, cl *Client, text, why string, to []int64) {
	h.logChatAsLocked(kind, where, cl.id, cl.name, text, why, to)
}

// logChatAsLocked is logChatLocked for a player with no socket here (a line said from the website, webchat.go).
func (h *Hub) logChatAsLocked(kind, where string, id int64, name, text, why string, to []int64) {
	h.logChatLineLocked(kind, where, id, name, text, why, to, 0)
}

// logChatLineLocked writes a line with its id (a delivered channel or thread line), so a deletion finds it.
func (h *Hub) logChatLineLocked(kind, where string, id int64, name, text, why string, to []int64, lid int64) {
	c := h.chatlogOf()
	if c == nil {
		return
	}
	at := h.now()
	l := chatLine{At: at.UTC().Format("2006-01-02T15:04:05.000Z"), Kind: kind, Where: where, ID: id, Name: name,
		Text: text, Delivered: why == "", Why: why, To: to, Lid: lid, t: at}
	if shown := filterWords(text); shown != text {
		l.Filtered, l.Shown = true, shown
	}
	c.addLocked(l)
}

// logDeletionLocked writes an administrator's deletion of a line (chatdelete.go). Under the hub lock; never waits.
func (h *Hub) logDeletionLocked(d chatDeletion) {
	c := h.chatlogOf()
	if c == nil {
		return
	}
	at := h.now()
	d.At = at.UTC().Format("2006-01-02T15:04:05.000Z")
	c.addLocked(chatLine{Delivered: true, t: at, del: &d})
}

func (c *chatLog) addLocked(l chatLine) {
	if c.closed {
		return
	}
	if !l.Delivered {
		b := c.refused[l.ID]
		if b == nil {
			b = &bucket{}
			c.refused[l.ID] = b
		}
		if !b.take(l.t, chatlogRefusedBurst, chatlogRefusedEvery) {
			c.skipped[l.ID]++
			return
		}
		l.Skipped = c.skipped[l.ID]
		delete(c.skipped, l.ID)
	}
	select {
	case c.queue <- l:
	default:
		c.dropped.Add(1)
	}
}

// close drains the queue to the file and stops the writer (a shutdown hook, under the hub lock).
func (c *chatLog) close() {
	if c.closed {
		return
	}
	c.closed = true
	close(c.stop)
	select {
	case <-c.done:
	case <-time.After(chatlogStopWait):
		log.Printf("chatlog: the writer did not finish in %s", chatlogStopWait)
	}
}

// flush waits until every line queued so far is in the file (bounded; never under the hub lock).
func (c *chatLog) flush() {
	ack := make(chan struct{})
	select {
	case c.flushReq <- ack:
	case <-c.done:
		return
	case <-time.After(chatlogFlushWait):
		return
	}
	select {
	case <-ack:
	case <-c.done:
	case <-time.After(chatlogFlushWait):
	}
}

// run is the writer: the only code that touches the files.
func (c *chatLog) run() {
	defer close(c.done)
	if c.hold != nil {
		select {
		case <-c.hold:
		case <-c.stop:
		}
	}
	var (
		f        *os.File
		w        *bufio.Writer
		day      string
		failed   string // the day an open failed, logged once
		prunedOn string
	)
	closeFile := func() {
		if f == nil {
			return
		}
		if err := w.Flush(); err != nil {
			log.Printf("chatlog %s: %v", f.Name(), err)
		}
		f.Sync()
		f.Close()
		f, w, day = nil, nil, ""
	}
	prune := func() {
		today := c.now().UTC().Format("2006-01-02")
		if today != prunedOn {
			prunedOn = today
			c.prune()
		}
	}
	write := func(l chatLine) {
		if l.del != nil {
			c.writeDeletion(*l.del)
			return
		}
		d := l.t.UTC().Format("2006-01-02")
		if d != day {
			closeFile()
			path := filepath.Join(c.dir, d+".jsonl")
			nf, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				if failed != d {
					failed = d
					log.Printf("chatlog %s: %v", path, err)
				}
				c.dropped.Add(1)
				return
			}
			nf.Chmod(0o600)
			f, w, day = nf, bufio.NewWriterSize(nf, 64<<10), d
			prune()
		}
		b, _ := json.Marshal(l)
		w.Write(append(b, '\n'))
	}
	flush := func() {
		if w != nil {
			if err := w.Flush(); err != nil {
				log.Printf("chatlog %s: %v", f.Name(), err)
			}
		}
	}
	drain := func() {
		for {
			select {
			case l := <-c.queue:
				write(l)
			default:
				return
			}
		}
	}
	prune()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case l := <-c.queue:
			write(l)
			if len(c.queue) == 0 {
				flush()
			}
		case ack := <-c.flushReq:
			drain()
			flush()
			close(ack)
		case <-tick.C:
			prune()
		case <-c.stop:
			drain()
			closeFile()
			return
		}
	}
}

// writeDeletion appends one deletion to deleted.jsonl (the writer's goroutine only).
func (c *chatLog) writeDeletion(d chatDeletion) {
	path := filepath.Join(c.dir, chatlogDeletedFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("chatlog %s: %v", path, err)
		c.dropped.Add(1)
		return
	}
	f.Chmod(0o600)
	b, _ := json.Marshal(d)
	if _, err := f.Write(append(b, '\n')); err != nil {
		log.Printf("chatlog %s: %v", path, err)
	}
	f.Sync()
	f.Close()
}

// deletions reads deleted.jsonl (a line not finished or not parsed is passed over).
func (c *chatLog) deletions() []chatDeletion {
	b, err := os.ReadFile(filepath.Join(c.dir, chatlogDeletedFile))
	if err != nil {
		return nil
	}
	rows := strings.Split(string(b), "\n")
	var out []chatDeletion
	for _, row := range rows[:len(rows)-1] {
		var d chatDeletion
		if json.Unmarshal([]byte(row), &d) == nil && d.Lid > 0 {
			out = append(out, d)
		}
	}
	return out
}

// pruneDeletions keeps the deletions made on or after cutoff's day, as the days' files are kept.
func (c *chatLog) pruneDeletions(cutoff time.Time) {
	path := filepath.Join(c.dir, chatlogDeletedFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	rows := strings.Split(string(b), "\n")
	var keep []byte
	gone := 0
	for _, row := range rows[:len(rows)-1] {
		var d chatDeletion
		if json.Unmarshal([]byte(row), &d) == nil {
			if at, err := time.Parse(time.RFC3339Nano, d.At); err == nil && at.UTC().Truncate(24*time.Hour).Before(cutoff) {
				gone++
				continue
			}
		}
		keep = append(append(keep, row...), '\n')
	}
	if gone == 0 {
		return
	}
	if len(keep) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("chatlog: %v", err)
		}
		return
	}
	// A search reading it meanwhile sees the old file or the new one, never half of one.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, keep, 0o600); err != nil {
		log.Printf("chatlog %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("chatlog %s: %v", path, err)
	}
}

// deletedMark is how a search marks l when one of dels deleted it, nil otherwise.
func deletedMark(l chatLine, at time.Time, dels []chatDeletion) *chatlogDeleted {
	if l.Kind != "channel" || !l.Delivered {
		return nil
	}
	for _, d := range dels {
		if d.Where != l.Where {
			continue
		}
		hit := l.Lid > 0 && d.Lid == l.Lid
		if l.Lid == 0 && d.ID == l.ID && d.LineAt > 0 && d.Text != "" {
			gap := at.Sub(time.UnixMilli(d.LineAt))
			hit = gap <= chatlogDeleteSlack && gap >= -chatlogDeleteSlack && (d.Text == l.Text || d.Text == l.Shown)
		}
		if hit {
			return &chatlogDeleted{At: d.At, By: d.By, ByName: d.ByName}
		}
	}
	return nil
}

// prune removes each day's file whose every line is more than c.days days old.
func (c *chatLog) prune() {
	cutoff := c.now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -c.days)
	c.pruneDeletions(cutoff)
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		log.Printf("chatlog %s: %v", c.dir, err)
		return
	}
	for _, e := range entries {
		m := chatlogFileRe.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			continue
		}
		d, err := time.Parse("2006-01-02", m[1])
		if err != nil || !d.Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(c.dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("chatlog: %v", err)
		}
	}
}

// chatQuery is the console's search.
type chatQuery struct {
	Player      int64  `json:"player"`
	Name        string `json:"name"`
	Q           string `json:"q"`
	Kind        string `json:"kind"`
	Since       int64  `json:"since"`
	Until       int64  `json:"until"`
	Undelivered bool   `json:"undelivered"`
	Limit       int    `json:"limit"`
	Before      string `json:"before"`
}

func readChatQuery(body []byte) (chatQuery, bool) {
	var q chatQuery
	if json.Unmarshal(body, &q) != nil || q.Player < 0 || q.Since < 0 || q.Until < 0 || q.Limit < 0 {
		return q, false
	}
	switch q.Kind {
	case "", "local", "channel", "party":
	default:
		return q, false
	}
	if q.Before != "" && !chatlogCursorRe.MatchString(q.Before) {
		return q, false
	}
	q.Name = strings.ToLower(strings.TrimSpace(q.Name))
	q.Q = strings.ToLower(strings.TrimSpace(q.Q))
	if len([]rune(q.Name)) > 64 || len([]rune(q.Q)) > maxChat {
		return q, false
	}
	if q.Limit == 0 {
		q.Limit = chatlogPageDefault
	}
	q.Limit = min(q.Limit, chatlogPageMax)
	return q, true
}

func (q chatQuery) match(l chatLine, at time.Time) bool {
	return (q.Player == 0 || l.ID == q.Player) &&
		(q.Kind == "" || l.Kind == q.Kind) &&
		(!q.Undelivered || !l.Delivered) &&
		(q.Since == 0 || at.UnixMilli() >= q.Since) &&
		(q.Until == 0 || at.UnixMilli() < q.Until) &&
		(q.Name == "" || strings.Contains(strings.ToLower(l.Name), q.Name)) &&
		(q.Q == "" || strings.Contains(strings.ToLower(l.Text), q.Q))
}

// search reads the files newest first. A line the writer has not finished (no newline yet) or that does not parse is passed over.
func (c *chatLog) search(q chatQuery) (lines []chatLine, next string) {
	entries, _ := os.ReadDir(c.dir)
	var days []string
	for _, e := range entries {
		if m := chatlogFileRe.FindStringSubmatch(e.Name()); m != nil && !e.IsDir() {
			days = append(days, m[1])
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	fromDay, toDay, cursorDay, upTo := "", "", "", 0
	if q.Since > 0 {
		fromDay = time.UnixMilli(q.Since).UTC().Format("2006-01-02")
	}
	if q.Until > 0 {
		toDay = time.UnixMilli(q.Until - 1).UTC().Format("2006-01-02")
	}
	if m := chatlogCursorRe.FindStringSubmatch(q.Before); m != nil {
		cursorDay = m[1]
		upTo, _ = strconv.Atoi(m[2])
	}
	lines = []chatLine{}
	dels := c.deletions()
	size := 0
	for _, d := range days {
		if (toDay != "" && d > toDay) || (fromDay != "" && d < fromDay) || (cursorDay != "" && d > cursorDay) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.dir, d+".jsonl"))
		if err != nil {
			continue
		}
		rows := strings.Split(string(b), "\n")
		rows = rows[:len(rows)-1] // the part after the last newline: empty, or a line still being written
		end := len(rows)
		if d == cursorDay {
			end = min(end, upTo)
		}
		for i := end - 1; i >= 0; i-- {
			var l chatLine
			if json.Unmarshal([]byte(rows[i]), &l) != nil {
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, l.At)
			if err != nil || !q.match(l, at) {
				continue
			}
			if len(lines) >= q.Limit || size+len(rows[i]) > chatlogPageBytes {
				return lines, fmt.Sprintf("%s:%d", d, i+1)
			}
			l.Deleted = deletedMark(l, at, dels)
			lines = append(lines, l)
			size += len(rows[i]) + 1
		}
	}
	return lines, ""
}

func adminChatlog(h *Hub, body []byte) (int, any) {
	q, ok := readChatQuery(body)
	if !ok {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	c := h.chatlogOf()
	h.mu.Unlock()
	if c == nil {
		return http.StatusOK, map[string]any{"status": "off", "lines": []chatLine{}, "next": "", "dropped": 0, "days": 0}
	}
	c.flush()
	lines, next := c.search(q)
	return http.StatusOK, map[string]any{"status": "ok", "lines": lines, "next": next, "dropped": c.dropped.Load(), "days": c.days}
}
