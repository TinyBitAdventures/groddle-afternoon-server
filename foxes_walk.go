package main

// Where a preserve's foxes run (slice 3 of research/plans/street-animals-shared.md). In the original
// the server ran the one npc_fox item along the street (npc_fox.js startMoving, onPathing 3/4) and
// every client drew it where the server said. Before this each player's game ran the foxes itself
// from the server's spawn and baits, so two players saw a fox in different places and the first to
// see it reach a bait ate it for everyone. Now the server runs them, with the game's own rules (the
// game's js/foxes.js foxDestination, foxArrival and stepFox, ported here) and the preserve's layout
// as every page lays it out (data/foxes.json "layouts": the street's width, the Fox Stop Markers,
// the Ranger and the spawner, written by the game's tools/build-realtime-foxes.mjs from
// js/foxes.js preserveLayout): a fox runs at 500 px/s from one place to the next, eats a bait it
// reaches (for everyone: fox_bait_gone), pauses at a marker, turns at the street's ends and at the
// Ranger, and leaves through the spawner when no bait is left. Each leg goes to the street as
// fox_walk ({"fox","x0","x1","t0","ms","pose","now"}: from x0 to x1 over ms from t0, server ms; pose
// "run", or "eat", "pause", "brushed" standing at x1), so every page draws the same fox in the same
// place. A brush stops it 600 ms where it is (stepFox's brushed state). Ours: the server does not
// know the floor, so a fox never turns at a gap (onStuck); the preserves are flat runs.
//
// A preserve the layout file does not know (an older data file) keeps the earlier rules: each page
// runs its foxes and reports eats (fox_eat), and a leaving fox counts for leaveMs.

import (
	"math"
	"time"
)

const (
	foxRunPxMs     = 0.5 // FOX_TIMING.runSpeed 500 px/s
	foxEdgeInset   = 100.0
	foxTargetGap   = 100.0
	foxRangerGap   = 20.0
	foxArrive      = 100.0
	foxPauseMs     = 1000
	foxEatMs       = 500
	foxEatResumeMs = 2500
	foxBrushedMs   = 600
	foxExitBeyond  = 500.0
)

// foxLayout is a preserve as every page lays it out (js/foxes.js preserveLayout).
type foxLayout struct {
	W       float64   `json:"w"`
	Markers []float64 `json:"markers"`
	Ranger  float64   `json:"ranger"`
	Spawner float64   `json:"spawner"`
}

// foxLeg is one leg of a fox's run, or a stand (x0 == x1).
type foxLeg struct {
	x0, x1 float64
	t0     time.Time
	ms     int64
	pose   string
}

func (l foxLeg) xAt(now time.Time) float64 {
	if l.ms <= 0 || l.pose != "run" {
		return l.x1
	}
	f := math.Min(1, math.Max(0, float64(now.Sub(l.t0).Milliseconds())/float64(l.ms)))
	return l.x0 + (l.x1-l.x0)*f
}

func (l foxLeg) json(id string, now time.Time) map[string]any {
	return map[string]any{"fox": id, "x0": math.Round(l.x0*10) / 10, "x1": math.Round(l.x1*10) / 10, "t0": l.t0.UnixMilli(), "ms": l.ms, "pose": l.pose, "now": now.UnixMilli()}
}

func inFoxDirection(dir int, itemX, destX, ourX, gap float64) bool {
	if dir < 0 {
		return itemX >= destX && itemX <= ourX-gap
	}
	return itemX <= destX && itemX >= ourX+gap
}

// foxDest is js/foxes.js foxDestination: where the fox runs next, and what it is ("edge", "bait",
// "marker", "ranger", "exit", "leave").
func (m *foxes) foxDest(lay foxLayout, p *preserve, f *preserveFox) (float64, string) {
	if f.leaving {
		if lay.Spawner < f.x {
			return lay.Spawner - foxExitBeyond, "exit"
		}
		return lay.Spawner + foxExitBeyond, "exit"
	}
	left, right := foxEdgeInset, lay.W-foxEdgeInset
	dir := f.dir
	var dest float64
	if dir < 0 && left != f.x {
		dest = left
	} else {
		dest = right
		dir = 1
	}
	kind := "edge"
	var best *preserveBait
	for _, b := range p.baits {
		if inFoxDirection(dir, b.x, dest, f.x, foxTargetGap) && (best == nil || math.Abs(b.x-f.x) < math.Abs(best.x-f.x)) {
			best = b
		}
	}
	if best != nil {
		dest, kind = best.x, "bait"
	} else if len(p.baits) == 0 {
		return f.x, "leave"
	}
	mark, found := 0.0, false
	for _, x := range lay.Markers {
		if inFoxDirection(dir, x, dest, f.x, foxTargetGap) && (!found || math.Abs(x-f.x) < math.Abs(mark-f.x)) {
			mark, found = x, true
		}
	}
	if found {
		dest, kind = mark, "marker"
	}
	if inFoxDirection(dir, lay.Ranger, dest, f.x, foxRangerGap) {
		dest, kind = lay.Ranger, "ranger"
	}
	return dest, kind
}

// foxGo is stepFox's begin: the next destination, a run toward it.
func (m *foxes) foxGo(street string, p *preserve, f *preserveFox, lay foxLayout) {
	dest, kind := m.foxDest(lay, p, f)
	if kind == "leave" {
		m.leave(street, p, f, m.t.Text.LeavesNoBait)
		dest, _ = m.foxDest(lay, p, f)
	} else if dest < f.x {
		f.dir = -1
	} else if dest > f.x {
		f.dir = 1
	}
	ms := int64(math.Round(math.Abs(dest-f.x) / foxRunPxMs))
	m.foxLeg(street, p, f, lay, foxLeg{x0: f.x, x1: dest, t0: m.h.Now(), ms: ms, pose: "run"})
}

// foxLeg starts a leg, tells the street, and arms its end.
func (m *foxes) foxLeg(street string, p *preserve, f *preserveFox, lay foxLayout, leg foxLeg) {
	if f.step != nil {
		f.step()
	}
	f.leg = leg
	msg := leg.json(f.id, m.h.Now())
	msg["t"] = "fox_walk"
	m.h.BroadcastStreetLocked(street, msg, 0)
	f.step = m.h.After(time.Duration(leg.ms)*time.Millisecond, func() {
		f.step = nil
		if m.at[street] != p || m.findFox(p, f.id) == nil {
			return
		}
		f.x = leg.x1
		if leg.pose == "run" {
			m.foxArrived(street, p, f, lay)
		} else {
			m.foxGo(street, p, f, lay)
		}
	})
}

// foxArrived is stepFox at its destination (foxArrival): gone at the exit, eats a bait in reach,
// pauses at a marker, else (the edge, the Ranger) turns round and runs on.
func (m *foxes) foxArrived(street string, p *preserve, f *preserveFox, lay foxLayout) {
	if f.leaving {
		m.foxGone(street, p, f)
		return
	}
	var bait *preserveBait
	for _, b := range p.baits {
		if bait == nil || math.Abs(b.x-f.x) < math.Abs(bait.x-f.x) {
			bait = b
		}
	}
	if bait != nil && math.Abs(f.x-bait.x) <= foxArrive {
		more := len(p.baits) > 1
		m.removeBait(street, bait.id)
		if m.findFox(p, f.id) == nil {
			return
		}
		if more && !f.leaving {
			if m.foxRoll() < 0.5 {
				f.dir = -f.dir
			}
			m.foxLeg(street, p, f, lay, foxLeg{x0: f.x, x1: f.x, t0: m.h.Now(), ms: foxEatResumeMs, pose: "eat"})
		} else {
			m.leave(street, p, f, m.t.Text.LeavesNoBait)
			m.foxLeg(street, p, f, lay, foxLeg{x0: f.x, x1: f.x, t0: m.h.Now(), ms: foxEatResumeMs - foxEatMs, pose: "eat"})
		}
		return
	}
	for _, x := range lay.Markers {
		if math.Abs(f.x-x) <= foxArrive {
			if m.foxRoll() < 0.5 {
				f.dir = -f.dir
			}
			m.foxLeg(street, p, f, lay, foxLeg{x0: f.x, x1: f.x, t0: m.h.Now(), ms: foxPauseMs, pose: "pause"})
			return
		}
	}
	f.dir = -f.dir
	m.foxGo(street, p, f, lay)
}

// foxGone: it reached the exit and left the street.
func (m *foxes) foxGone(street string, p *preserve, f *preserveFox) {
	if f.step != nil {
		f.step()
		f.step = nil
	}
	for i, o := range p.foxes {
		if o == f {
			p.foxes = append(p.foxes[:i], p.foxes[i+1:]...)
			m.h.BroadcastStreetLocked(street, map[string]any{"t": "fox_gone", "fox": f.id}, 0)
			return
		}
	}
}

// foxBrushed: a brush stops it where it is for 600 ms (stepFox's brushed state), then it runs on.
func (m *foxes) foxBrushed(street string, p *preserve, f *preserveFox) {
	lay, ok := m.t.Layouts[street]
	if !ok || f.leaving {
		return
	}
	f.x = f.leg.xAt(m.h.Now())
	m.foxLeg(street, p, f, lay, foxLeg{x0: f.x, x1: f.x, t0: m.h.Now(), ms: foxBrushedMs, pose: "brushed"})
}

// foxTurnToExit: no bait left and it was running toward something: it heads for the exit now.
func (m *foxes) foxTurnToExit(street string, p *preserve, f *preserveFox) {
	lay, ok := m.t.Layouts[street]
	if !ok || f.leg.pose != "run" {
		return
	}
	f.x = f.leg.xAt(m.h.Now())
	m.foxGo(street, p, f, lay)
}

func (m *foxes) foxRoll() float64 {
	if m.roll != nil {
		return m.roll()
	}
	return 0.5
}
