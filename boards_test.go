package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBoardSite stands in for the site's groddle/v1/ledger/board routes: it keeps the notes as the
// site's table does, counts a board's notes before a pin (LedgerBoard::pinOf) and pins the opening
// notices once (groddle_boards).
type fakeBoardSite struct {
	mu     sync.Mutex
	pins   []boardPinCall
	takes  []string
	loads  int
	seeded map[string]bool
	rows   map[string]boardRow
	order  []string
	taker  map[string]int64
	names  map[int64]string
	silent bool
	short  bool // the poster's ledger has no Note
	err    error
}

func newFakeBoardSite() *fakeBoardSite {
	return &fakeBoardSite{seeded: map[string]bool{}, rows: map[string]boardRow{}, taker: map[string]int64{}, names: map[int64]string{7: "Ada", 9: "Bo", 11: "Cy", 13: "Dee"}}
}

func (f *fakeBoardSite) count(street string) int {
	n := 0
	for _, id := range f.order {
		if r, ok := f.rows[id]; ok && r.Street == street {
			n++
		}
	}
	return n
}

func (f *fakeBoardSite) boardPin(c boardPinCall) boardAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pins = append(f.pins, c)
	if f.silent {
		return boardAnswer{}
	}
	if r, ok := f.rows[c.Op]; ok {
		return boardAnswer{Status: "settled", Note: &r}
	}
	if f.short {
		return boardAnswer{Status: "refused", Why: "short"}
	}
	if f.count(c.Street) >= c.Capacity {
		return boardAnswer{Status: "refused", Why: "full"}
	}
	author := c.ID
	if c.Author > 0 {
		author = c.Author
	}
	if c.Remake {
		author = 0
	}
	r := boardRow{ID: c.Op, Street: c.Street, Board: c.Board, Title: c.Title, Body: c.Body, Author: author, Name: f.names[author], Remake: c.Remake, At: 1_700_000_100}
	f.rows[c.Op] = r
	f.order = append(f.order, c.Op)
	return boardAnswer{Status: "settled", Note: &r}
}

func (f *fakeBoardSite) boardTake(op string, id int64) boardAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takes = append(f.takes, fmt.Sprintf("%s:%d", op, id))
	if f.silent {
		return boardAnswer{}
	}
	if by, ok := f.taker[op]; ok {
		if by == id {
			r := f.rows[op+"#taken"]
			return boardAnswer{Status: "settled", Op: op + "-t", Note: &r}
		}
		return boardAnswer{Status: "refused", Why: "gone", By: by}
	}
	r, ok := f.rows[op]
	if !ok {
		return boardAnswer{Status: "refused", Why: "gone"}
	}
	delete(f.rows, op)
	f.rows[op+"#taken"] = r
	f.taker[op] = id
	return boardAnswer{Status: "settled", Op: op + "-t", Note: &r}
}

func (f *fakeBoardSite) boardsLoad(seeds map[string][]giftNote) ([]boardRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.err != nil {
		return nil, f.err
	}
	for street, list := range seeds {
		if f.seeded[street] {
			continue
		}
		f.seeded[street] = true
		for i, s := range list {
			id := fmt.Sprintf("board-seed-%s-%d", street, i)
			f.rows[id] = boardRow{ID: id, Street: street, Title: s.Title, Body: s.Body, Remake: true, At: 1_700_000_000}
			f.order = append(f.order, id)
		}
	}
	out := []boardRow{}
	for _, id := range f.order {
		if r, ok := f.rows[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

type boardRig struct {
	t       *testing.T
	hub     *Hub
	m       *boards
	site    *fakeBoardSite
	now     time.Time
	timers  []*rigTimer
	pending []func()
}

// boardStreet is Gregarious Towers (three opening notices); boardPlain has no board.
const (
	boardStreet = "GA5134UPLRU29OH"
	boardForest = "GIF16EM56A12FSB" // the Forest Community Machine Room: a board, no notices
	boardPlain  = "LA5101HF7F429V"
)

func newBoardRig(t *testing.T, site *fakeBoardSite) *boardRig {
	if site == nil {
		site = newFakeBoardSite()
	}
	r := &boardRig{t: t, now: time.Unix(1_700_000_000, 0), site: site}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module { r.m = newBoards(h).(*boards); return r.m }}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	r.m.site = r.site
	r.m.spawn = func(fn func()) { r.pending = append(r.pending, fn) }
	n := 0
	r.m.newID = func() string { n++; return fmt.Sprintf("%016x", n) }
	r.m.load()
	r.settle()
	return r
}

func (r *boardRig) connect(id int64, name string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	drain(cl)
	return cl
}

func (r *boardRig) do(cl *Client, msg map[string]any) {
	r.now = r.now.Add(time.Second)
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

func (r *boardRig) at(cl *Client, street string) {
	r.do(cl, map[string]any{"t": "join", "street": street, "x": 100, "y": -100})
}

func (r *boardRig) settle() {
	for len(r.pending) > 0 {
		fn := r.pending[0]
		r.pending = r.pending[1:]
		fn()
	}
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if tm.d == 0 && !tm.stopped {
			tm.stopped = true
			tm.fn()
		}
	}
}

func (r *boardRig) pin(cl *Client, ref, title, body string) {
	r.do(cl, map[string]any{"t": "board_pin", "ref": ref, "board": 0, "note": map[string]any{"title": title, "body": body}})
}

// pair puts Ada (7) and Bo (9) on Gregarious Towers.
func (r *boardRig) pair() (ada, bo *Client) {
	ada, bo = r.connect(7, "Ada"), r.connect(9, "Bo")
	r.at(ada, boardStreet)
	r.at(bo, boardStreet)
	drain(ada)
	drain(bo)
	return
}

func of(frames []map[string]any, kind string) []map[string]any {
	out := []map[string]any{}
	for _, f := range frames {
		if f["t"] == kind {
			out = append(out, f)
		}
	}
	return out
}

func boardNotes(t *testing.T, st map[string]any) []any {
	t.Helper()
	b := st["boards"].([]any)
	if len(b) != 1 {
		t.Fatalf("one board: %v", st)
	}
	return b[0].(map[string]any)["notes"].([]any)
}

func TestTheBoardsDataNamesEveryBoardAndItsOpeningNotices(t *testing.T) {
	d := parseBoardData(boardsJSON)
	if d.Capacity != 10 || d.Title != 150 || d.Body != 1000 {
		t.Fatalf("bag_notice_board.js onCreate 155, note.js 246-247: %+v", d)
	}
	if len(d.Boards) != 8 {
		t.Fatalf("7 archived boards and the Forest machine room: %d", len(d.Boards))
	}
	if n := len(d.Boards[boardStreet].Seeds); n != 3 {
		t.Fatalf("Gregarious Towers opens with three notices: %d", n)
	}
	if n := len(d.Boards["GHVUR3UKGRU2SD2"].Seeds); n != 7 {
		t.Fatalf("Egret Taun Towers seven: %d", n)
	}
	if _, ok := d.Boards[boardForest]; !ok {
		t.Fatalf("the Forest room has a board")
	}
	for street := range d.Boards {
		if followPrivate(street) {
			t.Fatalf("a board street is a public street: %s", street)
		}
	}
}

func TestBoardsLoadPinTheOpeningNoticesOnceAndSurviveARestart(t *testing.T) {
	site := newFakeBoardSite()
	r := newBoardRig(t, site)
	if site.loads != 1 || !r.m.loaded {
		t.Fatalf("one load: %d", site.loads)
	}
	want := 0
	for _, b := range r.m.data.Boards {
		want += len(b.Seeds)
	}
	if len(r.m.notes) != want || want != 35 {
		t.Fatalf("every board's opening notices: %d of %d", len(r.m.notes), want)
	}
	ada := r.connect(7, "Ada")
	r.at(ada, boardStreet)
	st := pick(t, drain(ada), "board_state")
	notes := boardNotes(t, st)
	if st["capacity"] != float64(10) || len(notes) != 3 {
		t.Fatalf("Ada sees the three notices: %v", st)
	}
	first := notes[0].(map[string]any)
	if first["remake"] != true || first["author"] != nil || first["title"] != "Welcome" {
		t.Fatalf("the remake's own, with no author: %v", first)
	}
	// Ada takes one and pins one of her own; then the server restarts.
	r.do(ada, map[string]any{"t": "board_take", "id": first["id"]})
	r.settle()
	r.pin(ada, "p1", "Lost hat", "Blue, with a feather.")
	r.settle()
	drain(ada)

	r2 := newBoardRig(t, site)
	if site.loads != 2 {
		t.Fatalf("the restart loads again: %d", site.loads)
	}
	got := []string{}
	for _, n := range r2.m.byBoard[boardKey(boardStreet, 0)] {
		got = append(got, n.Title)
	}
	if strings.Join(got, "|") != "How to post|Be kind|Lost hat" {
		t.Fatalf("after a restart the board is as it was, the notices not pinned again: %v", got)
	}
	if len(r2.m.notes) != want {
		t.Fatalf("nothing lost or doubled: %d", len(r2.m.notes))
	}
}

func TestALoadThatFailsTriesAgainAndPinsWaitForIt(t *testing.T) {
	site := newFakeBoardSite()
	site.err = fmt.Errorf("down")
	r := newBoardRig(t, site)
	if r.m.loaded {
		t.Fatalf("not loaded while the site is down")
	}
	ada := r.connect(7, "Ada")
	r.at(ada, boardStreet)
	if st := of(drain(ada), "board_state"); len(st) != 0 {
		t.Fatalf("no board state before the load: %v", st)
	}
	r.pin(ada, "p1", "Hi", "There")
	if f := pick(t, drain(ada), "board_pinned"); f["status"] != "refused" || f["why"] != "off" || f["text"] != boardTextOff || f["ref"] != "p1" {
		t.Fatalf("refused until loaded: %v", f)
	}
	site.err = nil
	n := 0
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if tm.d == boardLoadRetry && !tm.stopped {
			tm.stopped = true
			tm.fn()
			n++
		}
	}
	r.settle()
	if n == 0 || !r.m.loaded {
		t.Fatalf("the retry loads: %d %v", n, r.m.loaded)
	}
	if st := pick(t, drain(ada), "board_state"); len(boardNotes(t, st)) != 3 {
		t.Fatalf("Ada, already there, is sent the board: %v", st)
	}
}

func TestAPinSettlesAtTheSiteAndEveryoneOnTheStreetSeesIt(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, bo := r.pair()
	r.pin(ada, "p1", "Lost hat", "Blue, with a feather.")
	if got := append(drain(ada), drain(bo)...); len(got) != 0 {
		t.Fatalf("nothing until the site answers: %v", got)
	}
	r.settle()
	c := r.site.pins[len(r.site.pins)-1]
	if c.Op != "board-0000000000000001" || c.ID != 7 || c.Street != boardStreet || c.Board != 0 || c.Title != "Lost hat" || c.Body != "Blue, with a feather." || c.Author != 0 || c.Remake || c.Capacity != 10 {
		t.Fatalf("the call: %+v", c)
	}
	af := drain(ada)
	mine := pick(t, af, "board_pinned")
	note := mine["note"].(map[string]any)
	if mine["status"] != "settled" || mine["op"] != c.Op || mine["ref"] != "p1" || note["id"] != c.Op || note["author"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("Ada is told: %v", mine)
	}
	if a := pick(t, af, "board_added"); a["note"].(map[string]any)["id"] != c.Op {
		t.Fatalf("Ada's board too: %v", a)
	}
	added := only(t, drain(bo), "board_added")
	if added["street"] != boardStreet || added["board"] != float64(0) || added["note"].(map[string]any)["body"] != "Blue, with a feather." {
		t.Fatalf("Bo sees it live: %v", added)
	}
	cy := r.connect(11, "Cy")
	r.at(cy, boardStreet)
	if notes := boardNotes(t, pick(t, drain(cy), "board_state")); len(notes) != 4 || notes[3].(map[string]any)["id"] != c.Op {
		t.Fatalf("Cy, arriving, reads it last: %v", notes)
	}
	if n := r.hub.ProvidedLocked("boards.count").(func(string) int)(boardStreet); n != 4 {
		t.Fatalf("boards.count: %d", n)
	}
}

func TestAPinGoesThroughTheWordFilterAndTheNoteLimits(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, _ := r.pair()
	r.pin(ada, "p1", "What the fuck\nnow"+strings.Repeat("t", 200), "you shit <b>x</b>\nhead\x00"+strings.Repeat("b", 1200))
	r.settle()
	c := r.site.pins[0]
	if strings.Contains(c.Title, "fuck") || strings.Contains(c.Body, "shit") {
		t.Fatalf("filtered before the site sees it: %+v", c)
	}
	if !strings.HasPrefix(c.Title, "What the #!@$ now") || !strings.HasPrefix(c.Body, "you #!@$ bx/b\nhead") {
		t.Fatalf("a grawlix of the same length, one line for a title, no tags or control characters: %q / %q", c.Title, c.Body[:20])
	}
	if n := len([]rune(c.Title)); n != 150 {
		t.Fatalf("note.js:246 title 150: %d", n)
	}
	if n := len([]rune(c.Body)); n != 1000 {
		t.Fatalf("note.js:247 body 1000: %d", n)
	}
}

func TestARemakeNotePinnedAgainKeepsItsMarker(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, bo := r.pair()
	body := r.m.data.Boards[boardStreet].Seeds[0].Body
	r.do(ada, map[string]any{"t": "board_pin", "ref": "p1", "board": 0, "note": map[string]any{"title": "Welcome", "body": body}, "author": 9})
	r.settle()
	c := r.site.pins[0]
	if !c.Remake || c.Author != 0 {
		t.Fatalf("the remake's words stay the remake's: %+v", c)
	}
	if n := only(t, drain(bo), "board_added")["note"].(map[string]any); n["remake"] != true || n["author"] != nil {
		t.Fatalf("shown as the remake's: %v", n)
	}
	drain(ada)
	// A note another player wrote: the game says who, the site checks it (LedgerBoard::authorOf).
	r.do(bo, map[string]any{"t": "board_pin", "ref": "p2", "board": 0, "note": map[string]any{"title": "Hi", "body": "From Ada"}, "author": 7})
	r.settle()
	if c := r.site.pins[1]; c.Author != 7 || c.Remake {
		t.Fatalf("the claimed writer goes to the site: %+v", c)
	}
	if n := only(t, drain(ada), "board_added")["note"].(map[string]any); n["author"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("Ada wrote it: %v", n)
	}
	r.do(bo, map[string]any{"t": "board_pin", "ref": "p3", "board": 0, "note": map[string]any{"title": "Mine", "body": "x"}, "author": 9})
	r.settle()
	if c := r.site.pins[2]; c.Author != 0 {
		t.Fatalf("yourself is the poster: %+v", c)
	}
}

func TestATenNoteBoardIsFullEvenWhenTwoPinAtOnce(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, bo := r.pair()
	// Three notices; six more makes nine.
	for i := 0; i < 6; i++ {
		r.pin(ada, fmt.Sprintf("p%d", i), "n", fmt.Sprint(i))
		r.settle()
		r.now = r.now.Add(2 * time.Second)
	}
	drain(ada)
	drain(bo)
	if n := len(r.m.byBoard[boardKey(boardStreet, 0)]); n != 9 {
		t.Fatalf("nine: %d", n)
	}
	calls := len(r.site.pins)
	// Ada and Bo both pin before the site answers either: only one is asked.
	r.pin(ada, "a", "Ada's", "tenth")
	r.pin(bo, "b", "Bo's", "eleventh")
	if f := pick(t, drain(bo), "board_pinned"); f["status"] != "refused" || f["why"] != "full" || f["text"] != boardTextFull {
		t.Fatalf("Bo is told it is full while Ada's is weighed: %v", f)
	}
	r.settle()
	if len(r.site.pins) != calls+1 || r.site.count(boardStreet) != 10 {
		t.Fatalf("one more call, ten on the board: %d %d", len(r.site.pins)-calls, r.site.count(boardStreet))
	}
	if f := pick(t, drain(ada), "board_pinned"); f["status"] != "settled" {
		t.Fatalf("Ada's went up: %v", f)
	}
	r.now = r.now.Add(2 * time.Second)
	r.pin(bo, "b2", "Bo's", "again")
	if f := pick(t, drain(bo), "board_pinned"); f["why"] != "full" || len(r.site.pins) != calls+1 {
		t.Fatalf("still full: %v", f)
	}
	// The site's own count decides too: a second server instance (or a stale one) cannot pass ten.
	r.m.byBoard[boardKey(boardStreet, 0)] = r.m.byBoard[boardKey(boardStreet, 0)][:5]
	r.pin(bo, "b3", "Bo's", "sneaky")
	r.settle()
	if f := pick(t, drain(bo), "board_pinned"); f["status"] != "refused" || f["why"] != "full" || f["text"] != boardTextFull {
		t.Fatalf("the site refuses past ten: %v", f)
	}
}

func TestAPinWithNoNoteInTheLedgerIsRefused(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, bo := r.pair()
	r.site.short = true
	r.pin(ada, "p1", "x", "y")
	r.settle()
	if f := pick(t, drain(ada), "board_pinned"); f["status"] != "refused" || f["why"] != "short" || f["text"] != boardTextNoNotes {
		t.Fatalf("bag_notice_board.js:114: %v", f)
	}
	if got := drain(bo); len(got) != 0 {
		t.Fatalf("nothing on the board: %v", got)
	}
}

func TestOnePinAtATimeAndNoBoardNoPin(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, _ := r.pair()
	r.pin(ada, "p1", "a", "b")
	r.pin(ada, "p2", "c", "d")
	if f := pick(t, drain(ada), "board_pinned"); f["why"] != "busy" || f["ref"] != "p2" {
		t.Fatalf("one at a time: %v", f)
	}
	r.settle()
	drain(ada)
	r.at(ada, boardPlain)
	drain(ada)
	r.pin(ada, "p3", "a", "b")
	if f := pick(t, drain(ada), "board_pinned"); f["why"] != "none" || f["text"] != boardTextNone {
		t.Fatalf("no board on that street: %v", f)
	}
	r.at(ada, "HOME_7")
	drain(ada)
	r.pin(ada, "p4", "a", "b")
	if f := pick(t, drain(ada), "board_pinned"); f["why"] != "none" {
		t.Fatalf("a private place has no shared board: %v", f)
	}
	r.do(ada, map[string]any{"t": "board_pin", "ref": "p5", "board": 1, "note": map[string]any{"title": "a"}})
	if f := pick(t, drain(ada), "board_pinned"); f["why"] != "bad" {
		t.Fatalf("board 0 only: %v", f)
	}
	if len(r.site.pins) != 1 {
		t.Fatalf("one call in all: %d", len(r.site.pins))
	}
}

func TestASilentSiteLeavesTheBoardAsItWas(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, bo := r.pair()
	r.site.silent = true
	r.pin(ada, "p1", "a", "b")
	r.settle()
	if f := pick(t, drain(ada), "board_pinned"); f["status"] != "refused" || f["why"] != "off" {
		t.Fatalf("no answer: %v", f)
	}
	if len(drain(bo)) != 0 || len(r.m.byBoard[boardKey(boardStreet, 0)]) != 3 || len(r.m.pinning) != 0 {
		t.Fatalf("nothing pinned, nothing held")
	}
}

func TestTheFirstToTakeANoteHasIt(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, bo := r.pair()
	id := r.m.byBoard[boardKey(boardStreet, 0)][0].ID
	r.do(ada, map[string]any{"t": "board_take", "id": id})
	r.do(bo, map[string]any{"t": "board_take", "id": id})
	if f := pick(t, drain(bo), "board_taken"); f["status"] != "refused" || f["why"] != "busy" || f["text"] != boardTextTaking {
		t.Fatalf("Bo waits while Ada's take is weighed: %v", f)
	}
	r.settle()
	if len(r.site.takes) != 1 || r.site.takes[0] != id+":7" {
		t.Fatalf("one take asked: %v", r.site.takes)
	}
	af := drain(ada)
	took := pick(t, af, "board_taken")
	if took["status"] != "settled" || took["op"] != id+"-t" || took["id"] != id || took["note"].(map[string]any)["title"] != "Welcome" || took["note"].(map[string]any)["remake"] != true {
		t.Fatalf("Ada has it, with its words: %v", took)
	}
	if g := pick(t, af, "board_gone"); g["why"] != "taken" {
		t.Fatalf("Ada's board loses it: %v", g)
	}
	g := only(t, drain(bo), "board_gone")
	if g["id"] != id || g["why"] != "taken" || g["by"].(map[string]any)["name"] != "Ada" {
		t.Fatalf("Bo sees it go: %v", g)
	}
	r.now = r.now.Add(2 * time.Second)
	r.do(bo, map[string]any{"t": "board_take", "id": id})
	if f := pick(t, drain(bo), "board_taken"); f["why"] != "gone" || f["text"] != boardTextGone {
		t.Fatalf("too late: %v", f)
	}
	if len(r.site.takes) != 1 || len(r.m.byBoard[boardKey(boardStreet, 0)]) != 2 {
		t.Fatalf("still one take, two left")
	}
	// From another street a note cannot be taken.
	other := r.m.byBoard[boardKey("GHVUR3UKGRU2SD2", 0)][0].ID
	r.do(bo, map[string]any{"t": "board_take", "id": other})
	if f := pick(t, drain(bo), "board_taken"); f["why"] != "gone" {
		t.Fatalf("not on Bo's street: %v", f)
	}
}

func TestAPinAndATakeRacingOnAFullBoard(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, bo := r.pair()
	for i := 0; i < 7; i++ {
		r.pin(ada, fmt.Sprintf("p%d", i), "n", fmt.Sprint(i))
		r.settle()
		r.now = r.now.Add(2 * time.Second)
	}
	drain(ada)
	drain(bo)
	id := r.m.byBoard[boardKey(boardStreet, 0)][0].ID
	// Bo takes one while Ada pins: the take frees a place only once the site has given it.
	r.do(bo, map[string]any{"t": "board_take", "id": id})
	r.pin(ada, "x", "n", "late")
	if f := pick(t, drain(ada), "board_pinned"); f["why"] != "full" {
		t.Fatalf("full until the take settles: %v", f)
	}
	r.settle()
	r.now = r.now.Add(2 * time.Second)
	r.pin(ada, "y", "n", "now")
	r.settle()
	if f := pick(t, drain(ada), "board_pinned"); f["status"] != "settled" {
		t.Fatalf("then there is room: %v", f)
	}
	if n := len(r.m.byBoard[boardKey(boardStreet, 0)]); n != 10 || r.site.count(boardStreet) != 10 {
		t.Fatalf("ten: %d", n)
	}
}

func TestTheConsoleRemovesANoteAndTheStreetSeesItGo(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, bo := r.pair()
	r.pin(ada, "p1", "Rude", "words")
	r.settle()
	drain(ada)
	drain(bo)
	op := r.site.pins[0].Op
	code, out := serviceHandlers["board_remove"](r.hub, []byte(`{"id":"`+op+`"}`))
	if code != 200 || out.(map[string]string)["status"] != "removed" {
		t.Fatalf("removed: %d %v", code, out)
	}
	for _, cl := range []*Client{ada, bo} {
		g := only(t, drain(cl), "board_gone")
		if g["id"] != op || g["why"] != "removed" || g["by"] != nil {
			t.Fatalf("the street sees it go: %v", g)
		}
	}
	if code, out := serviceHandlers["board_remove"](r.hub, []byte(`{"id":"`+op+`"}`)); code != 200 || out.(map[string]string)["status"] != "none" {
		t.Fatalf("again: none: %v", out)
	}
	if code, _ := serviceHandlers["board_remove"](r.hub, []byte(`{"id":"x"}`)); code != 400 {
		t.Fatalf("not a note id: %d", code)
	}
	// A take of a note the site removed (the server had not heard) is refused, and it leaves the board.
	id := r.m.byBoard[boardKey(boardStreet, 0)][0].ID
	delete(r.site.rows, id)
	r.do(bo, map[string]any{"t": "board_take", "id": id})
	r.settle()
	if f := pick(t, drain(bo), "board_taken"); f["status"] != "refused" || f["why"] != "gone" {
		t.Fatalf("gone: %v", f)
	}
	if g := only(t, drain(ada), "board_gone"); g["why"] != "removed" {
		t.Fatalf("Ada's board loses it too: %v", g)
	}
}

func TestBoardKindsRateLimitAndStayOutOfPrivatePlaces(t *testing.T) {
	r := newBoardRig(t, nil)
	ada, _ := r.pair()
	for i := 0; i < boardBurst+2; i++ {
		r.hub.Handle(ada, []byte(`{"t":"board_take","id":"board-ffffffffffffffff"}`))
	}
	if n := len(of(drain(ada), "error")); n == 0 {
		t.Fatalf("slow down")
	}
	if r.m.hasBoard("HOME_7") || r.m.hasBoard(boardPlain) || !r.m.hasBoard(boardForest) {
		t.Fatalf("boards are on the data's public streets only")
	}
}

// The race detector's own check: many players pin at once through the real lock and goroutines.
func TestBoardsUnderTheRaceDetector(t *testing.T) {
	site := newFakeBoardSite()
	saved := modules
	var m *boards
	modules = []moduleFactory{func(h *Hub) Module { m = newBoards(h).(*boards); return m }}
	hub := NewHub(time.Now)
	modules = saved
	m.site = site
	var idMu sync.Mutex
	idN := 0
	m.newID = func() string { idMu.Lock(); defer idMu.Unlock(); idN++; return fmt.Sprintf("%016x", idN) }
	var wg sync.WaitGroup
	m.spawn = func(fn func()) { wg.Add(1); go func() { defer wg.Done(); fn() }() }
	hub.mu.Lock()
	m.load()
	hub.mu.Unlock()
	wg.Wait()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
			hub.mu.Lock()
			done := ok()
			hub.mu.Unlock()
			if done {
				return
			}
		}
		t.Fatalf("waited for %s", what)
	}
	waitFor("the load", func() bool { return m.loaded })
	clients := []*Client{}
	for i := int64(0); i < 12; i++ {
		cl := hub.Connect(Claims{Sub: 100 + i, Name: fmt.Sprintf("P%d", i)})
		hub.Handle(cl, []byte(`{"t":"join","street":"`+boardStreet+`","x":0,"y":0}`))
		clients = append(clients, cl)
	}
	var players sync.WaitGroup
	for i, cl := range clients {
		players.Add(1)
		go func(i int, cl *Client) {
			defer players.Done()
			b, _ := json.Marshal(map[string]any{"t": "board_pin", "ref": fmt.Sprintf("r%d", i), "board": 0, "note": map[string]any{"title": "t", "body": fmt.Sprint(i)}})
			hub.Handle(cl, b)
			hub.Handle(cl, []byte(`{"t":"board_take","id":"board-seed-`+boardStreet+`-0"}`))
		}(i, cl)
	}
	players.Wait()
	wg.Wait()
	waitFor("every answer", func() bool { return len(m.posting) == 0 && len(m.taking) == 0 && len(m.pinning) == 0 })
	hub.mu.Lock()
	n := len(m.byBoard[boardKey(boardStreet, 0)])
	hub.mu.Unlock()
	site.mu.Lock()
	sn := site.count(boardStreet)
	takes := len(site.takes)
	site.mu.Unlock()
	// Nine or ten: whether the take freed a place before the last pins were weighed is the race's.
	if n != sn || n < 9 || n > 10 {
		t.Fatalf("at most ten on the board, both sides agree: server %d site %d", n, sn)
	}
	if takes != 1 {
		t.Fatalf("one take of the one note reached the site: %d", takes)
	}
}
