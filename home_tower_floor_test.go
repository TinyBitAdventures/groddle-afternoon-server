package main

import (
	"path/filepath"
	"testing"
	"time"
)

// G795: a Tower floor everyone on the floor under construction builds together (home_projects.go kind
// "tower"). Owner 7's Tower has 2 floors and is building its third: job t7-3 on TOWER_7_2.
const towerJob = "t7-3"

func towerToken(sub, owner int64, job string, floor int, got map[string]int64, now time.Time) string {
	return signBuddy(homesTestSecret, map[string]any{"k": "home_tower", "sub": sub, "o": owner, "job": job, "floor": floor, "got": got, "iat": now.Unix(), "exp": now.Add(30 * time.Minute).Unix()})
}

// floor3All is every requirement of job_tower_floor_3 at its need (js/home-tower.js TOWER_JOBS).
func floor3All() map[string]int64 {
	return map[string]int64{"r1706": 8, "r1707": 40, "r1708": 8, "r1712": 24, "r1709": 25, "r1710": 4, "r1711": 8, "r1713": 53, "r1807": 1, "r1714": 25}
}

func TestTowerFloorIsBuiltTogether(t *testing.T) {
	led := newFakeProjectLedger()
	led.items[9] = map[string]int64{"beam": 4}
	r := newHomeProjRig(t, "", led, time.Unix(1_700_000_000, 0))
	owner := r.player(7, "Ada", "TOWER_7_2")
	bo := r.player(9, "Bo", "TOWER_7_2")
	lobby := r.player(11, "Cy", "TOWER_7_0")
	drain(owner)
	drain(bo)
	drain(lobby)

	// Refused: a token for someone else, a floor that is not the job's, another owner's job, a class the table lacks.
	r.do(bo, map[string]any{"t": "project_home", "token": towerToken(11, 7, towerJob, 3, nil, r.now)})
	r.do(bo, map[string]any{"t": "project_home", "token": towerToken(9, 7, towerJob, 4, nil, r.now)})
	r.do(bo, map[string]any{"t": "project_home", "token": towerToken(9, 7, "t8-3", 3, nil, r.now)})
	r.do(bo, map[string]any{"t": "project_home", "token": towerToken(9, 7, "t7-2", 2, nil, r.now)})
	if got := len(kinds(drain(bo), "error")); got != 4 {
		t.Fatalf("bad tokens: %d errors, want 4", got)
	}
	if len(r.m.st.Homes) != 0 {
		t.Fatalf("a bad token made a job: %v", r.m.st.Homes)
	}

	// A visitor's token makes it, with what the owner put in alone: cut to each need, unknown ids left out.
	seed := map[string]int64{"r1706": 5, "r1707": 1000, "r9999": 3, "r1714": -2}
	r.do(bo, map[string]any{"t": "project_home", "token": towerToken(9, 7, towerJob, 3, seed, r.now)})
	j := r.m.st.Homes[towerJob]
	if j == nil || j.Kind != "tower" || j.Owner != 7 || j.Street != "TOWER_7_2" || j.Divisor != 1 || j.Phases[0].C != "job_tower_floor_3" {
		t.Fatalf("job: %+v", j)
	}
	if got := r.m.st.Jobs[towerJob].Got; len(got) != 2 || got["r1706"] != 5 || got["r1707"] != 40 {
		t.Fatalf("seed: %v", got)
	}
	drain(bo)
	// The same token again (the owner's, later) never seeds it twice.
	r.do(owner, map[string]any{"t": "project_home", "token": towerToken(7, 7, towerJob, 3, map[string]int64{"r1706": 8}, r.now)})
	if got := r.m.st.Jobs[towerJob].Got["r1706"]; got != 5 {
		t.Fatalf("seeded again: %d", got)
	}

	// The floor's sync lists it; another floor's does not, and nobody there can put anything in.
	r.do(owner, map[string]any{"t": "project_sync"})
	if st := kinds(drain(owner), "project_street"); len(st) != 1 || st[0]["jobs"].(map[string]any)[towerJob] == nil {
		t.Fatalf("sync on TOWER_7_2: %v", st)
	}
	r.do(lobby, map[string]any{"t": "project_sync"})
	if st := kinds(drain(lobby), "project_street"); len(st) != 1 || st[0]["jobs"].(map[string]any)[towerJob] != nil {
		t.Fatalf("sync on the ground floor: %v", st)
	}
	r.do(lobby, map[string]any{"t": "project_give", "job": towerJob, "req": "r1710", "n": 1})
	if e := kinds(drain(lobby), "error"); len(e) != 1 || e[0]["message"] != projectTextNotHere {
		t.Fatalf("from the ground floor: %v", e)
	}

	// The visitor's beams leave their ledger, as a street project's give does (bucket 2 is open from the start).
	r.do(bo, map[string]any{"t": "project_give", "job": towerJob, "req": "r1710", "n": 4})
	r.settle()
	if got := r.m.st.Jobs[towerJob].Got["r1710"]; got != 4 {
		t.Fatalf("beams counted: %d", got)
	}
	if left := led.items[9]["beam"]; left != 0 {
		t.Fatalf("beams left in Bo's ledger: %d", left)
	}
	given := kinds(drain(bo), "project_given")
	if len(given) != 1 || given[0]["items"].(map[string]any)["beam"].(float64) != 4 {
		t.Fatalf("project_given: %v", given)
	}

	// The rest: everything but the last unit of tinkering, then the visitor's one unit finishes it.
	row := r.m.st.Jobs[towerJob]
	for id, n := range floor3All() {
		row.Got[id] = n
	}
	row.Got["r1714"] = 24
	r.now = r.now.Add(1500 * time.Millisecond)
	r.do(bo, map[string]any{"t": "project_work", "job": towerJob, "req": "r1714"})
	r.settle()
	if row.Done == 0 {
		t.Fatalf("not finished: %+v", row)
	}
	rw := kinds(drain(bo), "project_reward")
	if len(rw) != 1 || rw[0]["owner"].(float64) != 7 || rw[0]["philanthropic"] != false || rw[0]["winner"] != false {
		t.Fatalf("Bo's reward: %v", rw)
	}
	built := kinds(drain(owner), "home_tower_floor")
	if len(built) != 1 || built[0]["job"] != towerJob || built[0]["floor"].(float64) != 3 || built[0]["id"] != "job-h-"+towerJob {
		t.Fatalf("home_tower_floor: %v", built)
	}
	if b := kinds(drain(lobby), "project_built"); len(b) != 0 {
		t.Fatalf("project_built went out: %v", b)
	}
	// Acked, it is not told again.
	r.do(owner, map[string]any{"t": "project_ack", "id": "job-h-" + towerJob})
	r.do(owner, map[string]any{"t": "project_sync"})
	if len(kinds(drain(owner), "home_tower_floor")) != 0 {
		t.Fatal("home_tower_floor sent after its ack")
	}
}

func TestTowerFloorFromTheOwnersSeedAloneIsBuilt(t *testing.T) {
	r := newHomeProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	bo := r.player(9, "Bo", "TOWER_7_2")
	// The owner finished it alone before it was shared: the first token finds it done, and the owner (away) is told on connect.
	r.do(bo, map[string]any{"t": "project_home", "token": towerToken(9, 7, towerJob, 3, floor3All(), r.now)})
	if row := r.m.st.Jobs[towerJob]; row == nil || row.Done == 0 {
		t.Fatalf("not finished: %+v", row)
	}
	if rw := kinds(drain(bo), "project_reward"); len(rw) != 0 {
		t.Fatalf("a reward for putting nothing in: %v", rw)
	}
	owner := r.player(7, "Ada", "GLI57DI72HI153U")
	if got := kinds(drain(owner), "home_tower_floor"); len(got) != 1 {
		t.Fatalf("owner on connect: %v", got)
	}
}

func TestTowerFloorOverHalfIsPhilanthropicAndNeverDropped(t *testing.T) {
	led := newFakeProjectLedger()
	led.items[9] = map[string]int64{"wall_segment": 8, "girder": 8}
	r := newHomeProjRig(t, "", led, time.Unix(1_700_000_000, 0))
	owner := r.player(7, "Ada", "TOWER_7_2")
	bo := r.player(9, "Bo", "TOWER_7_2")
	r.do(owner, map[string]any{"t": "project_home", "token": towerToken(7, 7, towerJob, 3, nil, r.now)})
	drain(owner)
	// A started floor is never given up (the owner's drop is ignored, as the source had no way to).
	r.do(owner, map[string]any{"t": "project_home_drop", "job": towerJob})
	if r.m.st.Homes[towerJob] == nil {
		t.Fatal("a Tower floor was dropped")
	}
	// Bo: 8 wall segments (20,000) and 8 girders (12,000) of 49,099 base cost: over half.
	r.do(bo, map[string]any{"t": "project_give", "job": towerJob, "req": "r1706", "n": 8})
	r.settle()
	r.now = r.now.Add(time.Second)
	r.do(bo, map[string]any{"t": "project_give", "job": towerJob, "req": "r1708", "n": 8})
	r.settle()
	row := r.m.st.Jobs[towerJob]
	for id, n := range floor3All() {
		if row.Got[id] < n {
			row.Got[id] = n
		}
	}
	row.Got["r1709"] = 24
	r.now = r.now.Add(1500 * time.Millisecond)
	r.do(owner, map[string]any{"t": "project_work", "job": towerJob, "req": "r1709"})
	r.settle()
	var boReward, adaReward map[string]any
	for _, m := range kinds(drain(bo), "project_reward") {
		boReward = m
	}
	for _, m := range kinds(drain(owner), "project_reward") {
		adaReward = m
	}
	if boReward == nil || adaReward == nil || boReward["philanthropic"] != true || adaReward["philanthropic"] != false {
		t.Fatalf("philanthropic: bo %v ada %v", boReward, adaReward)
	}
}

func TestTowerFloorsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	r := newHomeProjRig(t, path, newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	bo := r.player(9, "Bo", "TOWER_7_2")
	r.do(bo, map[string]any{"t": "project_home", "token": towerToken(9, 7, towerJob, 3, map[string]int64{"r1706": 3}, r.now)})
	r2 := newHomeProjRig(t, path, newFakeProjectLedger(), r.now)
	if j := r2.m.st.Homes[towerJob]; j == nil || j.Kind != "tower" || r2.m.st.Jobs[towerJob].Got["r1706"] != 3 {
		t.Fatalf("after restart: %+v", j)
	}
	// A tampered definition (another floor's street) is dropped on load.
	r2.m.st.Homes[towerJob].Street = "TOWER_7_0"
	r2.m.save()
	r3 := newHomeProjRig(t, path, newFakeProjectLedger(), r.now)
	if r3.m.st.Homes[towerJob] != nil {
		t.Fatal("a bad definition survived the load")
	}
}
