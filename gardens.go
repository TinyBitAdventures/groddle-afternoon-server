package main

// Shared public gardens (register G368): a street bed's plots are the same for
// everyone on the street, as the original's public gardens were
// (items/include/garden.js isPublic 1244-1247: a garden with no owner). Anyone
// may plant a clean plot, water a dry one, Guano a growing one, pick a ripe one
// and hoe a dirty one; the server keeps each plot's state, grows it and decides
// every contested act. A home's beds stay the owner's (homes.go, the site's
// HomeTending.php), and so do street Patches, the tutorial's spinach and
// tower rooms: data/gardens.json lists only the ten public streets
// (tools/build-realtime-gardens.mjs in the game repo).
//
// Client to server:
//
//	{"t":"garden_sync"}                                                after a join: the street's beds, please
//	{"t":"garden_act","req":7,"id":"default:1-2","verb":"plant","seed":"seed_spinach","mod":0.9}
//	{"t":"garden_act","req":8,"id":"default:1-2","verb":"water"}       (also "clear", "fertilize")
//	{"t":"garden_act","req":9,"id":"default:1-2","verb":"harvest","perks":["full"]}
//	{"t":"garden_act","req":10,"bed":"herb","verb":"water_all","ids":["herb:1-1","herb:2-1"]}
//	{"t":"garden_act",…,"verb":"water","tool":"irrigator_9000","ms":2000}  the can or hoe used, for the others' overlay
//	{"t":"garden_potion","req":11,"bed":"default","c":"potion_garden_plant","k":0,"seed":"seed_spinach","n":8}
//	                                                                    a Master Gardener pour (gardens_potions.go)
//	{"t":"garden_ack","id":"gg-…"}                                     a pick's crop is in the saved pack
//	{"t":"garden_dev","lock":5000,"water":20000}                       QA only, GRODDLE_GARDENS_DEV=1
//	{"t":"garden_dev","skip":180000}                                   QA only: garden time passes (below)
//
// Server to client:
//
//	{"t":"gardens","street":"GCR…","snap":true,"now":ms,"lock":60000,"water":4320000,
//	 "beds":{"default":[4,2],"herb":[3,2]},"plots":{"default:1-2":Plot}}   the answer to garden_sync
//	                                                    ("none":true: nothing is shared on this street)
//	{"t":"gardens","street":"GCR…","now":ms,"plots":{…}}                    a change, a stage, a drying
//	{"t":"garden_ok","req":7,"id":"default:1-2","verb":"plant","now":ms,"before":{…},"plots":{…},"use":{…}}
//	{"t":"garden_ok",…,"verb":"harvest","grant":{"id":"gg-…","c":"spinach","n":3}}
//	{"t":"garden_no","req":7,"id":"default:1-2","verb":"harvest","code":"locked","message":"…","now":ms,"plots":{…}}
//	{"t":"garden_grant","id":"gg-…","c":"spinach","n":3}                    a pick not acknowledged yet (on connect)
//	{"t":"garden_ev","street":"GCR…","by":7,"verb":"water","ids":["default:1-2"],"tool":"watering_can","ms":2000}
//	                                                    someone else tended (to the others); "tool": what the
//	                                                    original showed over their head (include/garden.js 708-722)
//
// Plot: {"state":"seeds","seed":"seed_spinach","wet":true,"wetAt":ms,"stageAt":ms,"banked":ms,"mod":0.9,
// "fert":true,"ripeAt":ms,"planter":7,"lock":ms,"pace":10,"wetPace":10}, times the server's Unix ms. A plot
// nobody touched is {"state":"clean"}. "planter" and "lock" (ms left) only while the planter's 60 seconds
// run. "pace" and "wetPace" say which clock the crop and the watering run on (below).
//
// The rules (js/garden.js, include/garden.js):
//   - Growth runs only while wet (stateChanged dry 989-1020: progress is kept). A watering lasts
//     rules.waterMs; seeds -> shoots -> crop take the seed's grow1Ms and grow2Ms times its grow
//     modifier (the Growth Spurt and Speedy Herbs cards, the planter's). The times are the source's
//     hours at rules.pace times Glitch's speed (Austin 2026-11-10: about 10, so a source hour is 6
//     minutes and a watering 72; js/garden.js GARDEN_SPEEDUP is the one number, and
//     tools/build-realtime-gardens.mjs writes it here). Growth goes on while the server is down, as
//     the game's does on a closed page; every stage a long gap passed is taken.
//   - A crop keeps the clock it was planted on, and a watering the clock it was poured on: a plot
//     says so with pace and wetPace, set at planting and at watering. A plot in the state file with
//     no pace is from before the speed changed (8 to 92 s a crop, a watering 12 real hours):
//     rules.legacy holds that clock, and the crop finishes on it. Nothing in the ground is lost,
//     reset or made to wait by the change.
//   - Plant only a clean plot (339-384, 441-446), the seed fitting the bed. The site's ledger takes
//     the seed first (groddle-afternoon-site LedgerUse.php, whole items, "n":1); the answer carries
//     its "use", which the page applies once (its trade slice's applied list).
//   - Water a dry plot, any state (the remake's G537); a can, as the ledger holds (objects_tools.go).
//     Water All is one message naming the plots the page's own rolls watered.
//   - Pick a crop: refused for anyone but the planter for rules.lockMs after it ripened
//     (952 pushEvent 0.0167 h, 1042-1046, 1229-1231; GardenView.as 481-486 refuses only in the
//     client, this server refuses too: Austin 2026-11-07). The first pick wins; the next hears
//     "Oops. There is nothing there to pick." (469-472). The base crop is credited to the site's
//     ledger as a "take" receipt (credits.go), produces_count halved without Croppery I or
//     Herbalism I (474-484; the page says "full"); a super harvest and drops are the page's,
//     through the budget, as on trees. The grant is kept, resent on connect and in the state file,
//     until the page says the crop is saved (garden_ack): a lost answer never loses a crop.
//   - Hoe a dirty plot (233-272, "Oops. Don't need no hoeing." 331-336); a hoe, as the ledger holds.
//   - Guano a growing plot at least 80% watered (609-669, GardenPlotView.as 152-168); the ledger
//     takes the Guano first. Seeds go to shoots, shoots keep a tenth of what is left (625-639).
//   - Energy, mood, iMG, tool wear and skill success rolls stay the page's (the plan: a cheat
//     only skips a retry).
//
// Ours: after a restart, a crop whose planter's 60 seconds ran (or began) while the server was down
// gives them a fresh 60 seconds, so a deploy never costs a planter the window.
//
// -gardens-file (default realtime-gardens.json beside -secret-file; off: memory only) keeps every
// touched plot and the grants; plant and pick write it at once, the rest within a second. A file the
// layout no longer fits drops the plots it names no more. -gardens off answers nothing (the hub's
// bad_frame), so pages keep their own plots, as before G368.

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

//go:embed data/gardens.json
var gardensJSON []byte

const (
	gardenLockLine     = "Another player planted those seeds, give them time to snag them up!" // GardenView.as 485
	gardenNothingPick  = "Oops. There is nothing there to pick."                               // include/garden.js 469-472
	gardenNotClean     = "Oops. You need to clean that plot first."                            // include/garden.js 441-446
	gardenNoSeed       = "Oops. You don't have that seed!"                                     // include/garden.js 433-438
	gardenNoHoeing     = "Oops. Don't need no hoeing."                                         // include/garden.js 331-336
	gardenWet          = "That plot is already wet."                                           // js/garden.js waterPlot
	gardenNoDry        = "Oops, there aren't any dry plots here."                              // potion_garden_water.js onPour
	gardenHerbOnly     = "Only herb seeds go in an herb garden."                               // js/garden.js plantPlot
	gardenCropOnly     = "Only crop seeds go in a crop garden."
	gardenFertNothing  = "Oops. There is nothing there to fertilize." // include/garden.js 675
	gardenFertNoGuano  = "Oops. You don't have any guano!"            // include/garden.js 664
	gardenFertDry      = "You need to water it first!"                // GardenPlotView.as 160-165
	gardenFertMore     = "You need to water it more first!"
	gardenBusy         = "Someone is already working on that plot."
	gardenGuano        = "guano"
	gardenGrantKeep    = 7 * 24 * time.Hour // a grant nobody acknowledged this long goes (the site keeps a receipt 24 h)
	gardenGrantsPerOne = 200
)

var (
	gardensFileFlag = flag.String("gardens-file", "", "file that keeps the shared public gardens' plots and the picks not yet saved (default: realtime-gardens.json beside -secret-file; off: memory only)")
	gardensFlag     = flag.String("gardens", "on", "on: the public gardens are shared (gardens.go); off: pages keep their own plots, as before")
	// gardensFileForTest lets a test give the module a state file; gardensOffForTest turns it off. Under go test the flags are ignored.
	gardensFileForTest string
	gardensOffForTest  bool

	// A bed's key: default, herb, or herb2..herb9 for a street with more herb beds (the Herb Gardens
	// streets: one bed in each planter box, js/street-ecology.js streetGardenBoxBeds); its kind is the
	// key without the digit.
	gardenPlotRe = regexp.MustCompile(`^(default|herb[2-9]?):(\d{1,2})-(\d{1,2})$`)
	gardenBedRe  = regexp.MustCompile(`^(default|herb[2-9]?)$`)
	gardenVerbs  = map[string]bool{"plant": true, "water": true, "water_all": true, "harvest": true, "clear": true, "fertilize": true}
)

// ---------------------------------------------------------------------------
// The layout and rules (data/gardens.json)
// ---------------------------------------------------------------------------

type gardenSeed struct {
	bed      string
	g1, g2   int64 // the two stages at rules.pace
	l1, l2   int64 // the same on the clock from before it (rules.legacy)
	crop     string
	produces int
}

type gardenBed struct {
	bed        string // the kind: default (crops) or herb
	key        string // the id in plot ids: the kind, or herb2..herb9
	cols, rows int
	x, y       float64
}

type gardenWorld struct {
	pace            int   // how many times faster than Glitch a garden runs (js/garden.js GARDEN_SPEEDUP)
	waterMs, lockMs int64 // a watering at that pace; the planter's lock
	legacyWaterMs   int64 // a watering poured before the pace (wetPace unset)
	fertilize       float64
	seeds           map[string]gardenSeed
	streets         map[string]map[string]*gardenBed
	potions         map[string]gardenPotion // gardens_potions.go
}

func loadGardenWorld(raw []byte) (*gardenWorld, error) {
	var file struct {
		Rules struct {
			Pace      int              `json:"pace"`
			WaterMs   int64            `json:"waterMs"`
			LockMs    int64            `json:"lockMs"`
			Fertilize float64          `json:"fertilize"`
			Seeds     map[string][]any `json:"seeds"`
			Legacy    struct {
				WaterMs int64              `json:"waterMs"`
				Seeds   map[string][]int64 `json:"seeds"`
			} `json:"legacy"`
			Potions map[string][]string `json:"potions"`
		} `json:"rules"`
		Streets map[string][][]any `json:"streets"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	w := &gardenWorld{pace: file.Rules.Pace, waterMs: file.Rules.WaterMs, legacyWaterMs: file.Rules.Legacy.WaterMs, lockMs: file.Rules.LockMs, fertilize: file.Rules.Fertilize, seeds: map[string]gardenSeed{}, streets: map[string]map[string]*gardenBed{}, potions: map[string]gardenPotion{}}
	for id, r := range file.Rules.Potions {
		if len(r) != 2 || !potionActions[r[0]] || r[1] == "" {
			return nil, fmt.Errorf("bad potion %s %v", id, r)
		}
		w.potions[id] = gardenPotion{action: r[0], noTarget: r[1]}
	}
	if w.pace <= 0 || w.waterMs <= 0 || w.legacyWaterMs <= 0 || w.lockMs <= 0 || w.fertilize <= 0 {
		return nil, fmt.Errorf("bad rules %+v", file.Rules)
	}
	for id, r := range file.Rules.Seeds {
		if len(r) != 5 {
			return nil, fmt.Errorf("bad seed %s", id)
		}
		bed, ok1 := r[0].(string)
		g1, ok2 := r[1].(float64)
		g2, ok3 := r[2].(float64)
		crop, ok4 := r[3].(string)
		n, ok5 := r[4].(float64)
		if !(ok1 && ok2 && ok3 && ok4 && ok5) || (bed != "default" && bed != "herb") || g1 <= 0 || g2 <= 0 || crop == "" || n < 1 {
			return nil, fmt.Errorf("bad seed %s %v", id, r)
		}
		old := file.Rules.Legacy.Seeds[id]
		if len(old) != 2 || old[0] <= 0 || old[1] <= 0 {
			return nil, fmt.Errorf("seed %s has no legacy times %v", id, old)
		}
		w.seeds[id] = gardenSeed{bed: bed, g1: int64(g1), g2: int64(g2), l1: old[0], l2: old[1], crop: crop, produces: int(n)}
	}
	for tsid, beds := range file.Streets {
		row := map[string]*gardenBed{}
		for _, b := range beds {
			if len(b) != 5 {
				return nil, fmt.Errorf("%s: bad bed %v", tsid, b)
			}
			key, ok1 := b[0].(string)
			cols, ok2 := b[1].(float64)
			rows, ok3 := b[2].(float64)
			x, ok4 := b[3].(float64)
			y, ok5 := b[4].(float64)
			if !(ok1 && ok2 && ok3 && ok4 && ok5) || !gardenBedRe.MatchString(key) || cols < 1 || rows < 1 || cols > 12 || rows > 12 || row[key] != nil {
				return nil, fmt.Errorf("%s: bad bed %v", tsid, b)
			}
			kind := key
			if strings.HasPrefix(key, "herb") {
				kind = "herb"
			}
			row[key] = &gardenBed{bed: kind, key: key, cols: int(cols), rows: int(rows), x: x, y: y}
		}
		w.streets[tsid] = row
	}
	return w, nil
}

var gardenWorldData *gardenWorld

func init() {
	w, err := loadGardenWorld(gardensJSON)
	if err != nil {
		panic("realtime: data/gardens.json: " + err.Error())
	}
	gardenWorldData = w
	modules = append(modules, newGardens)
}

// bedOf is the bed a plot id names on a street, and the plot's key ("1-2"), or nil.
func (w *gardenWorld) bedOf(street, id string) (*gardenBed, string) {
	m := gardenPlotRe.FindStringSubmatch(id)
	if m == nil {
		return nil, ""
	}
	bed := w.streets[street][m[1]]
	col, _ := strconv.Atoi(m[2])
	row, _ := strconv.Atoi(m[3])
	if bed == nil || col < 1 || row < 1 || col > bed.cols || row > bed.rows {
		return nil, ""
	}
	return bed, m[2] + "-" + m[3]
}

// ---------------------------------------------------------------------------
// Plots
// ---------------------------------------------------------------------------

// gPlot is one touched plot. Times are Unix ms. A clean dry plot is forgotten (fresh).
type gPlot struct {
	state   string // clean | seeds | shoots | crop | dirty
	seed    string
	wet     bool
	wetAt   int64
	stageAt int64 // the stage clock's start while wet (js/garden.js stageStartedAt)
	banked  int64 // growth kept from before a drying (growthMs)
	mod     float64
	fert    bool
	planter int64
	ripeAt  int64
	pace    int // the clock the crop was planted on (rules.pace); 0: before it (rules.legacy)
	wetPace int // the clock the watering was poured on; 0: before it
}

func freshPlot() *gPlot { return &gPlot{state: "clean", mod: 1} }

func (p *gPlot) fresh() bool { return p.state == "clean" && !p.wet }

func (p *gPlot) growing() bool { return p.state == "seeds" || p.state == "shoots" }

// need is the current stage's growing time, on the clock the crop was planted on.
func (g *gardens) need(p *gPlot) int64 {
	s, ok := g.w.seeds[p.seed]
	if !ok {
		return 0
	}
	t1, t2 := s.l1, s.l2
	if p.pace == g.w.pace {
		t1, t2 = s.g1, s.g2
	}
	if p.state == "shoots" {
		t1 = t2
	}
	return int64(math.Round(float64(t1) * p.mod))
}

// waterOf is how long the plot's watering lasts, on the clock it was poured on.
func (g *gardens) waterOf(p *gPlot) int64 {
	if p.wetPace == g.w.pace {
		return g.waterMs
	}
	return g.w.legacyWaterMs
}

// sow puts a seed in a plot on today's clock (include/garden.js 357-384; js/garden.js sowPlot).
func (g *gardens) sow(p *gPlot, seed string, mod float64, planter, now int64) {
	p.state, p.seed, p.fert, p.mod, p.planter, p.ripeAt = "seeds", seed, false, mod, planter, 0
	p.stageAt, p.banked, p.pace = now, 0, g.w.pace
}

// wetNow waters a plot (js/garden.js wetPlot): a dry plot's stage clock restarts, the watering starts again.
func (g *gardens) wetNow(p *gPlot, now int64) {
	if !p.wet {
		p.stageAt = now
	}
	p.wet, p.wetAt, p.wetPace = true, now, g.w.pace
}

// advance brings a plot to now: every stage the time passed (js/garden.js tickGarden, each
// transition at its own time), the drying, the end of the planter's lock. True when it changed.
func (g *gardens) advance(p *gPlot, now int64) bool {
	changed := false
	for p.wet {
		dryAt := p.wetAt + g.waterOf(p)
		at := min(now, dryAt)
		if p.growing() {
			if need := g.need(p); need > 0 && p.banked+(at-p.stageAt) >= need {
				t := p.stageAt + (need - p.banked)
				if p.state == "seeds" {
					p.state = "shoots"
				} else {
					// grow2 ends (include/garden.js 1038-1046): the crop, its time, the planter's lock from now.
					p.state, p.ripeAt = "crop", t
				}
				p.stageAt, p.banked = t, 0
				changed = true
				continue
			}
		}
		if now >= dryAt {
			// stateChanged('dry'): the stage's growth is banked and its clock stops.
			if p.growing() {
				p.banked += max(0, dryAt-p.stageAt)
			}
			p.stageAt, p.wet, p.wetAt, p.wetPace = dryAt, false, 0, 0
			changed = true
		}
		break
	}
	if p.state == "crop" && p.planter != 0 && now >= p.ripeAt+g.lockMs {
		// pushEvent(plot, 0.0167, 'unlock'): the crop is anyone's.
		p.planter = 0
		changed = true
	}
	return changed
}

// due is when the plot next changes on its own (0: never).
func (g *gardens) due(p *gPlot) int64 {
	var at int64
	pick := func(t int64) {
		if at == 0 || t < at {
			at = t
		}
	}
	if p.wet {
		pick(p.wetAt + g.waterOf(p))
		if p.growing() {
			if need := g.need(p); need > 0 {
				pick(p.stageAt + need - p.banked)
			}
		}
	}
	if p.state == "crop" && p.planter != 0 {
		pick(p.ripeAt + g.lockMs)
	}
	return at
}

// locked: someone else planted this crop and their 60 seconds run.
func (g *gardens) locked(p *gPlot, who int64, now int64) bool {
	return p.state == "crop" && p.planter != 0 && p.planter != who && now < p.ripeAt+g.lockMs
}

func (g *gardens) view(p *gPlot, now int64) map[string]any {
	if p == nil {
		return map[string]any{"state": "clean"}
	}
	m := map[string]any{"state": p.state, "stageAt": p.stageAt}
	if p.seed != "" {
		m["seed"] = p.seed
	}
	if p.wet {
		m["wet"], m["wetAt"] = true, p.wetAt
		if p.wetPace != 0 {
			m["wetPace"] = p.wetPace
		}
	}
	if p.pace != 0 && p.seed != "" {
		m["pace"] = p.pace
	}
	if p.banked > 0 {
		m["banked"] = p.banked
	}
	if p.mod != 1 {
		m["mod"] = p.mod
	}
	if p.fert {
		m["fert"] = true
	}
	if p.state == "crop" {
		m["ripeAt"] = p.ripeAt
		if p.planter != 0 && now < p.ripeAt+g.lockMs {
			m["planter"], m["lock"] = p.planter, p.ripeAt+g.lockMs-now
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// The module
// ---------------------------------------------------------------------------

type gardenGrant struct {
	ID   string `json:"id"`
	User int64  `json:"user"`
	C    string `json:"c"`
	N    int    `json:"n"`
	At   int64  `json:"at"`
}

type gardens struct {
	pace    *owedPacer // the connect's resend of grants, owedBatch at a time (owed_pace.go)
	h       *Hub
	w       *gardenWorld
	off     bool
	plots   map[string]map[string]*gPlot // street -> plot id -> touched plot
	holding map[string]bool              // street|id: a plant or Guano waiting for the ledger
	limits  map[int64]*objBucket
	uses    *useAsker
	grants  []gardenGrant
	boot    string
	seq     uint64

	waterMs, lockMs int64 // the rules' (GRODDLE_GARDENS_DEV may shorten them)
	dev             bool

	timerStop func() bool
	timerAt   int64

	path    string
	saveDue bool
	resumed bool
	savedAt int64 // when the file read at start was written (0: none)
}

func newGardens(h *Hub) Module {
	var b [4]byte
	rand.Read(b[:])
	g := &gardens{pace: newOwedPacer(h, nil), h: h, w: gardenWorldData, plots: map[string]map[string]*gPlot{}, holding: map[string]bool{}, limits: map[int64]*objBucket{},
		boot: hex.EncodeToString(b[:]), waterMs: gardenWorldData.waterMs, lockMs: gardenWorldData.lockMs, resumed: true}
	if testing.Testing() {
		g.off = gardensOffForTest
	} else {
		g.off = *gardensFlag == "off"
		g.dev = os.Getenv("GRODDLE_GARDENS_DEV") == "1"
	}
	if g.off {
		log.Printf("gardens: off (pages keep their own plots)")
		return g
	}
	g.uses = newUseAsker(h, "gardens: seeds and Guano")
	if g.path = statePath(gardensFileForTest, gardensFileFlag, "realtime-gardens.json"); g.path != "" {
		if err := g.load(); err != nil {
			log.Printf("gardens %s: %v (the gardens are kept in memory only)", g.path, err)
			g.path = ""
		}
	}
	return g
}

func (g *gardens) Register(r *Registry) {
	if g.off {
		return
	}
	r.Handle("garden_sync", g.handleSync)
	r.Handle("garden_act", g.handleAct)
	r.Handle("garden_ack", g.handleAck)
	r.Handle("garden_dev", g.handleDev)
	r.Handle("garden_potion", g.handlePotion)
	r.OnConnect(func(cl *Client) {
		g.resume()
		g.resendGrants(cl)
	})
	r.OnDisconnect(func(cl *Client) {
		delete(g.limits, cl.ID())
		g.pace.drop(cl)
	})
	r.OnShutdown(func() { g.save() })
}

func (g *gardens) nowMs() int64 { return g.h.Now().UnixMilli() }

func (g *gardens) allow(cl *Client) bool {
	now := g.h.Now()
	b := g.limits[cl.ID()]
	if b == nil {
		b = &objBucket{tokens: objBurst, at: now}
		g.limits[cl.ID()] = b
	}
	b.tokens = math.Min(objBurst, b.tokens+now.Sub(b.at).Seconds()*objEvery)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// plot is the live plot an id names (a fresh one, kept, if nobody touched it), brought to now.
func (g *gardens) plot(street, id string, now int64) *gPlot {
	room := g.plots[street]
	if room == nil {
		room = map[string]*gPlot{}
		g.plots[street] = room
	}
	p := room[id]
	if p == nil {
		p = freshPlot()
		room[id] = p
	}
	g.advance(p, now)
	return p
}

// forget drops the plots of a street that are fresh again.
func (g *gardens) forget(street string) {
	room := g.plots[street]
	for id, p := range room {
		if p.fresh() && !g.holding[street+"|"+id] {
			delete(room, id)
		}
	}
	if len(room) == 0 {
		delete(g.plots, street)
	}
}

func (g *gardens) views(street string, ids []string, now int64) map[string]any {
	out := make(map[string]any, len(ids))
	for _, id := range ids {
		out[id] = g.view(g.plots[street][id], now)
	}
	return out
}

func (g *gardens) broadcast(street string, ids []string, except int64) {
	if len(ids) == 0 {
		return
	}
	now := g.nowMs()
	g.h.BroadcastStreetLocked(street, map[string]any{"t": "gardens", "street": street, "now": now, "plots": g.views(street, ids, now)}, except)
}

// ---------------------------------------------------------------------------
// Time: one timer for the soonest plot (include/garden.js 886-929 keeps one too)
// ---------------------------------------------------------------------------

func (g *gardens) arm() {
	var next int64
	for _, room := range g.plots {
		for _, p := range room {
			if d := g.due(p); d > 0 && (next == 0 || d < next) {
				next = d
			}
		}
	}
	if next == g.timerAt && g.timerStop != nil {
		return
	}
	if g.timerStop != nil {
		g.timerStop()
		g.timerStop = nil
	}
	g.timerAt = next
	if next == 0 {
		return
	}
	wait := time.Duration(max(0, next-g.nowMs())) * time.Millisecond
	g.timerStop = g.h.After(wait, g.tick)
}

func (g *gardens) tick() {
	g.timerStop, g.timerAt = nil, 0
	now := g.nowMs()
	touched := false
	for street, room := range g.plots {
		var changed []string
		for id, p := range room {
			if g.advance(p, now) {
				changed = append(changed, id)
			}
		}
		if len(changed) > 0 {
			sort.Strings(changed)
			g.broadcast(street, changed, 0)
			g.forget(street)
			touched = true
		}
	}
	if touched {
		g.saveSoon()
	}
	g.arm()
}

// resume (the first connect after a start) brings every plot to now and arms the timer. Ours: a
// crop whose planter's 60 seconds ran or began while the server was down starts them again.
func (g *gardens) resume() {
	if g.resumed {
		return
	}
	g.resumed = true
	now := g.nowMs()
	for street, room := range g.plots {
		for _, p := range room {
			planter := p.planter
			g.advance(p, now)
			if p.state == "crop" && planter != 0 && g.savedAt > 0 && p.ripeAt+g.lockMs > g.savedAt {
				p.planter, p.ripeAt = planter, now
			}
		}
		g.forget(street)
	}
	g.arm()
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

func (g *gardens) handleSync(cl *Client, frame json.RawMessage) {
	street := cl.Street()
	if street == "" || !g.allow(cl) {
		return
	}
	beds := g.w.streets[street]
	if beds == nil {
		g.h.SendLocked(cl, map[string]any{"t": "gardens", "street": street, "snap": true, "none": true, "plots": map[string]any{}})
		return
	}
	g.resume()
	now := g.nowMs()
	ids := make([]string, 0, len(g.plots[street]))
	for id, p := range g.plots[street] {
		g.advance(p, now)
		ids = append(ids, id)
	}
	sort.Strings(ids)
	shape := map[string][]int{}
	for name, b := range beds {
		shape[name] = []int{b.cols, b.rows}
	}
	g.h.SendLocked(cl, map[string]any{"t": "gardens", "street": street, "snap": true, "now": now, "lock": g.lockMs, "water": g.waterMs, "beds": shape, "plots": g.views(street, ids, now)})
}

type gardenAct struct {
	Req   int64    `json:"req"`
	ID    string   `json:"id"`
	Bed   string   `json:"bed"`
	Verb  string   `json:"verb"`
	Seed  string   `json:"seed"`
	Mod   float64  `json:"mod"`
	Perks []string `json:"perks"`
	IDs   []string `json:"ids"`
	Tool  string   `json:"tool"` // water, clear: the can or hoe the page used, seen over its head by the others
	Ms    int64    `json:"ms"`   // how long that overlay lasts
}

// gardenReply answers one act.
type gardenReply struct {
	g      *gardens
	cl     *Client
	m      gardenAct
	street string
	ids    []string       // the plots it is about
	before map[string]any // their states before
}

func (rp *gardenReply) no(code, message string) {
	if !rp.cl.Online() {
		return
	}
	now := rp.g.nowMs()
	msg := map[string]any{"t": "garden_no", "req": rp.m.Req, "id": rp.m.ID, "verb": rp.m.Verb, "code": code, "message": message, "now": now}
	if rp.street != "" && len(rp.ids) > 0 {
		msg["plots"] = rp.g.views(rp.street, rp.ids, now)
	}
	rp.g.h.SendLocked(rp.cl, msg)
	rp.g.forget(rp.street)
}

// ok answers the act, tells the street what changed and who tended, and keeps the change.
func (rp *gardenReply) ok(changed []string, extra map[string]any, now bool) {
	g := rp.g
	t := g.nowMs()
	if rp.cl.Online() {
		msg := map[string]any{"t": "garden_ok", "req": rp.m.Req, "id": rp.m.ID, "verb": rp.m.Verb, "now": t, "before": rp.before, "plots": g.views(rp.street, rp.ids, t)}
		if rp.m.Bed != "" {
			msg["bed"] = rp.m.Bed
		}
		for k, v := range extra {
			msg[k] = v
		}
		g.h.SendLocked(rp.cl, msg)
	}
	g.broadcast(rp.street, changed, rp.cl.ID())
	g.h.tallyLocked("garden:"+rp.m.Verb, 1) // the Analytics tab's world tallies (tally.go)
	// The others see who tended (include/garden.js 708-722: the tool over the tender's head).
	ev := map[string]any{"t": "garden_ev", "street": rp.street, "by": rp.cl.ID(), "verb": rp.m.Verb, "ids": changed}
	if tool := overlayTool(rp.m); tool != "" {
		ev["tool"], ev["ms"] = tool, overlayMs(rp.m.Ms)
	}
	g.h.BroadcastStreetLocked(rp.street, ev, rp.cl.ID())
	g.forget(rp.street)
	if now {
		g.save()
	} else {
		g.saveSoon()
	}
	g.arm()
}

func (g *gardens) handleAct(cl *Client, frame json.RawMessage) {
	var m gardenAct
	if json.Unmarshal(frame, &m) != nil || m.Req < 1 || m.Req > 1<<31 || len(m.Perks) > 4 || len(m.IDs) > 12 || len(m.Seed) > 64 {
		g.h.ErrorLocked(cl, "bad_garden", "That garden message did not check out.")
		return
	}
	rp := &gardenReply{g: g, cl: cl, m: m}
	if !g.allow(cl) {
		rp.no("slow_down", "Slow down a little.")
		return
	}
	street := cl.Street()
	if street == "" || g.w.streets[street] == nil || !gardenVerbs[m.Verb] {
		rp.no("unknown", "That is not something here.")
		return
	}
	g.resume()
	rp.street = street
	now := g.nowMs()
	if g.bedHeld(street, m) {
		rp.no("busy", gardenBusy)
		return
	}
	var bed *gardenBed
	if m.Verb == "water_all" {
		bed = g.w.streets[street][m.Bed]
		if bed == nil || len(m.IDs) == 0 {
			rp.no("unknown", "That is not something here.")
			return
		}
		seen := map[string]bool{}
		for _, id := range m.IDs {
			b, _ := g.w.bedOf(street, id)
			if b != bed || seen[id] {
				rp.no("unknown", "That is not something here.")
				return
			}
			seen[id] = true
			rp.ids = append(rp.ids, id)
		}
	} else {
		b, _ := g.w.bedOf(street, m.ID)
		if b == nil {
			rp.no("unknown", "That is not something here.")
			return
		}
		bed = b
		rp.ids = []string{m.ID}
	}
	if x, y := cl.Pos(); math.Abs(x-bed.x) > objReachX || math.Abs(y-bed.y) > objReachY {
		rp.no("far", "You need to be closer.")
		return
	}
	for _, id := range rp.ids {
		g.plot(street, id, now)
	}
	rp.before = g.views(street, rp.ids, now)
	perks := map[string]bool{}
	for _, p := range m.Perks {
		perks[p] = true
	}
	switch m.Verb {
	case "plant":
		g.plant(rp, bed)
	case "water", "water_all":
		g.withTool(rp, "can", func() { g.water(rp) })
	case "harvest":
		g.harvest(rp, perks["full"])
	case "clear":
		g.withTool(rp, "hoe", func() { g.clear(rp) })
	case "fertilize":
		g.fertilize(rp)
	}
}

// withTool runs act once the site's ledger says the player holds the tool (objects_tools.go, the
// same answer the street's rocks and trees use), or at once without that module or ledger.
func (g *gardens) withTool(rp *gardenReply, group string, act func()) {
	fn, ok := g.h.ProvidedLocked("objects.withTool").(func(*Client, string, string, func(string, string), func(bool)))
	if !ok {
		act()
		return
	}
	fn(rp.cl, rp.street, group, func(code, message string) { rp.no(code, message) }, func(bool) {
		// The tool check may have waited: the plots are read again.
		now := g.nowMs()
		for _, id := range rp.ids {
			g.plot(rp.street, id, now)
		}
		rp.before = g.views(rp.street, rp.ids, now)
		act()
	})
}

// held runs a ledger use for one plot with the plot held, so nobody else plants or Guanos it meanwhile.
func (g *gardens) held(rp *gardenReply, class string, lacks string, then func(use map[string]any)) {
	key := rp.street + "|" + rp.m.ID
	if g.holding[key] {
		rp.no("busy", gardenBusy)
		return
	}
	g.holding[key] = true
	g.uses.ask(rp.cl, class, 1, func(use map[string]any, code, message string) {
		delete(g.holding, key)
		if code != "" {
			if code == "no_item" {
				message = lacks
			}
			rp.no(code, message)
			return
		}
		now := g.nowMs()
		g.plot(rp.street, rp.m.ID, now)
		rp.before = g.views(rp.street, rp.ids, now)
		then(use)
	})
}

func (g *gardens) plant(rp *gardenReply, bed *gardenBed) {
	p := g.plots[rp.street][rp.m.ID]
	if p.state != "clean" {
		rp.no("not_clean", gardenNotClean)
		return
	}
	seed, ok := g.w.seeds[rp.m.Seed]
	if !ok || seed.bed != bed.bed {
		line := gardenCropOnly
		if bed.bed == "herb" {
			line = gardenHerbOnly
		}
		rp.no("wrong_seed", line)
		return
	}
	mod := rp.m.Mod
	if !(mod >= 0.5 && mod <= 1) {
		mod = 1
	}
	g.held(rp, rp.m.Seed, gardenNoSeed, func(use map[string]any) {
		p := g.plots[rp.street][rp.m.ID]
		if p.state != "clean" {
			// Held, so nothing else can have planted it; logged, and the seed stays used as the ledger took it.
			log.Printf("gardens: %s %s planted by %d was no longer clean (%s)", rp.street, rp.m.ID, rp.cl.ID(), p.state)
			rp.no("not_clean", gardenNotClean)
			return
		}
		now := g.nowMs()
		// include/garden.js plant: the seed, its grow modifier and the planter (public gardens only).
		g.sow(p, rp.m.Seed, mod, rp.cl.ID(), now)
		log.Printf("gardens: %d planted %s in %s %s", rp.cl.ID(), rp.m.Seed, rp.street, rp.m.ID)
		var extra map[string]any
		if use != nil {
			extra = map[string]any{"use": use}
		}
		rp.ok(rp.ids, extra, true)
	})
}

func (g *gardens) water(rp *gardenReply) {
	now := g.nowMs()
	var changed []string
	for _, id := range rp.ids {
		p := g.plots[rp.street][id]
		if p == nil || p.wet {
			continue
		}
		// js/garden.js wetPlot: a dry plot's clock restarts now; a watering lasts waterMs.
		g.wetNow(p, now)
		changed = append(changed, id)
	}
	if len(changed) == 0 {
		line := gardenWet
		if rp.m.Verb == "water_all" {
			line = gardenNoDry
		}
		rp.no("wet", line)
		return
	}
	rp.ok(changed, map[string]any{"watered": len(changed)}, false)
}

func (g *gardens) harvest(rp *gardenReply, full bool) {
	now := g.nowMs()
	p := g.plots[rp.street][rp.m.ID]
	seed, known := g.w.seeds[p.seed]
	if p.state != "crop" || !known {
		rp.no("nothing", gardenNothingPick)
		return
	}
	if g.locked(p, rp.cl.ID(), now) {
		rp.no("locked", gardenLockLine)
		return
	}
	n := seed.produces
	if !full {
		n = int(math.Round(float64(n) / 2))
	}
	// The pick (include/garden.js 465-472): the plot is dirty, its fertilizer and planter gone.
	p.state, p.seed, p.fert, p.planter, p.ripeAt, p.stageAt, p.banked, p.pace = "dirty", "", false, 0, 0, now, 0, 0
	gr := g.grant(rp.cl.ID(), seed.crop, n)
	creditLocked(g.h, rp.cl.ID(), "take", creditItem(seed.crop, n))
	log.Printf("gardens: %d picked %d %s in %s %s (%s)", rp.cl.ID(), n, seed.crop, rp.street, rp.m.ID, gr.ID)
	rp.ok(rp.ids, map[string]any{"grant": map[string]any{"id": gr.ID, "c": gr.C, "n": gr.N}}, true)
}

func (g *gardens) clear(rp *gardenReply) {
	p := g.plots[rp.street][rp.m.ID]
	if p == nil || p.state != "dirty" {
		rp.no("not_dirty", gardenNoHoeing)
		return
	}
	p.state, p.seed, p.stageAt, p.banked = "clean", "", g.nowMs(), 0
	rp.ok(rp.ids, nil, false)
}

// fertCheck is GardenPlotView.as 152-168: fertilized, growing, then watered enough. "" when it takes Guano.
func (g *gardens) fertCheck(p *gPlot, now int64) (code, line string) {
	if _, known := g.w.seeds[p.seed]; !known || p.fert || !p.growing() {
		return "nothing", gardenFertNothing
	}
	left := 0.0
	if p.wet {
		left = math.Max(0, math.Min(1, 1-float64(now-p.wetAt)/float64(g.waterOf(p))))
	}
	if left < g.w.fertilize {
		if left > 0 {
			return "water", gardenFertMore
		}
		return "water", gardenFertDry
	}
	return "", ""
}

func (g *gardens) fertilize(rp *gardenReply) {
	p := g.plots[rp.street][rp.m.ID]
	if code, line := g.fertCheck(p, g.nowMs()); code != "" {
		rp.no(code, line)
		return
	}
	g.held(rp, gardenGuano, gardenFertNoGuano, func(use map[string]any) {
		p := g.plots[rp.street][rp.m.ID]
		now := g.nowMs()
		if code, line := g.fertCheck(p, now); code != "" {
			log.Printf("gardens: Guano by %d on %s %s was paid for, but the plot changed first (%s)", rp.cl.ID(), rp.street, rp.m.ID, code)
			rp.no(code, line)
			return
		}
		g.applyGuano(p, now)
		var extra map[string]any
		if use != nil {
			extra = map[string]any{"use": use}
		}
		rp.ok(rp.ids, extra, true)
	})
}

// applyGuano is include/garden.js 625-639 (js/garden.js applyFertilizer): seeds go to shoots, shoots
// keep a tenth of what is left of their grow and are marked fertilized.
func (g *gardens) applyGuano(p *gPlot, now int64) {
	if p.state == "seeds" {
		p.state, p.stageAt, p.banked = "shoots", now, 0
		return
	}
	need := g.need(p)
	left := max(0, need-(p.banked+max(0, now-p.stageAt)))
	p.stageAt = now - (need - int64(math.Round(0.1*float64(left))))
	p.banked, p.fert = 0, true
}

// ---------------------------------------------------------------------------
// Grants: a pick's crop until the page has saved it
// ---------------------------------------------------------------------------

func (g *gardens) grant(user int64, crop string, n int) gardenGrant {
	g.seq++
	gr := gardenGrant{ID: "gg-" + g.boot + "-" + strconv.FormatUint(g.seq, 36), User: user, C: crop, N: n, At: g.nowMs()}
	g.grants = append(g.grants, gr)
	g.trimGrants()
	return gr
}

// trimGrants drops grants older than gardenGrantKeep, and a player's oldest past gardenGrantsPerOne.
func (g *gardens) trimGrants() {
	cut := g.nowMs() - gardenGrantKeep.Milliseconds()
	per := map[int64]int{}
	for i := len(g.grants) - 1; i >= 0; i-- {
		per[g.grants[i].User]++
	}
	kept := g.grants[:0]
	for _, gr := range g.grants {
		if gr.At < cut {
			log.Printf("gardens: %s for %d was never acknowledged; dropped", gr.ID, gr.User)
			per[gr.User]--
			continue
		}
		if per[gr.User] > gardenGrantsPerOne {
			per[gr.User]--
			continue
		}
		kept = append(kept, gr)
	}
	g.grants = kept
}

func (g *gardens) resendGrants(cl *Client) {
	// The grants made before this connect, owedBatch at a time (owed_pace.go): one made from now
	// on goes with its own answer, as before, so the window never sends it a second time.
	id, upTo := cl.ID(), g.nowMs()
	g.pace.start(cl, func() []owedItem {
		var out []owedItem
		for _, gr := range g.grants {
			if gr.User == id && gr.At <= upTo {
				b, _ := json.Marshal(map[string]any{"t": "garden_grant", "id": gr.ID, "c": gr.C, "n": gr.N})
				out = append(out, owedItem{ID: gr.ID, Msg: b})
			}
		}
		return out
	})
}

func (g *gardens) handleAck(cl *Client, frame json.RawMessage) {
	var m struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(frame, &m) != nil || !strings.HasPrefix(m.ID, "gg-") || len(m.ID) > 40 {
		return
	}
	defer g.pace.acked(cl, m.ID) // after it is gone: the next resend takes its slot
	for i, gr := range g.grants {
		if gr.ID == m.ID && gr.User == cl.ID() {
			g.grants = append(g.grants[:i], g.grants[i+1:]...)
			g.save()
			return
		}
	}
}

// handleDev (GRODDLE_GARDENS_DEV=1 only): a shorter lock and drying for QA, since the online page
// strips its query parameters; and the garden's QA clock: "skip" moves every plot's stage and
// watering back by that many ms, as if each had been planted and watered that much earlier, and the
// plots grow and dry by the rules at once (crops take minutes to an hour, so a check lets the time
// pass instead of waiting; the game's solo ?gardenqa=1 does the same).
func (g *gardens) handleDev(cl *Client, frame json.RawMessage) {
	if !g.dev {
		g.h.ErrorLocked(cl, "bad_frame", "Unknown message.")
		return
	}
	var m struct {
		Lock  int64 `json:"lock"`
		Water int64 `json:"water"`
		Skip  int64 `json:"skip"`
	}
	if json.Unmarshal(frame, &m) != nil {
		return
	}
	if m.Skip > 0 && m.Skip <= 24*3_600_000 {
		for _, room := range g.plots {
			for _, p := range room {
				p.stageAt -= m.Skip
				if p.wet {
					p.wetAt -= m.Skip
				}
				if p.ripeAt > 0 {
					p.ripeAt -= m.Skip // the planter's lock runs on too
				}
			}
		}
		log.Printf("gardens: dev skip %d ms (by %d)", m.Skip, cl.ID())
		g.tick()
		return
	}
	if m.Lock >= 1000 {
		g.lockMs = m.Lock
	}
	if m.Water >= 1000 {
		g.waterMs = m.Water
	}
	log.Printf("gardens: dev lock %d ms, water %d ms (by %d)", g.lockMs, g.waterMs, cl.ID())
	g.arm()
}
