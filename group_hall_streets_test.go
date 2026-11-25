package main

import (
	"testing"
	"time"
)

// hallStreet694 is job 694's own street (its target_street in the game's street-jobs.json).
const hallStreet694 = "GA9499BQDPB2P0S"

// ownHall claims job 694 for hallGroup1 by player a and builds both phases (group_halls_test.go).
func (r *projRig) ownHall(a *Client) {
	r.t.Helper()
	r.led.currants[a.ID()] += 200
	r.do(a, map[string]any{"t": "project_claim", "job": hallJob, "group": hallGroup1})
	r.settle()
	r.fill(a, hallJob)
	r.now = r.now.Add(301 * time.Second)
	r.fill(a, hallJob)
	r.settle()
	if row := r.m.st.Jobs[hallJob]; row == nil || row.Hall == nil || !row.Hall.Owned {
		r.t.Fatalf("hall not owned: %+v", row)
	}
}

// hallMembers stands in for channels.go's signed groups lists.
func hallMembers(r *projRig, lists map[int64]map[int64]bool) {
	r.hub.mu.Lock()
	r.hub.provided["channels.inGroup"] = func(id, group int64) bool { return lists[id][group] }
	r.hub.mu.Unlock()
}

func hallErrors(msgs []map[string]any, code string) []string {
	var out []string
	for _, m := range kinds(msgs, "error") {
		if m["code"] == code {
			out = append(out, m["message"].(string))
		}
	}
	return out
}

func TestHallStreetIsTheJobsTargetStreet(t *testing.T) {
	r, _ := newHallRig(t, "", time.Unix(1_700_000_000, 0))
	if got := r.m.hallStreetJob(hallStreet694); got != hallJob {
		t.Fatalf("G street: %q", got)
	}
	if got := r.m.hallStreetJob("L" + hallStreet694[1:]); got != hallJob {
		t.Fatalf("L street: %q", got)
	}
	if got := r.m.hallStreetJob(hallBoard); got != "" {
		t.Fatalf("the lot's street is no hall: %q", got)
	}
}

func TestHallStreetLetsInMembersOnly(t *testing.T) {
	r, site := newHallRig(t, "", time.Unix(1_700_000_000, 0))
	lists := map[int64]map[int64]bool{1: {hallGroup1: true}}
	hallMembers(r, lists)
	a := r.player(1, "Ada", hallBoard)
	drain(a)
	// Not built: nobody goes in, members neither.
	r.do(a, map[string]any{"t": "join", "street": hallStreet694})
	if got := hallErrors(drain(a), "hall_members"); len(got) != 1 || got[0] != hallTextNotBuilt || a.Street() != hallBoard {
		t.Fatalf("unbuilt hall: %v on %s", got, a.Street())
	}
	r.ownHall(a)
	drain(a)
	// (Bo comes after the build, whose messages would fill an idle socket's queue.)
	b := r.player(2, "Bo", hallBoard)
	drain(b)
	// A member (the site's list says so) goes in.
	r.do(a, map[string]any{"t": "join", "street": hallStreet694})
	if got := hallErrors(drain(a), "hall_members"); len(got) != 0 || a.Street() != hallStreet694 {
		t.Fatalf("member: %v on %s", got, a.Street())
	}
	// Anyone else is told whose it is (inc_houses.js:500) and stays where they were.
	r.do(b, map[string]any{"t": "join", "street": hallStreet694})
	want := "The location is the property of Ada Pals, and you are not a member."
	if got := hallErrors(drain(b), "hall_members"); len(got) != 1 || got[0] != want || b.Street() != hallBoard {
		t.Fatalf("non-member: %v on %s", got, b.Street())
	}
	// Signing up to the group (a fresh list from the site) lets Bo in.
	lists[2] = map[int64]bool{hallGroup1: true}
	r.do(b, map[string]any{"t": "join", "street": hallStreet694})
	if b.Street() != hallStreet694 {
		t.Fatalf("new member: on %s", b.Street())
	}
	// Leaving it (the next list lacks the group) walks Bo out of the hall.
	lists[2] = map[int64]bool{}
	r.hub.mu.Lock()
	r.m.hallRecheck(b)
	r.hub.mu.Unlock()
	out := kinds(drain(b), "hall_out")
	if len(out) != 1 || out[0]["job"] != hallJob || out[0]["text"] != want || b.Street() != "" {
		t.Fatalf("left the group: %v on %q", out, b.Street())
	}
	// A member's fresh list keeps them in.
	r.hub.mu.Lock()
	r.m.hallRecheck(a)
	r.hub.mu.Unlock()
	if got := kinds(drain(a), "hall_out"); len(got) != 0 || a.Street() != hallStreet694 {
		t.Fatalf("member rechecked: %v on %s", got, a.Street())
	}
	_ = site
}

func TestHallStreetOfAPrivateGroupNamesNoGroup(t *testing.T) {
	r, site := newHallRig(t, "", time.Unix(1_700_000_000, 0))
	site.private[hallGroup1] = true
	hallMembers(r, map[int64]map[int64]bool{1: {hallGroup1: true}})
	a := r.player(1, "Ada", hallBoard)
	r.ownHall(a)
	b := r.player(2, "Bo", hallBoard)
	drain(b)
	r.do(b, map[string]any{"t": "join", "street": hallStreet694})
	if got := hallErrors(drain(b), "hall_members"); len(got) != 1 || got[0] != "The location is the property of A Private Group, and you are not a member." {
		t.Fatalf("private: %v", got)
	}
}

func TestHallStreetWithoutGroupListsLetsNobodyIn(t *testing.T) {
	// No channels module (or no list yet): the gate never guesses membership.
	r, _ := newHallRig(t, "", time.Unix(1_700_000_000, 0))
	a := r.player(1, "Ada", hallBoard)
	r.ownHall(a)
	drain(a)
	r.do(a, map[string]any{"t": "join", "street": hallStreet694})
	if got := hallErrors(drain(a), "hall_members"); len(got) != 1 || a.Street() != hallBoard {
		t.Fatalf("no list: %v on %s", got, a.Street())
	}
}

// channels.go's side: the signed list answers inGroup, and each new list asks for a recheck.
func TestHallStreetMembershipComesFromTheSignedList(t *testing.T) {
	r := newChanRig(t)
	var rechecked []int64
	r.hub.mu.Lock()
	r.hub.provided["projects.hallRecheck"] = func(cl *Client) { rechecked = append(rechecked, cl.ID()) }
	r.hub.mu.Unlock()
	a := r.connect(1, "Ada")
	in := func(id, g int64) bool {
		r.hub.mu.Lock()
		defer r.hub.mu.Unlock()
		return r.m.inGroup(id, g)
	}
	if in(1, 12) {
		t.Fatal("no list yet: in no group")
	}
	r.watch(a, []int64{12, 40}, "global", "")
	if !in(1, 12) || !in(1, 40) || in(1, 13) || in(2, 12) || len(rechecked) != 1 {
		t.Fatalf("after the list: %v %v %v rechecked %v", in(1, 12), in(1, 40), in(1, 13), rechecked)
	}
	// A forged list changes nothing.
	r.send(a, map[string]any{"t": "groups_watch", "token": chanSign([]byte("not the secret"), map[string]any{"k": "groups", "sub": 1, "g": chanIDs(13), "c": "global", "s": "", "iat": r.now.Unix(), "exp": r.now.Add(time.Hour).Unix()})})
	if in(1, 13) || !in(1, 12) || len(rechecked) != 1 {
		t.Fatalf("forged: %v %v", in(1, 13), rechecked)
	}
	// A list that has run out is no word at all.
	r.now = r.now.Add(2 * time.Hour)
	if in(1, 12) {
		t.Fatal("expired list")
	}
}
