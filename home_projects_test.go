package main

import (
	"path/filepath"
	"testing"
	"time"
)

// newHomeProjRig is newProjRig with the site's signing secret set (home_projects.go reads it at construction).
func newHomeProjRig(t *testing.T, path string, led *fakeProjectLedger, now time.Time) *projRig {
	saved := homesSecretForTest
	homesSecretForTest = homesTestSecret
	defer func() { homesSecretForTest = saved }()
	return newProjRig(t, path, led, now)
}

func restoreToken(sub, owner int64, job, proto, yard string, mult float64, now time.Time) string {
	return signBuddy(homesTestSecret, map[string]any{"k": "home_restore", "sub": sub, "o": owner, "job": job, "proto": proto, "yard": yard, "mult": mult, "iat": now.Unix(), "exp": now.Add(30 * time.Minute).Unix()})
}

// Owner 7 (base 36 "7"), item 12 ("c"), cycle 1: the Metal Rock's restoration on the home street.
const homeJob = "h7-c-1"

func TestRestorationIsSharedOnTheHomeStreet(t *testing.T) {
	led := newFakeProjectLedger()
	led.items[9] = map[string]int64{"plain_crystal": 2, "krazy_salts": 3}
	r := newHomeProjRig(t, "", led, time.Unix(1_700_000_000, 0))
	owner := r.player(7, "Ada", "HOME_7")
	bo := r.player(9, "Bo", "HOME_7")
	away := r.player(11, "Cy", "GLI57DI72HI153U")
	drain(owner)
	drain(bo)
	drain(away)

	// A token signed for someone else, an expired one and one naming another owner's job are refused.
	r.do(bo, map[string]any{"t": "project_home", "token": restoreToken(11, 7, homeJob, "proto_metal_rock", "street", 1.1, r.now)})
	r.do(bo, map[string]any{"t": "project_home", "token": restoreToken(9, 7, homeJob, "proto_metal_rock", "street", 1.1, r.now.Add(-time.Hour))})
	r.do(bo, map[string]any{"t": "project_home", "token": restoreToken(9, 7, "h8-c-1", "proto_metal_rock", "street", 1.1, r.now)})
	if got := len(kinds(drain(bo), "error")); got != 3 {
		t.Fatalf("bad tokens: %d errors, want 3", got)
	}
	if len(r.m.st.Homes) != 0 {
		t.Fatalf("a bad token made a job: %v", r.m.st.Homes)
	}

	r.do(bo, map[string]any{"t": "project_home", "token": restoreToken(9, 7, homeJob, "proto_metal_rock", "street", 1.1, r.now)})
	if row := lastRow(t, drain(bo), homeJob); row["phase"].(float64) != 0 {
		t.Fatalf("new restoration row: %v", row)
	}
	j := r.m.st.Homes[homeJob]
	if j == nil || j.Kind != "restore" || j.Owner != 7 || j.Street != "HOME_7" || j.Mult != 1.1 || j.Divisor != 1 {
		t.Fatalf("job: %+v", j)
	}

	// Someone not on the home street can't put anything in.
	r.do(away, map[string]any{"t": "project_give", "job": homeJob, "req": "r1682", "n": 1})
	if e := kinds(drain(away), "error"); len(e) != 1 || e[0]["message"] != projectTextNotHere {
		t.Fatalf("away: %v", e)
	}

	// The home street's sync lists it; the world's built list never does.
	r.do(owner, map[string]any{"t": "project_sync"})
	msgs := drain(owner)
	if st := kinds(msgs, "project_street"); len(st) != 1 || st[0]["jobs"].(map[string]any)[homeJob] == nil {
		t.Fatalf("sync on HOME_7: %v", st)
	}

	// Bo puts in the crystals and the salts (933 of 1193 base cost), Ada works the 20 units (260).
	r.do(bo, map[string]any{"t": "project_give", "job": homeJob, "req": "r1682", "n": 2})
	r.settle()
	r.now = r.now.Add(time.Second)
	r.do(bo, map[string]any{"t": "project_give", "job": homeJob, "req": "r1684", "n": 3})
	r.settle()
	for i := 0; i < 20; i++ {
		r.now = r.now.Add(1500 * time.Millisecond)
		r.do(owner, map[string]any{"t": "project_work", "job": homeJob, "req": "r1683"})
	}
	r.settle()
	row := r.m.st.Jobs[homeJob]
	if row == nil || row.Done == 0 {
		t.Fatalf("not finished: %+v", row)
	}
	var boReward, adaReward map[string]any
	for _, m := range kinds(drain(bo), "project_reward") {
		boReward = m
	}
	ada := drain(owner)
	for _, m := range kinds(ada, "project_reward") {
		adaReward = m
	}
	if boReward == nil || adaReward == nil {
		t.Fatalf("rewards: bo %v ada %v", boReward, adaReward)
	}
	// inc_jobs.js:437-438 and 459-463: 600 iMG x 933/1193 x 1.1, and over half of someone else's project.
	if xp := boReward["rewards"].(map[string]any)["xp"].(float64); xp != 516 {
		t.Fatalf("Bo's iMG %v, want 516", xp)
	}
	if boReward["philanthropic"] != true || adaReward["philanthropic"] != false || boReward["owner"].(float64) != 7 {
		t.Fatalf("philanthropic: bo %v ada %v", boReward["philanthropic"], adaReward["philanthropic"])
	}
	if xp := adaReward["rewards"].(map[string]any)["xp"].(float64); xp != 144 {
		t.Fatalf("Ada's iMG %v, want 144", xp)
	}
	restored := kinds(ada, "home_restored")
	if len(restored) != 1 || restored[0]["job"] != homeJob || restored[0]["proto"] != "proto_metal_rock" {
		t.Fatalf("home_restored: %v", restored)
	}
	// Nobody else hears of it as a built street.
	if b := kinds(drain(away), "project_built"); len(b) != 0 {
		t.Fatalf("project_built went out: %v", b)
	}
	// A finished restoration takes nothing more.
	r.now = r.now.Add(2 * time.Second)
	r.do(bo, map[string]any{"t": "project_give", "job": homeJob, "req": "r1682", "n": 1})
	if e := kinds(drain(bo), "error"); len(e) != 1 || e[0]["message"] != projectTextDone {
		t.Fatalf("after done: %v", e)
	}
	// Two hours on, it is forgotten.
	r.now = r.now.Add(3 * time.Hour)
	r.do(bo, map[string]any{"t": "project_home", "token": restoreToken(9, 7, "h7-d-1", "proto_peat", "street", 1, r.now)})
	if r.m.st.Homes[homeJob] != nil || r.m.st.Jobs[homeJob] != nil || r.m.st.Homes["h7-d-1"] == nil {
		t.Fatalf("forgetting: homes %v", r.m.st.Homes)
	}
}

func TestOwnerAloneStillRestoresAndIsNotPhilanthropic(t *testing.T) {
	led := newFakeProjectLedger()
	led.items[7] = map[string]int64{"plain_crystal": 2, "krazy_salts": 3}
	r := newHomeProjRig(t, "", led, time.Unix(1_700_000_000, 0))
	owner := r.player(7, "Ada", "HOME_7")
	r.do(owner, map[string]any{"t": "project_home", "token": restoreToken(7, 7, homeJob, "proto_metal_rock", "street", 1, r.now)})
	r.do(owner, map[string]any{"t": "project_give", "job": homeJob, "req": "r1682", "n": 2})
	r.settle()
	r.now = r.now.Add(time.Second)
	r.do(owner, map[string]any{"t": "project_give", "job": homeJob, "req": "r1684", "n": 3})
	r.settle()
	for i := 0; i < 20; i++ {
		r.now = r.now.Add(1500 * time.Millisecond)
		r.do(owner, map[string]any{"t": "project_work", "job": homeJob, "req": "r1683"})
	}
	r.settle()
	msgs := drain(owner)
	rw := kinds(msgs, "project_reward")
	if len(rw) != 1 || rw[0]["philanthropic"] != false || rw[0]["rewards"].(map[string]any)["xp"].(float64) != 600 {
		t.Fatalf("owner's reward: %v", rw)
	}
	if len(kinds(msgs, "home_restored")) != 1 {
		t.Fatal("no home_restored")
	}
	// Acked, it is not sent again on the next sync.
	r.do(owner, map[string]any{"t": "project_ack", "id": "job-h-" + homeJob})
	r.do(owner, map[string]any{"t": "project_sync"})
	if len(kinds(drain(owner), "home_restored")) != 0 {
		t.Fatal("home_restored sent after its ack")
	}
}

func TestOwnerOfflineIsToldOnConnect(t *testing.T) {
	led := newFakeProjectLedger()
	led.items[9] = map[string]int64{"plain_crystal": 2, "krazy_salts": 3}
	r := newHomeProjRig(t, "", led, time.Unix(1_700_000_000, 0))
	bo := r.player(9, "Bo", "HOME_7")
	r.do(bo, map[string]any{"t": "project_home", "token": restoreToken(9, 7, homeJob, "proto_metal_rock", "street", 1, r.now)})
	r.do(bo, map[string]any{"t": "project_give", "job": homeJob, "req": "r1682", "n": 2})
	r.settle()
	r.now = r.now.Add(time.Second)
	r.do(bo, map[string]any{"t": "project_give", "job": homeJob, "req": "r1684", "n": 3})
	r.settle()
	for i := 0; i < 20; i++ {
		r.now = r.now.Add(1500 * time.Millisecond)
		r.do(bo, map[string]any{"t": "project_work", "job": homeJob, "req": "r1683"})
	}
	r.settle()
	rw := kinds(drain(bo), "project_reward")
	if len(rw) != 1 || rw[0]["philanthropic"] != true || rw[0]["participation"].(float64) != 100 {
		t.Fatalf("Bo alone: %v", rw)
	}
	owner := r.player(7, "Ada", "GLI57DI72HI153U")
	if got := kinds(drain(owner), "home_restored"); len(got) != 1 {
		t.Fatalf("owner on connect: %v", got)
	}
}

func TestBackYardRestorationIsInTheHouse(t *testing.T) {
	r := newHomeProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	out := r.player(9, "Bo", "HOME_7")
	in := r.player(10, "Di", "HOUSE_7")
	drain(out)
	drain(in)
	// The yard must match the id's first letter (y: the back yard).
	r.do(in, map[string]any{"t": "project_home", "token": restoreToken(10, 7, "h7-c-1", "proto_peat", "back", 1, r.now)})
	if len(r.m.st.Homes) != 0 {
		t.Fatal("a back yard token with a home street id made a job")
	}
	drain(in)
	r.do(in, map[string]any{"t": "project_home", "token": restoreToken(10, 7, "y7-c-1", "proto_peat", "back", 1, r.now)})
	if j := r.m.st.Homes["y7-c-1"]; j == nil || j.Street != "HOUSE_7" {
		t.Fatalf("back yard job: %+v", j)
	}
	r.do(out, map[string]any{"t": "project_work", "job": "y7-c-1", "req": "r1695"})
	if e := kinds(drain(out), "error"); len(e) != 1 || e[0]["message"] != projectTextNotHere {
		t.Fatalf("from the street: %v", e)
	}
	r.do(in, map[string]any{"t": "project_sync"})
	if st := kinds(drain(in), "project_street"); len(st) != 1 || st[0]["jobs"].(map[string]any)["y7-c-1"] == nil {
		t.Fatalf("house sync: %v", st)
	}
}

func TestOnlyTheOwnerGivesARestorationUp(t *testing.T) {
	r := newHomeProjRig(t, "", newFakeProjectLedger(), time.Unix(1_700_000_000, 0))
	owner := r.player(7, "Ada", "HOME_7")
	bo := r.player(9, "Bo", "HOME_7")
	r.do(bo, map[string]any{"t": "project_home", "token": restoreToken(9, 7, homeJob, "proto_metal_rock", "street", 1, r.now)})
	r.now = r.now.Add(1500 * time.Millisecond)
	r.do(bo, map[string]any{"t": "project_work", "job": homeJob, "req": "r1683"})
	drain(owner)
	drain(bo)
	r.do(bo, map[string]any{"t": "project_home_drop", "job": homeJob})
	if e := kinds(drain(bo), "error"); len(e) != 1 || e[0]["message"] != projectTextNotYours || r.m.st.Homes[homeJob] == nil {
		t.Fatalf("visitor drop: %v", e)
	}
	r.do(owner, map[string]any{"t": "project_home_drop", "job": homeJob})
	if r.m.st.Homes[homeJob] != nil || r.m.st.Jobs[homeJob] != nil {
		t.Fatal("not dropped")
	}
	gone := kinds(drain(bo), "project_state")
	if len(gone) != 1 || gone[0]["gone"] != true {
		t.Fatalf("visitor not told: %v", gone)
	}
}

func TestRestorationsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	led := newFakeProjectLedger()
	led.items[9] = map[string]int64{"plain_crystal": 2}
	r := newHomeProjRig(t, path, led, time.Unix(1_700_000_000, 0))
	bo := r.player(9, "Bo", "HOME_7")
	r.do(bo, map[string]any{"t": "project_home", "token": restoreToken(9, 7, homeJob, "proto_metal_rock", "street", 1.25, r.now)})
	r.do(bo, map[string]any{"t": "project_give", "job": homeJob, "req": "r1682", "n": 2})
	r.settle()
	r2 := newHomeProjRig(t, path, led, r.now)
	if j := r2.m.st.Homes[homeJob]; j == nil || j.Mult != 1.25 {
		t.Fatalf("after restart: %+v", j)
	}
	if got := r2.m.st.Jobs[homeJob].Got["r1682"]; got != 2 {
		t.Fatalf("gives after restart: %d", got)
	}
	// A tampered definition in the file is dropped on load.
	r2.m.st.Homes[homeJob].Street = "GLI57DI72HI153U"
	r2.m.save()
	r3 := newHomeProjRig(t, path, led, r.now)
	if r3.m.st.Homes[homeJob] != nil || r3.m.st.Jobs[homeJob] != nil {
		t.Fatal("a bad definition survived the load")
	}
}
