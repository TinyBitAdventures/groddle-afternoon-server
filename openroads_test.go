package main

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"
)

// testdata/open-roads.json: the open-road rule's cases, shared with the game's tests/open-roads.test.js
// (js/open-roads.js answers each the same).
type openRoadsCase struct {
	Name      string              `json:"name"`
	Start     string              `json:"start"`
	Roads     map[string][]string `json:"roads"`
	Free      map[string][]string `json:"free"`
	Jobs      map[string]projJob  `json:"jobs"`
	Done      []string            `json:"done"`
	Kept      []string            `json:"kept"`
	Closed    []string            `json:"closed"`
	Reach     []string            `json:"reach"`
	Reachable []string            `json:"reachable"`
	Featured  string              `json:"featured"`
}

func loadOpenRoadsCases(t *testing.T) []openRoadsCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/open-roads.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []openRoadsCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) < 3 {
		t.Fatalf("%d cases", len(file.Cases))
	}
	return file.Cases
}

// openRoadsWorld is a server with the case's table and its done projects; withRoads false leaves the roads out.
func openRoadsWorld(t *testing.T, c openRoadsCase, withRoads bool) *projects {
	t.Helper()
	table := projTable{Divisor: 50, Jobs: map[string]projJob{}, Classes: map[string]projClass{
		"c1/50": {Reqs: []projReq{{ID: "r1", B: "1", G: 1, Type: "item", C: "plank", Need: 10, Cost: 2}}, Cut: 1},
	}, Names: map[string]string{}}
	for id, job := range c.Jobs {
		job.Phases = []projPhaseDef{{C: "c1"}}
		job.Divisor = 50
		if job.Kind == "upgrade" {
			job.Options = []string{"9"}
		}
		table.Jobs[id] = job
	}
	if withRoads {
		table.Start, table.Roads, table.Free = c.Start, c.Roads, c.Free
	}
	h := NewHub(func() time.Time { return time.Unix(1_700_000_000, 0) })
	m := newProjectsWith(h, table, "")
	for _, id := range c.Done {
		m.row(id).Done = m.ms()
	}
	return m
}

func setKeys(set map[string]bool) []string {
	out := []string{}
	for k, v := range set {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func TestOpenRoadsCasesMatchTheGame(t *testing.T) {
	for _, c := range loadOpenRoadsCases(t) {
		m := openRoadsWorld(t, c, true)
		closed, reach := m.openWorldNow()
		if got := setKeys(closed); !reflect.DeepEqual(got, c.Closed) {
			t.Errorf("%s: closed %v, want %v", c.Name, got, c.Closed)
		}
		if got := setKeys(reach); !reflect.DeepEqual(got, c.Reach) {
			t.Errorf("%s: open world %v, want %v", c.Name, got, c.Reach)
		}
		reachable := []string{}
		for id, job := range m.t.Jobs {
			if m.featuredOK(id) && projectReachable(job, reach) {
				reachable = append(reachable, id)
			}
		}
		sort.Strings(reachable)
		if !reflect.DeepEqual(reachable, c.Reachable) {
			t.Errorf("%s: reachable projects %v, want %v", c.Name, reachable, c.Reachable)
		}
		if got := m.pickFeatured(); got != c.Featured {
			t.Errorf("%s: featured %q, want %q", c.Name, got, c.Featured)
		}
	}
}

// Report #566: the featured project was a street whose board stood in a built pocket no open road
// joined to the world. Without the roads (the old one-neighbour test) job 1 is picked; with them it never is.
func TestFeaturedSkipsAFrontierStreetBehindAClosedRoad(t *testing.T) {
	c := loadOpenRoadsCases(t)[0]
	if got := openRoadsWorld(t, c, false).pickFeatured(); got != "1" {
		t.Fatalf("without roads the pick is %q; the case no longer shows the old rule's mistake", got)
	}
	m := openRoadsWorld(t, c, true)
	if got := m.pickFeatured(); got != "2" {
		t.Fatalf("featured %q, want 2 (the road into the pocket)", got)
	}
	// The global line names a board players can reach.
	if board := m.featuredBoard(m.t.Jobs["2"]); board != "GA" {
		t.Fatalf("board %q, want GA", board)
	}
	// A pick kept from before the reach test is picked again; an administrator's choice stays.
	m.st.Featured = &projFeatured{Job: "1", By: "auto"}
	if !m.ensureFeatured() || m.st.Featured.Job != "2" {
		t.Fatalf("an unreachable kept pick stayed: %+v", m.st.Featured)
	}
	if !m.featuredOK("1") {
		t.Fatal("job 1 should still be an open street project")
	}
	m.st.Featured = &projFeatured{Job: "1", By: "admin"}
	if m.ensureFeatured() || m.st.Featured.Job != "1" {
		t.Fatalf("an administrator's choice was replaced: %+v", m.st.Featured)
	}
	// Once the road in is built, the pocket's project is reachable and next in line.
	m.st.Featured = nil
	m.row("2").Done = m.ms()
	if got := m.pickFeatured(); got != "1" {
		t.Fatalf("featured %q after the road in was built, want 1", got)
	}
}

// The real table carries the open world, and a new world's open world holds the starter street and more.
func TestOpenWorldOnTheRealTable(t *testing.T) {
	var table projTable
	if err := json.Unmarshal(projectsJSON, &table); err != nil {
		t.Fatal(err)
	}
	if table.Start == "" || len(table.Roads) < 1000 || len(table.Free) == 0 {
		t.Fatalf("data/projects.json lacks the open world (start %q, %d roads, %d free): run the game's tools/build-realtime-projects.mjs", table.Start, len(table.Roads), len(table.Free))
	}
	h := NewHub(func() time.Time { return time.Unix(1_700_000_000, 0) })
	m := newProjectsWith(h, table, "")
	closed, reach := m.openWorldNow()
	if !reach[gStreet(table.Start)] || len(reach) < 20 {
		t.Fatalf("open world of a new world: %d streets", len(reach))
	}
	for s := range reach {
		if closed[s] && s != gStreet(table.Start) {
			t.Fatalf("the open world holds closed street %s", s)
		}
	}
}

// R11 (2026-11-21): the live world had only 5 walkable projects left for the featured pick. When
// none is walkable, a project the subway reaches is featured, and its words say "by subway"; with
// none even so, nothing is featured, as before.
func TestFeaturedFallsBackToTheSubwayWhenNothingIsWalkable(t *testing.T) {
	if len(subwayStops) < 2 {
		t.Fatalf("data/subway.json: %d stops", len(subwayStops))
	}
	gfj, far := subwayStops[0], subwayStops[1]
	// The starter street walks to one station; the board street GB is walkable only from another
	// station, so job 1 (GX, built from GB) is reached by subway and nothing is walkable.
	c := openRoadsCase{
		Start: "GS",
		Roads: map[string][]string{"GS": {gfj}, gfj: {"GS"}, far: {"GB"}, "GB": {far, "GX"}, "GX": {"GB"}},
		Jobs: map[string]projJob{
			"1": {Kind: "street", Street: "GX", Offer: []string{"GB"}, Connecting: []string{"GB"}, Label: "Ex Lane", Hub: "Testwood"},
		},
	}
	m := openRoadsWorld(t, c, true)
	m.t.Names["GB"] = "Bee Road"
	_, reach := m.openWorldNow()
	if projectReachable(m.t.Jobs["1"], reach) {
		t.Fatal("job 1 should not be walkable in this world")
	}
	if id, bySubway := m.pickFeaturedWay(); id != "1" || !bySubway {
		t.Fatalf("pick %q by subway %v, want 1 by subway", id, bySubway)
	}
	m.ensureFeatured() // the server picks at start; this is the same pick
	if m.st.Featured == nil || m.st.Featured.Job != "1" || !m.st.Featured.Subway {
		t.Fatalf("featured %+v, want job 1 by subway", m.st.Featured)
	}
	if got, want := m.featuredText("1"), "The featured street project is now Ex Lane, the road from Bee Road in Testwood (by subway). Help build it!"; got != want {
		t.Fatalf("Global line %q, want %q", got, want)
	}
	if msg := m.featuredMsg(); msg["job"] != "1" || msg["subway"] != true || msg["board"] != "GB" {
		t.Fatalf("project_featured %v", msg)
	}
	// It stays while the subway reaches it.
	if m.ensureFeatured() || m.st.Featured.Job != "1" {
		t.Fatalf("the subway pick was dropped: %+v", m.st.Featured)
	}

	// A walkable project comes first: job 2 is built from the starter street's own neighbour.
	c.Roads["GS"] = []string{gfj, "GY"}
	c.Roads["GY"] = []string{"GS"}
	c.Jobs["2"] = projJob{Kind: "street", Street: "GW", Offer: []string{"GY"}, Connecting: []string{"GY"}, Label: "Way Walk"}
	m2 := openRoadsWorld(t, c, true)
	if id, bySubway := m2.pickFeaturedWay(); id != "2" || bySubway {
		t.Fatalf("pick %q by subway %v, want 2 on foot", id, bySubway)
	}
	if msg := (func() map[string]any { m2.ensureFeatured(); return m2.featuredMsg() })(); msg["subway"] != nil {
		t.Fatalf("a walkable pick says subway: %v", msg)
	}

	// Nothing reachable even by subway: nothing is featured, as before.
	c.Roads = map[string][]string{"GS": {gfj}, gfj: {"GS"}, "GB": {"GX"}, "GX": {"GB"}}
	delete(c.Jobs, "2")
	m3 := openRoadsWorld(t, c, true)
	if got := m3.pickFeatured(); got != "" {
		t.Fatalf("featured %q with no project reachable even by subway", got)
	}
}
