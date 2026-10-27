package main

// Feats between real players (G345): one Feat at a time that everyone playing
// works toward together for a day, as in the original. The rules are
// GameServerJS groups/feat.js (the counter, goal, stretch_goal1 and
// stretch_goal2, increment 24-61 with its cap commented out so a Feat keeps
// counting past its goals, get_status 67-79), players/inc_feats.js
// (feats_get 15-35: open from the Feat's game_day for 24 real hours, one at a
// time as feats_get_current 295-309; feats_increment 37-113: the first
// contribution's feats:contributed, epics_contributed:<epic>,
// epics_completed:<epic>, quick_on_your_feat inside 30 minutes of the start
// and the vog_text with {remaining}; adminFeatsGiveRewards 129-151: place <=
// 26 counts feats:top_26, places 27 to 30 get the_cigar;
// feats_gather_rewards 255-283: feats:contributed_to once per reward) and
// main.js 7692-7733 doBreakIt, the one reward the source still shows. The
// blog (2012-10-12 "The Epic of Feats") has the rest: every Feat lasts
// exactly 24 hours, a minimum goal unlocks a bonus goal and then a
// supermegabonus goal, a Feat that misses its minimum has failed, the reward
// pool scales with what was done, everyone gets a small petrified rock and
// the top contributors in tiers larger ones. Goals were the web side's, so
// the amounts, the schedule and the reward tiers are the remake's, from
// data/feats.json (the game's tools/build-realtime-feats.mjs from
// js/feats-mp.js and js/solo-feats.js; the rules are stated there).
//
// Client to server:
//
//	{"t":"feat_add","feat":"animal_love","n":1}                       a contribution the client made
//	{"t":"feat_add","feat":"streaking","n":1,"act":"streak","street":"GA5…"}   a Feat whose reports name the act
//	{"t":"feat_ack","id":"feat-r-…"}                                  you applied a feat_first / feat_reward and saved
//	{"t":"feat_sync"}                                                  on connect: the Feat, and what you are owed
//	{"t":"feat_dev","end_in":30,"feat":"tottlys_toys"}                QA only, GRODDLE_FEATS_DEV=1
//
// Server to client:
//
//	{"t":"feat_state","now":ms,"run":n,"feat":"…","start":ms,"end":ms,"total":n,"goal":n,"bonus":n,"super":n,
//	     "tier":0-3,"count":n,"mine":n,"place":n,"top":[{"id","name","n"}],"last":Last|null}   answering a sync, and to everyone when a Feat begins
//	{"t":"feat_progress","run":n,"feat":"…","total":n,"tier":n,"count":n,"top":[…]}               to everyone, at most once a second
//	{"t":"feat_counted","feat":"…","run":n,"n":n,"total":n,"mine":n,"place":n,"reason":"…"}      to the contributor (n 0 and a reason when nothing counted)
//	{"t":"feat_goal","run":n,"feat":"…","tier":1-3,"total":n}                                     to everyone: a goal was met
//	{"t":"feat_ended","run":n,"feat":"…","total":n,"goal":n,"tier":0-3,"count":n,"top":[…]}       to everyone: the day is over (tier 0: failed)
//	{"t":"feat_first","id":"feat-f-<feat>-<player>","feat":"…","run":n,"counters":[{"counter","n"}],"grants":[…],"remaining":n|-1}
//	{"t":"feat_reward","id":"feat-r-<run>-<feat>-<player>","feat":"…","run":n,"place":n,"count":n,"tier":n,"total":n,"goal":n,"mine":n,"scale":x,
//	     "rewards":{"xp","currants","mood","energy","favor":[…]},"items":[{"c","n"}],"counters":[…],"grants":[…]}
//
// feat_first and feat_reward are kept until the player acks them (sent again on
// every sync), so a badge or a reward survives a dropped socket or a restart.
//
// Trust. The server owns the schedule, the totals, who put in how much, the
// goals, the ranks and the rewards. A contribution is the player's own client
// saying what it did (the server cannot see animals, packs or quests): the
// server takes the amount from its table where the source fixes one (acts),
// caps a report at the Feat's burst and lets each player add to each Feat at
// most pace.n every pace.everyMs (a bucket), so a forged client counts no
// faster than an honest one; a streak counts once per street per game day
// (location.js 1041-1055) and a qurazy quoin once per street per Feat. Other
// modules that see a contribution themselves count it through
// Provide("feats.count") with no pace.
//
// Rewards (the Feat met its minimum goal): every contributor gets the
// doBreakIt reward times the pool's scale, min(total, supermegabonus) over the
// minimum goal, Math.round, and a Small Petrified Rock; places 1 to 13 also a
// Large one (petrified_rock_large.js:8 "drawn to the 13 who contributed the
// most awe"), 14 to 26 a Medium one. Places rank by amount, then by who reached
// it first. The currants go into the site's ledger through a signed call
// (POST groddle/v1/ledger/feat, idempotent per id); the rest (iMG, mood,
// energy, favor, the rocks, which are not tradeable, and the badges) is the
// client's to apply from the feat_reward.
//
// Persistence: the Feat outlives sessions, so the state (the run, who has
// contributed to which Feat and been rewarded, credits not yet made,
// messages not yet acked) is written to -feats-file (a temp file, fsync,
// rename): at once for anything of value, within a second for a contribution.
//
// Departures: the Feats take turns (the source ran each once, on its own
// game_day); there is no make-up Feat after a failed one; the Conch gate
// (inc_feats.js:43 has_blown_conch) is not applied, as in solo.

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"testing"
	"time"
)

//go:embed data/feats.json
var featsJSON []byte

const (
	ledgerRouteFeat = "/groddle/v1/ledger/feat"
	featTop         = 5 // the top contributors players see
	featMaxN        = 100_000
	featKeepSeen    = 20_000
)

var featsFileFlag = flag.String("feats-file", "", "file that keeps the shared Feat, who contributed and was rewarded, and the rewards not yet applied (default: realtime-feats.json beside -secret-file; off: memory only)")

// featsFileForTest lets a test give the module a state file; under go test the flag is ignored.
var featsFileForTest string

func init() { modules = append(modules, newFeats) }

// —— The table (data/feats.json) ——

type featPace struct {
	N       int64 `json:"n"`
	EveryMs int64 `json:"everyMs"`
	Burst   int64 `json:"burst"`
}

type featAct struct {
	N      int64  `json:"n"`
	Dedupe string `json:"dedupe"` // "street" (once per Feat) | "street-day" (once per game day) | ""
}

type featDef struct {
	Title     string             `json:"title"`
	Epic      int                `json:"epic"`
	Goal      int64              `json:"goal"`
	Bonus     int64              `json:"bonus"`
	Super     int64              `json:"super"`
	Pace      featPace           `json:"pace"`
	Acts      map[string]featAct `json:"acts"`
	Elsewhere bool               `json:"elsewhere"` // the game counts its first feats:contributed itself
}

type featRewardDef struct {
	XP          float64     `json:"xp"`
	Currants    float64     `json:"currants"`
	Mood        float64     `json:"mood"`
	Energy      float64     `json:"energy"`
	Favor       []projFavor `json:"favor"`
	Small       string      `json:"small"`
	Medium      string      `json:"medium"`
	Large       string      `json:"large"`
	LargePlaces int         `json:"largePlaces"`
	TopPlaces   int         `json:"topPlaces"`
	CigarPlaces int         `json:"cigarPlaces"`
}

type featTable struct {
	EpochMs   int64              `json:"epochMs"`
	RunMs     int64              `json:"runMs"`
	GameDayMs int64              `json:"gameDayMs"`
	QuickMs   int64              `json:"quickMs"`
	Order     []string           `json:"order"`
	Feats     map[string]featDef `json:"feats"`
	Epics     map[string]int     `json:"epics"`
	Reward    featRewardDef      `json:"reward"`
}

// —— State ——

type featEntry struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	N     int64  `json:"n"`
	At    int64  `json:"at"` // ms it reached N (ties rank by who got there first)
	First int64  `json:"first"`
}

type featRun struct {
	Run     int64           `json:"run"`
	Feat    string          `json:"feat"`
	Start   int64           `json:"start"`
	End     int64           `json:"end"`
	Total   int64           `json:"total"`
	Tier    int             `json:"tier"`
	Players []*featEntry    `json:"players"`
	Seen    map[string]bool `json:"seen,omitempty"` // dedupe keys (streets streaked, qurazy streets)
	idx     map[int64]*featEntry
}

type featTopRow struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	N    int64  `json:"n"`
}

// featLast is the Feat that ended last, for the panel.
type featLast struct {
	Run   int64        `json:"run"`
	Feat  string       `json:"feat"`
	Total int64        `json:"total"`
	Goal  int64        `json:"goal"`
	Tier  int          `json:"tier"`
	Count int          `json:"count"`
	Top   []featTopRow `json:"top"`
}

type featCredit struct {
	ID       string          `json:"id"`
	Player   int64           `json:"player"`
	Currants int64           `json:"currants"`
	Msg      json.RawMessage `json:"msg"`
	busy     bool
	tries    int
}

type featOwed struct {
	ID  string          `json:"id"`
	Msg json.RawMessage `json:"msg"`
}

type featState struct {
	Run       *featRun               `json:"run"`
	Last      *featLast              `json:"last,omitempty"`
	Shift     int64                  `json:"shift,omitempty"`     // QA (feat_dev): ms the schedule runs ahead
	FeatShift int64                  `json:"featShift,omitempty"` // QA: turns the order runs ahead
	Ever      map[int64][]string     `json:"ever"`                // Feats a player has contributed to (feats_has_contributed)
	Rewarded  map[int64][]string     `json:"rewarded"`            // Feats a player was rewarded for (feats.feats)
	Credits   map[string]*featCredit `json:"credits"`
	Owed      map[int64][]featOwed   `json:"owed"`
}

func newFeatState() *featState {
	return &featState{Ever: map[int64][]string{}, Rewarded: map[int64][]string{}, Credits: map[string]*featCredit{}, Owed: map[int64][]featOwed{}}
}

// featLedger is the site's ledger for Feat rewards: put got in, once per id.
type featLedger interface {
	feat(id string, user int64, got tradeGoods) ledgerAnswer
}

func (l *httpLedger) feat(id string, user int64, got tradeGoods) ledgerAnswer {
	return l.call(ledgerRouteFeat, map[string]any{"id": id, "user": user, "got": goodsJSON(got)})
}

type featBucket struct {
	have float64
	at   time.Time
}

type feats struct {
	h       *Hub
	t       featTable
	st      *featState
	path    string
	ledger  featLedger // nil: no ledger (see trusted)
	trusted bool       // -trade-ledger off: rewards' currants are the client's (development only)
	spawn   func(func())
	dev     bool

	buckets    map[int64]map[string]*featBucket
	endStop    func() bool
	progressAt bool // a feat_progress is due
	saveAt     bool // a save is due
}

func newFeats(h *Hub) Module {
	var t featTable
	if err := json.Unmarshal(featsJSON, &t); err != nil || len(t.Order) == 0 {
		panic(fmt.Sprintf("realtime: data/feats.json: %v", err))
	}
	path := featsFileForTest
	if !testing.Testing() && featsFileForTest == "" {
		path = *featsFileFlag
		if path == "" {
			if f := flag.Lookup("secret-file"); f != nil && f.Value.String() != "" {
				path = filepath.Join(filepath.Dir(expandHome(f.Value.String())), "realtime-feats.json")
			}
		}
	}
	if path == "off" {
		path = ""
	}
	m := newFeatsWith(h, t, path)
	m.dev = os.Getenv("GRODDLE_FEATS_DEV") == "1"
	if !testing.Testing() {
		site, missing := newLedgerFromFlags()
		if hl, ok := site.(*httpLedger); ok && hl != nil {
			m.ledger = hl
		}
		m.trusted = site == nil && !missing
	}
	return m
}

func newFeatsWith(h *Hub, t featTable, path string) *feats {
	if t.RunMs <= 0 {
		t.RunMs = 24 * 60 * 60 * 1000
	}
	if t.GameDayMs <= 0 {
		t.GameDayMs = 4 * 60 * 60 * 1000
	}
	m := &feats{h: h, t: t, st: newFeatState(), path: path, spawn: func(fn func()) { go fn() }, buckets: map[int64]map[string]*featBucket{}}
	if path != "" {
		if err := m.load(); err != nil {
			log.Printf("feats %s: %v (the Feat is kept in memory only)", path, err)
			m.path = ""
		} else if m.st.Run != nil {
			log.Printf("feats %s: %s run %d, %d contributors, %d rewards to credit", path, m.st.Run.Feat, m.st.Run.Run, len(m.st.Run.Players), len(m.st.Credits))
		}
	}
	return m
}

func (m *feats) Register(r *Registry) {
	r.Handle("feat_add", m.add)
	r.Handle("feat_ack", m.ack)
	r.Handle("feat_sync", m.sync)
	r.Handle("feat_dev", m.devMsg)
	r.OnConnect(m.connected)
	r.OnDisconnect(func(cl *Client) { delete(m.buckets, cl.ID()) })
	// A module that sees a contribution itself (a hi sign landing, a package left with a Butler) counts it here, at no pace.
	r.Provide("feats.count", func(id int64, name, feat string, n int64) int64 {
		run := m.current()
		if run == nil || feat != run.Feat || n <= 0 {
			return 0
		}
		n = min(n, featMaxN)
		m.count(run, id, name, n)
		return n
	})
}

// —— Persistence ——

func (m *feats) load() error {
	b, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st := newFeatState()
	if err := json.Unmarshal(b, st); err != nil {
		return err
	}
	if st.Ever == nil {
		st.Ever = map[int64][]string{}
	}
	if st.Rewarded == nil {
		st.Rewarded = map[int64][]string{}
	}
	if st.Credits == nil {
		st.Credits = map[string]*featCredit{}
	}
	if st.Owed == nil {
		st.Owed = map[int64][]featOwed{}
	}
	if r := st.Run; r != nil {
		if _, ok := m.t.Feats[r.Feat]; !ok {
			st.Run = nil
		} else {
			r.idx = map[int64]*featEntry{}
			kept := r.Players[:0]
			for _, e := range r.Players {
				if e != nil && e.ID > 0 {
					r.idx[e.ID] = e
					kept = append(kept, e)
				}
			}
			r.Players = kept
			if r.Seen == nil {
				r.Seen = map[string]bool{}
			}
		}
	}
	m.st = st
	return nil
}

// save writes the whole state now; false when it could not (it stays in memory).
func (m *feats) save() bool {
	if m.path == "" {
		return true
	}
	b, err := json.Marshal(m.st)
	if err == nil {
		err = writeFileSynced(m.path, b)
	}
	if err != nil {
		log.Printf("feats %s: %v", m.path, err)
		return false
	}
	return true
}

// saveSoon writes within a second (contributions come often; one a second is plenty).
func (m *feats) saveSoon() {
	if m.path == "" || m.saveAt {
		return
	}
	m.saveAt = true
	m.h.After(time.Second, func() {
		m.saveAt = false
		m.save()
	})
}

// —— The schedule ——

func (m *feats) ms() int64 { return m.h.Now().UnixMilli() }

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// schedule is the run open at now: inc_feats.js feats_get, one Feat for runMs from its start, in turns.
func (m *feats) schedule(now int64) (n, start, end int64, feat string) {
	n = floorDiv(now+m.st.Shift-m.t.EpochMs, m.t.RunMs)
	start = m.t.EpochMs + n*m.t.RunMs - m.st.Shift
	k := int64(len(m.t.Order))
	feat = m.t.Order[((n+m.st.FeatShift)%k+k)%k]
	return n, start, start + m.t.RunMs, feat
}

// current is the open run, ending the last one and starting the next when its day is over.
func (m *feats) current() *featRun {
	n, start, end, feat := m.schedule(m.ms())
	r := m.st.Run
	if r != nil && r.Run == n && r.Feat == feat {
		if m.endStop == nil {
			m.arm(r)
		}
		return r
	}
	if r != nil {
		m.end(r)
	}
	r = &featRun{Run: n, Feat: feat, Start: start, End: end, Seen: map[string]bool{}, idx: map[int64]*featEntry{}}
	m.st.Run = r
	m.save()
	log.Printf("feat run %d: %s until %s", n, feat, time.UnixMilli(end).UTC().Format(time.RFC3339))
	m.arm(r)
	for _, cl := range m.h.clients {
		m.h.SendLocked(cl, m.stateJSON(r, cl.ID()))
	}
	return r
}

// arm wakes the module when the run's day is over, so the end is told on time.
func (m *feats) arm(r *featRun) {
	if m.endStop != nil {
		m.endStop()
	}
	wait := time.Duration(r.End-m.ms())*time.Millisecond + 50*time.Millisecond
	if wait < 0 {
		wait = 0
	}
	run := r.Run
	m.endStop = m.h.After(wait, func() {
		m.endStop = nil
		if m.st.Run != nil && m.st.Run.Run == run {
			m.current()
		}
	})
}

func (m *feats) tierOf(def featDef, total int64) int {
	tier := 0
	for _, g := range []int64{def.Goal, def.Bonus, def.Super} {
		if g > 0 && total >= g {
			tier++
		}
	}
	return tier
}

// ranked is the run's contributors by amount, then by who reached it first.
func ranked(r *featRun) []*featEntry {
	out := append([]*featEntry(nil), r.Players...)
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].N != out[b].N {
			return out[a].N > out[b].N
		}
		if out[a].At != out[b].At {
			return out[a].At < out[b].At
		}
		return out[a].ID < out[b].ID
	})
	return out
}

func topRows(sorted []*featEntry) []featTopRow {
	out := []featTopRow{}
	for _, e := range sorted[:min(featTop, len(sorted))] {
		out = append(out, featTopRow{ID: e.ID, Name: e.Name, N: e.N})
	}
	return out
}

// —— Contributions ——

func (m *feats) reply(cl *Client, run *featRun, feat string, n int64, reason string) {
	msg := map[string]any{"t": "feat_counted", "feat": feat, "n": n, "run": int64(0), "total": int64(0), "mine": int64(0), "place": 0}
	if run != nil {
		msg["run"], msg["total"] = run.Run, run.Total
		if e := run.idx[cl.ID()]; e != nil {
			msg["mine"] = e.N
			msg["place"] = m.place(run, cl.ID())
		}
	}
	if reason != "" {
		msg["reason"] = reason
	}
	m.h.SendLocked(cl, msg)
}

func (m *feats) place(r *featRun, id int64) int {
	for i, e := range ranked(r) {
		if e.ID == id {
			return i + 1
		}
	}
	return 0
}

// take spends up to n from the player's bucket for this Feat: n every everyMs, at most burst held.
func (m *feats) take(id int64, feat string, def featDef, n int64) int64 {
	p := def.Pace
	if p.N <= 0 || p.EveryMs <= 0 || p.Burst <= 0 {
		return n
	}
	byFeat := m.buckets[id]
	if byFeat == nil {
		byFeat = map[string]*featBucket{}
		m.buckets[id] = byFeat
	}
	now := m.h.Now()
	b := byFeat[feat]
	if b == nil {
		b = &featBucket{have: float64(p.Burst), at: now}
		byFeat[feat] = b
	}
	rate := float64(p.N) / float64(p.EveryMs) // per ms
	b.have = math.Min(float64(p.Burst), b.have+float64(now.Sub(b.at).Milliseconds())*rate)
	b.at = now
	got := min(n, int64(math.Floor(b.have+1e-9)))
	if got <= 0 {
		return 0
	}
	b.have -= float64(got)
	return got
}

func (m *feats) add(cl *Client, frame json.RawMessage) {
	var msg struct {
		Feat   string `json:"feat"`
		N      int64  `json:"n"`
		Act    string `json:"act"`
		Street string `json:"street"`
	}
	if json.Unmarshal(frame, &msg) != nil || len(msg.Feat) > 40 || len(msg.Act) > 16 {
		m.reply(cl, nil, "", 0, "bad")
		return
	}
	run := m.current()
	if msg.Feat != run.Feat {
		m.reply(cl, run, msg.Feat, 0, "not_today")
		return
	}
	def := m.t.Feats[run.Feat]
	n := msg.N
	key := ""
	if len(def.Acts) > 0 {
		a, ok := def.Acts[msg.Act]
		if !ok {
			m.reply(cl, run, msg.Feat, 0, "act")
			return
		}
		n = a.N
		if a.Dedupe != "" {
			if !streetRe.MatchString(msg.Street) {
				m.reply(cl, run, msg.Feat, 0, "street")
				return
			}
			switch a.Dedupe {
			case "street-day":
				key = fmt.Sprintf("%d|%d|%s", cl.ID(), floorDiv(m.ms()-m.t.EpochMs, m.t.GameDayMs), gStreet(msg.Street))
			default:
				key = fmt.Sprintf("%d|%s", cl.ID(), gStreet(msg.Street))
			}
			if run.Seen[key] {
				m.reply(cl, run, msg.Feat, 0, "already")
				return
			}
		}
	}
	if def.Pace.Burst > 0 {
		n = min(n, def.Pace.Burst)
	}
	n = min(n, featMaxN)
	if n <= 0 {
		m.reply(cl, run, msg.Feat, 0, "amount")
		return
	}
	n = m.take(cl.ID(), run.Feat, def, n)
	if n <= 0 {
		m.reply(cl, run, msg.Feat, 0, "pace")
		return
	}
	if key != "" && len(run.Seen) < featKeepSeen {
		run.Seen[key] = true
	}
	m.count(run, cl.ID(), cl.Name(), n)
	m.reply(cl, run, msg.Feat, n, "")
}

// count adds n for a player (groups/feat.js increment), tells the goals met and the first contribution.
func (m *feats) count(run *featRun, id int64, name string, n int64) {
	now := m.ms()
	def := m.t.Feats[run.Feat]
	run.Total += n
	e := run.idx[id]
	if e == nil {
		e = &featEntry{ID: id, Name: name, First: now}
		run.idx[id] = e
		run.Players = append(run.Players, e)
	}
	e.N += n
	e.At = now
	e.Name = name
	if tier := m.tierOf(def, run.Total); tier > run.Tier {
		run.Tier = tier
		m.toAll(map[string]any{"t": "feat_goal", "run": run.Run, "feat": run.Feat, "tier": tier, "total": run.Total})
		log.Printf("feat run %d: %s met goal %d at %d", run.Run, run.Feat, tier, run.Total)
	}
	if !slices.Contains(m.st.Ever[id], run.Feat) {
		m.first(run, id, def, now)
		m.save()
	} else {
		m.saveSoon()
	}
	m.progressSoon()
}

// first: inc_feats.js 53-84, the first contribution a player ever makes to this Feat.
func (m *feats) first(run *featRun, id int64, def featDef, now int64) {
	m.st.Ever[id] = append(m.st.Ever[id], run.Feat)
	counters := []map[string]any{}
	if !def.Elsewhere {
		counters = append(counters, map[string]any{"counter": "feats:contributed", "n": 1})
	}
	if def.Epic != 0 {
		epic := strconv.Itoa(def.Epic)
		counters = append(counters, map[string]any{"counter": "epics_contributed:" + epic, "n": 1})
		// 58-69: this Feat and every rewarded one of the epic.
		done := 1
		for _, f := range m.st.Rewarded[id] {
			if f != run.Feat && m.t.Feats[f].Epic == def.Epic {
				done++
			}
		}
		if done == m.t.Epics[epic] {
			counters = append(counters, map[string]any{"counter": "epics_completed:" + epic, "n": 1})
		}
	}
	grants := []string{}
	if now-run.Start <= m.t.QuickMs {
		grants = append(grants, "quick_on_your_feat")
	}
	// 76-79: the vog_text while the value is at or under the goal.
	remaining := int64(-1)
	if run.Total <= def.Goal {
		remaining = def.Goal - run.Total
	}
	fid := fmt.Sprintf("feat-f-%d-%d", slices.Index(m.t.Order, run.Feat), id) // once per Feat ever
	m.owe(id, fid, map[string]any{"t": "feat_first", "id": fid, "feat": run.Feat, "run": run.Run, "counters": counters, "grants": grants, "remaining": remaining})
}

func (m *feats) progressSoon() {
	if m.progressAt {
		return
	}
	m.progressAt = true
	m.h.After(time.Second, func() {
		m.progressAt = false
		r := m.st.Run
		if r == nil {
			return
		}
		m.toAll(map[string]any{"t": "feat_progress", "run": r.Run, "feat": r.Feat, "total": r.Total, "tier": r.Tier, "count": len(r.Players), "top": topRows(ranked(r))})
	})
}

// —— The end of a Feat's day ——

func (m *feats) end(r *featRun) {
	def := m.t.Feats[r.Feat]
	sorted := ranked(r)
	tier := m.tierOf(def, r.Total)
	m.st.Last = &featLast{Run: r.Run, Feat: r.Feat, Total: r.Total, Goal: def.Goal, Tier: tier, Count: len(sorted), Top: topRows(sorted)}
	m.st.Run = nil
	m.toAll(map[string]any{"t": "feat_ended", "run": r.Run, "feat": r.Feat, "total": r.Total, "goal": def.Goal, "tier": tier, "count": len(sorted), "top": topRows(sorted)})
	if tier == 0 || def.Goal <= 0 {
		log.Printf("feat run %d: %s failed at %d of %d (%d contributors)", r.Run, r.Feat, r.Total, def.Goal, len(sorted))
		m.save()
		return
	}
	// The pool scales with what was done (blog 2012-10-12), up to the supermegabonus.
	scale := float64(min(r.Total, max(def.Super, def.Goal))) / float64(def.Goal)
	rw := m.t.Reward
	var asks []*featCredit
	for i, e := range sorted {
		place := i + 1
		favor := []map[string]any{}
		for _, f := range rw.Favor {
			favor = append(favor, map[string]any{"giant": f.Giant, "points": jsRound(f.Points * scale)})
		}
		currants := jsRound(rw.Currants * scale)
		rewards := map[string]any{"xp": jsRound(rw.XP * scale), "currants": currants, "mood": jsRound(rw.Mood * scale), "energy": jsRound(rw.Energy * scale), "favor": favor}
		items := []map[string]any{{"c": rw.Small, "n": 1}}
		switch {
		case place <= rw.LargePlaces:
			items = append(items, map[string]any{"c": rw.Large, "n": 1})
		case place <= rw.TopPlaces:
			items = append(items, map[string]any{"c": rw.Medium, "n": 1})
		}
		// inc_feats.js 139-144 and 281.
		counters := []map[string]any{{"counter": "feats:contributed_to", "n": 1}}
		grants := []string{}
		if place <= rw.TopPlaces {
			counters = append(counters, map[string]any{"counter": "feats:top_26", "n": 1})
		} else if place <= rw.CigarPlaces {
			grants = append(grants, "the_cigar")
		}
		if !slices.Contains(m.st.Rewarded[e.ID], r.Feat) {
			m.st.Rewarded[e.ID] = append(m.st.Rewarded[e.ID], r.Feat)
		}
		id := fmt.Sprintf("feat-r-%s-%d-%d", strconv.FormatInt(r.Run, 36), slices.Index(m.t.Order, r.Feat), e.ID)
		msg := map[string]any{"t": "feat_reward", "id": id, "feat": r.Feat, "run": r.Run, "place": place, "count": len(sorted), "tier": tier,
			"total": r.Total, "goal": def.Goal, "mine": e.N, "scale": scale, "rewards": rewards, "items": items, "counters": counters, "grants": grants}
		log.Printf("feat run %d: %s placed %d of %d with %d: %d iMG, %d currants", r.Run, e.Name, place, len(sorted), e.N, rewards["xp"], currants)
		if m.ledger == nil || currants <= 0 {
			if m.ledger == nil && !m.trusted && currants > 0 {
				rewards["currants"] = int64(0) // no ledger to put them in
			}
			m.owe(e.ID, id, msg)
			continue
		}
		b, _ := json.Marshal(msg)
		c := &featCredit{ID: id, Player: e.ID, Currants: currants, Msg: b}
		m.st.Credits[id] = c
		asks = append(asks, c)
	}
	m.save()
	for _, c := range asks {
		m.askCredit(c)
	}
}

func (m *feats) askCredit(c *featCredit) {
	c.busy = true
	site := m.ledger
	id, player, got := c.ID, c.Player, tradeGoods{Items: []tradeItem{}, Currants: c.Currants}
	m.spawn(func() {
		a := site.feat(id, player, got)
		m.h.After(0, func() { m.credited(c, a) })
	})
}

func (m *feats) credited(c *featCredit, a ledgerAnswer) {
	c.busy = false
	if m.st.Credits[c.ID] != c {
		return
	}
	switch a.Status {
	case "settled", "refused", "void":
		var msg map[string]any
		json.Unmarshal(c.Msg, &msg)
		if a.Status != "settled" {
			log.Printf("feat reward %s: the ledger said %s; sent without currants", c.ID, a.Status)
			if rw, ok := msg["rewards"].(map[string]any); ok {
				rw["currants"] = 0
			}
		}
		delete(m.st.Credits, c.ID)
		m.owe(c.Player, c.ID, msg)
		m.save()
	default:
		c.tries++
		wait := min(time.Minute, time.Second<<min(c.tries, 6))
		log.Printf("feat reward %s: no ledger answer (try %d): again in %s", c.ID, c.tries, wait)
		m.h.After(wait, func() {
			if m.st.Credits[c.ID] == c && !c.busy {
				m.askCredit(c)
			}
		})
	}
}

// —— Owed messages ——

// owe keeps a message for a player until they ack it, and sends it now when they are online.
func (m *feats) owe(player int64, id string, msg map[string]any) {
	for _, o := range m.st.Owed[player] {
		if o.ID == id {
			return
		}
	}
	b, _ := json.Marshal(msg)
	m.st.Owed[player] = append(m.st.Owed[player], featOwed{ID: id, Msg: b})
	if cl := m.h.ClientLocked(player); cl != nil {
		m.h.SendLocked(cl, json.RawMessage(b))
	}
}

func (m *feats) ack(cl *Client, frame json.RawMessage) {
	var msg struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(frame, &msg) != nil || !projIDOK(msg.ID) {
		return
	}
	list := m.st.Owed[cl.ID()]
	for i, o := range list {
		if o.ID == msg.ID {
			list = append(list[:i:i], list[i+1:]...)
			if len(list) == 0 {
				delete(m.st.Owed, cl.ID())
			} else {
				m.st.Owed[cl.ID()] = list
			}
			m.save()
			return
		}
	}
}

// —— What players see ——

func (m *feats) toAll(msg any) {
	b, _ := json.Marshal(msg)
	for _, cl := range m.h.clients {
		m.h.pushLocked(cl, b)
	}
}

func (m *feats) stateJSON(r *featRun, reader int64) map[string]any {
	def := m.t.Feats[r.Feat]
	sorted := ranked(r)
	mine, place := int64(0), 0
	for i, e := range sorted {
		if e.ID == reader {
			mine, place = e.N, i+1
		}
	}
	var last any
	if m.st.Last != nil {
		last = m.st.Last
	}
	return map[string]any{"t": "feat_state", "now": m.ms(), "run": r.Run, "feat": r.Feat, "start": r.Start, "end": r.End, "total": r.Total,
		"goal": def.Goal, "bonus": def.Bonus, "super": def.Super, "tier": r.Tier, "count": len(sorted), "mine": mine, "place": place, "top": topRows(sorted), "last": last}
}

func (m *feats) sync(cl *Client, _ json.RawMessage) {
	before := m.st.Run
	r := m.current()
	if r == before { // a new run was just told to everyone
		m.h.SendLocked(cl, m.stateJSON(r, cl.ID()))
	}
	for _, o := range m.st.Owed[cl.ID()] {
		m.h.SendLocked(cl, o.Msg)
	}
}

// connected: rewards that got no answer (or came before a restart) are asked again now this player is here.
func (m *feats) connected(cl *Client) {
	if m.ledger == nil {
		return
	}
	for _, c := range m.st.Credits {
		if c.Player == cl.ID() && !c.busy {
			m.askCredit(c)
		}
	}
}

// devMsg (GRODDLE_FEATS_DEV=1) brings the end near, or opens another Feat now, for QA.
func (m *feats) devMsg(cl *Client, frame json.RawMessage) {
	if !m.dev {
		return
	}
	var msg struct {
		EndIn float64 `json:"end_in"`
		Feat  string  `json:"feat"`
	}
	if json.Unmarshal(frame, &msg) != nil {
		return
	}
	r := m.current()
	if _, ok := m.t.Feats[msg.Feat]; ok && msg.Feat != r.Feat {
		k := int64(len(m.t.Order))
		want := int64(slices.Index(m.t.Order, msg.Feat))
		m.st.FeatShift = ((want-r.Run)%k + k) % k
		r = m.current()
		r.Start = m.ms() // QA: the new Feat starts now (quick_on_your_feat's 30 minutes)
		m.save()
	}
	if e := int64(msg.EndIn * 1000); e > 0 && e < m.t.RunMs {
		at := m.ms() + e
		m.st.Shift += r.End - at
		r.End = at
		m.save()
		m.arm(r)
	}
	log.Printf("feat dev: %s run %d now ends %s", r.Feat, r.Run, time.UnixMilli(r.End).UTC().Format(time.RFC3339))
	for _, o := range m.h.clients {
		m.h.SendLocked(o, m.stateJSON(r, o.ID()))
	}
}
