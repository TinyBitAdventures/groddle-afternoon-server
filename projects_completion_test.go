package main

import (
	"testing"
	"time"
)

// A multi-phase street job, its board street and its phase count.
func multiPhaseJob(t *testing.T, r *projRig) (string, string, projJob) {
	t.Helper()
	for id, job := range r.m.t.Jobs {
		if job.Kind == "street" && len(job.Phases) >= 2 && len(job.Offer) > 0 {
			return id, job.Offer[0], job
		}
	}
	t.Skip("no multi-phase street job")
	return "", "", projJob{}
}

// fullGot is every requirement of a phase met.
func fullGot(r *projRig, job projJob, phase int) map[string]int64 {
	cls, _ := r.m.t.class(job, phase)
	got := map[string]int64{}
	for _, q := range cls.Reqs {
		got[q.ID] = q.Need
	}
	return got
}

func phaseTotal(r *projRig, job projJob, phase int) int64 {
	cls, _ := r.m.t.class(job, phase)
	var total int64
	for _, q := range cls.Reqs {
		total += q.Need * q.Cost
	}
	return total
}

// quest.js onJobComplete 1734-1773: at the end every contributor of every phase gets
// jobs_familiar_job_complete (players/inc_jobs.js 357-500), not only the last phase's; the shares are
// two decimals (quest.js expandText 261); the finisher is named.
func TestProjectEarlierPhaseHelpersHearTheEnd(t *testing.T) {
	r := newProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	r.m.ledger = nil
	r.m.trusted = true
	id, board, job := multiPhaseJob(t, r)
	last := len(job.Phases) - 1
	a := r.player(1, "Ada", board)
	b := r.player(2, "Bo", board)
	drain(a)
	drain(b)
	row := r.m.row(id)
	row.Started = r.m.ms()
	row.Phase = last
	t0 := phaseTotal(r, job, 0)
	row.Per = make([]projPhaseRec, last+1)
	row.Per[0] = projPhaseRec{Mine: []projContrib{{ID: 1, Name: "Ada", Cost: t0 / 3}, {ID: 2, Name: "Bo", Cost: t0 - t0/3}}}
	row.Per[last] = projPhaseRec{Mine: []projContrib{{ID: 2, Name: "Bo", Cost: phaseTotal(r, job, last)}}}
	row.Got = fullGot(r, job, last)
	r.m.settleIfComplete(id, 2)
	var toAda, toBo map[string]any
	for _, m := range kinds(drain(a), "project_reward") {
		toAda = m
	}
	for _, m := range kinds(drain(b), "project_reward") {
		toBo = m
	}
	if toAda == nil || toAda["complete"] != true || toAda["final"] != true {
		t.Fatalf("the phase-1 helper hears the end: %v", toAda)
	}
	rw := toAda["rewards"].(map[string]any)
	if n, _ := rw["xp"].(float64); n != 0 {
		t.Fatalf("the completion pays nothing: %v", rw)
	}
	shares := toAda["shares"].(map[string]any)
	want := float64(jsRound(float64(t0/3)/float64(t0)*10000)) / 100
	if got, _ := shares[job.Phases[0].C].(float64); got != want {
		t.Fatalf("two-decimal share of phase 1: %v want %v", got, want)
	}
	if _, ok := shares[job.Phases[last].C]; ok {
		t.Fatalf("no share named for a phase Ada skipped: %v", shares)
	}
	if toBo == nil || toBo["complete"] == true || toBo["finisher"] != true {
		t.Fatalf("the finisher's own reward: %v", toBo)
	}
	if len(r.m.st.Owed[1]) != 1 {
		t.Fatalf("owed until acked: %v", r.m.st.Owed[1])
	}
}

// getJobLeaderboard 524-560: the top ten and the reader's own place.
func TestProjectLeaderboardTopTenAndMe(t *testing.T) {
	r := newProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	row := r.m.row(projJobID)
	row.Started = r.m.ms()
	row.Per = []projPhaseRec{{}}
	for i := int64(1); i <= 12; i++ {
		row.Per[0].Mine = append(row.Per[0].Mine, projContrib{ID: i, Name: "P", Cost: 100 - i})
	}
	out := r.m.rowJSON(projJobID, row, 12)
	if top := out["top"].([]projContrib); len(top) != 10 || top[0].ID != 1 {
		t.Fatalf("the top ten: %v", top)
	}
	me := out["me"].(map[string]any)
	if me["position"] != 12 || me["cost"] != int64(88) {
		t.Fatalf("the reader's place: %v", me)
	}
	if _, ok := r.m.rowJSON(projJobID, row, 99)["me"]; ok {
		t.Fatal("no place for a reader who put nothing in")
	}
}

// locations/inc_jobs.js jobs_get_available 457-461: the 30-minute rest holds a project already under way too.
func TestProjectRestHoldsAStartedProject(t *testing.T) {
	r := newProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	r.m.ledger = nil
	r.m.trusted = true
	var street, second string
	for s, ids := range r.m.offer {
		n := 0
		for _, id := range ids {
			if r.m.t.Jobs[id].Kind == "street" {
				n++
				if n == 2 {
					street, second = s, id
				}
			}
		}
		if street != "" {
			break
		}
	}
	if street == "" {
		t.Skip("no street offers two street jobs")
	}
	r.m.row(second).Started = r.m.ms() - 1000
	r.m.st.Rest[street] = r.m.ms()
	a := r.player(1, "Ada", street)
	drain(a)
	if _, why := r.m.gate(a, second); why != projectTextCooldown {
		t.Fatalf("a started project rests too: %q", why)
	}
}
