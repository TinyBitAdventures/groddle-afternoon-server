package main

// Restorations on real players' homes (register G328, the Philanthropic
// Member of the Social Construct badge; lane HOMES3): a worn-out
// cultivation's restoration project, run by projects.go as a project of kind
// "restore" that everyone the owner lets stand on their home street (or in
// their house, for the back yard) shares. The rules are GameServerJS
// quest.js type 5 and players/inc_jobs.js:
//
//   - Anyone at the proto may put items, currants and work into it, the owner
//     too; the requirements, buckets and groups are the restoration class's
//     (items/proto_*.js job_class_id; the source amounts, divisor 1, as solo).
//   - Each contributor's reward is the class rewards x (their base cost / the
//     phase's total base cost, quest.js getTotalBasecost 1606-1615) x the
//     proto's lifespan multiplier (onJobComplete 1762-1767,
//     getMaxWearMultiplier; jobs_familiar_job_complete_do 437-438).
//   - A contributor who is not the owner and put in more than half gets the
//     Philanthropic Member of the Social Construct badge (inc_jobs.js
//     459-463): the reward says so (philanthropic) and the game grants it.
//   - Finished, the owner's item comes back (doJobEffects type 5): the owner
//     is told home_restored, kept until they ack it, whether or not they put
//     anything in.
//
// The job is made from the site's word: groddle/v1/homes/<id>/street
// (groddle-afternoon-site src/HomeStreet.php) reads the owner's stored save
// and signs a token for each worn-out item ({k: home_restore, sub, o, job,
// proto, yard, mult}); a player at that home hands it over (project_home)
// and the job exists from then on (it is kept in -projects-file). Its id is
// h<owner>-<item>-<cycle>, base 36 (y in place of h for the back yard), the
// same in both games. The owner drops it (project_home_drop) when they pour Wine of the
// Dead on the proto or remove it: everything put in is lost, as in the
// source. A finished restoration is forgotten two hours later.
//
// Tower floors (G795, kind "tower"): once a Tower's owner starts a floor at the
// Workbench (tower.js tower_start_expand, the owner's alone), the floor is a
// project everyone on the floor under construction shares. In Glitch it was a
// Workbench job (items/tower_expander.js configureJob, quests/job_tower_floor_3
// ..9.js, quest type 5) and items/include/jobs.js let anyone put items and
// work into a job with no owner check; the rest of the Tower's verbs were the
// owner's (furniture_tower_chassis.js pols_is_owner). The site signs it from
// the owner's stored save ({k: home_tower, sub, o, job, floor, got}); its id is
// t<owner>-<floor>, base 36, its street TOWER_<owner>_<floor - 1> (the floor
// under construction, homes.go towerOwner), its one phase job_tower_floor_<n>
// at divisor 1 (data/projects.json, the game's js/home-tower.js TOWER_JOBS).
// got is what the owner put in alone before the floor was shared: taken once,
// when the project is made, each count cut to its requirement's need. The
// rules otherwise are a restoration's (no winners, no rewards in the class,
// the Philanthropic Member badge for a visitor over half: inc_jobs.js 420-470
// covers every type 5 job). Finished, the owner is told home_tower_floor until
// they ack it, and the game adds the floor (tower.js tower_end_expand). There
// is no drop: a started floor in the source could not be given up.
//
// Client to server:
//
//	{"t":"project_home","token":"…"}       a restoration or a Tower floor the site vouches for (then project_state for it)
//	{"t":"project_home_drop","job":"h7-c-1"}  the owner gives it up
//
// Server to client (beside projects.go's):
//
//	{"t":"project_reward",…,"owner":7,"yard":"street","philanthropic":true}
//	{"t":"home_restored","id":"job-h-h7-c-1","job":"h7-c-1","proto":"proto_metal_rock","yard":"street"}   to the owner (ack it)
//	{"t":"home_tower_floor","id":"job-h-t7-3","job":"t7-3","floor":3}   to the owner (ack it): the floor is built

import (
	"encoding/json"
	"log"
	"regexp"
	"sort"
	"strconv"
	"time"
)

const (
	homeJobsPerOwner = 120           // ours: more than a full yard and back yard of cultivations
	homeJobForget    = 2 * time.Hour // a finished restoration is dropped after this (longer than a token lives)
	homeRestoredPre  = "job-h-"

	projectTextNotYours = "Only the owner can give up this project." // ours
)

var homeJobRe = regexp.MustCompile(`^([hy])([0-9a-z]{1,8})-[0-9a-z]{1,4}-[0-9a-z]{1,2}$`)

// towerJobRe is a Tower floor's id: t, the owner and the floor being built (3..9), base 36.
var towerJobRe = regexp.MustCompile(`^t([0-9a-z]{1,8})-([3-9])$`)

type projRestoration struct {
	Phases []projPhaseDef `json:"phases"`
}

type homeRestoreClaims struct {
	K     string  `json:"k"`
	Sub   int64   `json:"sub"`
	O     int64   `json:"o"`
	Job   string  `json:"job"`
	Proto string  `json:"proto"`
	Yard  string  `json:"yard"`
	Mult  float64 `json:"mult"`
	Exp   int64   `json:"exp"`
	// A Tower floor's (k home_tower): the floor being built and what the owner put in alone so far.
	Floor int              `json:"floor"`
	Got   map[string]int64 `json:"got"`
}

// isHomeKind: a project on someone's home, made from the site's word (a restoration or a Tower floor).
func isHomeKind(kind string) bool { return kind == "restore" || kind == "tower" }

// job is a project's definition: the table's, or a restoration made at a home.
func (m *projects) job(id string) (projJob, bool) {
	if j, ok := m.t.Jobs[id]; ok {
		return j, true
	}
	if j := m.st.Homes[id]; j != nil {
		return *j, true
	}
	return projJob{}, false
}

// homeStreetOf is where a restoration is offered: the home street, or the house for the back yard.
func homeStreetOf(owner int64, yard string) string {
	if yard == "back" {
		return houseStreetPrefix + strconv.FormatInt(owner, 10)
	}
	return homeStreetPrefix + strconv.FormatInt(owner, 10)
}

// towerFloorJob is a Tower floor's definition from its owner and the floor being built.
func towerFloorJob(owner int64, floor int) projJob {
	street := towerStreetPrefix + strconv.FormatInt(owner, 10) + "_" + strconv.Itoa(floor-1)
	return projJob{Kind: "tower", Street: street, Offer: []string{street}, Phases: []projPhaseDef{{C: "job_tower_floor_" + strconv.Itoa(floor)}}, Divisor: 1, Owner: owner, Mult: 1}
}

// towerJobOK checks a Tower floor's definition against its id and the table.
func (m *projects) towerJobOK(id string, j projJob) bool {
	sub := towerJobRe.FindStringSubmatch(id)
	if sub == nil || j.Kind != "tower" || j.Owner <= 0 || strconv.FormatInt(j.Owner, 36) != sub[1] {
		return false
	}
	floor, _ := strconv.Atoi(sub[2])
	want := towerFloorJob(j.Owner, floor)
	if _, ok := m.t.Classes[want.Phases[0].C+"/1"]; !ok {
		return false
	}
	return len(j.Phases) == 1 && j.Phases[0] == want.Phases[0] && j.Divisor == 1 && j.Street == want.Street && len(j.Offer) == 1 && j.Offer[0] == want.Street
}

// homeJobOK checks a restoration's (or a Tower floor's) definition against its id and the table.
func (m *projects) homeJobOK(id string, j projJob) bool {
	if j.Kind == "tower" {
		return m.towerJobOK(id, j)
	}
	sub := homeJobRe.FindStringSubmatch(id)
	if sub == nil || j.Kind != "restore" || j.Owner <= 0 || strconv.FormatInt(j.Owner, 36) != sub[2] {
		return false
	}
	if (j.Yard == "back") != (sub[1] == "y") {
		return false
	}
	rest, ok := m.t.Restorations[j.Proto]
	if !ok || len(rest.Phases) == 0 || len(j.Phases) != len(rest.Phases) {
		return false
	}
	for i, p := range rest.Phases {
		if j.Phases[i] != p {
			return false
		}
		if _, ok := m.t.Classes[p.C+"/1"]; !ok {
			return false
		}
	}
	street := homeStreetOf(j.Owner, j.Yard)
	return j.Divisor == 1 && j.Street == street && len(j.Offer) == 1 && j.Offer[0] == street && j.Mult >= 1 && j.Mult <= 2
}

// homeOffer is the restorations on a home street or in a house, and a Tower's floor under
// construction on that floor, in id order (for project_sync).
func (m *projects) homeOffer(street string) []string {
	owner, _ := homeOwner(street)
	if owner == 0 {
		owner = towerOwner(street)
	}
	if owner == 0 {
		return nil
	}
	var out []string
	for id, j := range m.st.Homes {
		if j.Owner == owner && j.Street == street {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// homeJob is project_home: the site's token names a worn-out item at a home; its project exists from now on.
func (m *projects) homeJob(cl *Client, frame json.RawMessage) {
	var msg struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(frame, &msg)
	var c homeRestoreClaims
	err := errBuddyOff
	if len(m.secret) > 0 {
		err = errBuddyToken
		if raw, e := signedBody(m.secret, msg.Token); e == nil && json.Unmarshal(raw, &c) == nil && (c.K == "home_restore" || c.K == "home_tower") && c.O > 0 {
			switch {
			case c.Sub != cl.ID():
				err = errBuddyOwner
			case c.Exp < m.h.Now().Unix():
				err = errBuddyExpired
			default:
				err = nil
			}
		}
	}
	if err != nil {
		m.h.ErrorLocked(cl, "homes", err.Error())
		return
	}
	m.forgetHomeJobs()
	id := c.Job
	if have := m.st.Homes[id]; have != nil {
		m.h.SendLocked(cl, map[string]any{"t": "project_state", "job": id, "now": m.ms(), "row": m.rowJSON(id, m.st.Jobs[id], cl.ID())})
		return
	}
	if c.K == "home_tower" {
		m.towerJob(cl, id, c)
		return
	}
	yard := "street"
	if c.Yard == "back" {
		yard = "back"
	}
	rest := m.t.Restorations[c.Proto]
	street := homeStreetOf(c.O, yard)
	j := projJob{Kind: "restore", Street: street, Offer: []string{street}, Phases: append([]projPhaseDef(nil), rest.Phases...), Divisor: 1,
		Owner: c.O, Proto: c.Proto, Yard: yard, Mult: min(2, max(1, c.Mult))}
	if !m.homeJobOK(id, j) {
		m.refuse(cl, projectTextNoJob)
		return
	}
	n := 0
	for _, h := range m.st.Homes {
		if h.Owner == c.O {
			n++
		}
	}
	if n >= homeJobsPerOwner {
		m.refuse(cl, projectTextNotSaved)
		return
	}
	m.st.Homes[id] = &j
	if !m.save() {
		delete(m.st.Homes, id)
		m.refuse(cl, projectTextNotSaved)
		return
	}
	log.Printf("project %s: restoration of %d's %s (%s yard) opened by %d", id, c.O, c.Proto, yard, cl.ID())
	m.h.SendLocked(cl, map[string]any{"t": "project_state", "job": id, "now": m.ms(), "row": m.rowJSON(id, nil, cl.ID())})
}

// towerJob makes a Tower floor's project from the site's word: what the owner put in alone so far
// is its start, each count cut to its requirement's need (an id the floor does not have is left out).
func (m *projects) towerJob(cl *Client, id string, c homeRestoreClaims) {
	j := towerFloorJob(c.O, c.Floor)
	if !m.towerJobOK(id, j) {
		m.refuse(cl, projectTextNoJob)
		return
	}
	n := 0
	for _, h := range m.st.Homes {
		if h.Owner == c.O {
			n++
		}
	}
	if n >= homeJobsPerOwner {
		m.refuse(cl, projectTextNotSaved)
		return
	}
	cls, _ := m.t.class(j, 0)
	got := map[string]int64{}
	for _, q := range cls.Reqs {
		if v := c.Got[q.ID]; v > 0 {
			got[q.ID] = min(v, q.Need)
		}
	}
	m.st.Homes[id] = &j
	r := m.row(id)
	r.Got = got
	if !m.save() {
		delete(m.st.Homes, id)
		delete(m.st.Jobs, id)
		m.refuse(cl, projectTextNotSaved)
		return
	}
	log.Printf("project %s: floor %d of %d's Tower opened by %d (%d requirements started)", id, c.Floor, c.O, cl.ID(), len(got))
	m.h.SendLocked(cl, map[string]any{"t": "project_state", "job": id, "now": m.ms(), "row": m.rowJSON(id, r, cl.ID())})
	// Everything the owner had put in alone may already be the whole floor.
	m.settleIfComplete(id)
}

// towerBuilt is tower_end_expand for the owner: the floor is built, told until acked.
func (m *projects) towerBuilt(jobID string, job projJob) {
	floor := 0
	if sub := towerJobRe.FindStringSubmatch(jobID); sub != nil {
		floor, _ = strconv.Atoi(sub[2])
	}
	log.Printf("project %s done: floor %d of %d's Tower built", jobID, floor, job.Owner)
	m.owe(job.Owner, homeRestoredPre+jobID, map[string]any{"t": "home_tower_floor", "id": homeRestoredPre + jobID, "job": jobID, "floor": floor})
}

// homeDrop is the owner giving a restoration up (Wine of the Dead, or the proto removed): what was put in is lost.
func (m *projects) homeDrop(cl *Client, frame json.RawMessage) {
	var msg struct {
		Job string `json:"job"`
	}
	if json.Unmarshal(frame, &msg) != nil || len(msg.Job) > 16 {
		return
	}
	j := m.st.Homes[msg.Job]
	if j == nil || j.Kind == "tower" {
		return // a started Tower floor could not be given up in the source
	}
	if j.Owner != cl.ID() {
		m.refuse(cl, projectTextNotYours)
		return
	}
	for _, g := range m.st.Gives {
		if g.Job == msg.Job {
			m.refuse(cl, projectTextCounting)
			return
		}
	}
	street := j.Street
	delete(m.st.Homes, msg.Job)
	delete(m.st.Jobs, msg.Job)
	m.save()
	log.Printf("project %s: restoration given up by its owner %d", msg.Job, cl.ID())
	gone := map[string]any{"t": "project_state", "job": msg.Job, "now": m.ms(), "row": m.rowJSON(msg.Job, nil, 0), "gone": true}
	for _, o := range m.h.StreetLocked(street) {
		m.h.SendLocked(o, gone)
	}
}

// homeReward adds to a restoration's project_reward who owns it and whether this share was philanthropic.
func (m *projects) homeReward(msg map[string]any, job projJob, player int64, share float64) {
	msg["owner"] = job.Owner
	msg["yard"] = job.Yard
	// players/inc_jobs.js:459-463: not the owner's home, and more than half of the project.
	msg["philanthropic"] = player != job.Owner && share > 0.5
}

// homeRestored is doJobEffects type 5 for the owner: their item comes back, told until acked.
func (m *projects) homeRestored(jobID string, job projJob) {
	log.Printf("project %s done: %d's %s restored", jobID, job.Owner, job.Proto)
	m.owe(job.Owner, homeRestoredPre+jobID, map[string]any{"t": "home_restored", "id": homeRestoredPre + jobID, "job": jobID, "proto": job.Proto, "yard": job.Yard})
}

// forgetHomeJobs drops restorations finished more than homeJobForget ago.
func (m *projects) forgetHomeJobs() {
	cut := m.ms() - homeJobForget.Milliseconds()
	for id := range m.st.Homes {
		if r := m.st.Jobs[id]; r != nil && r.Done > 0 && r.Done < cut {
			delete(m.st.Homes, id)
			delete(m.st.Jobs, id)
		}
	}
}
