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
// street), and not done. Among them, in order:
//  1. on the frontier: a closed street with a signpost neighbour that is open (one of the streets
//     it opens from or is offered on is not itself an unbuilt street), or an upgrade of an open
//     street;
//  2. the most progress (phases done plus the current phase's share by base cost, quest.js 1012);
//  3. not resting (a street just built offers no new build for 30 minutes, locations/inc_jobs.js);
//  4. the lowest job id (numbers before words), so every server picks the same.
//
// It stays featured until its last phase is done (featuredBuilt), then the next is picked the same
// way. An administrator may choose one or clear the choice through the console's signed call
// (project_featured below); a chosen one also stays until it is built.
//
// Server to client:
//
//	{"t":"project_featured","now":ms,"job":"62","kind":"street","street":"GLI…","board":"GLI…",
//	     "label":"Lowland Slough","hub":"Shimla Mirch","boardLabel":"…","phase":0,"phases":1,
//	     "perc":0.25,"percent":25,"since":ms,"by":"auto","row":Row}          answering every project_sync (a client syncs once connected), to everyone
//	                                                                when it changes, and as it moves (at most every featuredEvery)
//	{"t":"project_featured","now":ms,"job":""}                     none (every project is built)
//
// Row is projects.go's (rowJSON) with nobody's share in "mine". perc is the current phase's share by base cost (quest.js 1012, three places, rounded down);
// percent the whole project's, whole numbers. board is the street to build it from (the first
// offering street that is open). Global chat (channels.go notice) hears it when a project becomes
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
}

// featuredLive is what is not kept: the Global notice waiting for the chat, the progress push.
type featuredLive struct {
	notice  []string // lines for Global not yet posted (a pick before the chat module was there)
	pending bool     // a progress push is waiting for its timer
	dev     bool     // GRODDLE_PROJECTS_DEV=1: project_featured_dev
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

// closedStreets is every street still to be built: a street project's street, not done, not built from the start, not exempt.
func (m *projects) closedStreets() map[string]bool {
	out := map[string]bool{}
	for id, job := range m.t.Jobs {
		if job.Kind != "street" || job.Built || job.Exempt {
			continue
		}
		if r := m.st.Jobs[id]; r != nil && r.Done > 0 {
			continue
		}
		out[gStreet(job.Street)] = true
	}
	return out
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
	closed := m.closedStreets()
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
		_, _, whole := m.featuredProgress(id)
		resting := false
		if r := m.st.Jobs[id]; (r == nil || r.Started == 0) && job.Kind == "street" {
			for _, s := range job.Offer {
				if last := m.st.Rest[gStreet(s)]; last > 0 && now-last < m.t.CooldownMs {
					resting = true
				}
			}
		}
		list = append(list, cand{id: id, frontier: featuredFrontier(job, closed), progress: whole, resting: resting})
	}
	if len(list) == 0 {
		return ""
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.frontier != b.frontier {
			return a.frontier
		}
		if a.progress != b.progress {
			return a.progress > b.progress
		}
		if a.resting != b.resting {
			return !a.resting
		}
		return jobIDLess(a.id, b.id)
	})
	return list[0].id
}

// ensureFeatured picks a featured project when none stands (or the one kept can no longer be:
// built some other way, gone from the table). True when it changed.
func (m *projects) ensureFeatured() bool {
	if f := m.st.Featured; f != nil && m.featuredOK(f.Job) {
		return false
	}
	was := ""
	if m.st.Featured != nil {
		was = m.st.Featured.Job
	}
	next := m.pickFeatured()
	if next == "" {
		m.st.Featured = nil
	} else {
		m.st.Featured = &projFeatured{Job: next, Since: m.ms(), By: "auto"}
		m.queueNotice(m.featuredText(next))
		log.Printf("projects: featured street project %s (%s)", next, m.t.Jobs[next].Label)
	}
	if next == was {
		return false
	}
	m.save()
	return true
}

// featuredBuilt: a project's last phase is done. The featured one is thanked in Global and the next is picked.
func (m *projects) featuredBuilt(jobID string) {
	if f := m.st.Featured; f == nil || f.Job != jobID {
		return
	}
	m.queueNotice(m.builtText(jobID))
	m.st.Featured = nil
	m.ensureFeatured()
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

// featuredBoard is the street to build it from: the first offering street that is open.
func (m *projects) featuredBoard(job projJob) string {
	closed := m.closedStreets()
	for _, s := range job.Offer {
		if !closed[gStreet(s)] {
			return s
		}
	}
	if len(job.Offer) > 0 {
		return job.Offer[0]
	}
	return job.Street
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

// featuredText is Global's line when a project becomes featured.
func (m *projects) featuredText(jobID string) string {
	job := m.t.Jobs[jobID]
	label := m.jobLabel(job)
	in := ""
	if job.Hub != "" {
		in = " in " + job.Hub
	}
	if job.Kind == "upgrade" {
		return fmt.Sprintf("The featured street project is now the upgrade of %s%s. Help build it!", label, in)
	}
	if board := m.streetName(m.featuredBoard(job)); board != "" {
		return fmt.Sprintf("The featured street project is now %s, the road from %s%s. Help build it!", label, board, in)
	}
	return fmt.Sprintf("The featured street project is now %s%s. Help build it!", label, in)
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
	board := m.featuredBoard(job)
	msg["job"], msg["kind"], msg["street"], msg["board"] = f.Job, job.Kind, job.Street, board
	msg["label"], msg["hub"], msg["boardLabel"] = m.jobLabel(job), job.Hub, m.streetName(board)
	msg["phase"], msg["phases"], msg["perc"], msg["percent"] = phase, len(job.Phases), perc, int(math.Floor(whole*100))
	msg["since"], msg["by"] = f.Since, f.By
	r := m.st.Jobs[f.Job]
	if r != nil && r.Ready > m.ms() {
		msg["ready"] = r.Ready
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
		f.By = "admin"
		m.save()
		return "set"
	}
	m.st.Featured = &projFeatured{Job: jobID, Since: m.ms(), By: "admin"}
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
