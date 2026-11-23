package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A small world for the featured project's rule: GX is closed (job 3 builds it), GY and GZ are open.
const featuredTableJSON = `{"divisor":50,"workUnitMs":1500,"cooldownMs":1800000,
"jobs":{
 "1":{"kind":"street","street":"GA","offer":["GX"],"connecting":["GX"],"phases":[{"c":"c1","delay":0}],"divisor":50,"label":"Ay Way","hub":"Testwood"},
 "2":{"kind":"street","street":"GB","offer":["GY"],"connecting":["GY"],"phases":[{"c":"c1","delay":0}],"divisor":50,"label":"Bee Road","hub":"Testwood"},
 "3":{"kind":"street","street":"GX","offer":["GZ"],"connecting":["GZ"],"phases":[{"c":"c1","delay":0},{"c":"c1","delay":60}],"divisor":50,"label":"Ex Lane","hub":"Testwood"},
 "4":{"kind":"street","street":"GC","offer":["GY"],"connecting":["GY"],"phases":[{"c":"c1","delay":0}],"divisor":50,"label":"Sea Path","exempt":true},
 "5":{"kind":"street","street":"GD","offer":["GY"],"connecting":["GY"],"phases":[{"c":"c1","delay":0}],"divisor":50,"label":"Dee Row","built":true},
 "6":{"kind":"upgrade","street":"GA","offer":["GA"],"phases":[{"c":"c1","delay":0}],"options":["9"],"divisor":50,"label":"Ay Way"},
 "7":{"kind":"upgrade","street":"GY","offer":["GY"],"phases":[{"c":"c1","delay":0}],"options":["9"],"divisor":50,"label":"Yonder","hub":"Testwood"},
 "8":{"kind":"hall","street":"GH","offer":["GY"],"connecting":["GY"],"phases":[{"c":"c1","delay":0}],"divisor":50}
},
"classes":{"c1/50":{"reqs":[{"id":"r1","b":"1","g":1,"type":"item","c":"plank","need":10,"cost":2}],"rewards":{"xp":100},"perf":{},"pct":0,"cut":1}},
"drops":{},
"names":{"GX":"Ex Lane","GY":"Yonder","GZ":"Zed"}}`

type fpRig struct {
	t      *testing.T
	hub    *Hub
	m      *projects
	ch     *channels
	now    time.Time
	timers []*rigTimer
}

func newFpRig(t *testing.T, path string) *fpRig {
	r := &fpRig{t: t, now: time.Unix(1_700_000_000, 0)}
	saved := modules
	modules = []moduleFactory{func(h *Hub) Module {
		var table projTable
		if err := json.Unmarshal([]byte(featuredTableJSON), &table); err != nil {
			t.Fatal(err)
		}
		r.m = newProjectsWith(h, table, path)
		r.m.trusted = true // no ledger: gives count as sent
		r.m.spawn = func(fn func()) { fn() }
		r.m.rng = func() float64 { return 0.01 }
		return r.m
	}, newChannels}
	r.hub = NewHub(func() time.Time { return r.now })
	modules = saved
	r.m.feat.dev = true
	r.ch, _ = r.hub.provided["channels.self"].(*channels)
	r.hub.afterFunc = func(d time.Duration, fn func()) func() bool {
		tm := &rigTimer{d: d, fn: fn}
		r.timers = append(r.timers, tm)
		return func() bool { was := !tm.stopped; tm.stopped = true; return was }
	}
	return r
}

func (r *fpRig) do(cl *Client, msg map[string]any) {
	b, _ := json.Marshal(msg)
	r.hub.Handle(cl, b)
}

// player connects, joins a street, opens Global (by hand: the watch token is channels' own test) and syncs.
func (r *fpRig) player(id int64, name, street string) *Client {
	cl := r.hub.Connect(Claims{Sub: id, Name: name})
	r.do(cl, map[string]any{"t": "join", "street": street})
	room := r.ch.rooms["global"]
	if room == nil {
		room = &chanRoom{members: map[int64]bool{}}
		r.ch.rooms["global"] = room
	}
	room.members[id] = true
	r.ch.open[id] = map[string]string{"global": "global"}
	r.do(cl, map[string]any{"t": "project_sync"})
	return cl
}

// fire runs every timer waiting (the featured project's progress push).
func (r *fpRig) fire() {
	for _, tm := range append([]*rigTimer{}, r.timers...) {
		if !tm.stopped {
			tm.stopped = true
			tm.fn()
		}
	}
}

func (r *fpRig) featured() string {
	if r.m.st.Featured == nil {
		return ""
	}
	return r.m.st.Featured.Job
}

func (r *fpRig) globalNotes() []string {
	var out []string
	if room := r.ch.rooms["global"]; room != nil {
		for _, l := range room.history {
			if l.Note {
				out = append(out, l.Text)
			}
		}
	}
	return out
}

func TestFeaturedPicksTheFrontierThenProgressThenTheLowestID(t *testing.T) {
	r := newFpRig(t, "")
	// 1 sits behind closed GX, 4 is exempt, 5 built, 6 an upgrade of a closed street, 8 a hall: 2, 3 and 7 are open on the frontier.
	if got := r.featured(); got != "2" {
		t.Fatalf("featured %q, want 2 (the frontier's lowest id with no progress anywhere)", got)
	}
	// Progress elsewhere picks that one when the rule runs again.
	r.m.row("7").Got["r1"] = 4
	r.m.row("7").Started = 1
	if got := r.m.pickFeatured(); got != "7" {
		t.Fatalf("pick %q, want 7 (40%% of an upgrade on an open street)", got)
	}
	// Behind the frontier comes last, whatever its progress.
	r.m.row("1").Got["r1"] = 9
	if got := r.m.pickFeatured(); got != "7" {
		t.Fatalf("pick %q: a street with no open neighbour must wait", got)
	}
	// A resting board (a street just built from it) gives way to one that is not.
	r.m.row("7").Got["r1"] = 0
	r.m.st.Rest["GY"] = r.m.ms()
	if got := r.m.pickFeatured(); got != "3" {
		t.Fatalf("pick %q, want 3 (2 waits on Yonder's rest)", got)
	}
}

func TestFeaturedStaysUntilBuiltThenTheNextIsAnnounced(t *testing.T) {
	r := newFpRig(t, "")
	ada := r.player(7, "Ada", "GZ")
	sync := kinds(drain(ada), "project_featured")
	if len(sync) != 1 || sync[0]["job"] != "2" || sync[0]["label"] != "Bee Road" || sync[0]["boardLabel"] != "Yonder" || sync[0]["percent"].(float64) != 0 {
		t.Fatalf("the sync's featured project: %v", sync)
	}
	// The pick at start reaches Global once someone connects.
	if notes := r.globalNotes(); len(notes) != 1 || notes[0] != "The featured street project is now Bee Road, the road from Yonder in Testwood. Help build it!" {
		t.Fatalf("Global heard %q", notes)
	}
	// Progress on another project leaves the featured one alone.
	r.do(ada, map[string]any{"t": "project_give", "job": "3", "req": "r1", "n": 3})
	if r.featured() != "2" {
		t.Fatalf("featured moved to %s before 2 was built", r.featured())
	}
	// The featured project's progress reaches everyone, throttled.
	bo := r.player(9, "Bo", "GY")
	drain(ada)
	drain(bo)
	r.now = r.now.Add(time.Second)
	r.do(bo, map[string]any{"t": "project_give", "job": "2", "req": "r1", "n": 5})
	r.do(bo, map[string]any{"t": "project_give", "job": "2", "req": "r1", "n": 1}) // too soon: refused, no second push
	if n := len(kinds(drain(ada), "project_featured")); n != 0 {
		t.Fatalf("%d pushes before the timer", n)
	}
	r.fire()
	got := kinds(drain(ada), "project_featured")
	if len(got) != 1 || got[0]["perc"].(float64) != 0.5 || got[0]["percent"].(float64) != 50 {
		t.Fatalf("progress push: %v", got)
	}
	// Finishing it thanks everyone and features the next (3: 30%, the most progress on the frontier).
	r.do(bo, map[string]any{"t": "project_featured_dev", "finish": true})
	if r.featured() != "3" {
		t.Fatalf("next featured %q, want 3", r.featured())
	}
	notes := r.globalNotes()
	want := []string{"Bee Road is built! Thanks to everyone who helped.", "The featured street project is now Ex Lane, the road from Zed in Testwood. Help build it!"}
	if len(notes) != 3 || notes[1] != want[0] || notes[2] != want[1] {
		t.Fatalf("Global heard %q", notes)
	}
	msgs := drain(ada)
	var said []string
	for _, n := range kinds(msgs, "channel_note") {
		said = append(said, n["text"].(string))
	}
	if strings.Join(said, "|") != strings.Join(want, "|") {
		t.Fatalf("Ada in Global was told %q", said)
	}
	pushed := kinds(msgs, "project_featured")
	if len(pushed) == 0 || pushed[len(pushed)-1]["job"] != "3" || pushed[len(pushed)-1]["phases"].(float64) != 2 {
		t.Fatalf("the change reached Ada as %v", pushed)
	}
	// A phase of many is not the end: 3 stays through its delay and is built after its second phase.
	r.do(ada, map[string]any{"t": "project_featured_dev", "finish": true})
	if r.featured() != "3" || r.m.st.Jobs["3"].Phase != 1 {
		t.Fatalf("after phase 1: featured %s, phase %d", r.featured(), r.m.st.Jobs["3"].Phase)
	}
	r.do(ada, map[string]any{"t": "project_featured_dev", "finish": true})
	// GX is open now, so 1 is on the frontier; 7 and 1 have no progress: the lower id.
	if r.featured() != "1" {
		t.Fatalf("after 3 was built: featured %q, want 1", r.featured())
	}
	if n := r.globalNotes(); n[len(n)-2] != "Ex Lane is built! Thanks to everyone who helped." {
		t.Fatalf("Global heard %q", n)
	}
	// History lines are notes with no speaker.
	for _, l := range r.ch.history("global", 7) {
		if !l.Note || l.ID != 0 || l.Name != "" || l.Lid != 0 {
			t.Fatalf("a notice in the history: %+v", l)
		}
	}
}

// G1023: a street in its post-build rest shows no board (inc_jobs.js 463-467), so a resting project
// comes after every open one whatever its progress, and a featured one that rests says when it opens.
func TestFeaturedRestingComesAfterOpenAndSendsTheRestEnd(t *testing.T) {
	r := newFpRig(t, "")
	cool := r.m.t.CooldownMs
	// 2 (on Yonder) is nearly built, but Yonder rests: 3 on Zed is open, so 3 is picked.
	r.m.row("2").Got["r1"] = 9
	r.m.row("2").Started = 1
	r.m.st.Rest["GY"] = r.m.ms() - 10*60*1000
	if got := r.m.pickFeatured(); got != "3" {
		t.Fatalf("pick %q, want 3 (2 and 7 wait on Yonder's rest)", got)
	}
	// Every open one resting: the most progress, as before.
	r.m.st.Rest["GZ"] = r.m.ms() - 5*60*1000
	if got := r.m.pickFeatured(); got != "2" {
		t.Fatalf("pick %q, want 2 (all resting: the most progress)", got)
	}
	// Featured while resting: ready is Yonder's rest end, marked as the rest.
	r.m.setFeatured("2")
	msg := r.m.featuredMsg()
	if msg["job"] != "2" || msg["ready"] != r.m.st.Rest["GY"]+cool || msg["rest"] != true {
		t.Fatalf("resting featured: ready %v rest %v, want %d true", msg["ready"], msg["rest"], r.m.st.Rest["GY"]+cool)
	}
	// After the rest no ready is sent.
	r.now = r.now.Add(21 * time.Minute)
	if msg := r.m.featuredMsg(); msg["ready"] != nil || msg["rest"] != nil {
		t.Fatalf("after the rest: ready %v rest %v", msg["ready"], msg["rest"])
	}
	// A between-phase wait keeps its own ready, with no rest mark.
	r.m.setFeatured("3")
	r.m.st.Rest = map[string]int64{}
	r.m.row("3").Ready = r.m.ms() + 60_000
	if msg := r.m.featuredMsg(); msg["ready"] != r.m.ms()+60_000 || msg["rest"] != nil {
		t.Fatalf("phase wait: ready %v rest %v", msg["ready"], msg["rest"])
	}
	// Another project built beside the featured one's board sends the featured project again, now resting.
	r.m.row("3").Ready = 0
	r.m.setFeatured("2")
	ada := r.player(7, "Ada", "GY")
	drain(ada)
	r.m.row("7").Got["r1"] = 10
	r.m.settleIfComplete("7")
	got := kinds(drain(ada), "project_featured")
	if len(got) == 0 || got[len(got)-1]["job"] != "2" || got[len(got)-1]["rest"] != true || got[len(got)-1]["ready"].(float64) != float64(r.m.ms()+cool) {
		t.Fatalf("after Yonder's upgrade was built: %v", got)
	}
}

// G1140 (Austin 2026-11-22): the featured line skips a project waiting between phases (Glitch's
// jobs.js getAvailableJobs 2-62 offers no waiting job) and comes back to it when its next phase opens.
func TestFeaturedStepsAsideWhileItWaitsBetweenPhases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	r := newFpRig(t, path)
	since := r.m.ms() - 1000
	r.m.st.Featured = &projFeatured{Job: "3", Since: since, By: "auto"}
	ada := r.player(7, "Ada", "GZ")
	drain(ada)
	heard := len(r.globalNotes())
	// Phase 1 of Ex Lane done: phase 2 waits 60 s, so Bee Road (open, the frontier's lowest id) stands in.
	r.do(ada, map[string]any{"t": "project_featured_dev", "finish": true})
	f := r.m.st.Featured
	if f == nil || f.Job != "2" || f.Back == nil || f.Back.Job != "3" || f.Back.Since != since {
		t.Fatalf("while 3 waits: %+v", f)
	}
	// Global hears nothing of the swap (Austin 2026-11-22): the game shows it from project_featured.
	if notes := r.globalNotes(); len(notes) != heard {
		t.Fatalf("Global heard the stand-in: %q", notes[heard:])
	}
	got := kinds(drain(ada), "project_featured")
	if len(got) == 0 {
		t.Fatal("no project_featured for the stand-in")
	}
	last := got[len(got)-1]
	back, _ := last["back"].(map[string]any)
	if last["job"] != "2" || back == nil || back["job"] != "3" || back["label"] != "Ex Lane" || back["ready"].(float64) != float64(r.m.ms()+60_000) {
		t.Fatalf("the stand-in's message: %v", last)
	}
	// The rule itself ranks the waiting one after every open one.
	r.m.row("3").Got["r1"] = 9
	if pick := r.m.pickFeatured(); pick == "3" {
		t.Fatal("the pick took a project waiting between phases")
	}
	r.m.row("3").Got["r1"] = 0
	// Kept across a restart, Back and all.
	r2 := newFpRig(t, path)
	if f := r2.m.st.Featured; f == nil || f.Job != "2" || f.Back == nil || f.Back.Job != "3" || len(r2.m.feat.notice) != 0 {
		t.Fatalf("after a restart: %+v", f)
	}
	// The wait ends: Ex Lane is featured again, as it was (Since kept), and Global hears nothing.
	r.now = r.now.Add(61 * time.Second)
	r.fire()
	if f := r.m.st.Featured; f == nil || f.Job != "3" || f.Back != nil || f.Since != since {
		t.Fatalf("after the wait: %+v", f)
	}
	if notes := r.globalNotes(); len(notes) != heard {
		t.Fatalf("Global heard the return: %q", notes[heard:])
	}
	got = kinds(drain(ada), "project_featured")
	if len(got) == 0 || got[len(got)-1]["job"] != "3" || got[len(got)-1]["back"] != nil || got[len(got)-1]["ready"] != nil {
		t.Fatalf("the return reached Ada as %v", got)
	}
	// The restarted server had no timer from before: a sync after the wait brings Ex Lane back too.
	r2.now = r2.now.Add(61 * time.Second)
	r2.player(9, "Bo", "GY")
	if r2.featured() != "3" || r2.m.st.Featured.Back != nil {
		t.Fatalf("after the restart's wait: %+v", r2.m.st.Featured)
	}
}

// G1140: a stand-in built while the featured one waits hands back to it when it is open, and to
// another stand-in while it still waits; with nothing open, the waiting one stays featured.
func TestFeaturedStandInBuiltOrNothingOpen(t *testing.T) {
	r := newFpRig(t, "")
	r.m.st.Featured = &projFeatured{Job: "3", Since: r.m.ms(), By: "auto"}
	ada := r.player(7, "Ada", "GZ")
	r.do(ada, map[string]any{"t": "project_featured_dev", "finish": true})
	if f := r.m.st.Featured; f.Job != "2" || f.Back == nil {
		t.Fatalf("stand-in: %+v", f)
	}
	// Bee Road is built from Yonder, so Yonder rests and Yonder's upgrade (7) is not open: no other
	// stand-in, so Ex Lane is featured again, still waiting, and says when it opens. Global hears the
	// build, as before G1140, and nothing of the return.
	heard := len(r.globalNotes())
	r.do(ada, map[string]any{"t": "project_featured_dev", "finish": true})
	f := r.m.st.Featured
	if f == nil || f.Job != "3" || f.Back != nil {
		t.Fatalf("after the stand-in was built: %+v", f)
	}
	if notes := r.globalNotes(); len(notes) != heard+1 || notes[heard] != r.m.builtText("2") {
		t.Fatalf("Global heard %q after the stand-in was built", notes[heard:])
	}
	if msg := r.m.featuredMsg(); msg["ready"] != r.m.st.Jobs["3"].Ready || msg["rest"] != nil {
		t.Fatalf("waiting featured: %v", msg)
	}
	// An administrator's choice is not moved while it waits.
	r2 := newFpRig(t, "")
	r2.m.setFeatured("3")
	bo := r2.player(9, "Bo", "GZ")
	r2.do(bo, map[string]any{"t": "project_featured_dev", "finish": true})
	if f := r2.m.st.Featured; f.Job != "3" || f.By != "admin" || f.Back != nil {
		t.Fatalf("an administrator's choice moved: %+v", f)
	}
}

func TestFeaturedIsKeptAcrossARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	r := newFpRig(t, path)
	r.m.setFeatured("7")
	r2 := newFpRig(t, path)
	if r2.featured() != "7" || r2.m.st.Featured.By != "admin" || len(r2.m.feat.notice) != 0 {
		t.Fatalf("after a restart: %+v, notices %q", r2.m.st.Featured, r2.m.feat.notice)
	}
}

func TestFeaturedConsoleCall(t *testing.T) {
	r := newFpRig(t, "")
	code, out := callService(t, adminProjectFeatured, r.hub, map[string]any{})
	if code != 200 || out["job"] != "2" || out["by"] != "auto" {
		t.Fatalf("read: %d %v", code, out)
	}
	for _, bad := range []string{"4", "5", "8", "999"} {
		if code, _ := callService(t, adminProjectFeatured, r.hub, map[string]any{"job": bad}); code != 400 {
			t.Fatalf("job %s could be featured (%d)", bad, code)
		}
	}
	if code, out := callService(t, adminProjectFeatured, r.hub, map[string]any{"job": "7"}); code != 200 || out["status"] != "set" || r.featured() != "7" {
		t.Fatalf("set: %d %v", code, out)
	}
	if n := r.m.feat.notice; len(n) != 0 {
		t.Fatalf("notices left waiting: %q", n) // the call posts them at once
	}
	if code, out := callService(t, adminProjectFeatured, r.hub, map[string]any{"job": ""}); code != 200 || out["status"] != "cleared" || out["job"] != "2" {
		t.Fatalf("clear: %d %v", code, out)
	}
}

func TestFeaturedDevNeedsTheFlag(t *testing.T) {
	r := newFpRig(t, "")
	r.m.feat.dev = false
	ada := r.player(7, "Ada", "GY")
	drain(ada)
	r.do(ada, map[string]any{"t": "project_featured_dev", "finish": true})
	if r.m.st.Jobs["2"] != nil && r.m.st.Jobs["2"].Got["r1"] > 0 {
		t.Fatal("project_featured_dev worked without GRODDLE_PROJECTS_DEV")
	}
}

func TestFeaturedOnTheRealTable(t *testing.T) {
	var table projTable
	if err := json.Unmarshal(projectsJSON, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Names) == 0 {
		t.Fatal("data/projects.json has no street names (the game's tools/build-realtime-projects.mjs)")
	}
	h := NewHub(func() time.Time { return time.Unix(1_700_000_000, 0) })
	m := newProjectsWith(h, table, "")
	id := m.pickFeatured()
	job := m.t.Jobs[id]
	_, reach := m.openWorldNow()
	if id == "" || job.Exempt || job.Built || !featuredFrontier(job, m.closedStreets()) || reach == nil || !projectReachable(job, reach) {
		t.Fatalf("a new world features %q (%+v)", id, job)
	}
	if text := m.featuredText(id); strings.Contains(text, "a street") || strings.ContainsAny(text, "—–") {
		t.Fatalf("featured text %q", text)
	}
}
