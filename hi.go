package main

// Hi signs between real players (register G327): the viral Hi of
// config_prod.js:1353-1355 (feature_hi_viral, feature_hi_records,
// feature_report_hi_records). Saying hi ("5", or "hi" in Local chat) rolls
// your daily sign when you have none; it flies as a homing missile to the
// nearest player within 400 px you have not said hi to since your sign was
// set; a player with no sign catches yours; the same sign both ways is the
// bonus. The server keeps the day's tally of who has which sign
// (groups/hi_variants_tracker.js), yesterday's winning sign and top
// spreader, and each street's evasion records. The rules are GameServerJS
// players/inc_emotes.js; the copy is theirs where marked.
//
// Client to server:
//
//	{"t":"hi_sync"}                      after connecting: your sign today, your Butler's, the board, yesterday, what you are owed
//	{"t":"hi_say"}                       you said hi (your own Butler is no target here: a shared street has none)
//	{"t":"hi_say","butler":"target"}     you said hi to your Butler at home (no player target; the Butler's half is the game's)
//	{"t":"hi_say","butler":"infect"}     ... and it had no sign, so it caught yours
//	{"t":"hi_caught","from":"butler"}    your Butler said hi first and you had no sign: you catch its sign (the server's roll)
//	{"t":"hi_hit","from":7,"secs":12}    the hi sign player 7 sent you reached you after secs of chasing
//	{"t":"hi_ack","held":2}              you counted the daily evasion records hi_state said you held
//	{"t":"hi_board"}                     the leaderboard, now (the clock's panel asks once a minute while open)
//	{"t":"powder_spread","cls":"sneezing_powder"|"sparkle_powder"}   you used the powder: who else it reaches
//
// Server to client:
//
//	{"t":"hi_state","day":n,"variant":"bats"|"","butler":"stars","board":{…},"yesterday":Winner,"held":0}   answering hi_sync, and at each new game day
//	{"t":"hi_said","variant":"bats","random":true,"target":{"id":9,"name":"Bo"}|null,"infected":true,"bonus":false,
//	     "mood":5,"counters":[["hi_variants:bats",1]],"grant":["social_butterfly"],"activity":"…"}      to you, after a hi_say
//	{"t":"hi_emoted","id":7,"variant":"bats","target":9,"mood":5}                       to the rest of the street: draw the sign and its missile
//	{"t":"hi_got","from":{"id":7,"name":"Ada"},"variant":"bats","infected":true,"bonus":false,"mood":5,"counters":[…],"activity":"…"}   to the one it flies at
//	{"t":"hi_bonus","variant":"bats","color":"#353535","other":{"id":9,"name":"Bo"},"mood":10,"counters":[…],"grant":[…]}   to both, two seconds on
//	{"t":"hi_board","board":{"bats":3,…}}                                               the street of a player whose sign was just set; and answering hi_board
//	{"t":"hi_winner","yesterday":Winner}                                                to everyone, at the game day's end
//	{"t":"hi_evaded","secs":12,"grant":["hi_flyer"],"activity":"…"}                    to the one who evaded, after a good hi_hit
//	{"t":"hi_record","id":9,"name":"Bo","msg":"…"}                                      to the street: a record set, tied or broken there
//	{"t":"powder_spread","cls":"…","ok":true,"players":5,"counters":[…]}                to the user: go ahead (or ok false, why "crowd")
//	{"t":"powder_caught","cls":"…","from":{"id":7,"name":"Ada"}}                       to each other player on the street
//	{"t":"powder_sneeze","id":9,"last":false}                                            to the sneezer's street, at each sneeze
//
// Winner is {"variant","count","top":{"id","name","count","variant"}|null}.
//
// Rules kept from the source:
//   - Your sign (setHiEmoteVariant, inc_emotes.js:3-47): set once a game day,
//     rolled at your first hi (setHiEmoteVariantRandomly 49-57) unless someone
//     passed you theirs first (setHiEmoteVariantFromInfector 59-80); a new sign
//     starts the list of players you said hi to over (keeping yesterday's for
//     late missiles); doNewDay deletes it (players/player.js:1054).
//   - The target (doEmote 82-200): players within 400 px, not blocking or
//     blocked (buddies.ignoring, 145-146), not said hi to since your sign was
//     set, one in front of you before one behind. Here the nearest of each.
//   - A target with no sign catches yours (infected_pc / infected_by_pc, 70-71);
//     with your sign and having said hi to you since theirs was set, the bonus
//     two seconds on (doHiEmoteBonusWithOtherPlayer 335-363: you +10 mood, they
//     +15, hi_jackpot both ways, Hi and Mighty at 17 different players in a
//     day); otherwise they get 5 mood (218). said_hi daily, Social Butterfly
//     at 53 different players in a day (204-208); hi_variants (210).
//   - The tally (hi_variants_tracker.js): counts per sign today, infections per
//     spreader per sign (players only count for top spreader, 66), at the day's
//     end yesterday's winner and top spreader told to everyone (100-111), the
//     day kept in the history; the board sent to the street of the player whose
//     sign was just set (inc_emotes.js:32-39) and on request (main.js:7685-7690).
//   - Evasion (doHiEmoteMissileHit 516-628): good only for a missile the sender
//     really sent you (today's or yesterday's targets), no more than 5 s less
//     than the time since it was sent (589); Hi Flyer 11 s, Hi Speed Chase 61,
//     Hi As a Kite 151, Hi Velocity 311; from 10 s the street's daily and
//     all-time records (maybe_set_evasion_record 366-475; an instance counts for
//     its template, 371); "You evaded that hi sign for N seconds!" from 5 s;
//     each daily record held is remembered for four game days and counted
//     (daily_evasion_record:held) at the holder's next new day
//     (hi_variants_tracker.js:125-165, player.js:923-925).
//   - Powders (items/sneezing_powder.js:241-331, sparkle_powder.js:240-341):
//     Sneezing Powder gives every other player on the street the Sneezing buff
//     (21 s: a sneeze 0-8 s in and at 13 s, the bean at the end,
//     catalog_buffs.js:2411-2445, player.js:2632-2652), refused over 15 players;
//     Sparkle Powder +20 mood to every other player, refused over 10;
//     Sneeze-O-Rama at 5 others, Sparkle Buddy at 7 (powders:sneezeorama,
//     powders:sparkle_buddy).
//
// Ours: your Butler is the game's own (it lives on your home street, where no
// one else is), but its daily sign is rolled here (hi_state "butler") so a
// sign you catch from it is still the server's roll; a missile counts once;
// it cannot be evaded for longer than it has flown (the source only checked
// the other way); hi_say at most one per 1.9 s, powders one per 2 s; a race's
// street keeps no records; a player already sneezing does not catch another
// round.
//
// Trust. The server decides every sign, target, infection, bonus, count and
// record from the positions it holds and its own clock. Mood and badges are
// the client's to apply to its own save from these messages, as every
// reward is. A powder's use is the client's word that it held the jar (the
// server cannot see packs, the line projects.go draws for tools): the jar's
// charge is the user's own, nothing of value moves, and the pace is held.
//
// Persistence: the day's signs, targets, the tally, the records and what is
// owed are written to -hi-file (default realtime-hi.json beside
// -secret-file; off: memory only) a second after a change.

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// config_base.js:435 (hi_overlay.as order), 436-461 colours and names.
var hiVariants = []string{"bats", "birds", "butterflies", "cubes", "flowers", "hands", "hearts", "hi", "pigs", "rocketships", "stars"}

var hiVariantNames = map[string]string{"bats": "Bats", "birds": "Birds", "butterflies": "Butterflies", "cubes": "Cubes", "flowers": "Flowers",
	"hands": "Hands", "hearts": "Hearts", "hi": "HI", "pigs": "Pigs", "rocketships": "Rockets", "stars": "Stars"}

var hiVariantColors = map[string]string{"bats": "#353535", "birds": "#9afe66", "butterflies": "#ce42ab", "cubes": "#45669c", "flowers": "#50c7cf",
	"hands": "#ffffff", "hearts": "#840607", "hi": "#f67e30", "pigs": "#ef7e6c", "rocketships": "#cb0303", "stars": "#ffd205"}

const (
	hiBaseMood       = 5  // config_base.js:463 hi_emote_base_mood
	hiBonusMood      = 10 // config_base.js:464 hi_emote_bonus_mood
	hiRadius         = 400.0
	hiBonusDelay     = 2 * time.Second // inc_emotes.js:214 apiSetTimer 2000
	hiEvery          = 1900 * time.Millisecond
	hiButterflyCount = 53 // inc_emotes.js:205
	hiMightyCount    = 17 // inc_emotes.js:356
	hiRecordDays     = 4  // hi_variants_tracker.js:16
	hiMinRecordSecs  = 10 // inc_emotes.js:525
	hiMinFeedback    = 5  // inc_emotes.js:526
	hiLagSecs        = 5  // inc_emotes.js:589
	hiSlackSecs      = 2  // ours: a missile can't be evaded longer than it flew, but for rounding and lag
	hiAlgoVersion    = 10 // Missile.as ALGO_VERSION; maybe_set_evasion_record ignores < 10
	hiHistoryDays    = 30 // ours: days of tally history kept
	hiSaveDelay      = time.Second

	powderEvery       = 2 * time.Second
	powderSneezeMax   = 15               // sneezing_powder.js:254 getNumActivePlayers() > 15
	powderSparkleMax  = 10               // sparkle_powder.js:252 > 10
	powderSneezeRama  = 5                // sneezing_powder.js:327
	powderSparkleBud  = 7                // sparkle_powder.js:340
	powderSneezeLast  = 21 * time.Second // catalog_buffs.js:2414 duration 21
	powderSneezeLater = 13 * time.Second // catalog_buffs.js:2423
	powderSneezeEarly = 8                // catalog_buffs.js:2421 randInt(0, 8)
)

// hiEvasionBadges is doHiEmoteMissileHit's achievements (inc_emotes.js:595-609).
var hiEvasionBadges = []struct {
	secs int
	id   string
}{{11, "hi_flyer"}, {61, "hi_speed_chase"}, {151, "hi_as_a_kite"}, {311, "hi_velocity"}}

var hiFileFlag = flag.String("hi-file", "", "file that keeps the day's hi signs, the tally, the evasion records and what players are owed (default: realtime-hi.json beside -secret-file; off: memory only)")

// hiFileForTest lets a test give the module a state file; under go test the flag is ignored.
var hiFileForTest string

func init() { modules = append(modules, newHi) }

// hiPlayer is one character's hi state.
type hiPlayer struct {
	Name         string          `json:"name,omitempty"`
	Day          int64           `json:"day,omitempty"` // the game day Variant was set
	Variant      string          `json:"v,omitempty"`
	Pcs          map[int64]int64 `json:"pcs,omitempty"`  // whom you said hi to since Variant was set (ms)
	PcsYesterday map[int64]int64 `json:"pcsY,omitempty"` // and before that (late missiles)
	ButlerDay    int64           `json:"bday,omitempty"`
	Butler       string          `json:"b,omitempty"`    // your Butler's sign for ButlerDay, when it needs one
	ButlerInf    int64           `json:"binf,omitempty"` // the game day you passed your sign to your Butler (counted once)
	DailyDay     int64           `json:"dday,omitempty"`
	SaidHi       map[int64]bool  `json:"said,omitempty"` // said_hi today (achievements_increment_daily)
	Jackpot      map[int64]bool  `json:"jack,omitempty"` // hi_jackpot today
	HeldDay      int64           `json:"hday,omitempty"` // the last new day the held records were counted
	Held         int             `json:"held,omitempty"` // counted and not yet acked
}

type hiRecord struct {
	ID      int64  `json:"id"`
	Label   string `json:"label"`
	Secs    int    `json:"secs"`
	When    int64  `json:"when"`
	Version int    `json:"version"`
	Day     int64  `json:"day"`
}

type hiLoc struct {
	Alltime *hiRecord            `json:"alltime,omitempty"`
	Daily   *hiRecord            `json:"daily,omitempty"`
	History map[string]*hiRecord `json:"history,omitempty"`
}

type hiTop struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Variant string `json:"variant"`
}

type hiWinner struct {
	Variant string `json:"variant"`
	Count   int    `json:"count"`
	Top     *hiTop `json:"top"`
}

type hiDayRow struct {
	Winner hiWinner       `json:"winner"`
	Counts map[string]int `json:"counts"`
}

type hiTracker struct {
	Day       int64                    `json:"day"`
	Counts    map[string]int           `json:"counts"`
	Infectors map[string]map[int64]int `json:"infectors"` // sign -> spreader -> players infected today
	Yesterday hiWinner                 `json:"yesterday"`
	Records   []map[string]int64       `json:"records"` // daily_evasion_records.A: [0] today, a street's holder
	History   map[int64]hiDayRow       `json:"history"`
}

type hiState struct {
	Players map[int64]*hiPlayer `json:"players"`
	Tracker hiTracker           `json:"tracker"`
	Locs    map[string]*hiLoc   `json:"locs"`
}

type hiModule struct {
	h        *Hub
	st       *hiState
	path     string
	dirty    bool
	saving   bool
	rollAt   int64 // the game day the end-of-day timer is set for (0: none)
	toldDay  int64 // the game day whose start everyone online was told (hi_winner)
	roll     func(n int) int
	sayAt    map[int64]time.Time
	powderAt map[int64]time.Time
	hitDone  map[[2]int64]int64      // (from, to) -> the sent-at of the missile already counted
	sneezing map[int64][]func() bool // a player's sneezes still to come
}

func newHiState() *hiState {
	return &hiState{Players: map[int64]*hiPlayer{}, Locs: map[string]*hiLoc{},
		Tracker: hiTracker{Counts: map[string]int{}, Infectors: map[string]map[int64]int{}, Records: []map[string]int64{{}}, History: map[int64]hiDayRow{}}}
}

func newHi(h *Hub) Module {
	path := hiFileForTest
	if !testing.Testing() && hiFileForTest == "" {
		path = *hiFileFlag
		if path == "" {
			if f := flag.Lookup("secret-file"); f != nil && f.Value.String() != "" {
				path = filepath.Join(filepath.Dir(expandHome(f.Value.String())), "realtime-hi.json")
			}
		}
	}
	if path == "off" {
		path = ""
	}
	return newHiWith(h, path)
}

func newHiWith(h *Hub, path string) *hiModule {
	m := &hiModule{h: h, st: newHiState(), path: path, roll: rand.IntN, sayAt: map[int64]time.Time{}, powderAt: map[int64]time.Time{},
		hitDone: map[[2]int64]int64{}, sneezing: map[int64][]func() bool{}}
	if path != "" {
		if err := m.load(); err != nil {
			log.Printf("hi %s: %v (hi signs are kept in memory only)", path, err)
			m.path = ""
		} else {
			log.Printf("hi %s: %d players today, %d streets with evasion records", path, len(m.st.Players), len(m.st.Locs))
		}
	}
	if m.st.Tracker.Day == 0 {
		m.st.Tracker.Day = m.today()
	}
	m.toldDay = m.st.Tracker.Day
	return m
}

func (m *hiModule) Register(r *Registry) {
	r.Handle("hi_sync", m.sync)
	r.Handle("hi_say", m.say)
	r.Handle("hi_caught", m.caught)
	r.Handle("hi_hit", m.hit)
	r.Handle("hi_ack", m.ack)
	r.Handle("hi_board", m.boardMsg)
	r.Handle("powder_spread", m.powder)
	r.OnDisconnect(func(cl *Client) {
		delete(m.sayAt, cl.ID())
		delete(m.powderAt, cl.ID())
	})
}

// —— Days ——

func (m *hiModule) today() int64 {
	return int64(math.Floor(float64(m.h.Now().UnixMilli()-gameEpochMs) / gameDayMs))
}

// checkReset is hi_variants_tracker.js check_for_reset (39-45): a new game day ends the old one.
func (m *hiModule) checkReset() bool {
	today := m.today()
	t := &m.st.Tracker
	if t.Day == today {
		return false
	}
	// reset (48-121): yesterday's winning sign and top spreader, kept in the history.
	win := hiWinner{}
	for _, v := range hiVariants {
		if n := t.Counts[v]; n > win.Count {
			win.Count, win.Variant = n, v
		}
	}
	for _, v := range hiVariants {
		ids := make([]int64, 0, len(t.Infectors[v]))
		for id := range t.Infectors[v] {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			if n := t.Infectors[v][id]; n > 0 && (win.Top == nil || n > win.Top.Count) {
				win.Top = &hiTop{ID: id, Name: m.nameOf(id), Count: n, Variant: v}
			}
		}
	}
	if t.Day > 0 {
		t.History[t.Day] = hiDayRow{Winner: win, Counts: t.Counts}
	}
	for d := range t.History {
		if d <= today-hiHistoryDays {
			delete(t.History, d)
		}
	}
	// feature_report_hi_records (113-120): a new day at the front, four kept (one per day passed: ours).
	gap := today - t.Day
	if t.Day <= 0 || gap > hiRecordDays {
		gap = hiRecordDays
	}
	for i := int64(0); i < gap; i++ {
		t.Records = append([]map[string]int64{{}}, t.Records...)
	}
	if len(t.Records) > hiRecordDays {
		t.Records = t.Records[:hiRecordDays]
	}
	// Only yesterday's winner is yesterday's: after a longer gap there was none.
	if today-t.Day == 1 {
		t.Yesterday = win
	} else {
		t.Yesterday = hiWinner{}
	}
	t.Day = today
	t.Counts = map[string]int{}
	t.Infectors = map[string]map[int64]int{}
	m.prune(today)
	m.changed()
	return true
}

// prune forgets players with nothing left from yesterday or today.
func (m *hiModule) prune(today int64) {
	for id, p := range m.st.Players {
		if p.Held == 0 && p.Day < today-1 && p.DailyDay < today && p.ButlerDay < today && m.h.ClientLocked(id) == nil {
			delete(m.st.Players, id)
		}
	}
}

// endOfDay runs at each game day's end while the server is up: the tally resets and everyone hears
// yesterday's winner (feature_hi_viral, apiSendToAll), and each player online starts their new day.
func (m *hiModule) endOfDay() {
	m.rollAt = 0
	m.checkReset()
	if m.toldDay != m.st.Tracker.Day {
		m.toldDay = m.st.Tracker.Day
		msg := map[string]any{"t": "hi_winner", "yesterday": m.st.Tracker.Yesterday}
		for _, cl := range m.onlineLocked() {
			m.h.SendLocked(cl, msg)
			m.sendState(cl)
		}
	}
	m.armEndOfDay()
}

func (m *hiModule) armEndOfDay() {
	today := m.today()
	if m.rollAt == today+1 {
		return
	}
	m.rollAt = today + 1
	next := time.UnixMilli(gameEpochMs + (today+1)*gameDayMs)
	m.h.After(next.Sub(m.h.Now())+50*time.Millisecond, m.endOfDay)
}

// onlineLocked is every character online (the hub's own map: the module helpers list one street).
func (m *hiModule) onlineLocked() []*Client {
	out := make([]*Client, 0, len(m.h.clients))
	for _, cl := range m.h.clients {
		out = append(out, cl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// player is a character's state, brought to today: a sign from an earlier day is gone (doNewDay),
// the daily counters start over, and at their new day the evasion records they held are counted.
func (m *hiModule) player(id int64, name string) *hiPlayer {
	m.checkReset()
	today := m.today()
	p := m.st.Players[id]
	if p == nil {
		p = &hiPlayer{}
		m.st.Players[id] = p
		m.changed()
	}
	if name != "" && p.Name != name {
		p.Name = name
		m.changed()
	}
	if p.Variant != "" && p.Day != today {
		p.Variant = ""
		m.changed()
	}
	if p.DailyDay != today {
		p.DailyDay, p.SaidHi, p.Jackpot = today, nil, nil
		m.changed()
	}
	if p.HeldDay != today {
		// calculate_pcs_daily_evasion_record_achieves (hi_variants_tracker.js:140-165).
		held := 0
		for _, row := range m.st.Tracker.Records {
			for loc, who := range row {
				if who == id {
					held++
					delete(row, loc)
				}
			}
		}
		p.HeldDay = today
		p.Held += held
		m.changed()
	}
	return p
}

func (m *hiModule) nameOf(id int64) string {
	if cl := m.h.ClientLocked(id); cl != nil {
		return cl.Name()
	}
	if p := m.st.Players[id]; p != nil {
		return p.Name
	}
	return ""
}

func (m *hiModule) variantOf(p *hiPlayer) string {
	if p.Variant != "" && p.Day == m.today() {
		return p.Variant
	}
	return ""
}

// setVariant is setHiEmoteVariant (inc_emotes.js:3-47): once a day; a new sign starts the targets over.
// infector 0 is a roll or a Butler (no spreader to count).
func (m *hiModule) setVariant(id int64, p *hiPlayer, v string, infector int64, street string) bool {
	if m.variantOf(p) != "" || !hiValid(v) {
		return false
	}
	p.Variant, p.Day = v, m.today()
	p.PcsYesterday, p.Pcs = p.Pcs, map[int64]int64{}
	// increment_count (hi_variants_tracker.js:168-181).
	t := &m.st.Tracker
	t.Counts[v]++
	if infector > 0 {
		if t.Infectors[v] == nil {
			t.Infectors[v] = map[int64]int{}
		}
		t.Infectors[v][infector]++
	}
	m.changed()
	// feature_hi_viral: the board to the location of the player whose sign was set.
	if street != "" {
		m.h.BroadcastStreetLocked(street, map[string]any{"t": "hi_board", "board": m.board()}, 0)
	} else if cl := m.h.ClientLocked(id); cl != nil {
		m.h.SendLocked(cl, map[string]any{"t": "hi_board", "board": m.board()})
	}
	return true
}

func hiValid(v string) bool {
	for _, x := range hiVariants {
		if x == v {
			return true
		}
	}
	return false
}

func (m *hiModule) randomVariant() string { return hiVariants[m.roll(len(hiVariants))] }

// board is get_all_counts (hi_variants_tracker.js:190-197): every sign, 0 when nobody has it.
func (m *hiModule) board() map[string]int {
	out := make(map[string]int, len(hiVariants))
	for _, v := range hiVariants {
		out[v] = m.st.Tracker.Counts[v]
	}
	return out
}

// butlerRoll is your Butler's sign for today, rolled once a day (ours: the server rolls it, so a sign
// caught from it is still the server's).
func (m *hiModule) butlerRoll(p *hiPlayer) string {
	if today := m.today(); p.ButlerDay != today || !hiValid(p.Butler) {
		p.ButlerDay, p.Butler = today, m.randomVariant()
		m.changed()
	}
	return p.Butler
}

// —— Connect and state ——

// sync answers a game that just connected; it also sets the end-of-day timer (a connect alone is quiet).
func (m *hiModule) sync(cl *Client, _ json.RawMessage) {
	m.armEndOfDay()
	m.sendState(cl)
}

func (m *hiModule) sendState(cl *Client) {
	p := m.player(cl.ID(), cl.Name())
	m.h.SendLocked(cl, map[string]any{"t": "hi_state", "day": m.today(), "variant": m.variantOf(p), "butler": m.butlerRoll(p),
		"board": m.board(), "yesterday": m.st.Tracker.Yesterday, "held": p.Held})
}

func (m *hiModule) ack(cl *Client, frame json.RawMessage) {
	var msg struct {
		Held int `json:"held"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.Held <= 0 {
		return
	}
	p := m.player(cl.ID(), cl.Name())
	if msg.Held > p.Held {
		msg.Held = p.Held
	}
	if msg.Held > 0 {
		p.Held -= msg.Held
		m.changed()
	}
}

func (m *hiModule) boardMsg(cl *Client, _ json.RawMessage) {
	m.checkReset()
	m.h.SendLocked(cl, map[string]any{"t": "hi_board", "board": m.board()})
}

// —— Saying hi (doEmote) ——

func (m *hiModule) say(cl *Client, frame json.RawMessage) {
	var msg struct {
		Butler string `json:"butler"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	now := m.h.Now()
	if at, ok := m.sayAt[cl.ID()]; ok && now.Sub(at) < hiEvery {
		return
	}
	m.sayAt[cl.ID()] = now
	street := cl.Street()
	me := m.player(cl.ID(), cl.Name())
	random := false
	variant := m.variantOf(me)
	if variant == "" {
		variant = m.randomVariant()
		m.setVariant(cl.ID(), me, variant, 0, street)
		random = true
	}
	name := hiVariantNames[variant]
	out := map[string]any{"t": "hi_said", "variant": variant, "random": random, "target": nil, "infected": false, "bonus": false}
	// Your own Butler: the game runs its half (no player target, inc_emotes.js:128-137 prefers it).
	if msg.Butler == "target" || msg.Butler == "infect" {
		if msg.Butler == "infect" && me.ButlerInf != m.today() {
			// Your Butler caught your sign: setHiEmoteVariantFromInfector(pc) counts you as its spreader.
			me.ButlerInf = m.today()
			t := &m.st.Tracker
			t.Counts[variant]++
			if t.Infectors[variant] == nil {
				t.Infectors[variant] = map[int64]int{}
			}
			t.Infectors[variant][cl.ID()]++
			m.changed()
		}
		m.h.SendLocked(cl, out)
		return
	}
	var counters [][2]any
	var grant []string
	target := m.target(cl, me, street)
	activity := ""
	if target == nil {
		if random {
			activity = `You got the daily hi sign "` + name + `"!`
		}
		out["activity"] = activity
		m.h.SendLocked(cl, out)
		if street != "" {
			m.h.BroadcastStreetLocked(street, map[string]any{"t": "hi_emoted", "id": cl.ID(), "variant": variant, "target": 0}, cl.ID())
		}
		return
	}
	them := m.player(target.ID(), target.Name())
	theirs := m.variantOf(them)
	infected := false
	if theirs == "" {
		infected = m.setVariant(target.ID(), them, variant, cl.ID(), street)
		theirs = variant
		counters = append(counters, [2]any{"infected_pc:" + variant, 1})
	}
	if me.Pcs == nil {
		me.Pcs = map[int64]int64{}
	}
	me.Pcs[target.ID()] = now.UnixMilli()
	_, saidBack := them.Pcs[cl.ID()]
	bonus := theirs == variant && saidBack
	if me.SaidHi == nil {
		me.SaidHi = map[int64]bool{}
	}
	me.SaidHi[target.ID()] = true
	if len(me.SaidHi) >= hiButterflyCount {
		grant = append(grant, "social_butterfly")
	}
	counters = append(counters, [2]any{"hi_variants:" + variant, 1})
	m.changed()
	mood := 0
	if bonus {
		a, b := cl.ID(), target.ID()
		m.h.After(hiBonusDelay, func() { m.bonus(a, b, variant) })
	} else {
		mood = hiBaseMood
	}
	// feature_hi_viral's lines (inc_emotes.js:241-283).
	them1 := target.Name()
	switch {
	case infected && random:
		activity = `You got the daily hi sign "` + name + `", and passed it on to ` + them1 + `!`
	case infected:
		activity = `You just passed your hi sign to ` + them1 + `, now they have "` + name + `"!`
	case random:
		activity = `You got the daily hi sign "` + name + `"!`
	}
	if bonus {
		if activity != "" {
			activity += " Plus, you"
		} else {
			activity = "You"
		}
		activity += ` and ` + them1 + ` have the same hi sign "` + name + `". Bonus time!`
	}
	theirLine := ""
	if infected {
		// The source's line, with "5" alone (H is the remake's Feat key) and a colon for its dash.
		theirLine = cl.Name() + ` just passed their daily hi sign to you: you've got "` + name + `"! Say "hi" back with the "5" key and get a bonus!`
	} else if bonus {
		theirLine = `You and ` + cl.Name() + ` have the hi sign "` + name + `". Bonus time!`
	}
	out["target"] = map[string]any{"id": target.ID(), "name": target.Name()}
	out["infected"], out["bonus"], out["mood"] = infected, bonus, mood
	out["counters"], out["grant"], out["activity"] = counters, grant, activity
	m.h.SendLocked(cl, out)
	var theirCounters [][2]any
	if infected {
		theirCounters = append(theirCounters, [2]any{"infected_by_pc:" + variant, 1})
	}
	m.h.SendLocked(target, map[string]any{"t": "hi_got", "from": map[string]any{"id": cl.ID(), "name": cl.Name()}, "variant": theirs,
		"infected": infected, "bonus": bonus, "mood": mood, "counters": theirCounters, "activity": theirLine})
	m.h.BroadcastStreetLocked(street, map[string]any{"t": "hi_emoted", "id": cl.ID(), "variant": variant, "target": target.ID(), "mood": mood}, cl.ID())
}

// target is doEmote's choice (inc_emotes.js:139-170): within 400 px, neither blocking the other, not
// said hi to since your sign was set; one in front of you first, else one behind (the nearest of each).
func (m *hiModule) target(cl *Client, me *hiPlayer, street string) *Client {
	if street == "" {
		return nil
	}
	x, y := cl.Pos()
	facing := cl.player.Dir
	var front, behind *Client
	var fd, bd float64
	for _, o := range m.h.StreetLocked(street) {
		if o.ID() == cl.ID() || blockedBetween(m.h, cl.ID(), o.ID()) {
			continue
		}
		if _, done := me.Pcs[o.ID()]; done {
			continue
		}
		ox, oy := o.Pos()
		d := math.Hypot(ox-x, oy-y)
		if d > hiRadius {
			continue
		}
		isBehind := (facing >= 0 && ox < x) || (facing < 0 && ox > x)
		if isBehind {
			if behind == nil || d < bd || (d == bd && o.ID() < behind.ID()) {
				behind, bd = o, d
			}
		} else if front == nil || d < fd || (d == fd && o.ID() < front.ID()) {
			front, fd = o, d
		}
	}
	if front != nil {
		return front
	}
	return behind
}

// bonus is doHiEmoteBonusWithOtherPlayer (inc_emotes.js:335-363): +10 mood for the one who said hi, +15
// for the other, the hi_jackpot counters both ways, Hi and Mighty at 17 different players today.
func (m *hiModule) bonus(a, b int64, variant string) {
	for _, pair := range [][3]int64{{a, b, hiBonusMood}, {b, a, hiBaseMood + hiBonusMood}} {
		self, other, mood := pair[0], pair[1], int(pair[2])
		p := m.player(self, m.nameOf(self))
		if p.Jackpot == nil {
			p.Jackpot = map[int64]bool{}
		}
		p.Jackpot[other] = true
		var grant []string
		if len(p.Jackpot) >= hiMightyCount {
			grant = append(grant, "hi_and_mighty")
		}
		m.changed()
		if cl := m.h.ClientLocked(self); cl != nil {
			m.h.SendLocked(cl, map[string]any{"t": "hi_bonus", "variant": variant, "color": hiVariantColors[variant],
				"other": map[string]any{"id": other, "name": m.nameOf(other)}, "mood": mood,
				"counters": [][2]any{{"hi_jackpot:" + strconv.FormatInt(other, 10), 1}}, "grant": grant})
		}
	}
}

// caught: your Butler said hi first while you had no sign, so you took its sign: the server's roll for it today.
func (m *hiModule) caught(cl *Client, frame json.RawMessage) {
	var msg struct {
		From string `json:"from"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.From != "butler" {
		return
	}
	p := m.player(cl.ID(), cl.Name())
	if m.variantOf(p) == "" {
		m.setVariant(cl.ID(), p, m.butlerRoll(p), 0, cl.Street())
	}
	m.sendState(cl)
}

// —— Evasion (doHiEmoteMissileHit, maybe_set_evasion_record) ——

func (m *hiModule) hit(cl *Client, frame json.RawMessage) {
	var msg struct {
		From int64 `json:"from"`
		Secs int   `json:"secs"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.From <= 0 || msg.Secs < 0 || msg.Secs > 1_000_000 {
		return
	}
	street := cl.Street()
	from := m.st.Players[msg.From]
	if from == nil || street == "" || msg.From == cl.ID() {
		return
	}
	sent, ok := from.Pcs[cl.ID()]
	if !ok {
		sent, ok = from.PcsYesterday[cl.ID()]
	}
	key := [2]int64{msg.From, cl.ID()}
	if !ok || m.hitDone[key] == sent {
		log.Printf("hi: %d reported a hi from %d it was never sent (HI_EVASION_SEEMS_BOGUS)", cl.ID(), msg.From)
		return
	}
	flown := float64(m.h.Now().UnixMilli()-sent) / 1000
	if flown-float64(msg.Secs) > hiLagSecs || float64(msg.Secs) > flown+hiSlackSecs {
		log.Printf("hi: %d evaded %d s of a hi that flew %.1f s (HI_EVASION_SEEMS_BOGUS)", cl.ID(), msg.Secs, flown)
		return
	}
	m.hitDone[key] = sent
	for k, at := range m.hitDone {
		if m.h.Now().UnixMilli()-at > 2*gameDayMs {
			delete(m.hitDone, k)
		}
	}
	var grant []string
	for _, b := range hiEvasionBadges {
		if msg.Secs >= b.secs {
			grant = append(grant, b.id)
		}
	}
	var status, recMsg string
	if msg.Secs >= hiMinRecordSecs {
		status, recMsg = m.record(cl, street, msg.Secs)
	}
	out := map[string]any{"t": "hi_evaded", "secs": msg.Secs, "grant": grant, "record": status}
	if status != "" && status != "fail" {
		// feature_report_hi_records: the whole location hears it (inc_emotes.js:624).
		m.h.BroadcastStreetLocked(street, map[string]any{"t": "hi_record", "id": cl.ID(), "name": cl.Name(), "msg": recMsg}, 0)
	} else if msg.Secs >= hiMinFeedback {
		out["activity"] = "You evaded that hi sign for " + strconv.Itoa(msg.Secs) + " seconds!"
	}
	m.h.SendLocked(cl, out)
}

// recordStreet is the street a record is kept for: an instance's template (inc_emotes.js:371); a race's
// street keeps none (ours).
func (m *hiModule) recordStreet(street string) string {
	if strings.HasPrefix(street, "RACE_") {
		return ""
	}
	if fn, ok := m.h.ProvidedLocked("party.spaceStreet").(func(string) string); ok {
		if t := fn(street); t != "" {
			return t
		}
	}
	if strings.HasPrefix(street, "PARTY_") {
		return ""
	}
	return street
}

// record is maybe_set_evasion_record + make_and_store_evasion_record (inc_emotes.js:366-514).
func (m *hiModule) record(cl *Client, street string, secs int) (string, string) {
	loc := m.recordStreet(street)
	if loc == "" {
		return "", ""
	}
	today := m.today()
	r := m.st.Locs[loc]
	if r == nil {
		r = &hiLoc{}
		m.st.Locs[loc] = r
	}
	if r.Daily != nil && r.Daily.Day != today {
		r.Daily = nil
	}
	if r.Alltime != nil && r.Alltime.Version < hiAlgoVersion {
		if r.History == nil {
			r.History = map[string]*hiRecord{}
		}
		r.History["version_"+strconv.Itoa(r.Alltime.Version)] = r.Alltime
		r.Alltime = nil
	}
	status, text := "", ""
	alltime := false
	s := strconv.Itoa(secs)
	switch {
	case r.Alltime == nil:
		alltime, status, text = true, "set_alltime", s+" seconds set the ALL TIME hi sign evasion record for this location!"
	case secs > r.Alltime.Secs:
		alltime, status, text = true, "broke_alltime", s+" seconds broke the previous ALL TIME hi sign evasion record of "+strconv.Itoa(r.Alltime.Secs)+" seconds for this location, set by "+r.Alltime.Label+"!"
	case secs == r.Alltime.Secs:
		alltime, status, text = true, "tied_alltime", s+" seconds tied the previous ALL TIME hi sign evasion record for this location, set by "+r.Alltime.Label+"!"
	}
	// The daily check runs after the all-time one and its words win (inc_emotes.js:439-464).
	switch {
	case r.Daily == nil:
		status, text = "set_daily", s+" seconds set today's hi sign evasion record for this location!"
	case secs > r.Daily.Secs:
		status, text = "broke_daily", s+" seconds broke today's hi sign evasion record of "+strconv.Itoa(r.Daily.Secs)+" seconds for this location, set by "+r.Daily.Label+"!"
	case secs == r.Daily.Secs:
		status, text = "tied_daily", s+" seconds tied today's hi sign evasion record for this location, set by "+r.Daily.Label+"!"
	}
	if status == "" {
		return "fail", ""
	}
	log.Printf("hi: %s (%d) %s on %s: %d s", cl.Name(), cl.ID(), status, loc, secs)
	ob := &hiRecord{ID: cl.ID(), Label: cl.Name(), Secs: secs, When: m.h.Now().Unix(), Version: hiAlgoVersion, Day: today}
	// remember_pcs_daily_evasion_record (hi_variants_tracker.js:125-137): this street's holder today.
	if len(m.st.Tracker.Records) == 0 {
		m.st.Tracker.Records = []map[string]int64{{}}
	}
	if m.st.Tracker.Records[0] == nil {
		m.st.Tracker.Records[0] = map[string]int64{}
	}
	m.st.Tracker.Records[0][loc] = cl.ID()
	r.Daily = ob
	if alltime {
		cp := *ob
		r.Alltime = &cp
	}
	m.changed()
	return status, text
}

// —— Powders that spread (sneezing_powder.js, sparkle_powder.js) ——

func (m *hiModule) powder(cl *Client, frame json.RawMessage) {
	var msg struct {
		Cls string `json:"cls"`
	}
	if json.Unmarshal(frame, &msg) != nil || (msg.Cls != "sneezing_powder" && msg.Cls != "sparkle_powder") {
		return
	}
	now := m.h.Now()
	if at, ok := m.powderAt[cl.ID()]; ok && now.Sub(at) < powderEvery {
		m.h.SendLocked(cl, map[string]any{"t": "powder_spread", "cls": msg.Cls, "ok": false, "why": "slow"})
		return
	}
	m.powderAt[cl.ID()] = now
	street := cl.Street()
	var others []*Client
	if street != "" {
		for _, o := range m.h.StreetLocked(street) {
			if o.ID() != cl.ID() {
				others = append(others, o)
			}
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i].ID() < others[j].ID() })
	// getNumActivePlayers counts you too.
	limit := powderSparkleMax
	if msg.Cls == "sneezing_powder" {
		limit = powderSneezeMax
	}
	if len(others)+1 > limit {
		m.h.SendLocked(cl, map[string]any{"t": "powder_spread", "cls": msg.Cls, "ok": false, "why": "crowd"})
		return
	}
	from := map[string]any{"id": cl.ID(), "name": cl.Name()}
	reached := 0
	for _, o := range others {
		// One round of sneezes at a time (ours): a second scatter finds them sneezing already.
		if msg.Cls == "sneezing_powder" && len(m.sneezing[o.ID()]) == 0 {
			m.startSneezing(o.ID())
		}
		m.h.SendLocked(o, map[string]any{"t": "powder_caught", "cls": msg.Cls, "from": from})
		reached++
	}
	var counters [][2]any
	if msg.Cls == "sneezing_powder" && reached >= powderSneezeRama {
		counters = append(counters, [2]any{"powders:sneezeorama", 1})
	}
	if msg.Cls == "sparkle_powder" && reached >= powderSparkleBud {
		counters = append(counters, [2]any{"powders:sparkle_buddy", 1})
	}
	log.Printf("hi: %s (%d) spread %s to %d on %s", cl.Name(), cl.ID(), msg.Cls, reached, street)
	m.h.SendLocked(cl, map[string]any{"t": "powder_spread", "cls": msg.Cls, "ok": true, "players": reached, "counters": counters})
}

// startSneezing is the Sneezing buff's events (catalog_buffs.js:2420-2441): a sneeze 0-8 s in, one at
// 13 s, the last (and the bean) when it ends at 21 s; each told to the sneezer's street so everyone sees it.
func (m *hiModule) startSneezing(id int64) {
	at := []time.Duration{time.Duration(m.roll(powderSneezeEarly+1)) * time.Second, powderSneezeLater, powderSneezeLast}
	stops := make([]func() bool, 0, len(at))
	for i, d := range at {
		last := i == len(at)-1
		stops = append(stops, m.h.After(d, func() {
			if last {
				delete(m.sneezing, id)
			}
			cl := m.h.ClientLocked(id)
			if cl == nil {
				return
			}
			msg := map[string]any{"t": "powder_sneeze", "id": id, "last": last}
			if st := cl.Street(); st != "" {
				m.h.BroadcastStreetLocked(st, msg, 0)
			} else {
				m.h.SendLocked(cl, msg)
			}
		}))
	}
	m.sneezing[id] = stops
}

// —— Persistence ——

func (m *hiModule) changed() {
	m.dirty = true
	if m.path == "" || m.saving {
		return
	}
	m.saving = true
	m.h.After(hiSaveDelay, func() {
		m.saving = false
		m.save()
	})
}

func (m *hiModule) save() bool {
	if m.path == "" || !m.dirty {
		return true
	}
	b, err := json.Marshal(m.st)
	if err == nil {
		err = writeFileSynced(m.path, b)
	}
	if err != nil {
		log.Printf("hi %s: %v", m.path, err)
		return false
	}
	m.dirty = false
	return true
}

func (m *hiModule) load() error {
	b, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st := newHiState()
	if err := json.Unmarshal(b, st); err != nil {
		return err
	}
	if st.Players == nil {
		st.Players = map[int64]*hiPlayer{}
	}
	for id, p := range st.Players {
		if p == nil {
			delete(st.Players, id)
		}
	}
	if st.Locs == nil {
		st.Locs = map[string]*hiLoc{}
	}
	t := &st.Tracker
	if t.Counts == nil {
		t.Counts = map[string]int{}
	}
	if t.Infectors == nil {
		t.Infectors = map[string]map[int64]int{}
	}
	if t.History == nil {
		t.History = map[int64]hiDayRow{}
	}
	if len(t.Records) == 0 {
		t.Records = []map[string]int64{{}}
	}
	m.st = st
	return nil
}
