package main

import (
	"strings"
	"testing"
	"time"
)

func moveRig(t *testing.T) (*clockRig, *logBuf) {
	t.Helper()
	buf := captureLog(t)
	r := newClockRig(t, func(h *Hub) Module { return noModule{} })
	r.hub.moves().entries = map[string][][2]float64{"GA1": {{100, 600}, {5000, 600}}, "GA2": {{300, 600}}}
	return r, buf
}

func moveTo(r *clockRig, cl *Client, x, y float64) {
	r.do(cl, map[string]any{"t": "move", "x": x, "y": y, "dir": 1, "anim": "walk"})
}

func countLines(buf *logBuf, kind string) int {
	return strings.Count(buf.String(), "realtime: "+kind+" far")
}

func TestMoveWatchLetsEveryStepThroughAndNotesOnlyTheImpossibleOnes(t *testing.T) {
	r, buf := moveRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.do(ada, map[string]any{"t": "join", "street": "GA1", "x": 120, "y": 600})
	// A brisk walk and a fall: nothing noted.
	for i := 1; i <= 20; i++ {
		r.advance(100 * time.Millisecond)
		moveTo(r, ada, 120+float64(i)*57, 600+float64(i)*20)
	}
	if countLines(buf, "move") != 0 {
		t.Fatalf("an honest walk was noted: %s", buf.String())
	}
	// A leap across the street: the move still lands, and a line says so.
	r.advance(100 * time.Millisecond)
	moveTo(r, ada, 4900, 600)
	if ada.player.X != 4900 {
		t.Fatalf("the move was refused: %v", ada.player.X)
	}
	if countLines(buf, "move") != 1 || !strings.Contains(buf.String(), `player=1 name="Ada" street=GA1 steps=1`) {
		t.Fatalf("no line for the leap: %s", buf.String())
	}
	// More leaps in the same minute are counted, not written; the next minute's line carries the count.
	for i := 0; i < 5; i++ {
		r.advance(100 * time.Millisecond)
		moveTo(r, ada, float64(100+(i%2)*4800), 600)
	}
	if countLines(buf, "move") != 1 {
		t.Fatalf("more than one line a minute: %s", buf.String())
	}
	// A minute on, the count is written even with no new leap (the sweep).
	r.advance(time.Minute)
	moveTo(r, ada, 100, 600)
	if countLines(buf, "move") != 2 || !strings.Contains(buf.String(), "steps=5") {
		t.Fatalf("the next minute's line: %s", buf.String())
	}
}

func TestMoveWatchStartsAfreshOnAJoinAndAfterTheServerPlacesAPlayer(t *testing.T) {
	r, buf := moveRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.do(ada, map[string]any{"t": "join", "street": "GA1", "x": 120, "y": 600})
	// A street change through a signpost: the new street's arrive point.
	r.advance(time.Second)
	r.do(ada, map[string]any{"t": "join", "street": "GA2", "x": 320, "y": 610})
	r.advance(100 * time.Millisecond)
	moveTo(r, ada, 330, 610)
	// An administrator's teleport across the street: the next steps are not checked.
	r.hub.mu.Lock()
	r.hub.queueLocked(ada, map[string]any{"t": "teleport", "street": "GA2", "x": 9000, "y": 600, "by": "admin"})
	r.hub.mu.Unlock()
	r.advance(100 * time.Millisecond)
	moveTo(r, ada, 9000, 600)
	if n := countLines(buf, "move") + countLines(buf, "join"); n != 0 {
		t.Fatalf("honest arrivals noted: %s", buf.String())
	}
	// Once the moment has passed, a leap is noted again.
	r.advance(moveFreeFor)
	moveTo(r, ada, 9010, 600)
	r.advance(100 * time.Millisecond)
	moveTo(r, ada, 300, 600)
	if countLines(buf, "move") != 1 {
		t.Fatalf("a leap after the teleport's moment: %s", buf.String())
	}
}

func TestJoinWatchNotesAJoinFarFromEveryWayOnAndFromTheLastPlace(t *testing.T) {
	r, buf := moveRig(t)
	ada := r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	// Near an entry point: nothing.
	r.do(ada, map[string]any{"t": "join", "street": "GA1", "x": 400, "y": 600})
	r.advance(time.Second)
	moveTo(r, ada, 900, 600)
	r.advance(time.Second)
	moveTo(r, ada, 2500, 600)
	// A reload: back where the server last saw her, far from any entry point: nothing.
	r.hub.Disconnect(ada)
	r.advance(10 * time.Second)
	ada = r.hub.Connect(Claims{Sub: 1, Name: "Ada"})
	r.do(ada, map[string]any{"t": "join", "street": "GA1", "x": 2510, "y": 600})
	if countLines(buf, "join") != 0 {
		t.Fatalf("an honest join was noted: %s", buf.String())
	}
	// Joined somewhere nothing leads to: placed there all the same, and noted once.
	r.advance(2 * time.Second)
	r.do(ada, map[string]any{"t": "join", "street": "GA2", "x": 3000, "y": 100})
	if ada.Street() != "GA2" || ada.player.X != 3000 {
		t.Fatalf("the join was refused: %s %v", ada.Street(), ada.player.X)
	}
	if countLines(buf, "join") != 1 || !strings.Contains(buf.String(), "street=GA2 at=3000,100") {
		t.Fatalf("no line for the join: %s", buf.String())
	}
	// A street with no entry points (a home, a quest room) is never noted.
	r.advance(2 * time.Second)
	r.do(ada, map[string]any{"t": "join", "street": "HOME_5", "x": 99999, "y": 0})
	if countLines(buf, "join") != 1 {
		t.Fatalf("a street with no entry points was noted: %s", buf.String())
	}
}

func TestEntryPointsAreTheGamesData(t *testing.T) {
	e := parseEntries(entriesJSON)
	if len(e) < 1000 {
		t.Fatalf("only %d streets have entry points", len(e))
	}
	for street, pts := range e {
		if !streetRe.MatchString(street) || len(pts) == 0 {
			t.Fatalf("street %q has %d points", street, len(pts))
		}
	}
}
