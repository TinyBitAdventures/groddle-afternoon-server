package main

// The featured street project (register G656, Austin 2026-11-11): "One featured street project
// at a time, shown with the Feat and announced in Global. It might take many days before a
// project is finished, so keep it until it's finished before opening the next one." Report #183
// (Osprey): with the whole world waiting to be rebuilt, projects get lost; Glitch ran one
// well-publicised project at a time and Global filled with "Who's got loam?". Glitch had no
// featured-project screen (Tiny Speck chose which streets opened), so the rule and the words are
// ours.
//
// The pick (pickFeatured), from the open shared street projects: a street-building or
// street-upgrade job (never a group hall or a home's restoration), not built from the start, not
// one every player counts as built (projJob.Exempt: a subway street, a road from the starting
// street), not done, and one players can reach (report #566, G1075): its board stands in the open
// world, the streets the starter street reaches over open roads (openroads.go openWorld; an upgrade's
// street is in it). Parsin's Trail SE in Baqala was featured because one neighbour was built, though
// no open road joined that pocket to the world. Among them, in order:
//  1. on the frontier: a closed street with a signpost neighbour that is open (one of the streets
//     it opens from or is offered on is not itself an unbuilt street), or an upgrade of an open
//     street (with no roads in the table, the only test of where it stands);
//  2. open now: not resting, a street whose last street or upgrade job finished under 30 minutes
//     ago offers none, started or not (locations/inc_jobs.js jobs_get_available 463-467; projects.go
//     availability), so its board does not show (G1023: the line pointed players at a street with no
//     board); and not waiting between phases (projRow.Ready, projects.go gate: Glitch's jobs.js
//     getAvailableJobs 2-62 skips a waiting job, G1140). One that rests or waits comes after every
//     open one, whatever its progress;
//  3. the most progress (phases done plus the current phase's share by base cost, quest.js 1012);
//  4. the lowest job id (numbers before words), so every server picks the same.
//
// When no project is walkable (R11, 2026-11-21: the live pool was down to 5), the pick takes one
// reachable by subway instead (openroads.go openWorldBySubway; a rider needs papers), by the same
// order, and the Global line and the message say "by subway" ("subway":true). It stays until built,
// as any pick does. With none reachable even so, nothing is featured, as before.
//
// A featured project still resting (the only ones left rest, or a street project finished beside
// it after it was picked) sends the rest's end as "ready" with "rest":true, so the game says when
// its board opens; the street stays quiet meanwhile, as in Glitch.
//
// Waiting between phases (G1140, Austin 2026-11-22: "the featured line skips projects waiting
// between phases"): our rule, as Glitch had no featured project. When the featured project's phase
// is done and the next one waits (featuredPhaseWait), the rule picks again; when it finds a project
// open now, that one stands in and the waiting one is kept as Back, and Global hears it
// (standInText). When the wait ends (a timer, checked again on every sync and on start) Back is the
// featured project again, whatever the stand-in's progress, as it was featured first and is kept
// until built (G656); Global hears that too (againText). A stand-in that waits or is built in the
// meantime gives way the same way, Back kept. With nothing open to stand in, the waiting one stays
// featured and sends its "ready". An administrator's choice is not moved (it stays until built).
//
// It stays featured until its last phase is done (featuredBuilt), then the next is picked the same
// way; a pick the rule made that players cannot reach (one kept from before the reach test) is
// picked again. An administrator may choose one or clear the choice through the console's signed call
// (project_featured below); a chosen one also stays until it is built.
//
// Server to client:
//
//	{"t":"project_featured","now":ms,"job":"62","kind":"street","street":"GLI…","board":"GLI…",
//	     "label":"Lowland Slough","hub":"Shimla Mirch","boardLabel":"…","phase":0,"phases":1,
//	     "perc":0.25,"percent":25,"since":ms,"by":"auto","row":Row,
//	     "ready":ms,"rest":true,"subway":true,
//	     "back":{"job":"3","label":"Ex Lane","ready":ms}}           back: the featured project waiting between phases,
//	                                                                featured again at ready (this one stands in);
//	                                                                subway: its board is reached only by subway;
//	                                                                ready: the next phase opens (rest absent), or the
//	                                                                board street's post-build rest ends (rest true)          answering every project_sync (a client syncs once connected), to everyone
//	                                                                when it changes, and as it moves (at most every featuredEvery)
//	{"t":"project_featured","now":ms,"job":""}                     none (every project is built)
//
// Row is projects.go's (rowJSON) with nobody's share in "mine". perc is the current phase's share by base cost (quest.js 1012, three places, rounded down);
// percent the whole project's, whole numbers. board is the street to build it from (the first
// offering street in the open world). Global chat (channels.go notice) hears it when a project becomes
// featured and when it is built (featuredText, builtText).
//
// The console's call (POST /ws?call=project_featured, signed as admin.go's):
//
//	{}             200 {"status":"ok","job":"62","by":"auto"}         what is featured
//	{"job":"62"}   200 {"status":"set","job":"62"} | 400 {"status":"bad"} (not an open street project)
//	{"job":""}     200 {"status":"cleared","job":"81"}                 the rule picks again
//
// QA (GRODDLE_PROJECTS_DEV=1 only): {"t":"project_featured_dev","finish":true} puts in everything
// the featured project's current phase still needs, as the sender's own contribution (rewards and
// all), so a phase can be finished in one step.

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const featuredEvery = 3 * time.Second // progress reaches everyone at most this often

// projFeatured is the featured project as the state file keeps it.
type projFeatured struct {
	Job   string `json:"job"`
	Since int64  `json:"since"`        // ms
	By    string `json:"by,omitempty"` // "admin" when an administrator chose it
	// Subway: picked by subway because nothing was walkable; it stays while the subway reaches it.
	Subway bool `json:"subway,omitempty"`
	// Back (G1140): the featured project waiting between phases while this one stands in; it is
	// featured again when its next phase opens.
	Back *projFeatured `json:"back,omitempty"`
}

// featuredLive is what is not kept: the Global notice waiting for the chat, the progress push.
type featuredLive struct {
	notice  []string // lines for Global not yet posted (a pick before the chat module was there)
	pending bool     // a progress push is waiting for its timer
	dev     bool     // GRODDLE_PROJECTS_DEV=1: project_featured_dev
	backAt  int64    // ms the Back timer is set for (0: none)
	backOff func() bool
}

func init() { serviceHandlers["project_featured"] = adminProjectFeatured }

func (m *projects) registerFeatured(r *Registry) {
	m.feat.dev = os.Getenv("GRODDLE_PROJECTS_DEV") == "1"
	r.Provide("projects.self", m)
	r.Handle("project_featured_dev", m.featuredDev)
	// The featured project reaches a player in the answer to the project_sync every client sends
	// once connected (projects.go sync), not as an extra message on connect.
	r.OnConnect(func(*Client) { m.flushFeaturedNotice() })
}

// —— The rule ——

// featuredOK: may jobID be featured (an open shared street project)?
func (m *projects) featuredOK(jobID string) bool {
	job, ok := m.t.Jobs[jobID]
	if !ok || job.Built || job.Exempt || len(job.Phases) == 0 || (job.Kind != "street" && job.Kind != "upgrade") {
		return false
	}
	if r := m.st.Jobs[jobID]; r != nil && r.Done > 0 {
		return false
	}
	return true
}

// featuredFrontier: is the job next to the open world (a closed street with an open neighbour, an upgrade of an open street)?
func featuredFrontier(job projJob, closed map[string]bool) bool {
	if job.Kind == "upgrade" {
		return !closed[gStreet(job.Street)]
	}
	for _, list := range [][]string{job.Connecting, job.Offer} {
		for _, s := range list {
			if !closed[gStreet(s)] {
				return true
			}
		}
	}
	return false
}

// featuredProgress is a job's whole progress, 0..1: phases done plus the current phase's share by base cost.
func (m *projects) featuredProgress(jobID string) (phase int, perc, whole float64) {
	job := m.t.Jobs[jobID]
	r := m.st.Jobs[jobID]
	if r != nil {
		phase = r.Phase
	}
	var need, got int64
	for _, v := range m.reqs(job, r) {
		need += v.Need * v.Cost
		got += min(v.got, v.Need) * v.Cost
	}
	if need > 0 {
		perc = math.Floor(float64(got)/float64(need)*1000) / 1000 // quest.js 1012
	}
	if n := len(job.Phases); n > 0 {
		whole = math.Min(1, (float64(phase)+perc)/float64(n))
	}
	if r != nil && r.Done > 0 {
		whole = 1
	}
	return phase, perc, whole
}

// jobIDLess orders job ids as numbers first, then as words.
func jobIDLess(a, b string) bool {
	na, ea := strconv.Atoi(a)
	nb, eb := strconv.Atoi(b)
	switch {
	case ea == nil && eb == nil:
		return na < nb
	case ea == nil:
		return true
	case eb == nil:
		return false
	}
	return a < b
}

// pickFeatured is the rule (the file's header): the job to feature next, or "" when none is open.
func (m *projects) pickFeatured() string {
	id, _ := m.pickFeaturedWay()
	return id
}

// pickFeaturedWay is pickFeatured, and whether the pick is reached only by subway (nothing walkable).
func (m *projects) pickFeaturedWay() (string, bool) {
	closed, reach := m.openWorldNow()
	if id := m.pickFeaturedIn(closed, reach); id != "" || reach == nil {
		return id, false
	}
	if id := m.pickFeaturedIn(closed, openWorldBySubway(&m.t, closed)); id != "" {
		return id, true
	}
	return "", false
}

// pickFeaturedIn is the pick among the projects whose board stands in `reach`.
func (m *projects) pickFeaturedIn(closed, reach map[string]bool) string {
	now := m.ms()
	type cand struct {
		id       string
		frontier bool
		progress float64
		resting  bool
	}
	var list []cand
	for id, job := range m.t.Jobs {
		if !m.featuredOK(id) {
			continue
		}
		if job.Kind == "upgrade" && closed[gStreet(job.Street)] {
			continue // its board stands on a street nobody can reach yet
		}
		if !projectReachable(job, reach) {
			continue // #566: no open road from the world reaches its board
		}
		_, _, whole := m.featuredProgress(id)
		_, restEnd := m.featuredBoardRest(job, closed, reach, now)
		waits := m.featuredWaitEnd(id) > 0 // G1140: between phases
		list = append(list, cand{id: id, frontier: featuredFrontier(job, closed), progress: whole, resting: restEnd > 0 || waits})
	}
	if len(list) == 0 {
		return ""
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.frontier != b.frontier {
			return a.frontier
		}
		if a.resting != b.resting {
			return !a.resting
		}
		if a.progress != b.progress {
			return a.progress > b.progress
		}
		return jobIDLess(a.id, b.id)
	})
	return list[0].id
}

// ensureFeatured picks a featured project when none stands (or the one kept can no longer be:
// built some other way, gone from the table). A stand-in that can no longer be gives way to Back
// when it is open, else to another stand-in (refeature). True when it changed.
func (m *projects) ensureFeatured() bool {
	f := m.st.Featured
	if f != nil && m.featuredUsable(f) {
		return false
	}
	var back *projFeatured
	if f != nil && f.Back != nil && m.featuredUsable(f.Back) {
		back = f.Back
	}
	return m.refeature(back)
}

// featuredUsable: may f stay featured (an open street project players can reach, or an administrator's choice)?
func (m *projects) featuredUsable(f *projFeatured) bool {
	return m.featuredOK(f.Job) && (f.By == "admin" || m.featuredReachable(f.Job) || (f.Subway && m.featuredBySubway(f.Job)))
}

// featuredWaitEnd is when a job's next phase opens (ms), 0 when it is not waiting between phases.
func (m *projects) featuredWaitEnd(jobID string) int64 {
	if r := m.st.Jobs[jobID]; r != nil && r.Done == 0 && r.Ready > m.ms() {
		return r.Ready
	}
	return 0
}

// featuredOpenNow: does the job's board take things now (not waiting between phases, its board street not resting)?
func (m *projects) featuredOpenNow(jobID string) bool {
	if m.featuredWaitEnd(jobID) > 0 {
		return false
	}
	closed, reach, _ := m.featuredWorld(m.t.Jobs[jobID])
	_, restEnd := m.featuredBoardRest(m.t.Jobs[jobID], closed, reach, m.ms())
	return restEnd == 0
}

// refeature runs the rule again. back (nil: none) is the featured project waiting between phases:
// open again, it is featured; still waiting, a project open now stands in for it, and with none
// open it stays featured. True when the featured project changed.
func (m *projects) refeature(back *projFeatured) bool {
	was := ""
	if m.st.Featured != nil {
		was = m.st.Featured.Job
	}
	if back != nil {
		back.Back = nil
	}
	switch next, bySubway := m.pickFeaturedWay(); {
	case back != nil && (m.featuredWaitEnd(back.Job) == 0 || next == "" || next == back.Job || !m.featuredOpenNow(next)):
		// Open again, or nothing open to stand in for it (the rule ranks waiting ones last).
		m.st.Featured = back
		if back.Job != was && m.featuredWaitEnd(back.Job) == 0 {
			m.queueNotice(m.againText(back.Job))
		}
	case next == "":
		m.st.Featured = nil
	case back != nil:
		m.st.Featured = &projFeatured{Job: next, Since: m.ms(), By: "auto", Subway: bySubway, Back: back}
		if next != was {
			m.queueNotice(m.standInText(back.Job, next))
			log.Printf("projects: featured street project %s stands in while %s waits for its next phase", next, back.Job)
		}
	default:
		m.st.Featured = &projFeatured{Job: next, Since: m.ms(), By: "auto", Subway: bySubway}
		m.queueNotice(m.featuredText(next))
		log.Printf("projects: featured street project %s (%s)", next, m.t.Jobs[next].Label)
	}
	m.armFeaturedBack()
	now := ""
	if m.st.Featured != nil {
		now = m.st.Featured.Job
	}
	if now == was {
		return false
	}
	m.save()
	return true
}

// featuredPhaseWait (projects.go, a phase done and the next one waits): the featured project, or
// the one standing in for it, steps aside while it waits when another is open now (G1140).
func (m *projects) featuredPhaseWait(jobID string) {
	f := m.st.Featured
	if f == nil || f.Job != jobID || f.By == "admin" || m.featuredWaitEnd(jobID) == 0 {
		return
	}
	back := f.Back
	if back == nil || !m.featuredUsable(back) {
		self := *f
		back = &self
	}
	if m.refeature(back) {
		m.flushFeaturedNotice()
		m.toAll(m.featuredMsg())
	}
}

// featuredReopen: Back's next phase is open, so it is featured again. Run by the Back timer, on
// every sync and on start (a timer does not outlive the server).
func (m *projects) featuredReopen() {
	f := m.st.Featured
	if f == nil || f.Back == nil {
		m.armFeaturedBack()
		return
	}
	if !m.featuredUsable(f.Back) {
		f.Back = nil
		m.armFeaturedBack()
		m.save()
		return
	}
	if m.featuredWaitEnd(f.Back.Job) > 0 {
		m.armFeaturedBack()
		return
	}
	if m.refeature(f.Back) {
		m.flushFeaturedNotice()
		m.toAll(m.featuredMsg())
	}
}

// armFeaturedBack sets the timer for Back's wait to end (and stops one no longer wanted).
func (m *projects) armFeaturedBack() {
	at := int64(0)
	if f := m.st.Featured; f != nil && f.Back != nil {
		at = m.featuredWaitEnd(f.Back.Job)
	}
	if at == m.feat.backAt {
		return
	}
	if m.feat.backOff != nil {
		m.feat.backOff()
		m.feat.backOff = nil
	}
	m.feat.backAt = at
	if at == 0 || m.h == nil {
		return
	}
	m.feat.backOff = m.h.After(time.Duration(at-m.ms())*time.Millisecond, func() {
		m.feat.backAt, m.feat.backOff = 0, nil
		m.featuredReopen()
	})
}

// featuredReachable: can players reach the job's board from the open world (openroads.go)?
func (m *projects) featuredReachable(jobID string) bool {
	_, reach := m.openWorldNow()
	return projectReachable(m.t.Jobs[jobID], reach)
}

// featuredBySubway: is the job's board reached only by subway (not walkable, but the subway reaches it)?
func (m *projects) featuredBySubway(jobID string) bool {
	_, _, bySubway := m.featuredWorld(m.t.Jobs[jobID])
	return bySubway
}

// featuredWorld is the world a featured job is reached in: the open world, or (its board not in
// it) the open world by subway when that reaches it, then bySubway is true.
func (m *projects) featuredWorld(job projJob) (closed, reach map[string]bool, bySubway bool) {
	closed, reach = m.openWorldNow()
	if reach == nil || projectReachable(job, reach) {
		return closed, reach, false
	}
	if sub := openWorldBySubway(&m.t, closed); projectReachable(job, sub) {
		return closed, sub, true
	}
	return closed, reach, false
}

// featuredBuilt: a project's last phase is done. The featured one is thanked in Global and the next is picked;
// another one sends the featured project again, as its board street may rest now (G1023).
func (m *projects) featuredBuilt(jobID string) {
	if f := m.st.Featured; f == nil || f.Job != jobID {
		if f != nil {
			m.toAll(m.featuredMsg())
		}
		return
	}
	m.queueNotice(m.builtText(jobID))
	m.ensureFeatured() // the built one is no longer usable: Back, when one waits, or the rule's next
	m.flushFeaturedNotice()
	m.toAll(m.featuredMsg())
}

// featuredTouched: a project's row changed; the featured one's progress reaches everyone (throttled).
func (m *projects) featuredTouched(jobID string) {
	if f := m.st.Featured; f == nil || f.Job != jobID || m.feat.pending {
		return
	}
	m.feat.pending = true
	m.h.After(featuredEvery, func() {
		m.feat.pending = false
		m.toAll(m.featuredMsg())
	})
}

// —— Words (ours; plain, no dashes) ——

func (m *projects) streetName(tsid string) string {
	if n := m.t.Names[tsid]; n != "" {
		return n
	}
	if n := m.t.Names["L"+strings.TrimPrefix(tsid, "G")]; n != "" {
		return n
	}
	return m.t.Names["G"+strings.TrimPrefix(tsid, "L")]
}

// restEndAt is when a street's post-build rest ends (ms), 0 when it is not resting: no street or
// upgrade job is offered there for CooldownMs after its last one finished (inc_jobs.js 463-467).
func (m *projects) restEndAt(street string, now int64) int64 {
	if last := m.st.Rest[gStreet(street)]; last > 0 && now-last < m.t.CooldownMs {
		return last + m.t.CooldownMs
	}
	return 0
}

// featuredBoardRest is the street to build it from, and when that street's rest ends (0: its board
// shows now). The first open offering street not resting; when every open one rests, the one that
// opens first; with none open, the first offering street (or the job's own). Open is in the open
// world (`reach`, openroads.go) when the table has roads, else not closed.
func (m *projects) featuredBoardRest(job projJob, closed, reach map[string]bool, now int64) (string, int64) {
	board, end := "", int64(0)
	for _, s := range job.Offer {
		if closed[gStreet(s)] || (reach != nil && !reach[gStreet(s)]) {
			continue
		}
		e := int64(0)
		if job.Kind == "street" || job.Kind == "upgrade" {
			e = m.restEndAt(s, now)
		}
		if e == 0 {
			return s, 0
		}
		if board == "" || e < end {
			board, end = s, e
		}
	}
	if board != "" {
		return board, end
	}
	if len(job.Offer) > 0 {
		return job.Offer[0], 0
	}
	return job.Street, 0
}

// featuredBoard is the street to build it from (featuredBoardRest).
func (m *projects) featuredBoard(job projJob) string {
	closed, reach, _ := m.featuredWorld(job)
	board, _ := m.featuredBoardRest(job, closed, reach, m.ms())
	return board
}

func (m *projects) jobLabel(job projJob) string {
	if job.Label != "" {
		return job.Label
	}
	if n := m.streetName(job.Street); n != "" {
		return n
	}
	return "a street"
}

// featuredText is Global's line when a project becomes featured ("(by subway)" when only the subway reaches it).
func (m *projects) featuredText(jobID string) string {
	job := m.t.Jobs[jobID]
	label := m.jobLabel(job)
	in := ""
	if job.Hub != "" {
		in = " in " + job.Hub
	}
	if m.featuredBySubway(jobID) {
		in += " (by subway)"
	}
	if job.Kind == "upgrade" {
		return fmt.Sprintf("The featured street project is now the upgrade of %s%s. Help build it!", label, in)
	}
	if board := m.streetName(m.featuredBoard(job)); board != "" {
		return fmt.Sprintf("The featured street project is now %s, the road from %s%s. Help build it!", label, board, in)
	}
	return fmt.Sprintf("The featured street project is now %s%s. Help build it!", label, in)
}

// featuredName is how a line names a job mid-sentence: "Ex Lane", "the upgrade of Ay Way".
func (m *projects) featuredName(jobID string) string {
	job := m.t.Jobs[jobID]
	if job.Kind == "upgrade" {
		return "the upgrade of " + m.jobLabel(job)
	}
	return m.jobLabel(job)
}

// standInText is Global's line when a project stands in for the featured one waiting between phases (G1140).
func (m *projects) standInText(backID, jobID string) string {
	now := strings.TrimSuffix(strings.TrimPrefix(m.featuredText(jobID), "The featured street project is now "), " Help build it!")
	return fmt.Sprintf("While %s waits for its next phase, the featured street project is %s Help build it!", m.featuredName(backID), now)
}

// againText is Global's line when the waiting one's next phase opens and it is featured again (G1140).
func (m *projects) againText(jobID string) string {
	return fmt.Sprintf("The next phase of %s is open, and it is the featured street project again. Help build it!", m.featuredName(jobID))
}

// builtText is Global's line when the featured project is built.
func (m *projects) builtText(jobID string) string {
	job := m.t.Jobs[jobID]
	if job.Kind == "upgrade" {
		return fmt.Sprintf("The upgrade of %s is built! Thanks to everyone who helped.", m.jobLabel(job))
	}
	return fmt.Sprintf("%s is built! Thanks to everyone who helped.", m.jobLabel(job))
}

func (m *projects) queueNotice(text string) {
	if text != "" {
		m.feat.notice = append(m.feat.notice, text)
	}
}

// flushFeaturedNotice posts the lines waiting for Global (channels.go notice); without the chat module they are dropped.
func (m *projects) flushFeaturedNotice() {
	if len(m.feat.notice) == 0 {
		return
	}
	lines := m.feat.notice
	m.feat.notice = nil
	post, _ := m.h.ProvidedLocked("channels.notice").(func(string, string))
	if post == nil {
		return
	}
	for _, text := range lines {
		post("global", text)
	}
}

// featuredMsg is project_featured as everyone gets it.
func (m *projects) featuredMsg() map[string]any {
	msg := map[string]any{"t": "project_featured", "now": m.ms(), "job": ""}
	f := m.st.Featured
	if f == nil || !m.featuredOK(f.Job) {
		return msg
	}
	job := m.t.Jobs[f.Job]
	phase, perc, whole := m.featuredProgress(f.Job)
	now := m.ms()
	closed, reach, bySubway := m.featuredWorld(job)
	board, restEnd := m.featuredBoardRest(job, closed, reach, now)
	if bySubway {
		msg["subway"] = true
	}
	msg["job"], msg["kind"], msg["street"], msg["board"] = f.Job, job.Kind, job.Street, board
	msg["label"], msg["hub"], msg["boardLabel"] = m.jobLabel(job), job.Hub, m.streetName(board)
	msg["phase"], msg["phases"], msg["perc"], msg["percent"] = phase, len(job.Phases), perc, int(math.Floor(whole*100))
	msg["since"], msg["by"] = f.Since, f.By
	r := m.st.Jobs[f.Job]
	if r != nil && r.Ready > now {
		msg["ready"] = r.Ready
	}
	// G1023: the board street's post-build rest, when it ends later than any phase wait.
	if restEnd > now && (r == nil || restEnd > r.Ready) {
		msg["ready"], msg["rest"] = restEnd, true
	}
	// G1140: standing in for the featured project waiting between phases.
	if b := f.Back; b != nil && m.featuredOK(b.Job) {
		back := map[string]any{"job": b.Job, "label": m.jobLabel(m.t.Jobs[b.Job]), "kind": m.t.Jobs[b.Job].Kind}
		if end := m.featuredWaitEnd(b.Job); end > 0 {
			back["ready"] = end
		}
		msg["back"] = back
	}
	// The board's row (rowJSON, nobody's share): the Projects panel shows what it still needs anywhere.
	msg["row"] = m.rowJSON(f.Job, r, 0)
	return msg
}

// —— The console ——

// setFeatured makes an administrator's choice ("" clears it: the rule picks again). Returns the status word.
func (m *projects) setFeatured(jobID string) string {
	if jobID == "" {
		m.st.Featured = nil
		m.ensureFeatured()
		m.flushFeaturedNotice()
		m.toAll(m.featuredMsg())
		return "cleared"
	}
	if !m.featuredOK(jobID) {
		return "bad"
	}
	if f := m.st.Featured; f != nil && f.Job == jobID {
		f.By, f.Back = "admin", nil
		m.armFeaturedBack()
		m.save()
		return "set"
	}
	m.st.Featured = &projFeatured{Job: jobID, Since: m.ms(), By: "admin"}
	m.armFeaturedBack()
	m.queueNotice(m.featuredText(jobID))
	m.save()
	m.flushFeaturedNotice()
	m.toAll(m.featuredMsg())
	log.Printf("projects: featured street project %s chosen by an administrator", jobID)
	return "set"
}

func adminProjectFeatured(h *Hub, body []byte) (int, any) {
	var c struct {
		Job *string `json:"job"`
	}
	if json.Unmarshal(body, &c) != nil || (c.Job != nil && len(*c.Job) > 16) {
		return http.StatusBadRequest, adminBad
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	m, _ := h.provided["projects.self"].(*projects)
	if m == nil {
		return http.StatusOK, statusOf("none")
	}
	if c.Job != nil {
		st := m.setFeatured(strings.TrimSpace(*c.Job))
		if st == "bad" {
			return http.StatusBadRequest, adminBad
		}
		job := ""
		if m.st.Featured != nil {
			job = m.st.Featured.Job
		}
		return http.StatusOK, map[string]any{"status": st, "job": job}
	}
	out := map[string]any{"status": "ok", "job": ""}
	if f := m.st.Featured; f != nil {
		out["job"], out["by"], out["since"] = f.Job, f.By, f.Since
	}
	return http.StatusOK, out
}

// —— QA ——

// featuredDev (GRODDLE_PROJECTS_DEV=1): finish the featured project's current phase as the sender's contribution.
func (m *projects) featuredDev(cl *Client, frame json.RawMessage) {
	if !m.feat.dev {
		m.refuse(cl, projectTextNoJob)
		return
	}
	var msg struct {
		Finish bool `json:"finish"`
	}
	f := m.st.Featured
	if json.Unmarshal(frame, &msg) != nil || !msg.Finish || f == nil || !m.featuredOK(f.Job) {
		m.refuse(cl, projectTextNoJob)
		return
	}
	job := m.t.Jobs[f.Job]
	r := m.row(f.Job)
	r.Ready = 0
	option := m.option(job, "")
	for _, v := range m.reqs(job, r) {
		left := v.Need - v.got - v.held
		if left <= 0 {
			continue
		}
		r.Got[v.ID] += left
		m.contribute(r, cl.ID(), cl.Name(), left*v.Cost, option)
	}
	log.Printf("project %s: %s finished phase %d (project_featured_dev)", f.Job, cl.Name(), r.Phase+1)
	m.settleIfComplete(f.Job)
	m.save()
	m.broadcast(f.Job)
	m.toAll(m.featuredMsg())
}
