package main

// Group halls (G331): the ten "Build a Group-owned Location" projects, built
// between real players on the Job Board like any street project (projects.go
// runs their phases, contributions, work and rewards), with what makes them a
// group's. The rules are GameServerJS quests/quest.js:
//
//   - A hall takes nothing until someone claims it for one of their groups
//     (claimJob 1297-1338; the AS3 client shows only the claim screen until
//     then, JobDialog.as:383-385). Claiming pays the claim requirement, the
//     currants (the remake's amount, the street projects' 1/50: 200 of the
//     source's 10,000), and needs a group that owns no location yet
//     (group.hasPol(), one hall a group). The site checks the membership and
//     the group's hall and takes the currants in one signed call
//     (groddle-afternoon-site src/LedgerHall.php), so a claim never pays
//     without a group behind it and never twice (the call's id is recorded).
//   - The claimed phase has `duration` minutes (job_timeout, 180). When the
//     clock runs out first, the claimer hears "Oops! The project <b>Build a
//     Group-owned Location</b> has expired!" and the job resets: everything
//     put in is lost, the claim's currants too (onJobTimeout 1356-1380, the
//     type 3 description at getDesc 188). Later phases inherit the claim and
//     have no clock (1055-1059, 1080-1086).
//   - Anyone may contribute once it is claimed: nothing in the source checks
//     the group (a non-member's dialog offers "Get some group info",
//     JobIntroUI.as:261-262).
//   - Finished (doJobEffects 1831-1863): the rewards go out as for any
//     project, then the group owns the hall, unless it owns one by now, when
//     the job resets ("Suckers!"). A private group's hall is "A Private
//     Group" (1849-1851). No street rests after a hall (locations/inc_jobs.js
//     465-468 rests types 1 and 2 only).
//
// What it cannot do: the hall's street (each job's target_street) is not in
// the public dump, no signpost leads to it and the source never made one
// (quest.js:1844 "TODO: Create the POL on the fly from the template?"), so
// the members-only door (locations/inc_pols.js pols_canEnter 278-352) has no
// street to guard. The site keeps the ownership for the day there is one.
// The source's offerer, a Group Location Marker (items/include/jobs.js canOffer
// 104), has no spot on any of the ten streets, so the street's Job Board offers
// the hall.
//
// Client to server:
//
//	{"t":"project_claim","job":"694","group":12}     claim the hall for group 12 (one of yours)
//
// Server to client (beside projects.go's):
//
//	{"t":"project_given","id":"job-g-…","job":"694","req":"claim","claim":true,"currants":200,…}   the claim paid (ack it)
//	{"t":"project_expired","id":"job-e-…","job":"694","text":"…"}                                  your claim ran out (ack it)
//	{"t":"project_built","job":"694",…,"hall":{"group":{"id":12,"name":"Ada's Pals"}}}             the group owns it (everyone)
//
// A hall's Row carries "hall": {"claim":200,"minutes":180,"claimed":bool,"claiming":bool,
// "by":{"id","name"},"group":{"id","name"},"until":ms,"owned":bool}.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const ledgerRouteHall = "/groddle/v1/ledger/hall"

// Words players read: quest.js / the client where marked, or ours.
const (
	projectTextClaimFirst    = "Claim this project first."                                          // ours (JobDialog.as shows only the claim until then)
	projectTextClaimed       = "Someone has already claimed this project."                          // ours
	projectTextClaiming      = "Someone is claiming this project right now."                        // ours
	projectTextNotHall       = "This project is not one you can claim."                             // ours
	projectTextNotMember     = "You're not in that group"                                           // group.js:1058
	projectTextHasHall       = "That group already owns a group hall."                              // ours (group.hasPol, quest.js:1325)
	projectTextClaimCurrants = "You don't have enough currants to claim this project."              // ours (JobIntroUI.as greys the claim out)
	projectTextCouldNot      = "Could not claim"                                                    // main.js:6327
	projectTextExpired       = "Oops! The project <b>Build a Group-owned Location</b> has expired!" // quest.js:1363
	hallPrivateGroup         = "A Private Group"                                                    // quest.js:1850
)

type hallGroup struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Private bool   `json:"private,omitempty"`
}

// shown is the group's name as players read it (a private group's hall is "A Private Group").
func (g hallGroup) shown() string {
	if g.Private || g.Name == "" {
		return hallPrivateGroup
	}
	return g.Name
}

// projHall is a hall's standing claim.
type projHall struct {
	Claim  string    `json:"claim"` // the claim's id (the ledger's record of its currants)
	By     int64     `json:"by"`
	ByName string    `json:"byName"`
	Group  hallGroup `json:"group"`
	Until  int64     `json:"until,omitempty"` // ms: the claimed phase's end (0 once it is done)
	Owned  bool      `json:"owned,omitempty"`
}

// hallAsk is a call to the site waiting for its answer: claim (the currants, the group), own (at the end), expire.
type hallAsk struct {
	Op       string `json:"op"`
	ID       string `json:"id"` // the claim's id: one call of each op per claim
	Job      string `json:"job"`
	Player   int64  `json:"player"`
	Name     string `json:"name"`
	Group    int64  `json:"group"`
	Currants int64  `json:"currants,omitempty"`
	busy     bool
	tries    int
}

func (a *hallAsk) key() string { return a.Op + ":" + a.ID }

type hallAnswer struct {
	Status string     `json:"status"`        // settled | refused ("" no answer)
	Why    string     `json:"why,omitempty"` // member | owns | currants | gone
	Group  *hallGroup `json:"group,omitempty"`
}

// hallSite is the site's group halls (httpLedger). Calls block; the module makes them off the hub lock.
type hallSite interface {
	hall(a hallAsk) hallAnswer
}

func (l *httpLedger) hall(a hallAsk) hallAnswer {
	body, _ := json.Marshal(map[string]any{"op": a.Op, "id": a.ID, "job": a.Job, "user": a.Player, "group": a.Group, "currants": a.Currants})
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * 400 * time.Millisecond)
		}
		ans, err := l.hallOnce(body)
		if err == nil {
			return ans
		}
		log.Printf("group hall: %s: %v", ledgerRouteHall, err)
	}
	return hallAnswer{}
}

func (l *httpLedger) hallOnce(body []byte) (hallAnswer, error) {
	var nb [8]byte
	rand.Read(nb[:])
	req, err := http.NewRequest(http.MethodPost, l.base+"/wp-json"+ledgerRouteHall, bytes.NewReader(body))
	if err != nil {
		return hallAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Groddle-Service", signService(l.secret, http.MethodPost, ledgerRouteHall, l.now().Unix(), hex.EncodeToString(nb[:]), body))
	res, err := l.client.Do(req)
	if err != nil {
		return hallAnswer{}, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	var a hallAnswer
	if (res.StatusCode == http.StatusOK || res.StatusCode == http.StatusConflict) && json.Unmarshal(raw, &a) == nil && (a.Status == "settled" || a.Status == "refused") {
		return a, nil
	}
	return hallAnswer{}, fmt.Errorf("HTTP %d: %.200s", res.StatusCode, raw)
}

// —— Claiming ——

func (m *projects) claim(cl *Client, frame json.RawMessage) {
	var msg struct {
		Job   string `json:"job"`
		Group int64  `json:"group"`
	}
	if json.Unmarshal(frame, &msg) != nil || len(msg.Job) > 16 {
		m.refuse(cl, projectTextNoJob)
		return
	}
	job, ok := m.t.Jobs[msg.Job]
	if !ok {
		m.refuse(cl, projectTextNoJob)
		return
	}
	if job.Kind != "hall" {
		m.refuse(cl, projectTextNotHall)
		return
	}
	here := gStreet(cl.Street())
	at := false
	for _, s := range job.Offer {
		at = at || s == here
	}
	if !at {
		m.refuse(cl, projectTextNotHere)
		return
	}
	m.expireHalls()
	r := m.st.Jobs[msg.Job]
	switch {
	case r != nil && r.Done > 0:
		m.refuse(cl, projectTextDone)
		return
	case r != nil && r.Hall != nil:
		m.refuse(cl, projectTextClaimed)
		return
	case m.claimAsked(msg.Job) != nil:
		m.refuse(cl, projectTextClaiming)
		return
	case msg.Group <= 0:
		m.refuse(cl, projectTextNotMember)
		return
	}
	for _, a := range m.st.Halls {
		if a.Op == "claim" && a.Player == cl.ID() {
			m.refuse(cl, projectTextCounting)
			return
		}
	}
	if m.halls == nil && !m.trusted {
		m.refuse(cl, projectTextNoLedger)
		return
	}
	m.st.Seq++
	a := &hallAsk{Op: "claim", ID: fmt.Sprintf("job-g-%s-%s", m.boot, strconv.FormatInt(m.st.Seq, 36)), Job: msg.Job, Player: cl.ID(), Name: cl.Name(), Group: msg.Group, Currants: job.Claim}
	if m.halls == nil { // -trade-ledger off: the client's word (development only)
		m.claimed(a, hallAnswer{Status: "settled", Group: &hallGroup{ID: msg.Group, Name: "Group " + strconv.FormatInt(msg.Group, 10)}})
		return
	}
	m.st.Halls[a.key()] = a
	if !m.save() {
		delete(m.st.Halls, a.key())
		m.refuse(cl, projectTextNotSaved)
		return
	}
	m.broadcast(msg.Job)
	m.askHall(a)
}

// claimAsked is the claim the site is weighing for a job, if any.
func (m *projects) claimAsked(jobID string) *hallAsk {
	for _, a := range m.st.Halls {
		if a.Op == "claim" && a.Job == jobID {
			return a
		}
	}
	return nil
}

// askHall asks the site off the hub lock; the answer comes back under it.
func (m *projects) askHall(a *hallAsk) {
	a.busy = true
	site := m.halls
	call := *a
	m.spawn(func() {
		ans := site.hall(call)
		m.h.After(0, func() { m.hallAnswered(a, ans) })
	})
}

func (m *projects) hallAnswered(a *hallAsk, ans hallAnswer) {
	a.busy = false
	if m.st.Halls[a.key()] != a {
		return
	}
	if ans.Status == "" {
		a.tries++
		wait := min(time.Minute, time.Second<<min(a.tries, 6))
		log.Printf("group hall %s: no answer to %s %s (try %d): again in %s", a.Job, a.Op, a.ID, a.tries, wait)
		m.h.After(wait, func() {
			if m.st.Halls[a.key()] == a && !a.busy {
				m.askHall(a)
			}
		})
		return
	}
	delete(m.st.Halls, a.key())
	switch a.Op {
	case "claim":
		m.claimed(a, ans)
	case "own":
		m.owned(a, ans)
	default:
		m.save()
	}
}

// claimed: the site's word on a claim.
func (m *projects) claimed(a *hallAsk, ans hallAnswer) {
	cl := m.h.ClientLocked(a.Player)
	if ans.Status != "settled" || ans.Group == nil {
		m.save()
		log.Printf("group hall %s: %s's claim for group %d refused (%s)", a.Job, a.Name, a.Group, ans.Why)
		if cl != nil {
			text := projectTextCouldNot
			switch ans.Why {
			case "member":
				text = projectTextNotMember
			case "owns":
				text = projectTextHasHall
			case "currants":
				text = projectTextClaimCurrants
			}
			m.refuse(cl, text)
		}
		m.broadcast(a.Job)
		return
	}
	job := m.t.Jobs[a.Job]
	r := m.row(a.Job)
	if r.Done > 0 || r.Hall != nil {
		// Cannot happen (one claim is asked at a time); the currants are the ledger's record either way.
		log.Printf("group hall %s: claim %s settled for a hall already claimed", a.Job, a.ID)
	} else {
		r.Hall = &projHall{Claim: a.ID, By: a.Player, ByName: a.Name, Group: *ans.Group}
		if job.Minutes > 0 && r.Phase == 0 {
			r.Hall.Until = m.ms() + job.Minutes*60_000
			m.armHall(a.Job, r.Hall)
		}
	}
	cls := ""
	if len(job.Phases) > 0 {
		cls = job.Phases[0].C
	}
	// The claim's currants left the claimer's ledger: their save applies it once, like a contribution.
	m.owe(a.Player, a.ID, map[string]any{"t": "project_given", "id": a.ID, "job": a.Job, "req": "claim", "claim": true, "cls": cls, "phase": 0, "cost": 0, "items": map[string]int64{}, "currants": a.Currants})
	log.Printf("group hall %s: %s claimed it for %s (%d currants, %d minutes)", a.Job, a.Name, ans.Group.shown(), a.Currants, job.Minutes)
	m.save()
	m.broadcast(a.Job)
}

// —— The clock ——

// armHalls sets the clock on every standing claim (after a restart).
func (m *projects) armHalls() {
	for id, r := range m.st.Jobs {
		if r.Hall != nil && r.Hall.Until > 0 {
			m.armHall(id, r.Hall)
		}
	}
}

func (m *projects) armHall(jobID string, hall *projHall) {
	wait := time.Duration(max(0, hall.Until-m.ms())) * time.Millisecond
	m.h.After(wait, func() { m.expireHalls() })
}

// expireHalls resets every hall whose claimed phase ran out of time (onJobTimeout).
func (m *projects) expireHalls() {
	now := m.ms()
	for jobID, r := range m.st.Jobs {
		if r.Hall == nil || r.Hall.Until <= 0 || now < r.Hall.Until || r.Done > 0 {
			continue
		}
		hall := *r.Hall
		delete(m.st.Jobs, jobID) // jobs_reset: everything put in is lost
		m.owe(hall.By, "job-e-"+strings.TrimPrefix(hall.Claim, "job-g-"), map[string]any{"t": "project_expired", "id": "job-e-" + strings.TrimPrefix(hall.Claim, "job-g-"), "job": jobID, "text": projectTextExpired})
		log.Printf("group hall %s: %s's claim for %s expired", jobID, hall.ByName, hall.Group.shown())
		if !m.trusted {
			// Kept until the site answers (hallsConnected asks again).
			a := &hallAsk{Op: "expire", ID: hall.Claim, Job: jobID, Player: hall.By, Name: hall.ByName, Group: hall.Group.ID}
			m.st.Halls[a.key()] = a
			m.save()
			if m.halls != nil {
				m.askHall(a)
			}
		} else {
			m.save()
		}
		m.broadcast(jobID)
	}
}

// —— Finishing ——

// hallBuilt: the last phase is done (its rewards are on their way): the hall becomes the group's.
func (m *projects) hallBuilt(jobID string, r *projRow) {
	if r.Hall == nil {
		return
	}
	r.Hall.Until = 0
	a := &hallAsk{Op: "own", ID: r.Hall.Claim, Job: jobID, Player: r.Hall.By, Name: r.Hall.ByName, Group: r.Hall.Group.ID}
	if m.halls == nil {
		m.owned(a, hallAnswer{Status: "settled", Group: &r.Hall.Group})
		return
	}
	m.st.Halls[a.key()] = a // settleIfComplete saves it with the phase
	m.askHall(a)
}

// owned: the site's word on the finished hall.
func (m *projects) owned(a *hallAsk, ans hallAnswer) {
	r := m.st.Jobs[a.Job]
	if r == nil || r.Hall == nil || r.Hall.Claim != a.ID {
		m.save()
		return
	}
	if ans.Status != "settled" {
		// "Suckers!": the group owns a hall by now, so this one resets.
		log.Printf("group hall %s: %s owns a hall already: the project resets", a.Job, r.Hall.Group.shown())
		delete(m.st.Jobs, a.Job)
		m.save()
		m.broadcast(a.Job)
		return
	}
	if ans.Group != nil {
		r.Hall.Group = *ans.Group
	}
	r.Hall.Owned = true
	m.save()
	now := m.ms()
	m.toAll(map[string]any{"t": "project_built", "now": now, "job": a.Job, "at": r.Done, "option": "", "rest": map[string]int64{}, "hall": map[string]any{"group": map[string]any{"id": r.Hall.Group.ID, "name": r.Hall.Group.shown()}}})
	log.Printf("group hall %s done: %s owns it", a.Job, r.Hall.Group.shown())
	m.broadcast(a.Job)
}

// hallsConnected: calls from before a restart (or that got no answer) go again.
func (m *projects) hallsConnected(cl *Client) {
	if m.halls == nil {
		return
	}
	for _, a := range m.st.Halls {
		if !a.busy {
			m.askHall(a)
		}
	}
}

// —— What players see ——

func (m *projects) hallJSON(jobID string, job projJob, r *projRow) map[string]any {
	out := map[string]any{"claim": job.Claim, "minutes": job.Minutes, "claimed": false, "claiming": m.claimAsked(jobID) != nil, "owned": false, "until": int64(0)}
	if r == nil || r.Hall == nil {
		return out
	}
	h := r.Hall
	out["claimed"], out["owned"], out["until"] = true, h.Owned, h.Until
	out["by"] = map[string]any{"id": h.By, "name": h.ByName}
	out["group"] = map[string]any{"id": h.Group.ID, "name": h.Group.shown()}
	return out
}
