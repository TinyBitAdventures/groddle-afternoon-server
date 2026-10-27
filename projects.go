package main

// Street projects (G346): everyone on a street builds the same project, the
// Job Board's build-a-street and upgrade-this-street jobs, as in the
// original. The rules are GameServerJS quests/quest.js (requirements in
// buckets and groups, contributions in base cost, the winners),
// players/inc_jobs.js (each contributor's share of the rewards),
// items/include/jobs.js (work at one unit per 1,500 ms) and
// locations/inc_jobs.js (phases in order, the delay before the next one, the
// 30-minute rest after a street is built, the road it opens). The amounts are
// the remake's (js/street-jobs.js soloNeed: the source's over 50), from
// data/projects.json (the game's tools/build-realtime-projects.mjs).
//
// Client to server:
//
//	{"t":"project_give","job":"62","req":"r133","n":10,"option":"13"}   items for an item requirement
//	{"t":"project_give","job":"62","req":"r136","currants":400}         currants for a currants requirement
//	{"t":"project_work","job":"62","req":"r138","option":"13"}          one unit of work, done now
//	{"t":"project_ack","id":"…"}                                         you applied a project_given / project_reward and saved
//	{"t":"project_sync"}                                                 after a join (and on connect): the world, this street, what you are owed
//
// Server to client:
//
//	{"t":"project_world","now":ms,"built":{"62":{"at":ms,"option":""}},"rest":{"GLI…":ms}}   answering a sync: every finished project
//	{"t":"project_built","now":ms,"job":"62","at":ms,"option":"","rest":{…}}                   a project finished (everyone)
//	{"t":"project_street","street":"GLI…","now":ms,"jobs":{"62":Row}}                          answering a sync: the street's projects
//	{"t":"project_state","job":"62","now":ms,"row":Row}                                        a change (everyone on the board's street)
//	{"t":"project_given","id":"…","job":"62","req":"r133","cls":"…","phase":0,"items":{"ginger":10},"currants":0,"cost":40}
//	{"t":"project_worked","job":"62","req":"r138","cost":15,"left":5}                         your unit counted
//	{"t":"project_reward","id":"…","job":"62","phase":0,"cls":"…","final":true,"share":0.59,
//	     "participation":59,"rewards":{…},"performance":{…},"winner":true,"top5":true,"phases":1,"option":"","readyAt":ms}
//	{"t":"error","code":"project","message":"…"}
//
// Row is {"phase","got":{rid:n},"held":{rid:n},"mine":{class:cost},"started","ready","done",
// "option","votes":{option:cost},"top":[{"id","name","cost"}],"count"}: mine is the reader's own
// contribution per phase class, top and count the current phase's (the last one once done).
//
// Trust. The server owns each project (phases, the requirement totals, who put
// in how much, the delay and the rest) and never takes an amount from a
// client: an item or currant contribution counts only once the site's ledger
// (ledger.go, groddle-afternoon-site src/Ledger.php project) has confirmed the
// player held it and taken it out; while the site is asked, the amount is held
// against the requirement so two players never overfill it, and the ask is
// repeated with the same id until the site answers (it records each id once).
// A work unit is the client's word that it holds a working tool from the
// requirement's list and the skill, and had the energy: the server cannot see
// packs or skills (the same client-trusted line the economy doc draws for
// tools and skills). What it does check: the player stands on the board's
// street, the requirement is open, and the pace, one unit per 1,500 ms per
// player (items/include/jobs.js startJobWork unit_duration), so a forged client
// works no faster than an honest one.
//
// Rewards (players/inc_jobs.js jobs_familiar_turnin_do 228-258: my_contribution
// / job.getTotalBasecost(); quest.js getRewards 2033-2046, Math.round): each
// contributor's phase rewards are the class rewards times their share of the
// phase's total base cost, over the remake's 50 (as solo). The winners
// (quest.js getWinners 1617-1633: the top performance_percent of contributors,
// rounded up, else the top performance_cutoff) also get the performance
// rewards and a roll of each drop table; the top five get a job_phase_winner
// count (1715-1719). Currants and items go into the site's ledger through a
// signed call, once per phase per player (the call's id names the job, its
// run, the phase and the player); iMG, mood, energy, favor and badges are the
// client's to apply from the project_reward, which is sent until acked.
// Finishing a street project opens its road for everyone (project_built) and
// rests the streets that offered it for 30 minutes (locations/inc_jobs.js
// jobs_get_available); an upgrade takes the option with the most base cost
// voted for it in its last phase (quest.js addPlayerContribution 1415-1418,
// getWinningOption 2013-2031: the first of a tie).
//
// Persistence: a project outlives sessions, so the whole state (rows, gives
// waiting for the ledger, credits not yet made, messages not yet acked) is
// written to -projects-file after every change (a temp file, fsync, rename):
// a restart neither loses nor doubles a contribution or a reward.
//
// Departures: Hire a hand is solo only; projects in homes (restorations) stay
// per player; an exempt or grandfathered road is the player's own (the save's).

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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

//go:embed data/projects.json
var projectsJSON []byte

const (
	ledgerRouteProject = "/groddle/v1/ledger/project"
	projectWorkSlack   = 250 * time.Millisecond // a unit may arrive this early (timer and network jitter); the pace holds over time
	projectGiveEvery   = 250 * time.Millisecond
	projectTopFive     = 5 // quest.js getTopFive
	projectMaxN        = 1_000_000
	projectMaxID       = 40
)

// Words players read: js/street-jobs.js (the solo board's), main.js where marked, or ours.
const (
	projectTextNoJob      = "There is no such project here."                                   // ours
	projectTextNotHere    = "You need to be at the project board to do that."                  // ours
	projectTextDone       = "This project is finished."                                        // street-jobs.js canContribute
	projectTextDelayed    = "The next phase is not ready yet."                                 // street-jobs.js canContribute
	projectTextCooldown   = "This street just finished a project. The next one opens soon."    // street-jobs.js canContribute
	projectTextNoNeed     = "Nothing here needs that right now."                               // street-jobs.js contributeItems
	projectTextNoCurrants = "No currants are needed right now."                                // street-jobs.js contributeCurrants
	projectTextNoWork     = "There is no such work."                                           // street-jobs.js workCheck
	projectTextWorkDone   = "There's no more work left to do."                                 // street-jobs.js workCheck
	projectTextEarlier    = "Finish the earlier steps first."                                  // street-jobs.js workCheck
	projectTextNoItem     = "That item doesn't exist."                                         // main.js:6157 doJobContributeItem
	projectTextNotTaken   = "Didn't accept anything."                                          // main.js:6179 doJobContributeCurrants
	projectTextCounting   = "Your last contribution is still being counted."                   // ours
	projectTextPace       = "Work takes a moment a unit."                                      // ours
	projectTextNoLedger   = "Street projects are not set up on this server."                   // ours
	projectTextNotSaved   = "The project could not take that just now. Try again in a moment." // ours
)

var projectsFileFlag = flag.String("projects-file", "", "file that keeps street projects, the contributions waiting for the ledger and the rewards not yet applied (default: realtime-projects.json beside -secret-file; off: memory only)")

// projectsFileForTest lets a test give the module a state file; under go test the flag is ignored.
var projectsFileForTest string

func init() { modules = append(modules, newProjects) }

// —— The table (data/projects.json) ——

type projReq struct {
	ID   string `json:"id"`
	B    string `json:"b"`
	G    int    `json:"g"`
	Type string `json:"type"`
	C    string `json:"c,omitempty"`
	Need int64  `json:"need"`
	Cost int64  `json:"cost"`
}

type projFavor struct {
	Giant  string  `json:"giant"`
	Points float64 `json:"points"`
}

type projRewards struct {
	XP       float64     `json:"xp"`
	Currants float64     `json:"currants"`
	Mood     float64     `json:"mood"`
	Energy   float64     `json:"energy"`
	Favor    []projFavor `json:"favor"`
}

type projPerf struct {
	XP     float64  `json:"xp"`
	Mood   float64  `json:"mood"`
	Energy float64  `json:"energy"`
	Drops  []string `json:"drops"`
}

type projClass struct {
	Reqs    []projReq   `json:"reqs"`
	Rewards projRewards `json:"rewards"`
	Perf    projPerf    `json:"perf"`
	Pct     int         `json:"pct"`
	Cut     int         `json:"cut"`
}

type projPhaseDef struct {
	C     string `json:"c"`
	Delay int64  `json:"delay"`
}

type projJob struct {
	Kind       string         `json:"kind"`
	Street     string         `json:"street"`
	Offer      []string       `json:"offer"`
	Phases     []projPhaseDef `json:"phases"`
	Connecting []string       `json:"connecting"`
	Options    []string       `json:"options"`
	Divisor    int            `json:"divisor"`
}

type projDropRow struct {
	ClassID  string `json:"class_id"`
	Count    int64  `json:"count"`
	Currants int64  `json:"currants"`
}

// projDrop is one drop-table line: [cumulative %, rows].
type projDrop struct {
	Chance int
	Rows   []projDropRow
}

func (d *projDrop) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) != 2 {
		return errors.New("a drop line is [chance, rows]")
	}
	if err := json.Unmarshal(raw[0], &d.Chance); err != nil {
		return err
	}
	return json.Unmarshal(raw[1], &d.Rows)
}

type projTable struct {
	Divisor    int                   `json:"divisor"`
	WorkUnitMs int64                 `json:"workUnitMs"`
	CooldownMs int64                 `json:"cooldownMs"`
	Jobs       map[string]projJob    `json:"jobs"`
	Classes    map[string]projClass  `json:"classes"`
	Drops      map[string][]projDrop `json:"drops"`
}

func (t *projTable) class(job projJob, phase int) (projClass, bool) {
	if phase < 0 || phase >= len(job.Phases) {
		return projClass{}, false
	}
	c, ok := t.Classes[job.Phases[phase].C+"/"+strconv.Itoa(job.Divisor)]
	return c, ok
}

// —— State ——

type projContrib struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Cost int64  `json:"cost"`
}

// projPhaseRec is who put in how much in one phase, in the order they first did (ties keep it).
type projPhaseRec struct {
	Mine  []projContrib    `json:"mine,omitempty"`
	Votes map[string]int64 `json:"votes,omitempty"`
}

type projRow struct {
	Phase   int              `json:"phase"`
	Got     map[string]int64 `json:"got,omitempty"`
	Per     []projPhaseRec   `json:"per"`
	Started int64            `json:"started,omitempty"` // ms
	Ready   int64            `json:"ready,omitempty"`   // ms: the current phase opens
	Done    int64            `json:"done,omitempty"`    // ms
	Option  string           `json:"option,omitempty"`
	held    map[string]int64 // asked of the ledger, not answered (from gives)
}

// projGive is an item or currant contribution the site has been asked to take out of a ledger.
type projGive struct {
	ID       string `json:"id"`
	Job      string `json:"job"`
	Phase    int    `json:"phase"`
	Req      string `json:"req"`
	Player   int64  `json:"player"`
	Name     string `json:"name"`
	Item     string `json:"item,omitempty"`
	N        int64  `json:"n"`
	Currants bool   `json:"currants,omitempty"`
	Cost     int64  `json:"cost"`
	Option   string `json:"option,omitempty"`
	busy     bool
	tries    int
}

func (g *projGive) goods() tradeGoods {
	if g.Currants {
		return tradeGoods{Items: []tradeItem{}, Currants: g.N}
	}
	return tradeGoods{Items: []tradeItem{{C: g.Item, N: int(g.N)}}}
}

// projCredit is a phase reward's currants and items, to go into a ledger.
type projCredit struct {
	ID     string          `json:"id"`
	Player int64           `json:"player"`
	Goods  tradeGoods      `json:"goods"`
	Msg    json.RawMessage `json:"msg"`
	busy   bool
	tries  int
}

type projOwed struct {
	ID  string          `json:"id"`
	Msg json.RawMessage `json:"msg"`
}

type projState struct {
	Jobs    map[string]*projRow    `json:"jobs"`
	Rest    map[string]int64       `json:"rest"` // street -> ms its last project finished
	Gives   map[string]*projGive   `json:"gives"`
	Credits map[string]*projCredit `json:"credits"`
	Owed    map[int64][]projOwed   `json:"owed"`
	Seq     int64                  `json:"seq"`
}

func newProjState() *projState {
	return &projState{Jobs: map[string]*projRow{}, Rest: map[string]int64{}, Gives: map[string]*projGive{}, Credits: map[string]*projCredit{}, Owed: map[int64][]projOwed{}}
}

// projectLedger is the site's ledger for projects: take gave out, put got in, once per id.
type projectLedger interface {
	project(id string, user int64, gave, got tradeGoods) ledgerAnswer
}

func (l *httpLedger) project(id string, user int64, gave, got tradeGoods) ledgerAnswer {
	return l.call(ledgerRouteProject, map[string]any{"id": id, "user": user, "gave": goodsJSON(gave), "got": goodsJSON(got)})
}

type projects struct {
	h       *Hub
	t       projTable
	offer   map[string][]string // street -> job ids its board offers
	st      *projState
	path    string // "" memory only
	boot    string
	ledger  projectLedger // nil: no ledger (see trusted)
	trusted bool          // -trade-ledger off: item and currant gives count as sent (development only)
	spawn   func(func())
	rng     func() float64
	workAt  map[int64]time.Time
	giveAt  map[int64]time.Time
}

func newProjects(h *Hub) Module {
	var t projTable
	if err := json.Unmarshal(projectsJSON, &t); err != nil || len(t.Jobs) == 0 {
		panic(fmt.Sprintf("realtime: data/projects.json: %v", err))
	}
	path := projectsFileForTest
	if !testing.Testing() && projectsFileForTest == "" {
		path = *projectsFileFlag
		if path == "" {
			if f := flag.Lookup("secret-file"); f != nil && f.Value.String() != "" {
				path = filepath.Join(filepath.Dir(expandHome(f.Value.String())), "realtime-projects.json")
			}
		}
	}
	if path == "off" {
		path = ""
	}
	m := newProjectsWith(h, t, path)
	if !testing.Testing() {
		site, missing := newLedgerFromFlags()
		if hl, ok := site.(*httpLedger); ok && hl != nil {
			m.ledger = hl
		}
		m.trusted = site == nil && !missing
	}
	return m
}

func newProjectsWith(h *Hub, t projTable, path string) *projects {
	if t.Divisor <= 0 {
		t.Divisor = 50
	}
	if t.WorkUnitMs <= 0 {
		t.WorkUnitMs = 1500
	}
	if t.CooldownMs <= 0 {
		t.CooldownMs = 30 * 60 * 1000
	}
	m := &projects{h: h, t: t, offer: map[string][]string{}, st: newProjState(), path: path,
		boot: strconv.FormatInt(h.Now().UnixMilli(), 36), spawn: func(fn func()) { go fn() }, rng: rand.Float64,
		workAt: map[int64]time.Time{}, giveAt: map[int64]time.Time{}}
	ids := make([]string, 0, len(t.Jobs))
	for id := range t.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, s := range t.Jobs[id].Offer {
			m.offer[s] = append(m.offer[s], id)
		}
	}
	if path != "" {
		if err := m.load(); err != nil {
			log.Printf("projects %s: %v (street projects are kept in memory only)", path, err)
			m.path = ""
		} else {
			log.Printf("projects %s: %d projects, %d contributions waiting for the ledger, %d rewards to credit", path, len(m.st.Jobs), len(m.st.Gives), len(m.st.Credits))
		}
	}
	return m
}

func (m *projects) Register(r *Registry) {
	r.Handle("project_give", m.give)
	r.Handle("project_work", m.work)
	r.Handle("project_ack", m.ack)
	r.Handle("project_sync", m.sync)
	r.OnConnect(m.connected)
	r.OnDisconnect(func(cl *Client) {
		delete(m.workAt, cl.ID())
		delete(m.giveAt, cl.ID())
	})
}

// —— Persistence ——

func (m *projects) load() error {
	b, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st := newProjState()
	if err := json.Unmarshal(b, st); err != nil {
		return err
	}
	for id, row := range st.Jobs {
		if _, ok := m.t.Jobs[id]; !ok || row == nil {
			delete(st.Jobs, id)
			continue
		}
		if row.Got == nil {
			row.Got = map[string]int64{}
		}
		row.held = map[string]int64{}
	}
	for id, g := range st.Gives {
		row := st.Jobs[g.Job]
		if row == nil || row.Phase != g.Phase || !projIDOK(id) {
			delete(st.Gives, id)
			continue
		}
		row.held[g.Req] += g.N
	}
	if st.Rest == nil {
		st.Rest = map[string]int64{}
	}
	if st.Credits == nil {
		st.Credits = map[string]*projCredit{}
	}
	if st.Owed == nil {
		st.Owed = map[int64][]projOwed{}
	}
	if st.Gives == nil {
		st.Gives = map[string]*projGive{}
	}
	m.st = st
	return nil
}

// save writes the whole state; false when it could not (it stays in memory).
func (m *projects) save() bool {
	if m.path == "" {
		return true
	}
	b, err := json.Marshal(m.st)
	if err == nil {
		err = writeFileSynced(m.path, b)
	}
	if err != nil {
		log.Printf("projects %s: %v", m.path, err)
		return false
	}
	return true
}

func writeFileSynced(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// —— Rules ——

func gStreet(s string) string {
	if strings.HasPrefix(s, "L") {
		return "G" + s[1:]
	}
	return s
}

func projIDOK(id string) bool {
	if id == "" || len(id) > projectMaxID {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func (m *projects) ms() int64 { return m.h.Now().UnixMilli() }

func (m *projects) row(id string) *projRow {
	r := m.st.Jobs[id]
	if r == nil {
		r = &projRow{Got: map[string]int64{}, held: map[string]int64{}}
		m.st.Jobs[id] = r
	}
	if r.held == nil {
		r.held = map[string]int64{}
	}
	return r
}

// gate: may this player put anything into job now? ("" when yes)
func (m *projects) gate(cl *Client, jobID string) (projJob, string) {
	job, ok := m.t.Jobs[jobID]
	if !ok {
		return job, projectTextNoJob
	}
	here := gStreet(cl.Street())
	at := false
	for _, s := range job.Offer {
		if s == here {
			at = true
		}
	}
	if !at {
		return job, projectTextNotHere
	}
	r := m.st.Jobs[jobID]
	now := m.ms()
	if r != nil && r.Done > 0 {
		return job, projectTextDone
	}
	if r != nil && r.Ready > now {
		return job, projectTextDelayed
	}
	// locations/inc_jobs.js jobs_get_available: no new street-building job for 30 minutes after one finished here.
	if (r == nil || r.Started == 0) && job.Kind == "street" {
		if last := m.st.Rest[here]; last > 0 && now-last < m.t.CooldownMs {
			return job, projectTextCooldown
		}
	}
	return job, ""
}

type reqView struct {
	projReq
	got, held  int64
	done, open bool
}

// reqs is the current phase's requirements with progress and the group gate (quest.js getCurrentReqGroup).
func (m *projects) reqs(job projJob, r *projRow) []reqView {
	phase := 0
	if r != nil {
		phase = r.Phase
	}
	cls, ok := m.t.class(job, phase)
	if !ok {
		return nil
	}
	out := make([]reqView, len(cls.Reqs))
	for i, q := range cls.Reqs {
		v := reqView{projReq: q}
		if r != nil {
			v.got = min(q.Need, r.Got[q.ID])
			v.held = r.held[q.ID]
		}
		v.done = v.got >= q.Need
		out[i] = v
	}
	open := map[string]int{}
	for _, b := range uniqueBuckets(out) {
		var groups []int
		seen := map[int]bool{}
		for _, v := range out {
			if v.B == b && !seen[v.G] {
				seen[v.G] = true
				groups = append(groups, v.G)
			}
		}
		sort.Ints(groups)
		at := 1
		for _, g := range groups {
			all := true
			for _, v := range out {
				if v.B == b && v.G == g && !v.done {
					all = false
				}
			}
			if all {
				at = g + 1
			}
		}
		open[b] = at
	}
	for i := range out {
		out[i].open = !out[i].done && out[i].G <= open[out[i].B]
	}
	return out
}

func uniqueBuckets(rs []reqView) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range rs {
		if !seen[v.B] {
			seen[v.B] = true
			out = append(out, v.B)
		}
	}
	return out
}

func (m *projects) option(job projJob, want string) string {
	if job.Kind != "upgrade" || len(job.Options) == 0 {
		return ""
	}
	for _, o := range job.Options {
		if o == want {
			return o
		}
	}
	return job.Options[0]
}

// contribute counts base cost for a player in the row's current phase (quest.js addPlayerContribution).
func (m *projects) contribute(r *projRow, id int64, name string, cost int64, option string) {
	if cost <= 0 {
		return
	}
	if r.Started == 0 {
		r.Started = m.ms()
	}
	for len(r.Per) <= r.Phase {
		r.Per = append(r.Per, projPhaseRec{})
	}
	rec := &r.Per[r.Phase]
	found := false
	for i := range rec.Mine {
		if rec.Mine[i].ID == id {
			rec.Mine[i].Cost += cost
			rec.Mine[i].Name = name
			found = true
		}
	}
	if !found {
		rec.Mine = append(rec.Mine, projContrib{ID: id, Name: name, Cost: cost})
	}
	if option != "" {
		if rec.Votes == nil {
			rec.Votes = map[string]int64{}
		}
		rec.Votes[option] += cost
	}
}

// —— Handlers ——

func (m *projects) refuse(cl *Client, text string) { m.h.ErrorLocked(cl, "project", text) }

func (m *projects) give(cl *Client, frame json.RawMessage) {
	var msg struct {
		Job      string `json:"job"`
		Req      string `json:"req"`
		N        int64  `json:"n"`
		Currants int64  `json:"currants"`
		Option   string `json:"option"`
	}
	if json.Unmarshal(frame, &msg) != nil || len(msg.Job) > 16 || len(msg.Req) > 16 || len(msg.Option) > 16 {
		m.refuse(cl, projectTextNoJob)
		return
	}
	now := m.h.Now()
	if last, ok := m.giveAt[cl.ID()]; ok && now.Sub(last) < projectGiveEvery {
		m.refuse(cl, projectTextCounting)
		return
	}
	for _, g := range m.st.Gives {
		if g.Player == cl.ID() {
			m.refuse(cl, projectTextCounting)
			return
		}
	}
	job, why := m.gate(cl, msg.Job)
	if why != "" {
		m.refuse(cl, why)
		return
	}
	r := m.st.Jobs[msg.Job]
	var req *reqView
	for _, v := range m.reqs(job, r) {
		if v.ID == msg.Req {
			v := v
			req = &v
		}
	}
	currants := req != nil && req.Type == "currants"
	if req == nil || (req.Type != "item" && req.Type != "currants") {
		m.refuse(cl, projectTextNoNeed)
		return
	}
	if !req.open {
		if req.done {
			m.refuse(cl, projectTextNoNeed)
		} else {
			m.refuse(cl, projectTextEarlier)
		}
		return
	}
	want := msg.N
	if currants {
		want = msg.Currants
	}
	n := min(want, req.Need-req.got-req.held, projectMaxN)
	if n <= 0 {
		if currants {
			m.refuse(cl, projectTextNoCurrants)
		} else {
			m.refuse(cl, projectTextNoNeed)
		}
		return
	}
	if m.ledger == nil && !m.trusted {
		m.refuse(cl, projectTextNoLedger)
		return
	}
	m.giveAt[cl.ID()] = now
	r = m.row(msg.Job)
	m.st.Seq++
	g := &projGive{ID: fmt.Sprintf("job-g-%s-%s", m.boot, strconv.FormatInt(m.st.Seq, 36)), Job: msg.Job, Phase: r.Phase, Req: req.ID, Player: cl.ID(), Name: cl.Name(),
		Item: req.C, N: n, Currants: currants, Cost: n * req.Cost, Option: m.option(job, msg.Option)}
	if m.ledger == nil { // -trade-ledger off: the client's word (development only)
		m.counted(g)
		return
	}
	m.st.Gives[g.ID] = g
	r.held[g.Req] += g.N
	if !m.save() {
		delete(m.st.Gives, g.ID)
		r.held[g.Req] -= g.N
		m.refuse(cl, projectTextNotSaved)
		return
	}
	m.broadcast(msg.Job)
	m.askGive(g)
}

// askGive asks the site off the hub lock; the answer comes back under it.
func (m *projects) askGive(g *projGive) {
	g.busy = true
	site := m.ledger
	id, player, gave := g.ID, g.Player, g.goods()
	m.spawn(func() {
		a := site.project(id, player, gave, tradeGoods{Items: []tradeItem{}})
		m.h.After(0, func() { m.gaveAnswer(g, a) })
	})
}

func (m *projects) gaveAnswer(g *projGive, a ledgerAnswer) {
	g.busy = false
	if m.st.Gives[g.ID] != g {
		return
	}
	switch a.Status {
	case "settled":
		m.release(g)
		m.counted(g)
	case "refused", "void":
		m.release(g)
		m.save()
		log.Printf("project %s: give %s refused by the ledger (%d %s%s)", g.Job, g.ID, g.N, g.Item, map[bool]string{true: "currants"}[g.Currants])
		if cl := m.h.ClientLocked(g.Player); cl != nil {
			if g.Currants {
				m.refuse(cl, projectTextNotTaken)
			} else {
				m.refuse(cl, projectTextNoItem)
			}
		}
		m.broadcast(g.Job)
	default:
		// No answer: ask again with the same id (the site records it once), later and later.
		g.tries++
		wait := min(time.Minute, time.Second<<min(g.tries, 6))
		log.Printf("project %s: no ledger answer for give %s (try %d): again in %s", g.Job, g.ID, g.tries, wait)
		m.h.After(wait, func() {
			if m.st.Gives[g.ID] == g && !g.busy {
				m.askGive(g)
			}
		})
	}
}

func (m *projects) release(g *projGive) {
	delete(m.st.Gives, g.ID)
	if r := m.st.Jobs[g.Job]; r != nil {
		r.held[g.Req] -= g.N
		if r.held[g.Req] <= 0 {
			delete(r.held, g.Req)
		}
	}
}

// counted: the ledger took it (or the client's word, ledger off). It goes in, the giver hears, the street sees.
func (m *projects) counted(g *projGive) {
	r := m.row(g.Job)
	job := m.t.Jobs[g.Job]
	if r.Phase == g.Phase && r.Done == 0 {
		r.Got[g.Req] += g.N
		m.contribute(r, g.Player, g.Name, g.Cost, g.Option)
	}
	cls := ""
	if g.Phase < len(job.Phases) {
		cls = job.Phases[g.Phase].C
	}
	msg := map[string]any{"t": "project_given", "id": g.ID, "job": g.Job, "req": g.Req, "cls": cls, "phase": g.Phase, "cost": g.Cost, "items": map[string]int64{}, "currants": int64(0)}
	if g.Currants {
		msg["currants"] = g.N
	} else {
		msg["items"] = map[string]int64{g.Item: g.N}
	}
	m.owe(g.Player, g.ID, msg)
	log.Printf("project %s: %s gave %d %s (base cost %d)", g.Job, g.Name, g.N, map[bool]string{true: "currants", false: g.Item}[g.Currants], g.Cost)
	m.settleIfComplete(g.Job)
	m.save()
	m.broadcast(g.Job)
}

func (m *projects) work(cl *Client, frame json.RawMessage) {
	var msg struct {
		Job    string `json:"job"`
		Req    string `json:"req"`
		Option string `json:"option"`
	}
	if json.Unmarshal(frame, &msg) != nil || len(msg.Job) > 16 || len(msg.Req) > 16 || len(msg.Option) > 16 {
		m.refuse(cl, projectTextNoWork)
		return
	}
	job, why := m.gate(cl, msg.Job)
	if why != "" {
		m.refuse(cl, why)
		return
	}
	var req *reqView
	for _, v := range m.reqs(job, m.st.Jobs[msg.Job]) {
		if v.ID == msg.Req && v.Type == "work" {
			v := v
			req = &v
		}
	}
	if req == nil {
		m.refuse(cl, projectTextNoWork)
		return
	}
	if !req.open {
		if req.done {
			m.refuse(cl, projectTextWorkDone)
		} else {
			m.refuse(cl, projectTextEarlier)
		}
		return
	}
	if req.Need-req.got-req.held <= 0 {
		m.refuse(cl, projectTextWorkDone)
		return
	}
	// items/include/jobs.js startJobWork: one unit every 1,500 ms.
	now := m.h.Now()
	unit := time.Duration(m.t.WorkUnitMs) * time.Millisecond
	next, ok := m.workAt[cl.ID()]
	if ok && now.Before(next.Add(-projectWorkSlack)) {
		m.refuse(cl, projectTextPace)
		return
	}
	if !ok || next.Before(now) {
		next = now
	}
	m.workAt[cl.ID()] = next.Add(unit)
	r := m.row(msg.Job)
	r.Got[req.ID]++
	m.contribute(r, cl.ID(), cl.Name(), req.Cost, m.option(job, msg.Option))
	m.h.SendLocked(cl, map[string]any{"t": "project_worked", "job": msg.Job, "req": req.ID, "cost": req.Cost, "left": req.Need - req.got - 1})
	m.settleIfComplete(msg.Job)
	m.save()
	m.broadcast(msg.Job)
}

func (m *projects) ack(cl *Client, frame json.RawMessage) {
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

// owe keeps a message for a player until they ack it, and sends it now when they are online.
func (m *projects) owe(player int64, id string, msg map[string]any) {
	b, _ := json.Marshal(msg)
	for _, o := range m.st.Owed[player] {
		if o.ID == id {
			return
		}
	}
	m.st.Owed[player] = append(m.st.Owed[player], projOwed{ID: id, Msg: b})
	if cl := m.h.ClientLocked(player); cl != nil {
		m.h.SendLocked(cl, json.RawMessage(b))
	}
}

// —— Finishing a phase ——

// jsRound is Math.round: halves go up.
func jsRound(x float64) int64 { return int64(math.Floor(x + 0.5)) }

func (m *projects) rollDrop(table []projDrop) *projDropRow {
	roll := int(math.Floor(m.rng()*100)) + 1
	for _, d := range table {
		if roll <= d.Chance && len(d.Rows) > 0 {
			row := d.Rows[int(math.Floor(m.rng()*float64(len(d.Rows))))%len(d.Rows)]
			return &row
		}
	}
	return nil
}

// winningOption: quest.js getWinningOption, the most base cost voted, the first of a tie.
func winningOption(job projJob, votes map[string]int64) string {
	if len(job.Options) == 0 {
		return ""
	}
	best, at := job.Options[0], int64(-1)
	for _, o := range job.Options {
		if votes[o] > at {
			best, at = o, votes[o]
		}
	}
	return best
}

func (m *projects) settleIfComplete(jobID string) {
	job := m.t.Jobs[jobID]
	r := m.st.Jobs[jobID]
	if r == nil || r.Done > 0 {
		return
	}
	reqs := m.reqs(job, r)
	if len(reqs) == 0 {
		return
	}
	for _, v := range reqs {
		if !v.done {
			return
		}
	}
	cls, _ := m.t.class(job, r.Phase)
	var total int64
	for _, q := range cls.Reqs {
		total += q.Need * q.Cost // quest.js getTotalBasecost
	}
	for len(r.Per) <= r.Phase {
		r.Per = append(r.Per, projPhaseRec{})
	}
	rec := r.Per[r.Phase]
	sorted := append([]projContrib(nil), rec.Mine...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Cost > sorted[b].Cost })
	nWin := cls.Cut
	if cls.Pct > 0 {
		nWin = int(math.Ceil(float64(cls.Pct) / 100 * float64(len(sorted))))
	}
	winners, top := map[int64]bool{}, map[int64]bool{}
	for i, c := range sorted {
		if i < nWin {
			winners[c.ID] = true
		}
		if i < projectTopFive {
			top[c.ID] = true
		}
	}
	phase := r.Phase
	final := phase >= len(job.Phases)-1
	now := m.ms()
	option := ""
	if final && job.Kind == "upgrade" {
		option = winningOption(job, rec.Votes)
	}
	divisor := float64(job.Divisor)
	if divisor <= 0 {
		divisor = float64(m.t.Divisor)
	}
	var next int64
	if !final {
		if d := job.Phases[phase+1].Delay; d > 0 {
			next = now + d*1000
		}
	}
	type credit struct {
		c   *projCredit
		msg map[string]any
	}
	var credits []credit
	for _, c := range rec.Mine {
		share := 1.0
		if total > 0 {
			share = math.Min(1, float64(c.Cost)/float64(total))
		}
		mult := share / divisor
		rw := cls.Rewards
		favor := []map[string]any{}
		for _, f := range rw.Favor {
			favor = append(favor, map[string]any{"giant": f.Giant, "points": jsRound(f.Points * mult)})
		}
		rewards := map[string]any{"xp": jsRound(rw.XP * mult), "currants": jsRound(rw.Currants * mult), "mood": jsRound(rw.Mood * mult), "energy": jsRound(rw.Energy * mult), "favor": favor}
		goods := tradeGoods{Items: []tradeItem{}, Currants: jsRound(rw.Currants * mult)}
		perf := map[string]any{"xp": 0, "mood": 0, "energy": 0, "currants": int64(0), "items": []map[string]any{}, "favor": []any{}}
		if winners[c.ID] {
			perf["xp"], perf["mood"], perf["energy"] = cls.Perf.XP, cls.Perf.Mood, cls.Perf.Energy
			var items []map[string]any
			var cur int64
			for _, id := range cls.Perf.Drops {
				d := m.rollDrop(m.t.Drops[id])
				switch {
				case d == nil:
				case d.ClassID != "":
					n := max(1, d.Count)
					items = append(items, map[string]any{"c": d.ClassID, "n": n})
					goods.Items = append(goods.Items, tradeItem{C: d.ClassID, N: int(n)})
				case d.Currants > 0:
					cur += d.Currants
				}
			}
			if items == nil {
				items = []map[string]any{}
			}
			perf["items"], perf["currants"] = items, cur
			goods.Currants += cur
		}
		phases := 0
		for _, p := range r.Per {
			for _, o := range p.Mine {
				if o.ID == c.ID && o.Cost > 0 {
					phases++
				}
			}
		}
		id := fmt.Sprintf("job-r-%s-%d-%s-%d", jobID, phase, strconv.FormatInt(r.Started, 36), c.ID)
		msg := map[string]any{"t": "project_reward", "id": id, "job": jobID, "phase": phase, "cls": job.Phases[phase].C, "final": final,
			"share": share, "participation": jsRound(share * 100), "rewards": rewards, "performance": perf, "winner": winners[c.ID], "top5": top[c.ID],
			"phases": phases, "option": option, "readyAt": next, "count": len(rec.Mine), "cost": c.Cost, "total": total}
		credits = append(credits, credit{c: &projCredit{ID: id, Player: c.ID, Goods: goods}, msg: msg})
		log.Printf("project %s phase %d: %s put in %d of %d (%.1f%%): %d iMG, %d currants%s", jobID, phase+1, c.Name, c.Cost, total, share*100, rewards["xp"], goods.Currants, map[bool]string{true: ", a winner"}[winners[c.ID]])
	}
	// Advance the project (locations/inc_jobs.js): the next phase after its delay, or done.
	if final {
		r.Done = now
		r.Option = option
		rest := map[string]int64{}
		if job.Kind == "street" {
			for _, s := range job.Connecting {
				m.st.Rest[s] = now
				rest[s] = now
			}
		} else if job.Street != "" {
			m.st.Rest[job.Street] = now
			rest[job.Street] = now
		}
		m.toAll(map[string]any{"t": "project_built", "now": now, "job": jobID, "at": now, "option": option, "rest": rest})
		log.Printf("project %s done: %s %s", jobID, job.Kind, job.Street)
	} else {
		r.Phase = phase + 1
		r.Got = map[string]int64{}
		r.Ready = next
	}
	for _, cr := range credits {
		cr.c.Msg, _ = json.Marshal(cr.msg)
		if m.ledger == nil {
			m.owe(cr.c.Player, cr.c.ID, cr.msg)
			continue
		}
		m.st.Credits[cr.c.ID] = cr.c
	}
	m.save()
	for _, cr := range credits {
		if c := m.st.Credits[cr.c.ID]; c != nil {
			m.askCredit(c)
		}
	}
}

func (m *projects) askCredit(c *projCredit) {
	c.busy = true
	site := m.ledger
	id, player, goods := c.ID, c.Player, c.Goods
	m.spawn(func() {
		a := site.project(id, player, tradeGoods{Items: []tradeItem{}}, goods)
		m.h.After(0, func() { m.credited(c, a) })
	})
}

func (m *projects) credited(c *projCredit, a ledgerAnswer) {
	c.busy = false
	if m.st.Credits[c.ID] != c {
		return
	}
	switch a.Status {
	case "settled", "refused", "void":
		var msg map[string]any
		json.Unmarshal(c.Msg, &msg)
		if a.Status != "settled" {
			// A credit takes nothing out, so this is not expected: keep the rest of the reward, without the ledger's part.
			log.Printf("project reward %s: the ledger said %s; sent without currants and items", c.ID, a.Status)
			if rw, ok := msg["rewards"].(map[string]any); ok {
				rw["currants"] = 0
			}
			if pf, ok := msg["performance"].(map[string]any); ok {
				pf["currants"], pf["items"] = 0, []any{}
			}
		}
		delete(m.st.Credits, c.ID)
		m.owe(c.Player, c.ID, msg)
		m.save()
	default:
		c.tries++
		wait := min(time.Minute, time.Second<<min(c.tries, 6))
		log.Printf("project reward %s: no ledger answer (try %d): again in %s", c.ID, c.tries, wait)
		m.h.After(wait, func() {
			if m.st.Credits[c.ID] == c && !c.busy {
				m.askCredit(c)
			}
		})
	}
}

// toAll sends one message to every character online.
func (m *projects) toAll(msg any) {
	b, _ := json.Marshal(msg)
	for _, cl := range m.h.clients {
		m.h.pushLocked(cl, b)
	}
}

// —— What players see ——

func (m *projects) rowJSON(jobID string, r *projRow, reader int64) map[string]any {
	job := m.t.Jobs[jobID]
	out := map[string]any{"phase": 0, "got": map[string]int64{}, "held": map[string]int64{}, "mine": map[string]int64{}, "started": int64(0), "ready": int64(0), "done": int64(0), "option": "", "votes": map[string]int64{}, "top": []projContrib{}, "count": 0}
	if r == nil {
		return out
	}
	mine := map[string]int64{}
	for i, p := range r.Per {
		for _, c := range p.Mine {
			if c.ID == reader && i < len(job.Phases) {
				mine[job.Phases[i].C] += c.Cost
			}
		}
	}
	// The current phase's contributors (the last phase's once done).
	at := r.Phase
	if r.Done > 0 {
		at = len(r.Per) - 1
	}
	top := []projContrib{}
	votes := map[string]int64{}
	count := 0
	if at >= 0 && at < len(r.Per) {
		rec := r.Per[at]
		sorted := append([]projContrib(nil), rec.Mine...)
		sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Cost > sorted[b].Cost })
		top = sorted[:min(projectTopFive, len(sorted))]
		count = len(sorted)
		for k, v := range rec.Votes {
			votes[k] = v
		}
	}
	got := map[string]int64{}
	for k, v := range r.Got {
		got[k] = v
	}
	held := map[string]int64{}
	for k, v := range r.held {
		if v > 0 {
			held[k] = v
		}
	}
	out["phase"], out["got"], out["held"], out["mine"] = r.Phase, got, held, mine
	out["started"], out["ready"], out["done"], out["option"] = r.Started, r.Ready, r.Done, r.Option
	out["votes"], out["top"], out["count"] = votes, top, count
	return out
}

// broadcast sends a project's row to everyone at its board, each with their own share.
func (m *projects) broadcast(jobID string) {
	job, ok := m.t.Jobs[jobID]
	if !ok {
		return
	}
	r := m.st.Jobs[jobID]
	now := m.ms()
	seen := map[int64]bool{}
	for _, s := range job.Offer {
		for _, street := range []string{s, "L" + strings.TrimPrefix(s, "G")} {
			for _, cl := range m.h.StreetLocked(street) {
				if seen[cl.ID()] {
					continue
				}
				seen[cl.ID()] = true
				m.h.SendLocked(cl, map[string]any{"t": "project_state", "job": jobID, "now": now, "row": m.rowJSON(jobID, r, cl.ID())})
			}
		}
	}
}

// sync answers project_sync: the finished projects, this street's board and every message owed.
func (m *projects) sync(cl *Client, _ json.RawMessage) {
	built := map[string]any{}
	for id, r := range m.st.Jobs {
		if r.Done > 0 {
			built[id] = map[string]any{"at": r.Done, "option": r.Option}
		}
	}
	m.h.SendLocked(cl, map[string]any{"t": "project_world", "now": m.ms(), "built": built, "rest": m.st.Rest})
	if cl.Street() != "" {
		jobs := map[string]any{}
		for _, id := range m.offer[gStreet(cl.Street())] {
			jobs[id] = m.rowJSON(id, m.st.Jobs[id], cl.ID())
		}
		m.h.SendLocked(cl, map[string]any{"t": "project_street", "street": cl.Street(), "now": m.ms(), "jobs": jobs})
	}
	for _, o := range m.st.Owed[cl.ID()] {
		m.h.SendLocked(cl, o.Msg)
	}
}

// connected: asks from before a restart (or that got no answer) go again now that this player is here.
func (m *projects) connected(cl *Client) {
	if m.ledger == nil {
		return
	}
	for _, g := range m.st.Gives {
		if g.Player == cl.ID() && !g.busy {
			m.askGive(g)
		}
	}
	for _, c := range m.st.Credits {
		if c.Player == cl.ID() && !c.busy {
			m.askCredit(c)
		}
	}
}
