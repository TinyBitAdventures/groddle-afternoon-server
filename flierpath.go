package main

// Where a street Batterfly is on its shared flight (lane flyk, 2026-11-24), as the game draws it:
// the game's js/street-creatures.js flierPlan and flierAt, ported number for number, so the server
// drops each hourly Guano where the Batterfly is flying when it comes due (objects_guano.go), as
// the source did (npc_batterfly.js onInterval 1064-1072: createItemStackFromSource('guano', 1,
// this.x, this.y, this)).
//
// Glitch flew each Batterfly at random over the whole street in the 400 px over its floor
// (npc_batterfly.js flyAround 1039-1052, apiStartFlyingInTheArea). Every page flies it on legs
// between places rolled from a seed (mulberry32 on its kind, spawn index and spot) across the
// street's width, at its own speed, on the path's clock. The server needs only its x: the height
// rides over the floor, which the page knows, and a dropped Guano falls to the floor under it. The
// arithmetic is the same IEEE double arithmetic in the same order as the JavaScript (flierpath_test.go
// holds values the game computed).

import (
	"math"
	"sync"
)

// flyWander is js/street-creatures.js FLY_WANDER: speed px/s, the band over the floor, the edge kept.
type flyWander struct {
	speed0, speed1 float64
	low, high      float64
	edge           float64
}

var flyWanderKinds = map[string]flyWander{
	"butterfly": {30, 50, 40, 200, 40},
	"batterfly": {30, 45, 60, 400, 60},
}

const (
	flyLegs    = 12    // FLY_LEGS
	flyMinStep = 160.0 // FLY_MIN_STEP
)

type flierLeg struct{ at, ms, x, h, tx, th float64 }

type flierRound struct {
	ms, speed, offset float64
	legs              []flierLeg
}

// flierSeed is js/street-creatures.js flierSeed.
func flierSeed(kind string, index int, spotX float64) uint32 {
	salt := uint32(0x27d4eb2f)
	if kind == "batterfly" {
		salt = 0x5bd1e995
	}
	if s := creatureSeed(index, spotX) ^ salt; s != 0 {
		return s
	}
	return 1
}

// flierRange is js/street-creatures.js flierRange: the x a flier's point keeps to.
func flierRange(kind string, worldW float64) (float64, float64) {
	spec, ok := flyWanderKinds[kind]
	if !ok {
		spec = flyWanderKinds["butterfly"]
	}
	w := math.Max(0, worldW)
	if w <= 2*spec.edge {
		return w / 2, w / 2
	}
	return spec.edge, w - spec.edge
}

type flierKey struct {
	kind   string
	seed   uint32
	worldW float64
}

var (
	flierPlansMu sync.Mutex
	flierPlans   = map[flierKey]*flierRound{}
)

// flierPlan is js/street-creatures.js flierPlan.
func flierPlan(kind string, seed uint32, worldW float64) *flierRound {
	key := flierKey{kind, seed, worldW}
	flierPlansMu.Lock()
	defer flierPlansMu.Unlock()
	if p := flierPlans[key]; p != nil {
		return p
	}
	spec, ok := flyWanderKinds[kind]
	if !ok {
		spec = flyWanderKinds["butterfly"]
	}
	rng := newMulberry32(seed)
	speed := (spec.speed0 + rng.next()*(spec.speed1-spec.speed0)) / 1000
	lo, hi := flierRange(kind, worldW)
	step := math.Min(flyMinStep, (hi-lo)/3)
	height := func() float64 { return spec.low + rng.next()*(spec.high-spec.low) }
	x0 := lo + rng.next()*(hi-lo)
	h0 := height()
	p := &flierRound{speed: speed}
	at, x, h := 0.0, x0, h0
	leg := func(tx, th float64) {
		ms := math.Sqrt((tx-x)*(tx-x)+(th-h)*(th-h)) / speed
		p.legs = append(p.legs, flierLeg{at: at, ms: ms, x: x, h: h, tx: tx, th: th})
		at += ms
		x, h = tx, th
	}
	for k := 0; k < flyLegs; k++ {
		left := math.Max(0, x-step-lo)
		right := math.Max(0, hi-(x+step))
		r := rng.next() * (left + right)
		var tx float64
		switch {
		case left+right <= 0:
			tx = lo + rng.next()*(hi-lo)
		case r < left:
			tx = lo + r
		default:
			tx = x + step + (r - left)
		}
		tx = math.Min(hi, math.Max(lo, tx))
		leg(tx, height())
	}
	leg(x0, h0)
	p.ms = at
	p.offset = math.Floor(rng.next() * at)
	if len(flierPlans) > 512 {
		flierPlans = map[flierKey]*flierRound{}
	}
	flierPlans[key] = p
	return p
}

// flierX is js/street-creatures.js flierAt's x at path time t (ms since the game's epoch).
func flierX(kind string, seed uint32, worldW, t float64) float64 {
	p := flierPlan(kind, seed, worldW)
	tc := 0.0
	if p.ms > 0 {
		tc = fmod(t+p.offset, p.ms)
	}
	l := p.legs[0]
	for _, g := range p.legs {
		if g.at > tc {
			break
		}
		l = g
	}
	u := 1.0
	if l.ms > 0 {
		u = math.Min(1, math.Max(0, (tc-l.at)/l.ms))
	}
	return l.x + (l.tx-l.x)*u
}
