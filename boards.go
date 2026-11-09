package main

// Notice Boards shared by everyone on a street (Austin 2026-11-04). In the
// original a board was one bag in the location (GameServerJS
// items/bag_notice_board.js), so every player there read the same notes:
//
//   - capacity 10 (onCreate 155); "add a note" needs a Note in the pack
//     (conditions 77-93) and refuses a full board: "The notice board is
//     already full! You'll have to remove a note before you can add another."
//     (addNote 128-131); the note leaves the pack and goes on the board
//     (134-139).
//   - "view" lists every note with its title, body, when it was written and
//     its last editor's name (updateStatus 186-221), and anyone may take one
//     (can_take true): it leaves the board and goes into the taker's pack
//     (takeNote 169-184). The board's art is "m" + how many it holds (225).
//   - A note keeps its last editor (items/note.js 251) wherever it goes, and
//     its words are cut to 150 and 1000 characters (246-247).
//
// Here, as the ground drops are (drops.go): nothing moves on the client's
// word. A pin is asked of the site (groddle-afternoon-site src/LedgerBoard.php,
// POST groddle/v1/ledger/board/pin, signed as ledger.go signs): the Note leaves
// the poster's ledger and the note waits in the site's groddle_board_notes, in
// nobody's ledger; a take (POST groddle/v1/ledger/board/take) puts it in the
// taker's. Each is recorded once per op, so asking again answers as recorded,
// and the site counts the board's notes under a lock, so a board never holds
// more than ten however the pins race. The site's rows are the truth: this
// module loads them when it starts (POST groddle/v1/ledger/boards), so a
// restart loses nothing and duplicates nothing, and that same call pins each
// board's opening notices once (data/boards.json, the remake's own words from
// the game's BOARD_PINS, marked as the remake's); after that they are the
// players' like any other note. Each game applies an op once through the
// trade slice's applied list; a game that missed the answer applies it on its
// next connect (GET groddle/v1/boards/mine) or load.
//
// A note's words go through the word filter (wordfilter.go) and the gift
// note's cleaning (gifts.go cleanNoteText) before the site sees them. The
// author shown is the note's writer: the poster, unless the site finds the
// note came to the poster unchanged from its writer (a board take or a gift;
// LedgerBoard::authorOf). A note whose body is one the remake wrote
// (data/boards.json remake) stays marked as the remake's, with no author.
//
// Client to server:
//
//	{"t":"board_pin","ref":"…","board":0,"note":{"title":"…","body":"…"},"author":7}   author: who the game says wrote it (0: the poster)
//	{"t":"board_take","id":"board-…"}
//
// Server to client:
//
//	{"t":"board_state","street":"GA5…","capacity":10,"boards":[{"board":0,"notes":[Note…]}]}   after each join on a street with a board
//	{"t":"board_added","street":"GA5…","board":0,"note":Note}                                   to the street, the poster too
//	{"t":"board_pinned","op":"board-…","ref":"…","status":"settled"|"refused","why":"…","text":"…","note":Note|null}
//	{"t":"board_taken","op":"board-…-t","id":"board-…","status":"settled"|"refused","why":"…","text":"…","note":Note|null}
//	{"t":"board_gone","street":"GA5…","board":0,"id":"board-…","by":{"id":9,"name":"Bo"}|null,"why":"taken"|"removed"}
//
//	Note: {"id":"board-…","title":"…","body":"…","author":{"id":7,"name":"Ada"}|null,"remake":bool,"at":1730000000000}
//
// The administrators' console removes a note (board_remove below, a signed
// site call as admin.go's): the site has already marked it removed; here it
// leaves the board and the street sees it go. Nothing goes back to anyone.
//
// Ours: one pin and one take at a time per player; the taker's pack is not
// asked whether it has room (the client checks before asking); a board is
// found by its street (every street with one has exactly one, board 0).

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

//go:embed data/boards.json
var boardsJSON []byte

const (
	ledgerRouteBoardPin  = "/groddle/v1/ledger/board/pin"
	ledgerRouteBoardTake = "/groddle/v1/ledger/board/take"
	ledgerRouteBoards    = "/groddle/v1/ledger/boards"

	boardLoadRetry = 15 * time.Second
	boardBurst     = 6
	boardEvery     = time.Second

	boardTextFull    = "The notice board is already full! You'll have to remove a note before you can add another." // bag_notice_board.js:130
	boardTextNoNotes = "You have no notes to post!"                                                                 // bag_notice_board.js:114
	boardTextNone    = "There is no notice board here."                                                             // ours
	boardTextBusy    = "Wait a moment, you are still pinning a note."                                               // ours
	boardTextOff     = "The notice board can't be reached right now."                                               // ours
	boardTextBad     = "Pin one note."                                                                              // ours
	boardTextGone    = "That note is no longer on the board."                                                       // ours
	boardTextTaking  = "Someone is already taking that note."                                                       // ours
)

var boardOpRe = regexp.MustCompile(`^board-[0-9a-f]{16}$`)

func init() {
	modules = append(modules, newBoards)
	serviceHandlers["board_remove"] = adminBoardRemove
}

// boardData is data/boards.json (the game's tools/build-realtime-boards.mjs).
type boardData struct {
	Capacity int                  `json:"capacity"`
	Title    int                  `json:"title"`
	Body     int                  `json:"body"`
	Boards   map[string]boardSeed `json:"boards"`
	Remake   []string             `json:"remake"`
}

type boardSeed struct {
	Seeds []giftNote `json:"seeds"`
}

// boardNote is one note on a board, as the site holds it.
type boardNote struct {
	ID         string
	Street     string
	Board      int
	Title      string
	Body       string
	Author     int64
	AuthorName string
	Remake     bool
	At         time.Time

	taking int64 // a take the site is weighing
}

func (n *boardNote) view() map[string]any {
	var author any
	if n.Author > 0 && !n.Remake {
		author = map[string]any{"id": n.Author, "name": n.AuthorName}
	}
	return map[string]any{"id": n.ID, "title": n.Title, "body": n.Body, "author": author, "remake": n.Remake, "at": n.At.UnixMilli()}
}

// boardRow is a note as the site sends it.
type boardRow struct {
	ID     string `json:"id"`
	Street string `json:"street"`
	Board  int    `json:"board"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Author int64  `json:"author"`
	Name   string `json:"name"`
	Remake bool   `json:"remake"`
	At     int64  `json:"at"` // unix seconds
}

// boardPinCall is one pin as the site is asked it.
type boardPinCall struct {
	Op       string `json:"op"`
	ID       int64  `json:"id"`
	Street   string `json:"street"`
	Board    int    `json:"board"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Author   int64  `json:"author"`
	Remake   bool   `json:"remake"`
	Capacity int    `json:"capacity"`
}

// boardAnswer is the site's word on a pin or a take. Status "" means no answer came.
type boardAnswer struct {
	Status string    `json:"status"` // settled | refused
	Why    string    `json:"why,omitempty"`
	Text   string    `json:"text,omitempty"`
	Op     string    `json:"op,omitempty"`   // a take: its own op
	By     int64     `json:"by,omitempty"`   // a take refused as gone: who has it (0: removed)
	Note   *boardRow `json:"note,omitempty"` // the note as the site keeps it
}

// boardSite is the site's ledger (httpLedger). Calls block; the module makes them off the hub lock.
type boardSite interface {
	boardPin(c boardPinCall) boardAnswer
	boardTake(op string, id int64) boardAnswer
	boardsLoad(seeds map[string][]giftNote) ([]boardRow, error)
}

func (l *httpLedger) boardAsk(route string, body []byte) boardAnswer {
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * 400 * time.Millisecond)
		}
		var a boardAnswer
		err := l.dropPost(route, body, &a)
		if err == nil && (a.Status == "settled" || a.Status == "refused") {
			return a
		}
		if err == nil {
			err = fmt.Errorf("status %q", a.Status)
		}
		log.Printf("boards: %s: %v", route, err)
	}
	return boardAnswer{}
}

func (l *httpLedger) boardPin(c boardPinCall) boardAnswer {
	body, _ := json.Marshal(c)
	return l.boardAsk(ledgerRouteBoardPin, body)
}

func (l *httpLedger) boardTake(op string, id int64) boardAnswer {
	body, _ := json.Marshal(map[string]any{"op": op, "id": id})
	return l.boardAsk(ledgerRouteBoardTake, body)
}

func (l *httpLedger) boardsLoad(seeds map[string][]giftNote) ([]boardRow, error) {
	body, _ := json.Marshal(map[string]any{"seeds": seeds})
	var out struct {
		Notes []boardRow `json:"notes"`
	}
	if err := l.dropPost(ledgerRouteBoards, body, &out); err != nil {
		return nil, err
	}
	return out.Notes, nil
}

type boards struct {
	h       *Hub
	data    boardData
	remake  map[string]bool
	site    boardSite // nil: boards are not shared (no -ledger-url, or -trade-ledger off)
	spawn   func(func())
	newID   func() string
	loaded  bool
	loading bool
	notes   map[string]*boardNote
	byBoard map[string][]*boardNote // boardKey -> notes, oldest first
	pinning map[string]int          // boardKey -> pins the site is weighing
	posting map[int64]string        // poster -> the op the site is weighing
	taking  map[int64]string        // taker -> the note the site is weighing
	rates   map[int64]*bucket
}

func boardKey(street string, board int) string { return fmt.Sprintf("%s#%d", street, board) }

func parseBoardData(raw []byte) boardData {
	var d boardData
	if err := json.Unmarshal(raw, &d); err != nil || len(d.Boards) == 0 || d.Capacity < 1 {
		panic(fmt.Sprintf("realtime: data/boards.json: %v", err))
	}
	return d
}

func newBoards(h *Hub) Module {
	m := &boards{h: h, data: parseBoardData(boardsJSON), spawn: func(fn func()) { go fn() },
		notes: map[string]*boardNote{}, byBoard: map[string][]*boardNote{}, pinning: map[string]int{},
		posting: map[int64]string{}, taking: map[int64]string{}, rates: map[int64]*bucket{},
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return hex.EncodeToString(b)
		}}
	m.remake = map[string]bool{}
	for _, b := range m.data.Remake {
		m.remake[cleanNoteText(b, m.data.Body, true)] = true
	}
	if !testing.Testing() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.site = l
				m.load()
			}
		}
	}
	return m
}

func (m *boards) Register(r *Registry) {
	r.Handle("board_pin", m.pinFrame)
	r.Handle("board_take", m.takeFrame)
	r.OnJoin(m.sendState)
	r.OnDisconnect(func(cl *Client) { delete(m.rates, cl.ID()) })
	r.Provide("boards.self", m) // the console's board_remove
	// How many notes a street's board holds (for other modules and tests).
	r.Provide("boards.count", func(street string) int { return len(m.byBoard[boardKey(street, 0)]) })
}

// hasBoard: a public street the game hangs a Notice Board on.
func (m *boards) hasBoard(street string) bool {
	_, ok := m.data.Boards[street]
	return ok && !followPrivate(street)
}

// seeds is every board's opening notices, for the site to pin once.
func (m *boards) seeds() map[string][]giftNote {
	out := map[string][]giftNote{}
	for street, b := range m.data.Boards {
		list := []giftNote{}
		for _, s := range b.Seeds {
			list = append(list, giftNote{Title: cleanNoteText(s.Title, m.data.Title, false), Body: cleanNoteText(s.Body, m.data.Body, true)})
		}
		out[street] = list
	}
	return out
}

// load asks the site for every note on every board (pinning the opening notices once), again until it answers.
func (m *boards) load() {
	if m.site == nil || m.loading || m.loaded {
		return
	}
	m.loading = true
	site := m.site
	seeds := m.seeds()
	m.spawn(func() {
		rows, err := site.boardsLoad(seeds)
		m.h.After(0, func() {
			m.loading = false
			if err != nil {
				log.Printf("boards: loading the notice boards: %v (again in %s)", err, boardLoadRetry)
				m.h.After(boardLoadRetry, m.load)
				return
			}
			m.loaded = true
			for _, row := range rows {
				m.addRow(row)
			}
			log.Printf("boards: %d notes on %d boards", len(m.notes), len(m.byBoard))
			// Players already on a board's street see what is pinned there.
			for street := range m.data.Boards {
				m.h.BroadcastStreetLocked(street, m.stateMsg(street), 0)
			}
		})
	})
}

func (m *boards) addRow(row boardRow) *boardNote {
	if !boardOpRe.MatchString(row.ID) && !boardSeedRe.MatchString(row.ID) {
		return nil
	}
	if n := m.notes[row.ID]; n != nil || !m.hasBoard(row.Street) || row.Board != 0 {
		return n
	}
	n := &boardNote{ID: row.ID, Street: row.Street, Board: row.Board, Title: row.Title, Body: row.Body, Author: row.Author, AuthorName: row.Name, Remake: row.Remake, At: time.Unix(row.At, 0)}
	if n.Author > 0 && n.AuthorName == "" {
		n.AuthorName = unnamedPlayer
	}
	k := boardKey(n.Street, n.Board)
	m.notes[n.ID] = n
	m.byBoard[k] = append(m.byBoard[k], n)
	sort.SliceStable(m.byBoard[k], func(i, j int) bool {
		a, b := m.byBoard[k][i], m.byBoard[k][j]
		return a.At.Before(b.At) || (a.At.Equal(b.At) && a.ID < b.ID)
	})
	return n
}

// boardSeedRe: the site's ids for the opening notices (LedgerBoard::seedId).
var boardSeedRe = regexp.MustCompile(`^board-seed-[A-Z0-9]{1,32}-[0-9]{1,2}$`)

func (m *boards) remove(n *boardNote) {
	delete(m.notes, n.ID)
	k := boardKey(n.Street, n.Board)
	list := m.byBoard[k]
	for i, o := range list {
		if o == n {
			m.byBoard[k] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(m.byBoard[k]) == 0 {
		delete(m.byBoard, k)
	}
}

func (m *boards) stateMsg(street string) map[string]any {
	notes := []map[string]any{}
	for _, n := range m.byBoard[boardKey(street, 0)] {
		notes = append(notes, n.view())
	}
	return map[string]any{"t": "board_state", "street": street, "capacity": m.data.Capacity, "boards": []map[string]any{{"board": 0, "notes": notes}}}
}

func (m *boards) sendState(cl *Client) {
	if m.site == nil || !m.loaded || !m.hasBoard(cl.Street()) {
		return
	}
	m.h.SendLocked(cl, m.stateMsg(cl.Street()))
}

func (m *boards) allow(cl *Client) bool {
	b := m.rates[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.rates[cl.ID()] = b
	}
	if !b.take(m.h.Now(), boardBurst, boardEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return false
	}
	return true
}

func (m *boards) refusePin(cl *Client, ref, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "board_pinned", "ref": ref, "status": "refused", "why": why, "text": text, "note": nil})
}

// pinFrame is addNote: one Note from the pack onto the street's board, through the site's ledger.
func (m *boards) pinFrame(cl *Client, frame json.RawMessage) {
	if !m.allow(cl) {
		return
	}
	var msg struct {
		Ref    string    `json:"ref"`
		Board  int       `json:"board"`
		Note   *giftNote `json:"note"`
		Author int64     `json:"author"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.Note == nil || msg.Board != 0 {
		m.refusePin(cl, "", "bad", boardTextBad)
		return
	}
	ref := ""
	if giftRefRe.MatchString(msg.Ref) {
		ref = msg.Ref
	}
	street := cl.Street()
	if !m.hasBoard(street) {
		m.refusePin(cl, ref, "none", boardTextNone)
		return
	}
	if m.site == nil || !m.loaded {
		m.refusePin(cl, ref, "off", boardTextOff)
		if m.site != nil {
			m.load()
		}
		return
	}
	if _, waiting := m.posting[cl.ID()]; waiting {
		m.refusePin(cl, ref, "busy", boardTextBusy)
		return
	}
	k := boardKey(street, msg.Board)
	// The board's ten (bag_notice_board.js onCreate 155), counting pins the site is still weighing.
	if len(m.byBoard[k])+m.pinning[k] >= m.data.Capacity {
		m.refusePin(cl, ref, "full", boardTextFull)
		return
	}
	// note.js 246-247, then the word filter (wordfilter.go), as a gift's note (gifts.go give).
	title := filterWords(cleanNoteText(msg.Note.Title, m.data.Title, false))
	body := filterWords(cleanNoteText(msg.Note.Body, m.data.Body, true))
	remake := body != "" && m.remake[body]
	author := msg.Author
	if author < 0 || author == cl.ID() || remake {
		author = 0
	}
	call := boardPinCall{Op: "board-" + m.newID(), ID: cl.ID(), Street: street, Board: msg.Board, Title: title, Body: body, Author: author, Remake: remake, Capacity: m.data.Capacity}
	from := cl.ID()
	m.posting[from] = call.Op
	m.pinning[k]++
	site := m.site
	m.spawn(func() {
		a := site.boardPin(call)
		m.h.After(0, func() {
			if m.posting[from] == call.Op {
				delete(m.posting, from)
			}
			if m.pinning[k]--; m.pinning[k] <= 0 {
				delete(m.pinning, k)
			}
			if a.Status == "" {
				// No answer: the op is idempotent at the site; a settled one reaches the game on its next
				// connect, and the note the next time the boards are loaded.
				a.Status, a.Why, a.Text = "refused", "off", boardTextOff
			}
			if a.Status == "refused" && a.Text == "" {
				switch a.Why {
				case "full":
					a.Text = boardTextFull
				case "short":
					a.Text = boardTextNoNotes
				default:
					a.Text = boardTextOff
				}
			}
			out := map[string]any{"t": "board_pinned", "op": call.Op, "ref": ref, "status": a.Status, "why": a.Why, "text": a.Text, "note": nil}
			var n *boardNote
			if a.Status == "settled" {
				row := boardRow{ID: call.Op, Street: street, Board: call.Board, Title: title, Body: body, Author: from, Remake: remake, At: m.h.Now().Unix()}
				if a.Note != nil && a.Note.ID == call.Op {
					row = *a.Note
				}
				if row.Author == from && row.Name == "" {
					if c := m.h.ClientLocked(from); c != nil {
						row.Name = c.Name()
					}
				}
				n = m.addRow(row)
				if n != nil {
					out["note"] = n.view()
				}
			}
			if c := m.h.ClientLocked(from); c != nil {
				m.h.SendLocked(c, out)
			}
			if n == nil {
				return
			}
			log.Printf("boards: %d pinned %s on %s (%d of %d)", from, n.ID, n.Street, len(m.byBoard[k]), m.data.Capacity)
			m.h.BroadcastStreetLocked(n.Street, map[string]any{"t": "board_added", "street": n.Street, "board": n.Board, "note": n.view()}, 0)
		})
	})
}

func (m *boards) refuseTake(cl *Client, id, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "board_taken", "id": id, "status": "refused", "why": why, "text": text, "note": nil})
}

// takeFrame is takeNote: the first to ask has it.
func (m *boards) takeFrame(cl *Client, frame json.RawMessage) {
	if !m.allow(cl) {
		return
	}
	var msg struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(frame, &msg) != nil || !(boardOpRe.MatchString(msg.ID) || boardSeedRe.MatchString(msg.ID)) {
		m.refuseTake(cl, "", "bad", boardTextGone)
		return
	}
	n := m.notes[msg.ID]
	if n == nil || n.Street != cl.Street() {
		m.refuseTake(cl, msg.ID, "gone", boardTextGone)
		return
	}
	if m.site == nil {
		m.refuseTake(cl, n.ID, "off", boardTextOff)
		return
	}
	if n.taking != 0 {
		m.refuseTake(cl, n.ID, "busy", boardTextTaking)
		return
	}
	if _, waiting := m.taking[cl.ID()]; waiting {
		m.refuseTake(cl, n.ID, "busy", boardTextTaking)
		return
	}
	id, name := cl.ID(), cl.Name()
	n.taking = id
	m.taking[id] = n.ID
	site := m.site
	m.spawn(func() {
		a := site.boardTake(n.ID, id)
		m.h.After(0, func() {
			n.taking = 0
			if m.taking[id] == n.ID {
				delete(m.taking, id)
			}
			if a.Status == "" {
				a.Status, a.Why, a.Text = "refused", "off", boardTextOff
			}
			out := map[string]any{"t": "board_taken", "op": a.Op, "id": n.ID, "status": a.Status, "why": a.Why, "text": a.Text, "note": nil}
			if a.Status == "settled" {
				v := n.view()
				if a.Note != nil && a.Note.ID == n.ID {
					v = (&boardNote{ID: a.Note.ID, Title: a.Note.Title, Body: a.Note.Body, Author: a.Note.Author, AuthorName: a.Note.Name, Remake: a.Note.Remake, At: time.Unix(a.Note.At, 0)}).view()
				}
				out["note"] = v
			} else if a.Text == "" {
				out["text"] = boardTextGone
			}
			if c := m.h.ClientLocked(id); c != nil {
				m.h.SendLocked(c, out)
			}
			gone := a.Status == "settled" || a.Why == "gone"
			if !gone || m.notes[n.ID] != n {
				return
			}
			m.remove(n)
			var by any
			why := "taken"
			if a.Status == "settled" {
				by = map[string]any{"id": id, "name": name}
				log.Printf("boards: %s taken by %d", n.ID, id)
			} else if a.By == 0 {
				why = "removed"
			}
			m.h.BroadcastStreetLocked(n.Street, map[string]any{"t": "board_gone", "street": n.Street, "board": n.Board, "id": n.ID, "by": by, "why": why}, 0)
		})
	})
}

// removeLocked takes a note the console removed off its board: "removed", "none" (not on a board here).
func (m *boards) removeLocked(id string) string {
	n := m.notes[id]
	if n == nil {
		return "none"
	}
	m.remove(n)
	log.Printf("boards: %s removed by an administrator", n.ID)
	m.h.BroadcastStreetLocked(n.Street, map[string]any{"t": "board_gone", "street": n.Street, "board": n.Board, "id": n.ID, "by": nil, "why": "removed"}, 0)
	return "removed"
}

// adminBoardRemove is the console's board_remove {"id":"board-…"}: 200 {"status":"removed"|"none"}.
func adminBoardRemove(h *Hub, body []byte) (int, any) {
	var c struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &c) != nil || !(boardOpRe.MatchString(c.ID) || boardSeedRe.MatchString(c.ID)) {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m, _ := h.provided["boards.self"].(*boards)
	if m == nil {
		return http.StatusOK, statusOf("none")
	}
	return http.StatusOK, statusOf(m.removeLocked(strings.TrimSpace(c.ID)))
}
