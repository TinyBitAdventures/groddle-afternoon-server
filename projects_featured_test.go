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
	if id == "" || job.Exempt || job.Built || !featuredFrontier(job, m.closedStreets()) {
		t.Fatalf("a new world features %q (%+v)", id, job)
	}
	if text := m.featuredText(id); strings.Contains(text, "a street") || strings.ContainsAny(text, "—–") {
		t.Fatalf("featured text %q", text)
	}
}
