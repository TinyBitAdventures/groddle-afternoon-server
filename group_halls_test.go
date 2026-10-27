package main

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeHallSite is the site's group halls (src/LedgerHall.php): members per group, the halls groups own,
// the claim currants out of the project ledger's balances, each call answered once per op and id.
type fakeHallSite struct {
	mu      sync.Mutex
	led     *fakeProjectLedger
	members map[int64]map[int64]bool // group -> users
	names   map[int64]string
	private map[int64]bool
	owns    map[int64]string // group -> the claim id its hall came from
	seen    map[string]hallAnswer
	calls   []string
	silent  bool
}

func newFakeHallSite(led *fakeProjectLedger) *fakeHallSite {
	return &fakeHallSite{led: led, members: map[int64]map[int64]bool{}, names: map[int64]string{}, private: map[int64]bool{}, owns: map[int64]string{}, seen: map[string]hallAnswer{}}
}

func (f *fakeHallSite) hall(a hallAsk) hallAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, a.Op+":"+a.ID)
	if f.silent {
		return hallAnswer{}
	}
	if s, ok := f.seen[a.Op+":"+a.ID]; ok {
		return s
	}
	g := &hallGroup{ID: a.Group, Name: f.names[a.Group], Private: f.private[a.Group]}
	ans := hallAnswer{Status: "settled", Group: g}
	switch a.Op {
	case "claim":
		switch {
		case !f.members[a.Group][a.Player]:
			ans = hallAnswer{Status: "refused", Why: "member"}
		case f.owns[a.Group] != "":
			ans = hallAnswer{Status: "refused", Why: "owns"}
		default:
			if l := f.led.project(a.ID, a.Player, tradeGoods{Items: []tradeItem{}, Currants: a.Currants}, tradeGoods{Items: []tradeItem{}}); l.Status != "settled" {
				ans = hallAnswer{Status: "refused", Why: "currants"}
			}
		}
	case "own":
		if f.owns[a.Group] != "" && f.owns[a.Group] != a.ID {
			ans = hallAnswer{Status: "refused", Why: "owns"}
		} else {
			f.owns[a.Group] = a.ID
		}
	}
	f.seen[a.Op+":"+a.ID] = ans
	return ans
}

const (
	hallJob    = "694"             // Addingfoot Trip's hall: job_group_ph1_01c then job_street_ph2_07
	hallBoard  = "GCR10157DMK1L0G" // Addingfoot Trip
	hallGroup1 = int64(12)
)

func newHallRig(t *testing.T, path string, now time.Time) (*projRig, *fakeHallSite) {
	led := newFakeProjectLedger()
	site := newFakeHallSite(led)
	r := newProjRig(t, path, led, now)
	r.m.halls = site
	site.members[hallGroup1] = map[int64]bool{1: true}
	site.names[hallGroup1] = "Ada Pals"
	return r, site
}

// fill puts in everything the hall's current phase needs: items and currants through the ledger, work a unit at a time.
func (r *projRig) fill(cl *Client, jobID string) (msgs []map[string]any) {
	r.t.Helper()
	defer func() {
		r.settle()
		msgs = append(msgs, drain(cl)...)
		if errs := errorsOf(msgs); len(errs) > 0 {
			r.t.Fatalf("fill %s: %v", jobID, errs)
		}
	}()
	job := r.m.t.Jobs[jobID]
	row := r.m.st.Jobs[jobID]
	cls, _ := r.m.t.class(job, row.Phase)
	for group := 1; group <= 4; group++ {
		for _, q := range cls.Reqs {
			if q.G != group {
				continue
			}
			switch q.Type {
			case "item":
				if r.led.items[cl.ID()] == nil {
					r.led.items[cl.ID()] = map[string]int64{}
				}
				r.led.items[cl.ID()][q.C] += q.Need
				r.now = r.now.Add(time.Second)
				r.do(cl, map[string]any{"t": "project_give", "job": jobID, "req": q.ID, "n": q.Need})
				r.settle()
				msgs = append(msgs, drain(cl)...)
			case "currants":
				r.led.currants[cl.ID()] += q.Need
				r.now = r.now.Add(time.Second)
				r.do(cl, map[string]any{"t": "project_give", "job": jobID, "req": q.ID, "currants": q.Need})
				r.settle()
				msgs = append(msgs, drain(cl)...)
			case "work":
				for i := int64(0); i < q.Need; i++ {
					r.now = r.now.Add(1500 * time.Millisecond)
					r.do(cl, map[string]any{"t": "project_work", "job": jobID, "req": q.ID})
				}
				msgs = append(msgs, drain(cl)...)
			}
		}
	}
	return msgs
}

func TestHallTableComesFromTheGame(t *testing.T) {
	r, _ := newHallRig(t, "", time.Unix(1_700_000_000, 0))
	halls := 0
	for _, j := range r.m.t.Jobs {
		if j.Kind == "hall" {
			halls++
		}
	}
	job := r.m.t.Jobs[hallJob]
	// job_group_ph1_01c.js: claim_reqs 10,000 currants (200 at the remake's 1/50), duration 180.
	if halls != 10 || job.Kind != "hall" || job.Claim != 200 || job.Minutes != 180 || len(job.Offer) != 1 || job.Offer[0] != hallBoard || len(job.Phases) != 2 || job.Phases[1].C != "job_street_ph2_07" || job.Phases[1].Delay != 300 {
		t.Fatalf("the ten halls, job 694: %d %+v", halls, job)
	}
}

func TestHallTakesNothingUntilAGroupClaimsIt(t *testing.T) {
	r, site := newHallRig(t, "", time.Unix(1_700_000_000, 0))
	a := r.player(1, "Ada", hallBoard)
	b := r.player(2, "Bo", hallBoard)
	drain(a)
	drain(b)
	job := r.m.t.Jobs[hallJob]
	cls, _ := r.m.t.class(job, 0)
	r.do(a, map[string]any{"t": "project_give", "job": hallJob, "req": cls.Reqs[0].ID, "n": 1})
	if got := errorsOf(drain(a)); len(got) != 1 || got[0] != projectTextClaimFirst {
		t.Fatalf("unclaimed: %v", got)
	}
	// Bo is in no group: the site says so, nothing moves.
	r.led.currants[2] = 500
	r.do(b, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	r.settle()
	if got := errorsOf(drain(b)); len(got) != 1 || got[0] != projectTextNotMember || r.led.currants[2] != 500 {
		t.Fatalf("not a member: %v %d", got, r.led.currants[2])
	}
	// Ada is, but has too few currants.
	r.led.currants[1] = 150
	r.do(a, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	r.settle()
	if got := errorsOf(drain(a)); len(got) != 1 || got[0] != projectTextClaimCurrants {
		t.Fatalf("too few currants: %v", got)
	}
	// From another street: not at the board.
	c := r.player(3, "Cy", "GOTHER")
	r.do(c, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	if got := errorsOf(drain(c)); len(got) != 1 || got[0] != projectTextNotHere {
		t.Fatalf("elsewhere: %v", got)
	}
	// With 250 currants the claim stands: 200 out of her ledger, the street sees who has it.
	r.led.currants[1] = 250
	r.do(a, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	r.settle()
	msgs := drain(a)
	given := kinds(msgs, "project_given")
	if len(given) != 1 || given[0]["claim"] != true || num(given[0]["currants"]) != 200 || given[0]["req"] != "claim" || r.led.currants[1] != 50 {
		t.Fatalf("the claim paid: %v (ledger %d)", given, r.led.currants[1])
	}
	row := lastRow(t, drain(b), hallJob)
	hall := row["hall"].(map[string]any)
	if hall["claimed"] != true || hall["group"].(map[string]any)["name"] != "Ada Pals" || hall["by"].(map[string]any)["name"] != "Ada" ||
		int64(num(hall["until"])) != r.now.UnixMilli()+180*60_000 {
		t.Fatalf("claimed row: %v", hall)
	}
	// Nobody claims it twice; and now anyone may put things in (Bo is not in the group).
	r.do(b, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	if got := errorsOf(drain(b)); len(got) != 1 || got[0] != projectTextClaimed {
		t.Fatalf("claimed twice: %v", got)
	}
	r.led.items[2] = map[string]int64{cls.Reqs[0].C: 5}
	r.now = r.now.Add(time.Second)
	r.do(b, map[string]any{"t": "project_give", "job": hallJob, "req": cls.Reqs[0].ID, "n": 1})
	r.settle()
	if got := kinds(drain(b), "project_given"); len(got) != 1 {
		t.Fatalf("a non-member contributes: %v", got)
	}
	if len(site.calls) != 3 {
		t.Fatalf("site calls: %v", site.calls)
	}
}

func TestHallFinishedBelongsToTheGroup(t *testing.T) {
	r, site := newHallRig(t, "", time.Unix(1_700_000_000, 0))
	a := r.player(1, "Ada", hallBoard)
	b := r.player(2, "Bo", "GOTHER")
	r.led.currants[1] = 200
	r.do(a, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	r.settle()
	msgs := r.fill(a, hallJob)
	row := r.m.st.Jobs[hallJob]
	if row.Phase != 1 || row.Hall == nil || row.Hall.Until != 0 {
		t.Fatalf("phase 1 done: the clock stops (%+v %+v)", row, row.Hall)
	}
	rewards := kinds(msgs, "project_reward")
	if len(rewards) != 1 || rewards[0]["final"] != false || num(rewards[0]["rewards"].(map[string]any)["xp"]) != 300 {
		t.Fatalf("phase 1 reward (15,000 iMG / 50): %v", rewards)
	}
	// The second phase opens after its 300 s.
	r.now = r.now.Add(301 * time.Second)
	r.fill(a, hallJob)
	r.settle()
	if !row.Hall.Owned || row.Done == 0 || site.owns[hallGroup1] != row.Hall.Claim {
		t.Fatalf("owned: %+v %v", row.Hall, site.owns)
	}
	built := kinds(drain(b), "project_built")
	if len(built) != 1 || built[0]["hall"].(map[string]any)["group"].(map[string]any)["name"] != "Ada Pals" {
		t.Fatalf("everyone hears it: %v", built)
	}
	if _, rested := r.m.st.Rest[hallBoard]; rested {
		t.Fatal("no street rests after a hall")
	}
	// A sync lists it built; another claim is refused.
	r.do(b, map[string]any{"t": "project_sync"})
	world := kinds(drain(b), "project_world")
	if world[0]["built"].(map[string]any)[hallJob] == nil {
		t.Fatalf("built in the world: %v", world)
	}
	r.do(a, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	if got := errorsOf(drain(a)); len(got) != 1 || got[0] != projectTextDone {
		t.Fatalf("finished: %v", got)
	}
}

func TestHallClaimRunsOutAndEverythingIsLost(t *testing.T) {
	r, site := newHallRig(t, "", time.Unix(1_700_000_000, 0))
	a := r.player(1, "Ada", hallBoard)
	r.led.currants[1] = 200
	r.do(a, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	r.settle()
	job := r.m.t.Jobs[hallJob]
	cls, _ := r.m.t.class(job, 0)
	r.led.items[1] = map[string]int64{cls.Reqs[0].C: 5}
	r.now = r.now.Add(time.Second)
	r.do(a, map[string]any{"t": "project_give", "job": hallJob, "req": cls.Reqs[0].ID, "n": 2})
	r.settle()
	drain(a)
	r.now = r.now.Add(181 * time.Minute)
	r.do(a, map[string]any{"t": "project_give", "job": hallJob, "req": cls.Reqs[0].ID, "n": 1})
	msgs := drain(a)
	exp := kinds(msgs, "project_expired")
	if len(exp) != 1 || exp[0]["text"] != projectTextExpired || r.m.st.Jobs[hallJob] != nil {
		t.Fatalf("expired: %v (row %+v)", msgs, r.m.st.Jobs[hallJob])
	}
	if got := errorsOf(msgs); len(got) != 1 || got[0] != projectTextClaimFirst {
		t.Fatalf("after the reset it needs a claim again: %v", got)
	}
	r.settle()
	if site.calls[len(site.calls)-1][:7] != "expire:" {
		t.Fatalf("the site hears of it: %v", site.calls)
	}
	// The expiry is owed until acked.
	id := exp[0]["id"].(string)
	r.do(a, map[string]any{"t": "project_sync"})
	if len(kinds(drain(a), "project_expired")) != 1 {
		t.Fatal("owed again on sync")
	}
	r.do(a, map[string]any{"t": "project_ack", "id": id})
	r.do(a, map[string]any{"t": "project_sync"})
	if len(kinds(drain(a), "project_expired")) != 0 {
		t.Fatal("acked")
	}
}

func TestHallGroupWithAHallAlreadyLosesTheProject(t *testing.T) {
	r, site := newHallRig(t, "", time.Unix(1_700_000_000, 0))
	a := r.player(1, "Ada", hallBoard)
	r.led.currants[1] = 200
	r.do(a, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	r.settle()
	msgs := r.fill(a, hallJob)
	r.now = r.now.Add(301 * time.Second)
	// Meanwhile the group got another hall ("Suckers!", quest.js:1834-1838).
	site.owns[hallGroup1] = "job-g-other-1"
	msgs = append(msgs, r.fill(a, hallJob)...)
	if r.m.st.Jobs[hallJob] != nil {
		t.Fatalf("the project resets: %+v", r.m.st.Jobs[hallJob])
	}
	if got := kinds(msgs, "project_reward"); len(got) != 2 {
		t.Fatalf("the rewards still went out: %v", got)
	}
}

func TestHallClaimOutlivesARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	start := time.Unix(1_700_000_000, 0)
	r, site := newHallRig(t, path, start)
	a := r.player(1, "Ada", hallBoard)
	r.led.currants[1] = 200
	site.silent = true
	r.do(a, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	r.settle()
	if r.m.claimAsked(hallJob) == nil {
		t.Fatal("asking")
	}
	// Restart before the site answered: the claim is asked again with the same id, and pays once.
	r2, _ := newHallRig(t, path, start.Add(time.Minute))
	r2.m.halls = site
	r2.led = site.led
	site.silent = false
	a2 := r2.player(1, "Ada", hallBoard)
	r2.settle()
	row := r2.m.st.Jobs[hallJob]
	if row == nil || row.Hall == nil || site.led.currants[1] != 0 {
		t.Fatalf("claimed after the restart: %+v (currants %d)", row, site.led.currants[1])
	}
	if got := kinds(drain(a2), "project_given"); len(got) != 1 {
		t.Fatalf("the claim's receipt: %v", got)
	}
	// And the claim (with its clock) is still there after another restart.
	r3, _ := newHallRig(t, path, start.Add(2*time.Minute))
	if h := r3.m.st.Jobs[hallJob].Hall; h == nil || h.Until != start.Add(time.Minute).UnixMilli()+180*60_000 || h.Group.Name != "Ada Pals" {
		t.Fatalf("after a second restart: %+v", h)
	}
}

func TestHallClaimThatRanOutWhileDownExpiresOnStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	start := time.Unix(1_700_000_000, 0)
	r, site := newHallRig(t, path, start)
	a := r.player(1, "Ada", hallBoard)
	r.led.currants[1] = 200
	r.do(a, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	r.settle()
	claim := r.m.st.Jobs[hallJob].Hall.Claim
	// Down for four hours: the start sets the clock that has already run out (armHalls), and the site hears.
	r2, _ := newHallRig(t, path, start.Add(4*time.Hour))
	// The start's own timer (a real one here) runs under the hub lock too.
	r2.hub.mu.Lock()
	r2.m.halls = site
	r2.m.expireHalls()
	gone, asked := r2.m.st.Jobs[hallJob] == nil, r2.m.st.Halls["expire:"+claim] != nil || len(site.calls) > 0 && site.calls[len(site.calls)-1] == "expire:"+claim
	r2.hub.mu.Unlock()
	if !gone || !asked {
		t.Fatalf("expired on start, the site to be told: %v %v", gone, asked)
	}
	a2 := r2.player(1, "Ada", hallBoard)
	r2.settle()
	if len(kinds(drain(a2), "project_expired")) != 1 || r2.m.st.Halls["expire:"+claim] != nil || site.calls[len(site.calls)-1] != "expire:"+claim {
		t.Fatalf("told on connect: %v %v", r2.m.st.Halls, site.calls)
	}
}
