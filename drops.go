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
// Here: on a public street (no "_" in its id), on a player's home street
// (HOME_<owner>, below), in their house and on their Tower's floors (HOUSE_<owner>,
// TOWER_<owner>_<floor>, below); party spaces, races and arenas are private places,
// where each player's drops stay their own as in solo. Nothing moves on the client's word. A drop is
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
// its own, as in solo); on a street stacks do not merge (each drop is its own
// stack with its own dropper; a house or Tower floor merges, "Merging" below); a
// street holds at most dropStreetMax shared stacks (a house or a Tower
// floor any number, as Glitch: G1224); a
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
//
// The house, its back yard and the Tower's floors (G1190, Austin 2026-11-23: "make house floors
// shared as in Glitch", reversing his 2026-11-10 ruling that kept them private): in the original
// the house (with the back yard on its left, the one "house-right" location) and the Tower were
// locations like the street, so takeable_drop and takeable_pickup ran there too. The one home
// rule is house_lock (takeable.js 33-36, 179-198; main.js doModifyItem 4870-4891): a stack the
// owner sets down on a furniture plat (a shelf, a table: a plat whose source is a piece) in their
// own house or Tower is locked to the owner (pols_is_owner, not key-holders) for good; a stack on
// the floor is anyone's once its dropper has walked 100 px away, key-holder, let-in visitor or
// owner alike (acl keys only open the door and the cabinets, inc_acl_keys.js). So HOUSE_<owner>
// (site HOUSE-<owner>) and TOWER_<owner>_<floor> (site TOWER-<owner>-<floor>) are shared as the
// home street is, for whoever homes.go lets in. Ours there: what the owner sets on furniture never
// comes here (the owner's game keeps it in the save, as house_lock kept it the owner's; visitors
// see it through the site's visit, Neighbours.php stackRows); in the house a stack's x is kept
// from the house's left end, as on the street (the back yard grows to the left and moves the
// house); a Tower floor's x is its own (the Tower does not move); the owner's own stacks there
// stay past dropKeep, as on the home street.
//
// Merging (house and Tower floors only, Austin 2026-11-24 for #655: "the house floor is shared, as
// Glitch"): takeable_drop put a stack in with apiPutItemIntoPosition, which merged it into a like stack
// there below its stackmax (takeable.js 202-207; Eleven's rebuild, Location.addItem 475-503: within
// 100 px, while count < stackmax, the rest its own stack), and the Decoration Mode move (main.js
// doModifyItem 4852-4890) merged too, unless the client sent no_merge (HandOfDecorator.as shouldMerge
// 1656-1670: only when set directly on a stack). So in a house or on a Tower floor a drop names the like
// stack it lands on (the site joins as many as it takes in the drop's own transaction, LedgerDrop
// joins/grow), or none to join the nearest within dropMergeRadius, or "merge": false; and the owner's
// ground_move moves a stack along their floor, staying shared, onto a like stack too:
//
//	{"t":"ground_drop",…,"into":"drop-…"|"merge":false}
//	{"t":"ground_move","id":"drop-…","x":900,"y":620,"ox":640,"into"?:"drop-…"}
//	{"t":"ground_moved","id":"drop-…","status":"settled"|"refused","why":"…","text":"…","into"?:"drop-…"}
//	ground_dropped of a merged drop: "stack": null (the rest, if any, comes as ground_added), "item": what
//	left the pack, "into": the stack it joined; the stack it joined comes again as ground_added, its new count.
//
// Ours: a stack takes only from its own dropper (Austin 2026-11-24: each keeps its dropper for the return
// after dropKeep, so nobody's goods change hands or go home to someone else); not
// on public or home streets (two players' stacks: Austin's call); stacks with wear or words never merge.
// A pile on a house or Tower floor has no stack limit for any class (Austin 2026-11-24, pileMax: "Let's
// remove the pile limits"; Glitch's stackmax capped every pile, emblems 1, moonstones 3): it grows to the
// most a stack holds anywhere (dropPileMax, 9999), the rest lying beside it, and a pile may be dropped
// whole there. Never a class whose copies keep their own wear or words. The site is told the pile ("max")
// and applies the same rule from its own table. The pack keeps Glitch's stackmax.
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
	"strings"
	"testing"
	"time"
)

const (
	ledgerRouteDrop     = "/groddle/v1/ledger/drop"
	ledgerRouteDropTake = "/groddle/v1/ledger/drop/take"
	ledgerRouteDrops    = "/groddle/v1/ledger/drops"
	ledgerRouteDropEat  = "/groddle/v1/ledger/drop/eat"
	ledgerRouteDropPut  = "/groddle/v1/ledger/drop/place"
	ledgerRouteDropMove = "/groddle/v1/ledger/drop/move"

	dropLockRadius = 100.0 // takeable.js:38 distanceFromPlayer(dropper) < 100, measured across (dropLockHeight)
	dropLockHeight = 200.0 // ours: the game draws a stack 74 px above the feet (game.js dropPackItem), the source 20
	dropReach      = 800.0 // config_prod.js:37 verb_radius (main.js:2970)
	dropPlaceNear  = 300.0 // ours: a stack lands within this of the dropper (main.js:2944 required_range)
	dropLift       = 20.0  // takeable.js:167 pc.y-20
	dropStreetMax  = 60    // ours
	// A drop on a house or Tower floor joins a like stack this near (Eleven Location.addItem 475-503:
	// dist² < 10000, the rebuild of apiPutItemIntoPosition's default merge; takeable.js 202-207).
	dropMergeRadius = 100.0
	// G1224 (Austin 2026-11-24, "no limit on house and Tower floors, as Glitch": GSJS has no item
	// limit for a location): a house (with its back yard) or a Tower floor holds any number.
	// Measured at 300 stacks a house across 58 houses: 5.4 MB in all (309 B a stack), a 45 KB
	// ground_state on joining, under 1 ms to build.
	// dropRefreshEvery: how often joining a house or Tower floor asks the site for stacks it holds
	// there that this server has not heard of (a save's old floor drops moved to the shared floor
	// on the owner's load, the site's HouseFloors).
	dropRefreshEvery = time.Minute
	dropKeep         = 7 * 24 * time.Hour
	dropSweepEvery   = 10 * time.Minute
	dropLoadRetry    = 15 * time.Second
	dropBurst        = 6
	dropEvery        = time.Second

	dropTextSoulbound = "This item is locked to you and can't be dropped."                                 // takeable.js:118
	dropTextCant      = "That can't be dropped for others to find."                                        // ours: the ledger does not carry it
	dropTextPrivate   = "Only a drop on a public street or at someone's home is shared."                   // ours
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
	moveTextPlace     = "Only the owner can move things here."                                             // main.js:4853 "You are not at home"
)

var moveOpRe = regexp.MustCompile(`^move-[0-9a-f]{16}$`)

var dropOpRe = regexp.MustCompile(`^drop-[0-9a-f]{16}$`)

// A home place as this server names it and as the site keeps it (the header, "A home street" and
// "The house"): the site's street names take letters, digits and "-" only.
var (
	dropHomeSiteRe  = regexp.MustCompile(`^(HOME|HOUSE)-([1-9][0-9]{0,17})$`)
	dropTowerSiteRe = regexp.MustCompile(`^TOWER-([1-9][0-9]{0,17})-([0-9])$`)
)

// dropHomeOwner is whose home this place is (their home street, house or a Tower floor), or 0 for
// any other place.
func dropHomeOwner(street string) int64 {
	if id, _ := homeOwner(street); id > 0 {
		return id
	}
	return towerOwner(street)
}

// dropFull is whether a place holds all the shared stacks it may: a house or a Tower floor never
// fills (G1224), any other street (a home street too) at dropStreetMax.
func (m *drops) dropFull(street string) bool {
	if dropIndoors(street) {
		return false
	}
	return len(m.byStreet[street]) >= dropStreetMax
}

// dropIndoors is whether a place is someone's house (with its back yard) or a floor of their Tower.
func dropIndoors(street string) bool {
	if id, house := homeOwner(street); id > 0 && house {
		return true
	}
	return towerOwner(street) > 0
}

// dropSiteStreet is a street as the site is told it.
func dropSiteStreet(street string) string {
	if dropHomeOwner(street) > 0 {
		return strings.ReplaceAll(street, "_", "-")
	}
	return street
}

// dropStreetFromSite is a street the site names, as players join it.
func dropStreetFromSite(street string) string {
	if dropHomeSiteRe.MatchString(street) || dropTowerSiteRe.MatchString(street) {
		return strings.ReplaceAll(street, "-", "_")
	}
	return street
}

// dropOrigin is the x a game says its positions on this street start from: the house's x on a
// home street and in the house, nothing anywhere else (a Tower floor does not move).
func dropOrigin(street string, ox *float64) float64 {
	if id, _ := homeOwner(street); ox == nil || id == 0 || math.IsNaN(*ox) || math.Abs(*ox) > 1e6 {
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
	Into   string    `json:"into,omitempty"` // a like stack on a house or Tower floor it joins (below its pile)
	Max    int       `json:"max,omitempty"`  // the class's pile there (pileMax), with Into
}

// moveCall is the owner's floor-to-floor move as the site is asked it (LedgerDrop::moveStack).
type moveCall struct {
	Op     string `json:"op"`
	ID     int64  `json:"id"`
	Drop   string `json:"drop"`
	Street string `json:"street"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Into   string `json:"into,omitempty"`
	Max    int    `json:"max,omitempty"`
}

// dropAnswer is the site's word on a drop or a take. Status "" means no answer came.
type dropAnswer struct {
	Status string     `json:"status"` // settled | refused
	Why    string     `json:"why,omitempty"`
	Text   string     `json:"text,omitempty"`
	Op     string     `json:"op,omitempty"`   // a take: its own op
	By     int64      `json:"by,omitempty"`   // a take refused as gone: who has it
	Item   *tradeItem `json:"item,omitempty"` // a take: what it gave
	N      *int       `json:"n,omitempty"`    // a bite or a part taken (or refused as stale): how many are left; a merged drop or a move: what lies on its own
	Into   string     `json:"into,omitempty"` // a drop or a move that joined a like stack: which
	Joined int        `json:"joined,omitempty"`
	In     *int       `json:"in,omitempty"` // that stack's count now
	X      *int       `json:"x,omitempty"`  // a move: where the stack lies now
	Y      *int       `json:"y,omitempty"`
}

// dropSite is the site's ledger (httpLedger). Calls block; the module makes them off the hub lock.
type dropSite interface {
	drop(c dropCall) dropAnswer
	dropTake(op string, id int64, why string, n, from int) dropAnswer // n > 0: n of the from held (a part)
	dropsOnGround() ([]dropRow, error)
	dropsOnStreet(street string) ([]dropRow, error) // G1224: one place's stacks (the site's name for it)
	dropEat(op string, from int) dropAnswer
	dropPlace(c dropCall) dropAnswer // G605: a stack the street makes (a Plop, a Piglet), under the player whose act made it
	dropMove(c moveCall) dropAnswer  // the owner's floor-to-floor move in their house or Tower
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
			l.pause(time.Duration(try) * ledgerPauseStep)
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

func (l *httpLedger) dropMove(c moveCall) dropAnswer {
	body, _ := json.Marshal(c)
	return l.dropAsk(ledgerRouteDropMove, body)
}

func (l *httpLedger) dropsOnStreet(street string) ([]dropRow, error) {
	var out struct {
		Stacks []dropRow `json:"stacks"`
	}
	body, _ := json.Marshal(map[string]string{"street": street})
	if err := l.dropPost(ledgerRouteDrops, body, &out); err != nil {
		return nil, err
	}
	return out.Stacks, nil
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
	// G1224: when each house or Tower floor last asked the site for stacks this server has not heard of.
	refreshed map[string]time.Time
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
	r.Handle("ground_move", m.moveFrame)
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
	if m.site == nil || !m.loaded || followPrivate(street) || !ok || it.N < 1 || it.N > cls.Max || m.dropFull(street) {
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
// house or Tower floor, where drops are everyone's.
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
	street := cl.Street()
	// The owner coming into their own house or Tower floor always asks: the site moves a save's old floor
	// drops on the owner's load (G1224), and they must show at once, not after the minute.
	m.refresh(street, dropHomeOwner(street) == cl.ID() || towerOwner(street) == cl.ID())
}

// refresh asks the site, at most once a dropRefreshEvery, for the stacks lying in a house or on a
// Tower floor that this server has not heard of, and shows the street any it finds (ground_added).
// A save's old floor drops are moved to the shared floor on the owner's load (G1224, the site's
// HouseFloors), after this server loaded the ground. Only new ids are added: a stack held here
// already, or one taken or eaten (no longer on the ground at the site), is left as it is.
func (m *drops) refresh(street string, owner bool) {
	if !m.loaded || !dropIndoors(street) {
		return
	}
	now := m.h.Now()
	if at, ok := m.refreshed[street]; ok && !owner && now.Sub(at) < dropRefreshEvery {
		return
	}
	if m.refreshed == nil {
		m.refreshed = map[string]time.Time{}
	}
	m.refreshed[street] = now
	site := m.site
	m.spawn(func() {
		rows, err := site.dropsOnStreet(dropSiteStreet(street))
		m.h.After(0, func() {
			if err != nil {
				log.Printf("drops: refreshing %s: %v", street, err)
				delete(m.refreshed, street)
				return
			}
			added := 0
			for _, row := range rows {
				if dropStreetFromSite(row.Street) != street || m.stacks[row.ID] != nil {
					continue
				}
				if s := m.addRow(row); s != nil {
					m.h.BroadcastStreetLocked(street, map[string]any{"t": "ground_added", "stack": s.view()}, 0)
					added++
				}
			}
			if added > 0 {
				log.Printf("drops: %d stacks found in %s", added, street)
			}
		})
	})
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
		Ref   string     `json:"ref"`
		Item  *tradeItem `json:"item"`
		X     *float64   `json:"x"`
		Y     *float64   `json:"y"`
		Ox    *float64   `json:"ox"`
		Into  string     `json:"into"`  // the like stack it was set on (Decoration Mode); none: the nearest within dropMergeRadius
		Merge *bool      `json:"merge"` // false: on its own (HandOfDecorator.as shouldMerge 1656-1670 sent no_merge off a stack)
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
	case it.N < 1 || it.N > dropMaxN(street, cls) || it.N > tradeSlotMax:
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
	if m.dropFull(street) {
		m.refuseDrop(cl, ref, "full", dropTextFull)
		return
	}
	// Where it lands: where the client put it, near the dropper; else at their feet (takeable.js 166-172).
	// The owner in their own house or Tower sets it anywhere there (Decoration Mode: takeable_drop takes
	// msg.drop_x and drop_y as they come, takeable.js 166-172).
	px, py := cl.Pos()
	x, y := px, py-dropLift
	anywhere := dropIndoors(street) && dropHomeOwner(street) == cl.ID()
	if msg.X != nil && msg.Y != nil && !math.IsNaN(*msg.X) && !math.IsNaN(*msg.Y) && math.Abs(*msg.X) <= 1e6 && math.Abs(*msg.Y) <= 1e6 && (anywhere || math.Hypot(*msg.X-px, *msg.Y-py) <= dropPlaceNear) {
		x, y = *msg.X, *msg.Y
	}
	// On a home street the stack is kept from the house (the header, "A home street").
	x -= dropOrigin(street, msg.Ox)
	call := dropCall{Op: "drop-" + m.newID(), ID: cl.ID(), Street: dropSiteStreet(street), X: int(math.Round(x)), Y: int(math.Round(y)), Item: it}
	from, name := cl.ID(), cl.Name()
	// On a house or Tower floor it joins a like stack there (the header, "Merging").
	into := m.mergeTarget(street, from, it, float64(call.X), float64(call.Y), msg.Into, msg.Merge == nil || *msg.Merge)
	if into != nil {
		call.Into, call.Max = into.ID, pileMax(cls)
		into.picking = from // nobody takes from it, or eats it, while the site weighs the merge
	}
	m.dropping[from] = call.Op
	site := m.site
	m.spawn(func() {
		a := site.drop(call)
		m.h.After(0, func() {
			if m.dropping[from] == call.Op {
				delete(m.dropping, from)
			}
			if into != nil && into.picking == from {
				into.picking = 0
			}
			if a.Status == "settled" && into != nil && a.Into == into.ID && a.Joined > 0 {
				m.dropMerged(call, ref, a, into, name)
				return
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

// dropPileMax is the most one pile holds on a house or Tower floor: the count a stack may hold anywhere
// (tradeSlotMax, MakingSlot.as:25 MAX_COUNT; the site's ledger takes no more).
const dropPileMax = tradeSlotMax

// pileMax is how many of a class one pile on a house or Tower floor holds (floors, shelves and tables):
// dropPileMax for every class, except one whose copies keep their own identity, which never joins (1):
// a tool (pts, its wear) or a class whose copies carry words (tag; the game's keepsOwnStack classes, a
// Still and a Teleportation Script, are tagged). A drop that would pass it joins up to it, the rest lying
// beside it.
// ours: Austin 2026-11-24, "Let's remove the pile limits": piles at home have no limit for any class;
// Glitch's stackmax capped every pile (Eleven Location.addItem 475-503: emblems 1, moonstones 3). The pack
// keeps Glitch's stackmax. The game (js/world-items.js pileMaxOf) and the site (LedgerDrop::pileCap) apply
// the same rule.
func pileMax(cls tradeClass) int {
	if cls.Pts > 0 || cls.Tag != "" {
		return 1
	}
	return dropPileMax
}

// dropMaxN is the most one drop of a class may hold on a street: its stackmax (the trade table's max),
// or in a house or on a Tower floor its pile (pileMax), so a pile set down whole stays one.
func dropMaxN(street string, cls tradeClass) int {
	if dropIndoors(street) {
		return max(pileMax(cls), cls.Max)
	}
	return cls.Max
}

// mergeTarget is the like stack a drop of it at (x, y) by `by` joins, or nil: only on a house or Tower
// floor; `named` when the client set it on one (it must still be like it), else the nearest within
// dropMergeRadius when `near`. A like stack: the same class, no wear or words on either, below the class's
// pile (pileMax), nobody taking from it or eating it, and dropped by `by` (Austin 2026-11-24: only one player's
// own stacks join, so each keeps its dropper's return after dropKeep and nobody's goods change hands).
func (m *drops) mergeTarget(street string, by int64, it tradeItem, x, y float64, named string, near bool) *groundStack {
	if !dropIndoors(street) || it.R != nil || len(it.T) > 0 {
		return nil
	}
	cls, ok := m.table.Items[it.C]
	limit := pileMax(cls)
	if !ok || limit < 2 {
		return nil
	}
	like := func(s *groundStack) bool {
		return s != nil && s.Street == street && s.C == it.C && s.R == nil && len(s.T) == 0 && s.picking == 0 && !s.eating &&
			s.N < limit && s.By == by
	}
	if named != "" {
		if s := m.stacks[named]; like(s) {
			return s
		}
		return nil
	}
	if !near {
		return nil
	}
	var best *groundStack
	bestD := 0.0
	for _, s := range m.byStreet[street] {
		if !like(s) {
			continue
		}
		d := math.Hypot(s.X-x, s.Y-y)
		if d >= dropMergeRadius {
			continue
		}
		if best == nil || d < bestD || (d == bestD && s.ID < best.ID) {
			best, bestD = s, d
		}
	}
	return best
}

// dropMerged tells the dropper and the place a drop that joined the like stack `into` (all or part of it,
// the rest lying on its own as its own stack). The dropper's message carries what left the pack ("item")
// and no stack of its own, so a game from before merges applies its own count.
func (m *drops) dropMerged(call dropCall, ref string, a dropAnswer, into *groundStack, name string) {
	from := call.ID
	rest := call.Item.N - a.Joined
	if a.N != nil {
		rest = *a.N
	}
	if m.stacks[into.ID] == into && a.In != nil && *a.In > 0 {
		into.N = *a.In
		m.h.BroadcastStreetLocked(into.Street, map[string]any{"t": "ground_added", "stack": into.view()}, 0)
	}
	var s *groundStack
	if rest > 0 {
		street := dropStreetFromSite(call.Street)
		s = m.addRow(dropRow{ID: call.Op, By: from, Name: name, Street: street, X: float64(call.X), Y: float64(call.Y), C: call.Item.C, N: rest, R: call.Item.R, T: call.Item.T, At: m.h.Now().Unix()})
	}
	out := map[string]any{"t": "ground_dropped", "op": call.Op, "ref": ref, "status": "settled", "why": "", "text": "", "stack": nil,
		"item": call.Item, "into": into.ID}
	if c := m.h.ClientLocked(from); c != nil {
		m.h.SendLocked(c, out)
	}
	log.Printf("drops: %d dropped %d %s onto %s (%d joined, %d on its own)", from, call.Item.N, call.Item.C, into.ID, a.Joined, max(0, rest))
	if s != nil {
		m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_added", "stack": s.view()}, 0)
		m.sweepSoon()
	}
}

func (m *drops) refuseMove(cl *Client, id, why, text string) {
	m.h.SendLocked(cl, map[string]any{"t": "ground_moved", "id": id, "status": "refused", "why": why, "text": text})
}

// moveFrame is the owner's Decoration Mode move of a stack on their own house or Tower floor (main.js
// doModifyItem 4852-4890: at home only, any stack in the house, merged into a like stack it is set on).
// It stays on the floor for everyone, with its own dropper; the site moves it once per move op.
func (m *drops) moveFrame(cl *Client, frame json.RawMessage) {
	if !m.allow(cl) {
		return
	}
	var msg struct {
		ID   string   `json:"id"`
		X    *float64 `json:"x"`
		Y    *float64 `json:"y"`
		Ox   *float64 `json:"ox"`
		Into string   `json:"into"`
	}
	if json.Unmarshal(frame, &msg) != nil || !dropOpRe.MatchString(msg.ID) || msg.X == nil || msg.Y == nil || math.IsNaN(*msg.X) || math.IsNaN(*msg.Y) || math.Abs(*msg.X) > 1e6 || math.Abs(*msg.Y) > 1e6 {
		m.refuseMove(cl, msg.ID, "bad", pickTextGone)
		return
	}
	street := cl.Street()
	if !dropIndoors(street) || dropHomeOwner(street) != cl.ID() {
		m.refuseMove(cl, msg.ID, "place", moveTextPlace)
		return
	}
	s := m.stacks[msg.ID]
	if s == nil || s.Street != street {
		m.refuseMove(cl, msg.ID, "gone", pickTextGone)
		return
	}
	if m.site == nil {
		m.refuseMove(cl, s.ID, "off", pickTextOff)
		return
	}
	if s.picking != 0 || s.eating {
		m.refuseMove(cl, s.ID, "busy", pickTextBusy)
		return
	}
	x := math.Round(*msg.X - dropOrigin(street, msg.Ox))
	y := math.Round(*msg.Y)
	call := moveCall{Op: "move-" + m.newID(), ID: cl.ID(), Drop: s.ID, Street: dropSiteStreet(street), X: int(x), Y: int(y)}
	var into *groundStack
	if msg.Into != "" && msg.Into != s.ID {
		into = m.mergeTarget(street, s.By, s.item(), x, y, msg.Into, false)
		if into != nil {
			call.Into, call.Max = into.ID, pileMax(m.table.Items[s.C])
			into.picking = cl.ID()
		}
	}
	s.picking = cl.ID()
	site := m.site
	by := cl.ID()
	m.spawn(func() {
		a := site.dropMove(call)
		m.h.After(0, func() {
			if s.picking == by {
				s.picking = 0
			}
			if into != nil && into.picking == by {
				into.picking = 0
			}
			if a.Status == "" {
				a.Status, a.Why, a.Text = "refused", "off", pickTextOff
			}
			out := map[string]any{"t": "ground_moved", "id": s.ID, "status": a.Status, "why": a.Why, "text": a.Text}
			if a.Status != "settled" {
				if a.Text == "" {
					out["text"] = pickTextGone
					if a.Why == "place" {
						out["text"] = moveTextPlace
					}
				}
				if c := m.h.ClientLocked(by); c != nil {
					m.h.SendLocked(c, out)
				}
				return
			}
			if a.Into != "" && into != nil && a.Into == into.ID {
				out["into"] = into.ID
				if m.stacks[into.ID] == into && a.In != nil && *a.In > 0 {
					into.N = *a.In
					m.h.BroadcastStreetLocked(into.Street, map[string]any{"t": "ground_added", "stack": into.view()}, 0)
				}
			}
			if c := m.h.ClientLocked(by); c != nil {
				m.h.SendLocked(c, out)
			}
			if m.stacks[s.ID] != s {
				return
			}
			left := s.N
			if a.N != nil {
				left = *a.N
			}
			if left <= 0 {
				m.remove(s)
				m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_gone", "id": s.ID, "by": nil, "why": "merged"}, 0)
				return
			}
			s.N = left
			if a.X != nil && a.Y != nil {
				s.X, s.Y = float64(*a.X), float64(*a.Y)
			} else {
				s.X, s.Y = x, y
			}
			log.Printf("drops: %d moved %s on %s to %.0f,%.0f", by, s.ID, s.Street, s.X, s.Y)
			m.h.BroadcastStreetLocked(s.Street, map[string]any{"t": "ground_added", "stack": s.view()}, 0)
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
		// An owner's own stack at their own home stays (the header, "A home street" and "The house").
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
