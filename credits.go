package main

// Server outcomes as signed credits (G335 slice 3). What this server decides
// a player gets, it tells the site's ledger (groddle-afternoon-site
// src/LedgerCredits.php), so the player's next save can show it outside the
// earning budget:
//
//   - quoins.go: a currants quoin's grab, its benefit times the grabber's
//     multiplier (items/quoin.js 161-174; the multiplier as the client reports
//     it, at most the source's quoin_capacity_limit 100, config_base.js:252),
//     while the grabber has grabbed fewer than quoinCreditDaily quoins this
//     game day; and each currants shard (quoin.js 107-118).
//   - races.go: the winner's 100 currants (players/inc_games.js:1374
//     games_end_race, :1574 games_end_quoin_grab).
//   - objects.go: what a player takes from a shared rock, tree, Wood Tree or
//     resource node, as item counts (objCredit).
//   - rook.go: nothing. Every share of a Rook fight is iMG, energy, mood or
//     favor (rook_you), none of which the ledger holds.
//   - verbs.go: the 250 currants a Charades guess wins (main.js:1782).
//
// The call, signed as ledger.go signs (X-Groddle-Service over the body):
//
//	POST <site>/wp-json/groddle/v1/ledger/credits
//	     {"credits":[{"id":"cr-q-…","user":7,"why":"quoin","got":Goods,"at":1730000000},…]}  at most creditBatch
//	     200 {"done":["cr-q-…",…]}   every id the site has recorded, now or before, or will never take
//
// Idempotent per event id: the site keeps each id once, so sending one again
// (a lost answer, a retry) changes nothing. Delivery is at least once while
// this process runs: events wait in the outbox, oldest first, and a batch
// that gets no answer (the site down, a 5xx, a timeout) is sent again after a
// backoff that doubles from creditBackoffMin to creditBackoffMax, until the
// site answers. An id leaves the outbox only once the site names it done.
// The outbox holds at most creditQueueMax events; past that the oldest are
// dropped (logged).
//
// Across a restart of this server the outbox is kept: every event is
// written to a journal (-credits-file, default realtime-credits.jsonl beside
// -secret-file; off: memory only) as it is queued, one {"add":event} line,
// and each answer the site gives is a {"done":[ids]} line. A restart replays
// the journal and sends what the site has not named done, with the same ids,
// so nothing is lost and nothing is credited twice. Adds are written without
// an fsync (they happen under the hub lock, and what a process writes
// survives the process: a deploy's restart or a crash); the done lines,
// written off the hub lock, are synced. The journal is rewritten down to
// what still waits once it is creditCompactAt lines long, and when it is
// opened. A torn last line (a crash mid-write) is skipped. Past
// creditQueueMax the oldest events are dropped (logged): a credit lost so
// never reaches the site, and the player's save shows the outcome anyway, so
// the site pays for it from the earning budget as before slice 3: nothing is
// minted twice and nothing leaves the player's pack.
//
// Without the site's ledger (-trade-ledger off, no -ledger-url, tests) the
// module offers its hook but sends nothing.

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

const (
	ledgerRouteCredits = "/groddle/v1/ledger/credits"
	creditBatch        = 50
	creditGather       = 250 * time.Millisecond // a grab and its shards go in one call
	creditBackoffMin   = time.Second
	creditBackoffMax   = time.Minute
	creditQueueMax     = 20_000
	creditCompactAt    = 2000 // journal lines before it is rewritten to what still waits

	// quoinCreditDaily: the most quoins a player can collect in a game day,
	// 100 + 5 * 10 with daily_quoin_limit_5 (js/quoins.js quoinDailyLimitFor;
	// players/inc_stats.js:22 quoins_today top). Grabs past it in one game
	// day are not credited; the client's own limit stops it applying them.
	quoinCreditDaily = 150

	// raceWinCurrants: players/inc_games.js:1374 and :1574 stats_add_currants(100).
	raceWinCurrants = 100
)

var creditsFileFlag = flag.String("credits-file", "", "file that keeps the credits the site's ledger has not taken yet, so a restart sends them (default: realtime-credits.jsonl beside -secret-file; off: memory only)")

// creditsFileForTest lets a test give the module a journal; under go test the flag is ignored.
var creditsFileForTest string

// The why of each credit, and the letter its id carries (charades: verbs.go, a Charades guess's 250 currants).
var creditWhy = map[string]string{"quoin": "q", "shard": "s", "race": "r", "take": "t", "charades": "c"}

// creditEvent is one outcome for one player.
type creditEvent struct {
	ID   string     `json:"id"`
	User int64      `json:"user"`
	Why  string     `json:"why"`
	Got  tradeGoods `json:"got"`
	At   int64      `json:"at"`
}

// creditSite is the site's ledger as credits use it: one call, no retries of its own.
type creditSite interface {
	credits(batch []creditEvent) (done []string, err error)
}

func (l *httpLedger) credits(batch []creditEvent) ([]string, error) {
	body, _ := json.Marshal(map[string]any{"credits": batch})
	var nb [8]byte
	rand.Read(nb[:])
	req, err := http.NewRequest(http.MethodPost, l.base+"/wp-json"+ledgerRouteCredits, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Groddle-Service", signService(l.secret, http.MethodPost, ledgerRouteCredits, l.now().Unix(), hex.EncodeToString(nb[:]), body))
	res, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var a struct {
		Done []string `json:"done"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(raw, &a) != nil || a.Done == nil {
		return nil, fmt.Errorf("HTTP %d: %.200s", res.StatusCode, raw)
	}
	return a.Done, nil
}

// —— The outbox ——

type creditOutbox struct {
	site  creditSite
	sleep func(time.Duration)

	mu      sync.Mutex
	queue   []creditEvent
	dropped int
	wake    chan struct{}
	journal *creditJournal // nil: memory only
}

func newCreditOutbox(site creditSite) *creditOutbox {
	return &creditOutbox{site: site, sleep: time.Sleep, wake: make(chan struct{}, 1)}
}

// add queues one event (any goroutine; it never blocks on the site).
func (o *creditOutbox) add(ev creditEvent) {
	o.mu.Lock()
	if len(o.queue) >= creditQueueMax {
		o.queue = o.queue[1:]
		o.dropped++
		if o.dropped == 1 || o.dropped%1000 == 0 {
			log.Printf("credits: the outbox is full, %d oldest dropped (their outcomes go through the budget)", o.dropped)
		}
	}
	o.queue = append(o.queue, ev)
	if o.journal != nil {
		if err := o.journal.write(creditLine{Add: &ev}, false); err != nil {
			log.Printf("credits journal: %v (credit %s kept in memory only)", err, ev.ID)
		}
	}
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (o *creditOutbox) pending() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.queue)
}

// flushOnce sends the oldest batch and takes what the site names done out of
// the outbox. It returns how many are still waiting, and the error when the
// site gave no answer (the batch stays, to be sent again).
func (o *creditOutbox) flushOnce() (int, error) {
	o.mu.Lock()
	n := min(len(o.queue), creditBatch)
	batch := append([]creditEvent(nil), o.queue[:n]...)
	o.mu.Unlock()
	if n == 0 {
		return 0, nil
	}
	done, err := o.site.credits(batch)
	if err != nil {
		return o.pending(), err
	}
	gone := make(map[string]bool, len(done))
	for _, id := range done {
		gone[id] = true
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := o.queue[:0]
	for _, ev := range o.queue {
		if !gone[ev.ID] {
			kept = append(kept, ev)
		}
	}
	o.queue = kept
	if o.journal != nil && len(done) > 0 {
		if err := o.journal.write(creditLine{Done: done}, true); err != nil {
			log.Printf("credits journal: %v", err)
		}
		if o.journal.lines >= creditCompactAt {
			if err := o.journal.rewrite(o.queue); err != nil {
				log.Printf("credits journal: %v", err)
			}
		}
	}
	if len(gone) == 0 {
		// An answer that took none of them: keep them, but say so (a site that
		// refuses the shape would otherwise be retried in silence).
		return len(o.queue), fmt.Errorf("the site took none of %d", n)
	}
	return len(o.queue), nil
}

// run sends batches for as long as the process lives: after a wake it waits
// creditGather for more, then sends until the outbox is empty, backing off
// while the site does not answer.
func (o *creditOutbox) run() {
	backoff := creditBackoffMin
	for range o.wake {
		o.sleep(creditGather)
		for {
			left, err := o.flushOnce()
			if err != nil {
				log.Printf("credits: %v (%d waiting, again in %s)", err, left, backoff)
				o.sleep(backoff)
				backoff = min(2*backoff, creditBackoffMax)
				continue
			}
			backoff = creditBackoffMin
			if left == 0 {
				break
			}
		}
	}
}

// —— The journal ——

// creditLine is one line of the journal: an event queued, or the ids the site named done.
type creditLine struct {
	Add  *creditEvent `json:"add,omitempty"`
	Done []string     `json:"done,omitempty"`
}

type creditJournal struct {
	path  string
	f     *os.File
	lines int
}

// openCreditJournal replays a journal (a missing file is an empty one) and
// rewrites it down to the events still waiting, oldest first, at most
// creditQueueMax (the newest kept).
func openCreditJournal(path string) (*creditJournal, []creditEvent, error) {
	var queue []creditEvent
	at := map[string]int{} // id -> index in queue, -1 once done
	f, err := os.Open(path)
	if err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		bad := 0
		for sc.Scan() {
			var l creditLine
			if json.Unmarshal(sc.Bytes(), &l) != nil {
				bad++ // a torn last line from a crash
				continue
			}
			if ev := l.Add; ev != nil && ev.ID != "" && ev.User > 0 {
				if _, seen := at[ev.ID]; !seen {
					if ev.Got.Items == nil {
						ev.Got.Items = []tradeItem{}
					}
					at[ev.ID] = len(queue)
					queue = append(queue, *ev)
				}
			}
			for _, id := range l.Done {
				if i, ok := at[id]; ok && i >= 0 {
					queue[i].ID = ""
				}
				at[id] = -1
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, nil, err
		}
		if bad > 0 {
			log.Printf("credits journal %s: %d lines did not read (skipped)", path, bad)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	kept := queue[:0]
	for _, ev := range queue {
		if ev.ID != "" {
			kept = append(kept, ev)
		}
	}
	if len(kept) > creditQueueMax {
		log.Printf("credits journal %s: %d waiting, the oldest %d dropped (their outcomes go through the budget)", path, len(kept), len(kept)-creditQueueMax)
		kept = kept[len(kept)-creditQueueMax:]
	}
	j := &creditJournal{path: path}
	if err := j.rewrite(kept); err != nil {
		return nil, nil, err
	}
	return j, kept, nil
}

// rewrite replaces the file with one add line per event still waiting (a temp file and a rename).
func (j *creditJournal) rewrite(queue []creditEvent) error {
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return err
	}
	tmp := j.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for i := range queue {
		b, _ := json.Marshal(creditLine{Add: &queue[i]})
		w.Write(append(b, '\n'))
	}
	err = w.Flush()
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, j.path); err != nil {
		return err
	}
	if j.f != nil {
		j.f.Close()
	}
	j.f, err = os.OpenFile(j.path, os.O_APPEND|os.O_WRONLY, 0o600)
	j.lines = len(queue)
	return err
}

func (j *creditJournal) write(l creditLine, sync bool) error {
	if j.f == nil {
		return errors.New("journal closed")
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return err
	}
	j.lines++
	if sync {
		return j.f.Sync()
	}
	return nil
}

// journalled gives the outbox a journal at path ("" leaves it in memory only)
// and queues what the journal kept; the run loop is woken for them.
func (o *creditOutbox) journalled(path string) error {
	if path == "" {
		return nil
	}
	j, kept, err := openCreditJournal(path)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.journal = j
	o.queue = append(kept, o.queue...)
	n := len(o.queue)
	o.mu.Unlock()
	if n > 0 {
		log.Printf("credits journal %s: %d credits from before the restart to send", path, n)
		select {
		case o.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// —— The module ——

type credits struct {
	h    *Hub
	out  *creditOutbox // nil: no site ledger, nothing is sent
	boot string
	seq  uint64
}

func init() { modules = append(modules, newCredits) }

func newCredits(h *Hub) Module {
	var b [4]byte
	rand.Read(b[:])
	m := &credits{h: h, boot: hex.EncodeToString(b[:])}
	if !testing.Testing() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if hl, ok := site.(*httpLedger); ok {
				m.out = newCreditOutbox(hl)
				path := statePath(creditsFileForTest, creditsFileFlag, "realtime-credits.jsonl")
				if err := m.out.journalled(path); err != nil {
					log.Printf("credits journal %s: %v (credits are kept in memory only)", path, err)
				}
				go m.out.run()
			}
		}
	}
	return m
}

func (m *credits) Register(r *Registry) {
	r.Provide("credits.add", m.add)
}

// add records one outcome: its id is cr-<why letter>-<this process>-<n>,
// unique to the event, so a resend is the same id. Returns the id, or ""
// when nothing is sent (no ledger, nothing of value).
func (m *credits) add(user int64, why string, got tradeGoods) string {
	letter, ok := creditWhy[why]
	if m.out == nil || !ok || user <= 0 || got.Currants < 0 || got.empty() {
		return ""
	}
	m.seq++
	ev := creditEvent{ID: "cr-" + letter + "-" + m.boot + "-" + strconv.FormatUint(m.seq, 36), User: user, Why: why, Got: got, At: m.h.Now().Unix()}
	if ev.Got.Items == nil {
		ev.Got.Items = []tradeItem{}
	}
	m.out.add(ev)
	return ev.ID
}

// creditLocked hands the site one server outcome for a player, when the
// credits module runs (a hub built without it credits nothing). Under the hub lock.
func creditLocked(h *Hub, user int64, why string, got tradeGoods) {
	if fn, ok := h.ProvidedLocked("credits.add").(func(int64, string, tradeGoods) string); ok {
		fn(user, why, got)
	}
}

// creditItem is n of one class as goods (nothing at or under zero).
func creditItem(c string, n int) tradeGoods {
	if c == "" || n <= 0 {
		return tradeGoods{}
	}
	return tradeGoods{Items: []tradeItem{{C: c, N: n}}}
}
