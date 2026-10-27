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
// Client to server:
//
//	{"t":"project_home","token":"…"}       a restoration the site vouches for (then project_state for it)
//	{"t":"project_home_drop","job":"h7-c-1"}  the owner gives it up
//
// Server to client (beside projects.go's):
//
//	{"t":"project_reward",…,"owner":7,"yard":"street","philanthropic":true}
//	{"t":"home_restored","id":"job-h-h7-c-1","job":"h7-c-1","proto":"proto_metal_rock","yard":"street"}   to the owner (ack it)

import (
	"encoding/json"
	"log"
	"regexp"
	"sort"
	"strconv"
	"time"
)

const (
	homeJobsPerOwner = 120            // ours: more than a full yard and back yard of cultivations
	homeJobForget    = 2 * time.Hour  // a finished restoration is dropped after this (longer than a token lives)
	homeRestoredPre  = "job-h-"

	projectTextNotYours = "Only the owner can give up this project." // ours
)

var homeJobRe = regexp.MustCompile(`^([hy])([0-9a-z]{1,8})-[0-9a-z]{1,4}-[0-9a-z]{1,2}$`)

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
}

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

// homeJobOK checks a restoration's definition against its id and the table.
func (m *projects) homeJobOK(id string, j projJob) bool {
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

// homeOffer is the restorations on a home street or in a house, in id order (for project_sync).
func (m *projects) homeOffer(street string) []string {
	owner, _ := homeOwner(street)
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
		if raw, e := signedBody(m.secret, msg.Token); e == nil && json.Unmarshal(raw, &c) == nil && c.K == "home_restore" && c.O > 0 {
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

// homeDrop is the owner giving a restoration up (Wine of the Dead, or the proto removed): what was put in is lost.
func (m *projects) homeDrop(cl *Client, frame json.RawMessage) {
	var msg struct {
		Job string `json:"job"`
	}
	if json.Unmarshal(frame, &msg) != nil || len(msg.Job) > 16 {
		return
	}
	j := m.st.Homes[msg.Job]
	if j == nil {
		return
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
