package main

// Where a street Piggy is on its shared path (G334), as the game draws it: the game's
// js/street-creatures.js piggyPlan and piggyWalkAt, ported number for number, so the server knows
// where a Piggy stands when it sets off for food (objects_pigs.go startTrip) without moving it.
//
// A Piggy walks to and fro round its spot (range px each way at 27 px/s, game.js spawnPiggies:
// 0.45 px a frame), stopping every randInt(3, 10) s to look at the screen for 5 s (three times in
// ten) or sniff for 3 s and turn round (npc_piggy.js startMoving 1226-1245, onRooting 1131-1160,
// stopSniffing 1267-1271), never by a tree's trunk. The stops are rolled from a seed every page
// shares (mulberry32 on chickenSeed), as one round that repeats. The arithmetic is the same IEEE
// double arithmetic in the same order as the JavaScript, so both give the same x
// (pigpath_test.go holds values the game computed).

import "math"

const (
	pigPathSpeed   = 27.0 // CREATURE_PATHS.pig.speed, px a second
	pigPlanRoots   = 12   // PIGGY_PLAN_ROOTS
	pigRootWalkMin = 3    // PIGGY_ROOT.walkS
	pigRootWalkMax = 10
	pigRootLook    = 0.3 // PIGGY_ROOT.lookChance
	pigRootLookMs  = 5000.0
	pigRootSniffMs = 3000.0
	pigTrunkWaitMs = 400.0
	pigTrunkTries  = 25
)

// pigHome is a Piggy's walk: its spot, how far each way, which way it set off, and the trunks it
// never stops at (the street's tree x, within trunkPx).
type pigHome struct {
	baseX   float64
	rng     float64
	dir     int
	trunks  []float64
	trunkPx float64
}

type pathSeg struct {
	at, ms, s float64
	pose      string
}

type pigPlanRound struct {
	ms, r, v float64
	segs     []pathSeg
}

// mulberry32 is js/street-creatures.js seededRandom.
type mulberry32 struct{ a uint32 }

func newMulberry32(seed uint32) *mulberry32 {
	if seed == 0 {
		seed = 1
	}
	return &mulberry32{a: seed}
}

func (m *mulberry32) next() float64 {
	m.a += 0x6d2b79f5
	t := m.a
	t = (t ^ (t >> 15)) * (t | 1)
	t ^= t + (t^(t>>7))*(t|61)
	return float64(t^(t>>14)) / 4294967296
}

// mathRoundJS is Math.round: halves go up.
func mathRoundJS(x float64) float64 { return math.Floor(x + 0.5) }

// creatureSeed is js/street-creatures.js chickenSeed: the seed a street animal rolls its stops from.
func creatureSeed(index int, baseX float64) uint32 {
	s := uint32(index+1)*2654435761 ^ uint32(int32(int64(mathRoundJS(baseX))))
	if s == 0 {
		return 1
	}
	return s
}

func fmod(a, n float64) float64 { return math.Mod(math.Mod(a, n)+n, n) }

// loopAt: a place on the to-and-fro loop (0..4r) to its x and heading.
func loopAt(baseX, r, s float64) (float64, int) {
	switch {
	case s < r:
		return baseX + s, 1
	case s < 3*r:
		return baseX + 2*r - s, -1
	}
	return baseX + s - 4*r, 1
}

// loopTurn: the same x heading the other way.
func loopTurn(r, s float64) float64 {
	switch {
	case s < r:
		return fmod(2*r-s, 4*r)
	case s < 3*r:
		u := 2*r - s
		if u >= 0 {
			return u
		}
		return 4*r + u
	}
	return fmod(2*r-(s-4*r), 4*r)
}

// pigPlan is js/street-creatures.js piggyPlan.
func pigPlan(h pigHome, seed uint32) pigPlanRound {
	r := math.Max(1, h.rng)
	v := pigPathSpeed / 1000
	loop := 4 * r
	rng := newMulberry32(seed)
	start := 0.0
	if h.dir < 0 {
		start = 2 * r
	}
	byTrunk := func(s float64) bool {
		x, _ := loopAt(h.baseX, r, s)
		for _, t := range h.trunks {
			if math.Abs(t-x) < h.trunkPx {
				return true
			}
		}
		return false
	}
	segs := make([]pathSeg, 0, 2*pigPlanRoots+1)
	at, s := 0.0, start
	for k := 0; k < pigPlanRoots; k++ {
		walk := (pigRootWalkMin + math.Floor(rng.next()*(pigRootWalkMax-pigRootWalkMin+1))) * 1000
		for i := 0; i < pigTrunkTries && byTrunk(fmod(s+v*walk, loop)); i++ {
			walk += pigTrunkWaitMs
		}
		look := rng.next() < pigRootLook
		segs = append(segs, pathSeg{at: at, ms: walk, s: s, pose: "walk"})
		at += walk
		s = fmod(s+v*walk, loop)
		hold, pose := pigRootSniffMs, "sniff"
		if look {
			hold, pose = pigRootLookMs, "look"
		}
		segs = append(segs, pathSeg{at: at, ms: hold, s: s, pose: pose})
		at += hold
		if !look {
			s = loopTurn(r, s)
		}
	}
	back := fmod(start-s, loop) / v
	segs = append(segs, pathSeg{at: at, ms: back, s: s, pose: "walk"})
	at += back
	return pigPlanRound{ms: at, r: r, v: v, segs: segs}
}

// pigAt is js/street-creatures.js piggyWalkAt: x, heading and pose at path time t (ms since the
// game's epoch, the server's clock minus the path's lag).
func pigAt(h pigHome, seed uint32, t float64) (float64, int, string) {
	plan := pigPlan(h, seed)
	if !(plan.ms > 0) {
		x, d := loopAt(h.baseX, plan.r, 0)
		return x, d, "walk"
	}
	tc := fmod(t, plan.ms)
	seg := plan.segs[0]
	for _, g := range plan.segs {
		if g.at > tc {
			break
		}
		seg = g
	}
	if seg.pose != "walk" {
		x, d := loopAt(h.baseX, plan.r, seg.s)
		return x, d, seg.pose
	}
	x, d := loopAt(h.baseX, plan.r, fmod(seg.s+plan.v*(tc-seg.at), 4*plan.r))
	return x, d, "walk"
}
