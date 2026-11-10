package main

// Items dropped on a street (register G332): in the original a stack a player
// dropped lay in the location for everyone there, and whoever picked it up
// first had it.
//
//   - Drop (GameServerJS items/include/takeable.js takeable_drop 157-213): the
//     stack leaves the pack and lands at the dropper's feet, 20 px above
//     (166-167), or where the client put it; quest and soulbound items stay
//     (takeable_drop_conditions 104-135: "This item is locked to you and
//     can't be dropped.", 118). The stack remembers who dropped it and when
//     (takeable_drop_lock 215-218).
//   - Pick up (takeable_pickup_conditions 5-51, takeable_pickup 53-102):
//     anyone in the location may, except while the dropper is still there
//     within 100 px of the stack (29-44: "You are not allowed to pick this
//     up."; measured across here, as the game lifts a stack 74 px over the
//     feet where the source lifted it 20); once the dropper has walked away the mark is gone for good
//     (41-42). The dropper may always pick their own back up. A verb on a
//     location stack needs the player within config.verb_radius, 800 px
//     (config_prod.js:37), or "Oops. The <name> got too far away for you to
//     pick up. Get closer and then try again." (main.js 2970-2979).
//   - A street Piggy eats dropped food (GameServerJS items/npc_piggy.js onContainerItemAdded
//     1393-1411, onTwiceDaily 1879-1990, onFoodPathing 1482-1500: apiConsume(1) of an is_food
//     stack, include/food.js:1): objects_pigs.go decides which Piggy and when; here one of the
//     stack goes, through the site (POST groddle/v1/ledger/drop/eat: the stack is in nobody's
//     ledger, so only its count changes), and everyone on the street hears it.
//   - Nothing in the source ever clears a street of dropped stacks:
//     location.js deleteTakeables (4619-4626) is called from nowhere, and
//     dropped_on is only ever cleared by a pickup (takeable.js 41-42, 83).
//
// Here: on a public street (no "_" in its id) and on a player's home street
// (HOME_<owner>, below); house rooms, Tower floors, party spaces, races and
// arenas are private places, where each player's drops stay their own as in
// solo. Nothing moves on the client's word. A drop is
// asked of the site's ledger (groddle-afternoon-site src/LedgerDrop.php,
// POST groddle/v1/ledger/drop, signed as ledger.go signs): the stack leaves
// the dropper's ledger and waits in the site's groddle_drops, in nobody's
// ledger, until a pickup (POST groddle/v1/ledger/drop/take) puts it in the
// picker's; each is recorded once per op, so asking again answers as
// recorded. The site's rows are the truth: this module loads the stacks on
// the ground from the site when it starts (POST groddle/v1/ledger/drops),
// so a restart loses nothing and duplicates nothing. Each game applies an
// op once through the trade slice's applied list; a game that missed the
// answer applies it on its next connect (GET groddle/v1/drops) or load.
//
// Client to server:
//
//	{"t":"ground_drop","ref":"…","item":{"c":"apple","n":3,"r":40},"x":1200,"y":-120}
//	{"t":"ground_pickup","id":"drop-…","n":3}          n: how many (Pick up how many?); none or the whole count takes the stack
//	on a home street both also carry "ox":640, the house's x as this game draws it (below, "A home street")
//
// Server to client:
//
//	{"t":"ground_state","street":"LA5…","stacks":[Stack…]}             after each join (empty in a private place)
//	{"t":"ground_added","stack":Stack}                                   to the street, the dropper too
//	{"t":"ground_dropped","op":"drop-…","ref":"…","status":"settled"|"refused","why":"…","text":"…","stack":Stack|null}
//	{"t":"ground_taken","op":"drop-…-t","id":"drop-…","status":"settled"|"refused","why":"…","text":"…","item":{…}|null}
//	{"t":"ground_gone","id":"drop-…","by":{"id":9,"name":"Bo"}|null,"why":"picked"|"returned"}
//	{"t":"ground_left","street":"LA5…","id":"drop-…","n":7,"by":{"id":9,"name":"Bo"}|null}   some were picked up (n: left); by null: the count corrected
//	{"t":"ground_back","op":"drop-…-t","id":"drop-…","item":{…}}       to a dropper whose stack came home
//	{"t":"ground_eaten","street":"LA5…","id":"drop-…","n":2,"pig":"pig:1","x":1200,"y":-120}   a Piggy ate one (n: left; 0 is followed by ground_gone why "eaten")
//
//	Stack: {"id":"drop-…","c":"apple","n":3,"r":40?,"x":1200,"y":-120,"by":{"id":7,"name":"Ada"},"at":1730000000000}
//
// Ours: a class the ledger does not carry is not shared (the game drops it as
// its own, as in solo); stacks do not merge (each drop is its own stack with
// its own dropper); a street holds at most dropStreetMax shared stacks; a
// stack nobody picks up for dropKeep goes back to the dropper's ledger and
// pack (the source kept it forever, but nothing here should grow without
// end); one drop and one pickup at a time per player; the picker's pack is
// not asked whether it has room: the client asks only for what fits
// (takeable.js 84-93 addItemStack's remainder stays on the ground).
//
// Placed by the street (G605): a Plop a fed street Piggy leaves (npc_animal doPlop 1315-1350:
// apiPutItemIntoPosition beside the Piggy) and the Piglet a Potion of Animal Youth leaves where its
// Piggy stood (potion_animal_youth.js onPour 547-569, replaceWithPoofIfOnGround) come from no one's
// pack, so the objects module places them (Provide "drops.place"): the site records the stack under
// the player whose act made it (POST groddle/v1/ledger/drop/place: no ledger gives it up; the site
// counts it as an outcome of that act), and from there it is any stack: the 100 px guard is that
// player's (ours, Austin 2026-11-09: the feeder gets first chance; the source's plop had no dropper),
// the first pickup has it, and one left for dropKeep goes back to that player.
//
// A home street (report #173, Austin 2026-11-10: "visitors' and owners' drops are seen by everyone
// there, as on public streets"): in the original a home street was a location like any other, so
// takeable_drop and takeable_pickup ran there unchanged, the dropper's 100 px guard included (the
// one home rule, house_lock at takeable.js 34-36 and 179-198, is for what the owner sets on
// furniture inside the house or Tower, not for the street). So HOME_<owner> is shared for the owner
// and every visitor homes.go let onto it, with the same rules and the same ledger calls. Ours:
//   - The site keeps a street as letters, digits and "-" (LedgerDrop), so a home street goes to it
//     as HOME-<owner> and comes back as HOME_<owner> (dropSiteStreet, dropStreetFromSite).
//   - A stack's x is kept from the house, not from the street's left end: the remake's home street
//     grows to the left and moves the house's x with it (the source's house sat at x 0 and only
//     the left edge moved, inc_homes.js homes_yard_rebuild 2309-2335). Each game sends the house's
//     x it draws ("ox") with a drop and a pickup; positions are compared with it taken off.
//   - The owner's own stacks on their own home street never go back to the pack after dropKeep
//     (it is their street; dropStreetMax still bounds it). A visitor's do, as anywhere.
//   - The house, its back yard and the Tower's floors stay each player's own for now.
//
// Pick up how many (G560): a pickup may name a part of the stack (main.js
// 3227-3240 splits the stack, takeable_pickup takes the split part and the
// rest stays down for everyone). The site records each part once under
// <id>-t<from>, from being the count this module holds (a stack's count
// only falls), so a retried ask answers as recorded and a count that is no
// longer there is refused as stale with the count there is; the street hears
// the new count (ground_left). The whole stack (or a part that is all of it)
// goes as before under <id>-t.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"testing"
	"time"
)

const (
	ledgerRouteDrop     = "/groddle/v1/ledger/drop"
	ledgerRouteDropTake = "/groddle/v1/ledger/drop/take"
	ledgerRouteDrops    = "/groddle/v1/ledger/drops"
	ledgerRouteDropEat  = "/groddle/v1/ledger/drop/eat"
	ledgerRouteDropPut  = "/groddle/v1/ledger/drop/place"

	dropLockRadius = 100.0 // takeable.js:38 distanceFromPlayer(dropper) < 100, measured across (dropLockHeight)
	dropLockHeight = 200.0 // ours: the game draws a stack 74 px above the feet (game.js dropPackItem), the source 20
	dropReach      = 800.0 // config_prod.js:37 verb_radius (main.js:2970)
	dropPlaceNear  = 300.0 // ours: a stack lands within this of the dropper (main.js:2944 required_range)
	dropLift       = 20.0  // takeable.js:167 pc.y-20
	dropStreetMax  = 60    // ours
	dropKeep       = 7 * 24 * time.Hour
	dropSweepEvery = 10 * time.Minute
	dropLoadRetry  = 15 * time.Second
	dropBurst      = 6
	dropEvery      = time.Second

	dropTextSoulbound = "This item is locked to you and can't be dropped."                                 // takeable.js:118
	dropTextCant      = "That can't be dropped for others to find."                                        // ours: the ledger does not carry it
	dropTextPrivate   = "Only a drop on a public street or a home street is shared."                       // ours
	dropTextFull      = "This street can't hold any more dropped things."                                  // ours
	dropTextShort     = "You don't have enough of that item."                                              // inc_trading.js:456
	dropTextBusy      = "Wait a moment, you are still dropping something."                                 // ours
	dropTextOff       = "You can't drop things for others right now."                                      // ours: no ledger to move them
	dropTextBad       = "Drop one stack."                                                                  // ours
	pickTextLocked    = "You are not allowed to pick this up."                                             // takeable.js:39
	pickTextGone      = "That stack has already been picked up."                                           // the game's own (game.js pickupDroppedWorldItem)
	pickTextFar       = "Oops. The %s got too far away for you to pick up. Get closer and then try again." // main.js:2978
	pickTextBusy      = "Someone is already picking that up."                                              // ours
	pickTextOff       = "You can't pick that up right now."                                                // ours
	pickTextStale     = "That stack changed. Have another look."                                           // ours: a count no longer there
)

var dropOpRe = regexp.MustCompile(`^drop-[0-9a-f]{16}$`)

// A home street as this server names it and as the site keeps it (the header, "A home street").
var (
	dropHomeRe     = regexp.MustCompile(`^HOME_([1-9][0-9]{0,17})$`)
	dropHomeSiteRe = regexp.MustCompile(`^HOME-([1-9][0-9]{0,17})$`)
)

// dropHomeOwner is whose home street this is, or 0 for any other place.
func dropHomeOwner(street string) int64 {
	m := dropHomeRe.FindStringSubmatch(street)
	if m == nil {
		return 0
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	return id
}

// dropSiteStreet is a street as the site is told it.
func dropSiteStreet(street string) string {
	if dropHomeOwner(street) > 0 {
		return "HOME-" + street[len(homeStreetPrefix):]
	}
	return street
}

// dropStreetFromSite is a street the site names, as players join it.
func dropStreetFromSite(street string) string {
	if dropHomeSiteRe.MatchString(street) {
		return homeStreetPrefix + street[len("HOME-"):]
	}
	return street
}

// dropOrigin is the x a game says its positions on this street start from: the house's x on a
// home street, nothing anywhere else.
func dropOrigin(street string, ox *float64) float64 {
	if ox == nil || dropHomeOwner(street) == 0 || math.IsNaN(*ox) || math.Abs(*ox) > 1e6 {
		return 0
	}
	return math.Round(*ox)
}

func init() { modules = append(modules, newDrops) }

// groundStack is one shared stack on a street, as the site holds it.
type groundStack struct {
	ID     string
	Street string
	C      string
	N      int
	R      *int
	T      json.RawMessage // one copy's own words (copytags.go), as the dropper's game sent them, checked
	X, Y   float64
	By     int64
	ByName string
	At     time.Time

	unlocked bool  // the dropper walked away once (takeable.js 41-42)
	picking  int64 // a pickup the site is weighing
	eating   bool  // a Piggy's bite the site is weighing
}

func (s *groundStack) view() map[string]any {
	out := map[string]any{"id": s.ID, "c": s.C, "n": s.N, "x": s.X, "y": s.Y, "by": map[string]any{"id": s.By, "name": s.ByName}, "at": s.At.UnixMilli()}
	if s.R != nil {
		out["r"] = *s.R
	}
	if len(s.T) > 0 {
		out["t"] = s.T
	}
	return out
}

func (s *groundStack) item() tradeItem { return tradeItem{C: s.C, N: s.N, R: s.R, T: s.T} }

// dropRow is a stack as the site sends it.
type dropRow struct {
	ID     string          `json:"id"`
	By     int64           `json:"by"`
	Name   string          `json:"name"`
	Street string          `json:"street"`
	X      float64         `json:"x"`
	Y      float64         `json:"y"`
	C      string          `json:"c"`
	N      int             `json:"n"`
	R      *int            `json:"r,omitempty"`
	T      json.RawMessage `json:"t,omitempty"`
	At     int64           `json:"at"` // unix seconds
}

// dropCall is one drop as the site is asked it.
type dropCall struct {
	Op     string    `json:"op"`
	ID     int64     `json:"id"`
	Street string    `json:"street"`
	X      int       `json:"x"`
	Y      int       `json:"y"`
	Item   tradeItem `json:"item"`
}

// dropAnswer is the site's word on a drop or a take. Status "" means no answer came.
type dropAnswer struct {
	Status string     `json:"status"` // settled | refused
	Why    string     `json:"why,omitempty"`
	Text   string     `json:"text,omitempty"`
	Op     string     `json:"op,omitempty"`   // a take: its own op
	By     int64      `json:"by,omitempty"`   // a take refused as gone: who has it
	Item   *tradeItem `json:"item,omitempty"` // a take: what it gave
	N      *int       `json:"n,omitempty"`    // a bite or a part taken (or refused as stale): how many are left
}

// dropSite is the site's ledger (httpLedger). Calls block; the module makes them off the hub lock.
type dropSite interface {
	drop(c dropCall) dropAnswer
	dropTake(op string, id int64, why string, n, from int) dropAnswer // n > 0: n of the from held (a part)
	dropsOnGround() ([]dropRow, error)
	dropEat(op string, from int) dropAnswer
	dropPlace(c dropCall) dropAnswer // G605: a stack the street makes (a Plop, a Piglet), under the player whose act made it
}

func (l *httpLedger) dropPost(route string, body []byte, out any) error {
	var nb [8]byte
	rand.Read(nb[:])
	req, err := http.NewRequest(http.MethodPost, l.base+"/wp-json"+route, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Groddle-Service", signService(l.secret, http.MethodPost, route, l.now().Unix(), hex.EncodeToString(nb[:]), body))
	res, err := l.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if (res.StatusCode == http.StatusOK || res.StatusCode == http.StatusConflict) && json.Unmarshal(raw, out) == nil {
		return nil
	}
	return fmt.Errorf("HTTP %d: %.200s", res.StatusCode, raw)
}

func (l *httpLedger) dropAsk(route string, body []byte) dropAnswer {
	for try := 0; try < ledgerTries; try++ {
		if try > 0 {
			l.pause(time.Duration(try) * 400 * time.Millisecond)
		}
		var a dropAnswer
		err := l.dropPost(route, body, &a)
		if err == nil && (a.Status == "settled" || a.Status == "refused") {
			return a
		}
		if err == nil {
			err = fmt.Errorf("status %q", a.Status)
		}
		log.Printf("drops: %s: %v", route, err)
	}
	return dropAnswer{}
}

func (l *httpLedger) drop(c dropCall) dropAnswer {
	body, _ := json.Marshal(c)
	return l.dropAsk(ledgerRouteDrop, body)
}

func (l *httpLedger) dropTake(op string, id int64, why string, n, from int) dropAnswer {
	call := map[string]any{"op": op, "id": id, "why": why}
	if n > 0 {
		// The same body on every try (dropAsk): the site records the part once as <op>-t<from>.
		call["n"], call["from"] = n, from
	}
	body, _ := json.Marshal(call)
	return l.dropAsk(ledgerRouteDropTake, body)
}

func (l *httpLedger) dropEat(op string, from int) dropAnswer {
	body, _ := json.Marshal(map[string]any{"op": op, "from": from})
	return l.dropAsk(ledgerRouteDropEat, body)
}

func (l *httpLedger) dropPlace(c dropCall) dropAnswer {
	body, _ := json.Marshal(c)
	return l.dropAsk(ledgerRouteDropPut, body)
}

func (l *httpLedger) dropsOnGround() ([]dropRow, error) {
	var out struct {
		Stacks []dropRow `json:"stacks"`
	}
	if err := l.dropPost(ledgerRouteDrops, []byte(`{}`), &out); err != nil {
		return nil, err
	}
	return out.Stacks, nil
}

type drops struct {
	h        *Hub
	table    tradeTable
	site     dropSite // nil: nothing is shared (no -ledger-url, or -trade-ledger off)
	spawn    func(func())
	newID    func() string
	loaded   bool
	loading  bool
	stacks   map[string]*groundStack
	byStreet map[string]map[string]*groundStack
	dropping map[int64]string // dropper -> the op the site is weighing
	picking  map[int64]string // picker -> the stack the site is weighing
	rates    map[int64]*bucket
	sweeping bool
}

func newDrops(h *Hub) Module {
	var table tradeTable
	if err := json.Unmarshal(tradeJSON, &table); err != nil || len(table.Items) == 0 {
		panic(fmt.Sprintf("realtime: data/trade.json: %v", err))
	}
	m := &drops{h: h, table: table, spawn: func(fn func()) { go fn() }, stacks: map[string]*groundStack{}, byStreet: map[string]map[string]*groundStack{},
		dropping: map[int64]string{}, picking: map[int64]string{}, rates: map[int64]*bucket{},
		newID: func() string {
			b := make([]byte, 8)
			rand.Read(b)
			return hex.EncodeToString(b)
		}}
	if !testing.Testing() {
		if site, _ := newLedgerFromFlags(); site != nil {
			if l, ok := site.(*httpLedger); ok {
				m.site = l
				m.load()
			}
		}
	}
	return m
}

func (m *drops) Register(r *Registry) {
	r.Handle("ground_drop", m.dropFrame)
	r.Handle("ground_pickup", m.pickupFrame)
	r.OnJoin(m.sendState)
	r.OnDisconnect(func(cl *Client) { delete(m.rates, cl.ID()) })
	// How many shared stacks lie on a street (for other modules and tests).
	r.Provide("drops.count", func(street string) int { return len(m.byStreet[street]) })
	// A street Piggy's food (objects_pigs.go goForFood) and its bite.
	r.Provide("drops.food", m.food)
	r.Provide("drops.eat", m.eat)
	// G605: a stack the street makes (objects_pigs_gone.go: a Plop, a Piglet).
	r.Provide("drops.place", m.place)
}

// place puts a stack the street made on the ground for everyone (see the header, "Placed by the
// street"), under the player by whose act made it: the site records it, then the street hears it as
// any drop. Off (no ledger, not loaded, a private place, a full street, a class the ledger does not
// carry): false, and nothing is placed. A home street makes no stacks of its own (its animals are
// the owner's game's), so only a public street is asked.
func (m *drops) place(street string, by int64, name string, x, y float64, it tradeItem) bool {
	cls, ok := m.table.Items[it.C]
	if m.site == nil || !m.loaded || followPrivate(street) || !ok || it.N < 1 || it.N > cls.Max || len(m.byStreet[street]) >= dropStreetMax {
		return false
	}
	m.table.cleanTag(&it, m.h.Now())
	call := dropCall{Op: "drop-" + m.newID(), ID: by, Street: dropSiteStreet(street), X: int(math.Round(x)), Y: int(math.Round(y)), Item: it}
	site := m.site
	m.spawn(func() {
		a := site.dropPlace(call)
		m.h.After(0, func() {
			if a.Status != "settled" {
				log.Printf("drops: placing %d %s on %s for %d: %q %s", it.N, it.C, street, by, a.Status, a.Why)
				return
			}
			s := m.addRow(dropRow{ID: call.Op, By: by, Name: name, Street: street, X: float64(call.X), Y: float64(call.Y), C: it.C, N: it.N, R: it.R, T: it.T, At: m.h.Now().Unix()})
			if s == nil {
				return
			}
			log.Printf("drops: the street placed %d %s on %s for %d (%s)", s.N, s.C, s.Street, by, s.ID)
			m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_added", "stack": s.view()}, 0)
			m.sweepSoon()
		})
	})
	return true
}

// food is the stack id names on street if it is food a Piggy at (x, y) can reach and nobody is
// taking, or with id "" the nearest such stack across; "" when there is none. It answers its x too.
func (m *drops) food(street, id string, x, y, reachX, reachY float64) (string, float64) {
	ok := func(s *groundStack) bool {
		return s != nil && s.Street == street && s.picking == 0 && !s.eating && m.table.Items[s.C].Food &&
			math.Abs(s.X-x) <= reachX && math.Abs(s.Y-y) <= reachY
	}
	if id != "" {
		if s := m.stacks[id]; ok(s) {
			return s.ID, s.X
		}
		return "", 0
	}
	var best *groundStack
	for _, s := range m.byStreet[street] {
		if !ok(s) {
			continue
		}
		if best == nil || math.Abs(s.X-x) < math.Abs(best.X-x) || (math.Abs(s.X-x) == math.Abs(best.X-x) && s.ID < best.ID) {
			best = s
		}
	}
	if best == nil {
		return "", 0
	}
	return best.ID, best.X
}

// eat is onFoodPathing's apiConsume(1) on a shared stack, for the Piggy pig: done(true) once one is gone.
func (m *drops) eat(id, pig string, done func(bool)) {
	s := m.stacks[id]
	if s == nil || m.site == nil || s.picking != 0 || s.eating || !m.table.Items[s.C].Food {
		done(false)
		return
	}
	s.eating = true
	from := s.N
	site := m.site
	m.spawn(func() {
		a := site.dropEat(id, from)
		m.h.After(0, func() {
			s.eating = false
			if a.Status != "settled" {
				done(false)
				return
			}
			left := from - 1
			if a.N != nil {
				left = *a.N
			}
			if m.stacks[id] == s {
				s.N = left
				if left <= 0 {
					m.remove(s)
				}
			}
			log.Printf("drops: %s on %s ate one %s from %s (%d left)", pig, s.Street, s.C, s.ID, max(0, left))
			m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_eaten", "street": s.Street, "id": s.ID, "n": max(0, left), "pig": pig, "x": s.X, "y": s.Y}, 0)
			if left <= 0 {
				m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_gone", "id": s.ID, "by": nil, "why": "eaten"}, 0)
			}
			done(true)
		})
	})
}

// dropShared: a public street (follow.go's rule for a private place) or a player's home street,
// where drops are everyone's.
func dropShared(street string) bool { return !followPrivate(street) || dropHomeOwner(street) > 0 }

// load asks the site for every stack on the ground, again until it answers.
func (m *drops) load() {
	if m.site == nil || m.loading || m.loaded {
		return
	}
	m.loading = true
	site := m.site
	m.spawn(func() {
		rows, err := site.dropsOnGround()
		m.h.After(0, func() {
			m.loading = false
			if err != nil {
				log.Printf("drops: loading the stacks on the ground: %v (again in %s)", err, dropLoadRetry)
				m.h.After(dropLoadRetry, m.load)
				return
			}
			m.loaded = true
			for _, row := range rows {
				m.addRow(row)
			}
			log.Printf("drops: %d stacks on the ground", len(m.stacks))
			// Players already on a street see what lies there.
			for street := range m.byStreet {
				m.h.BroadcastStreetLocked(street, m.stateMsg(street), 0)
			}
			m.sweepSoon()
		})
	})
}

func (m *drops) addRow(row dropRow) *groundStack {
	row.Street = dropStreetFromSite(row.Street)
	if !dropOpRe.MatchString(row.ID) || !dropShared(row.Street) || row.N < 1 || m.stacks[row.ID] != nil {
		return m.stacks[row.ID]
	}
	s := &groundStack{ID: row.ID, Street: row.Street, C: row.C, N: row.N, R: row.R, X: row.X, Y: row.Y, By: row.By, ByName: row.Name, At: time.Unix(row.At, 0)}
	if len(row.T) > 0 {
		// The site's stored words, checked again as any are (a stack that lay through a rules change).
		it := tradeItem{C: row.C, N: row.N, T: row.T}
		m.table.cleanTag(&it, m.h.Now())
		s.T = it.T
	}
	if s.ByName == "" {
		s.ByName = unnamedPlayer
	}
	m.stacks[s.ID] = s
	if m.byStreet[s.Street] == nil {
		m.byStreet[s.Street] = map[string]*groundStack{}
	}
	m.byStreet[s.Street][s.ID] = s
	return s
}

func (m *drops) remove(s *groundStack) {
	delete(m.stacks, s.ID)
	if room := m.byStreet[s.Street]; room != nil {
		delete(room, s.ID)
		if len(room) == 0 {
			delete(m.byStreet, s.Street)
		}
	}
}

func (m *drops) stateMsg(street string) map[string]any {
	list := []map[string]any{}
	if dropShared(street) {
		rows := make([]*groundStack, 0, len(m.byStreet[street]))
		for _, s := range m.byStreet[street] {
			rows = append(rows, s)
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].At.Before(rows[j].At) || (rows[i].At.Equal(rows[j].At) && rows[i].ID < rows[j].ID)
		})
		for _, s := range rows {
			list = append(list, s.view())
		}
	}
	return map[string]any{"t": "ground_state", "street": street, "stacks": list}
}

func (m *drops) sendState(cl *Client) {
	if m.site == nil {
		return
	}
	m.h.SendLocked(cl, m.stateMsg(cl.Street()))
}

func (m *drops) allow(cl *Client) bool {
	b := m.rates[cl.ID()]
	if b == nil {
		b = &bucket{}
		m.rates[cl.ID()] = b
	}
	if !b.take(m.h.Now(), dropBurst, dropEvery) {
		m.h.ErrorLocked(cl, "slow_down", "Slow down a little.")
		return false
	}
	return true
}

func (m *drops) refuseDrop(cl *Client, ref, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "ground_dropped", "ref": ref, "status": "refused", "why": why, "text": text, "stack": nil})
}

// dropFrame is takeable_drop on a public street: one stack, through the site's ledger.
func (m *drops) dropFrame(cl *Client, frame json.RawMessage) {
	if !m.allow(cl) {
		return
	}
	var msg struct {
		Ref  string     `json:"ref"`
		Item *tradeItem `json:"item"`
		X    *float64   `json:"x"`
		Y    *float64   `json:"y"`
		Ox   *float64   `json:"ox"`
	}
	if json.Unmarshal(frame, &msg) != nil || msg.Item == nil {
		m.refuseDrop(cl, "", "bad", dropTextBad)
		return
	}
	ref := ""
	if giftRefRe.MatchString(msg.Ref) {
		ref = msg.Ref
	}
	street := cl.Street()
	if !dropShared(street) {
		m.refuseDrop(cl, ref, "private", dropTextPrivate)
		return
	}
	it := *msg.Item
	cls, ok := m.table.Items[it.C]
	switch {
	case !ok && m.table.Untradeable[it.C] == "soulbound":
		m.refuseDrop(cl, ref, "soulbound", dropTextSoulbound)
		return
	case !ok:
		m.refuseDrop(cl, ref, "type", dropTextCant)
		return
	case it.N < 1 || it.N > cls.Max || it.N > tradeSlotMax:
		m.refuseDrop(cl, ref, "count", dropTextShort)
		return
	}
	if it.R != nil {
		if *it.R < 0 || *it.R > tradeMaxPoints {
			m.refuseDrop(cl, ref, "type", dropTextCant)
			return
		}
		if cls.Pts > 0 && *it.R > cls.Pts {
			r := cls.Pts
			it.R = &r
		}
	}
	m.table.cleanTag(&it, m.h.Now()) // the dropper's own copy: its words, checked (copytags.go)
	if m.site == nil || !m.loaded {
		m.refuseDrop(cl, ref, "off", dropTextOff)
		if m.site != nil {
			m.load()
		}
		return
	}
	if _, waiting := m.dropping[cl.ID()]; waiting {
		m.refuseDrop(cl, ref, "busy", dropTextBusy)
		return
	}
	if len(m.byStreet[street]) >= dropStreetMax {
		m.refuseDrop(cl, ref, "full", dropTextFull)
		return
	}
	// Where it lands: where the client put it, near the dropper; else at their feet (takeable.js 166-172).
	px, py := cl.Pos()
	x, y := px, py-dropLift
	if msg.X != nil && msg.Y != nil && !math.IsNaN(*msg.X) && !math.IsNaN(*msg.Y) && math.Hypot(*msg.X-px, *msg.Y-py) <= dropPlaceNear {
		x, y = *msg.X, *msg.Y
	}
	// On a home street the stack is kept from the house (the header, "A home street").
	x -= dropOrigin(street, msg.Ox)
	call := dropCall{Op: "drop-" + m.newID(), ID: cl.ID(), Street: dropSiteStreet(street), X: int(math.Round(x)), Y: int(math.Round(y)), Item: it}
	from, name := cl.ID(), cl.Name()
	m.dropping[from] = call.Op
	site := m.site
	m.spawn(func() {
		a := site.drop(call)
		m.h.After(0, func() {
			if m.dropping[from] == call.Op {
				delete(m.dropping, from)
			}
			if a.Status == "" {
				// No answer: the op is idempotent at the site; a settled one reaches the game on its next connect,
				// and the stack the next time the stacks are loaded.
				a.Status, a.Why, a.Text = "refused", "off", dropTextOff
			}
			out := map[string]any{"t": "ground_dropped", "op": call.Op, "ref": ref, "status": a.Status, "why": a.Why, "text": a.Text, "stack": nil}
			var s *groundStack
			if a.Status == "settled" {
				s = m.addRow(dropRow{ID: call.Op, By: from, Name: name, Street: street, X: float64(call.X), Y: float64(call.Y), C: it.C, N: it.N, R: it.R, T: it.T, At: m.h.Now().Unix()})
				if s != nil {
					out["stack"] = s.view()
				}
			}
			if c := m.h.ClientLocked(from); c != nil {
				m.h.SendLocked(c, out)
			}
			if s == nil {
				return
			}
			log.Printf("drops: %d dropped %d %s on %s (%s)", from, s.N, s.C, s.Street, s.ID)
			m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_added", "stack": s.view()}, 0)
			m.sweepSoon()
			// onContainerItemAdded: a hungry street Piggy goes for dropped food.
			if m.table.Items[s.C].Food && dropHomeOwner(s.Street) == 0 {
				if fn, ok := m.h.ProvidedLocked("objects.pigFood").(func(string, string, float64, float64)); ok {
					fn(s.Street, s.ID, s.X, s.Y)
				}
			}
		})
	})
}

func (m *drops) refusePick(cl *Client, id, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "ground_taken", "id": id, "status": "refused", "why": why, "text": text, "item": nil})
}

// pickupFrame is takeable_pickup of a shared stack: the first to ask has it.
func (m *drops) pickupFrame(cl *Client, frame json.RawMessage) {
	if !m.allow(cl) {
		return
	}
	var msg struct {
		ID string   `json:"id"`
		N  *int     `json:"n"`
		Ox *float64 `json:"ox"`
	}
	if json.Unmarshal(frame, &msg) != nil || !dropOpRe.MatchString(msg.ID) || (msg.N != nil && *msg.N < 1) {
		m.refusePick(cl, msg.ID, "bad", pickTextGone)
		return
	}
	s := m.stacks[msg.ID]
	if s == nil || s.Street != cl.Street() {
		m.refusePick(cl, msg.ID, "gone", pickTextGone)
		return
	}
	if m.site == nil {
		m.refusePick(cl, s.ID, "off", pickTextOff)
		return
	}
	// A part: fewer than the stack holds (asking for the whole count, or more, takes the stack).
	part := 0
	if msg.N != nil && *msg.N < s.N {
		part = *msg.N
	}
	// A part names the count it is taken from, so it waits for a Piggy's bite to land too.
	if s.picking != 0 || (part > 0 && s.eating) {
		m.refusePick(cl, s.ID, "busy", pickTextBusy)
		return
	}
	if _, waiting := m.picking[cl.ID()]; waiting {
		m.refusePick(cl, s.ID, "busy", pickTextBusy)
		return
	}
	// On a home street the stack's x is from the house: the picker's and the dropper's are read the same way.
	ox := dropOrigin(s.Street, msg.Ox)
	px, py := cl.Pos()
	px -= ox
	if math.Hypot(s.X-px, s.Y-py) > dropReach {
		name := s.C
		if c, ok := m.table.Items[s.C]; ok && c.Name != "" {
			name = c.Name
		}
		m.refusePick(cl, s.ID, "far", fmt.Sprintf(pickTextFar, name))
		return
	}
	if s.By != cl.ID() && !s.unlocked {
		if d := m.h.ClientLocked(s.By); d != nil && d.Street() == s.Street {
			if dx, dy := d.Pos(); math.Abs(s.X-(dx-ox)) < dropLockRadius && math.Abs(s.Y-dy) <= dropLockHeight {
				m.refusePick(cl, s.ID, "locked", pickTextLocked)
				return
			}
		}
		s.unlocked = true
	}
	if part > 0 {
		m.takePart(s, cl.ID(), cl.Name(), part)
		return
	}
	m.take(s, cl.ID(), cl.Name(), "pickup")
}

// takePart asks the site for n of a stack (fewer than it holds); the rest stays for everyone.
func (m *drops) takePart(s *groundStack, id int64, name string, n int) {
	s.picking = id
	m.picking[id] = s.ID
	from := s.N
	site := m.site
	m.spawn(func() {
		a := site.dropTake(s.ID, id, "pickup", n, from)
		m.h.After(0, func() {
			s.picking = 0
			if m.picking[id] == s.ID {
				delete(m.picking, id)
			}
			if a.Status == "" {
				a.Status, a.Why, a.Text = "refused", "off", pickTextOff
			}
			out := map[string]any{"t": "ground_taken", "op": a.Op, "id": s.ID, "status": a.Status, "why": a.Why, "text": a.Text, "item": nil}
			if a.Status == "settled" {
				it := m.takenItem(s, a.Item)
				if a.Item == nil {
					it.N = n
				}
				out["item"] = it
			} else if a.Text == "" {
				out["text"] = pickTextGone
				if a.Why == "stale" {
					out["text"] = pickTextStale
				}
			}
			if c := m.h.ClientLocked(id); c != nil {
				m.h.SendLocked(c, out)
			}
			if m.stacks[s.ID] != s {
				return
			}
			// Gone, or a site from before parts that took the whole stack (<id>-t): the street forgets it.
			if a.Why == "gone" || (a.Status == "settled" && a.Op == s.ID+"-t") {
				var by any
				if a.Status == "settled" {
					by = map[string]any{"id": id, "name": name}
				}
				m.remove(s)
				m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_gone", "id": s.ID, "by": by, "why": "picked"}, 0)
				return
			}
			if a.Status != "settled" && a.Why != "stale" {
				return
			}
			// The site's count is the truth: what is left after this part, or what a stale ask found.
			left := s.N
			if a.N != nil {
				left = *a.N
			} else if a.Status == "settled" {
				left = from - n
			}
			var by any
			if a.Status == "settled" {
				by = map[string]any{"id": id, "name": name}
				log.Printf("drops: %s %d of %d picked by %d (%s, %d left)", s.ID, n, from, id, a.Op, left)
			}
			if left <= 0 {
				m.remove(s)
				m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_gone", "id": s.ID, "by": by, "why": "picked"}, 0)
				return
			}
			s.N = left
			m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_left", "street": s.Street, "id": s.ID, "n": left, "by": by}, 0)
		})
	})
}

// take asks the site to put a stack in someone's ledger: a pickup, or a stack going home to its dropper.
func (m *drops) take(s *groundStack, id int64, name, why string) {
	s.picking = id
	if why == "pickup" {
		m.picking[id] = s.ID
	}
	site := m.site
	m.spawn(func() {
		a := site.dropTake(s.ID, id, why, 0, 0)
		m.h.After(0, func() {
			s.picking = 0
			if m.picking[id] == s.ID {
				delete(m.picking, id)
			}
			if a.Status == "" {
				a.Status, a.Why, a.Text = "refused", "off", pickTextOff
			}
			gone := a.Status == "settled" || a.Why == "gone"
			if why == "pickup" {
				out := map[string]any{"t": "ground_taken", "op": a.Op, "id": s.ID, "status": a.Status, "why": a.Why, "text": a.Text, "item": nil}
				if a.Status == "settled" {
					it := m.takenItem(s, a.Item)
					out["item"] = it
				} else if a.Text == "" {
					out["text"] = pickTextGone
				}
				if c := m.h.ClientLocked(id); c != nil {
					m.h.SendLocked(c, out)
				}
			}
			if !gone || m.stacks[s.ID] != s {
				return
			}
			m.remove(s)
			var by any
			kind := "picked"
			if a.Status == "settled" {
				by = map[string]any{"id": id, "name": name}
				if why == "return" {
					kind = "returned"
				}
				log.Printf("drops: %s %s to %d (%s)", s.ID, kind, id, why)
			}
			m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_gone", "id": s.ID, "by": by, "why": kind}, 0)
			if a.Status == "settled" && why == "return" {
				if c := m.h.ClientLocked(id); c != nil {
					it := m.takenItem(s, a.Item)
					m.h.SendLocked(c, map[string]any{"t": "ground_back", "op": a.Op, "id": s.ID, "item": it})
				}
			}
		})
	})
}

// takenItem is what a take gave: the site's word on it (its own stored wear and words), else the
// stack as held here; words a site from before did not send are the stack's own.
func (m *drops) takenItem(s *groundStack, site *tradeItem) tradeItem {
	it := s.item()
	if site != nil {
		it = *site
		if len(it.T) == 0 && it.C == s.C {
			it.T = s.T
		}
	}
	m.table.cleanTag(&it, m.h.Now())
	return it
}

// sweepSoon starts the hourly-ish look for stacks left too long, while any lie on the ground.
func (m *drops) sweepSoon() {
	if m.sweeping || len(m.stacks) == 0 {
		return
	}
	m.sweeping = true
	m.h.After(dropSweepEvery, m.sweep)
}

// sweep sends home every stack nobody picked up for dropKeep (ours: the source kept them forever).
func (m *drops) sweep() {
	m.sweeping = false
	now := m.h.Now()
	old := []*groundStack{}
	for _, s := range m.stacks {
		// An owner's own stack on their own home street stays (the header, "A home street").
		if s.picking == 0 && now.Sub(s.At) >= dropKeep && dropHomeOwner(s.Street) != s.By {
			old = append(old, s)
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].ID < old[j].ID })
	for _, s := range old {
		m.take(s, s.By, s.ByName, "return")
	}
	m.sweepSoon()
}
